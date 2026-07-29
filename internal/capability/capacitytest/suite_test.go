package capacitytest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/capability/capacitytest"
	"github.com/benngarcia/mercator/internal/domain"
)

// This file is the suite held against itself. A conformance suite that cannot
// fail proves nothing about the backends it runs on, so every promise here is
// pointed at a provider built to break exactly that promise and at nothing else.

// TestAProviderThatKeepsTheContractKeepsEveryPromise is the baseline the broken
// providers below are read against: one in-memory backend that does what the
// contract says, and a suite that reports it green.
func TestAProviderThatKeepsTheContractKeepsEveryPromise(t *testing.T) {
	subject := subjectFor(t, newStub(flawless))

	for _, promise := range capacitytest.Promises() {
		t.Run(promise.Name, func(t *testing.T) {
			if err := promise.Keep(t.Context(), subject); err != nil {
				t.Fatalf("%s (%s): %v", promise.Name, promise.Rule, err)
			}
		})
	}
}

// TestEveryPromiseCatchesTheDefectItIsAbout is the deliberate failing case for
// each promise, kept rather than performed once and thrown away. Each provider
// below breaks one clause of the contract, and the case asserts both halves: the
// promises about that clause fail, and the rest do not, so a suite that went
// green everywhere or red everywhere would be caught here.
//
// A credential check that allocates a machine breaks two of them, and that is
// the suite working rather than a case stated loosely: the machine it left
// behind is still owned when the trial sweeps, which is exactly what the last
// promise is for.
func TestEveryPromiseCatchesTheDefectItIsAbout(t *testing.T) {
	tests := []struct {
		flaw   flaw
		broken []string
	}{
		{flawSellsAMachineItAlreadyHolds, []string{"listed_capacity_is_capacity_to_acquire"}},
		{flawResumesWhatItCannotStop, []string{"the_negotiated_set_is_one_a_provider_could_keep"}},
		{flawVerifyAllocatesAMachine, []string{"a_credential_check_allocates_nothing", "a_trial_leaves_nothing_owned"}},
		{flawProvisionsAFreshMachineEveryTime, []string{"one_provision_command_produces_one_machine"}},
		{flawOwnsAMachineUnderNoToken, []string{"a_lost_answer_costs_no_second_machine"}},
		{flawObservesADestroyedMachineAsActive, []string{"terminate_is_confirmed_and_stays_confirmed"}},
		{flawStopsWithoutPromisingTo, []string{"an_operation_the_provider_never_promised_is_refused"}},
		{flawGoesOnOwningWhatItDestroyed, []string{"a_trial_leaves_nothing_owned"}},
	}
	for _, test := range tests {
		t.Run(string(test.flaw), func(t *testing.T) {
			subject := subjectFor(t, newStub(test.flaw))
			expected := map[string]bool{}
			for _, name := range test.broken {
				expected[name] = true
			}

			for _, promise := range capacitytest.Promises() {
				err := promise.Keep(t.Context(), subject)
				broken := err != nil && !errors.Is(err, capacitytest.ErrNotApplicable)
				if broken != expected[promise.Name] {
					t.Errorf("%s reported %v against a provider that %s", promise.Name, err, test.flaw)
				}
			}
		})
	}
}

// TestAPromiseOutOfReachOfTheNegotiatedSetIsNotReportedKept is the difference
// between a promise a backend broke and one nothing could look at. A provider
// that deduplicates on the operation key is entitled to enumerate nothing it
// owns, and a suite that called those cases green would be claiming to have read
// a listing that does not exist.
func TestAPromiseOutOfReachOfTheNegotiatedSetIsNotReportedKept(t *testing.T) {
	subject := subjectFor(t, newStub(flawless, listsNothingItOwns))

	outOfReach := map[string]bool{}
	for _, promise := range capacitytest.Promises() {
		err := promise.Keep(t.Context(), subject)
		if errors.Is(err, capacitytest.ErrNotApplicable) {
			outOfReach[promise.Name] = true
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", promise.Name, err)
		}
	}
	for _, name := range []string{"a_lost_answer_costs_no_second_machine", "a_trial_leaves_nothing_owned"} {
		if !outOfReach[name] {
			t.Errorf("%s was reported kept by a provider that enumerates nothing it owns", name)
		}
	}
}

