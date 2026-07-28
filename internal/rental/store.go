// Package rental is the control plane's authority over the capacity Mercator
// holds: the leases it has taken, the generations each of them has been through,
// and the runtime a generation's end retires.
//
// A Rental is the lease. A Node is the runtime enrolled on one of its
// generations. Ending a generation is the one act that spans both, because the
// runtime exists to serve a machine that is now gone, and a machine Mercator can
// no longer reach must stop being offered as capacity in the same breath.
package rental

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

// ErrNotFound is returned for a lease this workspace never took.
var ErrNotFound = errors.New("rental: not found")

// Store is the durable record of the capacity Mercator holds. A control plane
// that forgot a lease across a restart would leave the machine behind it billing
// with nothing able to name it, so every transition is written here before
// anything acts on it.
type Store interface {
	// Save writes the lease that follows the version the store holds, and
	// returns eventlog.ErrConcurrencyConflict when something else moved first.
	// An expected version of zero is a lease being taken for the first time.
	Save(ctx context.Context, expectedVersion uint64, next domain.Rental) error
	Get(ctx context.Context, workspaceID, rentalID string) (domain.Rental, error)
	List(ctx context.Context, workspaceID string) ([]domain.Rental, error)
}

// Retirer ends the working life of one runtime. It is the half of a generation's
// end that the node registry owns: the Rental says the generation is over, and
// only the registry can stop the machine being offered and answered as capacity.
type Retirer interface {
	Retire(ctx context.Context, workspaceID, nodeID string) error
}

// Leases is where a generation ends. It exists because that one act crosses two
// authorities: the lease records that the machine stopped being Mercator's, and
// the registry retires the runtime that was serving it. Everything else about a
// lease is the Store's own business and callers ask it directly.
type Leases struct {
	store    Store
	runtimes Retirer
}

func NewLeases(store Store, runtimes Retirer) *Leases {
	return &Leases{store: store, runtimes: runtimes}
}

// EndGeneration closes the generation the caller names and retires the runtime
// that was invited onto it. The number is the caller's, because this call and the
// write it lands are separated by a network: an attempt whose answer was lost
// comes back to a lease that may already have resumed onto a fresh machine, and
// ending whichever generation is current then would retire a live runtime on the
// authority of a decision about a machine that is already gone.
//
// The runtime is retired before the lease is written, because the two failures
// are not symmetric. Retiring first and failing to write leaves a machine nothing
// will offer under a generation the record still calls open, and the next attempt
// ends it: retirement is idempotent, so nothing is lost. Writing first and
// failing to retire leaves the opposite, a runtime still publishing itself as
// capacity for a machine the record says Mercator gave up, and the Run that wins
// it starts by discovering there is nobody there.
func (leases *Leases) EndGeneration(
	ctx context.Context,
	workspaceID, rentalID string,
	generation uint64,
	ending domain.RentalGenerationEnding,
	at time.Time,
) (domain.Rental, error) {
	held, err := leases.store.Get(ctx, workspaceID, rentalID)
	if err != nil {
		return domain.Rental{}, err
	}
	next, ended, err := held.EndGeneration(generation, ending, at)
	if err != nil {
		return domain.Rental{}, err
	}
	if err := leases.runtimes.Retire(ctx, workspaceID, ended.NodeID); err != nil {
		return domain.Rental{}, fmt.Errorf("retire the runtime of Rental %q generation %d: %w", rentalID, ended.Number, err)
	}
	if next.Version == held.Version {
		// This generation already ended this way, so there is no transition to
		// write and the lease as it stands is the answer. Retirement was still
		// asked for, because the attempt that ended it may be the one whose answer
		// was lost before it got that far.
		return next, nil
	}
	if err := leases.store.Save(ctx, held.Version, next); err != nil {
		return domain.Rental{}, err
	}
	return next, nil
}

// NewMemoryStore returns an in-memory Store for focused tests and local
// compositions. Production uses the SQLite store, because a lease the control
// plane forgets is a machine nothing can reclaim.
func NewMemoryStore() Store {
	return &memoryStore{rentals: map[string]map[string]domain.Rental{}}
}

type memoryStore struct {
	mu      sync.Mutex
	rentals map[string]map[string]domain.Rental
}

func (store *memoryStore) Save(_ context.Context, expectedVersion uint64, next domain.Rental) error {
	if err := validSave(expectedVersion, next); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.rentals[next.WorkspaceID][next.ID].Version != expectedVersion {
		return fmt.Errorf("%w: Rental %s", eventlog.ErrConcurrencyConflict, next.ID)
	}
	if store.rentals[next.WorkspaceID] == nil {
		store.rentals[next.WorkspaceID] = map[string]domain.Rental{}
	}
	store.rentals[next.WorkspaceID][next.ID] = next.Clone()
	return nil
}

func (store *memoryStore) Get(_ context.Context, workspaceID, rentalID string) (domain.Rental, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	held, ok := store.rentals[workspaceID][rentalID]
	if !ok {
		return domain.Rental{}, fmt.Errorf("%w: %s", ErrNotFound, rentalID)
	}
	return held.Clone(), nil
}

func (store *memoryStore) List(_ context.Context, workspaceID string) ([]domain.Rental, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	held := make([]domain.Rental, 0, len(store.rentals[workspaceID]))
	for _, rental := range store.rentals[workspaceID] {
		held = append(held, rental.Clone())
	}
	sort.Slice(held, func(i, j int) bool { return held[i].ID < held[j].ID })
	return held, nil
}

// validSave is the shape every implementation refuses before it writes. A
// version that does not follow the one being replaced is a transition nobody
// made, and a lease that could not have been reached is worse in a store than in
// memory: everything downstream reads it back as history.
func validSave(expectedVersion uint64, next domain.Rental) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if next.Version <= expectedVersion {
		return fmt.Errorf("Rental %q at version %d does not follow version %d", next.ID, next.Version, expectedVersion)
	}
	return nil
}
