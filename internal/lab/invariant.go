package lab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/janitor"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scenario"
)

type InvariantStatus string

const (
	InvariantPassed InvariantStatus = "passed"
	InvariantFailed InvariantStatus = "failed"
)

type InvariantResult struct {
	ID               string          `json:"id"`
	Status           InvariantStatus `json:"status"`
	CheckedAt        time.Time       `json:"checked_at"`
	Transition       uint64          `json:"transition"`
	Assumptions      []string        `json:"assumptions,omitempty"`
	VirtualTimeBound string          `json:"virtual_time_bound,omitempty"`
	Violation        string          `json:"violation,omitempty"`
}

type InvariantObservation struct {
	StartedAt      time.Time
	Now            time.Time
	Transition     uint64
	Blueprint      string
	World          WorldTruthSnapshot
	MercatorEvents []eventlog.CloudEvent
	Effects        []EffectRecord
	Runs           []domain.RunRecord
	// Workloads is what Mercator recorded it was asked to run, by Run ID, read
	// back out of its own public event log. Rules about Mercator's decisions
	// read this rather than the World Tape's arrivals: the world knows what a
	// process really does, and a rule that read it would be checking the world
	// against itself.
	Workloads       map[string]domain.WorkloadRevision
	RentalSchedules map[string]domain.RentalSchedule
	RunRequirements map[string]RunArrival
	// ArtifactCatalog is what the object store says each Artifact version is
	// and when it became durable.
	ArtifactCatalog map[string]domain.ArtifactVersion
	// SeededLocality is the image content the World Tape put on each host
	// before any Run executed, keyed by offer. Content outside it has to be
	// explained by an accepted image pull against that same host.
	SeededLocality map[string]map[string]bool
	// SeededReplicas is the Artifact copies the World Tape put on each host
	// before any Run executed, keyed by offer. A copy outside it has to be
	// explained by content the ledger says landed there.
	SeededReplicas map[string]map[string]bool
	// Prewarm is what this world allows the control plane to have in flight for
	// work it has not admitted. A world that states none allows none, and the
	// concurrency rule has nothing to say about it.
	Prewarm *scenario.PrewarmSpec
	// SeededOrphans is the capacity this world began holding that Mercator never
	// launched. A rule about the orphan policy reads it rather than the fleet as it
	// stands, because the interesting case is capacity that is no longer here: a
	// machine converged without a stated rule leaves nothing behind to ask about.
	SeededOrphans map[string]bool
	// BootstrapCredentials is every enrollment token this world handed a machine
	// and what became of it. It is the only thing in an observation that carries
	// live credential material, and it carries it because the rules about a
	// bootstrap are asked in its terms: how many machines held it, how many times
	// it was redeemed, and whether it turns up anywhere in Mercator's own record.
	// It is built in memory for one evaluation and exported nowhere.
	BootstrapCredentials []bootstrapCredential
	// ContentCredentials is every credential this world watched Mercator hand a
	// machine so it could fetch one image or one Artifact, beside what the command
	// it arrived on was really for. It carries live material for the reason the
	// bootstraps do: the rule about it asks whether one fetch's material was ever
	// presented for another, and only the material itself can answer that.
	ContentCredentials          []contentCredential
	ProjectionRebuildEquivalent bool
}

type Invariant interface {
	ID() string
	Assumptions() []string
	VirtualTimeBound() time.Duration
	Check(InvariantObservation) error
}

type InvariantRegistry struct {
	invariants []Invariant
}

func NewInvariantRegistry(invariants ...Invariant) (InvariantRegistry, error) {
	seen := map[string]bool{}
	for _, invariant := range invariants {
		if invariant == nil || invariant.ID() == "" {
			return InvariantRegistry{}, fmt.Errorf("Lab invariants need a stable ID")
		}
		if seen[invariant.ID()] {
			return InvariantRegistry{}, fmt.Errorf("duplicate Lab invariant %q", invariant.ID())
		}
		seen[invariant.ID()] = true
	}
	return InvariantRegistry{invariants: slices.Clone(invariants)}, nil
}

func DefaultInvariantRegistry() InvariantRegistry {
	registry, err := NewInvariantRegistry(
		invariantRule{id: "safety.no_duplicate_active_execution", check: noDuplicateActiveExecution},
		invariantRule{id: "safety.exclusive_booking_capacity", check: exclusiveBookingCapacity},
		invariantRule{id: "safety.monotonic_terminal_state", check: monotonicTerminalState},
		invariantRule{id: "safety.start_is_observed_not_inferred", check: startIsObservedNotInferred},
		invariantRule{id: "safety.readiness_is_reported_not_inferred", check: readinessIsReportedNotInferred},
		invariantRule{id: "safety.prediction_is_recorded_against_its_actual", check: predictionIsRecordedAgainstItsActual},
		invariantRule{id: "safety.candidate_identity_recurs", check: candidateIdentityRecurs},
		invariantRule{id: "safety.prediction_states_its_provenance", check: predictionStatesItsProvenance},
		invariantRule{id: "safety.idempotent_external_commands", check: idempotentExternalCommands},
		invariantRule{id: "safety.lease_fencing", check: leaseFencing},
		invariantRule{id: "safety.artifact_dependencies", check: artifactDependencies},
		invariantRule{id: "safety.artifact_replica_verified", check: artifactReplicaVerified},
		invariantRule{id: "safety.monotonic_versions", check: monotonicVersions},
		invariantRule{id: "safety.owned_external_resources", check: ownedExternalResources},
		invariantRule{id: "safety.disk_reservation_respected", check: diskReservationRespected},
		invariantRule{id: "safety.cache_mount_workspace_isolation", check: cacheMountWorkspaceIsolation},
		invariantRule{id: "safety.projection_rebuild_equivalence", check: projectionRebuildEquivalence},
		invariantRule{id: "safety.secrets_absent", check: secretsAbsent},
		invariantRule{
			id:    "safety.bootstrap_credential_is_short_lived_and_single_use",
			check: bootstrapCredentialIsShortLivedAndSingleUse,
		},
		invariantRule{
			id:    "safety.content_credentials_are_scoped_and_expiring",
			check: contentCredentialsAreScopedAndExpiring,
		},
		invariantRule{id: "safety.ephemeral_capacity_not_reused", check: ephemeralCapacityNotReused},
		invariantRule{id: "safety.reusable_capacity_has_an_enrolled_runtime", check: reusableCapacityHasAnEnrolledRuntime},
		invariantRule{id: "safety.a_rental_identity_is_capacity_mercator_holds", check: aRentalIdentityIsCapacityMercatorHolds},
		invariantRule{id: "safety.enrolment_names_the_generation_it_was_invited_for", check: enrolmentNamesTheGenerationItWasInvitedFor},
		invariantRule{id: "safety.locality_provenance", check: localityProvenance},
		invariantRule{id: "safety.transfer_rate_is_attributed", check: transferRateIsAttributed},
		invariantRule{id: "safety.locality_is_never_infeasibility", check: localityIsNeverInfeasibility},
		invariantRule{id: "safety.score_is_reproducible_from_the_record", check: scoreIsReproducibleFromTheRecord},
		invariantRule{id: "safety.doubt_only_the_answers_the_score_reads", check: doubtOnlyTheAnswersTheScoreReads},
		invariantRule{id: "safety.promised_start_is_still_ahead", check: promisedStartIsStillAhead},
		invariantRule{id: "safety.prewarm_yields_to_real_work", check: prewarmYieldsToRealWork},
		invariantRule{id: "safety.prewarm_rate_within_bound", check: prewarmRateWithinBound},
		invariantRule{id: "safety.service_class_admission_order", check: serviceClassAdmissionOrder},
		invariantRule{id: "safety.class_bounds_honoured", check: classBoundsHonoured},
		invariantRule{id: "safety.nothing_waits_behind_an_impossible_ask", check: nothingWaitsBehindAnImpossibleAsk},
		invariantRule{id: "safety.a_silence_is_not_an_answer_about_capacity", check: aSilenceIsNotAnAnswerAboutCapacity},
		invariantRule{id: "safety.group_parallelism_respected", check: groupParallelismRespected},
		invariantRule{id: "safety.interruption_was_permitted", check: interruptionWasPermitted},
		invariantRule{id: "safety.no_capacity_is_free", check: noCapacityIsFree},
		invariantRule{id: "safety.committed_cost_is_not_double_counted", check: committedCostIsNotDoubleCounted},
		invariantRule{id: "safety.decisions_are_never_rewritten", check: decisionsAreNeverRewritten},
		invariantRule{id: "safety.orphan_policy_is_explicit", check: orphanPolicyIsExplicit},
		invariantRule{id: "safety.decision_is_reproducible", check: decisionIsReproducible},
		invariantRule{
			id:          "liveness.lost_response_reconciliation",
			assumptions: []string{"the provider preserves operation identity", "provider observation remains available"},
			bound:       5 * time.Minute,
			check:       lostResponseReconciliation,
		},
		invariantRule{
			id: "liveness.provisioned_capacity_enrolls_or_is_reclaimed",
			assumptions: []string{
				"virtual time advances",
				"the provider records every allocation it accepted",
			},
			bound: provisionedCapacityBound,
			check: provisionedCapacityEnrolsOrIsReclaimed,
		},
		invariantRule{
			id:          "liveness.stale_lease_expiry",
			assumptions: []string{"virtual time advances", "provider execution deadlines are observable"},
			bound:       5 * time.Minute,
			check:       staleLeaseExpiry,
		},
		invariantRule{
			id:          "liveness.orphan_convergence",
			assumptions: []string{"provider ownership listing is complete"},
			bound:       5 * time.Minute,
			check:       orphanConvergence,
		},
		invariantRule{
			id:          "liveness.superseded_booking_release",
			assumptions: []string{"Rental Schedule commits remain available"},
			bound:       5 * time.Minute,
			check:       supersededBookingRelease,
		},
		invariantRule{
			id: "liveness.prefetch_converges",
			assumptions: []string{
				"virtual time advances",
				"the registry and object store this content is served from keep answering",
			},
			bound: prefetchConvergenceBound,
			check: prefetchConverges,
		},
		invariantRule{
			id:          "liveness.admitted_run_progress",
			assumptions: []string{"provider observations remain available", "actual runtime is bounded by the World Tape"},
			bound:       24 * time.Hour,
			check:       admittedRunProgress,
		},
		invariantRule{
			id: "liveness.aging_prevents_starvation",
			// The list is what the rule is allowed to assume rather than a summary of
			// it, in the shape liveness.prefetch_converges states its own. The last
			// two are what the second half of this rule rests on: a refusal that says
			// nothing about the fleet cannot be told apart from a fleet too small, and
			// the promise that half a bound of waiting outranks any arrival is what
			// makes younger admitted work a violation rather than a policy choice.
			assumptions: []string{
				"virtual time advances",
				"capacity that could hold the Run eventually frees",
				"a wait names the fleet it was last measured against",
				"each class ages above every arriving class within half its own maximum queue delay",
			},
			// The longest wait any class declares, which is two hours, and well
			// inside the twenty four hours admitted_run_progress already holds every
			// execution to. A bound past that one would have lengthened every
			// execution in the tree to state a rule about the queue.
			bound: longestClassQueueDelay(),
			check: agingPreventsStarvation,
		},
	)
	if err != nil {
		panic(err)
	}
	return registry
}

func (registry InvariantRegistry) Empty() bool {
	return len(registry.invariants) == 0
}

// longestBound is the furthest into an execution any rule here is stated
// against. A driver that ended an execution before it would stop at a moment
// no liveness rule can speak about yet, and report as passing what it never
// gave the registry the chance to judge.
func (registry InvariantRegistry) longestBound() time.Duration {
	var longest time.Duration
	for _, invariant := range registry.invariants {
		longest = max(longest, invariant.VirtualTimeBound())
	}
	return longest
}

func (registry InvariantRegistry) Evaluate(observation InvariantObservation) []InvariantResult {
	results := make([]InvariantResult, 0, len(registry.invariants))
	for _, invariant := range registry.invariants {
		result := InvariantResult{
			ID:          invariant.ID(),
			Status:      InvariantPassed,
			CheckedAt:   observation.Now,
			Transition:  observation.Transition,
			Assumptions: slices.Clone(invariant.Assumptions()),
		}
		if bound := invariant.VirtualTimeBound(); bound > 0 {
			result.VirtualTimeBound = bound.String()
		}
		if err := invariant.Check(observation); err != nil {
			result.Status = InvariantFailed
			result.Violation = err.Error()
		}
		results = append(results, result)
	}
	return results
}

type InvariantViolationError struct {
	Result InvariantResult
}

func (err *InvariantViolationError) Error() string {
	return fmt.Sprintf("Lab invariant %q failed: %s", err.Result.ID, err.Result.Violation)
}

type invariantRule struct {
	id          string
	assumptions []string
	bound       time.Duration
	check       func(InvariantObservation) error
}

func (rule invariantRule) ID() string                      { return rule.id }
func (rule invariantRule) Assumptions() []string           { return slices.Clone(rule.assumptions) }
func (rule invariantRule) VirtualTimeBound() time.Duration { return rule.bound }
func (rule invariantRule) Check(observation InvariantObservation) error {
	return rule.check(observation)
}

func noDuplicateActiveExecution(observation InvariantObservation) error {
	activeByRun := map[string]string{}
	for _, execution := range observation.World.ActiveExecutions {
		if launchKey := activeByRun[execution.RunID]; launchKey != "" && launchKey != execution.LaunchKey {
			return fmt.Errorf("Run %q has active launches %q and %q", execution.RunID, launchKey, execution.LaunchKey)
		}
		activeByRun[execution.RunID] = execution.LaunchKey
	}
	return nil
}

func exclusiveBookingCapacity(observation InvariantObservation) error {
	for rentalID, schedule := range observation.RentalSchedules {
		running := 0
		var previousVersion uint64
		for index, scheduled := range schedule.Bookings {
			booking := scheduled.Booking
			if booking.ScheduleVersion == 0 ||
				booking.ScheduleVersion > schedule.Version ||
				booking.ScheduleVersion < previousVersion {
				return fmt.Errorf(
					"Rental %q Booking %q has nonmonotonic schedule version %d under %d",
					rentalID,
					booking.ID,
					booking.ScheduleVersion,
					schedule.Version,
				)
			}
			previousVersion = booking.ScheduleVersion
			switch booking.State {
			case domain.BookingStateRunning:
				running++
				if index != 0 {
					return fmt.Errorf("Rental %q has running Booking %q after queue head", rentalID, booking.ID)
				}
			case domain.BookingStateQueued:
				if index == 0 || booking.AfterBookingID != schedule.Bookings[index-1].Booking.ID {
					return fmt.Errorf("Rental %q queue chain is broken at Booking %q", rentalID, booking.ID)
				}
			default:
				return fmt.Errorf("Rental %q Booking %q has invalid state %q", rentalID, booking.ID, booking.State)
			}
		}
		if running > 1 {
			return fmt.Errorf("Rental %q has %d simultaneous exclusive Bookings", rentalID, running)
		}
		if len(schedule.Bookings) > domain.RentalScheduleQueueCapacity+1 {
			return fmt.Errorf("Rental %q exceeds queue capacity", rentalID)
		}
	}
	return nil
}

