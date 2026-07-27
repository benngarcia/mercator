package lab

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

// TestBackfillMayTakeCapacityGoingSpare and the test below it are the two halves
// of the one exemption in safety.service_class_admission_order. The registry's
// deliberate case drives the rule itself; these drive the carve-out, because a
// carve-out nothing exercises is either dead or a hole.
//
// Capacity going spare may be taken by a class that declared itself eligible for
// it, even while a class that outranks it is waiting. That is what backfill is
// for, and a rule that refused it would leave a machine idle beside work that
// says it will take whatever is free.
func TestBackfillMayTakeCapacityGoingSpare(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, map[string]domain.WorkloadRevision{
		"run-spare": classedWorkload(domain.ClassOpportunistic),
	}, []eventlog.CloudEvent{
		admissionDeferredEvent("run-watched", now, domain.ClassInteractive),
		admittedDecisionEvent("run-spare", now.Add(time.Minute)),
	})

	if err := serviceClassAdmissionOrder(observation); err != nil {
		t.Fatalf("backfill onto spare capacity is what the exemption is for, and the rule refused it: %v", err)
	}
}

// TestBackfillMayNotTakeTheSlotAStarvedRunIsWaitingFor is the deliberate failure
// of the same clause. Six minutes is past the five an interactive Run's class
// allows it to wait, so the machine coming free is not capacity going spare: it
// is the capacity that Run is owed, and taking it is the starvation the aging
// rule exists to make impossible.
func TestBackfillMayNotTakeTheSlotAStarvedRunIsWaitingFor(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, map[string]domain.WorkloadRevision{
		"run-spare": classedWorkload(domain.ClassOpportunistic),
	}, []eventlog.CloudEvent{
		admissionDeferredEvent("run-watched", now, domain.ClassInteractive),
		admittedDecisionEvent("run-spare", now.Add(6*time.Minute)),
	})

	err := serviceClassAdmissionOrder(observation)
	if err == nil {
		t.Fatalf("a backfill took the slot a starved interactive Run was waiting for and the rule allowed it")
	}
	t.Logf("violation: %v", err)
}

// TestTheOrderingIsOverOneTenantsQueue is the same flat reading in the ordering
// law, on the world it convicts. Mercator orders each workspace's queue on its own,
// so an interactive Run waiting in ws_alpha is not work an opportunistic Run in
// ws_beta was admitted past: no ordering relates them, and Rentals being shared
// across tenants is what makes such an execution expressible.
func TestTheOrderingIsOverOneTenantsQueue(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, map[string]domain.WorkloadRevision{
		"run-spare": classedWorkload(domain.ClassOpportunistic),
	}, []eventlog.CloudEvent{
		inWorkspace(admissionDeferredEvent("run-watched", now, domain.ClassInteractive), "ws_alpha"),
		inWorkspace(admittedDecisionEvent("run-spare", now.Add(6*time.Minute)), "ws_beta"),
	})

	if err := serviceClassAdmissionOrder(observation); err != nil {
		t.Fatalf("a Run admitted in another tenant was ordered against a queue it is not in: %v", err)
	}
}

