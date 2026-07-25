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
	SeededReplicas              map[string]map[string]bool
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
		invariantRule{id: "safety.cache_disk_accounting", check: cacheDiskAccounting},
		invariantRule{id: "safety.projection_rebuild_equivalence", check: projectionRebuildEquivalence},
		invariantRule{id: "safety.secrets_absent", check: secretsAbsent},
		invariantRule{id: "safety.ephemeral_capacity_not_reused", check: ephemeralCapacityNotReused},
		invariantRule{id: "safety.locality_provenance", check: localityProvenance},
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
		OperationImagePull,
		OperationImageRetained,
		OperationArtifactRead,
		OperationArtifactReplicated,
		OperationArtifactPublished,
		OperationCacheMountWrite,
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
		if replica.ContentDigest != version.ContentDigest {
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
	ephemeralRentals := map[string]string{}
	for _, event := range observation.MercatorEvents {
		if event.Type != orchestrator.EventBookingDecided {
			continue
		}
		var payload struct {
			Decision domain.BookingDecision `json:"decision"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return fmt.Errorf("decode Booking Decision from %s: %w", event.ID, err)
		}
		decision := payload.Decision
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
		if err := onlyKeptCapacityHoldsWhatItRan(offer, observation.SeededLocality[offer.ID]); err != nil {
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
func onlyKeptCapacityHoldsWhatItRan(offer domain.OfferSnapshot, seeded map[string]bool) error {
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
	// can outlive the workload that put it there. No World Tape seed is
	// admitted, because only a Rental can be declared holding one.
	if len(offer.Artifacts.Replicas) > 0 {
		return fmt.Errorf(
			"offer %q %s, and holds a copy of Artifact %q",
			offer.ID,
			reason,
			offer.Artifacts.Replicas[0].ArtifactID,
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

func cacheDiskAccounting(observation InvariantObservation) error {
	seenReplicas := map[string]bool{}
	for _, replica := range observation.World.ArtifactReplicas {
		key := replica.ArtifactID + "/" + replica.OfferID
		if replica.SizeBytes <= 0 || seenReplicas[key] {
			return fmt.Errorf("invalid Artifact replica %q", key)
		}
		seenReplicas[key] = true
	}
	seenMounts := map[string]bool{}
	for _, mount := range observation.World.CacheMounts {
		key := mount.OfferID + "/" + mount.Name
		if mount.Name == "" || mount.Revision == 0 || seenMounts[key] {
			return fmt.Errorf("invalid Cache Mount %q", key)
		}
		seenMounts[key] = true
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
