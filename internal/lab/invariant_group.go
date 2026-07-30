package lab

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the two bounds a caller declares that are about nothing on any
// machine: how wide its family of Runs may run at once, and whether its work may
// be taken away once it has started. Both are statements only the caller can make,
// both are enforced before anything runs, and both are checked here against what
// really happened rather than against what Mercator says it decided.
//
// They are read over the Effect Ledger, which is the world's own account, crossed
// with the workloads Mercator recorded. That crossing is the whole method: the
// ledger says which executions existed at once and which the world took away, and
// the recorded workload says what the caller declared about them. A rule stated
// over the control plane's own bookkeeping would be asking the counter whether it
// can count.

// groupParallelismRespected is the width every family declared, in the two halves
// it takes to state a bound the caller owns and Mercator carries.
//
// The first half is that the declaration arrived. A family is a bound on work
// Mercator was asked to do, so a submission that named one and a record that does
// not is a bound nothing can hold, whatever the executions then look like. It is
// the half that makes the rest falsifiable: the second half reads the family off
// the workload the control plane recorded, exactly as safety.artifact_dependencies
// reads a Run's inputs off it, and a translation that dropped the declaration on
// the way in would leave that reading with no family to police.
//
// The second half is the width itself, read over the launches that actually
// happened. It counts distinct Runs rather than launches, because a family's width
// is a bound on its members and one member relaunched after a machine refused it is
// still one member. It opens a member's interval at the launch the world accepted
// and closes it when the world says that execution is over: a release, a terminate,
// or a machine reclaimed underneath it. An execution still open at the end of the
// observation is still counted, which is the conservative direction and the honest
// one: the run is out there.
//
// A family whose members disagree about their own width is a violation of its own,
// because there is then no width to hold anything to. Nothing in Mercator registers
// a group in advance, which is deliberate: a group is a label the work carries. The
// price of that is exactly this, that the members have to agree, and the record is
// where the disagreement shows up.
func groupParallelismRespected(observation InvariantObservation) error {
	if err := everyDeclaredFamilyReachedTheRecord(observation); err != nil {
		return err
	}
	widths, err := declaredWidths(observation)
	if err != nil {
		return err
	}
	holding := map[string]map[string]bool{}
	for _, effect := range observation.Effects {
		if effect.Command != EffectCommandAccepted {
			continue
		}
		switch effect.Operation {
		case OperationProviderLaunch:
			group, member := declaredGroup(observation, effect.CorrelationID)
			if !member {
				continue
			}
			if holding[group.ID] == nil {
				holding[group.ID] = map[string]bool{}
			}
			holding[group.ID][effect.CorrelationID] = true
			if width := widths[group.ID]; len(holding[group.ID]) > width {
				return fmt.Errorf(
					"group %q declared that %d of its Runs may hold capacity at once, and at effect %d it held %d: %s",
					group.ID, width, effect.Sequence, len(holding[group.ID]), describeMembers(holding[group.ID]),
				)
			}
		case OperationProviderRelease, OperationProviderTerminate:
			group, member := declaredGroup(observation, effect.CorrelationID)
			if !member {
				continue
			}
			delete(holding[group.ID], effect.CorrelationID)
		case OperationCapacityPreempted:
			interrupted, err := interruptedRuns(effect)
			if err != nil {
				return err
			}
			for _, lost := range interrupted {
				group, member := declaredGroup(observation, lost.RunID)
				if !member {
					continue
				}
				delete(holding[group.ID], lost.RunID)
			}
		}
	}
	return nil
}

// everyDeclaredFamilyReachedTheRecord is the caller's declaration arriving intact.
// It is the one rule here that reads what was submitted rather than what Mercator
// holds, and it reads both: the claim is precisely that the two agree.
//
// A Run the control plane never accepted is skipped, because there is no bound to
// hold on work Mercator turned away at the door. A Run it accepted and recorded
// without the family it was submitted with is the violation, and it is the shape
// this bound spent a phase in: the group reached the World Tape, the translation
// into a workload had nowhere to put it, and every family in the corpus ran as wide
// as the fleet allowed with nothing in the tree able to say so.
func everyDeclaredFamilyReachedTheRecord(observation InvariantObservation) error {
	for _, runID := range slices.Sorted(maps.Keys(observation.RunRequirements)) {
		workload, accepted := observation.Workloads[runID]
		if !accepted {
			continue
		}
		declared := observation.RunRequirements[runID].Request.Group
		if recorded := workload.Spec.Placement.Group; recorded != declared {
			return fmt.Errorf(
				"Run %q was submitted into family %q at a width of %d and Mercator recorded family %q at a width of %d, so the bound its caller declared is one nothing here can hold",
				runID, declared.ID, declared.MaxParallel, recorded.ID, recorded.MaxParallel,
			)
		}
	}
	return nil
}

