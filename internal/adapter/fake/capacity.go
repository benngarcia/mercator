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
	rentalID string
	// offerID is the listing this machine was allocated from, and nativeRef the
	// provider's own handle for the machine itself. They are two strings because
	// they are two things: the listing goes on being sold after this machine is
	// destroyed, and the machine goes on existing after the listing is withdrawn.
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

// ListCapacity is the capacity this world sells, which is what a search of the
// marketplace returns. A capacity connection publishes no placement candidate of
// its own: what ListCapacity returns is capacity to acquire, and capacity held
// under a lease has already been acquired. Selling it again would offer a
// workspace a machine it is already paying for, under a lease it already holds.
//
// The fleet is the other question and ListOffers is where it is asked. A machine
// this world allocated, and a Rental a Blueprint declared, are both in that
// answer and neither is in this one.
func (w *World) ListCapacity(ctx context.Context, query capability.CapacityQuery) ([]domain.OfferSnapshot, error) {
	offers, err := w.ListOffers(ctx, adapter.OfferRequest{WorkspaceID: query.WorkspaceID, Resources: query.Resources})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(offers, leased), nil
}

func leased(offer domain.OfferSnapshot) bool { return offer.RentalID != "" }

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
	listing, exists := w.machines[command.OfferSnapshotID]
	if !exists {
		return capability.CapacityReceipt{}, fmt.Errorf("fake: no listing %q to allocate from", command.OfferSnapshotID)
	}
	nativeRef := machineHandle(listing, command.RentalID)
	if holder, taken := w.holderOf(nativeRef); taken {
		return capability.CapacityReceipt{}, fmt.Errorf("fake: listing %q sells machine %q, which Rental %q is already holding", command.OfferSnapshotID, nativeRef, holder)
	}
	now := w.clock.Now()
	held := &allocation{
		rentalID:       command.RentalID,
		offerID:        command.OfferSnapshotID,
		nativeRef:      nativeRef,
		workspaceID:    command.WorkspaceID,
		connectionID:   command.ConnectionID,
		ownershipToken: command.OwnershipToken,
		bootstrap:      command.Bootstrap,
		acceptedAt:     now,
	}
	w.allocations[command.RentalID] = held
	return capability.CapacityReceipt{
		NativeRef:  held.nativeRef,
		State:      capability.CapacityStateRequested,
		AcceptedAt: now,
		Pricing:    listing.Offer.Pricing,
	}, nil
}

// machineHandle is the provider's own handle for the machine one Rental
// allocated. A listing that names a machine is a listing of that machine, so its
// handle is the one the listing declared. A listing that names none is a
// product, and the machine it yields is one nothing has named yet, so this
// provider mints a handle from the lease that bought it.
//
// It is never the listing's own ID, and that is the whole reason it is derived
// here rather than read off the offer. A machine published under the ID of the
// product it came from replaces that product: the listing stops being sold, the
// provisioning stages it published vanish, and a later Run reading the
// marketplace finds standing capacity where a catalog entry was.
func machineHandle(listing *Machine, rentalID string) string {
	if listing.Offer.MachineID != "" {
		return listing.Offer.MachineID
	}
	return "mch_" + rentalID
}

// sold is one listing of a machine this world has already allocated, answering
// as what it now is: capacity nobody can buy. A listing that names a machine is
// a listing of that machine, so a lease on the machine takes every listing of it
// off the market at once, and two ask IDs for one host are two names for
// capacity that can be bought exactly once.
//
// It is published and refused rather than withdrawn, for the reason
// TerminateCapacity leaves the listing alone: a decision has to see the offer to
// record having refused it, and the product is on sale again the moment the
// lease ends. The machine itself is untouched here, because it is standing
// capacity under a lease rather than a listing of anything.
func (w *World) sold(offer domain.OfferSnapshot) domain.OfferSnapshot {
	if offer.RentalID != "" || offer.MachineID == "" {
		return offer
	}
	if _, taken := w.holderOf(offer.MachineID); taken {
		offer.Capacity.Available = false
	}
	return offer
}

