package lab

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
	"github.com/benngarcia/mercator/internal/scenario"
)

// This file is the Lab's half of the capacity lease: a machine allocated behind
// a listing, what the provider says about it, and the agent that does or does
// not arrive on it. It is separate from the execution seam in world.go for the
// reason the contracts are separate, and every act here writes its own ledger
// operation, because the ledger is the only account of what really happened to a
// machine.

// capacityLease is one machine this world allocated for one Rental. It is keyed
// by the lease and not by the listing: a marketplace numbers its listings afresh
// on every search, and the lease is what Mercator names when it asks again about
// an allocation whose answer it lost.
type capacityLease struct {
	RentalID       string
	NodeID         string
	Generation     uint64
	OfferID        string
	NativeRef      string
	WorkspaceID    string
	ConnectionID   string
	OwnershipToken string
	AcceptedAt     time.Time
	Terminated     bool
	Enrolled       bool
	// Token is the material handed to the machine, held so the agent can redeem
	// exactly the one its provider was given. It never leaves this world.
	Token string
}

// arrivesAt is when the agent on this machine opens its session, which is the
// whole of provisioning spent end to end. A listing whose agent never enrols
// reaches it and still says nothing, which is what makes that failure a silence
// rather than a negative answer.
func (lease capacityLease) arrivesAt(spend scenario.ProvisioningSpec) time.Time {
	return lease.AcceptedAt.Add(spend.Spend())
}

// ProvisionCapacity allocates the machine behind one listing and takes the
// bootstrap it was handed, verbatim. The same operation key twice allocates one
// machine and says so, which is what makes an allocation whose response was lost
// cost one repeat rather than one extra machine.
func (world *simulatedWorld) ProvisionCapacity(_ context.Context, command capability.ProvisionCommand) (capability.CapacityReceipt, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	if command.OperationKey == "" || command.RequestHash == "" {
		return capability.CapacityReceipt{}, fmt.Errorf("Lab provider provision needs operation key and request hash")
	}
	if lease, exists := world.leases[command.RentalID]; exists && !lease.Terminated {
		receipt := capability.CapacityReceipt{
			NativeRef:  lease.NativeRef,
			State:      world.capacityStateOf(lease),
			AcceptedAt: lease.AcceptedAt,
			Duplicate:  true,
		}
		world.recordCapacityEffect(OperationCapacityProvision, command.OperationKey, command.RequestHash,
			command.RentalID, EffectCommandDuplicate, provisionRequestFacts(command), receipt)
		return receipt, nil
	}
	state, exists := world.truth[command.OfferSnapshotID]
	if !exists {
		world.recordCapacityEffect(OperationCapacityProvision, command.OperationKey, command.RequestHash,
			command.RentalID, EffectCommandRejected, provisionRequestFacts(command), nil)
		return capability.CapacityReceipt{}, fmt.Errorf("Lab has no listing %q to allocate from", command.OfferSnapshotID)
	}
	lease := &capacityLease{
		RentalID:       command.RentalID,
		NodeID:         command.Bootstrap.NodeID,
		Generation:     command.Generation,
		OfferID:        command.OfferSnapshotID,
		NativeRef:      "lab-machine-" + command.RentalID,
		WorkspaceID:    command.WorkspaceID,
		ConnectionID:   labConnection,
		OwnershipToken: command.OwnershipToken,
		AcceptedAt:     world.now,
		Token:          command.Bootstrap.EnrollmentToken,
	}
	world.leases[command.RentalID] = lease
	receipt := capability.CapacityReceipt{
		NativeRef:  lease.NativeRef,
		State:      capability.CapacityStateRequested,
		AcceptedAt: world.now,
		Pricing:    state.offer.Pricing,
	}
	world.recordCapacityEffect(OperationCapacityProvision, command.OperationKey, command.RequestHash,
		command.RentalID, EffectCommandAccepted, provisionRequestFacts(command), receipt)
	return receipt, nil
}

// ObserveCapacity is what the provider can see, which is the allocation and the
// boot and nothing past them. Whether an agent opened a session is the registry's
// answer: a provider that reported a machine ready to run work would be
// answering for a contract it does not hold.
func (world *simulatedWorld) ObserveCapacity(_ context.Context, ref capability.CapacityRef) (capability.CapacityObservation, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	lease, exists := world.leases[ref.RentalID]
	if !exists {
		return capability.CapacityObservation{}, fmt.Errorf("Lab holds nothing for Rental %q", ref.RentalID)
	}
	observation := capability.CapacityObservation{
		NativeRef:  lease.NativeRef,
		State:      world.capacityStateOf(lease),
		ObservedAt: world.now,
	}
	world.recordCapacityEffect(OperationCapacityObserve, "", "", lease.RentalID, EffectCommandAccepted,
		map[string]any{"rental_id": lease.RentalID}, observation)
	return observation, nil
}

