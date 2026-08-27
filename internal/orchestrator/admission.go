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

// stepAdmit is admission: the width its own family declared, the queue in front
// of this Run, the moment its class says it must have started by, then Placement.
//
// The family is asked first because it is the only one of the three that no
// ordering and no machine can change. A Run whose group is already as wide as its
// caller said may not be placed on the idlest fleet in the world, so recording it
// as waiting behind work that outranks it would name the wrong cause, and letting
// it hold the queue would stop unrelated work for a bound that has nothing to do
// with capacity.
//
// The deadline is asked on both ways out. A Run being told to wait is asked by
// deferOrRefuse below, which records what was holding it. A Run nothing is holding
// is asked here, because the deadline used to bound only waiting: a Run whose
// capacity arrived after its own moment was placed by the very pass that should
// have refused it, so it spent the money to produce an answer nobody was waiting
// for, and how long a Run could overshoot by was however long the sweep interval
// is.
//
// What the deadline decides here and what the refusal is named are two questions,
// and Admission.BoundAlreadyBroken answers the second for both doors, off the wait
// and never off the answer of the moment. The deadline is the only bound that may
// stop a Run on its way to a machine, because the queue delay bounds waiting and
// this Run has stopped waiting. A wait Mercator caused that reached the deadline is
// a wait that broke the class's queue delay first, though, so naming the deadline
// here said the later of two broken promises and left the earlier one out of the
// only record the caller gets.
//
// The whole stage is one decision at a time, because it is the one
// stage that reads a fact about every Run and writes to a single stream. A Run's
// own version guards everything else it does, and the log refuses an append made
// against a version somebody else has spent; a family's width has no such guard,
// so a sweep submitted all at once had every member replay a queue none of the
// others were in yet, and a family declared one wide took as many machines as it
// had members. Ordering is always this Run's lock and then the deployment's, which
// is the order AdvanceRun already holds them in.
func (o *Orchestrator) stepAdmit(ctx context.Context, runID string, version uint64, state runState) (bool, error) {
	o.admissionLock.Lock()
	defer o.admissionLock.Unlock()

	queue, err := o.admissionQueue(ctx)
	if err != nil {
		return false, err
	}
	waiting := queue.position(runID, state, o.now().UTC())
	if siblings := queue.familyHolding(waiting); len(siblings) > 0 {
		deferral := waiting.deferral(domain.DeferredGroupAtParallelism, siblings)
		return false, o.deferOrRefuse(ctx, runID, version, state, waiting, admissionAnswer{deferral: deferral})
	}
	if behind := queue.ahead(waiting); len(behind) > 0 {
		deferral := waiting.deferral(domain.DeferredBehindHigherPriority, behind)
		return false, o.deferOrRefuse(ctx, runID, version, state, waiting, admissionAnswer{deferral: deferral})
	}
	if waiting.policy.DeadlinePassed(waiting.wait.Seconds) {
		refusal := waiting.deferral(waiting.policy.BoundAlreadyBroken(waiting.wait), nil)
		return false, o.recordRefusal(ctx, runID, version, state, admissionAnswer{deferral: refusal})
	}
	return o.stepPlace(ctx, runID, version, state, waiting)
}

// admissionQueue is every Run admission has already told to
// wait, built from the five public facts that put a Run in the queue or in the
// count of the capacity a family holds, and take it out of either again.
//
// It reads those facts rather than the Run read model on purpose: the
// read model is derived by reducing each Run's whole stream, so one Run carrying
// an event Mercator cannot read would stop every other Run in the deployment from
// being placed, and a queue nobody can join is worse than no queue at all.
//
// It is read once per admission rather than threaded between Runs, because each
// Run asks the same question of it and answers independently: a Run is admitted
// when nothing it may not pass is waiting, which is the same answer whatever
// order a sweep reaches the Runs in.
func (o *Orchestrator) admissionQueue(ctx context.Context) (admissionQueue, error) {
	filter := eventlog.EventFilter{

		StreamTypes: []string{"run"},
		EventTypes: []string{
			EventAdmissionDeferred, EventAdmissionRefused, EventBookingDecided, EventLaunchFailed, EventRunClosed,
		},
	}
	head, err := o.log.LatestPosition(ctx, filter)
	if err != nil {
		return admissionQueue{}, fmt.Errorf("orchestrator: read the admission queue: %w", err)
	}
	replay := queueReplay{
		waiting: map[string]waitingRun{},
		began:   map[string]time.Time{},
		holding: map[string]domain.RunGroup{},
	}
	for event, err := range eventlog.ScanAll(ctx, o.log, head, filter) {
		if err != nil {
			return admissionQueue{}, fmt.Errorf("orchestrator: read the admission queue: %w", err)
		}
		if err := replay.apply(event); err != nil {
			return admissionQueue{}, err
		}
	}
	queue := admissionQueue{
		waiting: slices.Collect(maps.Values(replay.waiting)),
		holding: replay.holding,
	}
	slices.SortFunc(queue.waiting, func(left, right waitingRun) int {
		return strings.Compare(left.runID, right.runID)
	})
	return queue, nil
}

