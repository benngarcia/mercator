package lab

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

// This file is the three laws the admission queue has to hold: that waiting ends,
// that it ends in the order the classes declared, and that nothing is made to wait
// behind a wait nobody can end.
//
// They are stated over the public record rather than over the control plane's
// own bookkeeping. The queue Mercator orders against is built inside a
// placement; these read the facts it appended afterwards, which is the only
// account an operator has and the only one a Run Bundle carries.

// longestClassQueueDelay is the furthest out any class states it may be kept
// waiting, derived from the classes rather than restated beside them. A bound
// written here by hand would silently stop covering a class whose own bound
// somebody later lengthened.
func longestClassQueueDelay() time.Duration {
	var longest time.Duration
	for _, class := range domain.KnownServiceClasses {
		bound := time.Duration(class.Admission().MaxQueueDelaySeconds) * time.Second
		longest = max(longest, bound)
	}
	return longest
}

// agingPreventsStarvation is the promise every class's maximum queue delay makes:
// a Run Mercator accepted and told to wait is admitted inside its own class's
// bound, however much work of a higher class arrives behind it.
//
// It replaced the exemption that let a Run sit in phase "queued" for ever inside
// admitted_run_progress. That exemption was written when nothing could reach the
// queued phase, so it cost nothing and read as a reasonable carve-out; the moment
// a Run could actually be queued it would have made starvation the one thing the
// liveness rules explicitly permit.
//
// It is stated against the read model rather than against the event log because
// what it is about is the state a Run is in now, and the projection is where
// Mercator says what that is. A Run that left the queue at any point is not
// starving whatever happened to it since.
func agingPreventsStarvation(observation InvariantObservation) error {
	for _, run := range observation.Runs {
		if run.Closed || run.Admission == nil || run.QueuedSince == nil {
			continue
		}
		waited := observation.Now.Sub(run.QueuedSince.UTC())
		bound := run.ServiceClass.Admission().MaxQueueDelaySeconds
		if bound <= 0 || waited.Seconds() <= bound {
			continue
		}
		return fmt.Errorf(
			"Run %q of class %q has waited %s, which is past the %.0fs its class allows, behind %s",
			run.ID, run.ServiceClass, waited, bound, describeQueuedAhead(run.Admission.Behind),
		)
	}
	return nil
}

func describeQueuedAhead(ahead []domain.QueuedAhead) string {
	if len(ahead) == 0 {
		return "nothing the record names"
	}
	names := make([]string, 0, len(ahead))
	for _, waiting := range ahead {
		names = append(names, waiting.RunID)
	}
	return strings.Join(names, ", ")
}

// serviceClassAdmissionOrder is the ordering the classes declare, checked against
// what Mercator actually admitted. No Run is placed while work worth more than it
// is sits in the queue waiting for capacity, and the one exemption is the admitted
// Run's own class declaring itself eligible to backfill: capacity going spare may be
// taken, and a Run already kept waiting longer than its class allows is not spare
// capacity.
//
// Waiting for capacity is what the ordering is over, and it is why the queue this
// replays holds only the Runs whose wait something can end. A Run every machine in
// the fleet was weighed against and none of them could take is waiting for capacity
// to be added, and nothing that fits the fleet as it stands is being admitted past
// it: they are not waiting for the same thing. A rule that counted it would forbid
// running work on an idle machine, and liveness.aging_prevents_starvation forbids
// leaving it there, so the two laws would contradict each other on the one world
// where a fleet holds a Run it can never place.
//
// Which Runs those are is read off the fleet each deferral records rather than off
// the reason beside it, for the reason
// safety.nothing_waits_behind_an_impossible_ask states: the reason is the answer
// under examination.
//
// The queue is replayed out of the public log rather than read off the read
// model, because the question is about a moment that has passed. What matters is
// who was waiting when the decision was taken, and the projection only ever says
// who is waiting now.
func serviceClassAdmissionOrder(observation InvariantObservation) error {
	queue := map[string]queuedRun{}
	for _, event := range observation.MercatorEvents {
		runID := strings.TrimPrefix(event.Subject, "runs/")
		switch event.Type {
		case orchestrator.EventAdmissionDeferred:
			deferral, err := recordedDeferral(event)
			if err != nil {
				return err
			}
			at, err := eventOccurredAt(event)
			if err != nil {
				return err
			}
			// The moment a wait began never moves, and what the Run is waiting for
			// does: a Run whose fleet filled up while it waited is waiting for
			// something the ordering has to respect, and one whose fleet emptied is
			// not.
			held := queue[runID]
			if held.since.IsZero() {
				held.since = at
			}
			held.class = deferral.Class
			held.heldByNothing = deferral.HoldsNoQueue(held.heldByNothing)
			queue[runID] = held
		case orchestrator.EventAdmissionRefused, orchestrator.EventRunClosed:
			delete(queue, runID)
		case orchestrator.EventBookingDecided:
			decision, err := recordedDecision(event)
			if err != nil {
				return err
			}
			// A decision that selected nothing admitted nothing, and there is
			// no moment to order anything against. It is also the shape every
			// synthetic Booking Decision in this tree carries, so asking for its
			// timestamp first would make this rule fail on records it has
			// nothing to say about.
			if decision.SelectedOfferSnapshotID == "" {
				continue
			}
			at, err := eventOccurredAt(event)
			if err != nil {
				return err
			}
			if err := admittedInClassOrder(observation, queue, decision.RunID, at); err != nil {
				return err
			}
			delete(queue, decision.RunID)
		}
	}
	return nil
}