// holderOf is the live Rental this world has already allocated one machine to.
//
// A listing that names a machine is a listing of that machine, and a marketplace
// cannot sell one machine twice. Two listings of it cannot either, which is a
// real shape: a provider republishing the same host under a fresh ask ID on
// every search sells one machine under as many names as it has been searched
// for. The second sale would hand Mercator a lease on a host another lease is
// already running work on, and the warm content, the busy window and the Rental
// identity of the first would all be the second's.
func (w *World) holderOf(nativeRef string) (string, bool) {
	for _, rentalID := range slices.Sorted(maps.Keys(w.allocations)) {
		if held := w.allocations[rentalID]; held.nativeRef == nativeRef && !held.terminated {
			return held.rentalID, true
		}
	}
	return "", false
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

// TerminateCapacity destroys the machine and stops publishing it, which is the
// inverse of the publication its enrolment performed. A provider that went on
// advertising a machine it destroyed would let a later Run be placed on, and
// recorded as having successfully executed on, a host that no longer exists and
// nobody is billed for, while ListOwnedCapacity in the same world reported
// nothing owned.
//
// The listing it came from is untouched: a marketplace goes on selling the
// product whose last machine Mercator gave back, which is what makes an offer
// struck out by an earlier attempt something a later decision still has to see
// and refuse, and what lets the machine be bought again.
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
	delete(w.machines, held.nativeRef)
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
		if err := w.publishEnrolledMachine(pending); err != nil {
			return err
		}
	}
	return nil
}

// publishEnrolledMachine is the machine this world just opened a session to
// taking its own place in the fleet: standing capacity in the reusable lane,
// under the provider's handle for the machine rather than under the listing it
// was bought from.
//
// It is published here and nowhere earlier because this is the moment it becomes
// any. A machine that is allocated, or booted, is a machine nothing can create a
// container on; what makes it executable is the agent's session, which is why the
// same rule refuses a Rental whose agent never enrols in AddMachine.
//
// The listing is left exactly as it was. A marketplace goes on selling the
// product a machine was allocated from, so a second Run sees the machine and the
// listing as two candidates and has to choose, which is the choice this fixture
// is about. Publishing the machine over the listing would have made reuse an
// arithmetic identity rather than a decision.
func (w *World) publishEnrolledMachine(held *allocation) error {
	w.mu.Lock()
	listing, exists := w.machines[held.offerID]
	w.mu.Unlock()
	if !exists {
		return fmt.Errorf("fake: Rental %q was allocated from a listing this world no longer has", held.rentalID)
	}
	return w.AddMachine(machineBehind(listing, held))
}

// machineBehind is the machine one listing turned into: the listing's own shape,
// under the machine's identity, owing none of the provisioning it has now spent
// and holding nothing yet.
func machineBehind(listing *Machine, held *allocation) *Machine {
	offer := listing.Offer
	offer.ID = held.nativeRef
	offer.NativeRef = held.nativeRef
	offer.MachineID = held.nativeRef
	// The lease this machine is held under, which is the one the invitation named
	// rather than the listing's ID. A launch history filed under the listing would
	// answer for every machine that product ever sold.
	offer.RentalID = held.rentalID
	offer.Kind = domain.OfferKindStanding
	// What a machine that does not exist yet publishes about coming into
	// existence, dropped because this one has. An offer that still carried them
	// would price the boot of a machine that is already up.
	offer.Provisioning = nil
	offer.Bootstrap = nil
	return &Machine{
		Offer:            offer,
		HeldLayers:       map[string]int64{},
		HeldDiffIDs:      map[string]bool{},
		HeldImages:       map[string]bool{},
		ArtifactReplicas: map[string]domain.ArtifactReplica{},
		HeldCaches:       map[string]domain.CacheMount{},
		// What a launch here costs once its content has arrived, which is the
		// listing's answer because it is the same machine. The provisioning spends
		// are deliberately not carried: this machine has finished spending them.
		UnpackSpend:           listing.UnpackSpend,
		ContainerStartSpend:   listing.ContainerStartSpend,
		ApplicationReadySpend: listing.ApplicationReadySpend,
		LinkMbps:              listing.LinkMbps,
	}
}

// executionHost is the machine one launch really runs on. A Run placed on a
// listing runs on the machine that listing was allocated into for this very
// attempt, which the ownership token names: the same token stamps the provision
// command and the launch, and it is what the ownership sweep already attributes a
// machine by. Everything else runs on the capacity the launch named.
func (w *World) executionHost(request adapter.LaunchRequest) (*Machine, bool) {
	for _, rentalID := range slices.Sorted(maps.Keys(w.allocations)) {
		held := w.allocations[rentalID]
		if held.ownershipToken != request.OwnershipToken || !held.enrolled {
			continue
		}
		machine, exists := w.machines[held.nativeRef]
		return machine, exists
	}
	machine, exists := w.machines[request.SelectedOfferSnapshotID]
	return machine, exists
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