// TestAnImpossibleAskEmptiesNoFleetUnderTheRealControlPlane is the queue's second
// law at L1. The placement corpus can show admission ordering two Runs; only this
// shows the machines actually running the other two, through the offer catalog, with
// the real orchestrator, event log, and Run projection in the loop, while the Run
// nothing can hold goes on waiting beside them.
//
// The fleet is busy while the impossible ask is weighed, which is what makes the
// case. One machine is five hours into work of its own, so a classification read off
// the Bookings Mercator holds rather than off what each machine refused calls the
// impossible ask a wait for capacity to come free, keeps the queue with it, and
// leaves the idle machine standing beside the Run that fits it.
//
// It is driven to completion because the impossible Run never becomes placeable. The
// execution has to reach the moment its class says the answer stopped being worth
// having and come back with that Run closed and nothing else stopped by it.
func TestAnImpossibleAskEmptiesNoFleetUnderTheRealControlPlane(t *testing.T) {
	execution := openBlueprintExecution(t, "../scenario/scenarios/conformance/an-impossible-ask-empties-no-fleet.json", DefaultLimits())
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive a fleet holding one Run it can never place: %v", err)
	}

	decisions := bookingDecisions(t, execution)
	if selected := decisions["run-occupies"].SelectedOfferSnapshotID; selected != "rental-big" {
		t.Fatalf("the Run needing 150GB was placed on %q, and only the 200GB machine has the room", selected)
	}
	if selected := decisions["run-fits"].SelectedOfferSnapshotID; selected != "rental-small" {
		t.Fatalf("the Run that fits this fleet was placed on %q, and the idle machine here has room for it", selected)
	}
	// The Run nothing could place is answered and placed nowhere, which are two
	// statements now. Before, a Run that found no feasible offer recorded no
	// decision at all, so its whole account of itself was the reason code and the
	// two counts asserted below, and every rule that reads Booking Decisions had
	// nothing to read on the one Run in this fleet whose refusal is the point.
	refusal, answered := decisions["run-impossible"]
	if !answered {
		t.Fatal("the Run nothing could place has no recorded decision to be explained from")
	}
	if refusal.SelectedOfferSnapshotID != "" {
		t.Fatalf("a Run asking for 900GB was placed on %q, and the largest machine in this fleet has 200GB", refusal.SelectedOfferSnapshotID)
	}
	if len(refusal.Candidates) != 2 {
		t.Fatalf("the recorded refusal weighed %d machines, and this fleet has two", len(refusal.Candidates))
	}
	for _, candidate := range refusal.Candidates {
		if candidate.Feasible || len(candidate.Rejections) == 0 {
			t.Fatalf("the recorded refusal has nothing to say about why %q could not take the Run: %+v", candidate.OfferSnapshotID, candidate)
		}
	}
	// It was never told to wait at all. A Run ordered behind the impossible ask is
	// the defect, and it is visible as one deferral of a Run the fleet had room for
	// the moment it arrived.
	if deferral, waited := admissionRecord(t, execution, "run-fits"); waited {
		t.Fatalf("the Run that fits was told to wait for %q behind %v", deferral.Reason, deferral.Behind)
	}
	waiting, _ := admissionRecord(t, execution, "run-impossible")
	if waiting.Reason != domain.DeferredNoCapacityFits {
		t.Fatalf("the impossible Run's record says it waited for %q, and every machine in this fleet was weighed against it", waiting.Reason)
	}
	if waiting.Fleet == nil {
		t.Fatal("the impossible Run's record says nothing at all about the fleet it was measured against")
	}
	if waiting.Fleet.Weighed != 2 || waiting.Fleet.CouldHold != 0 {
		t.Fatalf("the record says %d machines were weighed and %d of them could hold this Run, and the fleet has two machines and neither can",
			waiting.Fleet.Weighed, waiting.Fleet.CouldHold)
	}
	// The wait ends named for the earlier of the two bounds it broke. Driven to the
	// world's own horizon this execution reaches five hours in one advance, with the
	// half hour of queue this class allows and its four hour deadline both behind it,
	// and naming the deadline told the caller the answer had stopped being worth
	// having about a promise Mercator broke four and a half hours earlier.
	closing := refusalRecord(t, execution, "run-impossible")
	if closing.Reason != domain.RefusedQueueDelayExceeded {
		t.Fatalf("the wait ended as %q after %.0fs, against the %.0fs of queue this class allows",
			closing.Reason, closing.QueuedSeconds, closing.MaxQueueDelaySeconds)
	}
	if closing.QueuedSeconds <= closing.MaxQueueDelaySeconds {
		t.Fatalf("the refusal says the Run waited %.0fs against a bound of %.0fs, which is not a bound anything passed",
			closing.QueuedSeconds, closing.MaxQueueDelaySeconds)
	}
	// And the starvation law is silent about it, which is the exemption read off the
	// fleet's last measurement. This refusal carries no fleet answer of its own,
	// because the door it left by weighed no machine.
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("check invariants: %v", err)
	}
	starvation := invariantResultByID(t, latestInvariantResults(execution.invariants), "liveness.aging_prevents_starvation")
	if starvation.Status != InvariantPassed {
		t.Fatalf("the starvation law read a fleet nothing in it can hold this Run as a queue that wronged somebody: %+v", starvation)
	}
}

