package broker

import (
	"context"
	"fmt"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// Backend is one connection's built implementation together with the
// Declaration derived from the contracts it actually satisfies. Callers ask a
// Backend for the lane they need and get a typed error when the connection
// cannot serve it, instead of type-asserting and discovering the gap at the
// call site.
type Backend struct {
	Declaration capability.Declaration

	capacity  capability.CapacityProvider
	node      capability.NodeRuntime
	ephemeral capability.EphemeralExecutor
}

// NewBackend derives a Backend from a built implementation, refusing any
// implementation whose contracts do not support the lane it would land in.
func NewBackend(adapterType string, built capability.Backend) (Backend, error) {
	declaration, err := capability.Declare(adapterType, built)
	if err != nil {
		return Backend{}, err
	}
	backend := Backend{Declaration: declaration}
	backend.capacity, _ = built.(capability.CapacityProvider)
	backend.node, _ = built.(capability.NodeRuntime)
	backend.ephemeral, _ = built.(capability.EphemeralExecutor)
	return backend, nil
}

// Lane is the reuse semantics every offer from this connection carries.
func (backend Backend) Lane() domain.ExecutionLane { return backend.Declaration.Lane }

// Verify runs the connection's cheap credential and reachability check through
// whichever contract owns it.
func (backend Backend) Verify(ctx context.Context) error {
	switch {
	case backend.capacity != nil:
		return backend.capacity.Verify(ctx)
	case backend.ephemeral != nil:
		return backend.ephemeral.Verify(ctx)
	default:
		return backend.unsupported("verify")
	}
}

// Ephemeral returns the one-shot execution contract, or an error naming the
// lane this connection actually serves.
func (backend Backend) Ephemeral() (capability.EphemeralExecutor, error) {
	if backend.ephemeral == nil {
		return nil, backend.unsupported("one-shot execution")
	}
	return backend.ephemeral, nil
}

// Capacity returns the capacity allocation contract, or an error naming the
// lane this connection actually serves.
func (backend Backend) Capacity() (capability.CapacityProvider, error) {
	if backend.capacity == nil {
		return nil, backend.unsupported("capacity allocation")
	}
	return backend.capacity, nil
}

// Node returns the reusable execution contract, or an error naming the lane
// this connection actually serves.
func (backend Backend) Node() (capability.NodeRuntime, error) {
	if backend.node == nil {
		return nil, backend.unsupported("reusable execution")
	}
	return backend.node, nil
}

func (backend Backend) unsupported(what string) error {
	return fmt.Errorf(
		"%w: %s connection in the %s lane does not provide %s",
		capability.ErrCapabilityUnsupported,
		backend.Declaration.Type,
		backend.Declaration.Lane,
		what,
	)
}
