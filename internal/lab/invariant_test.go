package lab

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scenario"
)

func TestDefaultInvariantRegistryPassesTheCanonicalExecution(t *testing.T) {
	execution := openDemoExecution(t)
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.Drive(context.Background(), Quiesce()); err != nil {
		t.Fatalf("drive arrivals: %v", err)
	}
	if _, err := execution.Drive(context.Background(), Advance(time.Hour)); err != nil {
		t.Fatalf("advance execution: %v", err)
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("check invariants: %v", err)
	}

	latest := latestInvariantResults(execution.invariants)
	if len(latest) != 21 {
		t.Fatalf("latest invariant results = %d, want 21", len(latest))
	}
	for _, result := range latest {
		if result.Status != InvariantPassed {
			t.Fatalf("invariant did not pass: %+v", result)
		}
	}
}

func TestInvariantRegistryReportsAReplayableDuplicateExecutionViolation(t *testing.T) {
	world, arrival := openWorldFixture(t, "producer")
	prepareRun(t, world, "run-producer", arrival)
	request := worldLaunchRequest(arrival)
	if _, err := world.Launch(context.Background(), request); !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("launch: %v", err)
	}
	world.mu.Lock()
	duplicate := world.executions[request.LaunchKey]
	duplicate.LaunchKey = "launch-producer-2"
	duplicate.ExternalID = "lab-attempt-producer-2"
	world.executions[duplicate.LaunchKey] = duplicate
	world.mu.Unlock()

	results := DefaultInvariantRegistry().Evaluate(InvariantObservation{
		StartedAt:                   world.nowTime(),
		Now:                         world.nowTime(),
		World:                       world.truthSnapshot(),
		RunRequirements:             map[string]RunArrival{"run-producer": arrival},
		ArtifactCatalog:             world.invariantFacts().ArtifactCatalog,
		ProjectionRebuildEquivalent: true,
	})

	result := invariantResultByID(t, results, "safety.no_duplicate_active_execution")
	if result.Status != InvariantFailed {
		t.Fatalf("duplicate execution invariant = %+v", result)
	}
}

