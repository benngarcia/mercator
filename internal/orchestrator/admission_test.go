package orchestrator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/scheduler"
)

// TestARunNothingCanTakeWaitsInsteadOfSpinning is the loop the queue left behind.
// Admission turned a Run no candidate would take from an error into a deferral,
// and a deferral the record already carries appends nothing, so a placement that
// reported progress made AdvanceRun re-derive the same state and defer again
// without end. One submission to a control plane with no capacity spun a core
// inside its own request, holding that Run's lock, and never answered.
//
// The bound is what states the claim: the fixed loop stops in milliseconds, and
// the broken one only stops when the deadline kills the query it is in the middle
// of, which is what makes this a failure rather than a hang.
//
// It advances twice because the suppression is a claim about the second pass. One
// advance counts one deferral whether anything suppresses anything or not, which
// is the assertion this case used to make.
func TestARunNothingCanTakeWaitsInsteadOfSpinning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	orch := newTestOrchestrator(t, fake.New())
	submitClassed(t, ctx, orch, "run_unplaceable", domain.ClassStandard)

	for range 2 {
		if err := orch.AdvanceRun(ctx, "ws_1", "run_unplaceable"); err != nil {
			t.Fatalf("advance a Run nothing can take: %v", err)
		}
	}

	events := runEvents(t, ctx, orch, "run_unplaceable")
	// One wait, stated once. A Run told to wait for the same reason on every pass
	// is a Run whose own stream an operator cannot read.
	if deferrals := countEvents(events, EventAdmissionDeferred); deferrals != 1 {
		t.Fatalf("the Run recorded %d deferrals over two advances, and it has waited once for one reason: %v",
			deferrals, eventTypes(events))
	}
	// A fleet that published nothing is a wait for capacity to be added. An offer
	// query is a search on the shape asked for, so a fleet that answered with
	// nothing has said it holds no machine of that shape, and waiting for the fleet
	// as it stands is waiting for a machine that is not there.
	if reason := deferralReason(t, events); reason != domain.DeferredNoCapacityFits {
		t.Fatalf("the Run waits for %q, and this control plane published no machine that could ever hold it", reason)
	}
	if closed := countEvents(events, EventRunClosed); closed != 0 {
		t.Fatalf("a Run waiting for capacity was closed: %v", eventTypes(events))
	}
}

// TestAWaitThatChangedIsRecordedAndNotRefused is the second deferral of one Run. A
// deferral was appended under a command named after its reason alone, so the second
// time admission said the same thing about a changed queue the append replayed a
// spent command key with a different request hash, the event log refused it as an
// idempotency conflict, and AdvanceRun returned that error to every caller for as
// long as the state held: the refresh answered 502, the sweep logged it every tick,
// and the Run's own record stayed frozen at the stale answer.
//
// Three Runs are the smallest world that reaches it. One waits, one is told it is
// behind that Run, and a third arrival changes what the second one is behind.
//
// The fleet holds one machine that could take any of them once the capacity it is
// spending comes back, because that is the only wait the queue is ordered on. A
// fleet that published nothing is a wait for capacity to be added, and no Run is
// ordered behind an ask nothing in the fleet can hold.
func TestAWaitThatChangedIsRecordedAndNotRefused(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	orch := newTestOrchestrator(t, fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOccupiedOffer("off_busy", time.Now().UTC())})))
	submitClassed(t, ctx, orch, "run_watched", domain.ClassInteractive)
	submitClassed(t, ctx, orch, "run_watched_too", domain.ClassInteractive)
	submitClassed(t, ctx, orch, "run_quiet", domain.ClassStandard)

	for _, runID := range []string{"run_watched", "run_quiet", "run_watched_too", "run_quiet"} {
		if err := orch.AdvanceRun(ctx, "ws_1", runID); err != nil {
			t.Fatalf("advance %q the way a sweep does: %v", runID, err)
		}
	}

	events := runEvents(t, ctx, orch, "run_quiet")
	if deferrals := countEvents(events, EventAdmissionDeferred); deferrals != 2 {
		t.Fatalf("the Run recorded %d deferrals, and what it waits behind changed once: %v",
			deferrals, eventTypes(events))
	}
	if behind := deferralBehind(t, events); len(behind) != 2 {
		t.Fatalf("the Run waits behind %v, and two Runs of a class that outranks it are queued", behind)
	}
	if reason := deferralReason(t, events); reason != domain.DeferredBehindHigherPriority {
		t.Fatalf("the Run waits for %q, and what holds it is the queue in front of it", reason)
	}
}

