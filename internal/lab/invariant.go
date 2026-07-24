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
	StartedAt                   time.Time
	Now                         time.Time
	Transition                  uint64
	Blueprint                   string
	World                       WorldTruthSnapshot
	MercatorEvents              []eventlog.CloudEvent
	Effects                     []EffectRecord
	Runs                        []domain.RunRecord
	RentalSchedules             map[string]domain.RentalSchedule
	RunRequirements             map[string]RunArrival
	KnownArtifactIDs            map[string]bool
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
		invariantRule{id: "safety.monotonic_versions", check: monotonicVersions},
		invariantRule{id: "safety.owned_external_resources", check: ownedExternalResources},
		invariantRule{id: "safety.cache_disk_accounting", check: cacheDiskAccounting},
		invariantRule{id: "safety.projection_rebuild_equivalence", check: projectionRebuildEquivalence},
		invariantRule{id: "safety.secrets_absent", check: secretsAbsent},
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
		for index, scheduled := range schedule.Bookings {
			booking := scheduled.Booking
			if booking.ScheduleVersion != schedule.Version {
				return fmt.Errorf("Rental %q Booking %q has schedule version %d, want %d", rentalID, booking.ID, booking.ScheduleVersion, schedule.Version)
			}
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
		OperationArtifactPut,
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

func artifactDependencies(observation InvariantObservation) error {
	replicas := map[string]bool{}
	for _, replica := range observation.World.ArtifactReplicas {
		replicas[replica.ArtifactID+"/"+replica.OfferID] = true
	}
	for _, execution := range observation.World.ActiveExecutions {
		arrival := observation.RunRequirements[execution.RunID]
		for _, artifactID := range arrival.Request.ConsumesArtifacts {
			if !replicas[artifactID+"/"+execution.OfferID] {
				return fmt.Errorf("Run %q started on %q without Artifact %q", execution.RunID, execution.OfferID, artifactID)
			}
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
		if !observation.KnownArtifactIDs[replica.ArtifactID] || replica.SizeBytes <= 0 || seenReplicas[key] {
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
