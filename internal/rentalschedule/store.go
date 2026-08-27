package rentalschedule

import (
	"context"
	"fmt"
	"sync"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

type Store interface {
	List(ctx context.Context) (map[string]domain.RentalSchedule, error)
	Commit(ctx context.Context, event eventlog.AppendRequest, expectedVersion uint64, next domain.RentalSchedule) (eventlog.AppendResult, error)
}

type Memory struct {
	mu        sync.Mutex
	log       eventlog.EventLog
	schedules map[string]domain.RentalSchedule
	commands  map[string]eventlog.AppendResult
}

func NewMemory(log eventlog.EventLog) *Memory {
	return &Memory{
		log:       log,
		schedules: map[string]domain.RentalSchedule{},
		commands:  map[string]eventlog.AppendResult{},
	}
}

func (store *Memory) List(_ context.Context) (map[string]domain.RentalSchedule, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := map[string]domain.RentalSchedule{}
	for rentalID, schedule := range store.schedules {
		result[rentalID] = cloneSchedule(schedule)
	}
	return result, nil
}

func (store *Memory) Commit(ctx context.Context, event eventlog.AppendRequest, expectedVersion uint64, next domain.RentalSchedule) (eventlog.AppendResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := validCommit(event, expectedVersion, next); err != nil {
		return eventlog.AppendResult{}, err
	}
	if result, ok := store.commands[event.CommandKey]; ok {
		result.Duplicate = true
		return result, nil
	}
	current := store.schedules[next.RentalID]
	if current.Version != expectedVersion {
		return eventlog.AppendResult{}, eventlog.ErrConcurrencyConflict
	}
	result, err := store.log.Append(ctx, event)
	if err != nil {
		return eventlog.AppendResult{}, err
	}
	store.schedules[next.RentalID] = cloneSchedule(next)
	store.commands[event.CommandKey] = result
	return result, nil
}

func validCommit(event eventlog.AppendRequest, expectedVersion uint64, next domain.RentalSchedule) error {
	if next.RentalID == "" {
		return fmt.Errorf("Rental Schedule commit requires Rental identity")
	}
	if next.Version != expectedVersion+1 {
		return fmt.Errorf("Rental Schedule version %d does not follow %d", next.Version, expectedVersion)
	}
	return nil
}

func cloneSchedule(schedule domain.RentalSchedule) domain.RentalSchedule {
	cloned := schedule
	cloned.Bookings = append([]domain.ScheduledBooking(nil), schedule.Bookings...)
	for index := range cloned.Bookings {
		booking := cloned.Bookings[index].Booking
		if booking.ProjectedStartAt != nil {
			projected := *booking.ProjectedStartAt
			booking.ProjectedStartAt = &projected
		}
		if booking.LatestStartAt != nil {
			latest := *booking.LatestStartAt
			booking.LatestStartAt = &latest
		}
		cloned.Bookings[index].Booking = booking
	}
	return cloned
}
