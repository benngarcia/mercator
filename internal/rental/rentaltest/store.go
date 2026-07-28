// Package rentaltest is the shared conformance suite every rental.Store must
// pass.
//
// The in-memory store and the SQLite store make the same promises, and the
// promises are what the capacity lifecycle is built on: a lease comes back with
// every generation it has been through, in order and unedited, and a write that
// does not follow the version the store holds is refused rather than applied on
// top of somebody else's. Running one suite against both is what keeps those
// promises from drifting apart.
package rentaltest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/rental"
)

// NewStore builds one empty store for a single case.
type NewStore func(t *testing.T) rental.Store

const (
	workspaceID  = "ws_conformance"
	rentalID     = "rnt_conformance"
	connectionID = "con_simcloud"
	firstNode    = "nod_generation_1"
	secondNode   = "nod_generation_2"
)

var start = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// RunStoreSuite exercises every promise the capacity lifecycle relies on.
func RunStoreSuite(t *testing.T, newStore NewStore) {
	t.Helper()

	t.Run("a lease comes back with the generations it has been through", func(t *testing.T) {
		store := newStore(t)
		lease := stoppedAndResumed(t)

		if err := store.Save(context.Background(), 0, lease); err != nil {
			t.Fatalf("take the lease: %v", err)
		}
		held, err := store.Get(context.Background(), workspaceID, rentalID)
		if err != nil {
			t.Fatalf("read the lease back: %v", err)
		}

		if held.Version != lease.Version || len(held.Generations) != 2 {
			t.Fatalf("lease = version %d with %d generations, want version %d with 2", held.Version, len(held.Generations), lease.Version)
		}
		if held.Generations[0] != lease.Generations[0] {
			t.Fatalf("first generation = %+v, want %+v", held.Generations[0], lease.Generations[0])
		}
		if held.Generations[1] != lease.Generations[1] {
			t.Fatalf("second generation = %+v, want %+v", held.Generations[1], lease.Generations[1])
		}
		if held.ConnectionID != connectionID || held.OwnershipToken != "own_conformance" {
			t.Fatalf("lease identity = %q / %q, want the connection and ownership proof it was taken with", held.ConnectionID, held.OwnershipToken)
		}
	})

	t.Run("a released lease comes back released", func(t *testing.T) {
		store := newStore(t)
		lease := terminated(t)

		if err := store.Save(context.Background(), 0, lease); err != nil {
			t.Fatalf("take the lease: %v", err)
		}
		held, err := store.Get(context.Background(), workspaceID, rentalID)
		if err != nil {
			t.Fatalf("read the lease back: %v", err)
		}

		if held.Held() {
			t.Fatal("a lease whose machine was destroyed came back as capacity Mercator still has")
		}
		if held.Generations[0].Ending != domain.RentalTerminated {
			t.Fatalf("ending = %q, want %q", held.Generations[0].Ending, domain.RentalTerminated)
		}
	})

	t.Run("a write that does not follow the version the store holds is refused", func(t *testing.T) {
		store := newStore(t)
		opened := opened(t)
		if err := store.Save(context.Background(), 0, opened); err != nil {
			t.Fatalf("take the lease: %v", err)
		}
		acquired, err := opened.Acquire("i-0abc")
		if err != nil {
			t.Fatalf("acquire the machine: %v", err)
		}
		if err := store.Save(context.Background(), opened.Version, acquired); err != nil {
			t.Fatalf("record what the provider allocated: %v", err)
		}

		second, err := opened.Acquire("i-0def")
		if err != nil {
			t.Fatalf("acquire a second machine: %v", err)
		}
		err = store.Save(context.Background(), opened.Version, second)

		if !errors.Is(err, eventlog.ErrConcurrencyConflict) {
			t.Fatalf("second write at a spent version = %v, want a conflict", err)
		}
		held, readErr := store.Get(context.Background(), workspaceID, rentalID)
		if readErr != nil {
			t.Fatalf("read the lease back: %v", readErr)
		}
		if held.Generations[0].NativeRef != "i-0abc" {
			t.Fatalf("machine = %q, want the one the write that won allocated", held.Generations[0].NativeRef)
		}
	})

	t.Run("a lease identity is taken once", func(t *testing.T) {
		store := newStore(t)
		if err := store.Save(context.Background(), 0, opened(t)); err != nil {
			t.Fatalf("take the lease: %v", err)
		}

		err := store.Save(context.Background(), 0, opened(t))

		if !errors.Is(err, eventlog.ErrConcurrencyConflict) {
			t.Fatalf("taking a lease identity twice = %v, want a conflict", err)
		}
	})

	t.Run("a lease is listed only to the workspace that took it", func(t *testing.T) {
		store := newStore(t)
		if err := store.Save(context.Background(), 0, opened(t)); err != nil {
			t.Fatalf("take the lease: %v", err)
		}

		mine, err := store.List(context.Background(), workspaceID)
		if err != nil {
			t.Fatalf("list this workspace's leases: %v", err)
		}
		theirs, err := store.List(context.Background(), "ws_somebody_else")
		if err != nil {
			t.Fatalf("list another workspace's leases: %v", err)
		}

		if len(mine) != 1 || mine[0].ID != rentalID {
			t.Fatalf("leases = %+v, want the one this workspace took", mine)
		}
		if len(theirs) != 0 {
			t.Fatalf("another workspace saw %+v", theirs)
		}
	})

	t.Run("a lease nobody took is not found rather than empty", func(t *testing.T) {
		store := newStore(t)

		_, err := store.Get(context.Background(), workspaceID, "rnt_missing")

		if !errors.Is(err, rental.ErrNotFound) {
			t.Fatalf("reading a lease nobody took = %v, want ErrNotFound", err)
		}
	})

	t.Run("a lease Mercator could not have reached is refused before it is written", func(t *testing.T) {
		store := newStore(t)
		invented := opened(t)
		invented.Generations = append(invented.Generations, domain.RentalGeneration{
			Number: 2, NodeID: secondNode, BeganAt: start,
		})
		// The version is what two generations would really have cost, so the write
		// is refused for holding two machines at once rather than for counting its
		// own transitions wrong.
		invented.Version = 2

		err := store.Save(context.Background(), 0, invented)

		if err == nil {
			t.Fatal("a lease with two open generations at once was written")
		}
		if _, err := store.Get(context.Background(), workspaceID, rentalID); !errors.Is(err, rental.ErrNotFound) {
			t.Fatalf("after the refusal the store held %v, want nothing", err)
		}
	})
}

