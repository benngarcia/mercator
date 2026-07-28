package fake

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the simulated provider's half of the capacity lease: allocating
// a machine behind a listing, saying what it is doing, destroying it, and
// answering what this connection is holding. It is separate from the launch
// path in world.go for the reason the contracts are separate: what is allocated
// here outlives every workload run on it, and nothing here knows what a
// workload is.

// Enroller is the agent side of a bootstrap: what a machine does with the
// material a provider handed it. This world calls it at the moment its node
// agent would have opened a session, so what follows is a real enrolment
// against the real registry rather than a flag this world sets on itself.
type Enroller interface {
	Enroll(ctx context.Context, request capability.EnrollmentRequest) (capability.Enrollment, error)
}

// allocation is one machine this world allocated against one Rental. It is
// keyed by the lease rather than by the listing, because the lease is what
// Mercator names when it asks again after losing an answer, and a marketplace
// numbers its listings afresh on every search.
type allocation struct {
	rentalID       string
	offerID        string
	nativeRef      string
	workspaceID    string
	connectionID   string
	ownershipToken string
	bootstrap      capability.NodeBootstrap
	acceptedAt     time.Time
	terminated     bool
	terminatedAt   time.Time
	enrolled       bool
}

// capacityProgress is how far this world has got with one machine and the moment
// it got there. The two travel together because a provider that reports a state
// without dating it leaves its caller measuring the interval between two looks
// and calling that the machine's own spend.
type capacityProgress struct {
	state capability.CapacityState
	since time.Time
}

// CapacitySupport is what this simulated provider promises. It stops, resumes,
// keeps a disk across a stop, deduplicates on the operation key it is given,
// and can enumerate what it holds, which is the shape of every provider a
// Blueprint in this corpus describes.
func (w *World) CapacitySupport() capability.CapacitySupport {
	return capability.CapacitySupport{
		Stop:                true,
		Resume:              true,
		PersistentDisk:      true,
		ExactPricing:        true,
		IdempotentProvision: capability.IdempotentProvisionOperationKey,
		ListOwned:           true,
	}
}

// Verify allocates nothing and always answers, because a simulated provider has
// no credential to be wrong about.
func (w *World) Verify(_ context.Context) error { return nil }

// ListCapacity is the capacity this world sells, which is the same listing set
// placement already reads. A capacity connection publishes no placement
// candidate of its own: what ListCapacity returns is capacity to acquire.
func (w *World) ListCapacity(ctx context.Context, query capability.CapacityQuery) ([]domain.OfferSnapshot, error) {
	return w.ListOffers(ctx, adapter.OfferRequest{WorkspaceID: query.WorkspaceID, Resources: query.Resources})
}

// Provision allocates the machine behind one listing and holds the bootstrap it
// was handed. The same operation key twice allocates one machine and says so,
// which is the promise CapacitySupport makes above.
func (w *World) ProvisionCapacity(_ context.Context, command capability.ProvisionCommand) (capability.CapacityReceipt, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if held, exists := w.allocations[command.RentalID]; exists && !held.terminated {
		return capability.CapacityReceipt{
			NativeRef:  held.nativeRef,
			State:      w.capacityStateAt(held, w.clock.Now()).state,
			AcceptedAt: held.acceptedAt,
			Duplicate:  true,
		}, nil
	}
	machine, exists := w.machines[command.OfferSnapshotID]
	if !exists {
		return capability.CapacityReceipt{}, fmt.Errorf("fake: no listing %q to allocate from", command.OfferSnapshotID)
	}
	now := w.clock.Now()
	held := &allocation{
		rentalID:       command.RentalID,
		offerID:        command.OfferSnapshotID,
		nativeRef:      machine.Offer.MachineID,
		workspaceID:    command.WorkspaceID,
		connectionID:   command.ConnectionID,
		ownershipToken: command.OwnershipToken,
		bootstrap:      command.Bootstrap,
		acceptedAt:     now,
	}
	if held.nativeRef == "" {
		held.nativeRef = command.OfferSnapshotID
	}
	w.allocations[command.RentalID] = held
	return capability.CapacityReceipt{
		NativeRef:  held.nativeRef,
		State:      capability.CapacityStateRequested,
		AcceptedAt: now,
		Pricing:    machine.Offer.Pricing,
	}, nil
}

// ObserveCapacity is what the provider can see, which is allocation and boot and
// nothing past them. Whether an agent opened a session is the registry's answer
// and never this one: a provider that reported a machine ready to run work would
// be answering for a contract it does not hold.
func (w *World) ObserveCapacity(_ context.Context, ref capability.CapacityRef) (capability.CapacityObservation, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	held, exists := w.allocations[ref.RentalID]
	if !exists {
		return capability.CapacityObservation{}, fmt.Errorf("fake: nothing allocated for Rental %q", ref.RentalID)
	}
	now := w.clock.Now()
	progress := w.capacityStateAt(held, now)
	return capability.CapacityObservation{
		NativeRef:  held.nativeRef,
		State:      progress.state,
		ObservedAt: now,
		StateSince: progress.since,
	}, nil
}

// StartCapacity and StopCapacity are the two promises this provider makes and
// nothing in the corpus exercises yet. They are here because the contract is
// the whole set: a provider that claims a stop it cannot perform is exactly what
// the negotiated capability set exists to refuse.
func (w *World) StartCapacity(_ context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	return w.transition(command, capability.CapacityStateActive)
}

func (w *World) StopCapacity(_ context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	return w.transition(command, capability.CapacityStateStopped)
}

