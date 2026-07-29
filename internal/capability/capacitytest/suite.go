// Package capacitytest is the bounded set of promises every CapacityProvider
// keeps, stated once and run against any backend that implements the contract.
//
// It is the higher-fidelity half of what the Lab says about capacity. The Lab
// states what the control plane must do with a provider's answers; this states
// what a provider's answers must be. Every promise names the Lab rule it is the
// other half of, so a failure here can be read against the rule it breaks
// rather than against one adapter's code.
//
// The suite is bounded on purpose. Each promise rents at most one machine and
// gives it back before it returns, and none of them asserts how long a machine
// takes to arrive or whether one is in stock right now: what it establishes is
// that a backend keeps the contract, which is a fact about the contract rather
// than about the weather.
package capacitytest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// The Lab rules each promise is the other half of. They are cited by ID so a
// reader who watches a promise fail can find the rule that reads the same fact
// from the ledger.
//
// RuleNegotiatedLifecycle is not registered in the Lab, deliberately: nothing in
// the tree stops or resumes a machine yet, so a rule policing an act no path
// performs could only fail against a hand-written observation. Its production
// half is the seam, broker.Backend.CapacityFor, which refuses an operation a
// connection never claimed before any request is sent, and this suite is what
// holds the provider's own side of that refusal.
const (
	RuleNegotiatedLifecycle = "safety.capacity_lifecycle_is_negotiated"
	RuleIdempotentCommands  = "safety.idempotent_external_commands"
	RuleLostResponse        = "liveness.lost_response_reconciliation"
	RuleEnrolsOrIsReclaimed = "liveness.provisioned_capacity_enrolls_or_is_reclaimed"
)

// ErrNotApplicable is a promise this provider's negotiated set puts out of
// reach. It is a skip rather than a failure: a provider that promises no
// owned-capacity listing is not breaking the contract by having none, and a
// suite that reported it green would be claiming to have checked something it
// could not look at.
var ErrNotApplicable = errors.New("capacitytest: this promise is out of reach for the negotiated set")

// Origin is where one machine comes from, in the two forms a backend can be told
// about a listing: the snapshot Mercator selected, and the provider's own handle
// for what that snapshot sells.
type Origin struct {
	OfferSnapshotID string
	NativeRef       string
}

// Lease is the identity every machine this suite rents is held under. It is the
// caller's rather than the suite's because a trial has to be able to find its own
// machines afterwards: the workspace is what an owned-capacity listing is scoped
// to, and the trial ID is what keeps two runs of this suite from adopting each
// other's leases.
type Lease struct {
	TrialID      string
	WorkspaceID  string
	ConnectionID string
	// ControlPlaneURL is the origin written into every bootstrap this suite hands
	// a machine.
	ControlPlaneURL string
	AgentVersion    string
	// EnrollmentToken is the material a rented machine would join with. Nothing
	// this suite rents is expected to enrol, so the token it carries is one the
	// control plane will refuse: a machine that boots, tries to join, and is
	// turned away costs the trial nothing, and a token that worked would be a
	// credential left on a host for no reason.
	EnrollmentToken string
	// MaxLifetime is the provider-side reclamation backstop asked of every
	// machine, so a trial that dies between renting and giving back cannot bill
	// for ever.
	MaxLifetime time.Duration
}

// Subject is one backend under test, with the two things the suite cannot
// invent: which listing a machine can be rented from, and the identity Mercator
// would rent it under.
type Subject struct {
	Name     string
	Provider capability.CapacityProvider
	Lease    Lease
	// Capacity answers where one machine comes from. It is a call rather than a
	// value because a live catalog is a request, and the listing a promise acts on
	// has to be the one the provider is selling now.
	Capacity func(ctx context.Context) (Origin, error)
}

// Promise is one thing every CapacityProvider does. Keep returns nil when the
// backend kept it, ErrNotApplicable when the negotiated set puts it out of
// reach, and an error naming the broken promise otherwise.
type Promise struct {
	Name string
	// Rule is the Lab rule this promise is the higher-fidelity half of.
	Rule string
	Keep func(ctx context.Context, subject Subject) error
}

