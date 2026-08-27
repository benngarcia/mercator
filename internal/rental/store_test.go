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

	ended, err := leases.EndGeneration(context.Background(), rentalID, 1, domain.RentalTerminated, start.Add(time.Hour))

	if err != nil {
		t.Fatalf("end the generation: %v", err)
	}
	if ended.Held() {
		t.Fatal("a lease whose machine was destroyed is still held")
	}
	retired, err := fleet.registry.List(context.Background())
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
	before, err := fleet.registry.Offers(context.Background())
	if err != nil {
		t.Fatalf("read the offers before the generation ended: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("offers before = %+v, want the enrolled machine published", before)
	}

	if _, err := leases.EndGeneration(context.Background(), rentalID, 1, domain.RentalTerminated, start.Add(time.Hour)); err != nil {
		t.Fatalf("end the generation: %v", err)
	}

	after, err := fleet.registry.Offers(context.Background())
	if err != nil {
		t.Fatalf("read the offers after the generation ended: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("offers after = %+v, want a machine Mercator gave up published to nobody", after)
	}
	if _, err := fleet.registry.Ref(context.Background(), fleet.nodeID); err == nil {
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

	_, err := leases.EndGeneration(context.Background(), rentalID, 1, domain.RentalTerminated, start.Add(time.Hour))

	if err == nil {
		t.Fatal("a generation ended although the runtime serving it could not be retired")
	}
	held, readErr := fleet.store.Get(context.Background(), rentalID)
	if readErr != nil {
		t.Fatalf("read the lease back: %v", readErr)
	}
	if _, open := held.Current(); !open {
		t.Fatalf("lease = %+v, want the generation still open because nothing retired its runtime", held)
	}
}

// TestAnEndingRetriedAcrossAResumeTouchesNeitherTheLiveMachineNorItsRuntime is
// the failure this call is named for. A reconcile loop ends generation 1 and
// loses the answer; the lease is resumed onto generation 2 on a fresh machine
// with a fresh runtime and a Run is placed on it; the loop retries. Ending
// whichever generation is current then would retire a live runtime mid-Run and
// record that Mercator stopped a generation it never meant to touch.
//
// The retry is stamped after generation 2 began on purpose. A moment before that
// would be refused by the rule that a generation cannot end before it started,
// whichever generation the code picked, and the case would then pass on a clock
// that disagreed rather than on the number the caller named.
func TestAnEndingRetriedAcrossAResumeTouchesNeitherTheLiveMachineNorItsRuntime(t *testing.T) {
	fleet := enrolledFleet(t)
	leases := rental.NewLeases(fleet.store, fleet.registry)
	if _, err := leases.EndGeneration(context.Background(), rentalID, 1, domain.RentalStopped, start.Add(time.Hour)); err != nil {
		t.Fatalf("stop the machine: %v", err)
	}
	resumed := fleet.resume(t, start.Add(2*time.Hour))

	retried, err := leases.EndGeneration(context.Background(), rentalID, 1, domain.RentalStopped, start.Add(3*time.Hour))

	if err != nil {
		t.Fatalf("retry the ending of generation 1: %v", err)
	}
	fleet.mustNotBeRetired(t, resumed)
	current, open := retried.Current()
	if !open || current.NodeID != resumed {
		t.Fatalf("current generation = %+v open=%v, want the resumed machine still running", current, open)
	}
}

// TestAnEndingRefusesAGenerationTheLeaseHasNotReached is the same rule from the
// other side. A caller that names a generation this lease has never been through
// is deciding about a machine that does not exist, and the answer is a refusal
// rather than the nearest thing the record happens to hold.
func TestAnEndingRefusesAGenerationTheLeaseHasNotReached(t *testing.T) {
	fleet := enrolledFleet(t)
	leases := rental.NewLeases(fleet.store, fleet.registry)

	_, err := leases.EndGeneration(context.Background(), rentalID, 2, domain.RentalTerminated, start.Add(time.Hour))

	if err == nil {
		t.Fatal("a generation this lease has never been through was ended")
	}
	fleet.mustNotBeRetired(t, fleet.nodeID)
	held, readErr := fleet.store.Get(context.Background(), rentalID)
	if readErr != nil {
		t.Fatalf("read the lease back: %v", readErr)
	}
	if _, open := held.Current(); !open {
		t.Fatalf("lease = %+v, want the generation nobody named still open", held)
	}
}

type refusingRetirer struct{}

func (refusingRetirer) Retire(context.Context, string) error {
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
	registry := node.NewRegistry(
		node.NewMemoryStore(),
		node.NewSigner([]byte("conformance-signing-key-conformance")),
		"https://mercator.example",
		node.WithClock(func() time.Time { return start }),
	)
	fleet := fleet{store: rental.NewMemoryStore(), registry: registry}
	fleet.nodeID = fleet.enrolledRuntime(t, 1)

	lease, err := domain.OpenRental(domain.RentalIdentity{
		RentalID: rentalID,

		ConnectionID:   connectionID,
		OwnershipToken: "own_lifecycle",
	}, fleet.nodeID, start)
	if err != nil {
		t.Fatalf("open the lease: %v", err)
	}
	lease, err = lease.Acquire("i-0abc")
	if err != nil {
		t.Fatalf("acquire the machine: %v", err)
	}
	if err := fleet.store.Save(context.Background(), 0, lease); err != nil {
		t.Fatalf("write the lease: %v", err)
	}
	return fleet
}

// resume takes the stopped lease onto a fresh generation with a fresh runtime,
// which is the state a machine comes back in, and returns the runtime now
// serving it.
func (fleet fleet) resume(t *testing.T, at time.Time) string {
	t.Helper()
	held, err := fleet.store.Get(context.Background(), rentalID)
	if err != nil {
		t.Fatalf("read the stopped lease: %v", err)
	}
	resumed := fleet.enrolledRuntime(t, 2)
	lease, err := held.BeginGeneration(resumed, at)
	if err != nil {
		t.Fatalf("resume the lease: %v", err)
	}
	lease, err = lease.Acquire("i-0def")
	if err != nil {
		t.Fatalf("acquire the resumed machine: %v", err)
	}
	if err := fleet.store.Save(context.Background(), held.Version, lease); err != nil {
		t.Fatalf("write the resumed lease: %v", err)
	}
	return resumed
}

func (fleet fleet) enrolledRuntime(t *testing.T, generation uint64) string {
	t.Helper()
	bootstrap, err := fleet.registry.Invite(context.Background(), node.Invitation{

		RentalID:              rentalID,
		Generation:            generation,
		ShadowPriceUSDPerHour: 1.5,
	})
	if err != nil {
		t.Fatalf("invite a runtime: %v", err)
	}
	if _, err := fleet.registry.Enroll(context.Background(), capability.EnrollmentRequest{
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
	return bootstrap.NodeID
}

func (fleet fleet) mustNotBeRetired(t *testing.T, nodeID string) {
	t.Helper()
	record, err := fleet.registry.Ref(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("a runtime nothing decided about was retired: %v", err)
	}
	if record.NodeID != nodeID {
		t.Fatalf("resolved runtime = %q, want %q", record.NodeID, nodeID)
	}
}
