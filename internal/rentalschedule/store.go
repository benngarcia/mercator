package rentalschedule

import (
	"context"
	"fmt"
	"sync"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

type Store interface {
	List(ctx context.Context, workspaceID string) (map[string]domain.RentalSchedule, error)
	Commit(
		ctx context.Context,
		event eventlog.AppendRequest,
		expectedVersion uint64,
		next domain.RentalSchedule,
		run domain.RunRecord,
	) (eventlog.AppendResult, error)
}

type Memory struct {
	mu        sync.Mutex
	log       eventlog.WorkspaceEventLog
	schedules map[string]map[string]domain.RentalSchedule
	commands  map[string]eventlog.AppendResult
}

func NewMemory(log eventlog.WorkspaceEventLog) *Memory {
	return &Memory{
		log:       log,
		schedules: map[string]map[string]domain.RentalSchedule{},
		commands:  map[string]eventlog.AppendResult{},
	}
}

// Seed installs one Rental's whole schedule without the event a commit records.
// Only a fixture owns Bookings with no history behind them: production reaches
// every schedule through Commit, and the Bookings a simulated world starts with
// belong to Runs no event log ever saw. It writes the schedule whole rather than
// merging, so a caller changing seeded state reads the current schedule first
// and hands back what it should now be.
func (store *Memory) Seed(workspaceID string, schedule domain.RentalSchedule) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := validSeed(workspaceID, schedule); err != nil {
		return err
	}
	if store.schedules[workspaceID] == nil {
		store.schedules[workspaceID] = map[string]domain.RentalSchedule{}
	}
	store.schedules[workspaceID][schedule.RentalID] = cloneSchedule(schedule)
	return nil
}

// validSeed refuses a schedule Mercator could not have reached. A version counts
// the transitions this schedule has seen, and every Booking still on it took one
// to get there, so a version below the number of occupants is a history that did
// not happen. It matters beyond bookkeeping: the next Booking is minted at one
// past the version, so a schedule holding two Bookings at version one hands the
// arriving Run the version a Booking already on it consumed, and the store would
// then hold two Bookings created at one transition.
func validSeed(workspaceID string, schedule domain.RentalSchedule) error {
	if workspaceID == "" || schedule.RentalID == "" {
		return fmt.Errorf("Rental Schedule seed requires Workspace and Rental identity")
	}
	if schedule.Version < uint64(len(schedule.Bookings)) {
		return fmt.Errorf(
			"Rental Schedule seed for Rental %q holds %d Bookings at version %d, and each of them took a transition to get there",
			schedule.RentalID, len(schedule.Bookings), schedule.Version,
		)
	}
	return nil
}

func (store *Memory) List(_ context.Context, workspaceID string) (map[string]domain.RentalSchedule, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := map[string]domain.RentalSchedule{}
	for rentalID, schedule := range store.schedules[workspaceID] {
		result[rentalID] = cloneSchedule(schedule)
	}
	return result, nil
}

func (store *Memory) Commit(
	ctx context.Context,
	event eventlog.AppendRequest,
	expectedVersion uint64,
	next domain.RentalSchedule,
	_ domain.RunRecord,
) (eventlog.AppendResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := validCommit(event, expectedVersion, next); err != nil {
		return eventlog.AppendResult{}, err
	}
	commandID := event.Stream.WorkspaceID + ":" + event.CommandKey
	if result, ok := store.commands[commandID]; ok {
		result.Duplicate = true
		return result, nil
	}
	current := store.schedules[event.Stream.WorkspaceID][next.RentalID]
	if current.Version != expectedVersion {
		return eventlog.AppendResult{}, eventlog.ErrConcurrencyConflict
	}
	result, err := store.log.AppendIfWorkspaceActive(ctx, event)
	if err != nil {
		return eventlog.AppendResult{}, err
	}
	if store.schedules[event.Stream.WorkspaceID] == nil {
		store.schedules[event.Stream.WorkspaceID] = map[string]domain.RentalSchedule{}
	}
	store.schedules[event.Stream.WorkspaceID][next.RentalID] = cloneSchedule(next)
	store.commands[commandID] = result
	return result, nil
}

func validCommit(event eventlog.AppendRequest, expectedVersion uint64, next domain.RentalSchedule) error {
	if event.Stream.WorkspaceID == "" || next.RentalID == "" {
		return fmt.Errorf("Rental Schedule commit requires Workspace and Rental identity")
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
