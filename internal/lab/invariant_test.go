package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	if len(latest) != 35 {
		t.Fatalf("latest invariant results = %d, want 35", len(latest))
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
		// The world this exists to catch is the one the tree was in: the machine
		// reported the moment its container really began, and the record filed the
		// moment the launch was accepted. Nothing else in the registry reads either
		// field, so a start derived from acceptance was invisible until this rule.
		"safety.start_is_observed_not_inferred": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{
				runStartObserved("running", "2026-07-24T12:04:10Z", "2026-07-24T12:05:00Z"),
				runStartRecorded("2026-07-24T12:00:00Z"),
			}
		},
		// The world this exists to catch is a stage that happened and was measured
		// against nothing. The decision predicted all eight stages, the world spent
		// all eight, and the ledger reports seven: the unpack the machine really did
		// is a prediction with no actual, which is the state a bundle would be
		// exported in if a stage were dropped from the record.
		"safety.prediction_is_recorded_against_its_actual": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{launchPredictingEveryStage("run-waterfall")}
			observation.Effects = []EffectRecord{launchSpendingEveryStageBut("run-waterfall", domain.StageUnpack)}
		},
		// The world this exists to catch is the one the tree was in: the workload
		// stated a readiness and the record took it whatever it said, so a host with
		// the wrong clock filed an hour of ready latency as the application's own
		// measurement. Nothing else in the registry reads the readiness at all.
		"safety.readiness_is_reported_not_inferred": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{
				runReadinessReported("2026-07-24T13:04:10Z", "2026-07-24T12:05:00Z"),
			}
			observation.Runs = []domain.RunRecord{runRecordReady("2026-07-24T13:04:10Z")}
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
		// A candidate charged forty gigabytes at a throughput presented as
		// measured, on a machine that published nothing about that path. The
		// seconds read like every other prediction in the record, and there is
		// nobody at all behind the number they were divided by.
		"safety.transfer_rate_is_attributed": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{pricedAtARate(observation.Now, measuredByNobody(), domain.Estimate{Expected: 80, P50: 80, P90: 120, Confidence: 0.9}, 0.9)}
		},
		"safety.locality_is_never_infeasibility": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{refusedForHoldingNothing()}
		},
		"safety.score_is_reproducible_from_the_record": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{scoredOnATermReadOffTheOffer()}
		},
		// The world this exists to catch is the one the tree was in for a phase: a
		// candidate charged a tenth of a point for the confidence beside a published
		// risk history, which the score reads nothing of. The arithmetic rule above
		// finds nothing wrong with it, because both models charged that doubt and
		// the score reproduces from the record exactly.
		"safety.doubt_only_the_answers_the_score_reads": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{doubtedAboutARateNothingPrices()}
		},
		// A Run queued behind a Booking with nothing left to project from. The
		// latest start it was promised is the moment it was promised, so the
		// guarantee was already spent when the decision recording it was written.
		"safety.promised_start_is_still_ahead": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{queuedOnADeadlineAlreadyReached(now)}
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
		// A speculative fetch running on the same machine, at the same moment,
		// as the pull a Run already admitted there is waiting for. That Run's
		// start is now behind content nobody has asked to run.
		"safety.prewarm_yields_to_real_work": func(observation *InvariantObservation) {
			observation.Now = now.Add(2 * time.Minute)
			observation.Effects = []EffectRecord{
				admittedPullEffect(1, now, "rental-warm", "trainer@sha256:aaaa", now.Add(10*time.Minute)),
				prefetchEffect(2, now.Add(time.Minute), OperationNodePrepareImage, "rental-warm", "sha256:bbbb", "run-queued"),
			}
		},
		// Two speculative fetches begun a minute apart in a world that allows one
		// every five. Neither of them competes with admitted work and only one is
		// ever moving, so the depth rule and the starvation rule both pass: this
		// is the control plane spending a machine's link faster than its operator
		// said it may.
		"safety.prewarm_rate_within_bound": func(observation *InvariantObservation) {
			observation.Now = now.Add(2 * time.Minute)
			observation.Prewarm = &scenario.PrewarmSpec{
				MaxConcurrent: 2,
				MinInterval:   scenario.Duration(5 * time.Minute),
			}
			observation.Effects = []EffectRecord{
				prefetchEffect(1, now, OperationNodePrepareArtifact, "rental-warm", "artifact:corpus:v70", "run-queued"),
				prefetchEffect(2, now.Add(time.Minute), OperationNodePrepareArtifact, "rental-warm", "artifact:corpus:v7", "run-audit"),
			}
		},
		// A preparation that never resolved: the content has not landed, nothing
		// withdrew it, and the machine has been holding room for it all day.
		"liveness.prefetch_converges": func(observation *InvariantObservation) {
			observation.Now = now.Add(prefetchConvergenceBound + time.Hour)
			observation.Effects = []EffectRecord{
				prefetchEffect(1, now, OperationNodePrepareArtifact, "rental-warm", "artifact-1", "run-queued"),
			}
		},
		// Two machines the world says are different, filed under one key. This is
		// the state the tree was in twice over: a lease shared by two enrolled
		// machines named them both, and an inventory grouped one way dropped half a
		// machine's cards. The rule counts the cards where the key groups them, so
		// it can see a machine with twice the hardware wearing another's name.
		"safety.candidate_identity_recurs": func(observation *InvariantObservation) {
			shared := domain.CandidateIdentity{
				Lane:        domain.LaneEphemeral,
				Provider:    "simvast",
				Region:      "US-CA",
				Accelerator: "nvidia-a100x2",
				ImageDigest: "sha256:image",
			}
			observation.World.Offers = []domain.OfferSnapshot{
				gpuOffer("ask-4417", 2),
				gpuOffer("ask-90218", 4),
			}
			observation.MercatorEvents = []eventlog.CloudEvent{
				bookingDecidedEvent("decision-1", domain.BookingDecision{
					RunID: "run-1",
					Candidates: []domain.CandidateDecision{
						{OfferSnapshotID: "ask-4417", Candidate: shared},
						{OfferSnapshotID: "ask-90218", Candidate: shared},
					},
				}),
			}
		},
		// A prediction reporting this exact candidate out of the ask its listing
		// arrived under. The seconds and the sample count read as evidence about
		// this machine, and the key they came from is a number the marketplace
		// mints per search: whatever launch is behind them, it can never be read
		// back and it was never about this candidate.
		"safety.prediction_states_its_provenance": func(observation *InvariantObservation) {
			observation.MercatorEvents = []eventlog.CloudEvent{
				bookingDecidedEvent("decision-1", domain.BookingDecision{
					RunID: "run-1",
					Candidates: []domain.CandidateDecision{{
						OfferSnapshotID: "ask-4417",
						Candidate: domain.CandidateIdentity{
							Lane: domain.LaneEphemeral, Provider: "simvast", Region: "US-CA",
							Accelerator: "nvidia-a100x2", ImageDigest: "sha256:image",
						},
						Estimates: domain.CandidateEstimates{Stages: keyedStages("ask-4417")},
					}},
				}),
			}
		},
		"liveness.admitted_run_progress": func(observation *InvariantObservation) {
			observation.Now = now.Add(25 * time.Hour)
			observation.Runs = []domain.RunRecord{{ID: "run-1", Phase: "running"}}
			observation.RunRequirements["run-1"] = RunArrival{Name: "run-1"}
		},
		// Aging switched off: a Run of the most patient class still waiting three
		// hours after admission first deferred it, which is an hour past the
		// longest wait any class declares. This is the state every unplaceable
		// Run in the tree was in, and the liveness rule that should have caught
		// it exempted the queued phase by name.
		"liveness.aging_prevents_starvation": func(observation *InvariantObservation) {
			queuedSince := now.Add(-3 * time.Hour)
			observation.Runs = []domain.RunRecord{{
				ID:           "run-patient",
				Phase:        "queued",
				ServiceClass: domain.ClassOpportunistic,
				QueuedSince:  &queuedSince,
				Admission: &domain.AdmissionDeferral{
					Reason: domain.DeferredNoFeasibleOffer,
					Class:  domain.ClassOpportunistic,
					Behind: []domain.QueuedAhead{{RunID: "run-hog"}},
				},
			}}
		},
		// A stream of urgent arrivals stepping over work already waiting: an
		// interactive Run is admitted a minute after an experimental Run was told
		// to wait, and neither class declares itself eligible to backfill.
		"safety.service_class_admission_order": func(observation *InvariantObservation) {
			observation.Workloads["run-urgent"] = classedWorkload(domain.ClassInteractive)
			observation.MercatorEvents = []eventlog.CloudEvent{
				admissionDeferredEvent("run-patient", now, domain.ClassExperimental),
				admittedDecisionEvent("run-urgent", now.Add(10*time.Minute)),
			}
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

// TestEveryClauseOfTheCandidateIdentityRuleCanFail is the identity rule read the way
// every law here has to be readable. The registry's single deliberate case drives one
// of its clauses, a collision between two capacities the world says are different,
// and a reviewer showed that two of the others could be broken with the whole tree
// green. Each clause is shown failing on the one record it exists to catch.
//
// The lane clause and the collision clause are also driven through the whole control
// plane by a-candidate-recurs-through-the-control-plane, where dropping either from
// the derivation fails this rule on a real Booking Decision. The content clause is
// only here: no Blueprint states a world where two registries go silent on one
// machine, so nothing a World Tape can generate reaches it.
func TestEveryClauseOfTheCandidateIdentityRuleCanFail(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	product := domain.CandidateIdentity{
		Lane:        domain.LaneEphemeral,
		Provider:    "simvast",
		Region:      "US-CA",
		Accelerator: "nvidia-a100x2",
		ImageDigest: "sha256:image",
	}
	for name, observed := range map[string]struct {
		offers    []domain.OfferSnapshot
		workloads map[string]domain.WorkloadRevision
		decisions []domain.BookingDecision
	}{
		// One product a provider sells both ways. The listing that becomes a machine
		// Mercator enrols a runtime on and the listing that is a one-shot execution are
		// two things to learn about, and this key is one name over both.
		"a rental and a one-shot execution of one product under one key": {
			offers: []domain.OfferSnapshot{
				gpuOffer("ask-4417", 2),
				reusable(gpuOffer("ask-4417-rented", 2)),
			},
			decisions: []domain.BookingDecision{{
				RunID: "run-1",
				Candidates: []domain.CandidateDecision{
					{OfferSnapshotID: "ask-4417", Candidate: product},
					{OfferSnapshotID: "ask-4417-rented", Candidate: product},
				},
			}},
		},
		// Two Runs that asked one machine for different content, filed under one
		// content key. This is what every image a registry would not name looked like:
		// the digest was empty, the key carried the emptiness, and one bucket per
		// machine held every unresolvable image in the fleet.
		"two Runs that asked one machine for different content under one key": {
			offers: []domain.OfferSnapshot{gpuOffer("ask-4417", 2)},
			workloads: map[string]domain.WorkloadRevision{
				"run-1": workloadAsking("trainer@sha256:aaaa"),
				"run-2": workloadAsking("scorer@sha256:bbbb"),
			},
			decisions: []domain.BookingDecision{
				{RunID: "run-1", Candidates: []domain.CandidateDecision{{OfferSnapshotID: "ask-4417", Candidate: product}}},
				{RunID: "run-2", Candidates: []domain.CandidateDecision{{OfferSnapshotID: "ask-4417", Candidate: product}}},
			},
		},
		// A key naming the lease rather than the machine. An operator may invite two
		// machines against one rental_id, and this is the record that would file the
		// second machine's first launch under the first machine's pull samples.
		"a key naming something other than the machine its backend published": {
			offers: []domain.OfferSnapshot{enrolledOffer("node-1", "rnt_shared")},
			decisions: []domain.BookingDecision{{
				RunID: "run-1",
				Candidates: []domain.CandidateDecision{{
					OfferSnapshotID: "node-1",
					Candidate:       domain.CandidateIdentity{Lane: domain.LaneReusable, Provider: "simnode", Machine: "rnt_shared", ImageDigest: "sha256:image"},
				}},
			}},
		},
		// A key naming the listing a search found. A Vast ask ID is a fresh integer for
		// a machine that was already there, so this history is a pile of one-sample
		// keys, each reported as evidence about this exact candidate.
		"a key naming the listing search found": {
			offers: []domain.OfferSnapshot{gpuOffer("ask-4417", 2)},
			decisions: []domain.BookingDecision{{
				RunID: "run-1",
				Candidates: []domain.CandidateDecision{{
					OfferSnapshotID: "ask-4417",
					Candidate:       domain.CandidateIdentity{Lane: domain.LaneEphemeral, Provider: "simvast", InstanceType: "ask-4417", ImageDigest: "sha256:image"},
				}},
			}},
		},
		// A key for a one-shot pool that published nothing but its provider. Nothing
		// about it comes back, so a predictor answering "this exact candidate, one
		// sample" there is reporting candidate-specific evidence out of a name that
		// cannot hold a second sample.
		"a key for capacity with nothing published that outlives its listing": {
			offers: []domain.OfferSnapshot{oneShotOffer("off_pool_7f3a")},
			decisions: []domain.BookingDecision{{
				RunID: "run-1",
				Candidates: []domain.CandidateDecision{{
					OfferSnapshotID: "off_pool_7f3a",
					Candidate:       domain.CandidateIdentity{Lane: domain.LaneEphemeral, Provider: "simpool", Region: "US-CA", ImageDigest: "sha256:image"},
				}},
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			observation := InvariantObservation{
				StartedAt:       now,
				Now:             now,
				World:           WorldTruthSnapshot{At: now, Offers: observed.offers},
				Workloads:       observed.workloads,
				RentalSchedules: map[string]domain.RentalSchedule{},
				RunRequirements: map[string]RunArrival{},
				ArtifactCatalog: map[string]domain.ArtifactVersion{},
				SeededLocality:  map[string]map[string]bool{},
			}
			for index, decision := range observed.decisions {
				observation.MercatorEvents = append(observation.MercatorEvents,
					bookingDecidedEvent(fmt.Sprintf("decision-%d", index+1), decision))
			}

			result := invariantResultByID(t,
				DefaultInvariantRegistry().Evaluate(observation),
				"safety.candidate_identity_recurs",
			)

			if result.Status != InvariantFailed || result.Violation == "" {
				t.Fatalf("%s was reported as a key a launch history may be filed under: %+v", name, result)
			}
		})
	}
}

// TestEveryClauseOfThePredictionProvenanceRuleCanFail is the provenance rule read
// the way every law here has to be readable. The registry's single deliberate
// case drives the clause the rule exists for, an exact-candidate answer read out
// of the listing it arrived on, and each of the others is shown failing on the
// one record it is there to catch. None of these records is written by any code
// in this tree, which is what a standing rule is for: it is what the tree would
// have to start writing for the record to stop being auditable.
func TestEveryClauseOfThePredictionProvenanceRuleCanFail(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	machine := domain.CandidateIdentity{
		Lane: domain.LaneEphemeral, Provider: "simvast", Region: "US-CA",
		Machine: "machine-77", Accelerator: "nvidia-a100x2", ImageDigest: "sha256:image",
	}
	for name, answer := range map[string]domain.Estimate{
		// A prediction that does not say what it rests on. Ninety seconds from five
		// launches of this machine and ninety seconds from a constant read the same,
		// and a calibration cannot tell which of them it is allowed to grade.
		"a stage that names no level at all": {Expected: 90},
		// Samples filed under an answer that says nobody has ever watched this
		// happen. One of the two statements is false and the record does not say
		// which.
		"a prior carrying samples": {Expected: 90, Level: domain.LevelPrior, SampleCount: 3, Key: machine.Candidate(true)},
		// A level claiming measured launches with none behind it.
		"a keyed level with nothing measured under it": {
			Expected: 90, Level: domain.LevelProviderAndRegion, Key: machine.ProviderAndRegion(),
		},
		// An answer at a level of the hierarchy with no key to have read it from.
		"a keyed level naming no key": {Expected: 90, Level: domain.LevelProvider, SampleCount: 2},
		// The clause the rule exists for: this exact candidate, answered out of the
		// ask ID the listing arrived under, which a marketplace mints per search.
		"an exact-candidate answer read out of the listing": {
			Expected: 90, Level: domain.LevelExactCandidate, SampleCount: 1, Key: "ask-4417",
		},
		// The same defect wearing the provider's own name for the listing.
		"an answer read out of the provider's native reference": {
			Expected: 90, Level: domain.LevelExactCandidate, SampleCount: 1, Key: "lane=ephemeral;native=4417",
		},
		// An exact-candidate answer about some other machine. The key recurs, the
		// samples are real, and they are somebody else's.
		"an exact-candidate answer under another candidate's key": {
			Expected: 90, Level: domain.LevelExactCandidate, SampleCount: 4,
			Key: "lane=ephemeral;provider=simvast;machine=machine-88;image=sha256:image",
		},
		// A level nothing in the hierarchy answers at.
		"a level the hierarchy does not have": {
			Expected: 90, Level: domain.PredictionLevel("guess"), SampleCount: 1, Key: machine.Candidate(true),
		},
	} {
		t.Run(name, func(t *testing.T) {
			observation := predictionObservation(now, machine, everyStage(answer))

			result := invariantResultByID(t,
				DefaultInvariantRegistry().Evaluate(observation),
				"safety.prediction_states_its_provenance",
			)

			if result.Status != InvariantFailed || result.Violation == "" {
				t.Fatalf("%s was reported as a prediction that states its provenance: %+v", name, result)
			}
		})
	}
}

// TestAnAnsweredStageAndAPriorAreBothHonestProvenance is the counterpart: the
// rule has to pass the two records production actually writes, or it could only
// be satisfied by a tree that predicts nothing.
func TestAnAnsweredStageAndAPriorAreBothHonestProvenance(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	machine := domain.CandidateIdentity{
		Lane: domain.LaneEphemeral, Provider: "simvast", Region: "US-CA",
		Machine: "machine-77", Accelerator: "nvidia-a100x2", ImageDigest: "sha256:image",
	}
	for name, answer := range map[string]func(domain.LaunchStage) domain.Estimate{
		// The content stages carry the content in their key and the machine stages
		// do not, which is what keeps one machine's boot history from being split
		// across every image the fleet ever ran on it.
		"a stage answered from this machine's own launches": func(stage domain.LaunchStage) domain.Estimate {
			return domain.Estimate{
				Expected: 30, Level: domain.LevelExactCandidate, SampleCount: 1,
				Key: machine.Candidate(contentStage(stage)),
			}
		},
		"a stage answered from the province the machine is in": func(domain.LaunchStage) domain.Estimate {
			return domain.Estimate{
				Expected: 90, Level: domain.LevelProviderAndRegion, SampleCount: 2, Key: machine.ProviderAndRegion(),
			}
		},
		"a stage nobody has ever measured": func(domain.LaunchStage) domain.Estimate {
			return domain.Estimate{Expected: 300, Level: domain.LevelPrior}
		},
	} {
		t.Run(name, func(t *testing.T) {
			observation := predictionObservation(now, machine, answer)

			result := invariantResultByID(t,
				DefaultInvariantRegistry().Evaluate(observation),
				"safety.prediction_states_its_provenance",
			)

			if result.Status != InvariantPassed {
				t.Fatalf("%s was reported as a violation: %s", name, result.Violation)
			}
		})
	}
}

// predictionObservation is one recorded decision whose candidate answered every
// stage the same way, which is what lets a case state the answer alone.
func predictionObservation(now time.Time, identity domain.CandidateIdentity, answer func(domain.LaunchStage) domain.Estimate) InvariantObservation {
	return InvariantObservation{
		StartedAt:       now,
		Now:             now,
		World:           WorldTruthSnapshot{At: now, Offers: []domain.OfferSnapshot{gpuOffer("ask-4417", 2)}},
		Workloads:       map[string]domain.WorkloadRevision{},
		RentalSchedules: map[string]domain.RentalSchedule{},
		RunRequirements: map[string]RunArrival{},
		ArtifactCatalog: map[string]domain.ArtifactVersion{},
		SeededLocality:  map[string]map[string]bool{},
		MercatorEvents: []eventlog.CloudEvent{bookingDecidedEvent("decision-1", domain.BookingDecision{
			RunID: "run-1",
			Candidates: []domain.CandidateDecision{{
				OfferSnapshotID: "ask-4417",
				NativeRef:       "4417",
				Candidate:       identity,
				Estimates: domain.CandidateEstimates{Stages: domain.LaunchStageEstimates{}.Answered(
					func(stage domain.LaunchStage, _ domain.Estimate) domain.Estimate { return answer(stage) },
				)},
			}},
		})},
	}
}

// everyStage is one answer given whatever stage is asked, so a case about the
// record itself states it once rather than eight times.
func everyStage(answer domain.Estimate) func(domain.LaunchStage) domain.Estimate {
	return func(domain.LaunchStage) domain.Estimate { return answer }
}

// keyedStages is every stage answered as this exact candidate out of the listing
// the offer arrived under, which is the record the deliberate registry case is
// about.
func keyedStages(listing string) domain.LaunchStageEstimates {
	return domain.LaunchStageEstimates{}.Answered(
		func(domain.LaunchStage, domain.Estimate) domain.Estimate {
			return domain.Estimate{Expected: 90, Level: domain.LevelExactCandidate, SampleCount: 1, Key: listing}
		},
	)
}

// reusable is the same listing in the other lane: capacity Mercator would enrol a
// runtime on rather than borrow one execution from.
func reusable(offer domain.OfferSnapshot) domain.OfferSnapshot {
	offer.Lane = domain.LaneReusable
	offer.Kind = domain.OfferKindStanding
	return offer
}

// enrolledOffer is a machine Mercator keeps, which names itself and holds a lease
// two machines can share.
func enrolledOffer(nodeID, rentalID string) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:          nodeID,
		NativeRef:   nodeID,
		MachineID:   nodeID,
		RentalID:    rentalID,
		AdapterType: "simnode",
		Kind:        domain.OfferKindStanding,
		Lane:        domain.LaneReusable,
	}
}