// TestACostBoundRefusesTheMachineTheClassWouldBuyAtL1 is the caller's maximum cost
// under the real control plane. The placement corpus shows the refusal in a recorded
// decision; this shows the Run running on the machine it was left with, through the
// offer catalog, the real orchestrator, and every law in the registry.
//
// The class and the bound disagree here on purpose. Interactive work prices a second
// of waiting at twenty times the rent, so it buys the warm machine on the five
// minutes of pulling the cold one owes, and the bound is the only thing that says
// how far that reasoning may go.
func TestACostBoundRefusesTheMachineTheClassWouldBuyAtL1(t *testing.T) {
	execution := openConformanceExecution(t, "a-cost-bound-refuses-the-machine-the-class-would-buy")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive a Run whose caller bounded what it may cost: %v", err)
	}

	decision := bookingDecisions(t, execution)["run-watched"]
	if decision.SelectedOfferSnapshotID != "rental-cold" {
		t.Fatalf("the interactive Run landed on %q, and the machine its class prefers costs more than its caller allowed",
			decision.SelectedOfferSnapshotID)
	}
	warm := candidateFor(t, decision, "rental-warm")
	if warm.Feasible {
		t.Fatalf("the machine over the bound was feasible, scored %.4f, and cost %.4f USD",
			warm.ScoreUSD, warm.Estimates.CostUSD.Expected)
	}
	if warm.Rejections[0].Code != "COST_LIMIT_EXCEEDED" || warm.Rejections[0].Path != "placement.max_expected_cost_usd" {
		t.Fatalf("the record says the machine was refused as %+v, and what it exceeded is the bound on cost", warm.Rejections[0])
	}
	// The case is only about a bound if the class would have bought the machine the
	// bound refused. A world where the cheap machine also wins on score asserts
	// nothing about a limit.
	cold := candidateFor(t, decision, "rental-cold")
	if warm.ScoreUSD >= cold.ScoreUSD {
		t.Fatalf("the refused machine scored %.4f against the selected machine's %.4f, and this Run's class is supposed to prefer the one it may not have",
			warm.ScoreUSD, cold.ScoreUSD)
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("check invariants: %v", err)
	}
	bounds := invariantResultByID(t, latestInvariantResults(execution.invariants), "safety.class_bounds_honoured")
	if bounds.Status != InvariantPassed {
		t.Fatalf("the bounds law reports %+v", bounds)
	}
}

// TestAgingLiftsABatchRunPastSustainedArrivals is the claim the phase goal asks to
// be proved, under the real control plane. The placement corpus can show one moment
// of an ordering; starvation is a claim about what half an hour of arrivals does to
// a Run, so only an execution can state it.
//
// One machine, twenty four interactive Runs arriving on it over half an hour, and
// one batch Run whose base priority of twenty leaves it behind every one of them.
// What lifts it is the age of its own wait, and the fixture is only about that: the
// arrivals never stop, and the machine never has a moment free that nothing else
// wants.
//
// It is driven in thirty second advances rather than to completion, because this
// queue is decided between arrivals. A freed Booking position is given to whatever
// outranks the rest of the queue on the sweep that notices it, and driving from one
// arrival to the world's next horizon steps over every one of those sweeps.
func TestAgingLiftsABatchRunPastSustainedArrivals(t *testing.T) {
	execution := openConformanceExecution(t, "a-batch-run-eventually-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInSweeps(t, execution, 30*time.Second, 150)

	// The proof is that the arrivals were overtaken while they were still arriving,
	// and it is read off an interactive Run being told it waits behind the batch Run.
	// Interactive work starts at the highest priority any class declares, so nothing
	// but the age of its own wait can put a batch Run in front of one.
	overtaken := deferredBehind(t, execution, "run-quiet")
	if len(overtaken) == 0 {
		t.Fatal("no interactive Run was ever told it waits behind the batch Run, so nothing was overtaken")
	}
	// It waited half an hour to get there, which is what makes the fixture about
	// aging rather than about a machine that happened to be free.
	waited := admittedAfterWaiting(t, execution, "run-quiet")
	if waited < 25*time.Minute {
		t.Fatalf("the batch Run was admitted after waiting %s, and a wait this fixture can explain by anything other than aging proves nothing", waited)
	}
	// And it ran, which is the whole claim. A batch Run merely told to wait less
	// often is a batch Run that starved.
	quiet := projectedRun(t, execution, "run-quiet")
	if quiet.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("the batch Run ended %q in phase %q, and aging is supposed to have let it run", quiet.Outcome, quiet.Phase)
	}
	starvation := invariantResultByID(t, latestInvariantResults(execution.invariants), "liveness.aging_prevents_starvation")
	if starvation.Status != InvariantPassed {
		t.Fatalf("the starvation law reports %+v", starvation)
	}
}