func TestExecutionCertifiesTheStateEveryDriveReaches(t *testing.T) {
	var observed []time.Time
	registry, err := NewInvariantRegistry(invariantRule{
		id: "test.observation_clock",
		check: func(observation InvariantObservation) error {
			observed = append(observed, observation.Now)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	blueprint, tape, samples := demoInputs(t)
	execution, err := Open(context.Background(), Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Invariants:       registry,
		Limits:           testLimits(),
		Policy:           "policy:test",
		MercatorRevision: "revision:test",
	})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.Drive(context.Background(), Quiesce()); err != nil {
		t.Fatalf("drive arrivals: %v", err)
	}
	checkpoint, err := execution.Drive(context.Background(), Advance(10*time.Minute))
	if err != nil {
		t.Fatalf("advance the control plane: %v", err)
	}

	if len(observed) <= len(tape.Events) {
		t.Fatalf("invariant checks = %d, want more than the %d World Tape transitions", len(observed), len(tape.Events))
	}
	terminal := observed[len(observed)-1]
	if !terminal.Equal(checkpoint.Now) {
		t.Fatalf(
			"last invariant observation at %s, want the terminal virtual time %s",
			terminal.Format(time.RFC3339Nano),
			checkpoint.Now.Format(time.RFC3339Nano),
		)
	}
}

func TestEveryDefaultInvariantHasADeliberatelyFailingCase(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	cases := map[string]func(*InvariantObservation){
		"safety.no_duplicate_active_execution": func(observation *InvariantObservation) {
			observation.World.ActiveExecutions = []externalExecution{
				{RunID: "run-1", LaunchKey: "launch-1"},
				{RunID: "run-1", LaunchKey: "launch-2"},
			}
		},
		"safety.exclusive_booking_capacity": func(observation *InvariantObservation) {
			observation.RentalSchedules["rental-1"] = domain.RentalSchedule{
				RentalID: "rental-1",
				Version:  1,
				Bookings: []domain.ScheduledBooking{
					{Booking: domain.Booking{ID: "booking-1", State: domain.BookingStateRunning, ScheduleVersion: 1}},
					{Booking: domain.Booking{ID: "booking-2", State: domain.BookingStateRunning, ScheduleVersion: 1}},
				},
			}
		},
		"safety.monotonic_terminal_state": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{
				{Subject: "runs/run-1", Type: "compute.run.closed.v1"},
				{Subject: "runs/run-1", Type: "compute.run.requested.v1"},
			}
		},
		"safety.idempotent_external_commands": func(observation *InvariantObservation) {
			observation.Effects = []EffectRecord{
				{Operation: OperationProviderLaunch, OperationID: "launch-1", Command: EffectCommandAccepted, Consequence: []byte(`{"external_id":"one"}`)},
				{Operation: OperationProviderLaunch, OperationID: "launch-1", Command: EffectCommandAccepted, Consequence: []byte(`{"external_id":"two"}`)},
			}
		},
		"safety.lease_fencing": func(observation *InvariantObservation) {
			observation.World.ActiveExecutions = []externalExecution{{LaunchKey: "launch-1"}}
		},
		"safety.artifact_dependencies": func(observation *InvariantObservation) {
			observation.Effects = []EffectRecord{{
				Sequence:      1,
				Operation:     OperationProviderLaunch,
				Command:       EffectCommandAccepted,
				CorrelationID: "run-1",
			}}
			observation.Workloads["run-1"] = domain.WorkloadRevision{
				ID: "wrev_run-1",
				Spec: domain.WorkloadSpec{
					Artifacts: domain.ArtifactRequirements{Consumes: []string{"artifact-1"}},
				},
			}
		},
		// A copy of content nothing has published: the object store is what makes
		// an Artifact exist, so bytes on a machine are not an Artifact.
		"safety.artifact_replica_verified": func(observation *InvariantObservation) {
			observation.ArtifactCatalog["artifact-1"] = domain.ArtifactVersion{
				ID:            "artifact-1",
				ContentDigest: "sha256:aaaa",
			}
			observation.World.ArtifactReplicas = []ArtifactReplica{{
				OfferID: "rental-warm",
				ArtifactReplica: domain.ArtifactReplica{
					ArtifactID:    "artifact-1",
					ContentDigest: "sha256:aaaa",
					SizeBytes:     1,
					State:         domain.ArtifactReplicaVerified,
				},
			}}
		},
		"safety.monotonic_versions": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{
				{Subject: "runs/run-1", StreamVersion: 2, GlobalPosition: 1},
			}
		},
		"safety.owned_external_resources": func(observation *InvariantObservation) {
			observation.World.ActiveExecutions = []externalExecution{{RunID: "run-1", LaunchKey: "launch-1"}}
		},
		// A machine that has promised room it does not have. What is already on
		// this disk and what is still landing on it come to more than the disk
		// holds, which is exactly the state a reservation exists to prevent and
		// exactly what the rule this replaced could not see: it read Artifact
		// copies and Cache Mounts for well-formedness and never compared a byte
		// against a machine's capacity.
		"safety.disk_reservation_respected": func(observation *InvariantObservation) {
			observation.World.Disk = []DiskLedger{{
				OfferID:       "rental-cramped",
				CapacityBytes: 60 << 30,
				Resident:      []ResidentContent{{Kind: ResidentLayer, Name: "sha256:base", SizeBytes: 40 << 30}},
				ReservedBytes: 40 << 30,
			}}
		},
		// Two tenants attached to one cache. Each attachment names the identity it
		// landed in, and that identity has to be the one its own workspace, name,
		// and key produce, so a world that keyed this cache by the name alone is
		// caught handing both tenants the same bytes.
		"safety.cache_mount_workspace_isolation": func(observation *InvariantObservation) {
			observation.Effects = []EffectRecord{
				cacheMountAccessedUnderSharedIdentity(1, "ws_lab_alpha"),
				cacheMountAccessedUnderSharedIdentity(2, "ws_lab_beta"),
			}
		},
		"safety.projection_rebuild_equivalence": func(observation *InvariantObservation) {
			observation.ProjectionRebuildEquivalent = false
		},
		"safety.secrets_absent": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{{Data: []byte(`{"password":"exposed"}`)}}
		},
		"safety.ephemeral_capacity_not_reused": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{queuedBehindOneShotCapacity()}
		},
		"safety.locality_provenance": func(observation *InvariantObservation) {
			observation.World.Offers = []domain.OfferSnapshot{{
				ID:   "rental-warm",
				Kind: domain.OfferKindStanding,
				Lane: domain.LaneReusable,
				Images: domain.ImageInventory{
					Known:        true,
					LayerDigests: []string{"sha256:never-pulled"},
				},
			}}
		},
		"safety.locality_is_never_infeasibility": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{refusedForHoldingNothing()}
		},
		"liveness.lost_response_reconciliation": func(observation *InvariantObservation) {
			observation.Effects = []EffectRecord{{CorrelationID: "run-missing", Response: EffectResponseLost}}
		},
		"liveness.stale_lease_expiry": func(observation *InvariantObservation) {
			observation.World.ActiveExecutions = []externalExecution{
				{LaunchKey: "launch-1", CompletesAt: now.Add(-6 * time.Minute)},
			}
		},
		"liveness.orphan_convergence": func(observation *InvariantObservation) {
			observation.World.ActiveExecutions = []externalExecution{{RunID: "run-missing", LaunchKey: "launch-1"}}
		},
		"liveness.superseded_booking_release": func(observation *InvariantObservation) {
			observation.RentalSchedules["rental-1"] = domain.RentalSchedule{
				RentalID: "rental-1",
				Version:  1,
				Bookings: []domain.ScheduledBooking{{
					Booking: domain.Booking{
						ID:              "booking-1",
						RunID:           "run-missing",
						State:           domain.BookingStateRunning,
						ScheduleVersion: 1,
					},
				}},
			}
		},
		"liveness.admitted_run_progress": func(observation *InvariantObservation) {
			observation.Now = now.Add(25 * time.Hour)
			observation.Runs = []domain.RunRecord{{ID: "run-1", Phase: "running"}}
			observation.RunRequirements["run-1"] = RunArrival{Name: "run-1"}
		},
	}

	for id, makeFailure := range cases {
		t.Run(id, func(t *testing.T) {
			observation := InvariantObservation{
				StartedAt:                   now,
				Now:                         now,
				World:                       WorldTruthSnapshot{At: now},
				Workloads:                   map[string]domain.WorkloadRevision{},
				RentalSchedules:             map[string]domain.RentalSchedule{},
				RunRequirements:             map[string]RunArrival{},
				ArtifactCatalog:             map[string]domain.ArtifactVersion{},
				SeededLocality:              map[string]map[string]bool{},
				ProjectionRebuildEquivalent: true,
			}
			makeFailure(&observation)
			result := invariantResultByID(t, DefaultInvariantRegistry().Evaluate(observation), id)
			if result.Status != InvariantFailed || result.Violation == "" {
				t.Fatalf("deliberate failure did not fail: %+v", result)
			}
		})
	}

	if len(cases) != len(DefaultInvariantRegistry().invariants) {
		t.Fatalf("deliberate cases = %d, default invariants = %d", len(cases), len(DefaultInvariantRegistry().invariants))
	}
}

