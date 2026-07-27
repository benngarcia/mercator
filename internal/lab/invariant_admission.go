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

// This file is the laws about what a Run's class says: that waiting ends, that it
// ends in the order the classes declared, that nothing is made to wait behind a
// wait nobody can end, that a wait rests on an answer somebody gave about
// capacity, and that the bounds a Run carried into admission are honoured or
// refused.
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

// agingPreventsStarvation is the promise every class's maximum queue delay makes,
// in the two halves it takes to state it once the bound is a refusal.
//
// The first half is that no Run is left waiting past the bound. It replaced the
// exemption that let a Run sit in phase "queued" for ever inside
// admitted_run_progress. That exemption was written when nothing could reach the
// queued phase, so it cost nothing and read as a reasonable carve-out; the moment
// a Run could actually be queued it would have made starvation the one thing the
// liveness rules explicitly permit.
//
// The second half is what stops the first from being satisfied by refusing
// everything. Admission now ends a wait it cannot honour, so a Run stepped over
// for an hour and a Run whose fleet was never big enough both leave the queue
// inside the bound, and only the second of those is Mercator working. A refusal at
// the bound is therefore read as starvation whenever the record says the wait
// could have ended and younger work was admitted past it after the moment the
// Run's own class promises to have promoted it. A wait its own family's declared
// width was holding is the other thing no ordering could have ended, and it is
// exempt for the same reason: the bound counts members rather than machines, so a
// caller whose declared width outlasts its class's own patience has contradicted
// itself and nothing Mercator could have done would have placed the Run.
//
// That second half is deliberately stated over waits rather than over effective
// priority. Production orders the queue on the number EffectivePriority returns,
// and a law reading the same function would be checking the aging term against
// itself: deleting the term makes the ordering wrong and makes every reading of it
// agree. What this reads instead is the derivation the rate is built from, that a
// Run outranks anything that could arrive once it has waited half its own bound,
// and the only thing it takes from the class table is that bound.
func agingPreventsStarvation(observation InvariantObservation) error {
	if err := noWaitPastItsClassBound(observation); err != nil {
		return err
	}
	return noRefusalYoungerWorkOvertook(observation)
}