// TestAWaitPastItsDeadlineIsRefusedBehindTheQueueToo is the deadline rule reaching
// the wait admission itself causes. It was asked only of a Run Placement would not
// place, so a Run held behind work that outranks it was told to wait again on every
// tick for ever, past the moment its own class says the answer stopped being worth
// having, and the queue in front of it was the one thing that could keep it there.
//
// An interactive Run behind an experimental one is where the two curves cross. The
// patient class ages faster, because it is derived from a bound four times as long,
// so it passes the watched class after eight minutes and is still ahead of it at
// the ten minutes the watched class says is the end of the answer being worth
// having.
func TestAWaitPastItsDeadlineIsRefusedBehindTheQueueToo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	provider := fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOccupiedOffer("off_busy", now)}))
	orch := New(openOrchestratorLog(t), scheduler.New(), provider, WithClock(func() time.Time { return now }))
	submitClassed(t, ctx, orch, "run_iterating", domain.ClassExperimental)
	submitClassed(t, ctx, orch, "run_watched", domain.ClassInteractive)
	for _, runID := range []string{"run_iterating", "run_watched"} {
		if err := orch.AdvanceRun(ctx, "ws_1", runID); err != nil {
			t.Fatalf("advance %q the way a sweep does: %v", runID, err)
		}
	}

	deadline := domain.ClassInteractive.Admission().DeadlineSeconds
	now = now.Add(time.Duration(deadline+1) * time.Second)
	if err := orch.AdvanceRun(ctx, "ws_1", "run_watched"); err != nil {
		t.Fatalf("advance a Run whose deadline passed while it waited: %v", err)
	}

	events := runEvents(t, ctx, orch, "run_watched")
	if behind := deferralBehind(t, events); len(behind) != 1 {
		t.Fatalf("the Run was refused for its own deadline and not while held behind %v", behind)
	}
	if refused := countEvents(events, EventAdmissionRefused); refused != 1 {
		t.Fatalf("a Run %.0fs past the deadline its class states is still queued: %v", deadline, eventTypes(events))
	}
	if closed := countEvents(events, EventRunClosed); closed != 1 {
		t.Fatalf("admission refused the Run and left it open: %v", eventTypes(events))
	}
}

// TestAnImpossibleAskDoesNotStopWorkThatFits is head-of-line blocking. The queue
// ordered each Run against every wait worth more than its own and never asked
// whether that work was waiting for anything that can arrive, so one Run asking for
// more room than any machine in the fleet has left the fleet idle: the Run that fit
// it was queued behind an ask nothing could satisfy, until the impossible Run's own
// class deadline cleared it, which for a class declaring none is never.
func TestAnImpossibleAskDoesNotStopWorkThatFits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	offer := orchProvisionableOffer("off_small", time.Now().UTC())
	orch := newTestOrchestrator(t, fake.New(fake.WithOffers([]domain.OfferSnapshot{offer})))
	submitDisk(t, ctx, orch, "run_oversized", offer.Resources.EphemeralDiskBytes*100)
	submitDisk(t, ctx, orch, "run_fits", offer.Resources.EphemeralDiskBytes/2)

	for _, runID := range []string{"run_oversized", "run_fits"} {
		if err := orch.AdvanceRun(ctx, "ws_1", runID); err != nil {
			t.Fatalf("advance %q the way a sweep does: %v", runID, err)
		}
	}

	oversized := runEvents(t, ctx, orch, "run_oversized")
	if reason := deferralReason(t, oversized); reason != domain.DeferredNoCapacityFits {
		t.Fatalf("a Run asking for a hundred times the room this fleet has waits for %q", reason)
	}
	fits := runEvents(t, ctx, orch, "run_fits")
	if placed := countEvents(fits, EventLaunchIntentRecorded); placed != 1 {
		t.Fatalf("the machine sat idle beside a Run that fits it: %v", eventTypes(fits))
	}
}