// TestAReceiptTheSuiteRefusesStillCostsNoMachine is the suite's own hygiene. A
// provider can allocate a machine and then answer about it in a way the contract
// refuses, and the machine exists either way: a suite that gave back only the
// machines whose receipts it liked would bill for its own strictness.
func TestAReceiptTheSuiteRefusesStillCostsNoMachine(t *testing.T) {
	provider := newStub(flawAcceptsAMachineAtNoMoment)
	subject := subjectFor(t, provider)

	broken := everyPromiseAgainst(t, subject)

	if len(broken) == 0 {
		t.Fatal("a provider that dates no allocation broke no promise, so nothing here was ever refused")
	}
	if running := provider.running(); len(running) != 0 {
		t.Fatalf("the suite refused every receipt and left %d machines running: %v", len(running), running)
	}
}

// TestTheSecondMachineOneCommandAllocatedIsGivenBack is the leak this suite
// exists to report, made to happen. A provider that allocates a fresh machine
// per command is billing for two the moment the promise catches it, and the
// promise that reports the defect is the only thing holding a ref to the second
// one: this connection enumerates nothing it owns, so no sweep can find it
// afterwards.
func TestTheSecondMachineOneCommandAllocatedIsGivenBack(t *testing.T) {
	provider := newStub(flawProvisionsAFreshMachineEveryTime, listsNothingItOwns)
	subject := subjectFor(t, provider)

	broken := everyPromiseAgainst(t, subject)

	if !broken["one_provision_command_produces_one_machine"] {
		t.Fatal("a provider that allocates a fresh machine per command was reported as keeping provision idempotency")
	}
	if running := provider.running(); len(running) != 0 {
		t.Fatalf("the duplicate machines the promise reported are still running: %v", running)
	}
}

// TestAProvisionNobodyGotAnAnswerToIsStillGivenBack is the machine that costs
// money in silence: allocated, and named in no receipt. The suite cannot
// terminate a ref nothing handed it, so it asks the mechanism the provider
// negotiated for this case, and both of the legal answers are exercised here.
func TestAProvisionNobodyGotAnAnswerToIsStillGivenBack(t *testing.T) {
	tests := []struct {
		reconciliation string
		flaws          []flaw
	}{
		{"by what the connection owns", []flaw{flawLosesTheAnswerToTheFirstProvision}},
		{"by repeating the operation key", []flaw{flawLosesTheAnswerToTheFirstProvision, listsNothingItOwns}},
	}
	for _, test := range tests {
		t.Run(test.reconciliation, func(t *testing.T) {
			provider := newStub(test.flaws...)
			subject := subjectFor(t, provider)

			broken := everyPromiseAgainst(t, subject)

			if len(broken) == 0 {
				t.Fatal("a provision nobody got an answer to broke no promise")
			}
			if running := provider.running(); len(running) != 0 {
				t.Fatalf("the machine the lost answer allocated is still running: %v", running)
			}
		})
	}
}

// everyPromiseAgainst runs the whole suite and answers with the promises this
// provider broke.
func everyPromiseAgainst(t *testing.T, subject capacitytest.Subject) map[string]bool {
	t.Helper()
	broken := map[string]bool{}
	for _, promise := range capacitytest.Promises() {
		if err := promise.Keep(t.Context(), subject); err != nil && !errors.Is(err, capacitytest.ErrNotApplicable) {
			broken[promise.Name] = true
		}
	}
	return broken
}

// TestASubjectMissingWhatEveryMachineIsRentedUnderIsRefused keeps the suite from
// renting anything against half an identity. A trial that provisioned without a
// workspace or a reclamation backstop would leave machines nothing could find.
func TestASubjectMissingWhatEveryMachineIsRentedUnderIsRefused(t *testing.T) {
	subject := subjectFor(t, newStub(flawless))
	subject.Lease.MaxLifetime = 0

	err := capacitytest.Promises()[0].Keep(t.Context(), subject)

	if err == nil {
		t.Fatal("a trial that asks for no reclamation backstop was accepted")
	}
}

func subjectFor(t *testing.T, provider *stub) capacitytest.Subject {
	t.Helper()
	return capacitytest.Subject{
		Name:     "stub",
		Provider: provider,
		Lease: capacitytest.Lease{
			TrialID:         "trial01",
			WorkspaceID:     "ws_trial",
			ConnectionID:    "conn_trial",
			ControlPlaneURL: "https://reports.example.com",
			AgentVersion:    "v0.7.1",
			EnrollmentToken: "enrolment-nothing-minted",
			MaxLifetime:     30 * time.Minute,
		},
		Capacity: func(context.Context) (capacitytest.Origin, error) {
			return capacitytest.Origin{OfferSnapshotID: listingID, NativeRef: listingNativeRef}, nil
		},
	}
}

const (
	listingID        = "off_stub_a6000"
	listingNativeRef = "stubcloud/eu-1/A6000"
)