// Promises is the whole suite, in the order a trial runs it. The last promise
// reads what the connection owns after everything before it has finished, so
// running them in order is also a sweep over the trial itself.
func Promises() []Promise {
	return []Promise{
		{
			Name: "listed_capacity_is_capacity_to_acquire",
			Rule: RuleNegotiatedLifecycle,
			Keep: listedCapacityIsCapacityToAcquire,
		},
		{
			Name: "the_negotiated_set_is_one_a_provider_could_keep",
			Rule: RuleNegotiatedLifecycle,
			Keep: theNegotiatedSetIsOneAProviderCouldKeep,
		},
		{
			Name: "a_credential_check_allocates_nothing",
			Rule: RuleNegotiatedLifecycle,
			Keep: aCredentialCheckAllocatesNothing,
		},
		{
			Name: "one_provision_command_produces_one_machine",
			Rule: RuleIdempotentCommands,
			Keep: oneProvisionCommandProducesOneMachine,
		},
		{
			Name: "a_lost_answer_costs_no_second_machine",
			Rule: RuleLostResponse,
			Keep: aLostAnswerCostsNoSecondMachine,
		},
		{
			Name: "terminate_is_confirmed_and_stays_confirmed",
			Rule: RuleEnrolsOrIsReclaimed,
			Keep: terminateIsConfirmedAndStaysConfirmed,
		},
		{
			Name: "an_operation_the_provider_never_promised_is_refused",
			Rule: RuleNegotiatedLifecycle,
			Keep: anOperationTheProviderNeverPromisedIsRefused,
		},
		{
			Name: "a_trial_leaves_nothing_owned",
			Rule: RuleEnrolsOrIsReclaimed,
			Keep: aTrialLeavesNothingOwned,
		},
	}
}

// Affordable is the capacity a bounded trial rents: the cheapest listing this
// connection is selling right now whose rate over the whole trial stays inside
// the declared maximum.
//
// The choice is here rather than in each caller because it is part of what makes
// a trial bounded. A trial that rented whatever the catalog listed first would
// bill whatever that machine costs, and a trial that ignored availability would
// spend its timeout waiting for a region that is sold out.
func Affordable(
	ctx context.Context,
	provider capability.CapacityProvider,
	query capability.CapacityQuery,
	maxCostUSD float64,
	timeout time.Duration,
) (domain.OfferSnapshot, error) {
	listings, err := provider.ListCapacity(ctx, query)
	if err != nil {
		return domain.OfferSnapshot{}, fmt.Errorf("list capacity: %w", err)
	}
	var chosen domain.OfferSnapshot
	var found bool
	for _, listing := range listings {
		cost := listing.Pricing.RatePerSecondUSD * timeout.Seconds()
		switch {
		case !listing.Capacity.Available,
			!listing.Pricing.Known,
			listing.Pricing.Currency != "USD",
			cost > maxCostUSD:
			continue
		case !found || listing.Pricing.RatePerSecondUSD < chosen.Pricing.RatePerSecondUSD:
			chosen, found = listing, true
		}
	}
	if !found {
		return domain.OfferSnapshot{}, fmt.Errorf(
			"none of the %d listings on sale is available at a known USD rate under %.4f over %s",
			len(listings), maxCostUSD, timeout,
		)
	}
	return chosen, nil
}

// OriginOf names one listing the way a provision command names it.
func OriginOf(listing domain.OfferSnapshot) Origin {
	return Origin{OfferSnapshotID: listing.ID, NativeRef: listing.NativeRef}
}

// listedCapacityIsCapacityToAcquire holds what a capacity listing is. Every
// entry is a machine to rent rather than one this connection is already holding,
// it names a handle the provider will answer a provision command about, and it
// carries no Rental identity, because a lease is Mercator's own record and a
// listing is a product anybody could buy.
func listedCapacityIsCapacityToAcquire(ctx context.Context, subject Subject) error {
	if err := subject.check(); err != nil {
		return err
	}
	support := subject.Provider.CapacitySupport()
	listings, err := subject.Provider.ListCapacity(ctx, capability.CapacityQuery{WorkspaceID: subject.Lease.WorkspaceID})
	if err != nil {
		return fmt.Errorf("list capacity: %w", err)
	}
	if len(listings) == 0 {
		return errors.New("this connection sells no capacity at all, so nothing here can be rented")
	}
	for _, listing := range listings {
		if listing.Kind != domain.OfferKindProvisionable {
			return fmt.Errorf("listing %q is %q, and a capacity listing is capacity to acquire", listing.ID, listing.Kind)
		}
		if listing.NativeRef == "" {
			return fmt.Errorf("listing %q names no provider handle, so no provision command could name what to rent", listing.ID)
		}
		if listing.RentalID != "" {
			return fmt.Errorf("listing %q claims Rental %q, and only Mercator holds a lease", listing.ID, listing.RentalID)
		}
		if support.ExactPricing && (!listing.Pricing.Known || listing.Pricing.Currency != "USD") {
			return fmt.Errorf(
				"this provider states exact pricing and listing %q prices at %+v, which nothing can be billed against",
				listing.ID, listing.Pricing,
			)
		}
	}
	return nil
}

