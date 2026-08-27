package broker

import (
	"context"
	"errors"
	"sort"

	"github.com/benngarcia/mercator/internal/capability"
)

// This file is the control plane's reach into the capacity lease: allocating a
// machine, reading what a provider says about it, suspending it, bringing it
// back, and destroying it. Each act is its own method because each carries its
// own command, and a single generic call would have to take the widest of them
// and let callers leave the rest empty.
//
// Every one of them resolves the connection from the command itself. A capacity
// ref names the connection that allocated the machine, which is
// what lets a reconciler act on a machine after a control-plane restart with
// nothing in memory to look it up in.

// ProvisionCapacity allocates fresh capacity for one Rental.
func (b *Broker) ProvisionCapacity(ctx context.Context, command capability.ProvisionCommand) (capability.CapacityReceipt, error) {
	provider, err := b.providerFor(ctx, capability.CapacityRef{

		ConnectionID: command.ConnectionID,
	}, capability.CapacityProvision)
	if err != nil {
		return capability.CapacityReceipt{}, err
	}
	return provider.ProvisionCapacity(ctx, command)
}

// ObserveCapacity reads the provider's own view of one machine. It is the
// provider's authority over allocation and provider facts, and says nothing about
// the workload on the machine, which is the node's.
func (b *Broker) ObserveCapacity(ctx context.Context, ref capability.CapacityRef) (capability.CapacityObservation, error) {
	provider, err := b.providerFor(ctx, ref, capability.CapacityObserve)
	if err != nil {
		return capability.CapacityObservation{}, err
	}
	return provider.ObserveCapacity(ctx, ref)
}

// StopCapacity suspends a machine while keeping its identity, and its disk where
// the provider promised one that survives.
func (b *Broker) StopCapacity(ctx context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	provider, err := b.providerFor(ctx, command.CapacityRef, capability.CapacityStop)
	if err != nil {
		return capability.CapacityReceipt{}, err
	}
	return provider.StopCapacity(ctx, command)
}

// ResumeCapacity brings a stopped machine back under the same identity. It is
// named for the act the provider negotiated rather than for StartCapacity, which
// is how the act is performed.
func (b *Broker) ResumeCapacity(ctx context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	provider, err := b.providerFor(ctx, command.CapacityRef, capability.CapacityResume)
	if err != nil {
		return capability.CapacityReceipt{}, err
	}
	return provider.StartCapacity(ctx, command)
}

// TerminateCapacity destroys a machine. It ends the lease, so nothing of
// Mercator's is expected to be reachable on that host afterwards.
func (b *Broker) TerminateCapacity(ctx context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	provider, err := b.providerFor(ctx, command.CapacityRef, capability.CapacityTerminate)
	if err != nil {
		return capability.CapacityReceipt{}, err
	}
	return provider.TerminateCapacity(ctx, command)
}

// providerFor resolves one connection's capacity contract for one operation. The
// capability check happens here, before anything reaches a provider API, so an
// operation a connection never promised costs no request and leaves nothing
// half-changed behind it.
func (b *Broker) providerFor(
	ctx context.Context,
	ref capability.CapacityRef,
	operation capability.CapacityOperation,
) (capability.CapacityProvider, error) {
	_, backend, err := b.connByID(ctx, ref.ConnectionID)
	if err != nil {
		return nil, err
	}
	return backend.CapacityFor(operation)
}

// ListOwnedCapacity is every machine this deployment's capacity connections say
// they are holding, whatever Mercator's own record has. It is the answer a lost
// response is reconciled against: a provider that allocated a machine and could
// not tell Mercator so still knows it did, and the Rental identity travelled
// with the command precisely so this listing names it.
//
// A connection that promised no owned listing is skipped rather than failing the
// sweep. Its machines are reconcilable by nothing else, which is exactly what
// CapacitySupport.Validate refuses a provider for; a connection that got here
// without the promise is one that deduplicates provisions instead.
func (b *Broker) ListOwnedCapacity(ctx context.Context, query capability.OwnershipQuery) ([]capability.OwnedCapacity, error) {
	records, err := b.conns.List(ctx)
	if err != nil {
		return nil, err
	}
	var owned []capability.OwnedCapacity
	for _, record := range records {
		backend, err := b.build(ctx, record)
		if err != nil {
			return nil, err
		}
		provider, err := backend.CapacityFor(capability.CapacityListOwned)
		if errors.Is(err, capability.ErrCapabilityUnsupported) {
			continue
		}
		if err != nil {
			return nil, err
		}
		held, err := provider.ListOwnedCapacity(ctx, query)
		if err != nil {
			return nil, ConnectionErrors{{ConnectionID: record.ID, AdapterType: record.AdapterType, Err: err}}.OrNil()
		}
		for i := range held {
			held[i].ConnectionID = record.ID
		}
		owned = append(owned, held...)
	}
	sort.Slice(owned, func(i, j int) bool {
		if owned[i].ConnectionID != owned[j].ConnectionID {
			return owned[i].ConnectionID < owned[j].ConnectionID
		}
		return owned[i].NativeRef < owned[j].NativeRef
	})
	return owned, nil
}
