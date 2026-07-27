package capability_test

import (
	"context"
	"errors"
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

func TestCapacityWithoutANodeRuntimeIsRefused(t *testing.T) {
	_, err := capability.Declare("headless", capacityOnlyBackend{})

	if err == nil {
		t.Fatal("capacity with nothing to execute on it must not declare a lane")
	}
}

func TestNodeRuntimeCombinedWithOneShotExecutionIsRefused(t *testing.T) {
	_, err := capability.Declare("contradictory", contradictoryBackend{})

	if err == nil {
		t.Fatal("a backend cannot both control and not control its host runtime")
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

func TestStampLaneOverwritesTheLaneAndClearsUnearnedRentalIdentity(t *testing.T) {
	declaration, err := capability.Declare("oneshot", oneShotBackend{})
	if err != nil {
		t.Fatalf("declare one-shot backend: %v", err)
	}
	claimed := []domain.OfferSnapshot{{
		ID:       "off_1",
		RentalID: "rnt_claimed_without_a_node",
		Lane:     domain.LaneReusable,
	}}

	stamped := capability.StampLane(declaration, claimed)

	if stamped[0].Lane != domain.LaneEphemeral {
		t.Fatalf("lane = %q, want the declared %q", stamped[0].Lane, domain.LaneEphemeral)
	}
	if stamped[0].RentalID != "" {
		t.Fatalf("an ephemeral offer kept a Rental identity: %q", stamped[0].RentalID)
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
