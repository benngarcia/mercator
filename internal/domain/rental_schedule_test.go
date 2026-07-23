package domain

import (
	"testing"
	"time"
)

func TestRentalScheduleQueuesCompatibleRunsInReservationOrder(t *testing.T) {
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	schedule := NewRentalSchedule("rental-warm")

	withActive, active, err := schedule.Reserve(BookingRequest{
		BookingID:              "booking-active",
		RunID:                  "run-active",
		ExpectedRuntimeSeconds: 600,
		MaxRuntimeSeconds:      900,
		ReservedAt:             now,
	})
	if err != nil {
		t.Fatalf("reserve active Booking: %v", err)
	}
	withFirstQueued, firstQueued, err := withActive.Reserve(BookingRequest{
		BookingID:              "booking-first",
		RunID:                  "run-first",
		ExpectedRuntimeSeconds: 120,
		MaxRuntimeSeconds:      300,
		ReservedAt:             now,
	})
	if err != nil {
		t.Fatalf("reserve first queued Booking: %v", err)
	}
	result, secondQueued, err := withFirstQueued.Reserve(BookingRequest{
		BookingID:              "booking-second",
		RunID:                  "run-second",
		ExpectedRuntimeSeconds: 60,
		MaxRuntimeSeconds:      180,
		ReservedAt:             now,
	})
	if err != nil {
		t.Fatalf("reserve second queued Booking: %v", err)
	}

	if active.State != BookingStateRunning || active.RunID != "run-active" || active.ScheduleVersion != 1 {
		t.Fatalf("active Booking = %+v", active)
	}
	assertQueuedBooking(t, firstQueued, "run-first", "booking-active", now.Add(10*time.Minute), now.Add(15*time.Minute), 2)
	assertQueuedBooking(t, secondQueued, "run-second", "booking-first", now.Add(12*time.Minute), now.Add(20*time.Minute), 3)
	if result.Version != 3 || len(result.Bookings) != 3 {
		t.Fatalf("Rental Schedule = %+v", result)
	}
}

func TestRentalScheduleDispatchesAndReprojectsAfterActiveBookingCompletes(t *testing.T) {
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	schedule := reservedSchedule(t, now)

	result, dispatched, err := schedule.Complete("booking-active", now.Add(9*time.Minute))
	if err != nil {
		t.Fatalf("complete active Booking: %v", err)
	}

	if dispatched == nil || dispatched.ID != "booking-first" || dispatched.State != BookingStateRunning || dispatched.ScheduleVersion != 4 {
		t.Fatalf("dispatched Booking = %+v", dispatched)
	}
	if dispatched.AfterBookingID != "" || dispatched.ProjectedStartAt != nil || dispatched.LatestStartAt != nil {
		t.Fatalf("dispatched Booking retained queue position: %+v", dispatched)
	}
	if result.Version != 4 || len(result.Bookings) != 2 {
		t.Fatalf("Rental Schedule = %+v", result)
	}
	second := result.Bookings[1].Booking
	assertQueuedBooking(t, second, "run-second", "booking-first", now.Add(11*time.Minute), now.Add(14*time.Minute), 4)
}

func reservedSchedule(t *testing.T, now time.Time) RentalSchedule {
	t.Helper()
	schedule := NewRentalSchedule("rental-warm")
	requests := []BookingRequest{
		{BookingID: "booking-active", RunID: "run-active", ExpectedRuntimeSeconds: 600, MaxRuntimeSeconds: 900, ReservedAt: now},
		{BookingID: "booking-first", RunID: "run-first", ExpectedRuntimeSeconds: 120, MaxRuntimeSeconds: 300, ReservedAt: now},
		{BookingID: "booking-second", RunID: "run-second", ExpectedRuntimeSeconds: 60, MaxRuntimeSeconds: 180, ReservedAt: now},
	}
	for _, request := range requests {
		var err error
		schedule, _, err = schedule.Reserve(request)
		if err != nil {
			t.Fatalf("reserve %s: %v", request.BookingID, err)
		}
	}
	return schedule
}

func assertQueuedBooking(t *testing.T, booking Booking, runID, afterID string, projected, latest time.Time, version uint64) {
	t.Helper()
	if booking.State != BookingStateQueued || booking.RunID != runID || booking.AfterBookingID != afterID || booking.ScheduleVersion != version {
		t.Fatalf("queued Booking identity = %+v", booking)
	}
	if booking.ProjectedStartAt == nil || !booking.ProjectedStartAt.Equal(projected) {
		t.Fatalf("projected start = %v, want %v", booking.ProjectedStartAt, projected)
	}
	if booking.LatestStartAt == nil || !booking.LatestStartAt.Equal(latest) {
		t.Fatalf("latest start = %v, want %v", booking.LatestStartAt, latest)
	}
}