// queuedRun is one Run waiting at one point in the replay.
type queuedRun struct {
	class domain.ServiceClass
	since time.Time
	// heldByNothing is what the record last established about the fleet's answer
	// to this Run: asked about it, and holding no machine that could ever take it.
	// A Run in that state is waiting for capacity to be added, so the ordering is
	// not over it.
	heldByNothing bool
}

func admittedInClassOrder(observation InvariantObservation, queue map[string]queuedRun, runID string, at time.Time) error {
	class := observation.Workloads[runID].Spec.Placement.Class
	policy := class.Admission()
	admitted := 0.0
	if held, waiting := queue[runID]; waiting {
		admitted = at.Sub(held.since).Seconds()
	}
	priority := policy.EffectivePriority(admitted)
	for _, other := range slices.Sorted(maps.Keys(queue)) {
		if other == runID {
			continue
		}
		held := queue[other]
		if held.heldByNothing {
			continue
		}
		waited := at.Sub(held.since).Seconds()
		otherPolicy := held.class.Admission()
		otherPriority := otherPolicy.EffectivePriority(waited)
		if otherPriority <= priority {
			continue
		}
		if policy.BackfillEligible && !otherPolicy.Starved(waited) {
			continue
		}
		return fmt.Errorf(
			"Run %q of class %q was admitted at effective priority %.2f while %q of class %q had waited %.0fs to %.2f",
			runID, class, priority, other, held.class, waited, otherPriority,
		)
	}
	return nil
}

// nothingWaitsBehindAnImpossibleAsk is the other half of what the queue is for.
// Work is ordered behind work that outranks it, and never behind a wait nobody can
// end: a Run the fleet was asked about and answered with no machine that could ever
// take it is waiting for capacity to be added, and work that fits the fleet as it
// stands is not competing with it for anything.
//
// Without it one impossible submission empties a workspace. The Run that fits is
// ordered behind an ask nothing can satisfy, and it stays there until the
// impossible Run's own class deadline clears it, which for a class that declares no
// deadline is never.
//
// What makes a Run impossible is the evidence its own deferral carries, read
// through the one rule production orders the queue on. Two readings of it is what
// this law had before, and they disagreed in both directions at once: the Lab could
// not see a fleet that published nothing at all, and production called that fleet
// an ordinary wait, so the strongest impossible ask there is was invisible to the
// corpus and holding every workspace it landed in. The reason code is not read here
// at all, and the rule it is derived from is.
//
// So this law is about the ordering, which is what its name says. A Run named as
// work ahead is a Run the record has to say something can be waited for on, and a
// bug anywhere between the queue Mercator rebuilds from the log and the deferral it
// writes fails it.
//
// It is replayed out of the public log rather than read off the read model because
// it is a rule about the moment a decision was taken: what matters is what Mercator
// had already recorded about the Run it named as ahead, and the projection only
// says what is true now.
func nothingWaitsBehindAnImpossibleAsk(observation InvariantObservation) error {
	impossible := map[string]bool{}
	for _, event := range observation.MercatorEvents {
		runID := strings.TrimPrefix(event.Subject, "runs/")
		switch event.Type {
		case orchestrator.EventAdmissionDeferred:
			deferral, err := recordedDeferral(event)
			if err != nil {
				return err
			}
			if err := nothingAheadIsImpossible(impossible, runID, deferral); err != nil {
				return err
			}
			impossible[runID] = deferral.HoldsNoQueue(impossible[runID])
		case orchestrator.EventAdmissionRefused, orchestrator.EventRunClosed:
			delete(impossible, runID)
		case orchestrator.EventBookingDecided:
			decision, err := recordedDecision(event)
			if err != nil {
				return err
			}
			if decision.SelectedOfferSnapshotID != "" {
				delete(impossible, decision.RunID)
			}
		}
	}
	return nil
}

func nothingAheadIsImpossible(impossible map[string]bool, runID string, deferral domain.AdmissionDeferral) error {
	for _, ahead := range deferral.Behind {
		if !impossible[ahead.RunID] {
			continue
		}
		return fmt.Errorf(
			"Run %q of class %q was told it waits behind %q, and the record already said every machine this fleet published was weighed against %q and none of them can hold it",
			runID, deferral.Class, ahead.RunID, ahead.RunID,
		)
	}
	return nil
}

func recordedDeferral(event eventlog.CloudEvent) (domain.AdmissionDeferral, error) {
	var payload struct {
		Deferral domain.AdmissionDeferral `json:"deferral"`
	}
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return domain.AdmissionDeferral{}, fmt.Errorf("decode admission deferral from %s: %w", event.ID, err)
	}
	return payload.Deferral, nil
}

func recordedDecision(event eventlog.CloudEvent) (domain.BookingDecision, error) {
	var payload struct {
		Decision domain.BookingDecision `json:"decision"`
	}
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return domain.BookingDecision{}, fmt.Errorf("decode Booking Decision from %s: %w", event.ID, err)
	}
	return payload.Decision, nil
}

func eventOccurredAt(event eventlog.CloudEvent) (time.Time, error) {
	at, err := time.Parse(time.RFC3339Nano, event.Time)
	if err != nil {
		return time.Time{}, fmt.Errorf("read the moment %s happened: %w", event.ID, err)
	}
	return at.UTC(), nil
}
