package broker

import (
	"context"

	"github.com/benngarcia/mercator/internal/capability"
)

// This file is the control plane's reach into the capacity lease: allocating a
// machine, reading what a provider says about it, suspending it, bringing it
// back, and destroying it. Each act is its own method because each carries its
// own command, and a single generic call would have to take the widest of them
// and let callers leave the rest empty.
//
// Every one of them resolves the connection from the command itself. A capacity
// ref names the workspace and the connection that allocated the machine, which is
// what lets a reconciler act on a machine after a control-plane restart with
// nothing in memory to look it up in.

// ProvisionCapacity allocates fresh capacity for one Rental.
func (b *Broker) ProvisionCapacity(ctx context.Context, command capability.ProvisionCommand) (capability.CapacityReceipt, error) {
	provider, err := b.providerFor(ctx, capability.CapacityRef{
		WorkspaceID:  command.WorkspaceID,
		ConnectionID: command.ConnectionID,
	}, capability.CapacityProvision)
	if err != nil {
		return capability.CapacityReceipt{}, err
	}
	return provider.Provision(ctx, command)
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
	_, backend, err := b.connByID(ctx, ref.WorkspaceID, ref.ConnectionID)
	if err != nil {
		return nil, err
	}
	return backend.CapacityFor(operation)
}
