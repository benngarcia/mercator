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
	Prewarm                     *scenario.PrewarmSpec
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
		invariantRule{id: "safety.ephemeral_capacity_not_reused", check: ephemeralCapacityNotReused},
		invariantRule{id: "safety.locality_provenance", check: localityProvenance},
		invariantRule{id: "safety.transfer_rate_is_attributed", check: transferRateIsAttributed},
		invariantRule{id: "safety.locality_is_never_infeasibility", check: localityIsNeverInfeasibility},
		invariantRule{id: "safety.score_is_reproducible_from_the_record", check: scoreIsReproducibleFromTheRecord},
		invariantRule{id: "safety.promised_start_is_still_ahead", check: promisedStartIsStillAhead},
		invariantRule{id: "safety.prewarm_yields_to_real_work", check: prewarmYieldsToRealWork},
		invariantRule{id: "safety.prewarm_rate_within_bound", check: prewarmRateWithinBound},
		invariantRule{
			id:          "liveness.lost_response_reconciliation",
			assumptions: []string{"the provider preserves operation identity", "provider observation remains available"},
			bound:       5 * time.Minute,
			check:       lostResponseReconciliation,
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
		OperationNodePrepareImage,
		OperationNodePrepareArtifact,
		OperationNodePrepareAbandoned,
		OperationImagePull,
		OperationImageRetained,
		OperationArtifactRead,
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
// the object store, and no Run reads a copy nothing checked against the catalog.
//
// "Traces back to the object store" has exactly two shapes, and the second is
// why the rule is not simply "the version is durable": a copy was fetched from a
// publication, or it is the output the producing Run wrote on its way to
// becoming one. A copy of a version nothing published and no Run produced there
// is content from nowhere, which is what a replica standing in for an authority
// looks like.
func artifactReplicaVerified(observation InvariantObservation) error {
	produced, err := locallyProducedReplicas(observation.Effects)
	if err != nil {
		return err
	}
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
		if !version.Durable() && !produced[replica.ArtifactID+"/"+replica.OfferID] {
			return fmt.Errorf(
				"offer %q holds a copy of Artifact %q, which nothing published and no Run produced there",
				replica.OfferID, replica.ArtifactID,
			)
		}
	}
	return artifactReadsWereVerified(observation.Effects)
}

