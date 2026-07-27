package capability_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

func TestBackendWithOnlyOneShotExecutionDeclaresTheEphemeralLane(t *testing.T) {
	declaration, err := capability.Declare("oneshot", oneShotBackend{})
	if err != nil {
		t.Fatalf("declare one-shot backend: %v", err)
	}

	if declaration.Lane != domain.LaneEphemeral {
		t.Fatalf("lane = %q, want %q", declaration.Lane, domain.LaneEphemeral)
	}
	if declaration.Ephemeral == nil {
		t.Fatal("an ephemeral backend must report its ephemeral support")
	}
	if declaration.Capacity != nil || declaration.Node != nil {
		t.Fatalf("a one-shot backend must claim no capacity or node support, got %+v", declaration)
	}
}

func TestBackendWithCapacityAndNodeRuntimeDeclaresTheReusableLane(t *testing.T) {
	declaration, err := capability.Declare("reusable", reusableBackend{})
	if err != nil {
		t.Fatalf("declare reusable backend: %v", err)
	}

	if declaration.Lane != domain.LaneReusable {
		t.Fatalf("lane = %q, want %q", declaration.Lane, domain.LaneReusable)
	}
	if declaration.Capacity == nil || declaration.Node == nil {
		t.Fatalf("a reusable backend must report both capacity and node support, got %+v", declaration)
	}
}

// TestAProviderThatAllocatesMachinesSellsTheReusableLane is the shape every real
// provider has: it allocates machines and executes nothing, because the runtime
// that executes on a rented machine enrolls with the control plane rather than
// with the connection that rented it. A machine that outlives the workload run on
// it is reusable capacity, and that is the whole of what the lane says here.
//
// The Declaration asserts no node runtime, and that is the point. Whether
// anything can execute on one machine is that machine's own fact, established by
// the agent enrolled on it. A Declaration that answered for it would be this
// deployment stating a container runtime, a prewarm and an artifact replica store
// about a host it has never reached.
func TestAProviderThatAllocatesMachinesSellsTheReusableLane(t *testing.T) {
	declaration, err := capability.Declare("headless", capacityOnlyBackend{})
	if err != nil {
		t.Fatalf("declare a provider that allocates machines and executes nothing: %v", err)
	}

	if declaration.Lane != domain.LaneReusable {
		t.Fatalf("lane = %q, want %q", declaration.Lane, domain.LaneReusable)
	}
	if declaration.Capacity == nil {
		t.Fatal("a capacity provider must report the capacity set it negotiated")
	}
	if declaration.Node != nil {
		t.Fatalf("a provider adapter claimed a node runtime of its own: %+v", declaration.Node)
	}
}

func TestNodeRuntimeCombinedWithOneShotExecutionIsRefused(t *testing.T) {
	_, err := capability.Declare("contradictory", contradictoryBackend{})

	if err == nil {
		t.Fatal("a backend cannot both control and not control its host runtime")
	}
	if !strings.Contains(err.Error(), "does not control its host runtime") {
		t.Fatalf("refusal = %q, want the contradiction it was refused for", err.Error())
	}
}

// TestCapacityCombinedWithOneShotExecutionIsRefused refuses the connection that
// sells both. One lane is stamped on every offer a connection publishes, so a
// backend answering both ListCapacity and ListOffers would publish machines and
// one-shot executions under one word, and nothing downstream could say which of
// the two an offer came from. A provider selling both is two connections.
func TestCapacityCombinedWithOneShotExecutionIsRefused(t *testing.T) {
	_, err := capability.Declare("bothlanes", capacityAndOneShotBackend{})

	if err == nil {
		t.Fatal("one connection cannot sell capacity and one-shot execution under one lane")
	}
	if !strings.Contains(err.Error(), "capacity and one-shot execution on one connection") {
		t.Fatalf("refusal = %q, want the ambiguity it was refused for", err.Error())
	}
}

func TestBackendImplementingNoContractIsRefused(t *testing.T) {
	_, err := capability.Declare("empty", struct{}{})

	if err == nil {
		t.Fatal("a backend that implements no contract must be refused")
	}
}

// TestACapacitySetThatContradictsItselfIsRefused holds the four sets no provider
// could keep. Every field of the negotiated set is acted on without
// asking again, so a set that answers two of them incompatibly has to be refused
// where the connection is built rather than discovered by whichever caller happens
// to read both: a provider that deduplicates nothing and lists nothing leaks every
// machine a lost response allocated, and there is no later moment at which
// Mercator could find those machines to account for them.
func TestACapacitySetThatContradictsItselfIsRefused(t *testing.T) {
	for name, negotiated := range map[string]capability.CapacitySupport{
		"a resume of capacity nothing can stop": {
			IdempotentProvision: capability.IdempotentProvisionOperationKey,
			Resume:              true,
		},
		"a disk that survives a stop no provider performs": {
			IdempotentProvision: capability.IdempotentProvisionOperationKey,
			PersistentDisk:      true,
		},
		"no deduplication and no way to list what is owned": {
			IdempotentProvision: capability.IdempotentProvisionNone,
		},
		"an idempotency mechanism Mercator has never heard of": {
			IdempotentProvision: "eventually",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := capability.Declare("incoherent", reusableBackendNegotiating(negotiated))

			if err == nil {
				t.Fatalf("declaring %+v must be refused", negotiated)
			}
		})
	}
}

