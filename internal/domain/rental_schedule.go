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
}

type RentalSchedule struct {
	RentalID string             `json:"rental_id"`
	Version  uint64             `json:"version"`
	Bookings []ScheduledBooking `json:"bookings"`
}

func NewRentalSchedule(rentalID string) RentalSchedule {
	return RentalSchedule{RentalID: rentalID, Bookings: []ScheduledBooking{}}
}

func (schedule RentalSchedule) ExpectedWaitSeconds() float64 {
	var seconds float64
	for _, scheduled := range schedule.Bookings {
		seconds += scheduled.ExpectedRuntimeSeconds
	}
	return seconds
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
	})
	return next, booking, nil
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
		} else {
			booking.State = BookingStateQueued
			booking.AfterBookingID = schedule.Bookings[index-1].Booking.ID
			projectedStart := projected
			latestStart := latest
			booking.ProjectedStartAt = &projectedStart
			booking.LatestStartAt = &latestStart
		}
		schedule.Bookings[index].Booking = booking
		projected = projected.Add(time.Duration(schedule.Bookings[index].ExpectedRuntimeSeconds * float64(time.Second)))
		latest = latest.Add(time.Duration(schedule.Bookings[index].MaxRuntimeSeconds * float64(time.Second)))
	}
	return schedule
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

func (schedule RentalSchedule) startBounds(now time.Time) (time.Time, time.Time) {
	projected := now
	latest := now
	for _, scheduled := range schedule.Bookings {
		projected = projected.Add(time.Duration(scheduled.ExpectedRuntimeSeconds * float64(time.Second)))
		latest = latest.Add(time.Duration(scheduled.MaxRuntimeSeconds * float64(time.Second)))
	}
	return projected, latest
}