// TestAQueueDelayBoundIsRefusedLoudlyUnderTheRealControlPlane is the other door
// admission leaves by, at L1. The class here declares no deadline, so its maximum
// queue delay is the only thing that can end this wait: before the bound was a
// refusal, this Run waited for ever against a fleet that had already said it holds
// no machine which could ever take it.
//
// The starvation law has to stay silent about the refusal, which is the half of the
// rule that keeps it from calling a fleet too small a queue that wronged somebody.
func TestAQueueDelayBoundIsRefusedLoudlyUnderTheRealControlPlane(t *testing.T) {
	execution := openConformanceExecution(t, "a-queue-delay-bound-is-refused-loudly")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInSweeps(t, execution, 5*time.Minute, 25)

	refusal := refusalRecord(t, execution, "run-spare")
	if refusal.Reason != domain.RefusedQueueDelayExceeded {
		t.Fatalf("the wait ended as %q, and what this Run passed is the longest wait its class allows", refusal.Reason)
	}
	if refusal.QueuedSeconds <= refusal.MaxQueueDelaySeconds {
		t.Fatalf("the refusal says the Run waited %.0fs against a bound of %.0fs, which is not a bound anything passed",
			refusal.QueuedSeconds, refusal.MaxQueueDelaySeconds)
	}
	// Loudly means the caller can read it off the Run. A refusal that left the Run
	// open would be the wait it replaced with more words in the log.
	spare := projectedRun(t, execution, "run-spare")
	if !spare.Closed || spare.Outcome != domain.RunOutcomeFailed {
		t.Fatalf("the refused Run is recorded closed=%v outcome=%q", spare.Closed, spare.Outcome)
	}
	starvation := invariantResultByID(t, latestInvariantResults(execution.invariants), "liveness.aging_prevents_starvation")
	if starvation.Status != InvariantPassed {
		t.Fatalf("the starvation law read a fleet that can hold nothing as a Run somebody stepped over: %+v", starvation)
	}
}

// TestARefusedQueueDelayIsStarvationWhenYoungerWorkOvertookIt is the deliberate
// failure of the clause that keeps the starvation law from being satisfied by
// refusing everything. It is the world deleting the aging term produces: a batch
// Run refused an hour into its wait, and arrivals admitted past it long after the
// moment its own class promises to have promoted it above anything arriving.
func TestARefusedQueueDelayIsStarvationWhenYoungerWorkOvertookIt(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, nil, []eventlog.CloudEvent{
		admissionDeferredEvent("run-quiet", now, domain.ClassBatch),
		admittedForClassEvent("run-urgent", domain.ClassInteractive, now.Add(31*time.Minute)),
		refusedWaitEvent("run-quiet", now.Add(61*time.Minute), domain.ClassBatch, domain.RefusedQueueDelayExceeded, &domain.FleetAnswer{Weighed: 1, CouldHold: 1}),
	})

	err := agingPreventsStarvation(observation)
	if err == nil {
		t.Fatal("a batch Run was refused for a wait a fresh arrival was admitted in the middle of, and the rule allowed it")
	}
	t.Logf("violation: %v", err)
}

// TestARefusedQueueDelayIsNotStarvationWhenTheFleetCouldHoldNothing is the whole of
// the exemption. Every machine the fleet published was weighed against this Run and
// none of them can ever take it, so no ordering could have placed it and the refusal
// is Mercator reporting a fleet too small.
//
// Every machine means every moment of the wait, and the fixture says so from the
// first deferral. A wait whose opening answer records a machine that could have taken
// this Run is a wait some ordering could have ended, and this law convicts that: the
// refusal at the end says the fleet has nothing now, which is not a statement about
// the hour the Run spent waiting behind work that took the machine it wanted.
func TestARefusedQueueDelayIsNotStarvationWhenTheFleetCouldHoldNothing(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, nil, []eventlog.CloudEvent{
		deferralEvent("run-impossible", now, domain.AdmissionDeferral{
			Reason: domain.DeferredNoCapacityFits,
			Class:  domain.ClassBatch,
			Fleet:  &domain.FleetAnswer{Weighed: 1},
		}),
		admittedForClassEvent("run-urgent", domain.ClassInteractive, now.Add(31*time.Minute)),
		refusedWaitEvent("run-impossible", now.Add(61*time.Minute), domain.ClassBatch, domain.RefusedQueueDelayExceeded, &domain.FleetAnswer{Weighed: 1}),
	})

	if err := agingPreventsStarvation(observation); err != nil {
		t.Fatalf("a fleet that can hold nothing was read as a queue that wronged somebody: %v", err)
	}
}

