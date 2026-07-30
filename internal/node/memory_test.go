package node_test

import (
	"testing"

	"github.com/benngarcia/mercator/internal/node"
	"github.com/benngarcia/mercator/internal/node/nodetest"
)

func TestMemoryStoreSatisfiesTheNodeStoreContract(t *testing.T) {
	nodetest.RunStoreSuite(t, func(*testing.T) node.Store { return node.NewMemoryStore() })
}
