package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

// This file is the admission stage. Before it, the pipeline the goal names went
// gather, filter, price, predict, score, commit, record, and had nothing between
// a Run being ready and Placement pricing candidates for it: a Run nothing could
// take returned an error, was retried on the next minute tick for ever, and left
// no trace of having waited. That implicit loop was the whole queue.
//
// Admission is the stage that was missing. It asks three questions in order, and
// each one is a stage of the same pipeline: what is already waiting, whether this
// Run may go past it, and whether this Run can still start in time. A Run that
// may not proceed appends why, so the queue is a thing an operator can read
// rather than a thing they can only infer from a Run that never starts.

// stepAdmit is admission: the queue in front of this Run, then Placement.
func (o *Orchestrator) stepAdmit(ctx context.Context, workspaceID, runID string, version uint64, state runState) (bool, error) {
	queue, err := o.admissionQueue(ctx, workspaceID)
	if err != nil {
		return false, err
	}
	waiting := queue.position(runID, state, o.now().UTC())
	if behind := queue.ahead(waiting); len(behind) > 0 {
		return false, o.recordDeferral(ctx, workspaceID, runID, version, state, waiting.deferral(domain.DeferredBehindHigherClass, behind))
	}
	return true, o.stepPlace(ctx, workspaceID, runID, version, state, waiting)
}

// admissionQueue is every Run in this workspace admission has already told to
// wait, built from the four public facts that put a Run in the queue and take it
// out again. It reads those facts rather than the Run read model on purpose: the
// read model is derived by reducing each Run's whole stream, so one Run carrying
// an event Mercator cannot read would stop every other Run in the workspace from
// being placed, and a queue nobody can join is worse than no queue at all.
//
// It is read once per admission rather than threaded between Runs, because each
// Run asks the same question of it and answers independently: a Run is admitted
// when nothing it may not pass is waiting, which is the same answer whatever
// order a sweep reaches the Runs in.
func (o *Orchestrator) admissionQueue(ctx context.Context, workspaceID string) (admissionQueue, error) {
	filter := eventlog.EventFilter{
		WorkspaceID: workspaceID,
		StreamTypes: []string{"run"},
		EventTypes:  []string{EventAdmissionDeferred, EventAdmissionRefused, EventBookingDecided, EventRunClosed},
	}
	head, err := o.log.LatestPosition(ctx, filter)
	if err != nil {
		return admissionQueue{}, fmt.Errorf("orchestrator: read the admission queue: %w", err)
	}
	waiting := map[string]waitingRun{}
	for event, err := range eventlog.ScanAll(ctx, o.log, head, filter) {
		if err != nil {
			return admissionQueue{}, fmt.Errorf("orchestrator: read the admission queue: %w", err)
		}
		if err := applyToQueue(waiting, event); err != nil {
			return admissionQueue{}, err
		}
	}
	queue := admissionQueue{waiting: slices.Collect(maps.Values(waiting))}
	slices.SortFunc(queue.waiting, func(left, right waitingRun) int {
		return strings.Compare(left.runID, right.runID)
	})
	return queue, nil
}