// TestAPlacementDoesNotRestartTheWaitTheQueueOrdersOn is the queue and the Run's
// own door reading one wait the same way.
//
// A Run's standing rests on two facts and the queue kept one. Membership of the
// queue is who is waiting on a decision now, and it ends when a Run takes a
// machine. The moment its wait began is what every bound its class states is
// measured from, and runState.queuedSince holds that from the first deferral
// without ever clearing it. The queue dropped it at the placement, so a Run
// deferred, placed, and told to wait again was weighed by its own door at the
// standing of the whole wait and by every other Run in the tenant as an arrival
// that had waited nothing: it went on ageing toward a queue delay measured from a
// moment nobody else could see, while fresh work of a higher class was admitted
// past a Run that outranked it.
//
// The history is stated rather than driven, because no production path reaches it
// yet. A replacement that finds no machine closes the Run as RETRY_EXHAUSTED, and
// expiring a Booking past its latest start and re-placing its Run is the unbuilt
// schedule advancement the corpus still carries as a target. Both readings of the
// wait are in the tree today, and this is the record that will tell them apart.
func TestAPlacementDoesNotRestartTheWaitTheQueueOrdersOn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	provider := fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOccupiedOffer("off_busy", now)}))
	orch := New(openOrchestratorLog(t), scheduler.New(), provider, WithClock(func() time.Time { return now }))
	appendWaitPlacedAndWaitedAgain(t, ctx, orch, "run_quiet", domain.ClassBatch, now)
	submitClassed(t, ctx, orch, "run_fresh", domain.ClassStandard)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_fresh"); err != nil {
		t.Fatalf("advance a fresh Run into a queue holding a replaced one: %v", err)
	}

	fresh := runEvents(t, ctx, orch, "run_fresh")
	if reason := deferralReason(t, fresh); reason != domain.DeferredBehindHigherPriority {
		t.Fatalf("a standard Run was told it waits for %q, and a batch Run twenty minutes into its wait is worth more than it is", reason)
	}
	if behind := deferralBehind(t, fresh); len(behind) != 1 || behind[0] != "run_quiet" {
		t.Fatalf("the record says the fresh Run waits behind %v, and the batch Run had waited twenty minutes", behind)
	}
}

// appendWaitPlacedAndWaitedAgain states one Run's whole wait as the log carries it:
// admission told it to wait, a decision took a machine for it, and admission told
// it to wait again. The moments are stated because what this is about is a wait
// measured across the placement in the middle of it.
func appendWaitPlacedAndWaitedAgain(
	t *testing.T,
	ctx context.Context,
	orch *Orchestrator,
	runID string,
	class domain.ServiceClass,
	now time.Time,
) {
	t.Helper()
	deferral, err := json.Marshal(admissionDeferredData{Deferral: domain.AdmissionDeferral{
		Reason: domain.DeferredNoFeasibleOffer,
		Class:  class,
		Fleet:  &domain.FleetAnswer{Weighed: 1, CouldHold: 1},
	}})
	if err != nil {
		t.Fatalf("state the wait: %v", err)
	}
	placed, err := json.Marshal(bookingDecisionData{Decision: domain.BookingDecision{
		RunID:                   runID,
		SelectedOfferSnapshotID: "off_busy",
		Policy:                  domain.PlacementPolicy{Class: class},
	}})
	if err != nil {
		t.Fatalf("state the placement: %v", err)
	}
	if _, err := orch.log.Append(ctx, eventlog.AppendRequest{
		Stream:      eventlog.StreamKey{WorkspaceID: "ws_1", Type: "run", ID: runID},
		CommandKey:  "state:" + runID,
		RequestHash: "sha256:" + runID,
		Events: []eventlog.NewEvent{
			{ID: "admission_1", Type: EventAdmissionDeferred, SchemaVersion: 1, OccurredAt: now.Add(-20 * time.Minute), Data: deferral},
			{ID: "decided_1", Type: EventBookingDecided, SchemaVersion: 1, OccurredAt: now.Add(-2 * time.Minute), Data: placed},
			{ID: "admission_2", Type: EventAdmissionDeferred, SchemaVersion: 1, OccurredAt: now.Add(-time.Minute), Data: deferral},
		},
	}); err != nil {
		t.Fatalf("state the wait of %q: %v", runID, err)
	}
}