// TestARefusedQueueDelayIsStarvationWhenTheFleetOnceHeldIt is the other half of the
// same reading, and it is the world the fixture above states with one number changed.
//
// The fleet published a machine that could have taken this batch Run at the moment it
// was told to wait. An interactive Run that had waited nothing was admitted half an
// hour later, past the point batch work promises to have been promoted above anything
// arriving, and the refusal at the bound records a fleet that by then held nothing.
// The wait ended on a fleet too small; it did not begin on one, and the exemption is
// about whether any ordering could have ended the wait rather than about the last
// thing measured before it was refused.
func TestARefusedQueueDelayIsStarvationWhenTheFleetOnceHeldIt(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, nil, []eventlog.CloudEvent{
		admissionDeferredEvent("run-quiet", now, domain.ClassBatch),
		admittedForClassEvent("run-urgent", domain.ClassInteractive, now.Add(31*time.Minute)),
		refusedWaitEvent("run-quiet", now.Add(61*time.Minute), domain.ClassBatch,
			domain.RefusedQueueDelayExceeded, &domain.FleetAnswer{Weighed: 1}),
	})

	err := agingPreventsStarvation(observation)
	if err == nil {
		t.Fatal("a Run the fleet could hold when its wait began was exempted by what the fleet said an hour later")
	}
	t.Logf("violation: %v", err)
}

// TestAPlacementRevokesTheFleetExemption is the deliberate failure of that
// exemption, and it is the world a carried answer made permanently exempt.
//
// The fleet is asked once, at the very start, and holds no machine that could take
// this batch Run. Then Mercator selects a machine for that same Run, which is the
// strongest statement there is that the fleet could hold it. The launch fails, the
// Run comes back through admission behind work that outranks it, an interactive Run
// that had waited nothing is admitted past it well after the moment batch work
// promises to have been promoted, and the wait is refused at its bound through the
// priority door with no fleet answer on it at all.
//
// This is textbook starvation of a Run the fleet demonstrably held, and reading only
// the last answer anybody recorded left the law silent on it: every deferral after
// the placement carried no fleet, so the stale exemption from the first moment of the
// wait outlived the placement that refuted it.
func TestAPlacementRevokesTheFleetExemption(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now.Add(3602*time.Second), nil, []eventlog.CloudEvent{
		deferralEvent("run-quiet", now, domain.AdmissionDeferral{
			Reason: domain.DeferredNoCapacityFits,
			Class:  domain.ClassBatch,
			Fleet:  &domain.FleetAnswer{Weighed: 1},
		}),
		admittedForClassEvent("run-quiet", domain.ClassBatch, now.Add(600*time.Second)),
		deferredForEvent("run-quiet", now.Add(700*time.Second), domain.ClassBatch,
			domain.DeferredBehindHigherPriority, "run-other"),
		admittedForClassEvent("run-fresh", domain.ClassInteractive, now.Add(2000*time.Second)),
		refusedWaitEvent("run-quiet", now.Add(3601*time.Second), domain.ClassBatch,
			domain.RefusedQueueDelayExceeded, nil),
	})

	err := agingPreventsStarvation(observation)
	if err == nil {
		t.Fatal("a Run Mercator itself placed a machine for was exempted from the starvation law by a measurement taken before it")
	}
	t.Logf("violation: %v", err)
}

// TestAWaitPastItsBoundIsJudgedWhateverTheRefusalIsNamedFor is the record this
// tree held before the repair, and it is the deliberate failure of the clause
// reading the wait rather than the reason code.
//
// A standard Run waited four hours and fifty nine minutes, which is sixteen
// thousand seconds past the half hour its class allows, and the refusal that closed
// it named the deadline. Both bounds had gone by, so the word is a choice; the wait
// is not. Filtering the law on QUEUE_DELAY_EXCEEDED made the whole rule silent about
// exactly this record, because the other half skips a Run that is closed.
func TestAWaitPastItsBoundIsJudgedWhateverTheRefusalIsNamedFor(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, nil, []eventlog.CloudEvent{
		admissionDeferredEvent("run-quiet", now, domain.ClassStandard),
		admittedForClassEvent("run-urgent", domain.ClassInteractive, now.Add(3000*time.Second)),
		refusedWaitEvent("run-quiet", now.Add(17940*time.Second), domain.ClassStandard,
			domain.RefusedDeadlineUnreachable, &domain.FleetAnswer{Weighed: 1, CouldHold: 1}),
	})

	err := agingPreventsStarvation(observation)
	if err == nil {
		t.Fatal("a wait sixteen thousand seconds past its class bound was refused under another name and the rule allowed it")
	}
	t.Logf("violation: %v", err)
}