// oneShotOffer is a provider-native one-shot execution product that publishes no
// place, no product name, and no cards: its listing ID is the only handle it has.
func oneShotOffer(offerID string) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:          offerID,
		NativeRef:   offerID,
		AdapterType: "simpool",
		Kind:        domain.OfferKindStanding,
		Lane:        domain.LaneEphemeral,
	}
}

// workloadAsking is what Mercator recorded it was asked to run, which is where a rule
// about content reads the content from.
func workloadAsking(image string) domain.WorkloadRevision {
	return domain.WorkloadRevision{
		Spec: domain.WorkloadSpec{Containers: []domain.ContainerSpec{{Name: "main", Image: image}}},
	}
}

// TestEveryClauseOfTheStartRuleCanFail is the start rule read the way every law
// here has to be readable. The registry's single deliberate case drives one of its
// three clauses, and the clause about a moment ahead of the read that carried it
// could be deleted with the whole tree staying green: neither simulated world can
// publish a start that has not arrived, so the only inputs that reach it come from
// a real provider's foreign clock and no World Tape can generate one. Each clause
// is shown failing on the one record it exists to catch.
func TestEveryClauseOfTheStartRuleCanFail(t *testing.T) {
	for name, events := range map[string][]eventlog.CloudEvent{
		"records a start no observation of it reported": {
			runStartRecorded("2026-07-24T12:04:10Z"),
		},
		"records a moment its observations never stated": {
			runStartObserved("running", "2026-07-24T12:04:10Z", "2026-07-24T12:05:00Z"),
			runStartRecorded("2026-07-24T12:00:00Z"),
		},
		"records a start its holder published ahead of the read that carried it": {
			runStartObserved("running", "2026-07-24T12:06:00Z", "2026-07-24T12:05:00Z"),
			runStartRecorded("2026-07-24T12:06:00Z"),
		},
		"records a start its holder published for work it said had not begun": {
			runStartObserved("queued", "2026-07-24T12:04:10Z", "2026-07-24T12:05:00Z"),
			runStartRecorded("2026-07-24T12:04:10Z"),
		},
		"throws away a start its holder established": {
			runStartObserved("running", "2026-07-24T12:04:10Z", "2026-07-24T12:05:00Z"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			observation := startRuleObservation(events)

			result := invariantResultByID(t,
				DefaultInvariantRegistry().Evaluate(observation),
				"safety.start_is_observed_not_inferred",
			)

			if result.Status != InvariantFailed || result.Violation == "" {
				t.Fatalf("a Run that %s was reported as measuring a start somebody observed: %+v", name, result)
			}
		})
	}
}

