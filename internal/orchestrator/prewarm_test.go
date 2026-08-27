package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scheduler"
)

// TestNothingIsPreparedOnCapacityMercatorCannotSee is the rule about a machine
// that has left the catalog while a Run is still queued on it. Offers are the
// only thing Mercator learns capacity from, so a machine absent from the current
// listing is one it can say nothing about: not what it holds, not whether it is
// still there, and not whether a command would reach it.
//
// Wanting content there is worse than useless. The reusable lane is nodes, and a
// node leaves the catalog through the same predicate that makes the node registry
// refuse to hand out its address, so the preparation Mercator states becomes a
// command the Broker refuses and the whole fleet's pass ends on that error. Every
// other Runs' desire then goes unstated, including the withdrawals that stop
// transfers nobody wants any more, and it repeats on every trigger for as long as
// the Booking stays queued there.
func TestNothingIsPreparedOnCapacityMercatorCannotSee(t *testing.T) {
	ctx := context.Background()
	fleet := queuedBehindRunningWork(t)
	fleet.provider.Withdraw(warmRentalID)

	result, err := fleet.orch.Prewarm(ctx)

	if err != nil {
		t.Fatalf("the preparation pass failed over a machine that is no longer offered: %v", err)
	}
	if result.Wanted != 0 {
		t.Fatalf("Mercator wants %d pieces of content prepared on a machine it cannot see", result.Wanted)
	}
	for _, request := range fleet.prepared.requests {
		if len(request.Wanted) != 0 {
			t.Fatalf("a machine absent from the catalog was asked for %+v", request.Wanted)
		}
	}
}

// TestAQueuedRunPreparesTheMachineItIsStillOfferedOn is the other side of the
// same predicate, so that refusing to prepare a machine nobody can see is not
// refusing to prepare anything. The same world with the offer still standing
// states the queued Run's image as content its host should be holding.
func TestAQueuedRunPreparesTheMachineItIsStillOfferedOn(t *testing.T) {
	ctx := context.Background()
	fleet := queuedBehindRunningWork(t)

	result, err := fleet.orch.Prewarm(ctx)

	if err != nil {
		t.Fatalf("prepare the machine the queued Run is going to: %v", err)
	}
	if result.Wanted != 1 || len(fleet.prepared.requests) != 1 {
		t.Fatalf("Mercator wanted %d pieces of content and stated %+v", result.Wanted, fleet.prepared.requests)
	}
	if offer := fleet.prepared.requests[0].Wanted[0].OfferSnapshotID; offer != warmRentalID {
		t.Fatalf("the machine asked to prepare is %q, want the one the Booking named", offer)
	}
}

const warmRentalID = "rental-warm"

// preparedFleet is one machine, one Run running on it, and one Run queued behind
// that, at a moment when the running Run's own content has landed: a host still
// getting ready for work Mercator admitted there is one nothing speculative may
// touch, and this is the state where preparation is allowed to want something.
type preparedFleet struct {
	orch     *Orchestrator
	provider *fake.Adapter
	prepared *recordingPrewarmer
}

func queuedBehindRunningWork(t *testing.T) preparedFleet {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	at := now
	warm := orchOffer(warmRentalID, now)
	warm.ExpiresAt = now.Add(time.Hour)
	provider := fake.New(fake.WithOffers([]domain.OfferSnapshot{warm}), fake.WithNow(func() time.Time { return at }))
	prepared := &recordingPrewarmer{}
	orch := New(
		openOrchestratorLog(t), scheduler.New(), provider,
		WithClock(func() time.Time { return at }),
		WithPrewarm(prepared, PrewarmPolicy{MaxConcurrent: 4}, &memoryPreparationClock{}),

		withTestCapacity(),
	)
	createScheduledRun(t, ctx, orch, "run-active")
	if err := orch.AdvanceRun(ctx, "run-active"); err != nil {
		t.Fatalf("advance the running Run: %v", err)
	}
	createScheduledRun(t, ctx, orch, "run-queued")
	if err := orch.AdvanceRun(ctx, "run-queued"); err != nil {
		t.Fatalf("advance the queued Run: %v", err)
	}
	at = now.Add(10 * time.Minute)
	return preparedFleet{orch: orch, provider: provider, prepared: prepared}
}

// recordingPrewarmer is a machine that accepts every desire and remembers it.
type recordingPrewarmer struct {
	requests []adapter.PrepareRequest
}

func (p *recordingPrewarmer) Prepare(_ context.Context, request adapter.PrepareRequest) (adapter.PrepareReceipt, error) {
	p.requests = append(p.requests, request)
	return adapter.PrepareReceipt{OperationKey: request.OperationKey}, nil
}

// memoryPreparationClock is the durable clock with nothing durable about it,
// which is all a case that states no interval needs.
type memoryPreparationClock struct {
	began time.Time
	ever  bool
}

func (clock *memoryPreparationClock) LastBegan(context.Context) (time.Time, bool, error) {
	return clock.began, clock.ever, nil
}

func (clock *memoryPreparationClock) RecordBegan(_ context.Context, at time.Time) error {
	clock.began, clock.ever = at, true
	return nil
}