// TestAWaitIsJudgedAgainstItsOwnTenantsQueue is a lawful execution the flat replay
// convicted. Mercator orders each workspace's queue on its own, filtering the log by
// workspace to build it, so no Run in another tenant is ever in a Run's ahead-list
// and no ordering in ws_alpha could have placed ws_beta's work.
func TestAWaitIsJudgedAgainstItsOwnTenantsQueue(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, nil, []eventlog.CloudEvent{
		inWorkspace(admissionDeferredEvent("run-quiet", now, domain.ClassBatch), "ws_alpha"),
		inWorkspace(admittedForClassEvent("run-other-tenant", domain.ClassInteractive, now.Add(1900*time.Second)), "ws_beta"),
		inWorkspace(refusedWaitEvent("run-quiet", now.Add(3601*time.Second), domain.ClassBatch,
			domain.RefusedQueueDelayExceeded, &domain.FleetAnswer{Weighed: 1, CouldHold: 1}), "ws_alpha"),
	})

	if err := agingPreventsStarvation(observation); err != nil {
		t.Fatalf("an admission in another tenant convicted a queue it never competed with: %v", err)
	}
}

// TestAReplacedRunCarriesTheWaitProductionHeldItAt is the other lawful execution the
// replay convicted. A launch that failed for capacity nobody has left sends a Run
// back through admission, and the second decision that takes a machine for it is a
// replacement rather than an arrival.
//
// Production holds that Run at the standing of its whole wait, because queuedSince is
// set at the first deferral and nothing clears it. The replay used to start the wait
// again at every placement, so it read the oldest Run in the queue as work that had
// waited nothing and convicted the batch Run's refusal on it.
func TestAReplacedRunCarriesTheWaitProductionHeldItAt(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, nil, []eventlog.CloudEvent{
		admissionDeferredEvent("run-watched", now.Add(-1000*time.Second), domain.ClassInteractive),
		admissionDeferredEvent("run-quiet", now, domain.ClassBatch),
		admittedForClassEvent("run-watched", domain.ClassInteractive, now.Add(1000*time.Second)),
		admittedForClassEvent("run-watched", domain.ClassInteractive, now.Add(2000*time.Second)),
		refusedWaitEvent("run-quiet", now.Add(3601*time.Second), domain.ClassBatch,
			domain.RefusedQueueDelayExceeded, &domain.FleetAnswer{Weighed: 1, CouldHold: 1}),
	})

	if err := agingPreventsStarvation(observation); err != nil {
		t.Fatalf("a Run placed again after a failed launch was read as a fresh arrival that had waited nothing: %v", err)
	}
}

// TestARefusedQueueDelayIsNotStarvationWhenNothingEverHeldIt is the exemption read
// off every measurement taken during the wait rather than off the refusal alone.
//
// A Run no machine in this fleet can hold, once it is also behind work that outranks
// it, is deferred by the priority door: that wait weighs no machine, so it carries no
// fleet answer, and neither does the refusal that ends it at the bound. Reading only
// the refusal's own answer therefore reported starvation for a Run no ordering could
// ever have placed, which is the one thing the exemption exists to prevent.
func TestARefusedQueueDelayIsNotStarvationWhenNothingEverHeldIt(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, nil, []eventlog.CloudEvent{
		deferralEvent("run-unholdable", now, domain.AdmissionDeferral{
			Reason: domain.DeferredNoCapacityFits,
			Class:  domain.ClassInteractive,
			Fleet:  &domain.FleetAnswer{Weighed: 1},
		}),
		deferredForEvent("run-unholdable", now.Add(100*time.Second), domain.ClassInteractive,
			domain.DeferredBehindHigherPriority, "run-older"),
		admittedForClassEvent("run-fresh", domain.ClassInteractive, now.Add(200*time.Second)),
		refusedWaitEvent("run-unholdable", now.Add(301*time.Second), domain.ClassInteractive,
			domain.RefusedQueueDelayExceeded, nil),
	})

	if err := agingPreventsStarvation(observation); err != nil {
		t.Fatalf("a Run the fleet had already said it can never hold was read as a queue that wronged somebody: %v", err)
	}
}

