package lab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

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
		invariantRule{id: "safety.locality_is_never_infeasibility", check: localityIsNeverInfeasibility},
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
// about. A measured start latency is established too, because that is a
// measurement about this offer whatever anyone could enumerate.
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
// recomputes what was discounted from the per-candidate localities and per-kind
// seconds the decision records independently of the answer it reached.
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

// pricedSilenceSeconds is what this candidate was charged for content nothing
// could describe, recomputed from the localities the decision recorded and the
// seconds it recorded per kind of content. The Artifact half converts bytes into
// seconds through the unreadable share of the read itself, so this rule holds no
// opinion about the rate the scheduler used and cannot be satisfied by agreeing
// with it.
func pricedSilenceSeconds(candidate domain.CandidateDecision) float64 {
	seconds := 0.0
	if candidate.ImageLocality == domain.LocalityUnknown {
		seconds += candidate.Estimates.PullSeconds.Expected
	}
	return seconds + candidate.Estimates.ArtifactSeconds.Expected*unreadableShare(candidate.ArtifactEvidence)
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