// theNegotiatedSetIsOneAProviderCouldKeep is the set read where a caller reads
// it. A scheduler acts on these claims without asking again, so a set that
// contradicts itself is refused here rather than by whichever caller happens to
// read two of its fields together.
func theNegotiatedSetIsOneAProviderCouldKeep(_ context.Context, subject Subject) error {
	if err := subject.check(); err != nil {
		return err
	}
	support := subject.Provider.CapacitySupport()
	if err := support.Validate(); err != nil {
		return fmt.Errorf("negotiated set %+v is one no provider could keep: %w", support, err)
	}
	for _, floor := range []capability.CapacityOperation{
		capability.CapacityProvision,
		capability.CapacityObserve,
		capability.CapacityTerminate,
	} {
		if !support.Claims(floor) {
			return fmt.Errorf("the set does not claim %q, which is the floor of holding a machine at all", floor)
		}
	}
	return nil
}

// aCredentialCheckAllocatesNothing is the promise Verify makes: it is cheap, it
// answers, and a deployment that runs it on every connection at startup has
// rented nothing by doing so.
func aCredentialCheckAllocatesNothing(ctx context.Context, subject Subject) error {
	if err := subject.check(); err != nil {
		return err
	}
	before, err := subject.owned(ctx)
	if err != nil {
		return err
	}
	if err := subject.Provider.Verify(ctx); err != nil {
		return fmt.Errorf("verify this connection's credential: %w", err)
	}
	after, err := subject.owned(ctx)
	if err != nil {
		return err
	}
	if len(after) != len(before) {
		return fmt.Errorf("a credential check took this connection from %d owned machines to %d", len(before), len(after))
	}
	return nil
}

// oneProvisionCommandProducesOneMachine is the whole of provision idempotency,
// asked without caring which mechanism keeps it. A provider that deduplicates on
// the operation key and a provider that reconciles against the Rental's own
// identity both owe the same answer: the same command twice is one machine, and
// the second answer says so.
func oneProvisionCommandProducesOneMachine(ctx context.Context, subject Subject) (err error) {
	command, first, err := subject.rent(ctx, "idempotent")
	// The machine comes back before the promise is reported, and the defer is
	// registered before the renting is judged: a receipt this suite refuses is
	// still a machine somebody is billed for.
	defer func() { err = errors.Join(err, subject.giveBack(ctx, command, first.NativeRef)) }()
	if err != nil {
		return err
	}

	if first.Duplicate {
		return fmt.Errorf("the first provision of Rental %q reported a duplicate of nothing", command.RentalID)
	}
	repeat, err := subject.Provider.ProvisionCapacity(ctx, command)
	if err != nil {
		return fmt.Errorf("repeat the provision of Rental %q: %w", command.RentalID, err)
	}
	if repeat.NativeRef != first.NativeRef {
		return fmt.Errorf(
			"one provision command produced machine %q and then machine %q, and Rental %q is now billing for two",
			first.NativeRef, repeat.NativeRef, command.RentalID,
		)
	}
	if !repeat.Duplicate {
		return fmt.Errorf(
			"the repeated provision of Rental %q reported machine %q as freshly allocated, so a caller would count the effect twice",
			command.RentalID, repeat.NativeRef,
		)
	}
	if repeat.AcceptedAt.IsZero() {
		return fmt.Errorf("the repeated provision of Rental %q is dated nowhere, so no stage can be measured from it", command.RentalID)
	}
	return nil
}

