package scenario

import (
	"fmt"
	"slices"
	"time"

	"github.com/benngarcia/mercator/internal/orchestrator"
)

type ArrivalType string

const (
	ArrivalFixed    ArrivalType = "fixed"
	ArrivalPeriodic ArrivalType = "periodic"
	ArrivalBurst    ArrivalType = "burst"
)

// ArrivalPlan authors exogenous Run arrivals. Later schema-compatible fields
// add periodic and burst families without changing the execution seam.
type ArrivalPlan struct {
	Type     ArrivalType      `json:"type"`
	Runs     []RunArrivalSpec `json:"runs,omitempty"`
	Periodic *RunFamilySpec   `json:"periodic,omitempty"`
	Burst    *RunFamilySpec   `json:"burst,omitempty"`
	// Cancellations is work withdrawn after it arrived. A Run that goes away
	// before it ever executes is the one world in which speculative preparation
	// has to stop, and the corpus had no way to write it down: every arrival ran
	// or waited forever.
	Cancellations []RunCancellationSpec `json:"cancellations,omitempty"`
}

// RunCancellationSpec withdraws one declared Run at one moment.
type RunCancellationSpec struct {
	Run string   `json:"run"`
	At  Duration `json:"at"`
}

type RunArrivalSpec struct {
	Name string `json:"name"`
	// Workspace is the label of the tenant this Run belongs to. Empty means the
	// Blueprint's default workspace, which is where a single-tenant fixture puts
	// everything. It is a label rather than an ID because each backend mints its
	// own workspace identities, and a fixture that wrote one would be asserting
	// the Lab's naming as part of the public contract.
	Workspace string      `json:"workspace,omitempty"`
	At        Duration    `json:"at"`
	Request   RequestSpec `json:"request"`
}

type RunFamilySpec struct {
	NamePrefix string      `json:"name_prefix"`
	Workspace  string      `json:"workspace,omitempty"`
	At         Duration    `json:"at"`
	Interval   Duration    `json:"interval"`
	Count      int         `json:"count"`
	Request    RequestSpec `json:"request"`
}

type FaultAction string

const (
	FaultLoseResponse        FaultAction = "lose_response"
	FaultDelayResponse       FaultAction = "delay_response"
	FaultDuplicateResponse   FaultAction = "duplicate_response"
	FaultRejectCommand       FaultAction = "reject_command"
	FaultLoseCallback        FaultAction = "lose_callback"
	FaultDelayCallback       FaultAction = "delay_callback"
	FaultDuplicateCallback   FaultAction = "duplicate_callback"
	FaultReorderCallback     FaultAction = "reorder_callback"
	FaultRestartControlPlane FaultAction = "restart_control_plane"
)

type FaultSpec struct {
	ID      string           `json:"id"`
	Trigger FaultTriggerSpec `json:"trigger"`
	Action  FaultAction      `json:"action"`
	Delay   *Duration        `json:"delay,omitempty"`
}

