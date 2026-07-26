package domain

import (
	"fmt"
	"time"
)

const RentalScheduleQueueCapacity = 4

type BookingRequest struct {
	BookingID              string
	RunID                  string
	ExpectedRuntimeSeconds float64
	MaxRuntimeSeconds      float64
	ReservedAt             time.Time
}

type ScheduledBooking struct {
	Booking                Booking `json:"booking"`
	ExpectedRuntimeSeconds float64 `json:"expected_runtime_seconds"`
	MaxRuntimeSeconds      float64 `json:"max_runtime_seconds"`
	// StartedAt is when this Booking took the Rental, which is what makes the
	// runtimes above projectable rather than merely declared. Without it the
	// schedule reports an hour of waiting for a Booking one minute from its own
	// expected finish, and a Run that refuses to wait three minutes is told
	// there is no capacity for it. A queued Booking has not started and says so
	// with the zero time; so does a running one recorded before Mercator kept
	// this, and both then owe their whole declared runtime, because a schedule
	// that cannot say how much has elapsed must not assume any of it has.
	StartedAt time.Time `json:"started_at,omitzero"`
}

// RemainingExpectedSeconds is how much longer this Booking is expected to hold
// the Rental. Runtimes are what a caller declared, so this is a projection of
// somebody's estimate and never a measurement; what it is not is that estimate
// restated forever while the Booking runs.
func (scheduled ScheduledBooking) RemainingExpectedSeconds(now time.Time) float64 {
	return remainingSeconds(scheduled.ExpectedRuntimeSeconds, scheduled.StartedAt, now)
}

// RemainingMaxSeconds is the same projection against the runtime Mercator
// enforces, which is what a latest-start guarantee is made of.
func (scheduled ScheduledBooking) RemainingMaxSeconds(now time.Time) float64 {
	return remainingSeconds(scheduled.MaxRuntimeSeconds, scheduled.StartedAt, now)
}

// OverrunSeconds is how far past the runtime Mercator enforces this Booking has
// run. It exists because the projections above bottom out at zero, and zero read
// as a wait says a machine is a moment from free when what it says is that the
// arithmetic ran out: enforcement is a reconciliation rather than an instant, so
// a Booking whose node has gone quiet holds its Rental with nothing left to
// project from. A Booking inside its bound has overrun nothing.
func (scheduled ScheduledBooking) OverrunSeconds(now time.Time) float64 {
	if scheduled.StartedAt.IsZero() {
		return 0
	}
	return max(0, now.Sub(scheduled.StartedAt).Seconds()-scheduled.MaxRuntimeSeconds)
}

func remainingSeconds(declared float64, startedAt, now time.Time) float64 {
	if startedAt.IsZero() {
		return declared
	}
	return max(0, declared-now.Sub(startedAt).Seconds())
}

type RentalSchedule struct {
	RentalID string             `json:"rental_id"`
	Version  uint64             `json:"version"`
	Bookings []ScheduledBooking `json:"bookings"`
}

func NewRentalSchedule(rentalID string) RentalSchedule {
	return RentalSchedule{RentalID: rentalID, Bookings: []ScheduledBooking{}}
}

// ExpectedWaitSeconds is how long work arriving at this moment waits for the
// Rental, projected from where its Bookings actually are. It is asked as of a
// moment because that is what makes it a projection: a schedule that summed
// declared runtimes reported the same wait for an hour, so a machine a minute
// from finishing looked as busy as one that had just started.
func (schedule RentalSchedule) ExpectedWaitSeconds(now time.Time) float64 {
	var seconds float64
	for _, scheduled := range schedule.Bookings {
		seconds += scheduled.RemainingExpectedSeconds(now)
	}
	return seconds
}

// Exhausted reports whether this schedule can still say when its Rental comes
// free. The Booking holding the Rental is past the runtime Mercator enforces, so
// every projection from here reads zero, and zero is the same number a schedule
// with nothing on it reports: a machine still occupied looks idle, the wait it
// prices is nothing, and a Booking placed behind it gets a latest start already
// at its deadline. Only the Booking at the head has taken the Rental, so it is
// the only one that can be past anything.
func (schedule RentalSchedule) Exhausted(now time.Time) bool {
	return len(schedule.Bookings) > 0 && schedule.Bookings[0].RemainingMaxSeconds(now) <= 0
}

