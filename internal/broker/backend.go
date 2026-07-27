package broker

import (
	"context"
	"fmt"

	"github.com/benngarcia/mercator/internal/adapter"
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

// NewBackend derives a Backend from a built implementation and the node runtime
// this deployment holds, refusing any implementation whose contracts do not
// support the lane it would land in. A nil fleet is a Mercator with no enrolled
// node runtime, where nothing could execute a second workload on rented
// capacity.
func NewBackend(adapterType string, built capability.Backend, fleet capability.Fleet) (Backend, error) {
	declaration, err := capability.Declare(adapterType, built, fleet)
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

// ListOffers asks this connection what it is selling, through whichever contract
// it implements: a capacity provider is asked for the machines it can allocate,
// and a one-shot executor for the executions it can run. Which of the two
// answered is settled by the Declaration rather than at the call site, and a
// connection cannot be both, so there is no precedence rule here to get wrong.
func (backend Backend) ListOffers(ctx context.Context, request adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	if backend.capacity == nil {
		executor, err := backend.Ephemeral()
		if err != nil {
			return nil, err
		}
		return executor.ListOffers(ctx, request)
	}
	return backend.capacity.ListCapacity(ctx, capability.CapacityQuery{
		WorkspaceID: request.WorkspaceID,
		Resources:   request.Resources,
	})
}

// ListOwned asks this connection what it still holds for one workspace, through
// whichever contract it implements. A one-shot executor answers with the
// executions it is still running, and a capacity provider with the machines it
// still holds.
//
// A provider that does not claim the owned listing answers with nothing rather
// than a refusal, and that is the negotiated answer rather than a silence hiding
// a leak: CapacitySupport.Validate refuses the one set where a machine could go
// unaccounted for, which is a provider that deduplicates no provision AND lists
// no owned capacity. A provider that deduplicates on an operation key loses no
// machine to a lost response, so there is nothing for a sweep to discover.
func (backend Backend) ListOwned(ctx context.Context, request adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	if backend.capacity == nil {
		executor, err := backend.Ephemeral()
		if err != nil {
			return nil, err
		}
		return executor.ListOwned(ctx, request)
	}
	if !backend.Declaration.Capacity.Claims(capability.CapacityListOwned) {
		return nil, nil
	}
	held, err := backend.capacity.ListOwnedCapacity(ctx, capability.OwnershipQuery{WorkspaceID: request.WorkspaceID})
	if err != nil {
		return nil, err
	}
	return ownedMachines(held), nil
}

// CapacityFor returns the capacity contract for one operation, refusing at the
// seam any operation this provider's negotiated set never promised. A provider
// that cannot suspend a machine learns that from a caller that never sent the
// command, rather than from an API call that may have changed something before
// it failed.
func (backend Backend) CapacityFor(operation capability.CapacityOperation) (capability.CapacityProvider, error) {
	provider, err := backend.Capacity()
	if err != nil {
		return nil, err
	}
	if !backend.Declaration.Capacity.Claims(operation) {
		return nil, fmt.Errorf(
			"%w: %s connection does not promise the capacity operation %q",
			capability.ErrCapabilityUnsupported,
			backend.Declaration.Type,
			operation,
		)
	}
	return provider, nil
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