// aLostAnswerCostsNoSecondMachine is the answer that never came back. Mercator
// assigned the Rental identity before the provider answered precisely so this
// case has something to ask about, and what resolves it is reading what the
// connection owns rather than sending the command again.
func aLostAnswerCostsNoSecondMachine(ctx context.Context, subject Subject) (err error) {
	command, receipt, err := subject.rent(ctx, "lost-answer")
	// The machine comes back before the promise is reported, and the defer is
	// registered before the renting is judged: a receipt this suite refuses is
	// still a machine somebody is billed for.
	defer func() { err = errors.Join(err, subject.giveBack(ctx, command, receipt.NativeRef)) }()
	if err != nil {
		return err
	}

	support := subject.Provider.CapacitySupport()
	if !support.ListOwned {
		return fmt.Errorf(
			"%w: this provider enumerates nothing it owns, so a lost answer is reconciled by repeating the operation key instead",
			ErrNotApplicable,
		)
	}
	owned, err := subject.owned(ctx)
	if err != nil {
		return err
	}
	held := heldFor(owned, command.RentalID)
	if len(held) != 1 {
		return fmt.Errorf(
			"the connection reports %d machines for Rental %q, and a reconciler reading that cannot tell which machine this lease is",
			len(held), command.RentalID,
		)
	}
	machine := held[0]
	switch {
	case machine.NativeRef != receipt.NativeRef:
		return fmt.Errorf("Rental %q was accepted as machine %q and is owned as %q", command.RentalID, receipt.NativeRef, machine.NativeRef)
	case machine.WorkspaceID != command.WorkspaceID:
		return fmt.Errorf("machine %q is owned by workspace %q, and it was rented for %q", machine.NativeRef, machine.WorkspaceID, command.WorkspaceID)
	case machine.OwnershipToken != command.OwnershipToken:
		return fmt.Errorf("machine %q carries ownership token %q, and a reconciler acting on it would be acting on somebody else's machine", machine.NativeRef, machine.OwnershipToken)
	case machine.Generation != command.Generation:
		return fmt.Errorf("machine %q is owned at generation %d and was rented at %d", machine.NativeRef, machine.Generation, command.Generation)
	}
	observation, err := subject.Provider.ObserveCapacity(ctx, ref(command, receipt.NativeRef))
	if err != nil {
		return fmt.Errorf("observe the machine the listing says this connection holds: %w", err)
	}
	if !observation.State.Valid() || observation.State.Terminal() {
		return fmt.Errorf("machine %q is owned and observes as %q, so the listing and the observation disagree about a live machine", receipt.NativeRef, observation.State)
	}
	return nil
}

// terminateIsConfirmedAndStaysConfirmed is how Mercator learns a machine stopped
// costing money. The command is confirmed rather than accepted, a repeat says the
// bill ended once rather than twice, and what a later observation is allowed to
// say depends on what the provider promised about observing a machine it
// destroyed.
func terminateIsConfirmedAndStaysConfirmed(ctx context.Context, subject Subject) (err error) {
	command, receipt, err := subject.rent(ctx, "terminate")
	// The machine comes back before the promise is reported, and the defer is
	// registered before the renting is judged: a receipt this suite refuses is
	// still a machine somebody is billed for.
	defer func() { err = errors.Join(err, subject.giveBack(ctx, command, receipt.NativeRef)) }()
	if err != nil {
		return err
	}

	destroy := mutate(command, receipt.NativeRef, capability.CapacityTerminate)
	confirmed, err := subject.Provider.TerminateCapacity(ctx, destroy)
	if err != nil {
		return fmt.Errorf("terminate machine %q: %w", receipt.NativeRef, err)
	}
	if !confirmed.State.Terminal() {
		return fmt.Errorf("terminating machine %q was answered with state %q rather than a confirmation", receipt.NativeRef, confirmed.State)
	}
	if confirmed.Duplicate {
		return fmt.Errorf("the first terminate of machine %q reported a repeat of a destruction nothing performed", receipt.NativeRef)
	}
	repeat, err := subject.Provider.TerminateCapacity(ctx, destroy)
	if err != nil {
		return fmt.Errorf("repeat the terminate of machine %q: %w", receipt.NativeRef, err)
	}
	if !repeat.State.Terminal() || !repeat.Duplicate {
		return fmt.Errorf(
			"the repeated terminate of machine %q answered %q duplicate=%t, and a reader would count two machines ending",
			receipt.NativeRef, repeat.State, repeat.Duplicate,
		)
	}
	return observationAfterTerminate(ctx, subject, command, receipt.NativeRef)
}

// observationAfterTerminate reads the one thing the negotiated set decides. A
// provider that promised a destroyed machine stays observable owes an answer,
// and a provider that promised nothing of the kind may have no record left, so
// its silence is the confirmation. What neither of them may say is that the
// machine is still running.
func observationAfterTerminate(ctx context.Context, subject Subject, command capability.ProvisionCommand, nativeRef string) error {
	support := subject.Provider.CapacitySupport()
	observation, err := subject.Provider.ObserveCapacity(ctx, ref(command, nativeRef))
	if err != nil {
		if support.ObserveAfterTerminate {
			return fmt.Errorf(
				"this provider promises a destroyed machine stays observable, and observing %q after terminate failed: %w",
				nativeRef, err,
			)
		}
		return nil
	}
	if !observation.State.Terminal() {
		return fmt.Errorf("machine %q was terminated and observes as %q", nativeRef, observation.State)
	}
	return nil
}

