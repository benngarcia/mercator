package orchestrator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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