// Evidence is this schedule as a decision reads it at now: which version
// answered, what holds the Rental, what is already waiting, and the wait that
// projects from them. It is built here rather than by the caller because the
// remaining runtimes and the projection are this type's own arithmetic, and a
// record derived a second way would be a second opinion about one queue.
func (schedule RentalSchedule) Evidence(now time.Time) ScheduleEvidence {
	evidence := ScheduleEvidence{
		Version:               schedule.Version,
		ProjectedStartSeconds: schedule.ExpectedWaitSeconds(now),
	}
	for index, scheduled := range schedule.Bookings {
		if index == 0 {
			evidence.Running = &RunningBookingEvidence{
				BookingID:                       scheduled.Booking.ID,
				RunID:                           scheduled.Booking.RunID,
				RemainingMaxRuntimeSeconds:      scheduled.RemainingMaxSeconds(now),
				RemainingExpectedRuntimeSeconds: scheduled.RemainingExpectedSeconds(now),
				OverrunSeconds:                  scheduled.OverrunSeconds(now),
			}
			continue
		}
		evidence.Preceding = append(evidence.Preceding, WaitingBookingEvidence{
			BookingID:              scheduled.Booking.ID,
			RunID:                  scheduled.Booking.RunID,
			MaxRuntimeSeconds:      scheduled.RemainingMaxSeconds(now),
			ExpectedRuntimeSeconds: scheduled.RemainingExpectedSeconds(now),
		})
	}
	return evidence
}

func (schedule RentalSchedule) Reserve(request BookingRequest) (RentalSchedule, Booking, error) {
	if err := validBookingRequest(schedule, request); err != nil {
		return RentalSchedule{}, Booking{}, err
	}
	booking := schedule.bookingFor(request)
	next := RentalSchedule{
		RentalID: schedule.RentalID,
		Version:  booking.ScheduleVersion,
		Bookings: append([]ScheduledBooking(nil), schedule.Bookings...),
	}
	next.Bookings = append(next.Bookings, ScheduledBooking{
		Booking:                booking,
		ExpectedRuntimeSeconds: request.ExpectedRuntimeSeconds,
		MaxRuntimeSeconds:      request.MaxRuntimeSeconds,
		StartedAt:              tookTheRentalAt(booking, request.ReservedAt),
	})
	return next, booking, nil
}

// tookTheRentalAt is when this Booking began occupying the Rental: the moment it
// was reserved for one that goes straight to running, and nothing at all for one
// that waits, because a queued Booking starts when the Booking ahead of it
// finishes and that moment is recorded when it arrives.
func tookTheRentalAt(booking Booking, reservedAt time.Time) time.Time {
	if booking.State != BookingStateRunning {
		return time.Time{}
	}
	return reservedAt
}

func (schedule RentalSchedule) Complete(bookingID string, completedAt time.Time) (RentalSchedule, *Booking, error) {
	if bookingID == "" || completedAt.IsZero() {
		return RentalSchedule{}, nil, fmt.Errorf("Rental Schedule completion requires Booking identity and time")
	}
	index := schedule.bookingIndex(bookingID)
	if index < 0 {
		return RentalSchedule{}, nil, fmt.Errorf("Rental Schedule does not contain Booking %q", bookingID)
	}
	remaining := append([]ScheduledBooking(nil), schedule.Bookings[:index]...)
	remaining = append(remaining, schedule.Bookings[index+1:]...)
	next := RentalSchedule{
		RentalID: schedule.RentalID,
		Version:  schedule.Version + 1,
		Bookings: remaining,
	}.reproject(completedAt)
	if index != 0 || len(next.Bookings) == 0 {
		return next, nil, nil
	}
	dispatched := next.Bookings[0].Booking
	return next, &dispatched, nil
}

func (schedule RentalSchedule) bookingIndex(bookingID string) int {
	for index, scheduled := range schedule.Bookings {
		if scheduled.Booking.ID == bookingID {
			return index
		}
	}
	return -1
}