// queueReplay is the two facts the log keeps about waiting, because a Run's
// standing rests on two. Membership of the queue is who is waiting on a decision
// now. The moment each Run's wait began outlives its membership, exactly as
// runState.queuedSince does: it is set at the first deferral and nothing revises
// it, and every bound the class states is measured from it.
//
// Keeping only membership ranked one Run two ways at once. A Run deferred, placed,
// and told to wait again is one wait with a placement in the middle of it, and its
// own door read the whole wait while every other Run in the deployment read it as an
// arrival that had waited nothing. So it went on ageing toward a queue delay
// measured from a moment nobody else could see, and fresh work of a higher class
// was admitted past a Run that outranked it. Nothing reaches that today, because a
// replacement that finds no machine closes the Run and a Booking past its latest
// start is not yet re-placed, and both readings are already in the tree.
type queueReplay struct {
	waiting map[string]waitingRun
	began   map[string]time.Time
	// holding is the family of every Run that has taken capacity and not given it
	// back, which is what a group's declared width is counted over.
	//
	// A placement counts rather than an execution, and the difference is what makes
	// the bound hold. A member given a queued Booking behind somebody else's work is
	// not running yet and admission will never ask about it again, so counting only
	// what is running would let a family commit six machines and then run six.
	// a-family-place-is-taken-by-a-member-that-waits-its-turn is that world.
	//
	// Every fact that puts a Run in here or takes it out is a fact about capacity, and
	// that is deliberate. It used to leave on a deferral instead, on the argument that
	// admission is only ever asked of a Run that still needs a machine. That was true
	// of the one path in the tree that re-admits a Run, and it was an argument about
	// today's control flow standing in for a fact in the log: widening replacement to
	// a launch whose side effect nobody can determine, or re-placing a Booking past
	// its latest start, would re-admit a Run whose container may still be running and
	// quietly let its family commit a second machine. A Booking is given back in the
	// same commit as the launch failure that ended it, so the log says when the
	// capacity went, and this reads that rather than inferring it.
	holding map[string]domain.RunGroup
}