// predictionIsRecordedAgainstItsActual is ADR 0004's calibration requirement read
// over the whole launch waterfall rather than over one number. A launch the
// Effect Ledger accepted spent time on eight stages, and the record has to carry
// both halves of each: what Mercator predicted that stage would cost, and what it
// then cost.
//
// The two halves are read from independent places, which is what stops the rule
// being satisfied by the predictor agreeing with itself. The prediction comes off
// the Booking Decision Mercator wrote; the actual comes off the world's own launch
// consequence in the ledger. Six of the eight have no other source: Mercator can
// observe a container starting and an application reporting ready, and nothing in
// production tells it when a machine finished booting.
//
// It is deliberately not stated as accuracy. How close a prediction lands is a
// calibration metric, and a rule of that shape would fail on a fixture whose world
// is simply slow, which is a legitimate world and several of these fixtures are
// exactly that. What is a violation is a stage that happened and was predicted by
// nothing, or a stage the world spent and the record cannot name.
func predictionIsRecordedAgainstItsActual(observation InvariantObservation) error {
	waterfall, err := stageWaterfalls(observation.Effects, observation.MercatorEvents)
	if err != nil {
		return err
	}
	// Every launch the ledger accepted, and not only those that reported a duration
	// for something. Reading the accepted launches off the stage durations they
	// carried left the rule silent for exactly the launch it exists to catch: one
	// that measured nothing at all, whose eight predictions are then exported
	// against nothing while the law says it holds.
	for _, runID := range slices.Sorted(maps.Keys(waterfall.launched)) {
		spent := waterfall.actual[runID]
		predicted, decided := waterfall.predicted[runID]
		if !decided {
			return fmt.Errorf("Run %q had a launch accepted and no Booking Decision of its own to have predicted it", runID)
		}
		for _, stage := range domain.LaunchStages {
			seconds, simulated := spent[string(stage)]
			if !simulated && waterfall.unreached[runID][stage] {
				// A stage this launch never reached has nothing to measure, and the
				// world said so rather than leaving it to be read off the absence. A
				// workload that never comes up is the failure mode the readiness stage
				// exists to expose, and demanding an actual for it would make that
				// world unstatable.
				continue
			}
			if !simulated {
				return fmt.Errorf("Run %q launched and the ledger reports no %s actual, so that prediction is measured against nothing", runID, stage)
			}
			if predicted.Stage(stage).Source == "" {
				return fmt.Errorf("Run %q spent %.2fs on the %s stage and nothing in the record predicted it", runID, seconds, stage)
			}
		}
		for _, name := range slices.Sorted(maps.Keys(spent)) {
			if !slices.Contains(domain.LaunchStages, domain.LaunchStage(name)) {
				return fmt.Errorf("Run %q spent time on %q, which is not a stage any prediction can be recorded against", runID, name)
			}
		}
	}
	return nil
}

// startIsObservedNotInferred is the standing guard on the one thing that makes a
// stage duration learnable: its actual has to be a moment somebody observed.
//
// Four things hold of every Run in the log, and each is read from a different half
// of the record so no clause can be satisfied by Mercator agreeing with itself.
// What the run stream records as the moment this workload began must be a moment an
// observation of that same Run reported: not the moment the launch was accepted,
// which is when the machine started getting ready, and not the moment Mercator
// predicted, which is the thing being calibrated. It must be a moment one of those
// observations established, and this rule decides what that means in its own terms
// rather than by asking the production predicate: a moment ahead of the read that
// carried it is a prediction wearing an observation's clothes, and a moment carried
// by a phase saying the work has not begun is a provider calling a launch a start.
// A Run whose holder did establish a start must have it recorded, because a stage
// with an actual that reads as absent is a measurement thrown away. And the clock a
// Booking's runtime bounds are enforced from must be one of the same two things,
// because that clock decides when Mercator believes paid capacity came free.
//
// Restating the clauses is the point. Delegating them to
// adapter.ExternalObservation.EstablishedStart made this rule agree with whatever
// the control plane happened to decide, which is the shape
// safety.locality_is_never_infeasibility was already corrected for: an
// executable specification that asks production to confirm its own arithmetic
// constrains nothing. Deleting the clause about a moment ahead of its read now
// fails the world that publishes one.
//
// The published claim Mercator refused is not a violation of anything: the record
// keeps what the holder said, and this rule reads it the way the control plane was
// supposed to rather than blaming Mercator for declining a moment it could not
// defend.
//
// A Run with no start moment at all is not a violation. Acquisition and boot have
// no production observation until an agent bootstraps on provisioned capacity, and
// what the record must then say is that the stage is unobserved rather than that it
// took no time.
func startIsObservedNotInferred(observation InvariantObservation) error {
	looked, recorded, err := startMomentsByRun(observation)
	if err != nil {
		return err
	}
	for runID, moment := range recorded {
		looks, held := looked[runID]
		if !held {
			return fmt.Errorf(
				"Run %q records a start of %s that no observation of it reported",
				runID, moment.Format(time.RFC3339Nano),
			)
		}
		if !looks.established(moment) {
			return fmt.Errorf(
				"Run %q records a start of %s and its observations established %s",
				runID, moment.Format(time.RFC3339Nano), looks.describe(),
			)
		}
	}
	for runID, looks := range looked {
		if _, held := recorded[runID]; !held && looks.establishedAStart() {
			return fmt.Errorf(
				"Run %q was observed starting at %s and its run stream records no start moment",
				runID, looks.describe(),
			)
		}
	}
	return bookingClocksAreObserved(observation.RentalSchedules, looked)
}

// readinessIsReportedNotInferred is the same law over the last stage of a launch,
// which is the one stage whose actual comes from outside Mercator entirely. A
// provider, a node, and a container runtime can all see a process running and none
// of them can see whether it is serving, so the workload is the only authority and
// its report is the only source. That is exactly why the moment needs a rule: an
// authority Mercator cannot check is an authority Mercator cannot check.
//
// Four clauses, each read from a different half of the record. What the Run
// projection carries as its readiness must be a moment a readiness report on that
// same Run stated, so no readiness is Mercator's own arithmetic. It must be no later
// than the read that carried it, because a workload reads the clock of the host it
// runs on and a host an hour ahead reports an hour of ready latency nothing
// measured. It must not precede the start the same Run recorded, because an
// application cannot serve before the process serving it exists and the two moments
// come from two authorities that cannot see each other. And a Run whose report
// stated a moment satisfying both must carry it, because a readiness that reads as
// absent is the actual for this stage thrown away.
//
// The clauses are written out here rather than asked of the production predicate for
// the reason safety.start_is_observed_not_inferred states one rule up: a
// specification that asks the code to confirm its own arithmetic constrains nothing.
//
// A Run with no readiness at all is not a violation. A workload that never becomes
// ready is a world this corpus can state, and the record saying nothing about the
// stage is the honest answer for it.
func readinessIsReportedNotInferred(observation InvariantObservation) error {
	reported, err := readinessReportsByRun(observation)
	if err != nil {
		return err
	}
	for _, run := range observation.Runs {
		if run.ReadyAt == nil {
			continue
		}
		moment := run.ReadyAt.UTC()
		if !reported[run.ID].stated(moment) {
			return fmt.Errorf(
				"Run %q records an application readiness of %s that no report of it stated, and its reports said %s",
				run.ID, moment.Format(time.RFC3339Nano), reported[run.ID].describe(),
			)
		}
		if !reported[run.ID].defensible(moment) {
			return fmt.Errorf(
				"Run %q records an application readiness of %s that arrived ahead of the read carrying it, and its reports said %s",
				run.ID, moment.Format(time.RFC3339Nano), reported[run.ID].describe(),
			)
		}
	}
	return readinessFollowsItsContainer(observation, reported)
}

// readinessFollowsItsContainer is the pair of clauses that need the start moment
// beside the readiness: the ordering of the two stages, and the readiness a Run was
// told about and did not keep.
func readinessFollowsItsContainer(observation InvariantObservation, reported map[string]readinessClaims) error {
	_, started, err := startMomentsByRun(observation)
	if err != nil {
		return err
	}
	for _, run := range observation.Runs {
		startedAt, observed := started[run.ID]
		if run.ReadyAt != nil && observed && run.ReadyAt.UTC().Before(startedAt) {
			return fmt.Errorf(
				"Run %q records its application serving at %s and its container starting at %s",
				run.ID, run.ReadyAt.UTC().Format(time.RFC3339Nano), startedAt.Format(time.RFC3339Nano),
			)
		}
		if run.ReadyAt != nil {
			continue
		}
		for _, claim := range reported[run.ID] {
			if claim.defensible() && !(observed && claim.At.Before(startedAt)) {
				return fmt.Errorf(
					"Run %q was told its application was ready at %s by a report read at %s, and its record carries no readiness",
					run.ID, claim.At.Format(time.RFC3339Nano), claim.ReadAt.Format(time.RFC3339Nano),
				)
			}
		}
	}
	return nil
}

// readinessClaim is one readiness a workload stated and the moment Mercator read it
// stating so. Both halves travel together because the whole question about a foreign
// moment is how it compares with the read that carried it.
type readinessClaim struct {
	At     time.Time
	ReadAt time.Time
}

func (claim readinessClaim) defensible() bool { return !claim.At.After(claim.ReadAt) }

type readinessClaims []readinessClaim

func (claims readinessClaims) stated(moment time.Time) bool {
	return slices.ContainsFunc(claims, func(claim readinessClaim) bool { return claim.At.Equal(moment) })
}

func (claims readinessClaims) defensible(moment time.Time) bool {
	return slices.ContainsFunc(claims, func(claim readinessClaim) bool {
		return claim.At.Equal(moment) && claim.defensible()
	})
}

func (claims readinessClaims) describe() string {
	if len(claims) == 0 {
		return "nothing"
	}
	described := make([]string, 0, len(claims))
	for _, claim := range claims {
		described = append(described, fmt.Sprintf("%s (read at %s)",
			claim.At.Format(time.RFC3339Nano), claim.ReadAt.Format(time.RFC3339Nano)))
	}
	return strings.Join(described, ", ")
}

// readinessReportsByRun is every readiness a workload reported, by Run, with the
// moment Mercator appended each report. Reports of any other type carry no readiness
// and are not claims about one.
func readinessReportsByRun(observation InvariantObservation) (map[string]readinessClaims, error) {
	claims := map[string]readinessClaims{}
	for _, event := range observation.MercatorEvents {
		if event.Type != orchestrator.EventRunReported {
			continue
		}
		var payload struct {
			Type string `json:"type"`
			Data struct {
				ReadyAt time.Time `json:"ready_at"`
			} `json:"data"`
		}
		runID := strings.TrimPrefix(event.Subject, "runs/")
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return nil, fmt.Errorf("decode Run %q report: %w", runID, err)
		}
		if payload.Type != orchestrator.RunReportReady || payload.Data.ReadyAt.IsZero() {
			continue
		}
		readAt, err := time.Parse(time.RFC3339Nano, event.Time)
		if err != nil {
			return nil, fmt.Errorf("decode Run %q report time: %w", runID, err)
		}
		claims[runID] = append(claims[runID], readinessClaim{
			At:     payload.Data.ReadyAt.UTC(),
			ReadAt: readAt.UTC(),
		})
	}
	return claims, nil
}

// bookingClocksAreObserved holds the Rental Schedule to the same law as the run
// stream. A Booking's StartedAt is what its declared runtimes are measured from, so
// it decides when Mercator thinks a machine came free and when it thinks a workload
// has overrun: a moment from a host an hour ahead leaves the bound unexpired an
// hour after the capacity was really spent, with the schedule saying the machine is
// busy the whole time.
//
// Two moments are defensible. The container's own start where an observation
// established one, and the read that carried an observation of that Run, which is
// the last instant Mercator can prove the container was up. Anything else is a
// clock nobody here shares. This is the clause the run stream's version of the law
// was missing: the same append recorded no start and stamped this field from the
// same refused moment, and no rule in the corpus read the schedule.
func bookingClocksAreObserved(schedules map[string]domain.RentalSchedule, looked map[string]runLooks) error {
	for _, rentalID := range slices.Sorted(maps.Keys(schedules)) {
		for _, scheduled := range schedules[rentalID].Bookings {
			if scheduled.StartedAt.IsZero() {
				continue
			}
			runID := scheduled.Booking.RunID
			looks := looked[runID]
			if looks.established(scheduled.StartedAt.UTC()) || looks.read(scheduled.StartedAt.UTC()) {
				continue
			}
			return fmt.Errorf(
				"Rental %q measures Booking %q for Run %q from %s, and that Run's observations established %s",
				rentalID, scheduled.Booking.ID, runID,
				scheduled.StartedAt.Format(time.RFC3339Nano), looks.describe(),
			)
		}
	}
	return nil
}

// runLooks is every observation of one Run's workload the run stream recorded,
// kept whole so this rule can ask each one the question the control plane was
// supposed to ask it.
type runLooks []adapter.ExternalObservation

// established answers whether this is a moment one of these observations
// established. The three clauses are stated here, in the Lab's own terms, because
// a standing law that called the production predicate would pass whenever
// production and production agreed.
func (looks runLooks) established(moment time.Time) bool {
	for _, look := range looks {
		if established, ok := establishedStart(look); ok && established.Equal(moment) {
			return true
		}
	}
	return false
}

// establishedAStart answers whether any of these looks established one at all,
// which is what makes a missing record a measurement thrown away instead of a
// claim Mercator was right to decline.
func (looks runLooks) establishedAStart() bool {
	for _, look := range looks {
		if _, ok := establishedStart(look); ok {
			return true
		}
	}
	return false
}

// read answers whether this moment is one of the reads that carried these
// observations. It is the only moment other than an established start that a
// projection may be measured from, and it is Mercator's own clock on every seam
// that fills it in.
func (looks runLooks) read(moment time.Time) bool {
	for _, look := range looks {
		if look.ObservedAt.UTC().Equal(moment) {
			return true
		}
	}
	return false
}

func (looks runLooks) describe() string {
	if len(looks) == 0 {
		return "nothing"
	}
	described := make([]string, 0, len(looks))
	for _, look := range looks {
		described = append(described, fmt.Sprintf(
			"%s (%s, read at %s)",
			startedOrAbsent(look), look.Phase, look.ObservedAt.Format(time.RFC3339Nano),
		))
	}
	return strings.Join(described, ", ")
}