// cacheMountAccessedUnderSharedIdentity is one workload attached to a cache
// filed under an identity that carries no workspace, which is what a world keyed
// by name alone produces. It is the shape a cross-workspace leak actually has:
// each attachment names the tenant it happened under, and the identity they
// landed in is the same one.
func cacheMountAccessedUnderSharedIdentity(sequence uint64, workspaceID string) EffectRecord {
	request, err := json.Marshal(map[string]any{
		"identity":          "compiler-cache",
		"workspace_id":      workspaceID,
		"name":              "compiler-cache",
		"compatibility_key": "cuda-12.4",
		"offer_id":          "shared-builder",
	})
	if err != nil {
		panic(err)
	}
	return EffectRecord{
		Sequence:  sequence,
		Operation: OperationCacheMountAttach,
		Command:   EffectCommandAccepted,
		Response:  EffectResponseDelivered,
		Request:   request,
	}
}

// queuedBehindOneShotCapacity is the decision the lane split forbids: a Run
// parked in a queue behind capacity that will not exist once its workload exits.
func queuedBehindOneShotCapacity() eventlog.CloudEvent {
	return bookingDecidedEvent("evt_ephemeral_queue", domain.BookingDecision{
		ID:                      "dec_ephemeral_queue",
		RunID:                   "run-queued",
		SelectedOfferSnapshotID: "off_oneshot",
		Candidates: []domain.CandidateDecision{{
			OfferSnapshotID: "off_oneshot",
			Disposition:     domain.CandidateDispositionEphemeral,
			Feasible:        true,
		}},
		Booking: &domain.Booking{
			ID:       "bkg_queued",
			RunID:    "run-queued",
			RentalID: "rnt_oneshot",
			State:    domain.BookingStateQueued,
		},
	})
}

