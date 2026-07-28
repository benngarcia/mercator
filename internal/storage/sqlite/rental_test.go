package sqlite

import (
	"context"
	"testing"

	"github.com/benngarcia/mercator/internal/rental"
	"github.com/benngarcia/mercator/internal/rental/rentaltest"
)

// TestSQLiteRentalStoreSatisfiesTheRentalStoreContract is where the capacity
// lifecycle's restart safety is actually proved: the durable store makes the same
// promises the in-memory one does, so a control plane that comes back still knows
// which machines it is paying for and which generation each of them is on.
func TestSQLiteRentalStoreSatisfiesTheRentalStoreContract(t *testing.T) {
	rentaltest.RunStoreSuite(t, func(t *testing.T) rental.Store {
		storage, err := Open(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
		if err != nil {
			t.Fatalf("open storage: %v", err)
		}
		t.Cleanup(func() { _ = storage.Close() })
		return storage.Rentals()
	})
}