// TerminateCapacity destroys the machine. The listing it came from is untouched:
// a marketplace goes on selling the product whose last machine Mercator gave
// back, which is what makes an offer struck out by an earlier attempt something
// a later decision still has to see and refuse.
func (w *World) TerminateCapacity(_ context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	held, exists := w.allocations[command.RentalID]
	if !exists {
		return capability.CapacityReceipt{}, fmt.Errorf("fake: nothing allocated for Rental %q", command.RentalID)
	}
	duplicate := held.terminated
	if !duplicate {
		// The moment the machine was destroyed, which a repeat of the same command
		// does not move: the bill ended once.
		held.terminatedAt = w.clock.Now()
	}
	held.terminated = true
	return capability.CapacityReceipt{
		NativeRef:  held.nativeRef,
		State:      capability.CapacityStateTerminated,
		AcceptedAt: w.clock.Now(),
		Duplicate:  duplicate,
	}, nil
}

func (w *World) transition(command capability.CapacityCommand, state capability.CapacityState) (capability.CapacityReceipt, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	held, exists := w.allocations[command.RentalID]
	if !exists {
		return capability.CapacityReceipt{}, fmt.Errorf("fake: nothing allocated for Rental %q", command.RentalID)
	}
	return capability.CapacityReceipt{NativeRef: held.nativeRef, State: state, AcceptedAt: w.clock.Now()}, nil
}

// ListOwnedCapacity is every machine this world is still holding for a
// workspace. It is the answer a lost provision response is reconciled against,
// so it names the Rental the machine was allocated for: without that a
// reconciler could only count machines, and counting cannot tell the machine
// this Run is waiting for from the machine the Run beside it is waiting for.
func (w *World) ListOwnedCapacity(_ context.Context, query capability.OwnershipQuery) ([]capability.OwnedCapacity, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var owned []capability.OwnedCapacity
	for _, rentalID := range slices.Sorted(maps.Keys(w.allocations)) {
		held := w.allocations[rentalID]
		if held.terminated || held.workspaceID != query.WorkspaceID {
			continue
		}
		owned = append(owned, capability.OwnedCapacity{
			NativeRef:      held.nativeRef,
			ConnectionID:   held.connectionID,
			WorkspaceID:    held.workspaceID,
			RentalID:       held.rentalID,
			Generation:     held.bootstrap.Generation,
			OwnershipToken: held.ownershipToken,
			State:          w.capacityStateAt(held, w.clock.Now()).state,
			CreatedAt:      held.acceptedAt,
		})
	}
	return owned, nil
}

// capacityStateAt is how far this world has got with one machine, spent from the
// moment the allocation was accepted, and when it reached that. Acquisition puts
// it in starting and boot puts it in active; the agent's own arrival is past the
// end of what a provider can see, so nothing after boot moves this answer.
func (w *World) capacityStateAt(held *allocation, now time.Time) capacityProgress {
	if held.terminated {
		return capacityProgress{state: capability.CapacityStateTerminated, since: held.terminatedAt}
	}
	machine, exists := w.machines[held.offerID]
	if !exists {
		return capacityProgress{state: capability.CapacityStateUnknown}
	}
	acquired := held.acceptedAt.Add(machine.AcquisitionSpend)
	booted := acquired.Add(machine.BootSpend)
	switch {
	case now.Before(acquired):
		return capacityProgress{state: capability.CapacityStateRequested, since: held.acceptedAt}
	case now.Before(booted):
		return capacityProgress{state: capability.CapacityStateStarting, since: acquired}
	default:
		return capacityProgress{state: capability.CapacityStateActive, since: booted}
	}
}

// DeliverEnrolments is every agent in this world whose machine has finished
// booting and waiting, opening its session for the first time. It is a call the
// machine makes rather than something the control plane reads, which is what
// makes an agent that never arrives a silence rather than a negative answer.
//
// A machine whose listing says its agent never enrols is skipped for ever, and
// that is the whole of the failure: Mercator has no session to it, so nothing
// can create a container there, and the record says the start was never
// observed because nobody was ever able to look.
func (w *World) DeliverEnrolments(ctx context.Context) error {
	if w.Enroller == nil {
		return nil
	}
	for _, pending := range w.dueEnrolments() {
		if _, err := w.Enroller.Enroll(ctx, capability.EnrollmentRequest{
			NodeID:          pending.bootstrap.NodeID,
			RentalID:        pending.bootstrap.RentalID,
			Generation:      pending.bootstrap.Generation,
			EnrollmentToken: pending.bootstrap.EnrollmentToken,
			AgentVersion:    pending.bootstrap.AgentVersion,
			Facts: capability.NodeFacts{
				Host: capability.HostFacts{OS: "linux", ContainerRuntime: "docker"},
			},
		}); err != nil {
			return fmt.Errorf("fake: agent on Rental %q could not enrol: %w", pending.rentalID, err)
		}
	}
	return nil
}

func (w *World) dueEnrolments() []*allocation {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.clock.Now()
	var due []*allocation
	for _, rentalID := range slices.Sorted(maps.Keys(w.allocations)) {
		held := w.allocations[rentalID]
		machine, exists := w.machines[held.offerID]
		if held.enrolled || held.terminated || !exists || machine.NeverEnrolls {
			continue
		}
		arrives := held.acceptedAt.
			Add(machine.AcquisitionSpend).
			Add(machine.BootSpend).
			Add(machine.AgentReadySpend)
		if now.Before(arrives) {
			continue
		}
		held.enrolled = true
		due = append(due, held)
	}
	return due
}