// TestSilenceIsPricedAndAMeasurementBinds is the second clause of
// safety.locality_is_never_infeasibility, which the registry's own failing case
// cannot reach: a candidate struck out on a start bound. Every row is a refusal,
// and what separates them is what the rest of the record says about the seconds
// it rested on.
//
// The first two are the two ways silence becomes infeasibility, and the rule has
// to see both. A refusal against a bound the established prediction meets is a
// scheduler contradicting its own record. A refusal whose established prediction
// quietly includes content nobody could describe agrees with its record
// perfectly, so it is caught by recomputing what was discounted from the
// localities and the per-kind seconds recorded beside it. That second shape is
// what a scheduler counting a silence as established produces, and it is what the
// mutation cases in warming_test.go drive through a real execution.
//
// The last two are lawful. A measured start latency for this offer is a
// measurement whatever anyone could enumerate. And a machine already deep in its
// own stated queue is late whatever it could say about its disk, so one
// unreadable input must not buy it an exemption from the bound: the silence was
// charged and taken back out, and what remains is over the bound on its own.
func TestSilenceIsPricedAndAMeasurementBinds(t *testing.T) {
	unreadableDataset := []domain.ArtifactEvidence{{
		ArtifactID: "artifact:imagenet:v2.41",
		Locality:   domain.LocalityUnknown,
		FetchBytes: 40_000_000_000,
	}}
	for _, refusal := range []struct {
		name      string
		candidate domain.CandidateDecision
		lawful    bool
	}{
		{
			name: "a bound the established prediction meets",
			candidate: domain.CandidateDecision{
				ImageLocality:    domain.LocalityUnknown,
				ArtifactEvidence: unreadableDataset,
				Estimates: domain.CandidateEstimates{
					PullSeconds:             domain.Estimate{Expected: 289},
					ArtifactSeconds:         domain.Estimate{Expected: 640},
					StartSeconds:            domain.Estimate{Expected: 930, P90: 1394},
					EstablishedStartSeconds: domain.Estimate{Expected: 1, P90: 1.25},
				},
			},
		},
		{
			name: "a silence counted as established",
			candidate: domain.CandidateDecision{
				ImageLocality:    domain.LocalityUnknown,
				ArtifactEvidence: unreadableDataset,
				Estimates: domain.CandidateEstimates{
					PullSeconds:             domain.Estimate{Expected: 289},
					ArtifactSeconds:         domain.Estimate{Expected: 640},
					StartSeconds:            domain.Estimate{Expected: 930, P90: 1394},
					EstablishedStartSeconds: domain.Estimate{Expected: 930, P90: 1394},
				},
			},
		},
		{
			name: "a start latency measured on this offer",
			candidate: domain.CandidateDecision{
				ImageLocality:    domain.LocalityUnknown,
				ArtifactEvidence: unreadableDataset,
				Estimates: domain.CandidateEstimates{
					PullSeconds:             domain.Estimate{Expected: 289},
					ArtifactSeconds:         domain.Estimate{Expected: 640},
					StartSeconds:            domain.Estimate{Expected: 900, P90: 900, SampleCount: 4},
					EstablishedStartSeconds: domain.Estimate{Expected: 900, P90: 900, SampleCount: 4},
				},
			},
			lawful: true,
		},
		{
			name: "a queue the offer stated, beside an input nothing could enumerate",
			candidate: domain.CandidateDecision{
				ImageLocality:    domain.LocalityHot,
				ArtifactEvidence: unreadableDataset,
				Estimates: domain.CandidateEstimates{
					QueueSeconds:            domain.Estimate{Expected: 900, P90: 900},
					ArtifactSeconds:         domain.Estimate{Expected: 640},
					StartSeconds:            domain.Estimate{Expected: 1541, P90: 1861.25},
					EstablishedStartSeconds: domain.Estimate{Expected: 901, P90: 901.25},
				},
			},
			lawful: true,
		},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			candidate := refusal.candidate
			candidate.OfferSnapshotID = "borrowed-host"
			candidate.Rejections = []domain.Violation{{
				Code: "LATENCY_SLO_EXCEEDED",
				Path: "placement.max_p90_start_seconds",
			}}
			observation := InvariantObservation{
				MercatorEvents: []eventlog.CloudEvent{bookingDecidedEvent("evt_slo_refusal", domain.BookingDecision{
					ID:                   "dec_slo_refusal",
					RunID:                "run-impatient",
					Policy:               domain.PlacementPolicy{Objective: domain.ObjectiveBalanced, MaxP90StartSeconds: 180},
					Candidates:           []domain.CandidateDecision{candidate},
					SelectionReasonCodes: []string{"NO_FEASIBLE_OFFERS"},
				})},
			}

			err := localityIsNeverInfeasibility(observation)

			if refusal.lawful && err != nil {
				t.Fatalf("refusing a candidate on %s was called a violation: %v", refusal.name, err)
			}
			if !refusal.lawful && err == nil {
				t.Fatalf("a candidate refused on %s raised nothing", refusal.name)
			}
		})
	}
}

