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

func TestOrchestratorQueuesSecondRunWithoutLaunchingIt(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	warm := orchOffer("rental-warm", now)
	warm.Pricing.RatePerSecondUSD = 0.00001
	fresh := orchProvisionableOffer("offer-fresh", now)
	fresh.Pricing.RatePerSecondUSD = 0.001
	fresh.Provisioning = &domain.Estimate{Expected: 120, P90: 150}
	provider := fake.New(fake.WithOffers([]domain.OfferSnapshot{fresh, warm}), fake.WithNow(func() time.Time { return now }))
	orch := New(openOrchestratorLog(t), scheduler.New(), provider, WithClock(func() time.Time { return now }))

	createScheduledRun(t, ctx, orch, "run-active")
	if err := orch.AdvanceRun(ctx, "ws_1", "run-active"); err != nil {
		t.Fatalf("advance active Run: %v", err)
	}
	createScheduledRun(t, ctx, orch, "run-queued")
	if err := orch.AdvanceRun(ctx, "ws_1", "run-queued"); err != nil {
		t.Fatalf("advance queued Run: %v", err)
	}

	events, err := orch.GetRunEvents(ctx, "ws_1", "run-queued")
	if err != nil {
		t.Fatalf("get queued Run events: %v", err)
	}
	decision := bookingDecisionFromEvents(t, events)
	if decision.Booking == nil || decision.Booking.State != domain.BookingStateQueued || decision.Booking.RunID != "run-queued" || decision.Booking.RentalID != "rental-warm" {
		t.Fatalf("queued Booking Decision = %+v", decision)
	}
	if countEvents(events, EventAttemptCreated) != 0 || countEvents(events, EventLaunchIntentRecorded) != 0 {
		t.Fatalf("queued Run launched before dispatch: %v", eventTypes(events))
	}
}

func TestOrchestratorDispatchesQueuedRunWhenActiveBookingCompletes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	warm := orchOffer("rental-warm", now)
	provider := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{warm}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
		fake.WithOpenObservations(1),
		fake.WithNow(func() time.Time { return now }),
	)
	orch := New(openOrchestratorLog(t), scheduler.New(), provider, WithClock(func() time.Time { return now }))

	createScheduledRun(t, ctx, orch, "run-active")
	if err := orch.AdvanceRun(ctx, "ws_1", "run-active"); err != nil {
		t.Fatalf("start active Run: %v", err)
	}
	createScheduledRun(t, ctx, orch, "run-queued")
	if err := orch.AdvanceRun(ctx, "ws_1", "run-queued"); err != nil {
		t.Fatalf("queue second Run: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run-active"); err != nil {
		t.Fatalf("complete active Run: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run-queued"); err != nil {
		t.Fatalf("dispatch queued Run: %v", err)
	}

	events, err := orch.GetRunEvents(ctx, "ws_1", "run-queued")
	if err != nil {
		t.Fatalf("get dispatched Run events: %v", err)
	}
	if countEvents(events, EventBookingDispatched) != 1 || countEvents(events, EventAttemptCreated) != 1 || countEvents(events, EventLaunchIntentRecorded) != 1 {
		t.Fatalf("dispatched Run events = %v", eventTypes(events))
	}
}

func TestOrchestratorReleasesQueuedBookingWhenItsRunIsCancelled(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	warm := orchOffer("rental-warm", now)
	provider := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{warm}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
		fake.WithOpenObservations(1),
		fake.WithNow(func() time.Time { return now }),
	)
	orch := New(openOrchestratorLog(t), scheduler.New(), provider, WithClock(func() time.Time { return now }))

	createScheduledRun(t, ctx, orch, "run-active")
	if err := orch.AdvanceRun(ctx, "ws_1", "run-active"); err != nil {
		t.Fatalf("start active Run: %v", err)
	}
	createScheduledRun(t, ctx, orch, "run-queued")
	if err := orch.AdvanceRun(ctx, "ws_1", "run-queued"); err != nil {
		t.Fatalf("queue second Run: %v", err)
	}

	if _, err := orch.CancelRun(ctx, "ws_1", "run-queued", nil); err != nil {
		t.Fatalf("cancel queued Run: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run-queued"); err != nil {
		t.Fatalf("close cancelled Run: %v", err)
	}

	schedules, err := orch.schedules.List(ctx, "ws_1")
	if err != nil {
		t.Fatalf("list Rental Schedules: %v", err)
	}
	for _, scheduled := range schedules["rental-warm"].Bookings {
		if scheduled.Booking.RunID == "run-queued" {
			t.Fatalf("cancelled Run still booked on Rental: %+v", schedules["rental-warm"])
		}
	}

	if err := orch.AdvanceRun(ctx, "ws_1", "run-active"); err != nil {
		t.Fatalf("complete active Run: %v", err)
	}
	createScheduledRun(t, ctx, orch, "run-next")
	if err := orch.AdvanceRun(ctx, "ws_1", "run-next"); err != nil {
		t.Fatalf("place next Run: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run-next"); err != nil {
		t.Fatalf("dispatch next Run: %v", err)
	}
	events, err := orch.GetRunEvents(ctx, "ws_1", "run-next")
	if err != nil {
		t.Fatalf("get next Run events: %v", err)
	}
	if countEvents(events, EventAttemptCreated) != 1 {
		t.Fatalf("next Run never launched after cancelled Booking should have been released: %v", eventTypes(events))
	}
}

func createScheduledRun(t *testing.T, ctx context.Context, orch *Orchestrator, runID string) {
	t.Helper()
	revision := orchRevision()
	revision.ID = "wrev_" + runID
	revision.WorkloadID = "wrk_" + runID
	revision.Digest = "sha256:" + runID
	if _, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID: "ws_1", RunID: runID, IdempotencyKey: "create:" + runID, Workload: revision,
	}); err != nil {
		t.Fatalf("create %s: %v", runID, err)
	}
}

func bookingDecisionFromEvents(t *testing.T, events []eventlog.StoredEvent) domain.BookingDecision {
	t.Helper()
	for _, event := range events {
		if event.Type != EventBookingDecided {
			continue
		}
		var data bookingDecisionData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode Booking Decision: %v", err)
		}
		return data.Decision
	}
	t.Fatal("Booking Decision event not found")
	return domain.BookingDecision{}
}