// TestABookingClockIsHeldToTheSameLawAsTheRunStream is the clause the run stream's
// half of this rule was missing. A Booking's declared runtimes are enforced from its
// StartedAt, so a moment Mercator refused as this Run's start is a moment it must
// refuse as this Booking's clock too: adopting it leaves the bound unexpired long
// after the capacity was really spent, with the schedule reporting the machine busy
// the whole time. Two moments are defensible, and each wrong one is its own case.
func TestABookingClockIsHeldToTheSameLawAsTheRunStream(t *testing.T) {
	looks := []eventlog.CloudEvent{
		runStartObserved("running", "2026-07-24T13:04:10Z", "2026-07-24T12:05:00Z"),
	}
	for name, clock := range map[string]string{
		"a moment its holder published ahead of the read that carried it": "2026-07-24T13:04:10Z",
		"a moment nothing in its record ever stated":                      "2026-07-24T11:00:00Z",
	} {
		t.Run(name, func(t *testing.T) {
			observation := startRuleObservation(looks)
			observation.RentalSchedules = map[string]domain.RentalSchedule{
				"rental-ahead": bookingMeasuredFrom("run-1", clock),
			}

			result := invariantResultByID(t,
				DefaultInvariantRegistry().Evaluate(observation),
				"safety.start_is_observed_not_inferred",
			)

			if result.Status != InvariantFailed || result.Violation == "" {
				t.Fatalf("a Booking measured from %s was reported as measuring from a moment somebody observed: %+v", name, result)
			}
		})
	}
}