func TestStampLaneOverwritesTheLaneAnAdapterClaimed(t *testing.T) {
	declaration, err := capability.Declare("oneshot", oneShotBackend{})
	if err != nil {
		t.Fatalf("declare one-shot backend: %v", err)
	}
	claimed := []domain.OfferSnapshot{{ID: "off_1", Lane: domain.LaneReusable}}

	stamped := capability.StampLane(declaration, claimed)

	if stamped[0].Lane != domain.LaneEphemeral {
		t.Fatalf("lane = %q, want the declared %q", stamped[0].Lane, domain.LaneEphemeral)
	}
}

// TestNoAdapterCanStateARentalIdentity holds the identity Mercator alone mints.
// A Rental is Mercator's lease record, and the offers that carry one are the
// enrolled nodes' own, from the invitation that named it. An adapter filling the
// field in from its instance type or its contract id would publish a Rental
// Mercator does not hold on the public offer route, and a Booking bound to it
// would let a second Run queue behind a lease that never existed. The reusable
// lane is the case that matters, because that is the lane a provider that
// allocates machines lands in.
func TestNoAdapterCanStateARentalIdentity(t *testing.T) {
	for name, backend := range map[string]capability.Backend{
		"a provider that allocates machines": capacityOnlyBackend{},
		"a one-shot executor":                oneShotBackend{},
	} {
		t.Run(name, func(t *testing.T) {
			declaration, err := capability.Declare("stated", backend)
			if err != nil {
				t.Fatalf("declare %s: %v", name, err)
			}
			claimed := []domain.OfferSnapshot{{
				ID:       "off_1",
				RentalID: "rnt_adapter_invented",
				Kind:     domain.OfferKindStanding,
			}}

			stamped := capability.StampLane(declaration, claimed)

			if stamped[0].RentalID != "" {
				t.Fatalf("an offer in the %s lane kept the Rental identity %q its adapter stated",
					declaration.Lane, stamped[0].RentalID)
			}
		})
	}
}

// TestANegotiatedSetClaimsOnlyWhatItPromised is what a caller checks before
// sending a command. Allocating, observing, and destroying capacity are the floor
// of the contract and have no field to negotiate; suspending, resuming, and
// enumerating what a connection owns are promises a provider makes or does not.
func TestANegotiatedSetClaimsOnlyWhatItPromised(t *testing.T) {
	stops := capability.CapacitySupport{
		IdempotentProvision: capability.IdempotentProvisionOperationKey,
		Stop:                true,
	}
	suspendsNothing := capability.CapacitySupport{
		IdempotentProvision: capability.IdempotentProvisionOperationKey,
	}

	for operation, want := range map[capability.CapacityOperation]bool{
		capability.CapacityProvision: true,
		capability.CapacityObserve:   true,
		capability.CapacityTerminate: true,
		capability.CapacityStop:      true,
		capability.CapacityResume:    false,
		capability.CapacityListOwned: false,
	} {
		if got := stops.Claims(operation); got != want {
			t.Errorf("a provider that only stops claims %q = %v, want %v", operation, got, want)
		}
	}
	if suspendsNothing.Claims(capability.CapacityStop) {
		t.Error("a provider that negotiated no stop must not claim one")
	}
}

func TestUnsupportedCapabilityErrorsAreDistinguishable(t *testing.T) {
	if !errors.Is(capability.ErrCapabilityUnsupported, capability.ErrCapabilityUnsupported) {
		t.Fatal("the unsupported-capability sentinel must be matchable with errors.Is")
	}
}

type oneShotBackend struct{ capability.EphemeralExecutor }

func (oneShotBackend) EphemeralSupport() capability.EphemeralSupport {
	return capability.EphemeralSupport{IdempotentLaunch: "launch_key"}
}

type capacityOnlyBackend struct{ capability.CapacityProvider }

func (capacityOnlyBackend) CapacitySupport() capability.CapacitySupport {
	return capability.CapacitySupport{IdempotentProvision: "operation_key"}
}

type nodeBackend struct{ capability.NodeRuntime }

func (nodeBackend) NodeSupport() capability.NodeSupport {
	return capability.NodeSupport{ContainerRuntime: "docker", MaxConcurrentWorkloads: 1}
}

type reusableBackend struct {
	capacityOnlyBackend
	nodeBackend
}

func (reusableBackend) Verify(context.Context) error { return nil }

// negotiatingBackend is a reusable backend that negotiates whatever a test hands
// it, which is the only way to state a set that a real provider's own
// CapacitySupport method would never return.
type negotiatingBackend struct {
	capability.CapacityProvider
	nodeBackend
	negotiated capability.CapacitySupport
}

func (backend negotiatingBackend) CapacitySupport() capability.CapacitySupport {
	return backend.negotiated
}

func (negotiatingBackend) Verify(context.Context) error { return nil }

func reusableBackendNegotiating(negotiated capability.CapacitySupport) negotiatingBackend {
	return negotiatingBackend{negotiated: negotiated}
}

type contradictoryBackend struct {
	nodeBackend
	oneShotBackend
}

func (contradictoryBackend) Verify(context.Context) error { return nil }

func (contradictoryBackend) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return nil, nil
}

// capacityAndOneShotBackend is the connection that sells a machine and a one-shot
// execution at once, which one lane cannot describe. Verify is declared here
// because both contracts have one, and an ambiguous promoted method would leave
// this satisfying neither contract and being refused for the wrong reason.
type capacityAndOneShotBackend struct {
	capacityOnlyBackend
	oneShotBackend
}

func (capacityAndOneShotBackend) Verify(context.Context) error { return nil }
