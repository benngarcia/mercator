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
	RentalID   string
	Generation uint64
	// Bootstrap is the material handed to the machine, verbatim and entire. It is
	// held rather than reduced to a token because it is the whole of what the
	// agent on this machine knows about itself: the identity it enrols under, the
	// lease and generation it names doing so, and the credential it redeems. A
	// world that kept only the token would have to reconstruct the other three
	// from what Mercator asked the provider for, and then no enrolment could ever
	// disagree with the provision that carried it. It never leaves this world.
	Bootstrap      capability.NodeBootstrap
	OfferID        string
	NativeRef      string
	WorkspaceID    string
	ConnectionID   string
	OwnershipToken string
	AcceptedAt     time.Time
	Terminated     bool
	TerminatedAt   time.Time
	Enrolled       bool
	// SessionExpires is when the credential the agent on this machine currently
	// holds stops authenticating anything. It is held per machine rather than per
	// invitation because it outlives the invitation: the token that opened the
	// session is spent the moment it is redeemed, and what keeps the machine
	// working after that is a credential nothing was invited with.
	SessionExpires time.Time
}

// leaseState is what this world still holds for one Rental: whether the machine
// is there at all, and whether an agent ever opened a session on it. It answers
// with a copy, because the lease is this world's own record of what happened to
// a machine and a caller holding the pointer could rewrite it.
func (world *simulatedWorld) leaseState(rentalID string) (capacityLease, bool) {
	world.mu.Lock()
	defer world.mu.Unlock()
	lease, exists := world.leases[rentalID]
	if !exists {
		return capacityLease{}, false
	}
	return *lease, true
}

// capacityProgress is how far this world has got with one machine and the moment
// it got there. The two travel together because a provider that reports a state
// without dating it leaves its caller measuring the interval between two looks
// and calling that the machine's own spend.
type capacityProgress struct {
	state capability.CapacityState
	since time.Time
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
			State:      world.capacityProgressOf(lease).state,
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
		Generation:     command.Generation,
		Bootstrap:      command.Bootstrap,
		OfferID:        command.OfferSnapshotID,
		NativeRef:      "lab-machine-" + command.RentalID,
		WorkspaceID:    command.WorkspaceID,
		ConnectionID:   labConnection,
		OwnershipToken: command.OwnershipToken,
		AcceptedAt:     world.now,
	}
	world.leases[command.RentalID] = lease
	// The bootstrap really arrived on a machine, which is the fact a rule about
	// single use reads. It is counted here and not at the invitation, because
	// minting a credential hands it to nobody: what makes a bootstrap dangerous is
	// a machine holding it, and one held by two machines is one invitation two
	// hosts can enrol as.
	if credential, minted := world.credentials[command.Bootstrap.EnrollmentToken]; minted {
		credential.Provisions++
	}
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
	progress := world.capacityProgressOf(lease)
	observation := capability.CapacityObservation{
		NativeRef:  lease.NativeRef,
		State:      progress.state,
		ObservedAt: world.now,
		StateSince: progress.since,
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
	} else {
		// The moment the machine was destroyed, which a repeat of the same command
		// does not move: the bill ended once.
		lease.TerminatedAt = world.now
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
			State:          world.capacityProgressOf(lease).state,
			CreatedAt:      lease.AcceptedAt,
		})
	}
	world.recordCapacityEffect(OperationCapacityListOwned, "", "", query.WorkspaceID, EffectCommandAccepted,
		map[string]any{"workspace_id": query.WorkspaceID}, map[string]any{"rental_ids": ownedRentalIDs(owned)})
	return owned, nil
}