// TestAReplacedRunIsHeldToTheDeadlineOfItsWholeWait is the deliberate failure of
// safety.class_bounds_honoured on the shape a failed launch produces, and it is the
// record the third law in this file could not see.
//
// One wait with two placements in it. A standard Run is told to wait, a machine is
// selected for it, the launch fails for capacity nobody has, it comes back through
// admission, and four hours later a second machine is selected. Production refuses
// that second placement, because stepAdmit reads the whole wait off queuedSince and
// four hours and ten minutes is past the four hours this class states. A law that
// restarted its clock at the first placement measured the remainder and returned
// nothing, so it could not fail for any Run placed past its deadline that had been
// placed once already.
func TestAReplacedRunIsHeldToTheDeadlineOfItsWholeWait(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now.Add(15001*time.Second), nil, []eventlog.CloudEvent{
		admissionDeferredEvent("run-quiet", now, domain.ClassStandard),
		admittedForClassEvent("run-quiet", domain.ClassStandard, now.Add(1000*time.Second)),
		deferredForEvent("run-quiet", now.Add(1100*time.Second), domain.ClassStandard,
			domain.DeferredBehindHigherPriority, "run-other"),
		admittedForClassEvent("run-quiet", domain.ClassStandard, now.Add(15000*time.Second)),
	})

	err := classBoundsHonoured(observation)
	if err == nil {
		t.Fatal("a Run was placed four hours and ten minutes into a wait its class bounds at four hours, and the rule allowed it")
	}
	t.Logf("violation: %v", err)
}

// TestARefusedQueueDelayIsNotStarvationWhenOlderWorkWasAdmitted is the other thing
// the clause has to leave alone, and it is the state the aging fixture's own fleet
// spends half an hour in. More interactive work is arriving than one machine can
// serve, so the excess is refused at a bound Mercator cannot honour. Every Run
// admitted ahead of one of them had waited longer than it had, which is the queue
// working rather than anybody being stepped over.
func TestARefusedQueueDelayIsNotStarvationWhenOlderWorkWasAdmitted(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, nil, []eventlog.CloudEvent{
		admissionDeferredEvent("run-older", now.Add(-100*time.Second), domain.ClassInteractive),
		admissionDeferredEvent("run-watched", now, domain.ClassInteractive),
		admittedForClassEvent("run-older", domain.ClassInteractive, now.Add(200*time.Second)),
		refusedWaitEvent("run-watched", now.Add(301*time.Second), domain.ClassInteractive, domain.RefusedQueueDelayExceeded, &domain.FleetAnswer{Weighed: 1, CouldHold: 1}),
	})

	if err := agingPreventsStarvation(observation); err != nil {
		t.Fatalf("a fleet too small for its arrivals was read as starvation: %v", err)
	}
}

// driveInSweeps advances the execution in fixed steps, which is how a fixture about
// a queue is driven. DriveToCompletion jumps to whatever the world still owes, so
// every moment between the last arrival and the next completion, which is where the
// ordering is decided and where a class bound falls, happens in one advance with no
// sweep inside it.
func driveInSweeps(t *testing.T, execution *Execution, step time.Duration, sweeps int) {
	t.Helper()
	start := execution.now
	for range sweeps {
		if _, err := execution.Drive(context.Background(), Advance(step)); err != nil {
			t.Fatalf("advance to %s: %v", execution.now.Add(step).Sub(start), err)
		}
	}
}

// admittedAfterWaiting is how long the record says one Run waited before a decision
// took a machine for it, measured the way the class bounds are: from the first time
// admission told it to wait.
func admittedAfterWaiting(t *testing.T, execution *Execution, runID string) time.Duration {
	t.Helper()
	var since time.Time
	for _, event := range publicRunEvents(t, execution) {
		if !strings.HasSuffix(event.StreamID, runID) {
			continue
		}
		switch event.Type {
		case orchestrator.EventAdmissionDeferred:
			if since.IsZero() {
				since = event.OccurredAt.UTC()
			}
		case orchestrator.EventBookingDecided:
			var payload struct {
				Decision domain.BookingDecision `json:"decision"`
			}
			if err := json.Unmarshal(event.Data, &payload); err != nil {
				t.Fatalf("read the decision: %v", err)
			}
			if payload.Decision.SelectedOfferSnapshotID == "" || since.IsZero() {
				continue
			}
			return event.OccurredAt.UTC().Sub(since)
		}
	}
	t.Fatalf("nothing in the record ever placed Run %q", runID)
	return 0
}

