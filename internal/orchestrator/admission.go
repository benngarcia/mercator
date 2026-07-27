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
		deferral := waiting.deferral(domain.DeferredBehindHigherPriority, behind)
		return false, o.deferOrRefuse(ctx, workspaceID, runID, version, state, waiting, admissionAnswer{deferral: deferral})
	}
	return o.stepPlace(ctx, workspaceID, runID, version, state, waiting)
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
		// The moment a wait started never moves. The class's own bound is measured
		// from it, and a Run told to wait for a second reason has not started
		// waiting again. What it is waiting for does move, and the latest answer is
		// the one the queue is ordered against.
		queued := waiting[event.StreamID]
		if queued.since.IsZero() {
			queued.since = event.OccurredAt.UTC()
		}
		queued.runID = event.StreamID
		queued.class = data.Deferral.Class
		queued.holdsNoQueue = data.Deferral.HoldsNoQueue(queued.holdsNoQueue)
		waiting[event.StreamID] = queued
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

// waitingRun is one Run in the queue: what class it is, when its wait began, and
// whether work behind it has to wait for it.
type waitingRun struct {
	runID string
	class domain.ServiceClass
	since time.Time
	// holdsNoQueue is whether this Run's wait is one other work does not have to
	// respect, as domain.AdmissionDeferral.HoldsNoQueue reads it off each deferral
	// in turn.
	//
	// It is stated as the exemption rather than as the rule so that the zero value
	// is the rule. A Run whose record says nothing about the fleet holds the queue
	// like every other wait, and only the fleet's own answer may take that away.
	holdsNoQueue bool
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
// One rule and two exemptions. A Run does not go past work worth more than it is,
// which is what stops a stream of urgent arrivals from stepping over everything
// that has been waiting.
//
// The first exemption is the class's own declared backfill eligibility, and it is
// the only thing that grants it: taking capacity that is going spare is the whole
// point of a class that says waiting costs it nothing. It stops at a Run that has
// waited longer than its class allows, because capacity a starved Run is waiting
// for is not capacity going spare.
//
// The second is a Run that holds no queue at all: one the fleet was asked about
// and answered with no machine that could ever take it. Ordering work behind it is
// ordering it behind a wait for capacity nobody has, which leaves a machine idle
// beside work that fits it and stalls a workspace on one impossible submission
// until that Run's own deadline clears it.
func (queue admissionQueue) ahead(run queuePosition) []domain.QueuedAhead {
	var behind []domain.QueuedAhead
	for _, other := range queue.waiting {
		if other.runID == run.runID || other.holdsNoQueue {
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

// deferOrRefuse is what admission does with a Run it will not admit now. It
// waits, unless its class states a moment it must have started by that the wait
// in front of it is already past, in which case waiting is a promise the record
// says cannot be kept and the Run is refused instead.
//
// It is asked of every wait and not only of the one Placement caused. A Run held
// behind work that outranks it is waiting exactly as much as a Run no machine
// would take, and admission that checked the deadline on one and not the other
// would keep a Run queued for ever past the moment its own class says the answer
// stopped being worth having.
func (o *Orchestrator) deferOrRefuse(
	ctx context.Context,
	workspaceID, runID string,
	version uint64,
	state runState,
	run queuePosition,
	answer admissionAnswer,
) error {
	if run.policy.DeadlineUnreachable(run.queued, answer.deferral.ProjectedWaitSeconds, answer.projected) {
		answer.deferral.Reason = domain.RefusedDeadlineUnreachable
		return o.recordRefusal(ctx, workspaceID, runID, version, state, answer)
	}
	return o.recordDeferral(ctx, workspaceID, runID, version, state, answer)
}

// admissionAnswer is one answer admission reached about a Run it will not admit
// now: the wait it puts the Run in, whether anything projected an end to that
// wait, and the Booking Decision the answer was read off.
//
// The decision travels with the wait because the two are one fact recorded
// together. Before this, a Run Placement weighed the whole fleet for and placed
// nowhere recorded a reason code and a count of machines: no candidate, no
// rejection, no schedule the wait was projected from, and nothing at all for the
// rules that read Booking Decisions. A Run held behind work that outranks it
// carries none, because nothing weighed a machine on its behalf, and the queue it
// is in is the whole of what happened to it.
type admissionAnswer struct {
	deferral  domain.AdmissionDeferral
	projected bool
	decision  *domain.BookingDecision
}

// evidence is the decision this answer rests on, written as the fact it is, ahead
// of the admission event that cites it.
func (answer admissionAnswer) evidence(runID string, now time.Time) []eventlog.NewEvent {
	if answer.decision == nil {
		return nil
	}
	return []eventlog.NewEvent{decisionEvent(runID, *answer.decision, now)}
}

// placementDeferral is the wait a decision that selected nothing puts a Run in:
// which of the two waits it is, what the record says is in front of it, and the
// soonest anything that could hold this Run comes free.
//
// Everything it answers is answered over the machines that could hold this Run
// once the capacity they are spending comes back, and never over the whole
// candidate set. A machine that can never take this Run is not a wait it is in:
// naming it as work ahead tells an operator to wait for a machine that will
// refuse the Run again, projecting a start from it decides this Run's deadline on
// somebody else's runtime, and counting it as a queue this Run is in is what let
// one impossible ask empty a fleet the moment anything else was running.
// The reason is derived from that answer rather than decided beside it, so the
// word an operator reads and the classification the queue is ordered on are one
// fact. A fleet that published nothing an ask matches was the case where the two
// came apart: the strongest refusal a fleet can give was labelled as a wait for
// capacity to come free.
func placementDeferral(run queuePosition, decision domain.BookingDecision) (domain.AdmissionDeferral, bool) {
	waitable := candidatesThatCouldHold(decision)
	wait, projected := shortestProjectedWait(waitable)
	answer := domain.FleetAnswer{Weighed: len(decision.Candidates), CouldHold: len(waitable)}
	reason := domain.DeferredNoFeasibleOffer
	if answer.HoldsNothing() {
		reason = domain.DeferredNoCapacityFits
	}
	deferral := run.deferral(reason, workAhead(waitable))
	deferral.ProjectedWaitSeconds = wait
	deferral.Fleet = &answer
	return deferral, projected
}

// candidatesThatCouldHold is the machines this decision weighed that could take
// this Run when whatever they are spending now comes back. It is the whole fleet
// as far as a wait is concerned, and the rest of the candidate set is a record of
// machines this Run is not competing for.
func candidatesThatCouldHold(decision domain.BookingDecision) []domain.CandidateDecision {
	var waitable []domain.CandidateDecision
	for _, candidate := range decision.Candidates {
		if candidate.CouldHoldOnceFree() {
			waitable = append(waitable, candidate)
		}
	}
	return waitable
}

// shortestProjectedWait is the soonest the record says anything that could hold
// this Run comes free, projected from Bookings Mercator itself holds. A candidate
// that carried no schedule projected nothing, and the difference between that and
// a wait of zero is what the deadline rule refuses to guess over.
func shortestProjectedWait(candidates []domain.CandidateDecision) (float64, bool) {
	shortest, projected := 0.0, false
	for _, candidate := range candidates {
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
// recorded: the Runs whose Bookings hold the capacity that could otherwise have
// taken it. Their effective priority is left at zero, which is not a ranking.
// Work that already holds a machine is ahead because it is there.
func workAhead(candidates []domain.CandidateDecision) []domain.QueuedAhead {
	var ahead []domain.QueuedAhead
	seen := map[string]bool{}
	for _, candidate := range candidates {
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
//
// The decision it was read off is appended with it, and is suppressed with it. A
// Run waiting an hour on a fleet that keeps saying the same thing would otherwise
// write sixty decisions nobody asked a different question of, and the evidence for
// the wait an operator is reading is the evidence recorded when the answer last
// changed.
func (o *Orchestrator) recordDeferral(
	ctx context.Context,
	workspaceID, runID string,
	version uint64,
	state runState,
	answer admissionAnswer,
) error {
	if state.deferral != nil && sameAnswer(state, answer) {
		return nil
	}
	events := append(answer.evidence(runID, o.now()),
		mustEvent(runID, admissionEventID(state), EventAdmissionDeferred, admissionDeferredData{Deferral: answer.deferral}, o.now()),
	)
	return o.appendEvents(ctx, workspaceID, runID, version, admissionCommand(state, "deferred"), events)
}

// recordRefusal closes a Run admission will not queue, loudly: the reason, the
// outcome, and the closure in one commit, so a caller learns its deadline cannot
// be met from the Run rather than from the Run never starting.
func (o *Orchestrator) recordRefusal(
	ctx context.Context,
	workspaceID, runID string,
	version uint64,
	state runState,
	answer admissionAnswer,
) error {
	events := append(answer.evidence(runID, o.now()),
		mustEvent(runID, admissionEventID(state), EventAdmissionRefused, admissionDeferredData{Deferral: answer.deferral}, o.now()),
		mustEvent(runID, "outcome_recorded", EventRunOutcomeRecorded, runOutcomeRecordedData{Outcome: domain.RunOutcomeFailed}, o.now()),
		mustEvent(runID, "closed", EventRunClosed, runClosedData{Closed: true, Reason: answer.deferral.Reason}, o.now()),
	)
	command := admissionCommand(state, "refused")
	if state.bookingQueued() {
		return o.completeBookingAndAppend(ctx, workspaceID, runID, version, state, command, events)
	}
	return o.appendEvents(ctx, workspaceID, runID, version, command, events)
}

// admissionEventID numbers each deferral of one Run, because they are separate
// facts about separate moments and an event ID is unique within its stream.
func admissionEventID(state runState) string {
	return fmt.Sprintf("admission_%d", state.deferralCount+1)
}

// admissionCommand names the command one admission fact is appended under, and it
// carries the same number the fact does.
//
// A key that named only the reason was spent on the first Run this reason applied
// to. The second time admission said the same thing about a changed queue, the
// append replayed a command key against a different request hash, the event log
// refused it as an idempotency conflict, and AdvanceRun returned that error to
// every caller for as long as the state held: the refresh answered 502, the sweep
// logged it each tick, and the Run's own record stayed frozen at the stale answer.
// Recording the nth admission decision about a Run is a distinct command from
// recording the (n-1)th, so the key says which one it is.
func admissionCommand(state runState, outcome string) string {
	return "advance:" + admissionEventID(state) + ":" + outcome
}

// sameAnswer reports whether admission has already recorded this answer: the same
// wait, read off the same verdict about the same fleet.
//
// Both halves are needed because the deferral is a label and the decision is the
// evidence, and every law about Placement reads the evidence. Comparing the two
// labels alone dropped decisions whose candidate set had changed underneath them: a
// Run waiting on NO_CAPACITY_FITS has an empty Behind list by construction and a
// reason that does not move, so a machine that arrived while it waited and was
// struck out for something a rule exists to forbid was named in a decision nothing
// ever appended, and the rule reads recorded decisions.
func sameAnswer(state runState, answer admissionAnswer) bool {
	return sameDeferral(*state.deferral, answer.deferral) &&
		sameFleetVerdict(state.bookingDecision, answer.decision)
}

// sameFleetVerdict compares the fleet's verdict where this answer rests on one. A
// Run held behind work that outranks it weighed no machine at all, so it has no
// verdict of its own for anything to have changed.
func sameFleetVerdict(recorded, next *domain.BookingDecision) bool {
	if next == nil {
		return true
	}
	if recorded == nil {
		return false
	}
	return recorded.FleetVerdict() == next.FleetVerdict()
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