// opened is a lease taken and nothing more: the identity Mercator minted, the
// runtime it invited, and no machine yet, which is the state a provision is
// issued from.
func opened(t *testing.T) domain.Rental {
	t.Helper()
	lease, err := domain.OpenRental(domain.RentalIdentity{
		RentalID:       rentalID,
		WorkspaceID:    workspaceID,
		ConnectionID:   connectionID,
		OwnershipToken: "own_conformance",
	}, firstNode, start)
	if err != nil {
		t.Fatalf("open the lease: %v", err)
	}
	return lease
}

// stoppedAndResumed is the lease a round trip has to survive whole: two
// generations, the first ended and the second running, on two different machines
// with two different runtimes.
func stoppedAndResumed(t *testing.T) domain.Rental {
	t.Helper()
	lease, err := opened(t).Acquire("i-0abc")
	if err != nil {
		t.Fatalf("acquire the first machine: %v", err)
	}
	lease, _, err = lease.EndGeneration(domain.RentalStopped, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("stop the machine: %v", err)
	}
	lease, err = lease.BeginGeneration(secondNode, start.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("resume the lease: %v", err)
	}
	lease, err = lease.Acquire("i-0def")
	if err != nil {
		t.Fatalf("acquire the resumed machine: %v", err)
	}
	return lease
}

func terminated(t *testing.T) domain.Rental {
	t.Helper()
	lease, err := opened(t).Acquire("i-0abc")
	if err != nil {
		t.Fatalf("acquire the machine: %v", err)
	}
	lease, _, err = lease.EndGeneration(domain.RentalTerminated, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("terminate the machine: %v", err)
	}
	return lease
}