// declaredWidths is how wide each family in this execution said it may run, read
// off the workloads Mercator recorded. Members that disagree have declared no
// width at all, and that is reported here rather than resolved: taking either
// answer would hold the family to a bound half of it never asked for.
func declaredWidths(observation InvariantObservation) (map[string]int, error) {
	widths := map[string]int{}
	declaredBy := map[string]string{}
	for _, runID := range slices.Sorted(maps.Keys(observation.Workloads)) {
		group := observation.Workloads[runID].Spec.Placement.Group
		if !group.Declared() {
			continue
		}
		width, stated := widths[group.ID]
		if stated && width != group.MaxParallel {
			return nil, fmt.Errorf(
				"group %q was declared %d wide by Run %q and %d wide by Run %q, so it has no width to be held to",
				group.ID, width, declaredBy[group.ID], group.MaxParallel, runID,
			)
		}
		widths[group.ID] = group.MaxParallel
		declaredBy[group.ID] = runID
	}
	return widths, nil
}

// declaredGroup is the family Mercator recorded this Run as a member of. A Run the
// control plane holds no workload for is not treated as a member of anything here:
// safety.artifact_dependencies already convicts a launch whose workload Mercator
// cannot produce, and a second rule failing on the same record would say the same
// thing twice.
func declaredGroup(observation InvariantObservation, runID string) (domain.RunGroup, bool) {
	group := observation.Workloads[runID].Spec.Placement.Group
	return group, group.Declared()
}

func describeMembers(holding map[string]bool) string {
	return strings.Join(slices.Sorted(maps.Keys(holding)), ", ")
}

// interruptionWasPermitted is the other bound: no execution the world took away
// belonged to a class that forbids being interrupted.
//
// It reads the reclamation out of the ledger and the permission out of the
// workload, which is what makes it a rule about Mercator rather than about the
// world. Providers reclaim what they sold as reclaimable, and nothing Mercator does
// after the fact changes what happened to the work; the only decision it owns is
// whether to put the work there, and this is the law over that decision.
//
// A Run whose execution the world took away and whose workload Mercator cannot
// produce is a violation rather than something to skip. The class is the whole of
// the permission, so a record that cannot state it is a record in which no
// interruption was ever permitted.
func interruptionWasPermitted(observation InvariantObservation) error {
	for _, effect := range observation.Effects {
		if effect.Operation != OperationCapacityPreempted || effect.Command != EffectCommandAccepted {
			continue
		}
		interrupted, err := interruptedRuns(effect)
		if err != nil {
			return err
		}
		for _, lost := range interrupted {
			workload, recorded := observation.Workloads[lost.RunID]
			if !recorded {
				return fmt.Errorf(
					"Run %q was interrupted at effect %d and Mercator recorded no workload for it, so nothing says its class permitted that",
					lost.RunID, effect.Sequence,
				)
			}
			class := workload.Spec.Placement.Class
			if !class.Admission().PermitsInterruption {
				return fmt.Errorf(
					"Run %q of class %q was %s when the capacity it was placed on was reclaimed at effect %d, and its class does not permit interruption",
					lost.RunID, class, describeInterruption(lost.Started), effect.Sequence,
				)
			}
		}
	}
	return nil
}

// interruptedExecution is one execution a reclamation took away, as the world's
// own ledger states it.
type interruptedExecution struct {
	RunID string `json:"run_id"`
	// Started is whether the container had begun. It separates work that was lost
	// from a launch that only lost its machine, which is what the violation says
	// out loud: both are interruptions Mercator had to have permission for, and
	// they are not the same thing to have done to a caller.
	Started bool `json:"started"`
}

func interruptedRuns(effect EffectRecord) ([]interruptedExecution, error) {
	var consequence struct {
		Interrupted []interruptedExecution `json:"interrupted"`
	}
	if err := json.Unmarshal(effect.Consequence, &consequence); err != nil {
		return nil, fmt.Errorf("decode the reclamation at effect %d: %w", effect.Sequence, err)
	}
	return consequence.Interrupted, nil
}

func describeInterruption(started bool) string {
	if started {
		return "running"
	}
	return "waiting to start"
}
