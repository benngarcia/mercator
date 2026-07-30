package sqlite

import (
	"context"
	"testing"

	"github.com/benngarcia/mercator/internal/node"
	"github.com/benngarcia/mercator/internal/node/nodetest"
)

// TestSQLiteNodeStoreSatisfiesTheNodeStoreContract is where restart safety is
// actually proved: the durable store makes the same promises the in-memory one
// does, so a control plane that comes back knows what its nodes already did.
func TestSQLiteNodeStoreSatisfiesTheNodeStoreContract(t *testing.T) {
	nodetest.RunStoreSuite(t, func(t *testing.T) node.Store {
		storage, err := Open(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
		if err != nil {
			t.Fatalf("open storage: %v", err)
		}
		t.Cleanup(func() { _ = storage.Close() })
		return storage.Nodes()
	})
}
