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
	listsNothingItOwns                    flaw = "lists nothing it owns"
)

// stub is a capacity provider in a map: enough of the contract for the suite to
// exercise, with one clause of it breakable at a time.
type stub struct {
	flaws    map[flaw]bool
	machines map[string]*stubMachine
	next     int
}

type stubMachine struct {
	nativeRef      string
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
	return &stub{flaws: held, machines: map[string]*stubMachine{}}
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
	if held, exists := s.machines[command.RentalID]; exists && !held.terminated && !s.flaws[flawProvisionsAFreshMachineEveryTime] {
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
		workspaceID:    command.WorkspaceID,
		ownershipToken: command.OwnershipToken,
		generation:     command.Generation,
	}
	if s.flaws[flawOwnsAMachineUnderNoToken] {
		held.ownershipToken = ""
	}
	s.machines[command.RentalID] = held
	return capability.CapacityReceipt{
		NativeRef:  held.nativeRef,
		State:      capability.CapacityStateStarting,
		AcceptedAt: stubMoment,
	}, nil
}

func (s *stub) ObserveCapacity(_ context.Context, held capability.CapacityRef) (capability.CapacityObservation, error) {
	machine, exists := s.machines[held.RentalID]
	if !exists {
		return capability.CapacityObservation{}, fmt.Errorf("stub: nothing allocated for Rental %q", held.RentalID)
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
	machine, exists := s.machines[command.RentalID]
	if !exists {
		return capability.CapacityReceipt{}, fmt.Errorf("stub: nothing allocated for Rental %q", command.RentalID)
	}
	return capability.CapacityReceipt{NativeRef: machine.nativeRef, State: state, AcceptedAt: stubMoment}, nil
}

func (s *stub) TerminateCapacity(_ context.Context, command capability.CapacityCommand) (capability.CapacityReceipt, error) {
	machine, exists := s.machines[command.RentalID]
	if !exists {
		return capability.CapacityReceipt{}, fmt.Errorf("stub: nothing allocated for Rental %q", command.RentalID)
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
	for rentalID, machine := range s.machines {
		if (machine.terminated && !s.flaws[flawGoesOnOwningWhatItDestroyed]) || machine.workspaceID != query.WorkspaceID {
			continue
		}
		owned = append(owned, capability.OwnedCapacity{
			NativeRef:      machine.nativeRef,
			WorkspaceID:    machine.workspaceID,
			RentalID:       rentalID,
			Generation:     machine.generation,
			OwnershipToken: machine.ownershipToken,
			State:          capability.CapacityStateActive,
			CreatedAt:      stubMoment,
		})
	}
	return owned, nil
}

var stubMoment = time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