type FaultTriggerSpec struct {
	Operation string `json:"operation,omitempty"`
	Event     string `json:"event,omitempty"`
	Run       string `json:"run,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
}

type ProofEvidence string

const (
	EvidenceProducerSubmitted          ProofEvidence = "producer_submitted"
	EvidenceExistingVsFreshCompared    ProofEvidence = "existing_vs_fresh_compared"
	EvidencePartialImageReuse          ProofEvidence = "partial_image_reuse"
	EvidenceCapacityPrepared           ProofEvidence = "capacity_prepared"
	EvidenceArtifactPublished          ProofEvidence = "artifact_published"
	EvidenceConsumerUnblocked          ProofEvidence = "consumer_unblocked"
	EvidenceWarmthObserved             ProofEvidence = "warmth_observed"
	EvidenceQueueVsFreshCompared       ProofEvidence = "queue_vs_fresh_compared"
	EvidenceAmbiguousDelivery          ProofEvidence = "ambiguous_delivery"
	EvidenceReconciledWithoutDuplicate ProofEvidence = "reconciled_without_duplicate"
	EvidenceControlPlaneRestarted      ProofEvidence = "control_plane_restarted"
	EvidenceRestartEquivalent          ProofEvidence = "restart_equivalent"
	EvidenceUIRendered                 ProofEvidence = "ui_rendered"
	EvidenceBundleReplayed             ProofEvidence = "bundle_replayed"
	EvidenceInvariantsPassed           ProofEvidence = "invariants_passed"
)

var knownProofEvidence = map[ProofEvidence]bool{
	EvidenceProducerSubmitted:          true,
	EvidenceExistingVsFreshCompared:    true,
	EvidencePartialImageReuse:          true,
	EvidenceCapacityPrepared:           true,
	EvidenceArtifactPublished:          true,
	EvidenceConsumerUnblocked:          true,
	EvidenceWarmthObserved:             true,
	EvidenceQueueVsFreshCompared:       true,
	EvidenceAmbiguousDelivery:          true,
	EvidenceReconciledWithoutDuplicate: true,
	EvidenceControlPlaneRestarted:      true,
	EvidenceRestartEquivalent:          true,
	EvidenceUIRendered:                 true,
	EvidenceBundleReplayed:             true,
	EvidenceInvariantsPassed:           true,
}

type ProofCheckpoint struct {
	Step     int           `json:"step"`
	Evidence ProofEvidence `json:"evidence"`
}

func (plan ArrivalPlan) validate(world WorldSpec) error {
	runs, err := plan.ExpandedRuns()
	if err != nil {
		return err
	}
	names := map[string]bool{}
	producers := map[string]string{}
	for _, arrival := range runs {
		if arrival.Name == "" {
			return fmt.Errorf("Run arrivals need a name")
		}
		if names[arrival.Name] {
			return fmt.Errorf("duplicate Run arrival %q", arrival.Name)
		}
		names[arrival.Name] = true
		if arrival.At.Duration() < 0 {
			return fmt.Errorf("Run arrival %q occurs before virtual time zero", arrival.Name)
		}
		if err := world.validRequest(arrival.Request); err != nil {
			return fmt.Errorf("Run arrival %q: %w", arrival.Name, err)
		}
		// An Artifact belongs to one workspace, and a Blueprint's catalog is
		// declared in the default one. A Run in another workspace naming one of
		// those versions would be a fixture implying content crosses a workspace,
		// which it never does, so it is refused at the door rather than silently
		// held by admission for a version its own workspace has no entry for.
		if arrival.Workspace != "" && len(arrival.Request.ConsumesArtifacts)+len(arrival.Request.ProducesArtifacts) > 0 {
			return fmt.Errorf(
				"Run arrival %q is in workspace %q and names an Artifact: an Artifact belongs to the workspace that declared it",
				arrival.Name, arrival.Workspace,
			)
		}
		for _, artifactID := range arrival.Request.ProducesArtifacts {
			if producer := producers[artifactID]; producer != "" {
				return fmt.Errorf("Artifact %q has both producer %q and %q", artifactID, producer, arrival.Name)
			}
			producers[artifactID] = arrival.Name
		}
	}
	// The catalog and the arrival plan each say where an Artifact comes from,
	// and a consumer waits for its producer's publication. Two documents
	// disagreeing about which Run that is would park a consumer behind a
	// publication nothing was ever going to make.
	for _, artifact := range world.Artifacts {
		if artifact.Prepublished() {
			continue
		}
		if producer := producers[artifact.ID]; producer != artifact.ProducedBy {
			return fmt.Errorf(
				"Artifact %q names producer %q, and Run %q is what publishes it",
				artifact.ID, artifact.ProducedBy, producer,
			)
		}
	}
	return plan.validateCancellations(runs)
}

// validateCancellations refuses a withdrawal that names work this plan never
// declared, or that arrives before the Run it withdraws. Both are fixtures that
// would silently do nothing.
func (plan ArrivalPlan) validateCancellations(runs []RunArrivalSpec) error {
	arrivals := make(map[string]Duration, len(runs))
	for _, arrival := range runs {
		arrivals[arrival.Name] = arrival.At
	}
	withdrawn := map[string]bool{}
	for _, cancellation := range plan.Cancellations {
		at, declared := arrivals[cancellation.Run]
		if !declared {
			return fmt.Errorf("cancellation names unknown Run %q", cancellation.Run)
		}
		if withdrawn[cancellation.Run] {
			return fmt.Errorf("Run %q is cancelled twice", cancellation.Run)
		}
		withdrawn[cancellation.Run] = true
		if cancellation.At.Duration() < at.Duration() {
			return fmt.Errorf("Run %q is cancelled before it arrives", cancellation.Run)
		}
	}
	return nil
}

func (plan ArrivalPlan) ExpandedRuns() ([]RunArrivalSpec, error) {
	switch plan.Type {
	case ArrivalFixed:
		if len(plan.Runs) == 0 || plan.Periodic != nil || plan.Burst != nil {
			return nil, fmt.Errorf("fixed arrival plans need Runs and no family")
		}
		return append([]RunArrivalSpec(nil), plan.Runs...), nil
	case ArrivalPeriodic:
		if len(plan.Runs) > 0 || plan.Periodic == nil || plan.Burst != nil {
			return nil, fmt.Errorf("periodic arrival plans need exactly one periodic family")
		}
		return expandRunFamily(*plan.Periodic, false)
	case ArrivalBurst:
		if len(plan.Runs) > 0 || plan.Burst == nil || plan.Periodic != nil {
			return nil, fmt.Errorf("burst arrival plans need exactly one burst family")
		}
		return expandRunFamily(*plan.Burst, true)
	default:
		return nil, fmt.Errorf("unknown arrival type %q", plan.Type)
	}
}

func expandRunFamily(family RunFamilySpec, burst bool) ([]RunArrivalSpec, error) {
	if family.NamePrefix == "" || family.Count <= 0 {
		return nil, fmt.Errorf("arrival families need a name_prefix and positive count")
	}
	if family.At.Duration() < 0 || family.Interval.Duration() < 0 {
		return nil, fmt.Errorf("arrival family timing cannot be negative")
	}
	if !burst && family.Interval.Duration() <= 0 {
		return nil, fmt.Errorf("periodic arrival families need a positive interval")
	}
	runs := make([]RunArrivalSpec, family.Count)
	for index := range runs {
		at := family.At.Duration()
		if !burst {
			at += time.Duration(index) * family.Interval.Duration()
		}
		runs[index] = RunArrivalSpec{
			Name:      fmt.Sprintf("%s-%03d", family.NamePrefix, index+1),
			Workspace: family.Workspace,
			At:        Duration(at),
			Request:   family.Request,
		}
	}
	return runs, nil
}

// Workspaces is every workspace label this plan's Runs belong to, in sorted
// order, with the default workspace always first. A backend creates one
// workspace per label, which is what lets one Blueprint state two tenants.
func (plan ArrivalPlan) Workspaces() []string {
	runs, err := plan.ExpandedRuns()
	if err != nil {
		return []string{""}
	}
	labels := []string{""}
	for _, arrival := range runs {
		if arrival.Workspace != "" && !slices.Contains(labels, arrival.Workspace) {
			labels = append(labels, arrival.Workspace)
		}
	}
	slices.Sort(labels)
	return labels
}

func (plan ArrivalPlan) runNames() map[string]bool {
	runs, err := plan.ExpandedRuns()
	if err != nil {
		return nil
	}
	names := make(map[string]bool, len(runs))
	for _, arrival := range runs {
		names[arrival.Name] = true
	}
	return names
}

func validateFaults(faults []FaultSpec, runs map[string]bool) error {
	ids := map[string]bool{}
	for _, fault := range faults {
		if fault.ID == "" {
			return fmt.Errorf("faults need an id")
		}
		if ids[fault.ID] {
			return fmt.Errorf("duplicate fault %q", fault.ID)
		}
		ids[fault.ID] = true
		if fault.Trigger.Run != "" && !runs[fault.Trigger.Run] {
			return fmt.Errorf("fault %q references unknown Run %q", fault.ID, fault.Trigger.Run)
		}
		switch fault.Action {
		case FaultLoseResponse,
			FaultDuplicateResponse,
			FaultRejectCommand,
			FaultLoseCallback,
			FaultDuplicateCallback,
			FaultReorderCallback:
			if fault.Trigger.Operation == "" {
				return fmt.Errorf("fault %q needs trigger.operation", fault.ID)
			}
			if fault.Delay != nil {
				return fmt.Errorf("fault %q action %q does not accept delay", fault.ID, fault.Action)
			}
		case FaultDelayResponse, FaultDelayCallback:
			if fault.Trigger.Operation == "" {
				return fmt.Errorf("fault %q needs trigger.operation", fault.ID)
			}
			if fault.Delay == nil || fault.Delay.Duration() <= 0 {
				return fmt.Errorf("fault %q action %q needs a positive delay", fault.ID, fault.Action)
			}
		case FaultRestartControlPlane:
			if fault.Trigger.Event == "" {
				return fmt.Errorf("fault %q needs trigger.event", fault.ID)
			}
			if !orchestrator.IsRunEventType(fault.Trigger.Event) {
				return fmt.Errorf("fault %q triggers on unknown event %q", fault.ID, fault.Trigger.Event)
			}
			if fault.Delay != nil {
				return fmt.Errorf("fault %q action %q does not accept delay", fault.ID, fault.Action)
			}
		default:
			return fmt.Errorf("fault %q has unknown action %q", fault.ID, fault.Action)
		}
	}
	return nil
}

func validateProof(checkpoints []ProofCheckpoint) error {
	seen := map[ProofEvidence]bool{}
	for index, checkpoint := range checkpoints {
		if checkpoint.Step != index+1 {
			return fmt.Errorf("proof checkpoint at index %d has step %d, want %d", index, checkpoint.Step, index+1)
		}
		if !knownProofEvidence[checkpoint.Evidence] {
			return fmt.Errorf("proof checkpoint %d has unknown evidence %q", checkpoint.Step, checkpoint.Evidence)
		}
		if seen[checkpoint.Evidence] {
			return fmt.Errorf("proof evidence %q appears more than once", checkpoint.Evidence)
		}
		seen[checkpoint.Evidence] = true
	}
	return nil
}