// locallyProducedReplicas is every copy a Run wrote where it ran, keyed by
// version and host. It is the one legitimate reason a copy can exist before the
// object store holds anything.
func locallyProducedReplicas(effects []EffectRecord) (map[string]bool, error) {
	produced := map[string]bool{}
	for _, effect := range effects {
		if effect.Operation != OperationArtifactReplicated || effect.Command != EffectCommandAccepted {
			continue
		}
		var request struct {
			ArtifactID string `json:"artifact_id"`
			OfferID    string `json:"offer_id"`
			Source     string `json:"source"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			return nil, fmt.Errorf("decode Artifact replication %s: %w", effect.ID, err)
		}
		if request.Source == "run_output" {
			produced[request.ArtifactID+"/"+request.OfferID] = true
		}
	}
	return produced, nil
}

func artifactReadsWereVerified(effects []EffectRecord) error {
	for _, effect := range effects {
		if effect.Operation != OperationArtifactRead || effect.Command != EffectCommandAccepted {
			continue
		}
		var request struct {
			ArtifactID string `json:"artifact_id"`
			OfferID    string `json:"offer_id"`
		}
		var read struct {
			Source string                      `json:"source"`
			State  domain.ArtifactReplicaState `json:"state"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			return fmt.Errorf("decode Artifact read %s: %w", effect.ID, err)
		}
		if err := json.Unmarshal(effect.Consequence, &read); err != nil {
			return fmt.Errorf("decode Artifact read consequence %s: %w", effect.ID, err)
		}
		if read.Source == "replica" && !read.State.Usable() {
			return fmt.Errorf(
				"Run %q read Artifact %q from a %q copy on offer %q, which nothing checked against the catalog",
				effect.CorrelationID, request.ArtifactID, read.State, request.OfferID,
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
// itself. Every stage of every recorded candidate names the level its answer
// came from and how many measured launches stand behind it, and an answer
// claiming this exact candidate names a key that is not the listing it arrived
// on.
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
// nobody has watched this happen.
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
// well as against the listing, because those are the two ways an exact-candidate
// answer goes wrong: filed under something that does not recur, or filed under
// some other candidate's key entirely.
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
	if !candidate.Candidate.Recurs() {
		return fmt.Errorf(
			"Run %q answered candidate %q's %s stage at level %q under %q, and nothing about this capacity outlives its listing",
			runID, candidate.OfferSnapshotID, stage, answer.Level, answer.Key,
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
	if answer.Level != domain.LevelExactCandidate {
		return nil
	}
	if own := candidate.Candidate.Candidate(contentStage(stage)); answer.Key != own {
		return fmt.Errorf(
			"Run %q answered candidate %q's %s stage out of %q, and this candidate is %q",
			runID, candidate.OfferSnapshotID, stage, answer.Key, own,
		)
	}
	return nil
}

// contentStage is which stages carry the content in their key, stated here
// rather than read from the estimator. A rule that asked the predictor which key
// it should have used would be the predictor agreeing with itself: this is the
// Lab's own account of which durations are a property of what was pulled and
// which are a property of the machine.
func contentStage(stage domain.LaunchStage) bool {
	switch stage {
	case domain.StageImageFetch, domain.StageUnpack, domain.StageArtifactFetch, domain.StageApplicationReady:
		return true
	default:
		return false
	}
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
	var decisions []domain.BookingDecision
	for _, event := range observation.MercatorEvents {
		if event.Type != orchestrator.EventBookingDecided {
			continue
		}
		var payload struct {
			Decision domain.BookingDecision `json:"decision"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return nil, fmt.Errorf("decode Booking Decision from %s: %w", event.ID, err)
		}
		decisions = append(decisions, payload.Decision)
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
	replicated, err := replicatedByOffer(observation.Effects)
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
		if err := heldCopiesAreExplained(offer, observation.SeededReplicas[offer.ID], replicated[offer.ID]); err != nil {
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
// answer through retention. A copy is on a machine because the World Tape
// declared it there or because the ledger says content landed there, and a copy
// with neither is bytes from nowhere. Durability of the version answers a
// different question: it says the content exists, never that it exists HERE, and
// pricing a host warm for content nothing delivered to it is exactly the mistake
// a per-host rule exists to catch.
func heldCopiesAreExplained(offer domain.OfferSnapshot, seeded, replicated map[string]bool) error {
	for _, replica := range offer.Artifacts.Replicas {
		if seeded[replica.ArtifactID] || replicated[replica.ArtifactID] {
			continue
		}
		return fmt.Errorf(
			"offer %q holds a copy of Artifact %q with no World Tape seed and nothing recorded landing there",
			offer.ID,
			replica.ArtifactID,
		)
	}
	return nil
}

// replicatedByOffer reads back which Artifact copies the ledger says landed on
// each host, whether a fetch delivered them or the Run that produced them wrote
// them there.
func replicatedByOffer(effects []EffectRecord) (map[string]map[string]bool, error) {
	replicated := map[string]map[string]bool{}
	for _, effect := range effects {
		if effect.Operation != OperationArtifactReplicated || effect.Command != EffectCommandAccepted {
			continue
		}
		var landed struct {
			ArtifactID string `json:"artifact_id"`
			OfferID    string `json:"offer_id"`
		}
		if err := json.Unmarshal(effect.Request, &landed); err != nil {
			return nil, fmt.Errorf("decode Artifact replication %s: %w", effect.ID, err)
		}
		if replicated[landed.OfferID] == nil {
			replicated[landed.OfferID] = map[string]bool{}
		}
		replicated[landed.OfferID][landed.ArtifactID] = true
	}
	return replicated, nil
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

func secretsAbsent(observation InvariantObservation) error {
	forbidden := [][]byte{
		[]byte(`"credential"`),
		[]byte(`"password"`),
		[]byte(`"secret"`),
	}
	for _, value := range []any{observation.MercatorEvents, observation.Effects} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		lower := bytes.ToLower(encoded)
		for _, token := range forbidden {
			if bytes.Contains(lower, token) {
				return fmt.Errorf("recorded data contains forbidden secret field %s", token)
			}
		}
	}
	return nil
}

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
		if run.Phase != "queued" {
			return fmt.Errorf("Run %q exceeded %s without terminal or explicit queued state", run.ID, bound)
		}
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

func orphanConvergence(observation InvariantObservation) error {
	runs := runsByID(observation.Runs)
	for _, execution := range observation.World.ActiveExecutions {
		if runs[execution.RunID].ID == "" {
			return fmt.Errorf("external execution %q has no projected Run %q", execution.LaunchKey, execution.RunID)
		}
	}
	return nil
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