// anOperationTheProviderNeverPromisedIsRefused is the negotiated set held
// against the provider that stated it. A caller that sends a suspend to a
// provider which cannot suspend is told so; the alternative is a machine that
// went on billing while the control plane recorded it as stopped.
func anOperationTheProviderNeverPromisedIsRefused(ctx context.Context, subject Subject) (err error) {
	command, receipt, err := subject.rent(ctx, "negotiated")
	// The machine comes back before the promise is reported, and the defer is
	// registered before the renting is judged: a receipt this suite refuses is
	// still a machine somebody is billed for.
	defer func() { err = errors.Join(err, subject.giveBack(ctx, command, receipt.NativeRef)) }()
	if err != nil {
		return err
	}

	support := subject.Provider.CapacitySupport()
	for _, act := range []struct {
		operation capability.CapacityOperation
		perform   func() error
	}{
		{capability.CapacityStop, func() error {
			_, err := subject.Provider.StopCapacity(ctx, mutate(command, receipt.NativeRef, capability.CapacityStop))
			return err
		}},
		{capability.CapacityResume, func() error {
			_, err := subject.Provider.StartCapacity(ctx, mutate(command, receipt.NativeRef, capability.CapacityResume))
			return err
		}},
		{capability.CapacityListOwned, func() error {
			_, err := subject.Provider.ListOwnedCapacity(ctx, capability.OwnershipQuery{WorkspaceID: command.WorkspaceID})
			return err
		}},
	} {
		performed := act.perform()
		refused := errors.Is(performed, capability.ErrCapabilityUnsupported)
		switch {
		case support.Claims(act.operation) && refused:
			return fmt.Errorf("this provider promises %q and refuses it as unsupported: %w", act.operation, performed)
		case !support.Claims(act.operation) && !refused:
			return fmt.Errorf(
				"this provider promises no %q and answered one with %v, so a caller would believe an act nothing performed",
				act.operation, performed,
			)
		}
	}
	return nil
}

// aTrialLeavesNothingOwned is the promise a bounded trial rests on: renting a
// machine and giving it back leaves the connection holding nothing this trial
// put there. Run after the promises above, it reads the whole trial rather than
// its own machine, because every one of them rents under this workspace.
func aTrialLeavesNothingOwned(ctx context.Context, subject Subject) (err error) {
	support := subject.Provider.CapacitySupport()
	if !support.ListOwned {
		return fmt.Errorf(
			"%w: this provider enumerates nothing it owns, so nothing can read back what a trial left behind",
			ErrNotApplicable,
		)
	}
	command, receipt, err := subject.rent(ctx, "sweep")
	if err != nil {
		return errors.Join(err, subject.giveBack(ctx, command, receipt.NativeRef))
	}
	if err := subject.giveBack(ctx, command, receipt.NativeRef); err != nil {
		return err
	}
	owned, err := subject.owned(ctx)
	if err != nil {
		return err
	}
	if len(owned) != 0 {
		return fmt.Errorf("this trial's workspace still holds %d machines: %+v", len(owned), owned)
	}
	return nil
}

// rent allocates one machine for one promise from whatever the subject says is
// on sale.
func (subject Subject) rent(ctx context.Context, promise string) (capability.ProvisionCommand, capability.CapacityReceipt, error) {
	if err := subject.check(); err != nil {
		return capability.ProvisionCommand{}, capability.CapacityReceipt{}, err
	}
	origin, err := subject.Capacity(ctx)
	if err != nil {
		return capability.ProvisionCommand{}, capability.CapacityReceipt{}, fmt.Errorf("choose capacity to rent: %w", err)
	}
	command := subject.command(promise, origin)
	receipt, err := subject.Provider.ProvisionCapacity(ctx, command)
	if err != nil {
		return command, receipt, fmt.Errorf("provision capacity for Rental %q: %w", command.RentalID, err)
	}
	switch {
	case receipt.NativeRef == "":
		return command, receipt, fmt.Errorf("Rental %q was accepted without naming a machine, so nothing can observe or destroy it", command.RentalID)
	case !receipt.State.Valid() || receipt.State.Terminal():
		return command, receipt, fmt.Errorf("Rental %q was accepted in state %q", command.RentalID, receipt.State)
	case receipt.AcceptedAt.IsZero():
		return command, receipt, fmt.Errorf("Rental %q was accepted at no moment, so no stage can be measured from it", command.RentalID)
	}
	return command, receipt, nil
}

