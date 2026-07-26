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

// This file is the two laws the admission queue has to hold: that waiting ends,
// and that it ends in the order the classes declared.
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
// is sits in the queue, and the one exemption is the admitted Run's own class
// declaring itself eligible to backfill: capacity going spare may be taken, and a
// Run already kept waiting longer than its class allows is not spare capacity.
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
			if _, waiting := queue[runID]; !waiting {
				queue[runID] = queuedRun{class: deferral.Class, since: at}
			}
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
