package scenario

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

func FuzzScenarioSessionCommandsPreserveEventInvariants(f *testing.F) {
	base, err := Load("scenarios/no-rentals-provisions-fresh.json")
	if err != nil {
		f.Fatalf("load fuzz world: %v", err)
	}
	f.Add([]byte{0})
	f.Add([]byte{0, 1, 2, 0, 2, 1})
	f.Add([]byte{2, 2, 1, 0, 1, 2, 0, 2})

	f.Fuzz(func(t *testing.T, commands []byte) {
		if len(commands) == 0 {
			return
		}
		if len(commands) > 24 {
			commands = commands[:24]
		}
		session, err := (SimBackend{}).StartWorld(base.World)
		if err != nil {
			t.Fatalf("start generated world: %v", err)
		}
		defer session.Close()
		var names []string
		for _, command := range commands {
			switch command % 3 {
			case 0:
				names = submitGeneratedRun(t, session, names, *base.Request)
			case 1:
				session.AdvanceClock(time.Duration(command/3+1) * time.Second)
			case 2:
				if len(names) == 0 {
					names = submitGeneratedRun(t, session, names, *base.Request)
				} else if err := session.Reconcile(names[int(command)%len(names)]); err != nil {
					t.Fatalf("reconcile generated Run: %v", err)
				}
			}
			assertGeneratedEventInvariants(t, session, names)
		}
	})
}

func submitGeneratedRun(t *testing.T, session Session, names []string, request RequestSpec) []string {
	t.Helper()
	name := fmt.Sprintf("fuzz-%d", len(names))
	if err := session.Submit(name, request); err != nil {
		t.Fatalf("submit generated Run %q: %v", name, err)
	}
	return append(names, name)
}

func assertGeneratedEventInvariants(t *testing.T, session Session, names []string) {
	t.Helper()
	eventOwners := map[string]string{}
	positionOwners := map[eventlog.GlobalPosition]string{}
	bookingOwners := map[string]string{}
	runningRentals := map[string]string{}
	queuedRentals := map[string]map[string]bool{}
	for _, name := range names {
		events, err := session.RunEvents(name)
		if err != nil {
			t.Fatalf("read generated Run %q: %v", name, err)
		}
		assertGeneratedRunStream(t, name, events, eventOwners, positionOwners)
		for _, event := range events {
			if event.Type != orchestrator.EventBookingDecided {
				continue
			}
			decision := decodeGeneratedDecision(t, name, event)
			if decision.Booking == nil {
				continue
			}
			booking := decision.Booking
			if owner, exists := bookingOwners[booking.ID]; exists && owner != name {
				t.Fatalf("Booking %q belongs to both %q and %q", booking.ID, owner, name)
			}
			bookingOwners[booking.ID] = name
			switch booking.State {
			case domain.BookingStateRunning:
				if owner, exists := runningRentals[booking.RentalID]; exists && owner != name {
					t.Fatalf("Rental %q runs both %q and %q", booking.RentalID, owner, name)
				}
				runningRentals[booking.RentalID] = name
			case domain.BookingStateQueued:
				bookings := queuedRentals[booking.RentalID]
				if bookings == nil {
					bookings = map[string]bool{}
					queuedRentals[booking.RentalID] = bookings
				}
				bookings[booking.ID] = true
				if len(bookings) > MaxQueuedBookings {
					t.Fatalf("Rental %q has %d queued Bookings", booking.RentalID, len(bookings))
				}
			default:
				t.Fatalf("Booking %q has invalid state %q", booking.ID, booking.State)
			}
		}
	}
}

func assertGeneratedRunStream(
	t *testing.T,
	name string,
	events []eventlog.StoredEvent,
	eventOwners map[string]string,
	positionOwners map[eventlog.GlobalPosition]string,
) {
	t.Helper()
	requested := 0
	var previous eventlog.GlobalPosition
	for index, event := range events {
		if event.WorkspaceID != simWorkspace || event.StreamID != "run-"+name {
			t.Fatalf("Run %q event scope = %q/%q", name, event.WorkspaceID, event.StreamID)
		}
		if event.StreamVersion != uint64(index+1) {
			t.Fatalf("Run %q stream version = %d, want %d", name, event.StreamVersion, index+1)
		}
		if index > 0 && event.GlobalPosition <= previous {
			t.Fatalf("Run %q global positions are not increasing", name)
		}
		previous = event.GlobalPosition
		if owner, exists := eventOwners[event.ID]; exists && owner != name {
			t.Fatalf("event %q belongs to both %q and %q", event.ID, owner, name)
		}
		eventOwners[event.ID] = name
		if owner, exists := positionOwners[event.GlobalPosition]; exists && owner != name {
			t.Fatalf("global position %d belongs to both %q and %q", event.GlobalPosition, owner, name)
		}
		positionOwners[event.GlobalPosition] = name
		if event.Type == orchestrator.EventRunRequested {
			requested++
		}
	}
	if requested != 1 {
		t.Fatalf("Run %q has %d requested events, want one", name, requested)
	}
}

func decodeGeneratedDecision(t *testing.T, name string, event eventlog.StoredEvent) domain.BookingDecision {
	t.Helper()
	var payload struct {
		Decision domain.BookingDecision `json:"decision"`
	}
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		t.Fatalf("decode Run %q BookingDecision: %v", name, err)
	}
	if payload.Decision.ID == "" || payload.Decision.RunID != "run-"+name {
		t.Fatalf("Run %q records malformed BookingDecision %+v", name, payload.Decision)
	}
	return payload.Decision
}
