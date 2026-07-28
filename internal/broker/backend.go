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

// Publication is one connection's contribution to a placement read: the
// candidates it published, each stamped with the lane its backend actually
// serves, or the reason nobody asked it for any.
//
// The two are stated apart because the Booking Decision records them apart. A
// connection that answered with nothing and a connection nobody asked both
// publish no offer, and Placement reads an empty answer as the strongest thing a
// fleet can say about an ask, so a census that counted an unasked connection
// among the asked would tell an operator a provider was consulted about a Run it
// was never consulted about.
type Publication struct {
	Offers []domain.OfferSnapshot
	// NotAsked is why this connection was never asked for a candidate. It is
	// empty exactly when the connection was asked.
	NotAsked string
}

// ListOffers asks this connection for the candidates Placement may choose among,
// in the lane this connection sells. A one-shot executor answers with the
// executions it can run, and the lane is stamped here rather than by the caller,
// because this is where the offers cross out of the adapter: an offer that
// reached aggregation unstamped would carry whatever the adapter said about its
// own reuse semantics and its own Rental identity.
//
// A capacity connection is not asked at all, and that is where the migration
// stands rather than a gap in this seam. What a provider lists is capacity to
// acquire, and until an agent enrolls on one of those machines nothing on it can
// execute anything: an offer built from the listing would have to state a
// container runtime, an idempotent launch and free capacity, which are the node's
// facts to establish from its own heartbeat and not a provider's to assert about
// a host it does not run on. Placement acting on such an offer is a Run booked
// against a machine that cannot take it, which is exactly what happened while
// this returned the listing.
//
// The machines Mercator does hold are published by the node registry, from the
// enrollment: the Rental the invitation named, and the host the agent reported.
// Publishing the provider's own copy beside them would count one machine twice
// under two Rental identities and let two Runs each believe they held the only
// queue on it.
//
// A provider's listing becomes a candidate when the control plane can act on the
// selection, which is provisioning a Rental and bootstrapping an agent onto it:
// mercator#200. Until then ListCapacity has no caller on this path, and the
// census says so rather than naming a question nobody asked.
func (backend Backend) ListOffers(ctx context.Context, request adapter.OfferRequest) (Publication, error) {
	if backend.capacity != nil {
		return Publication{NotAsked: notAskedForCandidates}, nil
	}
	executor, err := backend.Ephemeral()
	if err != nil {
		return Publication{}, err
	}
	offers, err := executor.ListOffers(ctx, request)
	if err != nil {
		return Publication{}, err
	}
	return Publication{Offers: capability.StampLane(backend.Declaration, offers)}, nil
}

// notAskedForCandidates is what the decision record says about a capacity
// connection: it sells machines to acquire, and acquiring one is mercator#200.
const notAskedForCandidates = "sells capacity to acquire and publishes no placement candidate (mercator#200)"

// ListOwned asks this connection which workloads of Mercator's it is still
// running, which is what the ownership sweep converges: a one-shot execution left
// behind by a lost response bills until something reclaims it, and the sweep
// decides each one against Mercator's own record of the Run it was launched for.
//
// A capacity connection is running none, ever. It holds machines, and a machine
// is not a workload: it carries no Run, because a Rental outlives the Run placed
// on it, and the sweep reads an owned object naming no Run as capacity nobody can
// account for and destroys it. Reporting machines here therefore recorded a
// durable decision to terminate every machine a provider deliberately holds, and
// then failed to carry it out, aborting the sweep before it reached the one-shot
// executions that were genuinely leaking. Machines are swept against Rental
// records by the reconciler in mercator#199, in the Rental's own vocabulary.
//
// Answering with nothing is what this connection has to say rather than a silence
// over a leak. Nothing allocates a machine yet, and CapacitySupport.Validate
// already refuses the one negotiated set where a machine could go unaccounted
// for: a provider that deduplicates no provision and lists no owned capacity.
func (backend Backend) ListOwned(ctx context.Context, request adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	if backend.capacity != nil {
		return nil, nil
	}
	executor, err := backend.Ephemeral()
	if err != nil {
		return nil, err
	}
	return executor.ListOwned(ctx, request)
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
