package rental_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/node"
	"github.com/benngarcia/mercator/internal/rental"
	"github.com/benngarcia/mercator/internal/rental/rentaltest"
)

func TestMemoryStoreKeepsEveryPromiseTheCapacityLifecycleRelieson(t *testing.T) {
	rentaltest.RunStoreSuite(t, func(*testing.T) rental.Store { return rental.NewMemoryStore() })
}

const (
	workspaceID  = "ws_lifecycle"
	rentalID     = "rnt_lifecycle"
	connectionID = "con_simcloud"
)

var start = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// TestEndingAGenerationRetiresTheRuntimeItWasServing is the one act that spans
// both authorities. The lease says the machine stopped being Mercator's, and the
// runtime enrolled on it has to stop being answered as capacity in the same
// breath: everything downstream reads a ready node as a machine a Run can be sent
// to, and the machine behind this one is gone.
func TestEndingAGenerationRetiresTheRuntimeItWasServing(t *testing.T) {
	fleet := enrolledFleet(t)
	leases := rental.NewLeases(fleet.store, fleet.registry)

	ended, err := leases.EndGeneration(context.Background(), workspaceID, rentalID, domain.RentalTerminated, start.Add(time.Hour))

	if err != nil {
		t.Fatalf("end the generation: %v", err)
	}
	if ended.Held() {
		t.Fatal("a lease whose machine was destroyed is still held")
	}
	retired, err := fleet.registry.List(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("list the fleet: %v", err)
	}
	if len(retired) != 1 || retired[0].State != node.StateRetired {
		t.Fatalf("fleet = %+v, want the runtime of the ended generation retired", retired)
	}
}

// TestARetiredRuntimeIsNoLongerPublishableAsCapacity is what the retirement is
// for. A node in any other state is published by the registry as a machine
// Placement may choose, so a generation that ended without this would leave the
// fleet offering a host nothing can reach.
func TestARetiredRuntimeIsNoLongerPublishableAsCapacity(t *testing.T) {
	fleet := enrolledFleet(t)
	leases := rental.NewLeases(fleet.store, fleet.registry)
	before, err := fleet.registry.Offers(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("read the offers before the generation ended: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("offers before = %+v, want the enrolled machine published", before)
	}

	if _, err := leases.EndGeneration(context.Background(), workspaceID, rentalID, domain.RentalTerminated, start.Add(time.Hour)); err != nil {
		t.Fatalf("end the generation: %v", err)
	}

	after, err := fleet.registry.Offers(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("read the offers after the generation ended: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("offers after = %+v, want a machine Mercator gave up published to nobody", after)
	}
	if _, err := fleet.registry.Ref(context.Background(), workspaceID, fleet.nodeID); err == nil {
		t.Fatal("a retired runtime still resolved to a node commands could be sent to")
	}
}

// TestALeaseNothingCanWriteRetiresNoRuntime holds the order the two writes happen
// in. The runtime is retired first, so a lease write that fails leaves a machine
// nothing offers under a generation the record still calls open, and the next
// attempt ends it. The opposite order leaves a runtime publishing itself as
// capacity for a machine the record says Mercator gave up.
func TestALeaseNothingCanWriteRetiresNoRuntime(t *testing.T) {
	fleet := enrolledFleet(t)
	leases := rental.NewLeases(fleet.store, refusingRetirer{})

	_, err := leases.EndGeneration(context.Background(), workspaceID, rentalID, domain.RentalTerminated, start.Add(time.Hour))

	if err == nil {
		t.Fatal("a generation ended although the runtime serving it could not be retired")
	}
	held, readErr := fleet.store.Get(context.Background(), workspaceID, rentalID)
	if readErr != nil {
		t.Fatalf("read the lease back: %v", readErr)
	}
	if _, open := held.Current(); !open {
		t.Fatalf("lease = %+v, want the generation still open because nothing retired its runtime", held)
	}
}

type refusingRetirer struct{}

func (refusingRetirer) Retire(context.Context, string, string) error {
	return errors.New("the registry is unreachable")
}

// enrolledFleet is one machine Mercator holds: a lease with an open generation,
// and the runtime of that generation enrolled and reporting, which is the state
// every case above ends from.
type fleet struct {
	store    rental.Store
	registry *node.Registry
	nodeID   string
}

func enrolledFleet(t *testing.T) fleet {
	t.Helper()
	signer := node.NewSigner([]byte("conformance-signing-key-conformance"))
	registry := node.NewRegistry(node.NewMemoryStore(), signer, "https://mercator.example",
		node.WithClock(func() time.Time { return start }))
	bootstrap, err := registry.Invite(context.Background(), node.Invitation{
		WorkspaceID:           workspaceID,
		RentalID:              rentalID,
		Generation:            1,
		ShadowPriceUSDPerHour: 1.5,
	})
	if err != nil {
		t.Fatalf("invite a runtime: %v", err)
	}
	if _, err := registry.Enroll(context.Background(), capability.EnrollmentRequest{
		NodeID:          bootstrap.NodeID,
		RentalID:        bootstrap.RentalID,
		Generation:      bootstrap.Generation,
		EnrollmentToken: bootstrap.EnrollmentToken,
		AgentVersion:    "test",
		Facts: capability.NodeFacts{
			ObservedAt: start,
			Host:       capability.HostFacts{OS: "linux", Architecture: "amd64", ContainerRuntime: "docker"},
		},
	}); err != nil {
		t.Fatalf("enroll the runtime: %v", err)
	}

	store := rental.NewMemoryStore()
	lease, err := domain.OpenRental(domain.RentalIdentity{
		RentalID:       rentalID,
		WorkspaceID:    workspaceID,
		ConnectionID:   connectionID,
		OwnershipToken: "own_lifecycle",
	}, bootstrap.NodeID, start)
	if err != nil {
		t.Fatalf("open the lease: %v", err)
	}
	lease, err = lease.Acquire("i-0abc")
	if err != nil {
		t.Fatalf("acquire the machine: %v", err)
	}
	if err := store.Save(context.Background(), 0, lease); err != nil {
		t.Fatalf("write the lease: %v", err)
	}
	return fleet{store: store, registry: registry, nodeID: bootstrap.NodeID}
}