// TerminateCapacity destroys the machine and leaves the listing alone. A
// marketplace goes on selling the product whose last machine Mercator gave back,
// which is what makes a listing struck out by an earlier attempt something a
// later decision still has to see and refuse.
func (world *simulatedWorld) TerminateCapacity(_ context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	lease, exists := world.leases[command.RentalID]
	if !exists {
		return capability.CapacityReceipt{}, fmt.Errorf("Lab holds nothing for Rental %q", command.RentalID)
	}
	command2 := EffectCommandAccepted
	if lease.Terminated {
		command2 = EffectCommandDuplicate
	}
	lease.Terminated = true
	receipt := capability.CapacityReceipt{
		NativeRef:  lease.NativeRef,
		State:      capability.CapacityStateTerminated,
		AcceptedAt: world.now,
		Duplicate:  command2 == EffectCommandDuplicate,
	}
	world.recordCapacityEffect(OperationCapacityTerminate, command.OperationKey, command.RequestHash,
		command.RentalID, command2, map[string]any{"rental_id": command.RentalID}, receipt)
	return receipt, nil
}

// ListOwnedCapacity is every machine this world still holds for a workspace,
// each naming the Rental it was allocated for. Without that name a reconciler
// could only count machines, and counting cannot tell the machine this Run is
// waiting for from the machine the Run beside it is waiting for.
func (world *simulatedWorld) ListOwnedCapacity(_ context.Context, query capability.OwnershipQuery) ([]capability.OwnedCapacity, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	var owned []capability.OwnedCapacity
	for _, rentalID := range slices.Sorted(maps.Keys(world.leases)) {
		lease := world.leases[rentalID]
		if lease.Terminated || (query.WorkspaceID != "" && query.WorkspaceID != lease.WorkspaceID) {
			continue
		}
		owned = append(owned, capability.OwnedCapacity{
			NativeRef:      lease.NativeRef,
			ConnectionID:   lease.ConnectionID,
			WorkspaceID:    lease.WorkspaceID,
			RentalID:       lease.RentalID,
			Generation:     lease.Generation,
			OwnershipToken: lease.OwnershipToken,
			State:          world.capacityStateOf(lease),
			CreatedAt:      lease.AcceptedAt,
		})
	}
	world.recordCapacityEffect(OperationCapacityListOwned, "", "", query.WorkspaceID, EffectCommandAccepted,
		map[string]any{"workspace_id": query.WorkspaceID}, map[string]any{"rental_ids": ownedRentalIDs(owned)})
	return owned, nil
}

// capacityStateOf is how far this world has got with one machine, spent from the
// moment its allocation was accepted. Acquisition puts it in starting and boot
// puts it in active, and nothing after that moves this answer, because the agent
// arriving is past the end of what a provider can see.
func (world *simulatedWorld) capacityStateOf(lease *capacityLease) capability.CapacityState {
	if lease.Terminated {
		return capability.CapacityStateTerminated
	}
	spend := world.truth[lease.OfferID].provisioning
	switch elapsed := world.now.Sub(lease.AcceptedAt); {
	case elapsed < spend.AcquisitionSpend():
		return capability.CapacityStateRequested
	case elapsed < spend.AcquisitionSpend()+spend.BootSpend():
		return capability.CapacityStateStarting
	default:
		return capability.CapacityStateActive
	}
}

// Invite reserves the node identity a machine allocated for this Rental
// generation will enrol under, and mints the material it redeems. The Lab is its
// own registry here for the reason it is its own provider: what a Blueprint
// states about a bootstrap has to be something this world performs rather than
// something a second component performs beside it.
func (world *simulatedWorld) Invite(_ context.Context, invitation node.Invitation) (capability.NodeBootstrap, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	if invitation.NodeID == "" || invitation.RentalID == "" {
		return capability.NodeBootstrap{}, fmt.Errorf("Lab invitation needs a node and a Rental")
	}
	if _, exists := world.invitations[invitation.NodeID]; exists {
		return capability.NodeBootstrap{}, fmt.Errorf("%w: %s", node.ErrIdentityExists, invitation.NodeID)
	}
	world.invitations[invitation.NodeID] = &labInvitation{
		NodeID:                invitation.NodeID,
		RentalID:              invitation.RentalID,
		Generation:            invitation.Generation,
		WorkspaceID:           invitation.WorkspaceID,
		ShadowPriceUSDPerHour: invitation.ShadowPriceUSDPerHour,
	}
	return world.bootstrapFor(invitation.NodeID), nil
}