// TestABookingMeasuredFromTheReadThatCarriedItHolds is the fallback the clause has
// to allow. Nothing established this container's start, so the schedule projects
// from the last instant Mercator can prove the container was up, which is the moment
// it read the observation. That is a projection and never a record: it reaches no
// run stream and no calibration.
func TestABookingMeasuredFromTheReadThatCarriedItHolds(t *testing.T) {
	observation := startRuleObservation([]eventlog.CloudEvent{
		runStartObserved("running", "2026-07-24T13:04:10Z", "2026-07-24T12:05:00Z"),
	})
	observation.RentalSchedules = map[string]domain.RentalSchedule{
		"rental-ahead": bookingMeasuredFrom("run-1", "2026-07-24T12:05:00Z"),
	}

	result := invariantResultByID(t,
		DefaultInvariantRegistry().Evaluate(observation),
		"safety.start_is_observed_not_inferred",
	)

	if result.Status != InvariantPassed {
		t.Fatalf("a Booking measured from the read that carried its observation was refused: %s", result.Violation)
	}
}

// bookingMeasuredFrom is one Rental holding one Run's Booking, with the moment that
// Booking's enforced runtime is measured from.
func bookingMeasuredFrom(runID, from string) domain.RentalSchedule {
	startedAt, err := time.Parse(time.RFC3339Nano, from)
	if err != nil {
		panic(err)
	}
	return domain.RentalSchedule{
		RentalID: "rental-ahead",
		Bookings: []domain.ScheduledBooking{{
			Booking: domain.Booking{
				ID:       "booking-1",
				RunID:    runID,
				RentalID: "rental-ahead",
				State:    domain.BookingStateRunning,
			},
			ExpectedRuntimeSeconds: 600,
			MaxRuntimeSeconds:      1200,
			StartedAt:              startedAt,
		}},
	}
}

// TestAStartClaimMercatorRefusedIsNotAViolation is the other side of the same law.
// A host whose clock runs ahead publishes a moment Mercator cannot have observed,
// the control plane declines to make it this Run's start, and the observation still
// carries the claim. Blaming Mercator for the refusal would make the rule demand
// exactly the record it exists to forbid.
func TestAStartClaimMercatorRefusedIsNotAViolation(t *testing.T) {
	observation := startRuleObservation([]eventlog.CloudEvent{
		runStartObserved("running", "2026-07-24T12:06:00Z", "2026-07-24T12:05:00Z"),
		runStartObserved("queued", "2026-07-24T12:04:10Z", "2026-07-24T12:05:00Z"),
	})

	result := invariantResultByID(t,
		DefaultInvariantRegistry().Evaluate(observation),
		"safety.start_is_observed_not_inferred",
	)

	if result.Status != InvariantPassed {
		t.Fatalf("Mercator was blamed for declining a moment it could not defend: %s", result.Violation)
	}
}

func startRuleObservation(events []eventlog.CloudEvent) InvariantObservation {
	now := time.Date(2026, 7, 24, 12, 10, 0, 0, time.UTC)
	return InvariantObservation{
		StartedAt:                   now,
		Now:                         now,
		World:                       WorldTruthSnapshot{At: now},
		MercatorEvents:              events,
		Workloads:                   map[string]domain.WorkloadRevision{},
		RentalSchedules:             map[string]domain.RentalSchedule{},
		RunRequirements:             map[string]RunArrival{},
		ArtifactCatalog:             map[string]domain.ArtifactVersion{},
		SeededLocality:              map[string]map[string]bool{},
		ProjectionRebuildEquivalent: true,
	}
}

// runStartObserved is one look at run-1 that published a start moment, and
// runStartRecorded is run-1's stream saying that is when its workload began.
func runStartObserved(phase, started, observed string) eventlog.CloudEvent {
	return eventlog.CloudEvent{
		Subject: "runs/run-1",
		Type:    orchestrator.EventExternalStateObserved,
		Data: []byte(`{"launch_key":"launch-1","phase":"` + phase +
			`","observed_at":"` + observed + `","started_at":"` + started + `"}`),
	}
}

func runStartRecorded(started string) eventlog.CloudEvent {
	return eventlog.CloudEvent{
		Subject: "runs/run-1",
		Type:    orchestrator.EventExecutionStarted,
		Data:    []byte(`{"launch_key":"launch-1","started_at":"` + started + `"}`),
	}
}

// TestEveryClauseOfTheReadinessRuleCanFail is the readiness rule read the way every
// law here has to be readable. Four clauses, four records each one exists to catch,
// and the registry's single deliberate case drives one of them.
func TestEveryClauseOfTheReadinessRuleCanFail(t *testing.T) {
	for name, observed := range map[string]struct {
		events []eventlog.CloudEvent
		runs   []domain.RunRecord
	}{
		"records a readiness no report of it stated": {
			runs: []domain.RunRecord{runRecordReady("2026-07-24T12:06:00Z")},
		},
		"records a readiness its workload published ahead of the read that carried it": {
			events: []eventlog.CloudEvent{runReadinessReported("2026-07-24T12:06:00Z", "2026-07-24T12:05:00Z")},
			runs:   []domain.RunRecord{runRecordReady("2026-07-24T12:06:00Z")},
		},
		"records its application serving before its container started": {
			events: []eventlog.CloudEvent{
				runStartRecorded("2026-07-24T12:04:10Z"),
				runReadinessReported("2026-07-24T12:00:00Z", "2026-07-24T12:05:00Z"),
			},
			runs: []domain.RunRecord{runRecordReady("2026-07-24T12:00:00Z")},
		},
		"throws away a readiness its workload stated": {
			events: []eventlog.CloudEvent{runReadinessReported("2026-07-24T12:04:10Z", "2026-07-24T12:05:00Z")},
			runs:   []domain.RunRecord{{ID: "run-1"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			observation := startRuleObservation(observed.events)
			observation.Runs = observed.runs

			result := invariantResultByID(t,
				DefaultInvariantRegistry().Evaluate(observation),
				"safety.readiness_is_reported_not_inferred",
			)

			if result.Status != InvariantFailed || result.Violation == "" {
				t.Fatalf("a Run that %s was reported as measuring a readiness its workload stated: %+v", name, result)
			}
		})
	}
}

// TestAReadinessRefusedIsNotAViolation is the other side of the same law. A
// workload on a host whose clock runs ahead states a moment Mercator cannot have
// reached, the control plane declines to adopt it, and the report still sits in the
// log saying what the workload said. The rule is about what Mercator recorded, so
// the refusal is the rule holding rather than breaking.
func TestAReadinessRefusedIsNotAViolation(t *testing.T) {
	observation := startRuleObservation([]eventlog.CloudEvent{
		runReadinessReported("2026-07-24T13:04:10Z", "2026-07-24T12:05:00Z"),
	})
	observation.Runs = []domain.RunRecord{{ID: "run-1"}}

	result := invariantResultByID(t,
		DefaultInvariantRegistry().Evaluate(observation),
		"safety.readiness_is_reported_not_inferred",
	)

	if result.Status != InvariantPassed {
		t.Fatalf("declining a readiness nobody could defend was reported as a violation: %s", result.Violation)
	}
}

// runReadinessReported is run-1's workload saying it can do work, and the moment
// Mercator appended the report saying so.
func runReadinessReported(readyAt, readAt string) eventlog.CloudEvent {
	return eventlog.CloudEvent{
		Subject: "runs/run-1",
		Type:    orchestrator.EventRunReported,
		Time:    readAt,
		Data:    []byte(`{"type":"ready","data":{"ready_at":"` + readyAt + `"}}`),
	}
}

// runRecordReady is run-1 as Mercator's read model has it, carrying the readiness
// the control plane adopted.
func runRecordReady(readyAt string) domain.RunRecord {
	moment, err := time.Parse(time.RFC3339Nano, readyAt)
	if err != nil {
		panic(err)
	}
	return domain.RunRecord{ID: "run-1", ReadyAt: &moment}
}

// TestEveryClauseOfTheTransferRateRuleCanFail is the attribution rule read the
// way every law here has to be readable. The registry's single deliberate case
// drives one of its clauses, and each of the others is shown failing on the one
// record it exists to catch. Every case here is a record no code in this tree
// writes, which is what a standing law is: the rule exists so a slice reaching
// for a faster answer cannot write one of them and stay green.
func TestEveryClauseOfTheTransferRateRuleCanFail(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	reading := func(confidence float64) []domain.NetworkFact {
		return []domain.NetworkFact{{
			Scope:      domain.NetworkScopeObjectStore,
			Statistic:  "p10",
			ValueMbps:  200,
			Source:     "node_probe",
			ObservedAt: now,
			Confidence: confidence,
		}}
	}
	measured := reading(0.9)
	disowned := reading(0)

	for name, observed := range map[string]struct {
		published []domain.NetworkFact
		rate      domain.TransferRate
		// read is the answer the rate produced, stated only by the cases about what
		// an unmeasured transfer is worth. Everywhere else the seconds are beside the
		// point: the rule reads provenance, and a record that names nothing is
		// unattributed whatever it predicted.
		read domain.Estimate
		// scored is what the decision told its own score that answer was worth,
		// which is the number the ranking charges doubt from. It is stated on its
		// own because the record carries it separately from the estimate, so a
		// decision can be honest in one of them and not in the other.
		scored float64
	}{
		"priced a transfer from nothing it names": {
			published: measured,
			rate: domain.TransferRate{
				Stage: domain.StageArtifactFetch,
				Scope: domain.NetworkScopeObjectStore,
				Mbps:  200,
				Bytes: 40_000_000_000,
			},
		},
		"priced a transfer from a measurement and an assumption at once": {
			published: measured,
			rate: domain.TransferRate{
				Stage:       domain.StageArtifactFetch,
				Scope:       domain.NetworkScopeObjectStore,
				Mbps:        200,
				Bytes:       40_000_000_000,
				Measurement: "node_probe",
				Assumption:  domain.AssumptionObjectStoreRate,
			},
		},
		"priced a transfer at a measured rate this machine never reported": {
			published: measured,
			rate:      measuredByNobody(),
		},
		"priced a transfer at a measurement its own publisher disowned": {
			published: disowned,
			rate: domain.TransferRate{
				Stage:       domain.StageArtifactFetch,
				Scope:       domain.NetworkScopeObjectStore,
				Mbps:        200,
				Bytes:       40_000_000_000,
				Measurement: "node_probe",
			},
		},
		"priced a transfer at a measurement of another path": {
			published: measured,
			rate: domain.TransferRate{
				Stage:       domain.StageImageFetch,
				Scope:       domain.NetworkScopeRegistry,
				Mbps:        200,
				Bytes:       2_000_000_000,
				Measurement: "node_probe",
			},
		},
		"charged no doubt for a rate it admits nothing measured": {
			published: nil,
			rate: domain.TransferRate{
				Stage:      domain.StageArtifactFetch,
				Scope:      domain.NetworkScopeObjectStore,
				Mbps:       domain.DefaultObjectStoreDownloadMbps,
				Bytes:      40_000_000_000,
				Confidence: 1,
				Assumption: domain.AssumptionObjectStoreRate,
			},
			read:   domain.Estimate{Expected: 640, P50: 640, P90: 960, Confidence: 1},
			scored: 1,
		},
		"named its assumption and then answered as if it had measured": {
			published: nil,
			rate: domain.TransferRate{
				Stage:      domain.StageArtifactFetch,
				Scope:      domain.NetworkScopeObjectStore,
				Mbps:       domain.DefaultObjectStoreDownloadMbps,
				Bytes:      40_000_000_000,
				Confidence: domain.AssumedLinkConfidence,
				Assumption: domain.AssumptionObjectStoreRate,
			},
			read:   domain.Estimate{Expected: 640, P50: 640, P90: 960, Confidence: 0.95},
			scored: 0.95,
		},
		// The same lie told to the only reader that matters. The rate names its
		// assumption, the estimate is worth exactly what a guess is worth, and the
		// list the ranking charges doubt from says the read was certain. It is
		// reached by editing the function named for the score's own input, which is
		// nearer to hand than either of the other two.
		"charged the score no doubt for an answer it admits is a guess": {
			published: nil,
			rate: domain.TransferRate{
				Stage:      domain.StageArtifactFetch,
				Scope:      domain.NetworkScopeObjectStore,
				Mbps:       domain.DefaultObjectStoreDownloadMbps,
				Bytes:      40_000_000_000,
				Confidence: domain.AssumedLinkConfidence,
				Assumption: domain.AssumptionObjectStoreRate,
			},
			read:   domain.Estimate{Expected: 640, P50: 640, P90: 960, Confidence: domain.AssumedLinkConfidence},
			scored: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			observation := InvariantObservation{
				StartedAt: now,
				Now:       now,
				World: WorldTruthSnapshot{
					At:             now,
					PublishedPaths: map[string][]domain.NetworkFact{"rental-warm": observed.published},
				},
				MercatorEvents: []eventlog.CloudEvent{pricedAtARate(now, observed.rate, observed.read, observed.scored)},
			}

			result := invariantResultByID(t,
				DefaultInvariantRegistry().Evaluate(observation),
				"safety.transfer_rate_is_attributed",
			)

			if result.Status != InvariantFailed || result.Violation == "" {
				t.Fatalf("a decision that %s was reported as pricing every transfer from something: %+v", name, result)
			}
		})
	}
}