// applyToQueue is one public fact moving a Run into or out of the queue. Leaving
// is every way a Run stops waiting on a decision: it was placed, it was refused,
// or it is over.
func applyToQueue(waiting map[string]waitingRun, event eventlog.StoredEvent) error {
	switch event.Type {
	case EventAdmissionDeferred:
		var data admissionDeferredData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return fmt.Errorf("orchestrator: read the admission queue: %w", err)
		}
		if _, queued := waiting[event.StreamID]; queued {
			// The moment a wait started never moves. The class's own bound is
			// measured from it, and a Run told to wait for a second reason has
			// not started waiting again.
			return nil
		}
		waiting[event.StreamID] = waitingRun{runID: event.StreamID, class: data.Deferral.Class, since: event.OccurredAt.UTC()}
	case EventAdmissionRefused, EventRunClosed:
		delete(waiting, event.StreamID)
	case EventBookingDecided:
		var data struct {
			Decision struct {
				SelectedOfferSnapshotID string `json:"selected_offer_snapshot_id"`
			} `json:"decision"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return fmt.Errorf("orchestrator: read the admission queue: %w", err)
		}
		if data.Decision.SelectedOfferSnapshotID != "" {
			delete(waiting, event.StreamID)
		}
	}
	return nil
}

// admissionQueue is the work already waiting, which is the only thing a Run has
// to be ordered against. Work that is running is not in it: a machine is held by
// whoever is on it, and no priority takes that away.
type admissionQueue struct {
	waiting []waitingRun
}

// waitingRun is one Run in the queue: what class it is and when its wait began.
type waitingRun struct {
	runID string
	class domain.ServiceClass
	since time.Time
}

// queuePosition is one Run's standing in the queue at one moment: what its class
// says, how long it has waited, and what that is worth.
type queuePosition struct {
	runID    string
	class    domain.ServiceClass
	policy   domain.Admission
	at       time.Time
	queued   float64
	priority float64
}

func (queue admissionQueue) position(runID string, state runState, at time.Time) queuePosition {
	class := state.requested.Workload.Spec.Placement.Class
	policy := class.Admission()
	queued := 0.0
	if !state.queuedSince.IsZero() {
		queued = at.Sub(state.queuedSince).Seconds()
	}
	return queuePosition{
		runID:    runID,
		class:    class,
		policy:   policy,
		at:       at,
		queued:   queued,
		priority: policy.EffectivePriority(queued),
	}
}

// ahead is everything already waiting that this Run may not be admitted past.
//
// One rule and one exemption. A Run does not go past work worth more than it is,
// which is what stops a stream of urgent arrivals from stepping over everything
// that has been waiting. The exemption is the class's own declared backfill
// eligibility, and it is the only thing that grants it: taking capacity that is
// going spare is the whole point of a class that says waiting costs it nothing.
// It stops at a Run that has waited longer than its class allows, because
// capacity a starved Run is waiting for is not capacity going spare.
func (queue admissionQueue) ahead(run queuePosition) []domain.QueuedAhead {
	var behind []domain.QueuedAhead
	for _, other := range queue.waiting {
		if other.runID == run.runID {
			continue
		}
		policy := other.class.Admission()
		queued := run.at.Sub(other.since).Seconds()
		priority := policy.EffectivePriority(queued)
		if priority <= run.priority {
			continue
		}
		if run.policy.BackfillEligible && !policy.Starved(queued) {
			continue
		}
		behind = append(behind, domain.QueuedAhead{RunID: other.runID, Class: other.class, EffectivePriority: priority})
	}
	return behind
}

// deferral is this Run's standing written down as the record of one moment it
// was told to wait.
func (run queuePosition) deferral(reason string, behind []domain.QueuedAhead) domain.AdmissionDeferral {
	return domain.AdmissionDeferral{
		Reason:               reason,
		Class:                run.class,
		EffectivePriority:    run.priority,
		BasePriority:         run.policy.Priority,
		QueuedSeconds:        run.queued,
		MaxQueueDelaySeconds: run.policy.MaxQueueDelaySeconds,
		Behind:               behind,
	}
}

// deferOrRefuse is what admission does with a Run Placement would not place. It
// waits, unless its class states a moment it must have started by that the queue
// in front of it is already past, in which case waiting is a promise the record
// says cannot be kept and the Run is refused instead.
func (o *Orchestrator) deferOrRefuse(
	ctx context.Context,
	workspaceID, runID string,
	version uint64,
	state runState,
	run queuePosition,
	decision domain.BookingDecision,
) error {
	wait, projected := shortestProjectedWait(decision)
	deferral := run.deferral(domain.DeferredNoFeasibleOffer, workAhead(decision))
	deferral.ProjectedWaitSeconds = wait
	if run.policy.DeadlineUnreachable(run.queued, wait, projected) {
		deferral.Reason = domain.RefusedDeadlineUnreachable
		return o.recordRefusal(ctx, workspaceID, runID, version, state, deferral)
	}
	return o.recordDeferral(ctx, workspaceID, runID, version, state, deferral)
}

// shortestProjectedWait is the soonest the record says anything this Run was
// weighed against comes free, projected from Bookings Mercator itself holds. A
// decision whose candidates carried no schedule projected nothing, and the
// difference between that and a wait of zero is what the deadline rule refuses to
// guess over.
func shortestProjectedWait(decision domain.BookingDecision) (float64, bool) {
	shortest, projected := 0.0, false
	for _, candidate := range decision.Candidates {
		if candidate.RentalSchedule == nil {
			continue
		}
		seconds := candidate.RentalSchedule.ProjectedStartSeconds
		if !projected || seconds < shortest {
			shortest, projected = seconds, true
		}
	}
	return shortest, projected
}

// workAhead is what this Run is waiting behind, read off the decision Mercator
// recorded: the Runs whose Bookings hold the capacity its candidates were weighed
// against. Their effective priority is left at zero, which is not a ranking. Work
// that already holds a machine is ahead because it is there.
func workAhead(decision domain.BookingDecision) []domain.QueuedAhead {
	var ahead []domain.QueuedAhead
	seen := map[string]bool{}
	for _, candidate := range decision.Candidates {
		if candidate.RentalSchedule == nil {
			continue
		}
		runIDs := []string{}
		if running := candidate.RentalSchedule.Running; running != nil {
			runIDs = append(runIDs, running.RunID)
		}
		for _, waiting := range candidate.RentalSchedule.Preceding {
			runIDs = append(runIDs, waiting.RunID)
		}
		for _, runID := range runIDs {
			if runID == "" || seen[runID] {
				continue
			}
			seen[runID] = true
			ahead = append(ahead, domain.QueuedAhead{RunID: runID})
		}
	}
	slices.SortFunc(ahead, func(left, right domain.QueuedAhead) int {
		return strings.Compare(left.RunID, right.RunID)
	})
	return ahead
}

// recordDeferral appends why this Run is still waiting, and appends it only when
// the answer changed. A sweep asks this question every tick, and a Run waiting an
// hour would otherwise write sixty identical facts saying so: what an operator
// needs is what it is waiting for and since when, and both survive the
// suppression because the first deferral is always recorded.
func (o *Orchestrator) recordDeferral(
	ctx context.Context,
	workspaceID, runID string,
	version uint64,
	state runState,
	deferral domain.AdmissionDeferral,
) error {
	if state.deferral != nil && sameDeferral(*state.deferral, deferral) {
		return nil
	}
	return o.appendEvents(ctx, workspaceID, runID, version, "advance:admission_deferred:"+deferral.Reason, []eventlog.NewEvent{
		mustEvent(runID, admissionEventID(state), EventAdmissionDeferred, admissionDeferredData{Deferral: deferral}, o.now()),
	})
}

// recordRefusal closes a Run admission will not queue, loudly: the reason, the
// outcome, and the closure in one commit, so a caller learns its deadline cannot
// be met from the Run rather than from the Run never starting.
func (o *Orchestrator) recordRefusal(
	ctx context.Context,
	workspaceID, runID string,
	version uint64,
	state runState,
	refusal domain.AdmissionDeferral,
) error {
	events := []eventlog.NewEvent{
		mustEvent(runID, admissionEventID(state), EventAdmissionRefused, admissionDeferredData{Deferral: refusal}, o.now()),
		mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, runOutcomeRecordedData{Outcome: domain.RunOutcomeFailed}, o.now()),
		mustEvent(runID, "closed", EventRunClosed, runClosedData{Closed: true, Reason: refusal.Reason}, o.now()),
	}
	if state.bookingQueued() {
		return o.completeBookingAndAppend(ctx, workspaceID, runID, version, state, "advance:admission_refused", events)
	}
	return o.appendEvents(ctx, workspaceID, runID, version, "advance:admission_refused", events)
}

// admissionEventID numbers each deferral of one Run, because they are separate
// facts about separate moments and an event ID is unique within its stream.
func admissionEventID(state runState) string {
	return fmt.Sprintf("admission_%d", state.deferralCount+1)
}

// sameDeferral reports whether two deferrals say the same thing. The seconds and
// the priority are deliberately not compared: they move every tick by
// construction, and a rule that read them would defeat the suppression entirely.
func sameDeferral(recorded, next domain.AdmissionDeferral) bool {
	return recorded.Reason == next.Reason &&
		slices.EqualFunc(recorded.Behind, next.Behind, func(left, right domain.QueuedAhead) bool {
			return left.RunID == right.RunID
		})
}