// Reinvite mints a fresh token for an identity that already exists, which is
// what an allocation whose answer Mercator lost comes back through.
func (world *simulatedWorld) Reinvite(_ context.Context, _, nodeID string) (capability.NodeBootstrap, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	if _, exists := world.invitations[nodeID]; !exists {
		return capability.NodeBootstrap{}, fmt.Errorf("%w: %s", node.ErrNotFound, nodeID)
	}
	return world.bootstrapFor(nodeID), nil
}

// Enrolled reports whether the agent on this machine has opened its session.
// An identity nobody has heard from is not an error: a node invited and never
// filled is exactly the state provisioning waits in.
func (world *simulatedWorld) Enrolled(_ context.Context, ref capability.NodeRef) (bool, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	invitation, exists := world.invitations[ref.NodeID]
	return exists && invitation.Enrolled, nil
}

// labInvitation is one node identity this world reserved before any machine
// existed to fill it.
type labInvitation struct {
	NodeID                string
	RentalID              string
	Generation            uint64
	WorkspaceID           string
	ShadowPriceUSDPerHour float64
	Enrolled              bool
	Token                 string
	Spent                 bool
}

func (world *simulatedWorld) bootstrapFor(nodeID string) capability.NodeBootstrap {
	invitation := world.invitations[nodeID]
	invitation.Token = DeterministicID(world.seed, "enrollment", fmt.Sprintf("%s/%d/%d", nodeID, invitation.Generation, len(world.effects)))
	invitation.Spent = false
	return capability.NodeBootstrap{
		ControlPlaneURL: "https://lab.mercator.test",
		NodeID:          nodeID,
		RentalID:        invitation.RentalID,
		Generation:      invitation.Generation,
		EnrollmentToken: invitation.Token,
		AgentVersion:    "lab",
	}
}

// deliverEnrolments is every agent in this world whose machine has finished
// booting and waiting, opening its session for the first time. It is a call the
// machine makes rather than something the control plane reads, which is what
// makes an agent that never arrives a silence rather than a negative answer, and
// it writes node.enrolled for the reason a hand-enrolled Rental does: the ledger
// is the only account of which enrolment made a machine executable.
//
// A listing whose Blueprint says its agent never enrols is skipped for ever.
// Mercator has no session to that machine, so nothing can create a container
// there, and the record says the start was never observed because nobody was
// ever able to look.
func (world *simulatedWorld) deliverEnrolments() {
	world.mu.Lock()
	defer world.mu.Unlock()
	for _, rentalID := range slices.Sorted(maps.Keys(world.leases)) {
		lease := world.leases[rentalID]
		invitation, invited := world.invitations[lease.NodeID]
		if lease.Terminated || lease.Enrolled || !invited {
			continue
		}
		if world.truth[lease.OfferID].neverEnrolls || world.now.Before(lease.arrivesAt(world.truth[lease.OfferID].provisioning)) {
			continue
		}
		if invitation.Token != lease.Token || invitation.Spent {
			continue
		}
		lease.Enrolled = true
		invitation.Enrolled = true
		invitation.Spent = true
		world.recordEffect(
			OperationNodeEnrolled,
			fmt.Sprintf("%s/generation-%d", lease.NodeID, lease.Generation),
			EffectCommandAccepted,
			EffectResponseDelivered,
			lease.NativeRef,
			"enrolment",
			"",
			map[string]any{
				"machine_id": lease.NativeRef,
				"rental_id":  lease.RentalID,
				"node_id":    lease.NodeID,
				"generation": lease.Generation,
			},
			map[string]any{"node_id": lease.NodeID, "fencing_token": lease.Generation},
			"",
		)
	}
}

func (world *simulatedWorld) recordCapacityEffect(
	operation, operationKey, requestHash, correlationID string,
	command EffectCommand,
	request any,
	consequence any,
) {
	if operationKey == "" {
		operationKey = operation + "/" + correlationID
	}
	world.recordEffect(
		operation,
		operationKey,
		command,
		EffectResponseDelivered,
		correlationID,
		"capacity-lease",
		requestHash,
		request,
		consequence,
		"",
	)
}

// provisionRequestFacts is what a provision command says about the machine it
// asks for, with the bootstrap reduced to the identity it names. The enrollment
// token is deliberately not in it: the ledger is a record, and a record that
// carried a live credential would be a credential in every Run Bundle.
func provisionRequestFacts(command capability.ProvisionCommand) map[string]any {
	return map[string]any{
		"rental_id":         command.RentalID,
		"generation":        command.Generation,
		"offer_snapshot_id": command.OfferSnapshotID,
		"node_id":           command.Bootstrap.NodeID,
	}
}

func ownedRentalIDs(owned []capability.OwnedCapacity) []string {
	ids := make([]string, 0, len(owned))
	for _, held := range owned {
		ids = append(ids, held.RentalID)
	}
	return ids
}