// orchOccupiedOffer is one machine this fleet holds that could run the work and is
// not free to right now, which is the only wait other work in the queue has to be
// ordered behind. It says so with its own capacity evidence: a refusal that names
// capacity somebody is spending ends when they stop spending it.
func orchOccupiedOffer(id string, now time.Time) domain.OfferSnapshot {
	offer := orchProvisionableOffer(id, now)
	offer.Capacity.Available = false
	return offer
}

// submitClassed submits one Run of a stated class without advancing it, so a test
// can drive the sweep itself.
func submitClassed(t *testing.T, ctx context.Context, orch *Orchestrator, runID string, class domain.ServiceClass) {
	t.Helper()
	revision := orchRevision()
	revision.Spec.Placement.Class = class
	submitRevision(t, ctx, orch, runID, revision)
}

// submitDisk submits one Run that asks for a stated amount of room.
func submitDisk(t *testing.T, ctx context.Context, orch *Orchestrator, runID string, disk int64) {
	t.Helper()
	revision := orchRevision()
	revision.Spec.Resources.EphemeralDisk.MinBytes = disk
	submitRevision(t, ctx, orch, runID, revision)
}

func submitRevision(t *testing.T, ctx context.Context, orch *Orchestrator, runID string, revision domain.WorkloadRevision) {
	t.Helper()
	if _, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          runID,
		IdempotencyKey: "idem_" + runID,
		Workload:       revision,
	}); err != nil {
		t.Fatalf("submit %q: %v", runID, err)
	}
}

func runEvents(t *testing.T, ctx context.Context, orch *Orchestrator, runID string) []eventlog.StoredEvent {
	t.Helper()
	events, err := orch.GetRunEvents(ctx, "ws_1", runID)
	if err != nil {
		t.Fatalf("read the stream of %q: %v", runID, err)
	}
	return events
}

// deferralReason is what the last thing admission said about a Run says it is
// waiting for, read off the Run's own stream.
func deferralReason(t *testing.T, events []eventlog.StoredEvent) string {
	t.Helper()
	return latestDeferral(t, events).Reason
}

// deferralBehind names the work the last thing admission said puts in front of
// this Run.
func deferralBehind(t *testing.T, events []eventlog.StoredEvent) []string {
	t.Helper()
	names := []string{}
	for _, ahead := range latestDeferral(t, events).Behind {
		names = append(names, ahead.RunID)
	}
	return names
}

// latestDeferral is the last thing admission said about this Run, whether it said
// it as a wait or as a refusal: both are the same account of the same question.
func latestDeferral(t *testing.T, events []eventlog.StoredEvent) domain.AdmissionDeferral {
	t.Helper()
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type != EventAdmissionDeferred && events[index].Type != EventAdmissionRefused {
			continue
		}
		var data admissionDeferredData
		if err := json.Unmarshal(events[index].Data, &data); err != nil {
			t.Fatalf("read the deferral: %v", err)
		}
		return data.Deferral
	}
	t.Fatalf("admission recorded nothing about this Run: %v", eventTypes(events))
	return domain.AdmissionDeferral{}
}

