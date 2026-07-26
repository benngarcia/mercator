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

// TestRentalScheduleProjectsItsWaitFromWhereItsBookingsAre is why the wait is
// asked as of a moment. Summing what every caller declared reported the same
// half hour of waiting for the whole half hour, so a Rental a minute from free
// looked exactly as busy as one that had just started, and a Run that refused to
// wait three minutes was told there was no capacity for it.
func TestRentalScheduleProjectsItsWaitFromWhereItsBookingsAre(t *testing.T) {
	reserved := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	schedule, _, err := NewRentalSchedule("rental-warm").Reserve(BookingRequest{
		BookingID:              "booking-long",
		RunID:                  "run-long",
		ExpectedRuntimeSeconds: 1800,
		MaxRuntimeSeconds:      3600,
		ReservedAt:             reserved,
	})
	if err != nil {
		t.Fatalf("reserve the running Booking: %v", err)
	}

	for _, moment := range []struct {
		elapsed time.Duration
		wait    float64
	}{
		{elapsed: 0, wait: 1800},
		{elapsed: 29 * time.Minute, wait: 60},
		{elapsed: 45 * time.Minute, wait: 0},
	} {
		if wait := schedule.ExpectedWaitSeconds(reserved.Add(moment.elapsed)); wait != moment.wait {
			t.Errorf("%v into a half-hour Booking the wait is %v seconds, want %v", moment.elapsed, wait, moment.wait)
		}
	}
}

// TestABookingWithNoRecordedStartOwesItsWholeRuntime is the schedule Mercator
// persisted before it kept the moment a Booking took its Rental. A zero start is
// a schedule saying it cannot tell how much has elapsed, and the only safe
// reading of that is that none of it has: assuming otherwise would report a
// Rental free while the Booking on it is still going.
func TestABookingWithNoRecordedStartOwesItsWholeRuntime(t *testing.T) {
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	scheduled := ScheduledBooking{
		Booking:                Booking{ID: "booking-legacy", State: BookingStateRunning},
		ExpectedRuntimeSeconds: 1800,
		MaxRuntimeSeconds:      3600,
	}

	if remaining := scheduled.RemainingExpectedSeconds(now); remaining != 1800 {
		t.Errorf("a Booking with no recorded start has %v expected seconds left, want its whole runtime", remaining)
	}
	if remaining := scheduled.RemainingMaxSeconds(now); remaining != 3600 {
		t.Errorf("a Booking with no recorded start has %v enforced seconds left, want its whole bound", remaining)
	}
}

// TestAScheduleWhoseBookingIsPastItsBoundPromisesNothing is the difference
// between a queue that was measured and one whose arithmetic ran out. Both
// remaining runtimes bottom out at zero, so a Rental held by a Booking past the
// runtime Mercator enforces reports the same wait an idle Rental reports, and a
// Booking placed behind it would be handed a latest acceptable start of the
// moment it was made. The schedule refuses instead, and the evidence it publishes
// says how far past the bound the Booking holding the Rental has gone.
func TestAScheduleWhoseBookingIsPastItsBoundPromisesNothing(t *testing.T) {
	reserved := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	schedule, _, err := NewRentalSchedule("rental-warm").Reserve(BookingRequest{
		BookingID:              "booking-active",
		RunID:                  "run-active",
		ExpectedRuntimeSeconds: 2700,
		MaxRuntimeSeconds:      2700,
		ReservedAt:             reserved,
	})
	if err != nil {
		t.Fatalf("reserve the running Booking: %v", err)
	}
	overrun := reserved.Add(50 * time.Minute)

	if !schedule.Exhausted(overrun) {
		t.Fatalf("a Booking five minutes past its enforced 45 is a schedule that can still say when its Rental comes free")
	}
	if schedule.Exhausted(reserved.Add(44 * time.Minute)) {
		t.Fatalf("a Booking inside its enforced bound exhausts nothing")
	}
	evidence := schedule.Evidence(overrun)
	if evidence.Running.OverrunSeconds != 300 {
		t.Errorf("the evidence records %v seconds of overrun, want the 300 the Booking is past its bound", evidence.Running.OverrunSeconds)
	}
	_, _, err = schedule.Reserve(BookingRequest{
		BookingID:              "booking-arriving",
		RunID:                  "run-arriving",
		ExpectedRuntimeSeconds: 1200,
		MaxRuntimeSeconds:      3600,
		ReservedAt:             overrun,
	})
	if err == nil {
		t.Fatalf("the schedule promised a start behind a Booking it cannot project, at the moment of the reservation itself")
	}
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