// capacityProgressOf is how far this world has got with one machine, spent from
// the moment its allocation was accepted, and when it got there. Acquisition puts
// it in starting and boot puts it in active, and nothing after that moves this
// answer, because the agent arriving is past the end of what a provider can see.
//
// The moment is a provider fact and is answered as one. A machine really did
// finish booting at a moment of its own, and a provider that knows it and reports
// only its current state forces every reader to date the transition from its own
// next look.
func (world *simulatedWorld) capacityProgressOf(lease *capacityLease) capacityProgress {
	if lease.Terminated {
		return capacityProgress{state: capability.CapacityStateTerminated, since: lease.TerminatedAt}
	}
	spend := world.truth[lease.OfferID].provisioning
	acquired := lease.AcceptedAt.Add(spend.AcquisitionSpend())
	booted := acquired.Add(spend.BootSpend())
	switch {
	case world.now.Before(acquired):
		return capacityProgress{state: capability.CapacityStateRequested, since: lease.AcceptedAt}
	case world.now.Before(booted):
		return capacityProgress{state: capability.CapacityStateStarting, since: acquired}
	default:
		return capacityProgress{state: capability.CapacityStateActive, since: booted}
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

// EnrolledAt is when the agent on this machine opened its session, and the zero
// time while none has. An identity nobody has heard from is not an error: a node
// invited and never filled is exactly the state provisioning waits in.
//
// A question about a generation this identity is not on is an error, exactly as
// node.Registry makes it one. The registry answers about a node and a generation
// together, because that pair is what every act against a machine is addressed
// to, and a world that answered "enrolled and healthy" to a question about the
// wrong generation would report a machine ready to launch on where the real
// deployment cannot make progress at all.
func (world *simulatedWorld) EnrolledAt(_ context.Context, ref capability.NodeRef) (time.Time, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	invitation, exists := world.invitations[ref.NodeID]
	if !exists {
		return time.Time{}, nil
	}
	if ref.Generation != 0 && invitation.Generation != ref.Generation {
		return time.Time{}, fmt.Errorf("node: %q is generation %d, not %d", ref.NodeID, invitation.Generation, ref.Generation)
	}
	return invitation.EnrolledAt, nil
}

// labInvitation is one node identity this world reserved before any machine
// existed to fill it.
type labInvitation struct {
	NodeID                string
	RentalID              string
	Generation            uint64
	WorkspaceID           string
	ShadowPriceUSDPerHour float64
	// EnrolledAt is the moment the agent redeemed this invitation, which is the
	// moment its machine's whole provisioning ended. It is the agent's arrival and
	// not the sweep that noticed it: the registry is called by the machine, so it
	// knows when the session was opened, and a world that dated it from the next
	// look would make a stage's duration a property of how often Mercator asks.
	EnrolledAt time.Time
	Enrolled   bool
	// Token is the credential this identity's current invitation hands out. An
	// earlier one is not forgotten when a fresh invitation supersedes it: what
	// became of every credential this world ever minted is kept beside the
	// invitations, because a rule about single use has to be able to read a token
	// nobody is offering any more.
	Token string
}

// bootstrapCredential is one enrollment token this world minted and what became
// of it: how many accepted allocations carried it to a machine, and how many
// times a machine redeemed it. Both are counted rather than flagged, because the
// violations they exist to catch are second occurrences and a flag cannot tell a
// second from a first.
//
// It is World Truth and never Mercator's record. The world knows what it handed
// each machine, and the whole of what a rule can then ask is whether Mercator's
// own account of events ever contains it.
type bootstrapCredential struct {
	NodeID      string
	Generation  uint64
	Token       string
	Provisions  int
	Redemptions int
}

func (world *simulatedWorld) bootstrapFor(nodeID string) capability.NodeBootstrap {
	invitation := world.invitations[nodeID]
	invitation.Token = DeterministicID(world.seed, "enrollment", fmt.Sprintf("%s/%d/%d", nodeID, invitation.Generation, len(world.effects)))
	world.credentials[invitation.Token] = &bootstrapCredential{
		NodeID:     nodeID,
		Generation: invitation.Generation,
		Token:      invitation.Token,
	}
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
//
// What the record says the session was opened under is read off the bootstrap
// the machine holds and never off the lease it was allocated against. Those are
// two different facts of Mercator's making, and the whole point of writing the
// first is that the two can disagree: a control plane that provisions under one
// generation and mints the token under another produces a machine whose agent
// enrols as somebody the provider is not holding a machine for. Recording the
// lease's own generation here would be this world copying the provision into the
// enrolment and then agreeing with itself.
func (world *simulatedWorld) deliverEnrolments() {
	world.mu.Lock()
	defer world.mu.Unlock()
	for _, rentalID := range slices.Sorted(maps.Keys(world.leases)) {
		lease := world.leases[rentalID]
		invitation, invited := world.invitations[lease.Bootstrap.NodeID]
		if lease.Terminated || lease.Enrolled || !invited {
			continue
		}
		arrives := lease.arrivesAt(world.truth[lease.OfferID].provisioning)
		if world.truth[lease.OfferID].neverEnrolls || world.now.Before(arrives) {
			continue
		}
		credential, minted := world.credentials[lease.Bootstrap.EnrollmentToken]
		if !minted || invitation.Token != lease.Bootstrap.EnrollmentToken || credential.Redemptions > 0 {
			continue
		}
		lease.Enrolled = true
		lease.SessionExpires = arrives.Add(node.DefaultSession)
		invitation.Enrolled = true
		invitation.EnrolledAt = arrives
		credential.Redemptions++
		redeemed := lease.Bootstrap
		world.recordEffect(
			OperationNodeEnrolled,
			fmt.Sprintf("%s/generation-%d", redeemed.NodeID, redeemed.Generation),
			EffectCommandAccepted,
			EffectResponseDelivered,
			lease.NativeRef,
			"enrolment",
			"",
			map[string]any{
				"machine_id": lease.NativeRef,
				"rental_id":  redeemed.RentalID,
				"node_id":    redeemed.NodeID,
				"generation": redeemed.Generation,
			},
			map[string]any{"node_id": redeemed.NodeID, "fencing_token": redeemed.Generation},
			"",
		)
	}
}

// sessionRenewalMargin is how far ahead of expiry an agent in this world takes a
// fresh credential. An agent that waited for the credential it holds to lapse
// would be renewing nothing: it would have no session to renew with, and the only
// material left to it would be the invitation it joined on, which is spent.
//
// A tenth of the session is enough to be clearly ahead of the lapse and small
// enough that a Blueprint whose Run outlives one session really does record a
// renewal rather than a machine renewing on the tick it enrolled.
const sessionRenewalMargin = node.DefaultSession / 10

// renewSessions is every agent in this world whose session credential is about to
// lapse taking a fresh one. It is the act that makes a machine usable for longer
// than one session, and it is recorded as an operation of its own because it is
// one: nothing is redeemed, no fencing token moves, and the machine is the same
// machine it was a moment before.
//
// A retired or destroyed machine renews nothing. The credential outlives neither,
// and an agent that went on renewing against a lease Mercator gave up would be
// holding a session to a machine nobody is paying for.
func (world *simulatedWorld) renewSessions() {
	world.mu.Lock()
	defer world.mu.Unlock()
	for _, rentalID := range slices.Sorted(maps.Keys(world.leases)) {
		lease := world.leases[rentalID]
		if lease.Terminated || !lease.Enrolled {
			continue
		}
		if world.now.Before(lease.SessionExpires.Add(-sessionRenewalMargin)) {
			continue
		}
		lease.SessionExpires = world.now.Add(node.DefaultSession)
		world.recordEffect(
			OperationNodeSessionRenewed,
			fmt.Sprintf("%s/session-until-%d", lease.Bootstrap.NodeID, lease.SessionExpires.Unix()),
			EffectCommandAccepted,
			EffectResponseDelivered,
			lease.NativeRef,
			"node-session",
			"",
			map[string]any{
				"machine_id": lease.NativeRef,
				"rental_id":  lease.Bootstrap.RentalID,
				"node_id":    lease.Bootstrap.NodeID,
				"generation": lease.Bootstrap.Generation,
			},
			map[string]any{
				"node_id":         lease.Bootstrap.NodeID,
				"session_expires": lease.SessionExpires,
			},
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