// TestARatePricedFromTheStatedAssumptionIsNotAViolation is the other side of the
// same law. Nothing measures a host's storage, so every assembly in the fleet is
// priced from Mercator's own constant, and the rule exists to make that visible
// rather than to forbid it. A rule that failed an honest assumption would be a
// rule the tree could only satisfy by claiming measurements it does not have.
func TestARatePricedFromTheStatedAssumptionIsNotAViolation(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	observation := InvariantObservation{
		StartedAt: now,
		Now:       now,
		World:     WorldTruthSnapshot{At: now},
		MercatorEvents: []eventlog.CloudEvent{pricedAtARate(now, domain.TransferRateFor(
			domain.StageUnpack, "", 2_000_000_000, domain.UnpackRate(),
		), domain.Estimate{Expected: 8, P50: 8, P90: 12, Confidence: domain.AssumedLinkConfidence}, domain.AssumedLinkConfidence)},
	}

	result := invariantResultByID(t,
		DefaultInvariantRegistry().Evaluate(observation),
		"safety.transfer_rate_is_attributed",
	)

	if result.Status != InvariantPassed {
		t.Fatalf("assembly priced from the assumption every host in the fleet is assumed to unpack at was reported as a violation: %s", result.Violation)
	}
}

// TestARateMeasuredOnCapacitySinceRetiredIsNotAViolation is the third side of the
// law, and the one that decides what the rule is stated over. A decision is taken
// at a moment and judged at a later one: the machine that lost this placement sat
// idle, its lease elapsed, and the world took it away with everything on it. The
// placement was correct, the fact was published, and it was stood behind when the
// number was divided by. A rule asked of the fleet as it stands now reports that as
// a rate nobody measured, which is the accusation it exists to make about a
// prediction that invented one.
func TestARateMeasuredOnCapacitySinceRetiredIsNotAViolation(t *testing.T) {
	decidedAt := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	observation := InvariantObservation{
		StartedAt: decidedAt,
		Now:       decidedAt.Add(time.Hour),
		World: WorldTruthSnapshot{
			At:     decidedAt.Add(time.Hour),
			Offers: nil,
			PublishedPaths: map[string][]domain.NetworkFact{"rental-warm": {{
				Scope:      domain.NetworkScopeObjectStore,
				Statistic:  "p10",
				ValueMbps:  200,
				Source:     "node_probe",
				ObservedAt: decidedAt,
				Confidence: 0.9,
			}}},
		},
		MercatorEvents: []eventlog.CloudEvent{pricedAtARate(decidedAt, domain.TransferRate{
			Stage:       domain.StageArtifactFetch,
			Scope:       domain.NetworkScopeObjectStore,
			Mbps:        200,
			Bytes:       40_000_000_000,
			Confidence:  0.9,
			Measurement: "node_probe",
		}, domain.Estimate{Expected: 1600, P50: 1600, P90: 2400, Confidence: 0.9}, 0.9)},
	}

	result := invariantResultByID(t,
		DefaultInvariantRegistry().Evaluate(observation),
		"safety.transfer_rate_is_attributed",
	)

	if result.Status != InvariantPassed {
		t.Fatalf("a read priced from a path the machine published, on capacity the world has since retired, was reported as a violation: %s",
			result.Violation)
	}
}

