package lab

import (
	"context"
	"errors"
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
	// Unlisted is a machine this world allocated and has told Mercator nothing
	// about: the receipt was lost and the account listing does not name it either.
	// Both halves are one fact rather than two, because the fact is that Mercator
	// cannot get the answer by any route. A world that lost the receipt and listed
	// the machine would be a slow provider, and reconciling against the listing
	// would resolve it on the next look without anything ever being asked twice.
	Unlisted bool
	Enrolled bool
	// Refused is a machine whose agent presented its bootstrap and was turned
	// away. It holds one credential and can be told nothing more, so the refusal
	// is terminal for this machine rather than something it retries out of.
	Refused bool
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
// bootstrap it was handed, verbatim. The same lease twice allocates one machine
// and says so, which is what makes an allocation whose response was lost cost
// one repeat rather than one extra machine.
//
// That is a backend resolving duplicates below this seam, which is where a real
// adapter resolves them: a provider honouring no key of its own leaves the
// adapter to create, scan by the lease's tag, and destroy the losers, and what
// reaches this interface either way is one machine per lease. It is what
// "operation_key" declares, and compile refuses any listing declaring otherwise
// rather than letting a Blueprint state a contract this world does not keep.
//
// A world told to lose this answer allocates the machine all the same and tells
// Mercator nothing about it, by any route, until something asks about the lease
// again. That is the whole of what the Rental identity travelling to the
// provider exists to resolve: the repeat finds the machine already there and
// adopts it, carrying the bootstrap the first attempt put on it.
func (world *simulatedWorld) ProvisionCapacity(_ context.Context, command capability.ProvisionCommand) (capability.CapacityReceipt, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	if command.OperationKey == "" || command.RequestHash == "" {
		return capability.CapacityReceipt{}, fmt.Errorf("Lab provider provision needs operation key and request hash")
	}
	world.provisionCount[command.RentalID]++
	if lease, exists := world.leases[command.RentalID]; exists && !lease.Terminated {
		// Asking about this lease is what surfaces the machine a lost answer left.
		lease.Unlisted = false
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
	fault := world.matchOperationFault(OperationCapacityProvision, "", world.provisionCount[command.RentalID])
	if fault != nil && fault.Action == scenario.FaultLoseResponse {
		lease.Unlisted = true
		world.recordCapacityEffectAs(OperationCapacityProvision, command.OperationKey, command.RequestHash,
			command.RentalID, EffectCommandAccepted, EffectResponseLost, provisionRequestFacts(command), receipt, fault.ID)
		return capability.CapacityReceipt{}, fmt.Errorf(
			"%w: the Lab allocated a machine for Rental %q and the answer never came back",
			capability.ErrCapacityIndeterminate, command.RentalID,
		)
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
		if lease.Terminated || lease.Unlisted || (query.WorkspaceID != "" && query.WorkspaceID != lease.WorkspaceID) {
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

// labRegistry is Mercator's own node registry with this world watching what
// leaves it. The registry is control-plane code and not external behaviour, so
// the Lab runs the real one: a Blueprint that states what a machine holds is
// stating something about the component a deployment runs, and a copy of it here
// would be a specification of the copy.
//
// What the world adds is the account of what it saw leave. A credential is World
// Truth about material Mercator handed out, and the registry deliberately keeps
// none: it stores a digest, so nothing inside Mercator can tell a rule about a
// secret in the record which string to look for.
type labRegistry struct {
	*node.Registry
	world *simulatedWorld
	// answering is the moment of the inbound call from a machine this registry is
	// handling, and the zero time whenever it is answering the control plane
	// instead. An agent calls Mercator to enrol, so the registry dates the session
	// when it was opened; a registry that dated it from the sweep which noticed
	// would make how long a machine took to become usable a property of how often
	// Mercator looks, and would refuse a machine whose window closed between its
	// own arrival and the next tick.
	answering time.Time
}

func newLabRegistry(store node.Store, world *simulatedWorld) *labRegistry {
	registry := &labRegistry{world: world}
	registry.Registry = node.NewRegistry(
		store,
		node.NewSigner(node.DeriveKey([]byte("mercator-lab-node-key"))),
		"https://lab.mercator.test",
		node.WithClock(registry.clock),
		node.WithAgentVersion("lab"),
	)
	return registry
}

func (registry *labRegistry) clock() time.Time {
	if registry.answering.IsZero() {
		return registry.world.nowTime()
	}
	return registry.answering
}

// enrol is one machine presenting its bootstrap, dated at the moment the agent
// on it opened the session rather than at the tick this world got round to
// delivering the call.
func (registry *labRegistry) enrol(ctx context.Context, at time.Time, request capability.EnrollmentRequest) (capability.Enrollment, error) {
	registry.answering = at
	defer func() { registry.answering = time.Time{} }()
	return registry.Enroll(ctx, request)
}

func (registry *labRegistry) Invite(ctx context.Context, invitation node.Invitation) (capability.NodeBootstrap, error) {
	bootstrap, err := registry.Registry.Invite(ctx, invitation)
	if err != nil {
		return capability.NodeBootstrap{}, err
	}
	registry.world.notedInvitation(bootstrap)
	return bootstrap, nil
}

func (registry *labRegistry) Reinvite(ctx context.Context, workspaceID, nodeID string, redeemableThrough time.Time) (capability.NodeBootstrap, error) {
	bootstrap, err := registry.Registry.Reinvite(ctx, workspaceID, nodeID, redeemableThrough)
	if err != nil {
		return capability.NodeBootstrap{}, err
	}
	registry.world.notedInvitation(bootstrap)
	return bootstrap, nil
}

// notedInvitation records material Mercator minted. An invitation handed back
// rather than replaced is the same credential and is noted once, which is the
// difference the rule about single use is counting.
func (world *simulatedWorld) notedInvitation(bootstrap capability.NodeBootstrap) {
	world.mu.Lock()
	defer world.mu.Unlock()
	if _, known := world.credentials[bootstrap.EnrollmentToken]; known {
		return
	}
	world.credentials[bootstrap.EnrollmentToken] = &bootstrapCredential{
		NodeID:     bootstrap.NodeID,
		Generation: bootstrap.Generation,
		Token:      bootstrap.EnrollmentToken,
	}
}

// bootstrapCredential is one enrollment token Mercator minted and what became of
// it: how many accepted allocations carried it to a machine, and how many times
// a machine redeemed it. Both are counted rather than flagged, because the
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
	// Refused is material a machine presented and the registry would not take. It
	// is the answer the real registry gave rather than anything this world
	// decided, so it covers both doors that close on an invitation: one replaced
	// by a later mint, and one whose window ran out while the machine was still
	// booting. Either way the host is paid for and can enrol nowhere.
	Refused bool
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
// It is a real call to Mercator's own registry with the material the machine is
// holding, so what happens to an agent presenting an invitation nothing will
// take is what would happen to it in a deployment: the registry refuses it, the
// machine has nothing else to try, and this world records the refusal rather
// than deciding one.
func (world *simulatedWorld) deliverEnrolments(ctx context.Context, registry *labRegistry) error {
	for _, pending := range world.dueEnrolments() {
		enrolment, err := registry.enrol(ctx, pending.arrives, capability.EnrollmentRequest{
			NodeID:          pending.bootstrap.NodeID,
			RentalID:        pending.bootstrap.RentalID,
			Generation:      pending.bootstrap.Generation,
			EnrollmentToken: pending.bootstrap.EnrollmentToken,
			AgentVersion:    pending.bootstrap.AgentVersion,
			Facts:           capability.NodeFacts{Host: capability.HostFacts{OS: "linux", ContainerRuntime: "docker"}},
		})
		if err != nil {
			if err := world.recordRefusal(pending, err); err != nil {
				return err
			}
			continue
		}
		world.recordEnrolment(pending, enrolment)
	}
	return nil
}

// pendingEnrolment is one machine whose agent has finished booting, and the
// material it is about to present. The bootstrap is the machine's own copy, so
// an agent enrols as whoever Mercator wrote onto it rather than as whoever the
// lease says it should be.
type pendingEnrolment struct {
	rentalID  string
	nativeRef string
	arrives   time.Time
	bootstrap capability.NodeBootstrap
}

// dueEnrolments is read under the lock and answered outside it, because what
// happens next is a call into the control plane and the control plane reads this
// world's clock.
//
// They are delivered in the order the agents arrived. Two machines holding the
// same invitation is a state a rule here is about, and which of them opens the
// session is the one that got there first rather than whichever lease sorts
// earliest.
func (world *simulatedWorld) dueEnrolments() []pendingEnrolment {
	world.mu.Lock()
	defer world.mu.Unlock()
	var due []pendingEnrolment
	for _, rentalID := range slices.Sorted(maps.Keys(world.leases)) {
		lease := world.leases[rentalID]
		if lease.Terminated || lease.Enrolled || lease.Refused || lease.Bootstrap.NodeID == "" {
			continue
		}
		arrives := lease.arrivesAt(world.truth[lease.OfferID].provisioning)
		if world.truth[lease.OfferID].neverEnrolls || world.now.Before(arrives) {
			continue
		}
		due = append(due, pendingEnrolment{
			rentalID:  rentalID,
			nativeRef: lease.NativeRef,
			arrives:   arrives,
			bootstrap: lease.Bootstrap,
		})
	}
	slices.SortStableFunc(due, func(left, right pendingEnrolment) int {
		return left.arrives.Compare(right.arrives)
	})
	return due
}

func (world *simulatedWorld) recordEnrolment(pending pendingEnrolment, enrolment capability.Enrollment) {
	world.mu.Lock()
	defer world.mu.Unlock()
	lease := world.leases[pending.rentalID]
	lease.Enrolled = true
	lease.SessionExpires = enrolment.SessionExpires
	world.credentials[pending.bootstrap.EnrollmentToken].Redemptions++
	redeemed := pending.bootstrap
	world.recordEffect(
		OperationNodeEnrolled,
		fmt.Sprintf("%s/generation-%d", redeemed.NodeID, redeemed.Generation),
		EffectCommandAccepted,
		EffectResponseDelivered,
		pending.nativeRef,
		"enrolment",
		"",
		map[string]any{
			"machine_id": pending.nativeRef,
			"rental_id":  redeemed.RentalID,
			"node_id":    redeemed.NodeID,
			"generation": redeemed.Generation,
		},
		map[string]any{"node_id": redeemed.NodeID, "fencing_token": enrolment.FencingToken},
		"",
	)
}

// recordRefusal is a paid machine turned away at the one door it has. The two
// refusals a bootstrap can meet are the ones a rule about locked-out hosts is
// for: material the record no longer names, and material whose window closed.
// Anything else is this world doing something no machine does, and it stops the
// execution rather than being filed as a machine's misfortune.
//
// The machine is not asked again. It holds one bootstrap and can be told
// nothing more, so an agent refused once is an agent refused for ever, and a
// world that retried every tick would fill the ledger with the same rejection.
func (world *simulatedWorld) recordRefusal(pending pendingEnrolment, refusal error) error {
	if !errors.Is(refusal, node.ErrEnrollmentInvalid) && !errors.Is(refusal, node.ErrEnrollmentSpent) {
		return fmt.Errorf("Lab agent on Rental %q could not enrol: %w", pending.rentalID, refusal)
	}
	world.mu.Lock()
	defer world.mu.Unlock()
	world.leases[pending.rentalID].Refused = true
	world.credentials[pending.bootstrap.EnrollmentToken].Refused = true
	refused := pending.bootstrap
	world.recordEffect(
		OperationNodeEnrolled,
		fmt.Sprintf("%s/generation-%d", refused.NodeID, refused.Generation),
		EffectCommandRejected,
		EffectResponseDelivered,
		pending.nativeRef,
		"enrolment",
		"",
		map[string]any{
			"machine_id": pending.nativeRef,
			"rental_id":  refused.RentalID,
			"node_id":    refused.NodeID,
			"generation": refused.Generation,
		},
		map[string]any{"node_id": refused.NodeID, "refusal": refusal.Error()},
		"",
	)
	return nil
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
// A machine this world destroyed renews nothing, and neither does one whose agent
// never arrived. There is nobody on either to hold a session, and an agent that
// went on renewing against a lease Mercator handed back would be a session to a
// machine that no longer exists.
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
	world.recordCapacityEffectAs(operation, operationKey, requestHash, correlationID,
		command, EffectResponseDelivered, request, consequence, "")
}

func (world *simulatedWorld) recordCapacityEffectAs(
	operation, operationKey, requestHash, correlationID string,
	command EffectCommand,
	response EffectResponse,
	request any,
	consequence any,
	faultID string,
) {
	if operationKey == "" {
		operationKey = operation + "/" + correlationID
	}
	world.recordEffect(
		operation,
		operationKey,
		command,
		response,
		correlationID,
		"capacity-lease",
		requestHash,
		request,
		consequence,
		faultID,
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