// deferredBehind is every Run the record says was told it waits behind this one.
func deferredBehind(t *testing.T, execution *Execution, runID string) []string {
	t.Helper()
	var behind []string
	for _, event := range publicRunEvents(t, execution) {
		if event.Type != orchestrator.EventAdmissionDeferred {
			continue
		}
		var payload struct {
			Deferral domain.AdmissionDeferral `json:"deferral"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatalf("read the deferral: %v", err)
		}
		for _, ahead := range payload.Deferral.Behind {
			if ahead.RunID == runID {
				behind = append(behind, strings.TrimPrefix(event.StreamID, "runs/"))
			}
		}
	}
	return behind
}

// refusalRecord is the wait admission refused to go on holding, read off the public
// log.
func refusalRecord(t *testing.T, execution *Execution, runID string) domain.AdmissionDeferral {
	t.Helper()
	for _, event := range publicRunEvents(t, execution) {
		if event.Type != orchestrator.EventAdmissionRefused || !strings.HasSuffix(event.StreamID, runID) {
			continue
		}
		var payload struct {
			Deferral domain.AdmissionDeferral `json:"deferral"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatalf("read the refusal: %v", err)
		}
		return payload.Deferral
	}
	t.Fatalf("admission never refused Run %q", runID)
	return domain.AdmissionDeferral{}
}

func publicRunEvents(t *testing.T, execution *Execution) []eventlog.StoredEvent {
	t.Helper()
	events, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	return events
}

// admittedForClassEvent is a Booking Decision that took a machine for a Run of a
// stated class. The class is on the decision because that is where the starvation
// rule reads it: what a Run was worth when it was admitted is a fact about the
// decision rather than about today's class table.
func admittedForClassEvent(runID string, class domain.ServiceClass, at time.Time) eventlog.CloudEvent {
	event := bookingDecidedEvent("decided-"+runID+"-"+at.Format(time.RFC3339Nano), domain.BookingDecision{
		RunID:                   runID,
		SelectedOfferSnapshotID: "offer-1",
		Policy:                  domain.PlacementPolicy{Class: class},
	})
	event.Subject = "runs/" + runID
	event.Time = at.Format(time.RFC3339Nano)
	return event
}

// refusedWaitEvent is admission ending a wait, named for the bound it says the wait
// passed and carrying the fleet answer it rested on. The reason is a parameter
// because the starvation law reads the wait rather than the word: a fixture that
// could only state one reason could not say what the law does about the other.
func refusedWaitEvent(runID string, at time.Time, class domain.ServiceClass, reason string, fleet *domain.FleetAnswer) eventlog.CloudEvent {
	event := deferralEvent(runID, at, domain.AdmissionDeferral{
		Reason: reason,
		Class:  class,
		Fleet:  fleet,
	})
	event.Type = orchestrator.EventAdmissionRefused
	return event
}

// inWorkspace is one recorded fact filed under the tenant it happened in, which is
// how every event Mercator appends arrives and what the queue is partitioned by.
func inWorkspace(event eventlog.CloudEvent, workspaceID string) eventlog.CloudEvent {
	event.WorkspaceID = workspaceID
	return event
}

// admissionRecord is the first thing admission said about one Run, read off the
// public log the way an operator reads it, and whether it said anything at all: a
// Run the fleet had room for on arrival was never told to wait.
func admissionRecord(t *testing.T, execution *Execution, runID string) (domain.AdmissionDeferral, bool) {
	t.Helper()
	events, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	for _, event := range events {
		if event.Type != orchestrator.EventAdmissionDeferred || !strings.HasSuffix(event.StreamID, runID) {
			continue
		}
		var payload struct {
			Deferral domain.AdmissionDeferral `json:"deferral"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatalf("read the deferral: %v", err)
		}
		return payload.Deferral, true
	}
	return domain.AdmissionDeferral{}, false
}

func admissionObservation(now time.Time, workloads map[string]domain.WorkloadRevision, events []eventlog.CloudEvent) InvariantObservation {
	return InvariantObservation{
		StartedAt:      now,
		Now:            now,
		World:          WorldTruthSnapshot{At: now},
		Workloads:      workloads,
		MercatorEvents: events,
	}
}