func startedOrAbsent(look adapter.ExternalObservation) string {
	if look.StartedAt == nil {
		return "no start"
	}
	return look.StartedAt.Format(time.RFC3339Nano)
}

// establishedStart is what this Lab holds a start moment to, spelled out rather
// than delegated: a moment the holder stated, about work it said had begun, no
// later than the read that carried it. The phases are named one by one for the same
// reason the comparison is written out here.
func establishedStart(look adapter.ExternalObservation) (time.Time, bool) {
	if look.StartedAt == nil || look.StartedAt.IsZero() || look.StartedAt.After(look.ObservedAt) {
		return time.Time{}, false
	}
	switch look.Phase {
	case adapter.ExternalPhaseRunning, adapter.ExternalPhaseSucceeded, adapter.ExternalPhaseFailed:
		return look.StartedAt.UTC(), true
	default:
		return time.Time{}, false
	}
}

// startMomentsByRun is every observation the run stream carried for each Run, and
// the start each Run's stream recorded. Observations that published no start moment
// are kept: the read that carried one is what a Booking's clock may fall back to,
// and a rule that dropped them could not tell that fallback from an invented
// moment.
func startMomentsByRun(observation InvariantObservation) (map[string]runLooks, map[string]time.Time, error) {
	looked := map[string]runLooks{}
	recorded := map[string]time.Time{}
	for _, event := range observation.MercatorEvents {
		runID := strings.TrimPrefix(event.Subject, "runs/")
		switch event.Type {
		case orchestrator.EventExternalStateObserved:
			var look adapter.ExternalObservation
			if err := json.Unmarshal(event.Data, &look); err != nil {
				return nil, nil, fmt.Errorf("decode Run %q observation: %w", runID, err)
			}
			looked[runID] = append(looked[runID], look)
		case orchestrator.EventExecutionStarted:
			var payload struct {
				StartedAt time.Time `json:"started_at"`
			}
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				return nil, nil, fmt.Errorf("decode Run %q start moment: %w", runID, err)
			}
			recorded[runID] = payload.StartedAt.UTC()
		}
	}
	return looked, recorded, nil
}

func monotonicTerminalState(observation InvariantObservation) error {
	closed := map[string]bool{}
	outcomes := map[string]string{}
	for _, event := range observation.MercatorEvents {
		runID := strings.TrimPrefix(event.Subject, "runs/")
		if closed[runID] {
			return fmt.Errorf("Run %q recorded %q after closure", runID, event.Type)
		}
		if event.Type == "compute.run.outcome_recorded.v1" {
			var payload struct {
				Outcome string `json:"outcome"`
			}
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				return fmt.Errorf("decode Run %q outcome: %w", runID, err)
			}
			if previous := outcomes[runID]; previous != "" && previous != payload.Outcome {
				return fmt.Errorf("Run %q changed terminal outcome from %q to %q", runID, previous, payload.Outcome)
			}
			outcomes[runID] = payload.Outcome
		}
		if event.Type == "compute.run.closed.v1" {
			closed[runID] = true
		}
	}
	return nil
}

func idempotentExternalCommands(observation InvariantObservation) error {
	type consequence struct {
		hash string
		data string
	}
	accepted := map[string]consequence{}
	for _, effect := range observation.Effects {
		if !effectMutatesWorld(effect.Operation) {
			continue
		}
		key := effect.Operation + "/" + effect.OperationID
		if effect.Command == EffectCommandDuplicate {
			if _, exists := accepted[key]; !exists {
				return fmt.Errorf("duplicate effect %q has no accepted command", key)
			}
			continue
		}
		if effect.Command != EffectCommandAccepted {
			continue
		}
		current := consequence{hash: effect.RequestHash, data: string(effect.Consequence)}
		if previous, exists := accepted[key]; exists && previous != current {
			return fmt.Errorf("accepted command %q changed consequence", key)
		}
		accepted[key] = current
	}
	return nil
}

func effectMutatesWorld(operation string) bool {
	switch operation {
	case OperationProviderLaunch,
		OperationProviderRelease,
		OperationProviderTerminate,
		// Allocating a machine, suspending it, bringing it back, and destroying it
		// each change what a provider is holding for Mercator, and each is asked for
		// under an operation key the provider is expected to honour. The two reads
		// in the same family, capacity.observe and capacity.list_owned, are
		// deliberately not here, and neither are the three things this world does on
		// its own account: capacity.preempted, node.enrolled, and
		// node.session_renewed.
		OperationCapacityProvision,
		OperationCapacityStop,
		OperationCapacityResume,
		OperationCapacityTerminate,
		OperationNodePrepareImage,
		OperationNodePrepareArtifact,
		OperationNodePrepareAbandoned,
		OperationImagePull,
		OperationImageRetained,
		OperationArtifactRead,
		OperationArtifactWritten,
		OperationArtifactReplicated,
		OperationArtifactPublished,
		OperationCacheMountAttach,
		OperationControlPlaneRestart:
		return true
	default:
		return false
	}
}

func leaseFencing(observation InvariantObservation) error {
	owners := map[string]string{}
	for _, execution := range observation.World.ActiveExecutions {
		if execution.OwnershipToken == "" {
			return fmt.Errorf("active launch %q has no ownership token", execution.LaunchKey)
		}
		if launchKey := owners[execution.OwnershipToken]; launchKey != "" && launchKey != execution.LaunchKey {
			return fmt.Errorf("ownership token fences launches %q and %q", launchKey, execution.LaunchKey)
		}
		owners[execution.OwnershipToken] = execution.LaunchKey
	}
	return nil
}

// artifactDependencies orders every consuming launch against the publication it
// depends on. What a Run consumes is read from the workload Mercator recorded,
// so this checks Mercator's own admission decision against the object store's
// own history. Reading the World Tape's arrivals instead would let the rule pass
// on a Run whose declaration the control plane never held, and checking current
// replicas would assert what the launch path itself just wrote.
func artifactDependencies(observation InvariantObservation) error {
	publishedAt, err := publicationSequences(observation)
	if err != nil {
		return err
	}
	for _, effect := range observation.Effects {
		if effect.Operation != OperationProviderLaunch || effect.Command != EffectCommandAccepted {
			continue
		}
		// A launch Mercator holds no workload for is a violation rather than a
		// launch that reads nothing. The rule is about what the control plane
		// decided, so a missing decision is the one thing it may never read as
		// permission.
		workload, recorded := observation.Workloads[effect.CorrelationID]
		if !recorded {
			return fmt.Errorf(
				"Run %q launched at effect %d and Mercator recorded no workload for it",
				effect.CorrelationID,
				effect.Sequence,
			)
		}
		for _, artifactID := range workload.Spec.Artifacts.Consumes {
			published, exists := publishedAt[artifactID]
			if !exists || published > effect.Sequence {
				return fmt.Errorf(
					"Run %q launched at effect %d before Artifact %q was durable",
					effect.CorrelationID,
					effect.Sequence,
					artifactID,
				)
			}
		}
	}
	return nil
}