// flaw is one clause of the capacity contract, broken on purpose.
type flaw string

const (
	flawless                              flaw = "keeps the contract"
	flawSellsAMachineItAlreadyHolds       flaw = "sells a machine it already holds"
	flawResumesWhatItCannotStop           flaw = "resumes what it cannot stop"
	flawVerifyAllocatesAMachine           flaw = "allocates a machine to check its credential"
	flawProvisionsAFreshMachineEveryTime  flaw = "provisions a fresh machine every time"
	flawOwnsAMachineUnderNoToken          flaw = "owns a machine under no token"
	flawObservesADestroyedMachineAsActive flaw = "observes a destroyed machine as active"
	flawStopsWithoutPromisingTo           flaw = "stops without promising to"
	flawGoesOnOwningWhatItDestroyed       flaw = "goes on owning what it destroyed"
	flawAcceptsAMachineAtNoMoment         flaw = "accepts a machine at no moment"
	flawLosesTheAnswerToTheFirstProvision flaw = "allocates a machine and loses the answer"
	listsNothingItOwns                    flaw = "lists nothing it owns"
)

// stub is a capacity provider in a slice: enough of the contract for the suite
// to exercise, with one clause of it breakable at a time.
//
// Every machine is addressed by its own native ref, the way a real backend
// addresses one. A stub that kept one machine per Rental could not hold the two
// a non-idempotent provider allocates for the same command, and a suite run
// against it would report a leak reclaimed that nothing had reclaimed.
type stub struct {
	flaws    map[flaw]bool
	machines []*stubMachine
	lost     map[string]bool
	next     int
}

type stubMachine struct {
	nativeRef      string
	rentalID       string
	workspaceID    string
	ownershipToken string
	generation     uint64
	terminated     bool
}

func newStub(flaws ...flaw) *stub {
	held := map[flaw]bool{}
	for _, broken := range flaws {
		held[broken] = true
	}
	return &stub{flaws: held, lost: map[string]bool{}}
}

// running is every machine this provider is still billing for, whatever the
// suite believes it gave back.
func (s *stub) running() []string {
	var live []string
	for _, machine := range s.machines {
		if !machine.terminated {
			live = append(live, machine.nativeRef)
		}
	}
	return live
}

func (s *stub) at(nativeRef string) (*stubMachine, bool) {
	for _, machine := range s.machines {
		if machine.nativeRef == nativeRef {
			return machine, true
		}
	}
	return nil, false
}

func (s *stub) liveFor(rentalID string) (*stubMachine, bool) {
	for _, machine := range s.machines {
		if machine.rentalID == rentalID && !machine.terminated {
			return machine, true
		}
	}
	return nil, false
}

func (s *stub) CapacitySupport() capability.CapacitySupport {
	support := capability.CapacitySupport{
		Stop:                  true,
		Resume:                true,
		PersistentDisk:        true,
		ExactPricing:          true,
		IdempotentProvision:   capability.IdempotentProvisionOperationKey,
		ListOwned:             true,
		ObserveAfterTerminate: true,
	}
	if s.flaws[flawResumesWhatItCannotStop] {
		support.Stop = false
	}
	if s.flaws[flawStopsWithoutPromisingTo] {
		support.Stop = false
		support.Resume = false
		support.PersistentDisk = false
	}
	if s.flaws[listsNothingItOwns] {
		support.ListOwned = false
	}
	return support
}

func (s *stub) Verify(ctx context.Context) error {
	if s.flaws[flawVerifyAllocatesAMachine] {
		_, _ = s.ProvisionCapacity(ctx, capability.ProvisionCommand{
			WorkspaceID: "ws_trial",
			RentalID:    "rnt_verify",
		})
	}
	return nil
}

func (s *stub) ListCapacity(context.Context, capability.CapacityQuery) ([]domain.OfferSnapshot, error) {
	listing := domain.OfferSnapshot{
		ID:        listingID,
		Kind:      domain.OfferKindProvisionable,
		NativeRef: listingNativeRef,
		Pricing:   domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.0004, Known: true},
	}
	if s.flaws[flawSellsAMachineItAlreadyHolds] {
		listing.Kind = domain.OfferKindStanding
		listing.RentalID = "rnt_already_held"
	}
	return []domain.OfferSnapshot{listing}, nil
}