// apply is one public fact moving a Run into or out of the queue, or into and out of
// the capacity its family is counted on. Leaving the queue is every way a Run stops
// waiting on a decision: it was placed, it was refused, or it is over, and only the
// last two end the wait itself. Leaving the count is the capacity going back: the
// launch that failed, the refusal, or the end of the Run.
func (replay queueReplay) apply(event eventlog.StoredEvent) error {
	switch event.Type {
	case EventAdmissionDeferred:
		var data admissionDeferredData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return fmt.Errorf("orchestrator: read the admission queue: %w", err)
		}
		if _, waiting := replay.began[event.StreamID]; !waiting {
			replay.began[event.StreamID] = event.OccurredAt.UTC()
		}
		// What a Run is waiting for moves, and the latest answer is the one the
		// queue is ordered against.
		replay.waiting[event.StreamID] = waitingRun{
			runID:        event.StreamID,
			class:        data.Deferral.Class,
			since:        replay.began[event.StreamID],
			holdsNoQueue: data.Deferral.HoldsNoQueue(),
		}
	case EventLaunchFailed:
		// A launch that failed is a Booking given back: the Run's capacity is completed
		// in the same commit as this fact, on both paths that record one. So a member
		// whose machine refused to start the work no longer holds its family's place,
		// and leaving it counted would hold a sibling behind a machine nobody has until
		// something asks about this Run again. A launch nobody can determine the outcome
		// of records a different fact and keeps its capacity, which is the reading this
		// one exists to keep honest.
		delete(replay.holding, event.StreamID)
	case EventAdmissionRefused, EventRunClosed:
		delete(replay.waiting, event.StreamID)
		delete(replay.began, event.StreamID)
		delete(replay.holding, event.StreamID)
	case EventBookingDecided:
		var data struct {
			Decision struct {
				SelectedOfferSnapshotID string                 `json:"selected_offer_snapshot_id"`
				Policy                  domain.PlacementPolicy `json:"policy"`
			} `json:"decision"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return fmt.Errorf("orchestrator: read the admission queue: %w", err)
		}
		if data.Decision.SelectedOfferSnapshotID == "" {
			return nil
		}
		delete(replay.waiting, event.StreamID)
		// The family is read off the decision rather than off the Run's workload,
		// because the decision is what this replay already reads and because it is
		// the record of what Mercator took the Run to be when it gave it capacity.
		if data.Decision.Policy.Group.Declared() {
			replay.holding[event.StreamID] = data.Decision.Policy.Group
		}
	}
	return nil
}

// admissionQueue is the work already waiting, which is the only thing a Run has
// to be ordered against. Work that is running is not in it: a machine is held by
// whoever is on it, and no priority takes that away.
type admissionQueue struct {
	waiting []waitingRun
	// holding is every Run in this deployment that has been given capacity and
	// belongs to a family, by Run ID. It is the complement of the queue above: that
	// is the work still owed an answer, and this is the work that has one, which is
	// what a family's declared width is counted over.
	holding map[string]domain.RunGroup
}

// waitingRun is one Run in the queue: what class it is, when its wait began, and
// whether work behind it has to wait for it.
type waitingRun struct {
	runID string
	class domain.ServiceClass
	// since is the moment admission first told this Run to wait, which is the
	// number every other Run in the deployment weighs it at. It survives a placement
	// because queueReplay keeps it separately from membership, and it is the same
	// moment runState.queuedSince holds for the Run's own door.
	since time.Time
	// holdsNoQueue is whether this Run's wait is one other work does not have to
	// respect, as domain.AdmissionDeferral.HoldsNoQueue reads it off this Run's
	// latest deferral.
	//
	// It is the latest one and not the strongest one ever recorded. An exemption
	// is a claim about the fleet at the moment it was measured, and a Run held
	// behind work that outranks it measures nothing, so carrying the claim
	// forward through such a wait states it about a fleet nobody has asked since.
	//
	// It is stated as the exemption rather than as the rule so that the zero value
	// is the rule. A Run whose record says nothing about the fleet holds the queue
	// like every other wait, and only the fleet's own answer may take that away.
	holdsNoQueue bool
}

// queuePosition is one Run's standing in the queue at one moment: what its class
// says, how long it has waited, and what that is worth.
type queuePosition struct {
	runID string
	class domain.ServiceClass
	// group is the family this Run declared and the width it declared for it. It is
	// read from the Run's own workload rather than from the queue, because the bound
	// is the caller's statement about this Run and not something the queue can say.
	group  domain.RunGroup
	policy domain.Admission
	at     time.Time
	// wait is how long this Run has waited and how much of that its caller's own
	// declaration held, because the two bounds the class states are asked of
	// different parts of it.
	wait domain.Wait
	// priority is what the ordering weighs this Run at, and it is derived from the
	// whole wait rather than from the part Mercator caused. The bounds are promises
	// about what Mercator does, so they are charged by cause; the ordering is about
	// which work has gone longest without an answer, which is the same question
	// whoever caused the delay. A Run held by its own family holds no queue anyway,
	// so the only thing this decides is what that Run is worth when it competes
	// again.
	priority float64
}

func (queue admissionQueue) position(runID string, state runState, at time.Time) queuePosition {
	class := state.requested.Workload.Spec.Placement.Class
	policy := class.Admission()
	wait := state.wait(at)
	return queuePosition{
		runID:    runID,
		class:    class,
		group:    state.requested.Workload.Spec.Placement.Group,
		policy:   policy,
		at:       at,
		wait:     wait,
		priority: policy.EffectivePriority(wait.Seconds),
	}
}

// familyHolding is the members of this Run's own family that already have
// capacity, where there are as many of them as the family said may run at once,
// and nothing at all otherwise.
//
// The Run itself is never one of them. A Run being placed again after a launch
// that failed for capacity nobody has still holds the decision that gave it a
// machine, and counting that against its own family would make a member of a
// family of one unplaceable for ever.
//
// The width is the one this Run declared. Every member states it, so members that
// disagree are each held to their own answer, which is the only reading that needs
// nothing registered in advance: Mercator holds the Runs, and a family is what
// they say they belong to.
func (queue admissionQueue) familyHolding(run queuePosition) []domain.QueuedAhead {
	if !run.group.Declared() {
		return nil
	}
	var holding []domain.QueuedAhead
	for _, runID := range slices.Sorted(maps.Keys(queue.holding)) {
		if runID == run.runID || queue.holding[runID].ID != run.group.ID {
			continue
		}
		holding = append(holding, domain.QueuedAhead{RunID: runID})
	}
	if !run.group.Full(len(holding)) {
		return nil
	}
	return holding
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
// beside work that fits it and stalls a deployment on one impossible submission
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
		QueuedSeconds:        run.wait.Seconds,
		SelfImposedSeconds:   run.wait.SelfImposedSeconds,
		MaxQueueDelaySeconds: run.policy.MaxQueueDelaySeconds,
		Behind:               behind,
	}
}

// deferOrRefuse is what admission does with a Run it will not admit now. It
// waits, unless one of the two bounds its class states about waiting has gone by,
// in which case waiting is a promise the record says is already broken and the Run
// is refused instead.
//
// It is asked of every wait and not only of the one Placement caused. A Run held
// behind work that outranks it is waiting exactly as much as a Run no machine
// would take, and admission that checked a bound on one and not the other would
// keep a Run queued for ever past the moment its own class says the answer stopped
// being worth having.
//
// This is the only door the queue delay is asked at, because that bound is on
// waiting and nothing else. A Run whose capacity came free has stopped waiting, so
// stepAdmit asks it nothing on the way to Placement; refusing it there would spend
// the whole wait and then discard the answer it was for. The deadline is asked
// there as well, because it is a different question: whether the answer is still
// worth producing at all.
//
// Which of the two a refused wait is named for is Admission.BoundAlreadyBroken's
// answer, and it is the same answer stepAdmit's own door gets, off the same wait.
// Naming it from the answer of the moment instead is how one Run got told two
// different things depending on whether a machine happened to be free: the wait
// its family had held for a day was refused for Mercator's queue delay at this
// door and for the caller's own deadline at the other one, on the same record and
// the same second, which is the sweep-cadence dependence the bound naming exists
// to end. The projected miss below is the one thing only this door can see: a wait
// still inside both bounds, which the record says cannot end in time.
func (o *Orchestrator) deferOrRefuse(
	ctx context.Context,
	runID string,
	version uint64,
	state runState,
	run queuePosition,
	answer admissionAnswer,
) error {
	if reason := run.policy.BoundAlreadyBroken(run.wait); reason != "" {
		answer.deferral.Reason = reason
		return o.recordRefusal(ctx, runID, version, state, answer)
	}
	if run.policy.DeadlineUnreachable(run.wait.Seconds, answer.deferral.ProjectedWaitSeconds, answer.projected) {
		answer.deferral.Reason = domain.RefusedDeadlineUnreachable
		return o.recordRefusal(ctx, runID, version, state, answer)
	}
	return o.recordDeferral(ctx, runID, version, state, answer)
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
	answer, waitable := fleetAnswer(decision)
	wait, projected := shortestProjectedWait(waitable)
	deferral := run.deferral(answer.Reason(), workAhead(waitable))
	deferral.ProjectedWaitSeconds = wait
	deferral.Fleet = &answer
	return deferral, projected
}

// fleetAnswer is the fleet's own account of this decision, and the machines that
// could take this Run when whatever they are spending now comes back. Those are
// the whole fleet as far as a wait is concerned, and the rest of the candidate
// set is a record of machines this Run is not competing for.
func fleetAnswer(decision domain.BookingDecision) (domain.FleetAnswer, []domain.CandidateDecision) {
	answer := domain.FleetAnswer{Weighed: len(decision.Candidates)}
	var waitable []domain.CandidateDecision
	for _, candidate := range decision.Candidates {
		switch candidate.Standing() {
		case domain.StandingCouldHold:
			waitable = append(waitable, candidate)
			answer.CouldHold++
		case domain.StandingUnstated:
			answer.Unstated++
		case domain.StandingNeverHolds:
		}
	}
	return answer, waitable
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
	runID string,
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
	return o.appendEvents(ctx, runID, version, admissionCommand(state, "deferred"), events)
}

// recordRefusal closes a Run admission will not queue, loudly: the reason, the
// outcome, and the closure in one commit, so a caller learns its deadline cannot
// be met from the Run rather than from the Run never starting.
func (o *Orchestrator) recordRefusal(
	ctx context.Context,
	runID string,
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
		return o.completeBookingAndAppend(ctx, runID, version, state, command, events)
	}
	return o.appendEvents(ctx, runID, version, command, events)
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