// TestAFamilyBurstSubmittedIsStillHeldToItsWidth is the family bound under the
// arrival pattern it exists for. A sweep is submitted all at once, Intake advances
// each Run inside its own request, and the bound is decided by replaying the
// workspace log and then appending to one Run's own stream. Two members asked at
// the same instant therefore both read a family holding nothing and both took a
// machine, so a family declared one wide held two, and nothing in the corpus could
// see it: the Lab drives admission one Run at a time and the daemon case submits in
// sequence.
//
// The offers are provisionable on purpose. A queued Booking on an existing Rental
// commits through a Rental Schedule whose version is checked, so two members
// competing for one machine would be serialised by that check for a reason that has
// nothing to do with the family. Provisioning mints a fresh Rental per Booking, so
// there is no shared version anywhere and the width is the only thing that can hold
// the second member back.
func TestAFamilyBurstSubmittedIsStillHeldToItsWidth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	sweep := domain.RunGroup{ID: "sweep", MaxParallel: 1}
	provider := fake.New(fake.WithOffers([]domain.OfferSnapshot{
		orchProvisionableOffer("off_one", now),
		orchProvisionableOffer("off_two", now),
	}))
	orch := New(openOrchestratorLog(t), scheduler.New(), provider, WithClock(func() time.Time { return now }))
	members := []string{"run_a", "run_b"}
	for _, runID := range members {
		submitInFamily(t, ctx, orch, runID, sweep)
	}

	advanced := make(chan error, len(members))
	burst := make(chan struct{})
	for _, runID := range members {
		go func() {
			<-burst
			advanced <- orch.AdvanceRun(ctx, "ws_1", runID)
		}()
	}
	close(burst)
	for range members {
		if err := <-advanced; err != nil {
			t.Fatalf("advance a burst-submitted member of a family: %v", err)
		}
	}

	if placed := placedRuns(t, ctx, orch, members); len(placed) != sweep.MaxParallel {
		t.Fatalf("a family declared %d wide was given capacity for %v, and every member of it asked at the same instant",
			sweep.MaxParallel, placed)
	}
}

// placedRuns is the members of a family that were given capacity, read off each
// Run's own stream rather than off a read model, because a decision that selected
// a machine is the fact the bound is counted over.
func placedRuns(t *testing.T, ctx context.Context, orch *Orchestrator, runIDs []string) []string {
	t.Helper()
	placed := []string{}
	for _, runID := range runIDs {
		for _, event := range runEvents(t, ctx, orch, runID) {
			if event.Type != EventBookingDecided {
				continue
			}
			var data bookingDecisionData
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("read the decision of %q: %v", runID, err)
			}
			if data.Decision.SelectedOfferSnapshotID != "" {
				placed = append(placed, runID+" on "+data.Decision.SelectedOfferSnapshotID)
			}
		}
	}
	return placed
}

// TestAFamilyAtItsWidthHoldsItsOwnMembersBack is the group bound over the log the
// queue is really replayed out of, and it states the reading the corpus cannot: the
// member holding the family's one place has a queued Booking on a busy machine
// rather than a running container. Counting what runs instead of what was placed
// would let a family of one commit a second machine here and then run two.
func TestAFamilyAtItsWidthHoldsItsOwnMembersBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	sweep := domain.RunGroup{ID: "sweep", MaxParallel: 1}
	provider := fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOccupiedOffer("off_busy", now)}))
	orch := New(openOrchestratorLog(t), scheduler.New(), provider, WithClock(func() time.Time { return now }))
	appendMemberPlacedInFamily(t, ctx, orch, "run_first", sweep, now, false)
	submitInFamily(t, ctx, orch, "run_second", sweep)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_second"); err != nil {
		t.Fatalf("advance the second member of a family already at its width: %v", err)
	}

	second := runEvents(t, ctx, orch, "run_second")
	if reason := deferralReason(t, second); reason != domain.DeferredGroupAtParallelism {
		t.Fatalf("the second member was told it waits for %q, and what holds it is the width its caller declared", reason)
	}
	if behind := deferralBehind(t, second); len(behind) != 1 || behind[0] != "run_first" {
		t.Fatalf("the record says the second member waits behind %v, and the member holding the family's one place is run_first", behind)
	}
}

