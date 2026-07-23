package rentalschedule

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

func TestMemoryStoreCommitsScheduleWithRunEvent(t *testing.T) {
	log, err := eventlog.OpenSQLite(t.Context(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	store := NewMemory(activeLog{EventLog: log})
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	next, _, err := domain.NewRentalSchedule("rental-warm").Reserve(domain.BookingRequest{
		BookingID: "booking-1", RunID: "run-1", ExpectedRuntimeSeconds: 60, MaxRuntimeSeconds: 120, ReservedAt: now,
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}

	_, err = store.Commit(t.Context(), appendRequest(now), 0, next)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	schedules, err := store.List(t.Context(), "ws-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if schedules["rental-warm"].Version != 1 || schedules["rental-warm"].Bookings[0].Booking.RunID != "run-1" {
		t.Fatalf("stored schedules = %+v", schedules)
	}
	events, err := log.ReadStream(t.Context(), eventlog.StreamKey{WorkspaceID: "ws-1", Type: "run", ID: "run-1"}, 0, 10)
	if err != nil || len(events) != 1 || events[0].Type != "compute.run.booking_decided.v1" {
		t.Fatalf("stored events = %+v, %v", events, err)
	}

	stale := appendRequest(now.Add(time.Second))
	stale.CommandKey = "run-1:place-stale"
	stale.RequestHash = "sha256:stale"
	stale.Events[0].ID = "evt-ws-1-run-1-booking-stale"
	_, err = store.Commit(t.Context(), stale, 0, next)
	if !errors.Is(err, eventlog.ErrConcurrencyConflict) {
		t.Fatalf("stale commit error = %v", err)
	}
}

type activeLog struct{ eventlog.EventLog }

func (l activeLog) AppendIfWorkspaceActive(ctx context.Context, request eventlog.AppendRequest) (eventlog.AppendResult, error) {
	return l.Append(ctx, request)
}

func appendRequest(now time.Time) eventlog.AppendRequest {
	data, _ := json.Marshal(map[string]string{"decision": "fixture"})
	return eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: "ws-1", Type: "run", ID: "run-1"},
		ExpectedStreamVersion: 0,
		CommandKey:            "run-1:place",
		RequestHash:           "sha256:fixture",
		CorrelationID:         "run-1",
		Events: []eventlog.NewEvent{{
			ID: "evt-ws-1-run-1-booking", Type: "compute.run.booking_decided.v1", SchemaVersion: 1, OccurredAt: now, Data: data,
		}},
	}
}