func (schedule RentalSchedule) reproject(now time.Time) RentalSchedule {
	projected := now
	latest := now
	for index := range schedule.Bookings {
		booking := schedule.Bookings[index].Booking
		booking.ScheduleVersion = schedule.Version
		if index == 0 {
			booking.State = BookingStateRunning
			booking.AfterBookingID = ""
			booking.ProjectedStartAt = nil
			booking.LatestStartAt = nil
			if schedule.Bookings[index].StartedAt.IsZero() {
				// The Booking ahead of this one just finished, so this is the
				// moment this one took the Rental. Recording it is what lets
				// every later projection ask how much of its runtime is left.
				schedule.Bookings[index].StartedAt = now
			}
		} else {
			booking.State = BookingStateQueued
			booking.AfterBookingID = schedule.Bookings[index-1].Booking.ID
			projectedStart := projected
			latestStart := latest
			booking.ProjectedStartAt = &projectedStart
			booking.LatestStartAt = &latestStart
		}
		schedule.Bookings[index].Booking = booking
		projected = projected.Add(seconds(schedule.Bookings[index].RemainingExpectedSeconds(now)))
		latest = latest.Add(seconds(schedule.Bookings[index].RemainingMaxSeconds(now)))
	}
	return schedule
}

func seconds(count float64) time.Duration {
	return time.Duration(count * float64(time.Second))
}

func validBookingRequest(schedule RentalSchedule, request BookingRequest) error {
	if schedule.RentalID == "" || request.BookingID == "" || request.RunID == "" {
		return fmt.Errorf("Rental Schedule requires Rental, Booking, and Run identity")
	}
	if request.ReservedAt.IsZero() || request.ExpectedRuntimeSeconds <= 0 || request.MaxRuntimeSeconds <= 0 {
		return fmt.Errorf("Rental Schedule requires reservation time and positive runtime bounds")
	}
	if request.ExpectedRuntimeSeconds > request.MaxRuntimeSeconds {
		return fmt.Errorf("Rental Schedule expected runtime exceeds enforced maximum")
	}
	if len(schedule.Bookings) >= RentalScheduleQueueCapacity+1 {
		return fmt.Errorf("Rental Schedule queue capacity is %d", RentalScheduleQueueCapacity)
	}
	// Nothing may be promised a start behind a Booking that is past the runtime
	// Mercator enforces, because the start bounds this schedule would compute for
	// it are the moment of the reservation itself: a guarantee handed out already
	// at its deadline. Refusing here rather than trusting the caller is what keeps
	// the record honest whatever selected this Rental.
	if schedule.Exhausted(request.ReservedAt) {
		return fmt.Errorf(
			"Rental Schedule cannot promise a start behind Booking %q, which is %.0fs past the runtime Mercator enforces",
			schedule.Bookings[0].Booking.ID, schedule.Bookings[0].OverrunSeconds(request.ReservedAt),
		)
	}
	for _, scheduled := range schedule.Bookings {
		if scheduled.Booking.ID == request.BookingID || scheduled.Booking.RunID == request.RunID {
			return fmt.Errorf("Rental Schedule already contains Booking or Run")
		}
	}
	return nil
}

func (schedule RentalSchedule) bookingFor(request BookingRequest) Booking {
	booking := Booking{
		ID:              request.BookingID,
		RunID:           request.RunID,
		RentalID:        schedule.RentalID,
		State:           BookingStateRunning,
		ScheduleVersion: schedule.Version + 1,
	}
	if len(schedule.Bookings) == 0 {
		return booking
	}
	projected, latest := schedule.startBounds(request.ReservedAt)
	booking.State = BookingStateQueued
	booking.AfterBookingID = schedule.Bookings[len(schedule.Bookings)-1].Booking.ID
	booking.ProjectedStartAt = &projected
	booking.LatestStartAt = &latest
	return booking
}

// startBounds is when a Booking arriving now is projected to start and the
// latest it may. Both are read from what the Bookings ahead of it have left
// rather than from what they declared, so a Rental most of the way through its
// queue promises a start most of the way through it too.
func (schedule RentalSchedule) startBounds(now time.Time) (time.Time, time.Time) {
	projected := now
	latest := now
	for _, scheduled := range schedule.Bookings {
		projected = projected.Add(seconds(scheduled.RemainingExpectedSeconds(now)))
		latest = latest.Add(seconds(scheduled.RemainingMaxSeconds(now)))
	}
	return projected, latest
}