// giveBack destroys what one promise rented. It runs even when the promise
// already failed, because a suite that leaks a machine on its way to reporting a
// broken promise bills for the report.
func (subject Subject) giveBack(ctx context.Context, command capability.ProvisionCommand, nativeRef string) error {
	if nativeRef == "" {
		return nil
	}
	if _, err := subject.Provider.TerminateCapacity(ctx, mutate(command, nativeRef, capability.CapacityTerminate)); err != nil {
		return fmt.Errorf("give machine %q back: %w", nativeRef, err)
	}
	return nil
}

func (subject Subject) owned(ctx context.Context) ([]capability.OwnedCapacity, error) {
	if !subject.Provider.CapacitySupport().ListOwned {
		return nil, nil
	}
	owned, err := subject.Provider.ListOwnedCapacity(ctx, capability.OwnershipQuery{WorkspaceID: subject.Lease.WorkspaceID})
	if err != nil {
		return nil, fmt.Errorf("list what this connection owns: %w", err)
	}
	return owned, nil
}

// command is the whole provision, with the identity Mercator assigns before the
// provider answers. The operation key is derived from the Rental rather than
// generated, so a repeat of one promise's command really is the same command.
func (subject Subject) command(promise string, origin Origin) capability.ProvisionCommand {
	rentalID := "rnt_" + subject.Lease.TrialID + "_" + promise
	return capability.ProvisionCommand{
		WorkspaceID:     subject.Lease.WorkspaceID,
		ConnectionID:    subject.Lease.ConnectionID,
		OperationKey:    "provision_" + rentalID,
		RentalID:        rentalID,
		Generation:      1,
		OwnershipToken:  "own_" + rentalID,
		OfferSnapshotID: origin.OfferSnapshotID,
		NativeRef:       origin.NativeRef,
		Bootstrap: capability.NodeBootstrap{
			ControlPlaneURL: subject.Lease.ControlPlaneURL,
			NodeID:          "nod_" + rentalID,
			RentalID:        rentalID,
			Generation:      1,
			EnrollmentToken: subject.Lease.EnrollmentToken,
			AgentVersion:    subject.Lease.AgentVersion,
		},
		MaxLifetimeSeconds: int64(subject.Lease.MaxLifetime / time.Second),
	}
}

func (subject Subject) check() error {
	switch {
	case subject.Provider == nil:
		return errors.New("capacitytest: a subject with no provider has nothing to keep a promise")
	case subject.Capacity == nil:
		return errors.New("capacitytest: a subject that names no capacity to rent cannot be provisioned from")
	case subject.Lease.TrialID == "" || subject.Lease.WorkspaceID == "" || subject.Lease.ConnectionID == "":
		return errors.New("capacitytest: every machine is rented under a trial, a workspace, and a connection")
	case subject.Lease.ControlPlaneURL == "" || subject.Lease.AgentVersion == "" || subject.Lease.EnrollmentToken == "":
		return errors.New("capacitytest: a bootstrap names a control plane, an agent build, and the material to join with")
	case subject.Lease.MaxLifetime <= 0:
		return errors.New("capacitytest: a trial that asks for no reclamation backstop can bill for ever")
	}
	return nil
}

func ref(command capability.ProvisionCommand, nativeRef string) capability.CapacityRef {
	return capability.CapacityRef{
		WorkspaceID:    command.WorkspaceID,
		ConnectionID:   command.ConnectionID,
		RentalID:       command.RentalID,
		NativeRef:      nativeRef,
		OwnershipToken: command.OwnershipToken,
	}
}

// mutate is one command against an allocated machine, keyed by the act it
// performs so a replay of a stop is a replay of that stop rather than of
// whatever the caller last sent.
func mutate(command capability.ProvisionCommand, nativeRef string, operation capability.CapacityOperation) capability.CapacityCommand {
	return capability.CapacityCommand{
		CapacityRef:  ref(command, nativeRef),
		OperationKey: string(operation) + "_" + command.RentalID,
		Generation:   command.Generation,
	}
}

func heldFor(owned []capability.OwnedCapacity, rentalID string) []capability.OwnedCapacity {
	var held []capability.OwnedCapacity
	for _, machine := range owned {
		if machine.RentalID == rentalID {
			held = append(held, machine)
		}
	}
	return held
}