// noWaitPastItsClassBound is a Run Mercator accepted and told to wait still
// waiting past the longest wait its own class allows.
//
// It is stated against the read model rather than against the event log because
// what it is about is the state a Run is in now, and the projection is where
// Mercator says what that is. A Run that left the queue at any point is not
// starving whatever happened to it since.
//
// A wait the caller's own declaration is holding is exempt, which is the exemption
// noRefusalYoungerWorkOvertook already carries below and for the same reason: a
// family's width counts members rather than machines, so no ordering could have
// ended the wait and a fleet standing idle beside it changes nothing. Stating it in
// one half and not the other had the two halves demanding opposite things of one
// record. This half could only be satisfied by refusing the held member at the
// bound, and that refusal closed the later members of every family narrower than
// its class's patience as failed Runs, on a promise Mercator had not broken. Both
// halves now say what the other says: a caller whose declared width outlasts its
// own class's patience has contradicted itself, and that is not Mercator starving
// anybody.
func noWaitPastItsClassBound(observation InvariantObservation) error {
	for _, run := range observation.Runs {
		if run.Closed || run.Admission == nil || run.QueuedSince == nil || run.Admission.SelfImposed() {
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

// noRefusalYoungerWorkOvertook holds every wait admission ended past the class
// bound to the reason it is allowed to end there. A fleet that never published a
// machine which could take this Run is one, and it is the whole of the exemption:
// no ordering could have placed work nothing can hold, so the refusal is Mercator
// reporting a fleet too small rather than a queue that wronged somebody.
//
// Anything else refused past the bound has to be explained by what was admitted
// while it waited, and the rule is the one the aging rate is derived from. Once a
// Run has waited half its own maximum queue delay it is worth more than any class
// starting fresh, so work admitted past it from that moment on must itself have
// waited at least half of its own bound. Work that had not is younger work that
// stepped over it, which is exactly the starvation the derivation exists to
// prevent.
//
// Which refusals those are is read off the wait and never off the reason code,
// which is what safety.nothing_waits_behind_an_impossible_ask says about reasons
// in general: the word Mercator chose is the answer under examination. Reading
// QUEUE_DELAY_EXCEEDED alone left the law silent on exactly the record it exists
// to catch. A Run refused after a wait sixteen thousand seconds past its class
// bound was named for the later of the two bounds it broke, so neither half of
// this rule saw it: the wait is over, which is what the first half skips, and the
// refusal did not carry the word, which is what the second half filtered on.
//
// It is replayed out of the public log because it is about moments that have
// passed: which Runs took capacity while this one waited, and how long each of
// them had waited when they did. The projection only says who is waiting now.
func noRefusalYoungerWorkOvertook(observation InvariantObservation) error {
	began, err := waitsBegan(observation)
	if err != nil {
		return err
	}
	admitted, refused, err := replayQueueDepartures(observation, began)
	if err != nil {
		return err
	}
	for _, wait := range refused {
		if err := agingShouldHaveTaken(wait, admitted); err != nil {
			return err
		}
	}
	return nil
}

// waitsBegan is the moment each Run's wait began, which is the one number every
// law in this file that measures a wait measures from. It is the first thing
// admission recorded about the Run, and no later event revises it: a Run ID is
// unique, and when its wait started is a fact about it.
//
// It is the number production measures too. runState.queuedSince is set at the
// first deferral and nothing ever clears it, so a Run that took a machine, failed
// to launch on it and came back through admission is held at the standing of its
// whole wait, and the queue admission orders against keeps that same moment across
// the placement. A reading that began the wait again there disagreed with the
// scheduler about the same Run: the re-placed Run read back as a fresh arrival
// that had waited nothing, so one law convicted the queue it was in fact the
// oldest member of and another could not see a placement past a deadline at all.
//
// It is one function because three laws need it. Each of them replayed it for
// itself, the same eight lines three times, and a repair to two of them left the
// third measuring a shorter wait than production does and contradicting its own
// doc comment.
func waitsBegan(observation InvariantObservation) (map[string]time.Time, error) {
	began := map[string]time.Time{}
	for _, event := range observation.MercatorEvents {
		if event.Type != orchestrator.EventAdmissionDeferred && event.Type != orchestrator.EventAdmissionRefused {
			continue
		}
		runID := strings.TrimPrefix(event.Subject, "runs/")
		if _, waiting := began[runID]; waiting {
			continue
		}
		at, err := eventOccurredAt(event)
		if err != nil {
			return nil, err
		}
		began[runID] = at
	}
	return began, nil
}

// replayQueueDepartures is every way this log says a wait ended: the Runs that
// left it by taking capacity, with how long each had waited when it did, and the
// waits admission would not go on holding.
//
// Every moment is read only where the rule needs one, which is why the timestamp
// is parsed inside each case rather than beside the event. A record whose
// admission facts cannot state when they happened is a record this rule fails on,
// and the rest of the stream is none of its business.
func replayQueueDepartures(observation InvariantObservation, began map[string]time.Time) ([]admittedRun, []refusedWait, error) {
	waits := map[string]queueWait{}
	var admitted []admittedRun
	var refused []refusedWait
	for _, event := range observation.MercatorEvents {
		runID := strings.TrimPrefix(event.Subject, "runs/")
		switch event.Type {
		case orchestrator.EventAdmissionDeferred, orchestrator.EventAdmissionRefused:
			deferral, err := recordedDeferral(event)
			if err != nil {
				return nil, nil, err
			}
			at, err := eventOccurredAt(event)
			if err != nil {
				return nil, nil, err
			}
			wait := waits[runID].asked(event.WorkspaceID, deferral)
			waits[runID] = wait
			if event.Type == orchestrator.EventAdmissionRefused {
				refused = append(refused, wait.ended(runID, deferral, began[runID], at))
			}
		case orchestrator.EventBookingDecided:
			decision, err := recordedDecision(event)
			if err != nil {
				return nil, nil, err
			}
			// A decision that selected nothing admitted nothing, and it is also the
			// shape every synthetic Booking Decision in this tree carries, so asking
			// for its timestamp first would fail this rule on records it has nothing
			// to say about.
			if decision.SelectedOfferSnapshotID == "" {
				continue
			}
			at, err := eventOccurredAt(event)
			if err != nil {
				return nil, nil, err
			}
			waits[decision.RunID] = waits[decision.RunID].placed()
			admitted = append(admitted, admittedRun{
				runID: decision.RunID, workspace: event.WorkspaceID,
				class: decision.Policy.Class, at: at,
				waited: waitedBy(began[decision.RunID], at),
			})
		}
	}
	return admitted, refused, nil
}

// queueWait is what the record establishes about one Run's wait as it goes on:
// whose queue it is in, and whether anything ever said a machine could have taken
// it.
type queueWait struct {
	workspace string
	// fleetAnswered and fleetCouldHold are the whole account of the fleet during
	// this wait. A wait nobody ever measured a fleet against establishes no
	// exemption, and one measurement saying a machine could hold this Run is enough
	// to say an ordering could have ended the wait.
	fleetAnswered  bool
	fleetCouldHold bool
	// familyHeld is whether the last thing admission said about this wait is that the
	// Run's own family is already as wide as its caller declared. It is the second
	// exemption this rule needs, and it is not about capacity at all: the bound
	// counts members of the family rather than machines, so no ordering could have
	// ended the wait and a fleet standing idle beside it changes nothing.
	//
	// It is the latest answer rather than any answer during the wait, for the reason
	// AdmissionDeferral.HoldsNoQueue reads the latest one. A Run whose family was
	// full an hour ago and has since been waiting for a machine is in a wait an
	// ordering could have ended, and carrying the exemption forward would excuse
	// exactly that.
	familyHeld bool
}

// asked is this wait after one more moment admission decided about it.
func (wait queueWait) asked(workspace string, deferral domain.AdmissionDeferral) queueWait {
	wait.workspace = workspace
	wait.familyHeld = deferral.Reason == domain.DeferredGroupAtParallelism
	if deferral.Fleet == nil {
		return wait
	}
	wait.fleetAnswered = true
	wait.fleetCouldHold = wait.fleetCouldHold || !deferral.Fleet.HoldsNothing()
	return wait
}

// placed is this wait after Mercator selected a machine for the Run, which is the
// strongest thing that can be said about whether the fleet could hold it. A
// placement is an answer nobody has to recount: the fleet published a machine, the
// scheduler weighed it, and it took the Run.
func (wait queueWait) placed() queueWait {
	wait.fleetAnswered, wait.fleetCouldHold, wait.familyHeld = true, true, false
	return wait
}

// heldNothing is the exemption this wait carries: the fleet was asked, and nothing
// anybody measured during the whole wait could have taken this Run.
//
// It is every answer during the wait rather than the answer beside the refusal, and
// it is not the reading production orders the queue on, which is the difference
// between the two questions. Production asks whether other work has to be held
// behind this wait now, so it reads the latest deferral and a Run held behind
// higher priority work measured no machine at all. This asks whether any ordering
// could have ended the wait, over a wait that is now over, where an absence of
// evidence is not evidence the fleet had room: a Run nothing could ever hold,
// refused at its bound through the priority door, carries no fleet answer on the
// refusal whatsoever, and reading only that answer convicted Mercator of starving a
// Run no machine it published could take.
//
// Reading the last answer instead of all of them let a stale exemption outlive the
// evidence against it. A Run measured unholdable once, then placed on a machine
// Mercator itself selected, is a Run the fleet demonstrably could hold, and the
// carried answer exempted it from this law for the rest of its life.
func (wait queueWait) heldNothing() bool {
	return wait.fleetAnswered && !wait.fleetCouldHold
}

// ended is this wait written as the refusal that closed it.
func (wait queueWait) ended(runID string, deferral domain.AdmissionDeferral, since, at time.Time) refusedWait {
	return refusedWait{
		runID:            runID,
		workspace:        wait.workspace,
		class:            deferral.Class,
		since:            since,
		at:               at,
		fleetHeldNothing: wait.heldNothing(),
		familyHeld:       wait.familyHeld,
	}
}

// admittedRun is one Run taking capacity at one moment, whose queue it left, and
// how long it had itself been kept waiting when it did.
type admittedRun struct {
	runID     string
	workspace string
	class     domain.ServiceClass
	at        time.Time
	waited    float64
}

// refusedWait is one wait admission would not go on holding: whose it was, which
// queue it was in, when it began and ended, and whether the fleet had said there
// was nothing here to wait for.
type refusedWait struct {
	runID            string
	workspace        string
	class            domain.ServiceClass
	since            time.Time
	at               time.Time
	fleetHeldNothing bool
	// familyHeld is the wait's other exemption: the Run's own family was as wide as
	// its caller said it may run, which is a bound no ordering and no machine can
	// lift. A caller whose declared width outlasts its class's own patience has
	// contradicted itself, and that is not Mercator starving anybody.
	familyHeld bool
}

// agingShouldHaveTaken adjudicates one refused wait against every admission taken
// during it.
//
// A wait refused inside its own class bound is not judged, because the bound is
// what this rule is about: a Run refused before reaching it was refused for
// something else, and a Run refused without ever having been deferred waited for
// nothing at all.
func agingShouldHaveTaken(wait refusedWait, admissions []admittedRun) error {
	held := wait.at.Sub(wait.since).Seconds()
	if wait.fleetHeldNothing || wait.familyHeld || !wait.class.Admission().Starved(held) {
		return nil
	}
	// A class that states no bound cannot reach this, because a wait past a bound of
	// nothing is not a wait anything passed.
	promoted := halfTheBound(wait.class)
	for _, admission := range admissions {
		// One queue is one workspace's, which is how production builds it: a Run is
		// ordered against the waits in its own tenant and against nothing else, so an
		// admission in another workspace competed with this wait for nothing and
		// convicts it of nothing.
		if admission.workspace != wait.workspace || admission.runID == wait.runID {
			continue
		}
		if admission.at.Before(wait.since) || admission.at.After(wait.at) {
			continue
		}
		waited := admission.at.Sub(wait.since).Seconds()
		if waited <= promoted {
			continue
		}
		// Work that had itself aged to the top of the queue is not younger work
		// stepping over anybody, and capacity going spare may be taken by a class
		// that declared itself eligible for it right up to the moment the Run
		// waiting for it is past its own bound.
		if ahead := halfTheBound(admission.class); ahead > 0 && admission.waited >= ahead {
			continue
		}
		if admission.class.Admission().BackfillEligible && !wait.class.Admission().Starved(waited) {
			continue
		}
		return fmt.Errorf(
			"Run %q of class %q was refused after waiting %.0fs, and %q of class %q was admitted %.0fs into that wait having waited %.0fs, which is past the %.0fs at which this class promises to have promoted a Run above anything arriving",
			wait.runID, wait.class, wait.at.Sub(wait.since).Seconds(),
			admission.runID, admission.class, waited, admission.waited, promoted,
		)
	}
	return nil
}

// halfTheBound is the moment a class's own aging rate has promoted a Run of it
// above every class starting fresh. It is half the maximum queue delay because
// that is what Admission.AgingPerSecond is derived from, and it is recomputed
// here rather than read off that function so the derivation and the rule built on
// it cannot be broken by one edit.
func halfTheBound(class domain.ServiceClass) float64 {
	return class.Admission().MaxQueueDelaySeconds / 2
}

// waitedBy is how long a wait that began at one moment had run by another. A
// moment before the wait began waited nothing rather than a negative amount of
// time: a Run placed on its very first pass and told to wait afterwards did not
// wait for the placement.
func waitedBy(since, at time.Time) float64 {
	if since.IsZero() || at.Before(since) {
		return 0
	}
	return at.Sub(since).Seconds()
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
//
// Two facts are replayed and not one, because production keeps two. Membership of
// the queue ends when a Run takes a machine, is refused, or is over, which is what
// admissionQueue reads out of the log. The moment a Run's own wait began outlives
// that, which is waitsBegan above, so a Run placed again after a launch that failed
// is weighed at the standing of its whole wait rather than as an arrival.
func serviceClassAdmissionOrder(observation InvariantObservation) error {
	began, err := waitsBegan(observation)
	if err != nil {
		return err
	}
	queue := map[string]queuedRun{}
	for _, event := range observation.MercatorEvents {
		runID := strings.TrimPrefix(event.Subject, "runs/")
		switch event.Type {
		case orchestrator.EventAdmissionDeferred:
			deferral, err := recordedDeferral(event)
			if err != nil {
				return err
			}
			// What the Run is waiting for moves and the moment its wait began does
			// not: a Run whose fleet filled up while it waited is waiting for
			// something the ordering has to respect, and one whose fleet emptied is
			// not.
			queue[runID] = queuedRun{
				workspace:     event.WorkspaceID,
				class:         deferral.Class,
				since:         began[runID],
				heldByNothing: deferral.HoldsNoQueue(),
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
			admitted := admittedRun{
				runID:     decision.RunID,
				workspace: event.WorkspaceID,
				class:     observation.Workloads[decision.RunID].Spec.Placement.Class,
				at:        at,
				waited:    waitedBy(began[decision.RunID], at),
			}
			if err := admittedInClassOrder(queue, admitted); err != nil {
				return err
			}
			delete(queue, decision.RunID)
		}
	}
	return nil
}

// queuedRun is one Run waiting at one point in the replay.
type queuedRun struct {
	// workspace is whose queue this is. Mercator orders each tenant's queue on its
	// own, building it with the workspace in the event filter, so a Run is weighed
	// against the waits beside it and against nothing in another tenant.
	workspace string
	class     domain.ServiceClass
	since     time.Time
	// heldByNothing is what the record last established about the fleet's answer
	// to this Run: asked about it, and holding no machine that could ever take it.
	// A Run in that state is waiting for capacity to be added, so the ordering is
	// not over it.
	heldByNothing bool
}

func admittedInClassOrder(queue map[string]queuedRun, admitted admittedRun) error {
	policy := admitted.class.Admission()
	priority := policy.EffectivePriority(admitted.waited)
	for _, other := range slices.Sorted(maps.Keys(queue)) {
		if other == admitted.runID {
			continue
		}
		held := queue[other]
		if held.workspace != admitted.workspace || held.heldByNothing {
			continue
		}
		waited := admitted.at.Sub(held.since).Seconds()
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
			admitted.runID, admitted.class, priority, other, held.class, waited, otherPriority,
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
			impossible[runID] = deferral.HoldsNoQueue()
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

// classBoundsHonoured is the standing guard on the two bounds a Run carries into
// admission: the money its caller allowed it to spend, and the moment its class
// says the answer stops being worth having. A Run that cannot run inside them is
// refused, and the refusal is in the record; what this forbids is running past one
// of them with nothing said.
//
// The two are one law because they are the same failure. A bound is a caller's
// declaration that Mercator may not decide against, and a class is a caller's
// declaration that Mercator scores every candidate on, so the class can always be
// talked into a costlier or a later machine and the bounds are what say how far.
// Everything else in this registry reads the ranking; this reads the limits the
// ranking was allowed to work inside.
//
// The maximum queue delay is deliberately not restated here. It is the promise
// liveness.aging_prevents_starvation is stated over, and two laws over one bound
// would let a repair satisfy one of them and be believed.
func classBoundsHonoured(observation InvariantObservation) error {
	if err := noPlacementOverItsBudget(observation); err != nil {
		return err
	}
	return noPlacementPastItsDeadline(observation)
}

// noPlacementOverItsBudget reads what the Run was placed on against the bound the
// same record says its caller set. Both halves come off the decision, which is what
// makes it checkable at all: the policy states the number and the selected
// candidate states its own cost estimate.
//
// A machine nobody quoted fails it under any bound. A bound on dollars is not
// cleared by a candidate that has none, and pricing that absence at zero is how an
// unquoted machine passed every budget a Run could state.
func noPlacementOverItsBudget(observation InvariantObservation) error {
	decisions, err := recordedDecisions(observation)
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		budget, selected := decision.Policy.MaxExpectedCostUSD, selectedCandidate(decision)
		if budget == nil || selected == nil {
			continue
		}
		if err := placedInsideItsBudget(decision, *selected, *budget); err != nil {
			return err
		}
	}
	return nil
}

// placedInsideItsBudget compares the two numbers the way the check that should have
// refused this candidate compares them, with no tolerance between them. Nothing is
// re-derived here: both come off one record, so a tolerance would only buy an
// overshoot production had already refused.
func placedInsideItsBudget(decision domain.BookingDecision, selected domain.CandidateDecision, budget float64) error {
	cost := selected.Estimates.CostUSD
	switch {
	case cost.Source == domain.CostUnpriced:
		return fmt.Errorf(
			"Run %q of class %q was placed on candidate %q under a bound of %.4f USD, and nobody quoted what that machine costs",
			decision.RunID, decision.Policy.Class, selected.OfferSnapshotID, budget,
		)
	case cost.Expected > budget:
		return fmt.Errorf(
			"Run %q of class %q was placed on candidate %q at %.4f USD, and its caller allowed %.4f",
			decision.RunID, decision.Policy.Class, selected.OfferSnapshotID, cost.Expected, budget,
		)
	default:
		return nil
	}
}

// noPlacementPastItsDeadline replays the waits out of the public log and holds each
// placement to the moment the placed Run's own class states. A class deadline is
// measured from when admission first told the Run to wait, so the log is where both
// ends of it are: waitsBegan, and the decision that took a machine.
//
// A Run nothing ever deferred has no such moment and is not judged here, which is
// what the class means by it. Waiting is what a deadline bounds.
//
// Every placement in a wait is judged and not only the first, which is the same
// number production refuses on. A Run placed once, sent back through admission by a
// launch that failed for capacity nobody has, and placed again is one wait with two
// placements in it, and stepAdmit reads the whole wait off runState.queuedSince at
// both of them. A reading that restarted the clock at the first placement could not
// fail for any Run placed past its deadline that had been placed once before, which
// is exactly the shape a failed launch produces.
//
// The world this exists to catch is the one the tree was in: the deadline was asked
// only of a Run being told to wait, so a Run whose capacity came free after its own
// moment was placed by the very pass that should have refused it. Nothing else in
// this registry reads a class deadline at all, and the refusal that should have been
// recorded is an absence, which no rule about what a record says can see.
func noPlacementPastItsDeadline(observation InvariantObservation) error {
	began, err := waitsBegan(observation)
	if err != nil {
		return err
	}
	for _, event := range observation.MercatorEvents {
		if event.Type != orchestrator.EventBookingDecided {
			continue
		}
		decision, err := recordedDecision(event)
		if err != nil {
			return err
		}
		// A decision that selected nothing placed nothing, and a Run nothing ever
		// deferred has no moment to measure a deadline from. Both are asked before
		// the timestamp, because a decision this rule has nothing to say about is
		// also the shape every synthetic Booking Decision in this tree carries.
		since, waited := began[decision.RunID]
		if decision.SelectedOfferSnapshotID == "" || !waited {
			continue
		}
		at, err := eventOccurredAt(event)
		if err != nil {
			return err
		}
		if err := placedInsideItsDeadline(decision, waitedBy(since, at)); err != nil {
			return err
		}
	}
	return nil
}

func placedInsideItsDeadline(decision domain.BookingDecision, queuedSeconds float64) error {
	policy := decision.Policy.Class.Admission()
	if !policy.DeadlinePassed(queuedSeconds) {
		return nil
	}
	return fmt.Errorf(
		"Run %q of class %q was placed on %q after waiting %.0fs, and its class states it must have started within %.0fs of being told to wait",
		decision.RunID, decision.Policy.Class, decision.SelectedOfferSnapshotID, queuedSeconds, policy.DeadlineSeconds,
	)
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

// aSilenceIsNotAnAnswerAboutCapacity reads every recorded wait against the
// decision it was read off, and recounts the fleet's answer from the refusals.
//
// The strongest thing a fleet can say about an ask is that nothing it published
// can ever hold it, and that answer is the one the whole queue is ordered on: work
// that fits the fleet as it stands is not competing with such a Run for anything.
// It may not be said over a machine that published nothing. A node whose disk
// probe failed reports no room and has not said it is full, and every Run carries
// a disk floor, so reading that silence as a measurement made every Run in the
// workspace an ask no capacity can ever hold and took the ordering away from all
// of them at once.
//
// It recounts rather than trusting the answer beside the reason, which is the same
// reason silenceWasTakenBackOut recomputes what a candidate was charged: a
// scheduler that miscounts its own evidence agrees with itself perfectly, and the
// only reading that can catch it is one taken off the record's other half.
//
// A wait carrying no fleet answer is a wait the queue caused on its own account
// and there is nothing to recount. A decision that selected a machine placed the
// Run and is not a wait at all.
func aSilenceIsNotAnAnswerAboutCapacity(observation InvariantObservation) error {
	weighed := map[string]domain.BookingDecision{}
	for _, event := range observation.MercatorEvents {
		runID := strings.TrimPrefix(event.Subject, "runs/")
		switch event.Type {
		case orchestrator.EventBookingDecided:
			decision, err := recordedDecision(event)
			if err != nil {
				return err
			}
			// Filed under the Run the decision names rather than under the event's
			// subject: a decision says which Run it is about, and that is the half of
			// the pairing that has to be right for this reading to be independent.
			weighed[decision.RunID] = decision
		case orchestrator.EventAdmissionDeferred, orchestrator.EventAdmissionRefused:
			deferral, err := recordedDeferral(event)
			if err != nil {
				return err
			}
			if err := fleetAnswerMatchesItsEvidence(runID, deferral, weighed[runID]); err != nil {
				return err
			}
		}
	}
	return nil
}

// fleetAnswerMatchesItsEvidence recounts one recorded answer off the candidates
// the decision beside it recorded.
func fleetAnswerMatchesItsEvidence(runID string, deferral domain.AdmissionDeferral, decision domain.BookingDecision) error {
	if deferral.Fleet == nil || decision.RunID != runID || decision.SelectedOfferSnapshotID != "" {
		return nil
	}
	counted, silent := 0, 0
	for _, candidate := range decision.Candidates {
		switch candidate.Standing() {
		case domain.StandingCouldHold:
			counted++
		case domain.StandingUnstated:
			silent++
		case domain.StandingNeverHolds:
		}
	}
	if deferral.Fleet.CouldHold != counted || deferral.Fleet.Unstated != silent {
		return fmt.Errorf(
			"Run %q: the wait records a fleet of %d machines that could hold it and %d that said too little, and its decision weighed %d and %d",
			runID, deferral.Fleet.CouldHold, deferral.Fleet.Unstated, counted, silent,
		)
	}
	if deferral.Fleet.HoldsNothing() && silent > 0 {
		return fmt.Errorf(
			"Run %q: the wait says no machine in this fleet can ever hold it, and %d of the machines weighed published nothing to say it about",
			runID, silent,
		)
	}
	return nil
}