// TestEveryClauseOfTheDiskRuleCanFail is the disk rule read the way every law
// here has to be readable: a promise nothing can break is not a promise. The
// rule makes four, and the capacity one is the only one the registry's single
// deliberate case drives, so each of the others is shown failing on the one
// world it exists to catch. The rule this replaced was deleted for exactly this,
// and rebuilding it with three clauses driven by nothing would have been the
// same defect under a better name.
func TestEveryClauseOfTheDiskRuleCanFail(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	held := ArtifactReplica{
		OfferID: "rental-warm",
		ArtifactReplica: domain.ArtifactReplica{
			ArtifactID:    "artifact-1",
			ContentDigest: "sha256:aaaa",
			SizeBytes:     10 << 30,
			State:         domain.ArtifactReplicaVerified,
		},
	}
	accounted := DiskLedger{
		OfferID:       "rental-warm",
		CapacityBytes: 100 << 30,
		Resident:      []ResidentContent{{Kind: ResidentArtifact, Name: "artifact-1", SizeBytes: 10 << 30}},
	}

	for name, world := range map[string]WorldTruthSnapshot{
		"accounts for one machine's disk twice": {
			Disk: []DiskLedger{accounted, accounted}, ArtifactReplicas: []ArtifactReplica{held},
		},
		"holds content this world cannot size": {
			Disk: []DiskLedger{{
				OfferID:       "rental-warm",
				CapacityBytes: 100 << 30,
				Resident:      []ResidentContent{{Kind: ResidentArtifact, Name: "artifact-1"}},
			}},
			ArtifactReplicas: []ArtifactReplica{held},
		},
		"counts the same content twice": {
			Disk: []DiskLedger{{
				OfferID:       "rental-warm",
				CapacityBytes: 100 << 30,
				Resident:      append(slices.Clone(accounted.Resident), accounted.Resident[0]),
			}},
			ArtifactReplicas: []ArtifactReplica{held},
		},
		"holds a copy it accounts for no room for": {
			Disk:             []DiskLedger{{OfferID: "rental-warm", CapacityBytes: 100 << 30}},
			ArtifactReplicas: []ArtifactReplica{held},
		},
		"holds a cache it accounts for no room for": {
			Disk:        []DiskLedger{{OfferID: "rental-warm", CapacityBytes: 100 << 30}},
			CacheMounts: []CacheMountState{{OfferID: "rental-warm", Identity: "ws_lab/build-cache/v1"}},
		},
		"reserves room for content no machine holds": {
			Disk: []DiskLedger{accounted},
		},
	} {
		t.Run(name, func(t *testing.T) {
			world.At = now
			observation := InvariantObservation{
				StartedAt:                   now,
				Now:                         now,
				World:                       world,
				Workloads:                   map[string]domain.WorkloadRevision{},
				RentalSchedules:             map[string]domain.RentalSchedule{},
				RunRequirements:             map[string]RunArrival{},
				ArtifactCatalog:             map[string]domain.ArtifactVersion{"artifact-1": {ID: "artifact-1", ContentDigest: "sha256:aaaa"}},
				SeededLocality:              map[string]map[string]bool{},
				ProjectionRebuildEquivalent: true,
			}

			result := invariantResultByID(t,
				DefaultInvariantRegistry().Evaluate(observation),
				"safety.disk_reservation_respected",
			)

			if result.Status != InvariantFailed || result.Violation == "" {
				t.Fatalf("a machine that %s was reported as keeping an account that adds up: %+v", name, result)
			}
		})
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

// prefetchEffect is Mercator asking one machine to get content ready for a Run
// it has not admitted, with bytes still to move.
func prefetchEffect(sequence uint64, at time.Time, operation, offerID, content, runID string) EffectRecord {
	return EffectRecord{
		Sequence:      sequence,
		At:            at,
		Operation:     operation,
		OperationID:   operation + "/" + offerID + "/" + content,
		Command:       EffectCommandAccepted,
		Response:      EffectResponseDelivered,
		CorrelationID: runID,
		Request:       mustJSON(map[string]any{"offer_id": offerID, "content": content, "run_id": runID}),
		Consequence:   mustJSON(map[string]any{"ready": false, "fetched_bytes": 1 << 30}),
	}
}

// admittedPullEffect is the pull a launch dispatched: content a Run Mercator has
// already placed here is waiting on before its process can start.
func admittedPullEffect(sequence uint64, at time.Time, offerID, image string, completesAt time.Time) EffectRecord {
	return EffectRecord{
		Sequence:      sequence,
		At:            at,
		Operation:     OperationImagePull,
		OperationID:   "image-pull/" + offerID + "/" + image,
		Command:       EffectCommandAccepted,
		Response:      EffectResponseDelivered,
		CorrelationID: "run-admitted",
		Request:       mustJSON(map[string]any{"image": image, "offer_id": offerID}),
		Consequence:   mustJSON(map[string]any{"fetched_bytes": 1 << 30, "completes_at": completesAt}),
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

// queuedOnADeadlineAlreadyReached is the placement an exhausted schedule
// produces: the Rental is held by a Booking past the runtime Mercator enforces, so
// the remaining runtimes it projects from are zero and the arriving Run's latest
// acceptable start is the instant of the decision itself.
func queuedOnADeadlineAlreadyReached(evaluatedAt time.Time) eventlog.CloudEvent {
	return bookingDecidedEvent("evt_deadline_reached", domain.BookingDecision{
		ID:                      "dec_deadline_reached",
		RunID:                   "run-arriving",
		EvaluatedAt:             evaluatedAt,
		SelectedOfferSnapshotID: "rental-warm",
		Candidates: []domain.CandidateDecision{{
			OfferSnapshotID: "rental-warm",
			Disposition:     domain.CandidateDispositionQueue,
			Feasible:        true,
		}},
		Booking: &domain.Booking{
			ID:               "bkg_arriving",
			RunID:            "run-arriving",
			RentalID:         "rental-warm",
			State:            domain.BookingStateQueued,
			AfterBookingID:   "bkg_overrun",
			ProjectedStartAt: &evaluatedAt,
			LatestStartAt:    &evaluatedAt,
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
// The third is the same mistake made about the other half of a duration, and it
// is the one an exact byte count disguises: this machine enumerated its copies
// and holds none of them, so nothing about the fetch is unknown except how fast
// the path carries it, and the seconds it was refused on came out of the prior
// every silent machine is priced from. Its lawful twin beside it changes one
// field, the provenance of the rate, which is the whole difference between a
// machine found to be slow and a machine Mercator guessed about.
//
// The last two are lawful for reasons of their own. A measured start latency for this offer is a
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
	// The same forty gigabytes on a machine that enumerated its copies and holds
	// none of them. The byte count is a fact here, which is what leaves the pair
	// of refusals below turning on the rate alone.
	enumeratedDataset := []domain.ArtifactEvidence{{
		ArtifactID: "artifact:imagenet:v2.41",
		Locality:   domain.LocalityCold,
		FetchBytes: 40_000_000_000,
	}}
	readAt := func(rate domain.TransferRate) domain.CandidateDecision {
		return domain.CandidateDecision{
			ImageLocality:    domain.LocalityHot,
			ArtifactEvidence: enumeratedDataset,
			TransferRates:    []domain.TransferRate{rate},
			Estimates: domain.CandidateEstimates{
				Stages:                  domain.LaunchStageEstimates{ArtifactFetch: domain.Estimate{Expected: 640}},
				StartSeconds:            domain.Estimate{Expected: 641, P90: 961},
				EstablishedStartSeconds: domain.Estimate{Expected: 641, P90: 961},
			},
		}
	}
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
					Stages: domain.LaunchStageEstimates{
						ImageFetch:    domain.Estimate{Expected: 289},
						ArtifactFetch: domain.Estimate{Expected: 640},
					},
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
					Stages: domain.LaunchStageEstimates{
						ImageFetch:    domain.Estimate{Expected: 289},
						ArtifactFetch: domain.Estimate{Expected: 640},
					},
					StartSeconds:            domain.Estimate{Expected: 930, P90: 1394},
					EstablishedStartSeconds: domain.Estimate{Expected: 930, P90: 1394},
				},
			},
		},
		{
			name: "a guess about the path counted as established",
			candidate: readAt(domain.TransferRate{
				Stage:      domain.StageArtifactFetch,
				Scope:      domain.NetworkScopeObjectStore,
				Mbps:       domain.DefaultObjectStoreDownloadMbps,
				Bytes:      40_000_000_000,
				Confidence: domain.AssumedLinkConfidence,
				Assumption: domain.AssumptionObjectStoreRate,
			}),
		},
		{
			name: "a path this machine measured itself",
			candidate: readAt(domain.TransferRate{
				Stage:       domain.StageArtifactFetch,
				Scope:       domain.NetworkScopeObjectStore,
				Mbps:        domain.DefaultObjectStoreDownloadMbps,
				Bytes:       40_000_000_000,
				Confidence:  0.9,
				Measurement: "node_artifact_copy",
			}),
			lawful: true,
		},
		{
			name: "a start latency measured on this offer",
			candidate: domain.CandidateDecision{
				ImageLocality:    domain.LocalityUnknown,
				ArtifactEvidence: unreadableDataset,
				Estimates: domain.CandidateEstimates{
					Stages: domain.LaunchStageEstimates{
						ImageFetch:    domain.Estimate{Expected: 289},
						ArtifactFetch: domain.Estimate{Expected: 640},
					},
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
					Stages:                  domain.LaunchStageEstimates{ArtifactFetch: domain.Estimate{Expected: 640}},
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
					Policy:               domain.PlacementPolicy{Class: domain.ClassStandard, MaxP90StartSeconds: 180},
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

// TestAScoreOffTheOfferIsNotReproducibleFromTheRecord is the reproducibility rule
// shown catching the thing it exists for, and leaving alone the thing it does not.
//
// The unlawful decision is scored on a term whose input is nowhere in it: a full
// point of doubt for a machine that could not enumerate its images, read off the
// offer the way the reference model used to read it. Its confidences say the
// answers it was given were worth half a point, so the record derives 0.30 USD of
// doubt and the score claims 0.90. That is exactly how the two definitions of
// uncertainty drifted: nothing in the record could tell them apart, and nothing
// could tell them apart while both were multiplied by zero.
//
// The lawful decision beside it is the same machine scored on the confidences it
// recorded, which is the honest version of the same placement.
func TestAScoreOffTheOfferIsNotReproducibleFromTheRecord(t *testing.T) {
	for _, decision := range []struct {
		name     string
		scoreUSD float64
		lawful   bool
	}{
		{"a score derived from the confidences the candidate recorded", 0.643333, true},
		{"a score charged a point for an inventory nobody recorded", 1.243333, false},
	} {
		t.Run(decision.name, func(t *testing.T) {
			observation := InvariantObservation{
				MercatorEvents: []eventlog.CloudEvent{borrowedHostScoredAt(decision.scoreUSD)},
			}

			err := scoreIsReproducibleFromTheRecord(observation)

			if decision.lawful && err != nil {
				t.Fatalf("%s was called a violation: %v", decision.name, err)
			}
			if !decision.lawful && err == nil {
				t.Fatalf("%s raised nothing", decision.name)
			}
		})
	}
}

// TestDoubtIsChargedAboutOnlyWhatTheScoreReads is the doubt rule shown catching
// the thing it exists for, at the same number the reproducibility rule beside it
// finds nothing wrong with.
//
// Every case here charges one tenth of a point. What separates them is the
// question that tenth was about: a stage of the launch is a term of the start the
// score multiplies by a rate, and a published risk history is a fact this model
// prices nowhere, so charging for the latter is a charge for having answered.
// The capacity claim is here too, because a rule that only ever fires would be
// as useless as one that never does.
func TestDoubtIsChargedAboutOnlyWhatTheScoreReads(t *testing.T) {
	for _, answer := range []struct {
		name   string
		lawful bool
	}{
		{domain.AnswerCapacity, true},
		{domain.StageImageFetch.ConfidenceAnswer(), true},
		{domain.StageApplicationReady.ConfidenceAnswer(), true},
		{"reliability", false},
		{"pull_seconds", false},
	} {
		t.Run(answer.name, func(t *testing.T) {
			observation := InvariantObservation{
				MercatorEvents: []eventlog.CloudEvent{bookingDecidedEvent("evt_doubt", domain.BookingDecision{
					ID:    "dec_doubt",
					RunID: "run-doubtful",
					Candidates: []domain.CandidateDecision{{
						OfferSnapshotID: "ask-steady",
						Feasible:        true,
						Confidences:     []domain.Confidence{{Answer: answer.name, Value: 0.9}},
					}},
				})},
			}

			err := doubtOnlyTheAnswersTheScoreReads(observation)

			if answer.lawful && err != nil {
				t.Fatalf("doubt about %q was called a violation: %v", answer.name, err)
			}
			if !answer.lawful && err == nil {
				t.Fatalf("doubt about %q raised nothing", answer.name)
			}
		})
	}
}

// borrowedHostScoredAt is one interactive placement on a machine nothing could
// ask: 0.33 USD of rent, one second of launch at a hundredth of a dollar, and a
// transfer estimate worth half of certainty.
func borrowedHostScoredAt(scoreUSD float64) eventlog.CloudEvent {
	return bookingDecidedEvent("evt_score_record", domain.BookingDecision{
		ID:      "dec_score_record",
		RunID:   "run-hurried",
		Policy:  domain.PlacementPolicy{Class: domain.ClassInteractive, ExpectedRuntimeSeconds: 600},
		Weights: domain.ClassInteractive.Weights(),
		Candidates: []domain.CandidateDecision{{
			OfferSnapshotID: "host-unaskable",
			Feasible:        true,
			ImageLocality:   domain.LocalityUnknown,
			Estimates: domain.CandidateEstimates{
				Stages:       domain.LaunchStageEstimates{ImageFetch: domain.Estimate{Expected: 289.14, Confidence: 0.5}},
				StartSeconds: domain.Estimate{Expected: 1},
				CostUSD:      domain.Estimate{Expected: 0.333333},
			},
			Confidences: []domain.Confidence{
				{Answer: domain.AnswerCapacity, Value: 1},
				{Answer: domain.StageImageFetch.ConfidenceAnswer(), Value: 0.5},
			},
			ScoreUSD: scoreUSD,
		}},
		SelectionReasonCodes: []string{"FEASIBLE", domain.ClassInteractive.SelectionReason()},
	})
}

// scoredOnATermReadOffTheOffer is the decision the reproducibility rule forbids.
func scoredOnATermReadOffTheOffer() eventlog.CloudEvent {
	return borrowedHostScoredAt(1.243333)
}

// doubtedAboutARateNothingPrices is the decision the doubt rule forbids: a
// machine whose provider published a risk history, charged for the confidence it
// stated it at, on a model that prices no refusal. The candidate beside it is the
// same placement with the same doubt about a duration the score does multiply,
// which is what makes the rule a statement about the answer rather than about the
// number.
func doubtedAboutARateNothingPrices() eventlog.CloudEvent {
	return bookingDecidedEvent("evt_reliability_doubt", domain.BookingDecision{
		ID:      "dec_reliability_doubt",
		RunID:   "run-measured",
		Policy:  domain.PlacementPolicy{Class: domain.ClassStandard, ExpectedRuntimeSeconds: 600},
		Weights: domain.ClassStandard.Weights(),
		Candidates: []domain.CandidateDecision{{
			OfferSnapshotID: "ask-steady",
			Feasible:        true,
			Reliability: domain.ReliabilityEvidence{
				StartFailures: domain.StatedRate{Rate: 0, Confidence: 0.9},
			},
			Confidences: []domain.Confidence{
				{Answer: domain.AnswerCapacity, Value: 1},
				{Answer: "reliability", Value: 0.9},
			},
		}},
		SelectionReasonCodes: []string{"FEASIBLE", domain.ClassStandard.SelectionReason()},
	})
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

// launchPredictingEveryStage is a Booking Decision that predicted the whole
// waterfall, which is what makes a missing actual the only thing left to catch.
func launchPredictingEveryStage(runID string) eventlog.CloudEvent {
	stages := domain.LaunchStageEstimates{}
	for _, stage := range domain.LaunchStages {
		predicted := domain.Estimate{Expected: 10, P50: 10, P90: 15, Source: "test"}
		switch stage {
		case domain.StageAcquisition:
			stages.Acquisition = predicted
		case domain.StageBoot:
			stages.Boot = predicted
		case domain.StageAgentReady:
			stages.AgentReady = predicted
		case domain.StageImageFetch:
			stages.ImageFetch = predicted
		case domain.StageUnpack:
			stages.Unpack = predicted
		case domain.StageArtifactFetch:
			stages.ArtifactFetch = predicted
		case domain.StageContainerStart:
			stages.ContainerStart = predicted
		case domain.StageApplicationReady:
			stages.ApplicationReady = predicted
		}
	}
	return bookingDecidedEvent("evt_waterfall", domain.BookingDecision{
		ID:                      "dec_waterfall",
		RunID:                   runID,
		SelectedOfferSnapshotID: "rental-warm",
		Candidates: []domain.CandidateDecision{{
			OfferSnapshotID: "rental-warm",
			Feasible:        true,
			Estimates:       domain.CandidateEstimates{Stages: stages},
		}},
	})
}

// TestALaunchThatMeasuredNothingIsNotSilentlyExempt is the launch the rule used to
// skip. Reading the accepted launches off the durations they reported meant a
// launch whose consequence carried no stage_seconds at all was not a launch as far
// as the law was concerned, so the eight predictions Mercator wrote were exported
// against nothing while every invariant passed. That is the shape a launch path
// with no stage accounting would take, which is what the node lane and a
// provisioned agent's bootstrap are.
func TestALaunchThatMeasuredNothingIsNotSilentlyExempt(t *testing.T) {
	observation := startRuleObservation([]eventlog.CloudEvent{launchPredictingEveryStage("run-waterfall")})
	observation.Effects = []EffectRecord{launchSpendingNoStage("run-waterfall")}

	result := invariantResultByID(t,
		DefaultInvariantRegistry().Evaluate(observation),
		"safety.prediction_is_recorded_against_its_actual",
	)

	if result.Status != InvariantFailed || result.Violation == "" {
		t.Fatalf("a launch that measured no stage at all was reported as recorded against its actual: %+v", result)
	}
}

// TestALaunchThatMeasuredNothingNamesTheAbsenceInTheBundle is the record half of
// the same thing. A row whose actual is zero because nothing timed the stage and a
// row whose actual is zero because the stage was instant are opposite facts, and a
// calibration reading the first as a measurement would train on it.
func TestALaunchThatMeasuredNothingNamesTheAbsenceInTheBundle(t *testing.T) {
	waterfall, err := stageWaterfalls(
		[]EffectRecord{launchSpendingNoStage("run-waterfall")},
		[]eventlog.CloudEvent{launchPredictingEveryStage("run-waterfall")},
	)
	if err != nil {
		t.Fatalf("read the waterfall: %v", err)
	}

	for _, row := range waterfall.records("run-waterfall") {
		if row.ActualSource != "launch_reported_no_actual" {
			t.Fatalf("%s is sourced %q at %.2fs, and this launch reported no stage duration at all",
				row.Metric, row.ActualSource, row.ActualSeconds)
		}
	}
}

// launchSpendingNoStage is the world's own account of a launch that reported no
// stage duration at all, which is what a launch path with no stage accounting on it
// leaves in the ledger.
func launchSpendingNoStage(runID string) EffectRecord {
	record := launchSpendingEveryStageBut(runID, "")
	record.Consequence = mustJSON(map[string]any{"external_id": "lab-" + runID})
	return record
}

// launchSpendingEveryStageBut is the world's own account of a launch with one
// stage left out of it.
func launchSpendingEveryStageBut(runID string, omitted domain.LaunchStage) EffectRecord {
	spent := map[string]float64{}
	for _, stage := range domain.LaunchStages {
		if stage == omitted {
			continue
		}
		spent[string(stage)] = 12
	}
	return EffectRecord{
		Sequence:      1,
		Operation:     OperationProviderLaunch,
		OperationID:   "launch-waterfall",
		Command:       EffectCommandAccepted,
		Response:      EffectResponseDelivered,
		CorrelationID: runID,
		Consequence:   mustJSON(map[string]any{"stage_seconds": spent}),
	}
}

// pricedAtARate is a Booking Decision whose one candidate was charged a transfer
// at one rate, answered one estimate over it, and told its own score what that
// answer was worth. It is the whole input to the attribution rule: the rule reads
// what a decision recorded about where a rate came from and what it then claimed
// the answer was worth, and nothing else.
//
// The estimate is filed under the read, because that is the stage the cases about
// what a guess is worth price, and a decision recording a rate for one stage and
// an answer for another would be a record the scheduler cannot write.
//
// The doubt the score was charged is stated separately from the estimate rather
// than copied off it, because production states it separately: the two are built
// by different code and a case that could not tell them apart could not reach the
// route where only the second one lies. A confidence of zero is left out of the
// list entirely, which is what production does with an answer nobody stated one
// for.
func pricedAtARate(at time.Time, rate domain.TransferRate, read domain.Estimate, scored float64) eventlog.CloudEvent {
	var charged []domain.Confidence
	if scored > 0 {
		charged = []domain.Confidence{{Answer: rate.Stage.ConfidenceAnswer(), Value: scored}}
	}
	return bookingDecidedEvent("evt_transfer_rate", domain.BookingDecision{
		ID:          "dec_transfer_rate",
		RunID:       "run-reader",
		EvaluatedAt: at,
		Candidates: []domain.CandidateDecision{{
			OfferSnapshotID: "rental-warm",
			Feasible:        true,
			TransferRates:   []domain.TransferRate{rate},
			Estimates: domain.CandidateEstimates{
				Stages: domain.LaunchStageEstimates{ArtifactFetch: read},
			},
			Confidences: charged,
		}},
		SelectedOfferSnapshotID: "rental-warm",
		SelectionReasonCodes:    []string{"FEASIBLE"},
	})
}

// measuredByNobody is a rate a decision presents as measured on a machine that
// published no such fact. It is the record a prediction slice reaching for a
// faster answer would write.
func measuredByNobody() domain.TransferRate {
	return domain.TransferRate{
		Stage:       domain.StageArtifactFetch,
		Scope:       domain.NetworkScopeObjectStore,
		Mbps:        4000,
		Bytes:       40_000_000_000,
		Confidence:  0.9,
		Measurement: "somebody",
	}
}

// classedWorkload is the one thing an admission rule reads a workload for: the
// class its caller declared it to be.
func classedWorkload(class domain.ServiceClass) domain.WorkloadRevision {
	return domain.WorkloadRevision{Spec: domain.WorkloadSpec{Placement: domain.PlacementPolicy{Class: class}}}
}

// admissionDeferredEvent is Mercator telling a Run to wait, as the public log
// carries it.
func admissionDeferredEvent(runID string, at time.Time, class domain.ServiceClass) eventlog.CloudEvent {
	data, err := json.Marshal(struct {
		Deferral domain.AdmissionDeferral `json:"deferral"`
	}{domain.AdmissionDeferral{Reason: domain.DeferredNoFeasibleOffer, Class: class}})
	if err != nil {
		panic(err)
	}
	return eventlog.CloudEvent{
		ID:      "deferred-" + runID,
		Type:    orchestrator.EventAdmissionDeferred,
		Subject: "runs/" + runID,
		Time:    at.Format(time.RFC3339Nano),
		Data:    data,
	}
}

// admittedDecisionEvent is a Booking Decision that selected something, which is
// the moment a Run leaves the queue.
func admittedDecisionEvent(runID string, at time.Time) eventlog.CloudEvent {
	event := bookingDecidedEvent("decided-"+runID, domain.BookingDecision{
		RunID:                   runID,
		SelectedOfferSnapshotID: "offer-1",
	})
	event.Subject = "runs/" + runID
	event.Time = at.Format(time.RFC3339Nano)
	return event
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

// gpuOffer is one marketplace ask for a machine holding cards, stated as the world
// knows it rather than as a key summarises it.
func gpuOffer(id string, cards int) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:          id,
		NativeRef:   id,
		AdapterType: "simvast",
		Region:      "US-CA",
		Kind:        domain.OfferKindProvisionable,
		Lane:        domain.LaneEphemeral,
		Resources: domain.ResourceInventory{
			Accelerators: []domain.AcceleratorInventory{{
				Vendor: "NVIDIA", Model: "A100", CanonicalModel: "nvidia-a100",
				Count: cards, MemoryBytes: 80_000_000_000,
			}},
		},
	}
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

// TestAHostRunningAheadIsRefusedThroughTheWholeLabWorld drives the skewed-clock
// Blueprint through the Lab's own world rather than through hand-built events. It is
// the half the unit cases above cannot cover: those state a record and ask the rule
// about it, and this one puts a machine whose clock runs an hour ahead into a world,
// lets the control plane observe it, and asks what the record then says.
//
// Nothing observed this container start on a clock Mercator shares, so the run
// stream records no start and the start-latency row names the absence rather than
// filing an hour as a measurement. Every standing law still holds, which is the
// second assertion: refusing a moment is not a violation of the rule that a start
// must be observed.
//
// The Booking's clock reaches this world only as the fallback. This provider says
// running from the moment it accepts a launch, so the first observation the control
// plane gets carries no start moment at all and the schedule is measured from that
// read, which is the case the rule has to allow. The lane where a start arrives with
// the first running observation is the reusable one, and the fleet case
// TestANodeWithASkewedClockDoesNotSetMercatorsOwn is where adopting it is shown to
// cost an hour of paid capacity.
func TestAHostRunningAheadIsRefusedThroughTheWholeLabWorld(t *testing.T) {
	execution := openBlueprintExecution(t, "../scenario/scenarios/conformance/a-clock-nobody-shares-measures-nothing.json", testLimits())
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.Drive(context.Background(), Quiesce()); err != nil {
		t.Fatalf("drive arrivals: %v", err)
	}
	if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
		t.Fatalf("advance execution: %v", err)
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("check invariants: %v", err)
	}

	for _, result := range latestInvariantResults(execution.invariants) {
		if result.Status != InvariantPassed {
			t.Fatalf("a host with a skewed clock made a law fail: %+v", result)
		}
	}
	for _, record := range startLatencyRows(t, execution) {
		if record.ActualSource != "start_not_observed" {
			t.Fatalf("the start-latency row is sourced %q with %.2fs, and this host's clock is an hour ahead of Mercator's",
				record.ActualSource, record.ActualSeconds)
		}
	}
	// The application on that host reads the same clock, so its readiness is an hour
	// in Mercator's future as well. The record has to carry the absence rather than
	// the hour, and the projection is where the refusal shows.
	for _, run := range labRunRecords(t, execution) {
		if run.ReadyAt != nil {
			t.Fatalf("Run %q records an application readiness of %s, stated by a workload whose host runs an hour ahead of Mercator",
				run.ID, run.ReadyAt.Format(time.RFC3339Nano))
		}
	}
}

// labRunRecords is every Run in this execution as Mercator's own read model has it,
// which is where the moments the control plane adopted live.
func labRunRecords(t *testing.T, execution *Execution) []domain.RunRecord {
	t.Helper()
	records, err := execution.runtime.allRuns(context.Background())
	if err != nil {
		t.Fatalf("read run records: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("this execution recorded no Run at all")
	}
	return records
}

// startLatencyRows is every Run's start-latency row in this execution's Run
// Bundle, which is the record a calibration would read.
func startLatencyRows(t *testing.T, execution *Execution) []predictionActualRecord {
	t.Helper()
	mercatorEvents, effects, _, err := execution.bundleRuntimeData(context.Background())
	if err != nil {
		t.Fatalf("read bundle data: %v", err)
	}
	records, err := predictionActualRecords(execution.config.Tape, effects, mercatorEvents)
	if err != nil {
		t.Fatalf("build prediction records: %v", err)
	}
	rows := make([]predictionActualRecord, 0, len(records))
	for _, record := range records {
		if record.Metric == "start_latency_seconds" {
			rows = append(rows, record)
		}
	}
	if len(rows) == 0 {
		t.Fatal("this execution recorded no start-latency row at all")
	}
	return rows
}