// refusedForHoldingNothing is the decision the locality rule forbids: a machine
// struck out for what it holds. It is deliberately not a code the tree writes,
// because the rule is a law about states Placement must never reach rather than
// a test of one it currently reaches.
func refusedForHoldingNothing() eventlog.CloudEvent {
	return bookingDecidedEvent("evt_locality_refusal", domain.BookingDecision{
		ID:    "dec_locality_refusal",
		RunID: "run-cold",
		Candidates: []domain.CandidateDecision{{
			OfferSnapshotID: "rental-cold",
			ImageLocality:   domain.LocalityCold,
			Rejections: []domain.Violation{{
				Code:    "IMAGE_NOT_CACHED",
				Path:    "images",
				Message: "Offer does not hold the Run's image.",
			}},
		}},
		SelectionReasonCodes: []string{"NO_FEASIBLE_OFFERS"},
	})
}

func bookingDecidedEvent(id string, decision domain.BookingDecision) eventlog.CloudEvent {
	data, err := json.Marshal(struct {
		Decision domain.BookingDecision `json:"decision"`
	}{decision})
	if err != nil {
		panic(err)
	}
	return eventlog.CloudEvent{ID: id, Type: orchestrator.EventBookingDecided, Data: data}
}

func invariantResultByID(t *testing.T, results []InvariantResult, id string) InvariantResult {
	t.Helper()
	for _, result := range results {
		if result.ID == id {
			return result
		}
	}
	t.Fatalf("invariant results have no %q: %+v", id, results)
	return InvariantResult{}
}

func demoInputs(t *testing.T) (scenario.Blueprint, WorldTape, []Sample) {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	return blueprint, tape, samples
}