// publicationSequences is when each Artifact version became durable, in ledger
// order. A version the catalog already holds published was durable before the
// first effect, which is what content produced outside this world looks like.
func publicationSequences(observation InvariantObservation) (map[string]uint64, error) {
	published := map[string]uint64{}
	for id, version := range observation.ArtifactCatalog {
		if version.Durable() && !version.PublishedAt.After(observation.StartedAt) {
			published[id] = 0
		}
	}
	for _, effect := range observation.Effects {
		if effect.Operation != OperationArtifactPublished || effect.Command != EffectCommandAccepted {
			continue
		}
		var request struct {
			ArtifactID string `json:"artifact_id"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			return nil, fmt.Errorf("decode Artifact publication %s: %w", effect.ID, err)
		}
		if _, exists := published[request.ArtifactID]; !exists {
			published[request.ArtifactID] = effect.Sequence
		}
	}
	return published, nil
}

// artifactReplicaVerified is the standing guard on what a local copy is worth.
// The object store is the authority and a replica is an optimisation over it, so
// four things hold at once: no copy exists of content the catalog cannot name,
// no copy claims a digest that version does not have, every copy traces back to
// the object store, and no Run reads a copy that was not checked against the
// version it names.
//
// "Traces back to the object store" is exactly the version being durable, with
// no second shape. Content a workload wrote for itself is not one of these: no
// runtime enumerates, hashes, or files it, so it is a write in the ledger and
// never a copy in an inventory. A replica of a version nothing published is
// content from nowhere, which is what a replica standing in for an authority
// looks like.
func artifactReplicaVerified(observation InvariantObservation) error {
	for _, replica := range observation.World.ArtifactReplicas {
		version, known := observation.ArtifactCatalog[replica.ArtifactID]
		if !known {
			return fmt.Errorf("offer %q holds a copy of Artifact %q, which the catalog does not know", replica.OfferID, replica.ArtifactID)
		}
		// A copy this world delivered carries the catalog's digest, because that
		// is what it copied. A copy the World Tape seeded carries whatever the
		// machine claims, which is a fact about that machine's own bookkeeping:
		// an operator who restored an older snapshot leaves a host reporting a
		// checked copy of content this version does not have. Forbidding that
		// state outright would make the digest half of ArtifactInventory.Holds
		// unreachable, which is to say it would make the mistake it exists to
		// catch impossible to write down.
		if !observation.SeededReplicas[replica.OfferID][replica.ArtifactID] &&
			replica.ContentDigest != version.ContentDigest {
			return fmt.Errorf(
				"offer %q holds Artifact %q claiming digest %s, and the catalog says %s",
				replica.OfferID, replica.ArtifactID, replica.ContentDigest, version.ContentDigest,
			)
		}
		if !version.Durable() {
			return fmt.Errorf(
				"offer %q holds a copy of Artifact %q, which nothing published",
				replica.OfferID, replica.ArtifactID,
			)
		}
	}
	return artifactReadsWereVerified(observation)
}

// artifactReadsWereVerified is the read side of the same rule, stated over every
// read a Run made and against the catalog rather than against a copy's own state.
// What a workload was handed has to be the version it asked for however the bytes
// reached it: restoring an older volume snapshot leaves a machine holding a
// verified copy of the version before under this version's name, and a Run handed
// those bytes read the wrong content at local-disk speed on a candidate every
// predicate in the control plane priced at the whole read.
//
// Stating it over reads out of the object store as well is what makes the ledger
// answerable for them. The durable copy is the authority, so a read from it is the
// right bytes by construction, and a record that said otherwise would be the world
// reporting a read it did not perform.
func artifactReadsWereVerified(observation InvariantObservation) error {
	for _, effect := range observation.Effects {
		if effect.Operation != OperationArtifactRead || effect.Command != EffectCommandAccepted {
			continue
		}
		var request struct {
			ArtifactID string `json:"artifact_id"`
			OfferID    string `json:"offer_id"`
		}
		var read artifactRead
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			return fmt.Errorf("decode Artifact read %s: %w", effect.ID, err)
		}
		if err := json.Unmarshal(effect.Consequence, &read); err != nil {
			return fmt.Errorf("decode Artifact read consequence %s: %w", effect.ID, err)
		}
		if read.Source == "replica" && !read.ReplicaState.Usable() {
			return fmt.Errorf(
				"Run %q read Artifact %q from a %q copy on offer %q, which nothing checked against the catalog",
				effect.CorrelationID, request.ArtifactID, read.ReplicaState, request.OfferID,
			)
		}
		if digest := observation.ArtifactCatalog[request.ArtifactID].ContentDigest; read.ContentDigest != digest {
			return fmt.Errorf(
				"Run %q read Artifact %q from the %s on offer %q and was handed digest %s, and the catalog says %s",
				effect.CorrelationID, request.ArtifactID, read.Source, request.OfferID, read.ContentDigest, digest,
			)
		}
	}
	return nil
}

// ephemeralCapacityNotReused is the standing guard on the lane split. A one-shot
// execution holds nothing after it exits, so nothing may ever wait behind it and
// nothing may inherit its capacity. Placement records the disposition it chose;
// this reads the audit trail back and checks the Rental Schedules agree.
func ephemeralCapacityNotReused(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	ephemeralRentals := map[string]string{}
	for _, decision := range decisions {
		for _, candidate := range decision.Candidates {
			if candidate.Disposition != domain.CandidateDispositionEphemeral {
				continue
			}
			if candidate.OfferSnapshotID != decision.SelectedOfferSnapshotID || decision.Booking == nil {
				continue
			}
			ephemeralRentals[decision.Booking.RentalID] = decision.RunID
		}
		if decision.Booking == nil || decision.Booking.State != domain.BookingStateQueued {
			continue
		}
		if selected := selectedCandidate(decision); selected != nil && selected.Disposition == domain.CandidateDispositionEphemeral {
			return fmt.Errorf(
				"Run %q was queued behind one-shot capacity %q, which holds nothing to wait for",
				decision.RunID,
				selected.OfferSnapshotID,
			)
		}
	}
	for rentalID, runID := range ephemeralRentals {
		schedule, ok := observation.RentalSchedules[rentalID]
		if !ok {
			continue
		}
		if len(schedule.Bookings) > 1 {
			return fmt.Errorf(
				"one-shot capacity held for Run %q accumulated %d Bookings, which claims reuse it cannot perform",
				runID,
				len(schedule.Bookings),
			)
		}
	}
	return nil
}

// reusableCapacityHasAnEnrolledRuntime is the lane split read from the side the
// machines are on. safety.ephemeral_capacity_not_reused holds that a one-shot
// product accumulates nothing once its workload exits. This holds the converse:
// the capacity that does accumulate is capacity Mercator can reach, because every
// way a machine accumulates anything runs through an agent of Mercator's on it.
//
// Each of the four is that agent's own work. An image inventory is what the agent
// enumerated on its own disk. A cache is a volume the agent attached. A verified
// Artifact copy is a fetch the agent performed and hashed on arrival. A second
// Booking is a promise that the next Run will be launched here, and a launch is a
// command that travels down the node's own outbound session. A machine no agent
// enrolled on can do none of the four, so a world that shows one doing any of them
// is a world where Mercator is counting warmth, room, or a queue on a host it has
// no way to speak to, and the Run that inherits it begins by discovering there is
// nobody there.
//
// What the World Tape seeded is exempt for images and Artifact copies, exactly as
// safety.locality_provenance exempts it: a machine Mercator borrows a slot on may
// well already be sitting on the content, and that is a fact about the host rather
// than something an agent of Mercator's put there. A cache has no such exemption
// and needs none. A cache exists only where a workload wrote it, so a fixture
// stating one is stating that Mercator ran something on that machine and the
// machine kept what it wrote, which is the claim an enrolment stands behind.
//
// The queue clause is asked of the enrolments rather than of the fleet as it
// stands, because a Rental whose machine has gone is the case most worth asking
// about: the lease and the Bookings waiting on it outlive the offer, and a rule
// that read the current fleet would fall silent about a queue at the moment the
// machine holding it stopped being published.
//
// Only capacity that keeps what it runs is asked, which is what the name of this
// rule says and what an earlier revision did not do. A listing and a one-shot
// host are refused the same three things by safety.locality_provenance, over
// exactly the same worlds, and that rule names the reason an operator has to act
// on: the machine does not exist yet, or it holds nothing once its workload
// exits. Neither has an agent to enrol, so answering first with a violation about
// an enrolment sent a reader after the one remedy the ephemeral lane must never
// apply.
func reusableCapacityHasAnEnrolledRuntime(observation InvariantObservation) error {
	enrolled, err := enrolledRuntimes(observation.Effects)
	if err != nil {
		return err
	}
	for _, offer := range observation.World.Offers {
		if err := accumulationRunsThroughAnAgent(
			offer,
			enrolled,
			observation.SeededLocality[offer.ID],
			observation.SeededReplicas[offer.ID],
		); err != nil {
			return err
		}
	}
	return everyQueueHasAnAgentToDispatchThrough(observation.RentalSchedules, enrolled)
}

// enrolledRuntime is every machine an agent of Mercator's has opened a session
// for, and every lease those sessions were invited under. Both are read out of the
// ledger rather than off the offers, because the ledger is the only account of
// what really happened on the machine: an offer that claimed an enrolled runtime
// would be the world agreeing with itself, which is the one thing a rule here may
// never rest on.
//
// The two sets are kept apart because the two clauses ask different questions. An
// inventory belongs to the machine that enumerated it, and a queue belongs to the
// lease, and one lease may be invited against a second machine when its first
// generation ends.
type enrolledRuntime struct {
	machines map[string]bool
	rentals  map[string]bool
}

func enrolledRuntimes(effects []EffectRecord) (enrolledRuntime, error) {
	enrolled := enrolledRuntime{machines: map[string]bool{}, rentals: map[string]bool{}}
	for _, effect := range effects {
		if effect.Operation != OperationNodeEnrolled || effect.Command != EffectCommandAccepted {
			continue
		}
		var session struct {
			MachineID string `json:"machine_id"`
			RentalID  string `json:"rental_id"`
		}
		if err := json.Unmarshal(effect.Request, &session); err != nil {
			return enrolledRuntime{}, fmt.Errorf("decode enrolment %s: %w", effect.ID, err)
		}
		// An enrolment that names neither is a record this rule cannot use, and
		// dropping it quietly is how the rule would weaken without anything saying
		// so. The listing clause below is keyed on a machine handle being absent,
		// so one such record read as an enrolment of the empty handle would clear
		// every listing in the world at once.
		if session.MachineID == "" || session.RentalID == "" {
			return enrolledRuntime{}, fmt.Errorf(
				"enrolment %s opened a session on machine %q under lease %q, and an enrolment names both",
				effect.ID, session.MachineID, session.RentalID,
			)
		}
		enrolled.machines[session.MachineID] = true
		enrolled.rentals[session.RentalID] = true
	}
	return enrolled, nil
}

// accumulationRunsThroughAnAgent holds the three clauses about one machine. The
// machine is named by its own handle rather than by the offer it was published
// under, because an enrolment is about a machine and an offer is one publication
// of it.
//
// Capacity that keeps nothing is not asked. A listing describes a machine that
// does not exist yet and a one-shot host holds nothing once its workload exits,
// so neither has an agent to enrol, and safety.locality_provenance refuses both
// the same three things while naming the reason that is actually theirs.
func accumulationRunsThroughAnAgent(
	offer domain.OfferSnapshot,
	enrolled enrolledRuntime,
	seeded, seededCopies map[string]bool,
) error {
	if !offer.KeepsWhatItRuns() || enrolled.machines[offer.MachineID] {
		return nil
	}
	for _, digest := range heldDigests(offer.Images) {
		if seeded[digest] {
			continue
		}
		return fmt.Errorf(
			"offer %q holds %s, and no agent has enrolled on machine %q, so nothing of Mercator's fetched or enumerated it",
			offer.ID, digest, offer.MachineID,
		)
	}
	if len(offer.Caches.Mounts) > 0 {
		return fmt.Errorf(
			"offer %q holds cache %q for workspace %q, and no agent has enrolled on machine %q, so no workload of Mercator's ever wrote it there",
			offer.ID, offer.Caches.Mounts[0].Name, offer.Caches.Mounts[0].WorkspaceID, offer.MachineID,
		)
	}
	for _, replica := range offer.Artifacts.Replicas {
		if seededCopies[replica.ArtifactID] {
			continue
		}
		return fmt.Errorf(
			"offer %q holds a copy of Artifact %q, and no agent has enrolled on machine %q, so nothing of Mercator's fetched those bytes or checked them",
			offer.ID, replica.ArtifactID, offer.MachineID,
		)
	}
	return nil
}

// everyQueueHasAnAgentToDispatchThrough holds the fourth clause. One Booking on a
// lease is the Run that took the capacity, and the launch path answers for it. A
// second is Mercator promising a Run it will be started on that machine when the
// one ahead of it finishes, and the only thing that can start it is a command down
// the node's own session.
func everyQueueHasAnAgentToDispatchThrough(schedules map[string]domain.RentalSchedule, enrolled enrolledRuntime) error {
	for _, rentalID := range slices.Sorted(maps.Keys(schedules)) {
		schedule := schedules[rentalID]
		if len(schedule.Bookings) < 2 || enrolled.rentals[rentalID] {
			continue
		}
		return fmt.Errorf(
			"Rental %q holds %d Bookings and no agent ever enrolled against it, so the Runs waiting there wait for a dispatch nothing can carry",
			rentalID, len(schedule.Bookings),
		)
	}
	return nil
}

// aRentalIdentityIsCapacityMercatorHolds is the law on who may carry a lease. A
// Rental is Mercator's own record of capacity it holds, so the offers that carry
// one are the machines it holds: standing capacity in the reusable lane, named by
// the enrolment that put an agent on it. Everything else in a fleet is capacity to
// acquire or a product that ends with its workload, and neither is a lease.
//
// The harm is a queue. A Rental Schedule is keyed by Rental identity, so an offer
// that publishes one Mercator does not hold gives Placement somewhere to put a
// Booking and somewhere for the next Run to wait: the second Run queues behind a
// lease that was never allocated, is promised the first one's finish, and waits
// for a machine nothing will ever free. That is the defect the production offer
// route was carrying, in two forms. An adapter stated its own contract id as a
// rental_id and it survived the reusable lane, and aggregation minted one for any
// standing offer in that lane, which is OfferKind answering a question it does not
// answer: Kind says who owns the host, so a marketplace listing of somebody
// else's idle machine is standing.
//
// It is stated over the fleet as published rather than over the Bookings, because
// the Bookings are downstream of the mistake. A Rental identity on a template for
// a machine that does not exist yet is wrong when it is published, whether or not
// a Run happened to land on it in this Blueprint, and a rule that waited for the
// queue to form would pass every fixture with one Run in it.
func aRentalIdentityIsCapacityMercatorHolds(observation InvariantObservation) error {
	for _, offer := range observation.World.Offers {
		if offer.RentalID == "" || offer.KeepsWhatItRuns() {
			continue
		}
		reason := "is a template for a machine that does not exist yet"
		if !offer.Lane.Reusable() {
			reason = "is a one-shot product that holds nothing once its workload exits"
		}
		return fmt.Errorf(
			"offer %q %s, and publishes Rental %q, which is a lease Mercator does not hold and a queue the next Run can wait in",
			offer.ID,
			reason,
			offer.RentalID,
		)
	}
	return nil
}

// candidateIdentityRecurs is the law on what a launch history may be filed under.
// A prediction that reports evidence about this exact candidate is only worth
// reading if the key it was filed under is the same thing twice, and every way of
// getting that wrong is silent: two machines that share a key trade each other's
// pull samples, and a key nothing can recur under reports a single sample as
// candidate-specific evidence forever.
//
// It is stated as a collision against World Truth rather than as a derivation. The
// world knows which machine is which and what cards each one holds, and it counts
// them where the key groups them, so a rule that agreed with the key by
// construction could not have caught either of the two bugs it was written for: an
// inventory that dropped cards when a probe grouped them differently, and a Docker
// machine named by the route Mercator took to reach it.
func candidateIdentityRecurs(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	published := map[string]domain.OfferSnapshot{}
	for _, offer := range observation.World.Offers {
		published[offer.ID] = offer
	}
	keyed := map[string]keyedCandidate{}
	for _, decision := range decisions {
		asked := imageAsked(observation, decision.RunID)
		for _, candidate := range decision.Candidates {
			offer, known := published[candidate.OfferSnapshotID]
			if !known {
				continue
			}
			if err := candidateKeyIsHonest(decision.RunID, candidate, offer); err != nil {
				return err
			}
			key := candidate.Candidate.Candidate(true)
			if key == "" {
				continue
			}
			held, clash := keyed[key]
			switch {
			case clash && !sameCapacity(held.offer, offer):
				return fmt.Errorf(
					"Run %q filed candidate %q under the key %q, and %s already holds it: %s",
					decision.RunID, offer.ID, key, held.offer.ID, describeCapacity(held.offer, offer),
				)
			case clash && held.image != asked:
				return fmt.Errorf(
					"Run %q filed candidate %q under the content key %q, and it already holds %q: two Runs asked this machine for different content",
					decision.RunID, offer.ID, key, held.image,
				)
			}
			keyed[key] = keyedCandidate{offer: offer, image: asked}
		}
	}
	return nil
}

// predictionStatesItsProvenance is the law on what a prediction has to say about
// itself. Every stage of every recorded candidate names the level its answer came
// from and how many measured launches stand behind it, and the key it was read
// under is the key this candidate has at that level: not the listing it arrived
// on, and not a coarser bucket's name for something else.
//
// The last clause is the load-bearing one. A marketplace mints a fresh ask ID
// for every search of a machine that was already there, so a history filed under
// the listing accumulates keys holding one sample each and reports every one of
// them as candidate-specific evidence: the answer is wrong, the sample count is
// right, and nothing in the record says which. Comparing the key the estimator
// read against the listing the offer arrived under is what catches it, and the
// corpus states the world it happens in by publishing one machine under two ask
// IDs.
//
// The other clauses are about the record being readable at all. A stage with no
// level cannot be told from a stage answered by a constant, a keyed level with
// no samples is a claim of evidence with none behind it, and a prior carrying
// samples is the opposite: measured launches filed under an answer that says
// nobody has watched this happen. And a stage that moves bytes may not be
// answered from launches at all, which is the clause every other reader of a
// transfer's seconds rests on.
func predictionStatesItsProvenance(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		for _, candidate := range decision.Candidates {
			for _, stage := range domain.LaunchStages {
				if err := stageStatesItsProvenance(decision.RunID, candidate, stage); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// stageStatesItsProvenance holds one stage of one candidate to what its level
// claims. The key is checked against the candidate's own recorded identity as
// well as against the listing, because those are the two ways a keyed answer goes
// wrong: filed under something that does not recur, or filed under a key that is
// some other candidate's or some other workload's.
func stageStatesItsProvenance(runID string, candidate domain.CandidateDecision, stage domain.LaunchStage) error {
	answer := candidate.Estimates.Stages.Stage(stage)
	switch answer.Level {
	case "":
		return fmt.Errorf(
			"Run %q predicted candidate %q spending %.2fs on %s, and the record does not say what that rests on",
			runID, candidate.OfferSnapshotID, answer.Expected, stage,
		)
	case domain.LevelPrior:
		if answer.SampleCount != 0 || answer.Key != "" {
			return fmt.Errorf(
				"Run %q answered candidate %q's %s stage from the prior, and filed %d samples under %q against it",
				runID, candidate.OfferSnapshotID, stage, answer.SampleCount, answer.Key,
			)
		}
		return nil
	case domain.LevelExactCandidate, domain.LevelProviderAndRegion, domain.LevelProvider:
		if err := aTransferWasNotAnsweredFromLaunches(runID, candidate, stage, answer); err != nil {
			return err
		}
		return keyedAnswerIsHonest(runID, candidate, stage, answer)
	default:
		return fmt.Errorf(
			"Run %q answered candidate %q's %s stage at level %q, which is not a level of the hierarchy",
			runID, candidate.OfferSnapshotID, stage, answer.Level,
		)
	}
}

func keyedAnswerIsHonest(runID string, candidate domain.CandidateDecision, stage domain.LaunchStage, answer domain.Estimate) error {
	if answer.SampleCount <= 0 || answer.Key == "" {
		return fmt.Errorf(
			"Run %q answered candidate %q's %s stage at level %q from %d samples under %q, which is a claim of evidence with none behind it",
			runID, candidate.OfferSnapshotID, stage, answer.Level, answer.SampleCount, answer.Key,
		)
	}
	for _, listing := range []string{candidate.OfferSnapshotID, candidate.NativeRef} {
		if listing != "" && strings.Contains(answer.Key, listing) {
			return fmt.Errorf(
				"Run %q answered candidate %q's %s stage under the key %q, which names the listing %q rather than what recurs",
				runID, candidate.OfferSnapshotID, stage, answer.Key, listing,
			)
		}
	}
	if answer.Level == domain.LevelExactCandidate && !candidate.Candidate.Recurs() {
		return fmt.Errorf(
			"Run %q answered candidate %q's %s stage as this exact candidate under %q, and nothing about this capacity outlives its listing",
			runID, candidate.OfferSnapshotID, stage, answer.Key,
		)
	}
	own := keyOfLevel(candidate.Candidate, answer.Level, stage)
	if own == "" {
		return fmt.Errorf(
			"Run %q answered candidate %q's %s stage at level %q under %q, and this candidate has no key at that level for evidence to be filed under",
			runID, candidate.OfferSnapshotID, stage, answer.Level, answer.Key,
		)
	}
	if answer.Key != own {
		return fmt.Errorf(
			"Run %q answered candidate %q's %s stage out of %q, and at level %q this candidate is %q",
			runID, candidate.OfferSnapshotID, stage, answer.Key, answer.Level, own,
		)
	}
	return nil
}

// keyOfLevel is the key this candidate's own recorded identity has at one level of
// the hierarchy, restated here rather than read from the estimator: a rule that
// asked the predictor what the predictor should have used could not fail.
//
// Every level is derived, not only the narrowest. A coarse rung answers about
// other machines by design and about other content never, so the content the
// stage is about has to reach the key at whichever rung answered, and a rung that
// dropped it reads back as a region's evidence about a workload nobody has run
// there.
func keyOfLevel(identity domain.CandidateIdentity, level domain.PredictionLevel, stage domain.LaunchStage) string {
	switch level {
	case domain.LevelExactCandidate:
		return identity.Candidate(contentStage(stage))
	case domain.LevelProviderAndRegion:
		return identity.ProviderAndRegion(contentStage(stage))
	case domain.LevelProvider:
		return identity.ProviderKey(contentStage(stage))
	default:
		return ""
	}
}

// aTransferWasNotAnsweredFromLaunches is the clause every other reader of a
// transfer's seconds rests on, this rule's neighbours included.
//
// A stage that moves bytes is priced from two things and the record explains both:
// the locality evidence accounts for the byte count, the transfer rate accounts
// for the throughput, and safety.locality_is_never_infeasibility reads the seconds
// back as their product when it works out how much of a refusal was charged for
// content nobody could describe. Measured launches of this candidate are a
// measurement of some other launch's byte count, because what a machine still has
// to move is whatever it does not already hold at the moment it is asked. Answered
// from them, the seconds belong to neither half: a share of the bytes applied to
// them is a share of a quantity that did not produce them, and a host holding every
// byte is charged the transfer it performed the last time it held none.
//
// So it is refused outright rather than accounted for. The alternative is every
// rule that reads a transfer's seconds having to ask first whether these ones are a
// transfer's seconds at all, which is a condition spread across the laws instead of
// stated once.
func aTransferWasNotAnsweredFromLaunches(
	runID string,
	candidate domain.CandidateDecision,
	stage domain.LaunchStage,
	answer domain.Estimate,
) error {
	if !pricedFromBytes(stage) {
		return nil
	}
	return fmt.Errorf(
		"Run %q answered candidate %q's %s stage from %d measured launches under %q, and a transfer's seconds are this launch's own missing bytes over the path they cross",
		runID, candidate.OfferSnapshotID, stage, answer.SampleCount, answer.Key,
	)
}

// pricedFromBytes is which stages of a launch are a byte count over a throughput,
// stated here rather than read from the estimator. Reading an image out of a
// registry, assembling it, and reading the Run's declared inputs all are, and each
// of them is the one kind of stage this Lab holds the record to accounting for in
// two halves.
func pricedFromBytes(stage domain.LaunchStage) bool {
	switch stage {
	case domain.StageImageFetch, domain.StageUnpack, domain.StageArtifactFetch:
		return true
	default:
		return false
	}
}

// contentStage is which stages carry the content in their key, stated here
// rather than read from the estimator. A rule that asked the predictor which key
// it should have used would be the predictor agreeing with itself: this is the
// Lab's own account of which durations are a property of what was pulled and
// which are a property of the machine.
//
// One stage is left. An application coming up is a property of the image, because
// the application is the image; the transfers are a property of the image too and
// are not answered from launches at all, so no key of theirs is ever read back.
func contentStage(stage domain.LaunchStage) bool {
	return stage == domain.StageApplicationReady
}

// keyedCandidate is what a candidate key has already been handed out for: the
// capacity it was filed about and the content that capacity was asked to run.
type keyedCandidate struct {
	offer domain.OfferSnapshot
	image string
}

// imageAsked is the image Mercator recorded it was asked to run for this Run, read
// out of its own workload record rather than out of the identity under judgment. A
// key that carries content has to have been derived from the content this Run
// named, and asking the identity what content it names would be the derivation
// agreeing with itself.
func imageAsked(observation InvariantObservation, runID string) string {
	workload, known := observation.Workloads[runID]
	if !known || len(workload.Spec.Containers) == 0 {
		return ""
	}
	return workload.Spec.Containers[0].Image
}

// candidateKeyIsHonest holds the clauses about one recorded candidate: capacity with
// nothing published that outlives its listing has no key at all, and a key names the
// machine its backend published and never the listing search found.
func candidateKeyIsHonest(runID string, candidate domain.CandidateDecision, offer domain.OfferSnapshot) error {
	key := candidate.Candidate.Candidate(true)
	if key == "" {
		return nil
	}
	if nothingOutlivesTheListing(offer) {
		return fmt.Errorf(
			"Run %q filed candidate %q under the key %q, and this world publishes nothing about it that outlives the listing",
			runID, offer.ID, key,
		)
	}
	if candidate.Candidate.Machine != offer.MachineID {
		return fmt.Errorf(
			"Run %q filed candidate %q under machine %q, and the machine it is is %q",
			runID, offer.ID, candidate.Candidate.Machine, offer.MachineID,
		)
	}
	for _, listing := range []string{offer.ID, offer.NativeRef} {
		if listing == "" || listing == offer.MachineID {
			continue
		}
		if strings.Contains(key, listing) {
			return fmt.Errorf(
				"Run %q filed candidate %q under the key %q, which names the listing %q rather than what recurs",
				runID, offer.ID, key, listing,
			)
		}
	}
	return nil
}

// nothingOutlivesTheListing reports whether this world published anything about this
// capacity that is still true the next time it is offered. A machine handle, a place,
// a product name, and cards all are; the provider alone is not, because it
// distinguishes no two of the things it sells from each other.
//
// It reads the offer rather than the identity, which is what makes it a rule instead
// of the derivation agreeing with itself: a Mercator that invented a region, or that
// kept a key for a one-shot pool naming only its own provider, would be filing
// history under a name the world never gave it.
func nothingOutlivesTheListing(offer domain.OfferSnapshot) bool {
	return offer.MachineID == "" &&
		offer.Region == "" &&
		offer.InstanceType == "" &&
		len(offer.Resources.Accelerators) == 0
}

// sameCapacity reports whether two offers are the same thing to learn about. It
// compares the facts a candidate key summarizes, counting the accelerators rather
// than grouping them: a machine with twice the cards, or with the same cards in two
// memory sizes, is a different product however a probe reported its inventory.
//
// The lane is one of those facts. A world may sell one product both ways, and the
// reusable listing becomes a machine with an enrolled runtime whose disk outlives
// the Run while the one-shot holds nothing, so two offers that differ only in their
// lane are two things to learn about and a shared key is a collision.
func sameCapacity(first, second domain.OfferSnapshot) bool {
	firstCards, firstMemory := acceleratorTotals(first)
	secondCards, secondMemory := acceleratorTotals(second)
	return first.MachineID == second.MachineID &&
		first.Lane == second.Lane &&
		first.AdapterType == second.AdapterType &&
		first.Region == second.Region &&
		first.InstanceType == second.InstanceType &&
		firstCards == secondCards &&
		firstMemory == secondMemory
}

// acceleratorTotals is how many cards a machine holds and how much accelerator
// memory they add up to.
func acceleratorTotals(offer domain.OfferSnapshot) (cards int, memoryBytes int64) {
	for _, accelerator := range offer.Resources.Accelerators {
		cards += accelerator.Count
		memoryBytes += int64(accelerator.Count) * accelerator.MemoryBytes
	}
	return cards, memoryBytes
}

func describeCapacity(first, second domain.OfferSnapshot) string {
	firstCards, firstMemory := acceleratorTotals(first)
	secondCards, secondMemory := acceleratorTotals(second)
	return fmt.Sprintf(
		"%s is %s machine %q on %s/%s/%s with %d cards of %d bytes, and %s is %s machine %q on %s/%s/%s with %d cards of %d bytes",
		first.ID, first.Lane, first.MachineID, first.AdapterType, first.Region, first.InstanceType, firstCards, firstMemory,
		second.ID, second.Lane, second.MachineID, second.AdapterType, second.Region, second.InstanceType, secondCards, secondMemory,
	)
}

// recordedDecisions is every Booking Decision Mercator recorded, in event
// order. Rules about what Placement decided read this rather than world state:
// the decision is the thing under judgment, and it is the only place a candidate
// Mercator refused leaves any trace at all.
func recordedDecisions(observation InvariantObservation) ([]domain.BookingDecision, error) {
	records, err := recordedDecisionRecords(observation)
	if err != nil {
		return nil, err
	}
	decisions := make([]domain.BookingDecision, 0, len(records))
	for _, record := range records {
		decisions = append(decisions, record.decision)
	}
	return decisions, nil
}

// localityRefusals are the codes a refusal-for-content would carry. None of them
// is written anywhere in this tree, which is exactly what a standing rule is
// for: a law about states the system must never reach, rather than a test of one
// it currently reaches.
var localityRefusals = map[string]bool{
	"IMAGE_NOT_CACHED":   true,
	"ARTIFACT_NOT_LOCAL": true,
	"LOCALITY_UNKNOWN":   true,
}

// localityPaths are the offer fields that answer what a machine holds. A
// rejection pointing at one of them is a rejection for content whatever code it
// carries, because those fields say nothing else.
var localityPaths = map[string]bool{
	"images":            true,
	"artifacts":         true,
	"image_locality":    true,
	"artifact_evidence": true,
}

// localityIsNeverInfeasibility is the standing guard on the architectural rule
// that unknown locality is uncertainty. Two things hold in every Booking
// Decision Mercator recorded.
//
// No candidate is struck out for what it holds. Locality answers how long a
// machine takes to become ready and never whether it may be used at all, so a
// rejection citing content is a preference that grew into a constraint, and the
// capacity it removes is capacity the Run cannot then have at any price.
//
// And a start bound strikes out only lateness somebody established. Silence is
// charged the whole content, which is what stops it outranking a machine
// provably ready; letting that price reach a hard bound would strike out the
// machine that may already be holding every byte, which is the exact failure
// this rule exists to prevent. So a LATENCY_SLO_EXCEEDED rejection must be
// justified by the candidate's own established start prediction: queue and
// provisioning, which the offer stated, plus content some inventory answered
// about crossing a path some machine measured. A measured start latency is
// established too, because that is a measurement about this offer whatever
// anyone could enumerate.
//
// The path is asked about for the same reason the content is. A transfer is
// bytes over a rate, and a machine nobody has measured the path of is priced
// from the fleet-wide prior every silent machine is priced from, so seconds out
// of that prior refuse capacity for a number nothing on the machine answered
// for. Counting an exact byte count as established and then dividing it by a
// guess is how a bound could refuse a silence while the record showed nothing
// but established content.
//
// Stating it against the established estimate rather than against "was anything
// unknown" is what keeps the rule from buying silence an exemption. A machine
// fifteen minutes deep in its own stated queue is late whatever it could say
// about its disk, and a Run that refuses to wait three minutes gets to strike
// it out.
//
// The rule is checked twice over, because asking only whether a refusal agrees
// with the established estimate recorded beside it is asking the scheduler to
// confirm its own arithmetic: both sides read one number, so the error where that
// number is the thing computed wrong is invisible. So the second reading
// recomputes what was discounted from the localities, the transfer rates, and the
// per-kind seconds the decision records, independently of the answer it reached.
func localityIsNeverInfeasibility(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		for _, candidate := range decision.Candidates {
			if err := candidateWasPricedNotRefused(decision, candidate); err != nil {
				return err
			}
		}
	}
	return nil
}

func candidateWasPricedNotRefused(decision domain.BookingDecision, candidate domain.CandidateDecision) error {
	for _, rejection := range candidate.Rejections {
		if localityRefusals[rejection.Code] || localityPaths[rejection.Path] {
			return fmt.Errorf(
				"Run %q: candidate %q was refused with %s at %q, and what a machine holds is a price rather than a permission",
				decision.RunID, candidate.OfferSnapshotID, rejection.Code, rejection.Path,
			)
		}
		if rejection.Code != "LATENCY_SLO_EXCEEDED" {
			continue
		}
		if candidate.Estimates.EstablishedStartSeconds.P90 <= decision.Policy.MaxP90StartSeconds {
			return fmt.Errorf(
				"Run %q: candidate %q was refused for a p90 start of %.2fs against a bound of %.2fs, and only %.2fs of that was established",
				decision.RunID,
				candidate.OfferSnapshotID,
				candidate.Estimates.StartSeconds.P90,
				decision.Policy.MaxP90StartSeconds,
				candidate.Estimates.EstablishedStartSeconds.P90,
			)
		}
		if err := silenceWasTakenBackOut(decision, candidate); err != nil {
			return err
		}
	}
	return nil
}

// silenceWasTakenBackOut reads the same refusal off the independent halves of the
// record: what each kind of content cost this candidate, and what the decision
// recorded finding of each. The seconds taken out of the prediction to reach the
// established one must be at least the seconds charged for content nobody could
// describe. A scheduler that quietly counted a silence as established fails here
// while agreeing with itself perfectly, which is the whole reason this reading
// exists beside the other one.
func silenceWasTakenBackOut(decision domain.BookingDecision, candidate domain.CandidateDecision) error {
	estimates := candidate.Estimates
	// A measured start latency is a measurement about this offer whatever anyone
	// could enumerate, and it stands as both halves of the prediction, so there
	// is nothing for it to have discounted.
	if estimates.StartSeconds.SampleCount > 0 {
		return nil
	}
	priced := pricedSilenceSeconds(candidate)
	discounted := estimates.StartSeconds.Expected - estimates.EstablishedStartSeconds.Expected
	if discounted+arithmeticTolerance >= priced {
		return nil
	}
	return fmt.Errorf(
		"Run %q: candidate %q was refused against a %.2fs bound having been charged %.2fs for content nobody could describe, of which only %.2fs was left out of the established start",
		decision.RunID,
		candidate.OfferSnapshotID,
		decision.Policy.MaxP90StartSeconds,
		priced,
		discounted,
	)
}

// pricedSilenceSeconds is what this candidate was charged for a launch nobody
// could describe, recomputed from the localities, the rates, and the seconds the
// decision recorded per kind of content. It holds no opinion about the arithmetic
// the scheduler did and cannot be satisfied by agreeing with it: the shares are
// read off the record's own evidence and applied to the record's own seconds.
//
// A duration is bytes over a rate and either one can be a silence. Bytes nobody
// enumerated are the half this rule was written for. Seconds over a rate nothing
// on the machine published are the other half and are worth no less: the number
// dividing them is the same fleet-wide prior every silent machine is given, so a
// bound refusing capacity on them refuses it for a number nobody answered for.
// The two silences overlap on a stage that suffered both, and a stage is charged
// once at whichever share of it was larger, because a stage discounted twice
// would demand a discount larger than the seconds it was charged.
func pricedSilenceSeconds(candidate domain.CandidateDecision) float64 {
	stages := candidate.Estimates.Stages
	unenumerated := imageIsUnknownShare(candidate.ImageLocality)
	seconds := 0.0
	for _, priced := range []struct {
		stage    domain.LaunchStage
		estimate domain.Estimate
		silent   float64
	}{
		{domain.StageImageFetch, stages.ImageFetch, unenumerated},
		{domain.StageUnpack, stages.Unpack, unenumerated},
		{domain.StageArtifactFetch, stages.ArtifactFetch, unreadableShare(candidate.ArtifactEvidence)},
	} {
		seconds += priced.estimate.Expected * max(priced.silent, guessedRateShare(candidate, priced.stage))
	}
	return seconds
}

// imageIsUnknownShare is how much of an image a host that cannot enumerate itself
// is charged for, which is all of it: the whole content is priced because nothing
// said the bytes are here and nothing said they are not.
func imageIsUnknownShare(locality domain.LocalityState) float64 {
	if locality == domain.LocalityUnknown {
		return 1
	}
	return 0
}

// guessedRateShare is how much of one stage's seconds came out of a rate nobody
// measured, which is all of them or none: a stage records the one throughput it
// was priced from, and Mercator's own prior divides every byte of it or none.
func guessedRateShare(candidate domain.CandidateDecision, stage domain.LaunchStage) float64 {
	for _, rate := range candidate.TransferRates {
		if rate.Stage == stage && rate.Assumption != "" {
			return 1
		}
	}
	return 0
}

// unreadableShare is how much of what this candidate owes on its declared inputs
// is content nobody could describe, by bytes.
func unreadableShare(evidence []domain.ArtifactEvidence) float64 {
	owed, unreadable := int64(0), int64(0)
	for _, found := range evidence {
		owed += found.FetchBytes
		if found.Locality == domain.LocalityUnknown {
			unreadable += found.FetchBytes
		}
	}
	if owed == 0 {
		return 0
	}
	return float64(unreadable) / float64(owed)
}

// arithmeticTolerance is how far two readings of the same seconds may differ
// before the difference is a disagreement rather than floating-point noise.
const arithmeticTolerance = 1e-6

// scoreIsReproducibleFromTheRecord is the standing guard on the score being
// derivable rather than merely reported. For every candidate of every Booking
// Decision Mercator recorded, ScoreUSD is the arithmetic over the terms that
// decision itself carries, at the weights it says it used.
//
// What it forbids is a scoring term whose input is nowhere in the record. Such a
// term is invisible: it moves placements that a reader with the whole decision in
// front of them cannot explain, and no rule can police a number nobody wrote
// down. That is not hypothetical. Two definitions of uncertainty ran side by side
// for a phase, one counting the confidences a candidate's answers carried and the
// other counting facts read straight off the offer, and they agreed on every
// decision only because both were multiplied by zero. The first Run scored with a
// nonzero weight would have made them disagree about every borrowed host, and
// nothing here would have said so.
//
// It says nothing about whether the weights are the right ones. Which rate a class
// declares is a policy statement, and this rule is about the record being enough
// to check the arithmetic that rate was used in.
func scoreIsReproducibleFromTheRecord(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		for _, candidate := range decision.Candidates {
			derived := decision.Weights.ScoreUSD(candidate, decision.Policy.ExpectedRuntimeSeconds)
			if math.Abs(derived-candidate.ScoreUSD) <= arithmeticTolerance {
				continue
			}
			return fmt.Errorf(
				"Run %q: candidate %q recorded a score of %.6f USD, and the terms recorded beside it at the weights the decision states derive %.6f: %s",
				decision.RunID, candidate.OfferSnapshotID, candidate.ScoreUSD, derived, describeScoreTerms(decision, candidate),
			)
		}
	}
	return nil
}

// doubtOnlyTheAnswersTheScoreReads is the other half of reproducibility. The rule
// above asks whether the arithmetic in the record adds up; this asks whether the
// uncertainty term was entitled to charge for what it charged for.
//
// Doubt is charged as one minus a stated confidence, and a silence states nothing
// and is charged nothing. So an answer the score never reads can only ever move a
// candidate one way: it penalises the publisher that measured its machine and
// stood behind the result, leaves alone the publisher that said nothing, and
// leaves alone too the publisher certain its machine refuses every start. The
// machine nobody has measured comes out ahead of both, which is the inverse of
// modelling the unknown as uncertainty.
//
// A published reliability history sat in that list for a phase, doubted here and
// priced nowhere, and no arithmetic check could see it: both models charged the
// same doubt, so the score reproduced from the record exactly. What catches it is
// asking what the answer was about, and domain.ScoredAnswers is where the score
// says which questions it reads.
func doubtOnlyTheAnswersTheScoreReads(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	scored := domain.ScoredAnswers()
	for _, decision := range decisions {
		for _, candidate := range decision.Candidates {
			for _, confidence := range candidate.Confidences {
				if slices.Contains(scored, confidence.Answer) {
					continue
				}
				return fmt.Errorf(
					"Run %q: candidate %q was charged %.3f points of doubt about %q, and the score reads no answer to that; it reads %v",
					decision.RunID, candidate.OfferSnapshotID, 1-confidence.Value, confidence.Answer, scored,
				)
			}
		}
	}
	return nil
}

// promisedStartIsStillAhead is the guarantee half of a Booking that waits. Such a
// Booking carries the latest moment it may start, computed from what the Bookings
// ahead of it have left, and every one of those remainders bottoms out at zero. So
// a Rental held by a Booking past the runtime Mercator enforces prices no wait at
// all and hands the arriving Run a deadline that has already arrived: the
// reconciler that expires missed deadlines removes the Booking on its first pass,
// the Run is placed again, reads the same zero, and comes back to the same machine
// that never came free.
//
// It is stated over the record rather than over the schedule because a promise can
// only be judged against the moment it was made, and the decision is where that
// moment is written down. It says nothing about a deadline that passes later:
// waiting longer than promised is what expiry exists to answer.
func promisedStartIsStillAhead(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		booking := decision.Booking
		if booking == nil || booking.LatestStartAt == nil || booking.LatestStartAt.After(decision.EvaluatedAt) {
			continue
		}
		return fmt.Errorf(
			"Run %q took Booking %q on Rental %q promising a start no later than %s, and the decision that minted it was evaluated at %s",
			decision.RunID, booking.ID, booking.RentalID,
			booking.LatestStartAt.Format(time.RFC3339), decision.EvaluatedAt.Format(time.RFC3339),
		)
	}
	return nil
}

// describeScoreTerms is every quantity the derivation had to work with, so a
// failure names what the record held rather than only that it disagreed.
func describeScoreTerms(decision domain.BookingDecision, candidate domain.CandidateDecision) string {
	return fmt.Sprintf(
		"cost %.6f USD, start %.2fs, declared runtime %.2fs, uncertainty %.3f points, weights %+v",
		candidate.Estimates.CostUSD.Expected,
		candidate.Estimates.StartSeconds.Expected,
		decision.Policy.ExpectedRuntimeSeconds,
		candidate.Uncertainty(),
		decision.Weights,
	)
}

// localityProvenance is the standing guard on how a host becomes warm. Content
// arrives on a machine exactly two ways: the World Tape seeded it there, or a
// pull's bytes landed there and were recorded as retained. And only capacity
// Mercator keeps holds anything beyond its seed at all.
//
// It deliberately says nothing about a host holding less than it held before.
// Locality decays, which is why ImageInventory carries the age of the answer:
// content reclaimed under disk pressure or lost with the machine is a fact to
// model, not a control-plane failure.
func localityProvenance(observation InvariantObservation) error {
	retained, err := retainedByOffer(observation.Effects)
	if err != nil {
		return err
	}
	prepared, err := preparedCopiesByOffer(observation.Effects)
	if err != nil {
		return err
	}
	for _, offer := range observation.World.Offers {
		if err := onlyKeptCapacityHoldsWhatItRan(offer, observation.SeededLocality[offer.ID], observation.SeededReplicas[offer.ID]); err != nil {
			return err
		}
		if err := heldContentIsExplained(offer, observation.SeededLocality[offer.ID], retained[offer.ID]); err != nil {
			return err
		}
		if err := heldCopiesAreExplained(offer, observation.SeededReplicas[offer.ID], prepared[offer.ID]); err != nil {
			return err
		}
	}
	return nil
}

// transferRateIsAttributed is the provenance rule for the other half of a
// transfer prediction. safety.locality_provenance holds that the bytes a
// candidate is charged are explained; this holds that the rate they were divided
// by is, because seconds are the product of the two and either one can be
// invented.
//
// Three things fail it. A transfer priced from nothing, naming neither a
// measurement nor an assumption, is a duration whose reader cannot tell which it
// was, and the two are different claims about the fleet: one says Mercator
// measured this machine, the other says Mercator guessed the same way it guesses
// about every machine. A transfer priced at a throughput presented as measured,
// on a host that published no such fact, is worse: it is a number with a
// measurement's standing and nobody behind it, and it is exactly what a
// prediction slice reaching for a faster answer would write. And a transfer
// priced from an assumption may not be worth what a measurement is worth, which
// is the clause that keeps the first two from being bookkeeping: naming the
// assumption honestly and then charging no doubt for it produces exactly the
// ranking a fabricated measurement would.
//
// It is stated over what the decision recorded rather than over the arithmetic.
// A rule that recomputed the seconds would be a second implementation of the
// predictor agreeing with the first; this asks the record the question an
// operator asks, which is who says so.
func transferRateIsAttributed(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		for _, candidate := range decision.Candidates {
			if err := everyTransferNamesItsRate(decision, candidate); err != nil {
				return err
			}
			for _, rate := range candidate.TransferRates {
				if err := ratePricedFromSomething(decision, candidate, rate); err != nil {
					return err
				}
				if err := measuredRateWasReported(decision, candidate, rate, observation.World.PublishedPaths); err != nil {
					return err
				}
				if err := assumedRateIsWorthAGuess(decision, candidate, rate); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// everyTransferNamesItsRate is what stops the three clauses below from being
// silent by omission. Each of them is stated over the rates a decision recorded,
// so a decision that recorded none says nothing they can adjudicate, and a
// prediction reaching for a faster answer has an easier way out than inventing a
// measurement: charge the seconds and leave the throughput off the record.
//
// A stage that moved bytes always spent time doing it, and a stage that moved none
// spent none, so seconds are the question asked here rather than a byte count this
// rule would have to be told. A transfer charged seconds with no rate beside them
// is a duration whose second half nobody wrote down, and the reader that suffers is
// safety.locality_is_never_infeasibility: it works out how much of a refusal was
// charged for content nobody could describe, and an unattributed rate reads there
// as a rate somebody measured.
func everyTransferNamesItsRate(decision domain.BookingDecision, candidate domain.CandidateDecision) error {
	priced := map[domain.LaunchStage]bool{}
	for _, rate := range candidate.TransferRates {
		priced[rate.Stage] = true
	}
	for _, stage := range domain.LaunchStages {
		seconds := candidate.Estimates.Stages.Stage(stage).Expected
		if !pricedFromBytes(stage) || seconds <= 0 || priced[stage] {
			continue
		}
		return fmt.Errorf(
			"Run %q: candidate %q was charged %.2fs on its %s stage, and the record names no throughput it was divided by",
			decision.RunID, candidate.OfferSnapshotID, seconds, stage,
		)
	}
	return nil
}

func ratePricedFromSomething(decision domain.BookingDecision, candidate domain.CandidateDecision, rate domain.TransferRate) error {
	if rate.Attributed() {
		return nil
	}
	return fmt.Errorf(
		"Run %q: candidate %q was charged %d bytes at %.2f Mbps on its %s stage, and the record names %s",
		decision.RunID, candidate.OfferSnapshotID, rate.Bytes, rate.Mbps, rate.Stage, describeRateProvenance(rate),
	)
}

// measuredRateWasReported holds the second clause: a rate the record presents as
// measured has to be a number some machine in this world really published, at the
// scope it was priced over, and one its publisher stood behind when the placement
// was taken. A disowned or expired fact is silence for every other reader here, so
// it may not become a measurement by being divided by.
//
// It is asked of what the world published and at the decision's own moment, which
// is both the only moment the record carries and the moment production priced the
// rate at. OfferSnapshot.DownloadRate takes it from the same field, so a rate this
// finds unpublished is one the scheduler had no standing fact for either. It read
// the offer's observation moment instead for a while, and a fact that lapsed in
// between was priced as a measurement here and reported as a fabrication there,
// against a decision taken by the scheduler's own documented rule.
//
// Capacity is retired while the decisions taken about it stay written down, so a
// rule stated against the fleet as it stands now would turn a correct placement
// into a violation the moment the losing machine's lease elapsed, and would report
// it in the words of the thing it exists to catch. A measurement nobody ever
// published still fails here, because this world remembers every fact it handed to
// Mercator and no machine's silence becomes a publication by being forgotten.
func measuredRateWasReported(
	decision domain.BookingDecision,
	candidate domain.CandidateDecision,
	rate domain.TransferRate,
	published map[string][]domain.NetworkFact,
) error {
	if rate.Measurement == "" {
		return nil
	}
	reported := domain.NetworkFacts{Download: published[candidate.OfferSnapshotID]}
	fact, answered := reported.DownloadP10(rate.Scope, decision.EvaluatedAt)
	if !answered || fact.ValueMbps != rate.Mbps {
		return fmt.Errorf(
			"Run %q: candidate %q priced its %s stage at %.2f Mbps measured by %q, and %s published about its %q path when the decision was taken",
			decision.RunID, candidate.OfferSnapshotID, rate.Stage, rate.Mbps, rate.Measurement,
			describeReportedPath(fact, answered), rate.Scope,
		)
	}
	return nil
}

// assumedRateIsWorthAGuess holds the third clause: a transfer nobody measured is
// worth at most domain.AssumedLinkConfidence, and so is every reading of it the
// decision published. Nothing on the path answered, so the seconds are the
// fleet-wide constant divided into bytes, and confidence is the one field that
// says so.
//
// It is the clause that gives the other two teeth. A prediction that named its
// assumption truthfully and then presented the duration as certain would rank an
// unmeasured machine exactly where a machine that measured a gigabit path ranks,
// which is the outcome a fabricated measurement buys and the one the whole slice
// exists to stop. A prediction reaching for that is far likelier to arrive by
// raising an assumption's confidence than by inventing a source, because raising
// it looks like a tuning constant.
//
// The doubt the score charged is asked about by name rather than inferred from
// the estimate, because the decision carries the two separately and only one of
// them is what the ranking reads.
//
// Zero bytes is not judged here and cannot be: a stage with nothing to move
// records no rate at all, and a host an inventory says holds the content is
// certainly zero seconds from finishing.
func assumedRateIsWorthAGuess(decision domain.BookingDecision, candidate domain.CandidateDecision, rate domain.TransferRate) error {
	if rate.Assumption == "" {
		return nil
	}
	part, worth, overconfident := overconfidentGuess(rate, candidate, rate.Stage)
	if !overconfident {
		return nil
	}
	return fmt.Errorf(
		"Run %q: candidate %q priced its %s stage from %q, which nothing on this machine measured, and %s is worth %.2f where a duration over an unmeasured rate is worth at most %.2f",
		decision.RunID, candidate.OfferSnapshotID, rate.Stage, rate.Assumption, part, worth, domain.AssumedLinkConfidence,
	)
}

// overconfidentGuess names which reading of an unmeasured transfer claims more
// than a guess is worth. There are three of them and they are three separate
// mistakes.
//
// The rate is what a future caller of this model will divide by. The stage
// estimate is the answer this decision published about the duration, and a tree
// that stopped carrying the rate's confidence onto it would pass a rule that read
// only the rate. And the confidence the decision listed for that stage is what
// the score itself charges doubt from: domain.CandidateDecision.Confidences is
// what Uncertainty reads, it is built separately from the estimate rather than
// derived from it, and a rule stated over the estimate alone leaves the ranking
// reachable by editing the one function named for the score's own input.
func overconfidentGuess(rate domain.TransferRate, candidate domain.CandidateDecision, stage domain.LaunchStage) (string, float64, bool) {
	if rate.Confidence > domain.AssumedLinkConfidence {
		return "the rate itself", rate.Confidence, true
	}
	if estimate := candidate.Estimates.Stages.Stage(stage); estimate.Confidence > domain.AssumedLinkConfidence {
		return "the estimate it produced", estimate.Confidence, true
	}
	if scored := scoredConfidence(candidate, stage); scored > domain.AssumedLinkConfidence {
		return "the doubt the score charged for it", scored, true
	}
	return "", 0, false
}

// scoredConfidence is what this decision told its own score one stage's duration
// was worth. An answer nobody stated a confidence for is not listed at all and is
// charged no doubt, which is a silence rather than a claim of certainty, so it is
// not judged here: what a stage costs when nothing answered about it is the
// business of the rules about locality.
func scoredConfidence(candidate domain.CandidateDecision, stage domain.LaunchStage) float64 {
	for _, confidence := range candidate.Confidences {
		if confidence.Answer == stage.ConfidenceAnswer() {
			return confidence.Value
		}
	}
	return 0
}

func describeRateProvenance(rate domain.TransferRate) string {
	if rate.Measurement != "" && rate.Assumption != "" {
		return fmt.Sprintf("both the measurement %q and the assumption %q, which is a decision that cannot have been taken twice", rate.Measurement, rate.Assumption)
	}
	return "neither a measurement nor an assumption it was priced from"
}

func describeReportedPath(fact domain.NetworkFact, answered bool) string {
	if !answered {
		return "nothing its publisher stood behind was"
	}
	return fmt.Sprintf("%.2f Mbps was", fact.ValueMbps)
}

// onlyKeptCapacityHoldsWhatItRan holds the line the lane split draws. It names
// the reason the offer cannot have accumulated content, because a provisionable
// offer and a one-shot product fail this for different reasons: one is a
// machine that does not exist yet, the other exists only for its workload.
func onlyKeptCapacityHoldsWhatItRan(offer domain.OfferSnapshot, seeded, seededCopies map[string]bool) error {
	if offer.KeepsWhatItRuns() {
		return nil
	}
	reason := "is a machine that does not exist yet"
	if !offer.Lane.Reusable() {
		reason = "holds nothing once its workload exits"
	}
	for _, digest := range heldDigests(offer.Images) {
		if !seeded[digest] {
			return fmt.Errorf(
				"offer %q %s, and holds %s beyond what the World Tape seeded",
				offer.ID,
				reason,
				digest,
			)
		}
	}
	// Artifact copies obey the same rule as image content, and for the same
	// reason: a copy is local, and this machine is not somewhere local content
	// can outlive the workload that put it there. What the World Tape seeded is
	// admitted exactly as it is for images, because a machine Mercator borrows a
	// slot on may well already be sitting on the dataset; what it may not do is
	// accumulate a copy from something Mercator ran there.
	// A mutable cache obeys the rule for a third time, and with no seed to
	// exempt: no fixture can put one on capacity that keeps nothing, because a
	// cache exists only where a workload wrote it. An offer advertising one here
	// would have Placement counting warmth that cannot survive its own host.
	if len(offer.Caches.Mounts) > 0 {
		return fmt.Errorf(
			"offer %q %s, and holds cache %q for workspace %q",
			offer.ID,
			reason,
			offer.Caches.Mounts[0].Name,
			offer.Caches.Mounts[0].WorkspaceID,
		)
	}
	for _, replica := range offer.Artifacts.Replicas {
		if seededCopies[replica.ArtifactID] {
			continue
		}
		return fmt.Errorf(
			"offer %q %s, and holds a copy of Artifact %q beyond what the World Tape seeded",
			offer.ID,
			reason,
			replica.ArtifactID,
		)
	}
	return nil
}

func heldContentIsExplained(offer domain.OfferSnapshot, seeded, retained map[string]bool) error {
	for _, digest := range heldDigests(offer.Images) {
		if seeded[digest] || retained[digest] {
			continue
		}
		return fmt.Errorf(
			"offer %q holds %s with no World Tape seed and no content retained against that host",
			offer.ID,
			digest,
		)
	}
	return nil
}

// heldCopiesAreExplained is the Artifact half of the same question images
// answer through retention, and it is stricter, because the two kinds of content
// are kept by different things. A copy is on a machine because the World Tape
// declared it there or because a preparation Mercator issued landed it there, and
// a copy with neither is bytes from nowhere. Durability of the version answers a
// different question: it says the content exists, never that it exists HERE, and
// pricing a host warm for content nothing delivered to it is exactly the mistake
// a per-host rule exists to catch.
//
// A launch leaves no copy, which is why any landing at all is not enough. An
// image pull is a runtime operation and the image stays in that runtime's store
// afterwards; a Run reading its declared inputs is a workload reading into its own
// container, and nothing enumerates, hashes, or files that content. So a copy
// explained only by an execution is warmth the next Run cannot collect.
func heldCopiesAreExplained(offer domain.OfferSnapshot, seeded, prepared map[string]bool) error {
	for _, replica := range offer.Artifacts.Replicas {
		if seeded[replica.ArtifactID] || prepared[replica.ArtifactID] {
			continue
		}
		return fmt.Errorf(
			"offer %q holds a copy of Artifact %q with no World Tape seed and no preparation recorded landing one there",
			offer.ID,
			replica.ArtifactID,
		)
	}
	return nil
}

// preparedCopiesByOffer reads back which Artifact copies the ledger says a
// preparation of Mercator's landed on each host. Why the bytes moved is part of
// the question, because it is the only way a machine may come to hold a copy: a
// landing recorded against anything else is read here as no landing at all.
func preparedCopiesByOffer(effects []EffectRecord) (map[string]map[string]bool, error) {
	prepared := map[string]map[string]bool{}
	for _, effect := range effects {
		if effect.Operation != OperationArtifactReplicated || effect.Command != EffectCommandAccepted {
			continue
		}
		var landed struct {
			ArtifactID string `json:"artifact_id"`
			OfferID    string `json:"offer_id"`
			Source     string `json:"source"`
		}
		if err := json.Unmarshal(effect.Request, &landed); err != nil {
			return nil, fmt.Errorf("decode Artifact replication %s: %w", effect.ID, err)
		}
		if landed.Source != contentSourcePrewarm {
			continue
		}
		if prepared[landed.OfferID] == nil {
			prepared[landed.OfferID] = map[string]bool{}
		}
		prepared[landed.OfferID][landed.ArtifactID] = true
	}
	return prepared, nil
}

// retainedByOffer reads back what the effect ledger says each host kept. It
// reads retention rather than dispatch, because a pull states what a host will
// have once its bytes land, and a host that holds content before then holds
// content nothing has delivered. A one-shot execution leaves no retention at
// all, and neither does a launch onto a host that already held the image.
func retainedByOffer(effects []EffectRecord) (map[string]map[string]bool, error) {
	retained := map[string]map[string]bool{}
	for _, effect := range effects {
		if effect.Operation != OperationImageRetained || effect.Command != EffectCommandAccepted {
			continue
		}
		var host struct {
			OfferID string `json:"offer_id"`
		}
		var kept struct {
			RetainedDigests []string `json:"retained_digests"`
		}
		if err := json.Unmarshal(effect.Request, &host); err != nil {
			return nil, fmt.Errorf("decode retained content %s: %w", effect.ID, err)
		}
		if err := json.Unmarshal(effect.Consequence, &kept); err != nil {
			return nil, fmt.Errorf("decode retained content consequence %s: %w", effect.ID, err)
		}
		if retained[host.OfferID] == nil {
			retained[host.OfferID] = map[string]bool{}
		}
		for _, digest := range kept.RetainedDigests {
			retained[host.OfferID][digest] = true
		}
	}
	return retained, nil
}

func heldDigests(inventory domain.ImageInventory) []string {
	held := append(slices.Clone(inventory.ImageDigests), inventory.LayerDigests...)
	// A host that answers in diff IDs holds the same bytes as one that answers
	// in blob digests, and the ledger records content under every name it has.
	// Reading one space would let content arrive unexplained on any machine
	// whose runtime speaks the other.
	held = append(held, inventory.LayerDiffIDs...)
	// Content a host fetched and never assembled is content it holds. Whether
	// it can start on those bytes is the offer projection's business; where
	// they came from is this rule's, and they came from somewhere.
	return append(held, inventory.PulledImageDigests...)
}

func selectedCandidate(decision domain.BookingDecision) *domain.CandidateDecision {
	for index := range decision.Candidates {
		if decision.Candidates[index].OfferSnapshotID == decision.SelectedOfferSnapshotID {
			return &decision.Candidates[index]
		}
	}
	return nil
}

func monotonicVersions(observation InvariantObservation) error {
	streamVersions := map[string]uint64{}
	var global eventlog.GlobalPosition
	for _, event := range observation.MercatorEvents {
		if event.GlobalPosition <= global {
			return fmt.Errorf("global event position %d does not follow %d", event.GlobalPosition, global)
		}
		global = event.GlobalPosition
		previous := streamVersions[event.Subject]
		if event.StreamVersion != previous+1 {
			return fmt.Errorf("stream %q version %d does not follow %d", event.Subject, event.StreamVersion, previous)
		}
		streamVersions[event.Subject] = event.StreamVersion
	}
	return nil
}

func ownedExternalResources(observation InvariantObservation) error {
	for _, execution := range observation.World.ActiveExecutions {
		if execution.RunID == "" || execution.LaunchKey == "" || execution.ExternalID == "" {
			return fmt.Errorf("external execution lacks Run, launch, or external identity")
		}
	}
	return nil
}

// diskReservationRespected is the rule that makes disk a resource rather than a
// figure on an offer. Content Mercator puts on a machine has to fit there, and
// the name this replaced, cache_disk_accounting, accounted for no disk at all:
// it checked that a copy named a known Artifact and appeared once, and never
// compared a byte of what a machine held against what it had room for. Deleting
// the name is the point of the change, because a rule that promises accounting
// and performs none is worse than no rule, and every world it passed was a world
// nobody had checked.
//
// What it says is that a machine's own account of its disk adds up. Every item
// resident on it names content with a size, no item is counted twice, what is
// resident plus what is promised to content still arriving never exceeds the
// disk, and the copies and caches World Truth says are on a machine are exactly
// the ones taking up room in its account. That last clause is what stops the
// rule from being satisfied by a ledger that simply forgot a kind of content.
func diskReservationRespected(observation InvariantObservation) error {
	ledgers := map[string]DiskLedger{}
	for _, ledger := range observation.World.Disk {
		if _, twice := ledgers[ledger.OfferID]; twice {
			return fmt.Errorf("machine %q accounts for its disk twice", ledger.OfferID)
		}
		ledgers[ledger.OfferID] = ledger
		if err := residentContentIsAccountable(ledger); err != nil {
			return err
		}
		if used := ledger.ResidentBytes() + ledger.ReservedBytes; used > ledger.CapacityBytes {
			return fmt.Errorf(
				"machine %q holds and reserves %d bytes on a %d byte disk",
				ledger.OfferID, used, ledger.CapacityBytes,
			)
		}
	}
	return everythingHeldTakesUpRoom(observation, ledgers)
}

// residentContentIsAccountable is one machine's items read on their own terms.
// An item nothing names is an item nothing can be checked against, an item of no
// size is content this world cannot account for, and the same content listed
// twice is a disk that looks fuller than it is.
//
// There is no clause here about a machine with no disk holding content, because
// there is no world it would be the one to catch: every item has a size, so a
// machine holding anything at all on no disk is already over its capacity.
func residentContentIsAccountable(ledger DiskLedger) error {
	seen := map[string]bool{}
	for _, item := range ledger.Resident {
		key := string(item.Kind) + "/" + item.Name
		if item.Name == "" || item.SizeBytes <= 0 {
			return fmt.Errorf("machine %q holds %s content this world cannot size: %+v", ledger.OfferID, item.Kind, item)
		}
		if seen[key] {
			return fmt.Errorf("machine %q counts %s twice", ledger.OfferID, key)
		}
		seen[key] = true
	}
	return nil
}

// everythingHeldTakesUpRoom reads the two halves of World Truth against each
// other. A copy of an Artifact and a Cache Mount are bytes on a disk, so a
// machine that reports holding one and accounts for no room for it has lost
// track of its own disk, and an account naming content no machine holds is
// reserving room for nothing.
func everythingHeldTakesUpRoom(observation InvariantObservation, ledgers map[string]DiskLedger) error {
	held := map[string]bool{}
	for _, replica := range observation.World.ArtifactReplicas {
		held[replica.OfferID+"/artifact/"+replica.ArtifactID] = true
		if !ledgers[replica.OfferID].holds(ResidentArtifact, replica.ArtifactID) {
			return fmt.Errorf(
				"machine %q holds a copy of %q and accounts for no room for it",
				replica.OfferID, replica.ArtifactID,
			)
		}
	}
	for _, mount := range observation.World.CacheMounts {
		held[mount.OfferID+"/cache/"+mount.Identity] = true
		if !ledgers[mount.OfferID].holds(ResidentCache, mount.Identity) {
			return fmt.Errorf(
				"machine %q holds cache %q and accounts for no room for it",
				mount.OfferID, mount.Identity,
			)
		}
	}
	for _, ledger := range ledgers {
		for _, item := range ledger.Resident {
			if item.Kind != ResidentLayer && !held[ledger.OfferID+"/"+string(item.Kind)+"/"+item.Name] {
				return fmt.Errorf(
					"machine %q reserves room for %s %q and holds no such content",
					ledger.OfferID, item.Kind, item.Name,
				)
			}
		}
	}
	return nil
}

// cacheMountWorkspaceIsolation is the hard rule on mutable state. A Cache
// Mount's only identity is its workspace-scoped name, so isolation is not a
// preference the scheduler weighs: two tenants that both call a cache
// compiler-cache have two caches, and nothing may ever hand one of them the
// other's bytes.
//
// The rule is that no cache identity is ever observed under two workspaces, read
// over the ledger of what was touched and over what each host is holding. It is
// deliberately not stated as "the identity equals what this workspace, name, and
// generation derive": the world derives identities with the same function such a
// rule would check them against, so the one error that matters, a derivation
// that drops the workspace, would agree with itself and pass. Asking instead
// whether two tenants ever met on one identity is a question the derivation
// cannot answer for itself.
//
// Every attachment is claimed, not only the ones that wrote something: a cache
// opened under the wrong workspace has already leaked, whatever the workload
// went on to do with it.
//
// The rule reads what each execution asked for and what each host ended up
// holding, and deliberately nothing about which storage an attachment resolved
// to. Storage is reached by the identity itself, here and on a container runtime
// alike: a volume is named by the workspace, the cache, and the generation
// together, so the slot a read lands in is that string by construction and a
// consequence restating it could never disagree with the request beside it. What
// a wandering resolution would actually be is a derivation that stopped carrying
// the workspace, and that shows up here as two tenants claiming one identity.
func cacheMountWorkspaceIsolation(observation InvariantObservation) error {
	owners := map[string]string{}
	claim := func(offerID, identity, workspaceID, what string) error {
		if identity == "" || workspaceID == "" {
			return fmt.Errorf("%s on %q names cache identity %q for workspace %q", what, offerID, identity, workspaceID)
		}
		key := offerID + "/" + identity
		if owner, claimed := owners[key]; claimed && owner != workspaceID {
			return fmt.Errorf(
				"cache %q on %q is used by workspaces %q and %q, and a cache belongs to one workspace",
				identity, offerID, owner, workspaceID,
			)
		}
		owners[key] = workspaceID
		return nil
	}
	for _, effect := range observation.Effects {
		if effect.Operation != OperationCacheMountAttach {
			continue
		}
		var touched struct {
			Identity    string `json:"identity"`
			WorkspaceID string `json:"workspace_id"`
			OfferID     string `json:"offer_id"`
		}
		if err := json.Unmarshal(effect.Request, &touched); err != nil {
			return fmt.Errorf("decode Cache Mount access %s: %w", effect.ID, err)
		}
		if err := claim(touched.OfferID, touched.Identity, touched.WorkspaceID, effect.Operation); err != nil {
			return err
		}
	}
	for _, mount := range observation.World.CacheMounts {
		if err := claim(mount.OfferID, mount.Identity, mount.WorkspaceID, "holding"); err != nil {
			return err
		}
	}
	return nil
}

func projectionRebuildEquivalence(observation InvariantObservation) error {
	if !observation.ProjectionRebuildEquivalent {
		return fmt.Errorf("Run projection changed after rebuilding from the event log")
	}
	return nil
}

// secretsAbsent is the standing guard on what Mercator writes down. It reads the
// public event log and the Effect Ledger, which together are everything a Run
// Bundle exports and everything an operator can ask this control plane for.
//
// It holds three things, and the first one alone was a rule about vocabulary
// rather than about secrets. A field called credential, password, or secret is
// refused, which catches material somebody filed under an honest name. A signed
// URL is refused wherever it appears, because a presigned read is a bearer
// credential written as a location: recording the location a node was handed
// would put a working read of the object store into every export, and the query
// markers named here belong to no field name in this record. And a bootstrap
// credential this world minted is refused whatever it is filed under, which is
// the clause the name half of the rule cannot reach: an enrollment token in a
// field called enrollment_token passes every name check ever written, because
// the name is the truthful one.
//
// The last clause is stated over the credentials rather than over a shape,
// because a token has no shape. What makes a string a secret here is that this
// world handed it to a machine, and the world is the only thing that knows.
func secretsAbsent(observation InvariantObservation) error {
	forbiddenFields := [][]byte{
		[]byte(`"credential"`),
		[]byte(`"password"`),
		[]byte(`"secret"`),
	}
	signedReads := [][]byte{
		[]byte("x-amz-signature="),
		[]byte("x-amz-credential="),
		[]byte("x-goog-signature="),
		[]byte("&signature="),
		[]byte("?signature="),
	}
	for _, value := range []any{observation.MercatorEvents, observation.Effects} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		lower := bytes.ToLower(encoded)
		for _, field := range forbiddenFields {
			if bytes.Contains(lower, field) {
				return fmt.Errorf("recorded data contains forbidden secret field %s", field)
			}
		}
		for _, marker := range signedReads {
			if bytes.Contains(lower, marker) {
				return fmt.Errorf("recorded data contains a signed URL, which is a credential written as a location: %s", marker)
			}
		}
		for _, credential := range observation.BootstrapCredentials {
			// A credential with no material is a record the rule about bootstraps
			// refuses on its own. Searching for the empty string here would match
			// every record ever written, so this rule leaves it to the one that
			// names the problem.
			if credential.Token == "" {
				continue
			}
			if bytes.Contains(encoded, []byte(credential.Token)) {
				return fmt.Errorf(
					"recorded data contains the enrollment token %s was bootstrapped with, whatever field it is filed under",
					credential.NodeID,
				)
			}
		}
		// The material a machine was handed for one fetch is read the same way and
		// for the same reason. It is caught by the markers above only when it is a
		// signed URL and the marker happens to be one of the five named there; a
		// registry secret in a field called anything at all is a string this world
		// knows it minted and the record has no business carrying.
		for _, credential := range observation.ContentCredentials {
			if credential.Material == "" {
				continue
			}
			if bytes.Contains(encoded, []byte(credential.Material)) {
				return fmt.Errorf(
					"recorded data contains the material a machine was handed to fetch %s, whatever field it is filed under",
					credential.Content,
				)
			}
		}
	}
	return nil
}

// bootstrapCredentialIsShortLivedAndSingleUse is the law on the one credential a
// machine ever receives from outside itself. A bootstrap is how a host that
// Mercator has never spoken to proves it is the node it claims to be, and it is
// the whole of what an attacker needs to become that node, so three things hold
// of every one this world minted.
//
// One machine holds it. A credential carried to two accepted allocations is one
// invitation two hosts can enrol as, and the second is a machine Mercator would
// then address every command about the first to.
//
// It is redeemed once. This is what makes the credential short-lived in the only
// sense that matters: it stops being usable when it is used, rather than when
// somebody remembers to expire it. It is counted rather than flagged because the
// violation is the second redemption, and the store's own spend record and the
// signer's expiry are the two doors production refuses it at.
//
// It is never written down. The event log and the ledger are what a Run Bundle
// exports and what an operator can read back, so a token in either is a token in
// every copy of the record forever, long outliving the thirty minutes the
// invitation is redeemable for.
//
// The third clause is deliberately also held by safety.secrets_absent, which
// reads the same bytes for the same string. They are not the same rule: that one
// is about everything Mercator may not write and knows nothing about redemption,
// and this one is about the lifecycle of one credential and would still be the
// rule that fails if the record were clean and the token were spent twice.
func bootstrapCredentialIsShortLivedAndSingleUse(observation InvariantObservation) error {
	for _, credential := range observation.BootstrapCredentials {
		// A credential with no material is a record this rule cannot use, and
		// dropping it quietly is how the rule would weaken without anything saying
		// so: the clause below searches the record for the token, and the empty
		// string is in every record ever written.
		if credential.Token == "" {
			return fmt.Errorf(
				"the invitation minted for %q generation %d carries no material, and a bootstrap is the material a machine presents",
				credential.NodeID, credential.Generation,
			)
		}
		if credential.Provisions > 1 {
			return fmt.Errorf(
				"the bootstrap minted for %s generation %d was handed to %d machines, and each of them can enrol as that node",
				credential.NodeID, credential.Generation, credential.Provisions,
			)
		}
		if credential.Redemptions > 1 {
			return fmt.Errorf(
				"the bootstrap minted for %s generation %d was redeemed %d times, and an invitation is spent by redeeming it",
				credential.NodeID, credential.Generation, credential.Redemptions,
			)
		}
	}
	return recordedCredentials(observation)
}

// recordedCredentials is the clause about the record, read over the two halves of
// it Mercator publishes: its own event log, and the ledger of what really crossed
// into the world.
func recordedCredentials(observation InvariantObservation) error {
	for _, value := range []any{observation.MercatorEvents, observation.Effects} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		for _, credential := range observation.BootstrapCredentials {
			if bytes.Contains(encoded, []byte(credential.Token)) {
				return fmt.Errorf(
					"the bootstrap minted for %s generation %d appears in Mercator's own record, which outlives the invitation by the whole life of the record",
					credential.NodeID, credential.Generation,
				)
			}
		}
	}
	return nil
}

// admittedRunProgress is a Run Mercator accepted reaching an answer. It used to
// exempt anything in phase "queued", which was free while nothing could reach
// that phase and would have become a licence to starve the moment something
// could. The exemption is gone rather than narrowed, and what replaced it is
// liveness.aging_prevents_starvation: a queued Run is held to its own class's
// maximum queue delay, which is a much earlier bound than this one.
func admittedRunProgress(observation InvariantObservation) error {
	bound := 24 * time.Hour
	if observation.Now.Sub(observation.StartedAt) <= bound {
		return nil
	}
	for _, run := range observation.Runs {
		if run.Closed {
			continue
		}
		arrival := observation.RunRequirements[run.ID]
		if arrival.Name == "" {
			continue
		}
		return fmt.Errorf("Run %q exceeded %s without reaching a terminal state", run.ID, bound)
	}
	return nil
}

func lostResponseReconciliation(observation InvariantObservation) error {
	runs := runsByID(observation.Runs)
	active := map[string]bool{}
	for _, execution := range observation.World.ActiveExecutions {
		active[execution.RunID] = true
	}
	for _, effect := range observation.Effects {
		if effect.Response != EffectResponseLost {
			continue
		}
		run := runs[effect.CorrelationID]
		if !active[effect.CorrelationID] && run.ID == "" {
			return fmt.Errorf("lost response for %q has neither active consequence nor projected Run", effect.CorrelationID)
		}
	}
	return nil
}

func staleLeaseExpiry(observation InvariantObservation) error {
	const grace = 5 * time.Minute
	for _, execution := range observation.World.ActiveExecutions {
		if observation.Now.After(execution.CompletesAt.Add(grace)) {
			return fmt.Errorf("external execution %q survived %s beyond its deadline", execution.LaunchKey, grace)
		}
	}
	return nil
}

// orphanConvergence is the projection rule. Every execution this world is
// running is work Mercator launched, so a Run this control plane can no longer
// name is a projection that lost an execution rather than a provider that gained
// one, and the machine goes on running with nothing that will ever come for it.
//
// It reads executions and never the capacity a world declared orphaned. Those are
// deliberately different facts: an execution is Mercator's own and carries the
// identities Mercator minted for it, and capacity nobody recognises is the
// opposite statement, answered by the policy rule below.
func orphanConvergence(observation InvariantObservation) error {
	runs := runsByID(observation.Runs)
	for _, execution := range observation.World.ActiveExecutions {
		if runs[execution.RunID].ID == "" {
			return fmt.Errorf("external execution %q has no projected Run %q", execution.LaunchKey, execution.RunID)
		}
	}
	return nil
}

// orphanPolicyIsExplicit is the rule that makes reconciliation able to choose.
// Capacity Mercator does not recognise is either taken back into the fleet or
// destroyed, and whichever it was, the record names the policy that decided and
// the reason it applied.
//
// It is the half orphanConvergence has nothing to say about. That rule asks that
// no execution outlive the Run it belonged to, which is silent on what ought to
// happen to capacity that was never Mercator's execution at all, so the two are
// stated apart and a world holding an orphan is answered by this one.
//
// A machine still standing has not been decided about yet, which is not a
// violation: a control plane that has not swept is a control plane that has not
// looked. Converging one without a stated rule is the violation, and so is a
// decision naming no policy, no reason, or an outcome that is neither of the two
// an operator can act on.
func orphanPolicyIsExplicit(observation InvariantObservation) error {
	decided, err := recordedOrphanDecisions(observation.MercatorEvents)
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for _, orphan := range observation.World.Orphans {
		held[orphan.LaunchKey] = true
	}
	for _, identity := range slices.Sorted(maps.Keys(observation.SeededOrphans)) {
		if held[identity] {
			continue
		}
		if _, decision := decided[identity]; !decision {
			return fmt.Errorf(
				"orphaned capacity %q is gone from this world and no decision names the policy that took it",
				identity,
			)
		}
	}
	for _, identity := range slices.Sorted(maps.Keys(decided)) {
		if err := statesItsPolicy(identity, decided[identity]); err != nil {
			return err
		}
	}
	return nil
}

func statesItsPolicy(identity string, decision janitor.OrphanConvergence) error {
	switch {
	case decision.Policy == "":
		return fmt.Errorf("the decision about orphaned capacity %q names no policy", identity)
	case decision.Outcome != janitor.OrphanAdopted && decision.Outcome != janitor.OrphanTerminated:
		return fmt.Errorf(
			"orphaned capacity %q was converged as %q, which is neither adopting it nor terminating it",
			identity, decision.Outcome,
		)
	case decision.Reason == "":
		return fmt.Errorf("the decision about orphaned capacity %q gives no reason the policy applied", identity)
	default:
		return nil
	}
}

// recordedOrphanDecisions is every policy decision Mercator's public record holds
// about capacity it did not recognise, by the identity it decided about.
func recordedOrphanDecisions(events []eventlog.CloudEvent) (map[string]janitor.OrphanConvergence, error) {
	decided := map[string]janitor.OrphanConvergence{}
	for _, event := range events {
		if event.Type != janitor.EventOrphanConverged {
			continue
		}
		var convergence janitor.OrphanConvergence
		if err := json.Unmarshal(event.Data, &convergence); err != nil {
			return nil, fmt.Errorf("decode orphan convergence %s: %w", event.ID, err)
		}
		decided[convergence.LaunchKey] = convergence
	}
	return decided, nil
}

func supersededBookingRelease(observation InvariantObservation) error {
	runs := runsByID(observation.Runs)
	for rentalID, schedule := range observation.RentalSchedules {
		for _, scheduled := range schedule.Bookings {
			run := runs[scheduled.Booking.RunID]
			if run.ID == "" {
				return fmt.Errorf("Rental %q retains Booking %q for unknown Run", rentalID, scheduled.Booking.ID)
			}
			if run.Closed {
				return fmt.Errorf("Rental %q retains Booking %q for closed Run %q", rentalID, scheduled.Booking.ID, run.ID)
			}
		}
	}
	return nil
}

func runsByID(runs []domain.RunRecord) map[string]domain.RunRecord {
	indexed := make(map[string]domain.RunRecord, len(runs))
	for _, run := range runs {
		indexed[run.ID] = run
	}
	return indexed
}

func latestInvariantResults(results []InvariantResult) []InvariantResult {
	latest := map[string]InvariantResult{}
	for _, result := range results {
		latest[result.ID] = result
	}
	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	ordered := make([]InvariantResult, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, latest[id])
	}
	return ordered
}