func (s *stub) ProvisionCapacity(_ context.Context, command capability.ProvisionCommand) (capability.CapacityReceipt, error) {
	if held, exists := s.liveFor(command.RentalID); exists && !s.flaws[flawProvisionsAFreshMachineEveryTime] {
		return capability.CapacityReceipt{
			NativeRef:  held.nativeRef,
			State:      capability.CapacityStateActive,
			AcceptedAt: stubMoment,
			Duplicate:  true,
		}, nil
	}
	s.next++
	held := &stubMachine{
		nativeRef:      fmt.Sprintf("mch_%d", s.next),
		rentalID:       command.RentalID,
		workspaceID:    command.WorkspaceID,
		ownershipToken: command.OwnershipToken,
		generation:     command.Generation,
	}
	if s.flaws[flawOwnsAMachineUnderNoToken] {
		held.ownershipToken = ""
	}
	s.machines = append(s.machines, held)
	if s.flaws[flawLosesTheAnswerToTheFirstProvision] && !s.lost[command.RentalID] {
		s.lost[command.RentalID] = true
		return capability.CapacityReceipt{}, fmt.Errorf(
			"%w: stub allocated %s for Rental %q and the answer never came back",
			capability.ErrCapacityIndeterminate, held.nativeRef, command.RentalID,
		)
	}
	receipt := capability.CapacityReceipt{
		NativeRef:  held.nativeRef,
		State:      capability.CapacityStateStarting,
		AcceptedAt: stubMoment,
	}
	if s.flaws[flawAcceptsAMachineAtNoMoment] {
		receipt.AcceptedAt = time.Time{}
	}
	return receipt, nil
}

func (s *stub) ObserveCapacity(_ context.Context, held capability.CapacityRef) (capability.CapacityObservation, error) {
	machine, exists := s.at(held.NativeRef)
	if !exists {
		return capability.CapacityObservation{}, fmt.Errorf("stub: nothing allocated as machine %q", held.NativeRef)
	}
	state := capability.CapacityStateActive
	if machine.terminated && !s.flaws[flawObservesADestroyedMachineAsActive] {
		state = capability.CapacityStateTerminated
	}
	return capability.CapacityObservation{NativeRef: machine.nativeRef, State: state, ObservedAt: stubMoment}, nil
}

func (s *stub) StartCapacity(_ context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	return s.transition(command, capability.CapacityResume, capability.CapacityStateActive)
}

func (s *stub) StopCapacity(_ context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	return s.transition(command, capability.CapacityStop, capability.CapacityStateStopped)
}

func (s *stub) transition(
	command capability.CapacityCommand,
	operation capability.CapacityOperation,
	state capability.CapacityState,
) (capability.CapacityReceipt, error) {
	if !s.CapacitySupport().Claims(operation) && !s.flaws[flawStopsWithoutPromisingTo] {
		return capability.CapacityReceipt{}, fmt.Errorf("%w: stub promises no %s", capability.ErrCapabilityUnsupported, operation)
	}
	machine, exists := s.at(command.NativeRef)
	if !exists {
		return capability.CapacityReceipt{}, fmt.Errorf("stub: nothing allocated as machine %q", command.NativeRef)
	}
	return capability.CapacityReceipt{NativeRef: machine.nativeRef, State: state, AcceptedAt: stubMoment}, nil
}

func (s *stub) TerminateCapacity(_ context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	machine, exists := s.at(command.NativeRef)
	if !exists {
		return capability.CapacityReceipt{}, fmt.Errorf("stub: nothing allocated as machine %q", command.NativeRef)
	}
	duplicate := machine.terminated
	machine.terminated = true
	return capability.CapacityReceipt{
		NativeRef:  machine.nativeRef,
		State:      capability.CapacityStateTerminated,
		AcceptedAt: stubMoment,
		Duplicate:  duplicate,
	}, nil
}

func (s *stub) ListOwnedCapacity(_ context.Context, query capability.OwnershipQuery) ([]capability.OwnedCapacity, error) {
	if !s.CapacitySupport().ListOwned {
		return nil, fmt.Errorf("%w: stub promises no owned-capacity listing", capability.ErrCapabilityUnsupported)
	}
	var owned []capability.OwnedCapacity
	for _, machine := range s.machines {
		if (machine.terminated && !s.flaws[flawGoesOnOwningWhatItDestroyed]) || machine.workspaceID != query.WorkspaceID {
			continue
		}
		owned = append(owned, capability.OwnedCapacity{
			NativeRef:      machine.nativeRef,
			WorkspaceID:    machine.workspaceID,
			RentalID:       machine.rentalID,
			Generation:     machine.generation,
			OwnershipToken: machine.ownershipToken,
			State:          capability.CapacityStateActive,
			CreatedAt:      stubMoment,
		})
	}
	return owned, nil
}

var stubMoment = time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