// TestAMemberThatGaveItsCapacityBackLeavesRoomForItsFamily is the other side of the
// count: the fact that takes a member out of it. A launch that failed gives the
// Booking back in the same commit, so the member holds nothing and its family has
// room again.
//
// The record it is stated over is a member whose launch has failed and which nothing
// has decided about since, which is the state a sweep interrupted between the failure
// and the replacement leaves behind. That is where the two readings differ.
// a-member-that-gave-its-capacity-back-leaves-room drives the whole thing under the
// real control plane, where the replacement follows in the same pass, and this is the
// moment in the middle of it.
func TestAMemberThatGaveItsCapacityBackLeavesRoomForItsFamily(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC()
	sweep := domain.RunGroup{ID: "sweep", MaxParallel: 1}
	provider := fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOccupiedOffer("off_busy", now)}))
	orch := New(openOrchestratorLog(t), scheduler.New(), provider, WithClock(func() time.Time { return now }))
	appendMemberPlacedInFamily(t, ctx, orch, "run_first", sweep, now, true)
	submitInFamily(t, ctx, orch, "run_second", sweep)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_second"); err != nil {
		t.Fatalf("advance a member whose sibling gave its capacity back: %v", err)
	}

	second := runEvents(t, ctx, orch, "run_second")
	if reason := deferralReason(t, second); reason == domain.DeferredGroupAtParallelism {
		t.Fatalf("the second member is held by a family whose only other member holds no capacity: %v", deferralBehind(t, second))
	}
}

// appendMemberPlacedInFamily states one member of a family as the log carries it:
// admission told it to wait, a decision took a machine for it, and, where the
// machine refused to start the work, the launch failed and admission told it to
// wait a second time.
//
// The launch failure is what gives the capacity back, and it is stated here because
// it is stated in production: both paths that record it complete the Run's Booking
// in the same commit. A fixture that wrote the second deferral without it would be
// asserting that admission alone says a Run holds nothing, which is the reading
// this file is here to keep out of the count.
func appendMemberPlacedInFamily(
	t *testing.T,
	ctx context.Context,
	orch *Orchestrator,
	runID string,
	group domain.RunGroup,
	now time.Time,
	launchFailed bool,
) {
	t.Helper()
	policy := domain.PlacementPolicy{Class: domain.ClassBatch, Group: group}
	deferral, err := json.Marshal(admissionDeferredData{Deferral: domain.AdmissionDeferral{
		Reason: domain.DeferredNoFeasibleOffer,
		Class:  policy.Class,
		Fleet:  &domain.FleetAnswer{Weighed: 1, CouldHold: 1},
	}})
	if err != nil {
		t.Fatalf("state the wait: %v", err)
	}
	placed, err := json.Marshal(bookingDecisionData{Decision: domain.BookingDecision{
		RunID:                   runID,
		SelectedOfferSnapshotID: "off_busy",
		Policy:                  policy,
	}})
	if err != nil {
		t.Fatalf("state the placement: %v", err)
	}
	refused, err := json.Marshal(domain.ProviderError{
		Code:       "ADAPTER_CAPACITY_UNAVAILABLE",
		Retryable:  true,
		SideEffect: string(adapter.SideEffectNone),
		LaunchKey:  "launch_" + runID,
	})
	if err != nil {
		t.Fatalf("state the launch failure: %v", err)
	}
	events := []eventlog.NewEvent{
		{ID: "admission_1", Type: EventAdmissionDeferred, SchemaVersion: 1, OccurredAt: now.Add(-20 * time.Minute), Data: deferral},
		{ID: "decided_1", Type: EventBookingDecided, SchemaVersion: 1, OccurredAt: now.Add(-2 * time.Minute), Data: placed},
	}
	if launchFailed {
		events = append(events, eventlog.NewEvent{
			ID: "launch_failed_1", Type: EventLaunchFailed, SchemaVersion: 1, OccurredAt: now.Add(-90 * time.Second), Data: refused,
		})
	}
	if _, err := orch.log.Append(ctx, eventlog.AppendRequest{
		Stream:      eventlog.StreamKey{WorkspaceID: "ws_1", Type: "run", ID: runID},
		CommandKey:  "state:" + runID,
		RequestHash: "sha256:" + runID,
		Events:      events,
	}); err != nil {
		t.Fatalf("state the placement of %q: %v", runID, err)
	}
}

// submitInFamily submits one Run that belongs to a family of work and states how
// wide that family may run.
func submitInFamily(t *testing.T, ctx context.Context, orch *Orchestrator, runID string, group domain.RunGroup) {
	t.Helper()
	revision := orchRevision()
	revision.Spec.Placement.Class = domain.ClassBatch
	revision.Spec.Placement.Group = group
	submitRevision(t, ctx, orch, runID, revision)
}
