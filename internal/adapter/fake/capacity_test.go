package fake

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// acceptingRegistry is a control plane that lets every agent in. What an agent
// redeems is the registry's business and is tested against the real one in the
// scenario harness; what this world owes is what it does once a session is open.
type acceptingRegistry struct {
	enrolled []capability.EnrollmentRequest
}

func (r *acceptingRegistry) Enroll(_ context.Context, request capability.EnrollmentRequest) (capability.Enrollment, error) {
	r.enrolled = append(r.enrolled, request)
	return capability.Enrollment{NodeID: request.NodeID}, nil
}

const listedMachine = "simcloud-4090-0f31"

// listedWorld is one marketplace listing selling one machine that takes a
// minute to allocate and a minute more to boot, and an agent that opens its
// session a minute after that.
func listedWorld(t *testing.T) (*World, *acceptingRegistry) {
	t.Helper()
	world := newLayeredWorld(t)
	registry := &acceptingRegistry{}
	world.Enroller = registry
	listing := &Machine{
		Offer: domain.OfferSnapshot{
			ID:        "reusable-4090",
			MachineID: listedMachine,
			Kind:      domain.OfferKindProvisionable,
			Lane:      domain.LaneReusable,
			Pricing:   domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.0005, Known: true},
			Resources: domain.ResourceInventory{EphemeralDiskBytes: 200 << 30, EphemeralDiskKnown: true},
		},
		AcquisitionSpend: time.Minute,
		BootSpend:        time.Minute,
		AgentReadySpend:  time.Minute,
	}
	if err := world.AddMachine(listing); err != nil {
		t.Fatalf("add listing: %v", err)
	}
	return world, registry
}

func provisionAndEnrol(t *testing.T, world *World, rentalID, ownershipToken string) {
	t.Helper()
	if _, err := world.ProvisionCapacity(context.Background(), capability.ProvisionCommand{
		WorkspaceID:     "ws_fake",
		RentalID:        rentalID,
		OfferSnapshotID: "reusable-4090",
		OwnershipToken:  ownershipToken,
		Bootstrap:       capability.NodeBootstrap{NodeID: "nod_" + rentalID, RentalID: rentalID, Generation: 1},
	}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	world.Clock().Advance(3 * time.Minute)
	if err := world.DeliverEnrolments(context.Background()); err != nil {
		t.Fatalf("deliver enrolments: %v", err)
	}
}

// TestWorldPublishesTheMachineOneListingBecameBesideTheListing is the whole of
// the transition this world could not describe: a machine that does not exist
// becomes standing reusable capacity of its own, under the provider's handle for
// the machine and against the lease the invitation named. The listing is still
// published beside it and is no longer capacity anybody can buy, which is what
// one machine sold under one name comes to once Mercator holds it.
func TestWorldPublishesTheMachineOneListingBecameBesideTheListing(t *testing.T) {
	world, _ := listedWorld(t)

	provisionAndEnrol(t, world, "rnt_first", "own-first")

	offers := worldOffers(t, world)
	machine, published := offers[listedMachine]
	if !published {
		t.Fatalf("an agent opened a session and the machine is published nowhere: %v", offers)
	}
	if machine.Kind != domain.OfferKindStanding || !machine.Lane.Reusable() {
		t.Errorf("the machine publishes %q in the %q lane, want standing reusable capacity", machine.Kind, machine.Lane)
	}
	if machine.RentalID != "rnt_first" {
		t.Errorf("the machine is held under Rental %q, want the lease its invitation named", machine.RentalID)
	}
	if machine.Provisioning != nil {
		t.Errorf("the machine still publishes %+v to spend coming into existence, and it is already up", machine.Provisioning)
	}
	listing, stillPublished := offers["reusable-4090"]
	if !stillPublished || listing.Kind != domain.OfferKindProvisionable {
		t.Errorf("the listing is now %+v, and a decision has to see it to record refusing it", listing)
	}
	if listing.RentalID != "" {
		t.Errorf("the listing names Rental %q, and a listing is a product rather than a lease", listing.RentalID)
	}
	if listing.Capacity.Available {
		t.Errorf("the listing still sells machine %q, which this workspace is already leasing", listing.MachineID)
	}
}

// TestWorldRefusesToSellOneMachineTwice is the rule the publication above rests
// on, said where a caller can be refused by it. A listing that names a machine is
// a listing of that machine, and two listings of it are two names for capacity
// that can be bought once. Allocating it twice handed Mercator a lease on a host
// another lease was already running work on, and the second publication then
// wiped the first machine's warm content, its busy window and its Rental
// identity, so a world that erased what a machine held on the next purchase was
// the evidence the reuse corpus was reading.
func TestWorldRefusesToSellOneMachineTwice(t *testing.T) {
	world, _ := listedWorld(t)
	provisionAndEnrol(t, world, "rnt_first", "own-first")
	launch := worldLaunch("reusable-4090", "trainer:v1")
	launch.OwnershipToken = "own-first"
	if _, err := world.Launch(context.Background(), launch); err != nil {
		t.Fatalf("launch: %v", err)
	}
	world.Clock().Advance(time.Hour)

	_, err := world.ProvisionCapacity(context.Background(), capability.ProvisionCommand{
		WorkspaceID:     "ws_fake",
		RentalID:        "rnt_second",
		OfferSnapshotID: "reusable-4090",
		OwnershipToken:  "own-second",
		Bootstrap:       capability.NodeBootstrap{NodeID: "nod_second", RentalID: "rnt_second", Generation: 1},
	})

	if err == nil {
		t.Fatal("this world sold one machine to two Rentals")
	}
	if !strings.Contains(err.Error(), `Rental "rnt_first" is already holding`) {
		t.Fatalf("refusal = %q, want the lease that is already holding the machine", err)
	}
	machine := worldOffers(t, world)[listedMachine]
	if machine.RentalID != "rnt_first" {
		t.Errorf("the machine is held under Rental %q, want the lease that bought it", machine.RentalID)
	}
	if !machine.Images.HoldsLayer(domain.ImageLayer{Digest: "layer-base"}) {
		t.Errorf("the machine holds %+v, want the content its own Run left on it", machine.Images)
	}
}

// TestWorldMintsAHandleForTheMachineAProductYields is the other half of naming a
// machine. A listing that names none is a product rather than a host, so the
// machine it yields is one nothing has named yet and this provider mints a
// handle for it. Publishing it under the listing's own ID replaced the product:
// the catalog entry stopped being sold, the provisioning stages it published
// vanished, and a second purchase from it overwrote the first machine.
func TestWorldMintsAHandleForTheMachineAProductYields(t *testing.T) {
	world := newLayeredWorld(t)
	world.Enroller = &acceptingRegistry{}
	if err := world.AddMachine(&Machine{
		Offer: domain.OfferSnapshot{
			ID:        "fresh-4090",
			Kind:      domain.OfferKindProvisionable,
			Lane:      domain.LaneReusable,
			Resources: domain.ResourceInventory{EphemeralDiskBytes: 200 << 30, EphemeralDiskKnown: true},
		},
	}); err != nil {
		t.Fatalf("add listing: %v", err)
	}

	for _, rentalID := range []string{"rnt_first", "rnt_second"} {
		if _, err := world.ProvisionCapacity(context.Background(), capability.ProvisionCommand{
			WorkspaceID:     "ws_fake",
			RentalID:        rentalID,
			OfferSnapshotID: "fresh-4090",
			OwnershipToken:  "own-" + rentalID,
			Bootstrap:       capability.NodeBootstrap{NodeID: "nod_" + rentalID, RentalID: rentalID, Generation: 1},
		}); err != nil {
			t.Fatalf("provision %s: %v", rentalID, err)
		}
	}
	if err := world.DeliverEnrolments(context.Background()); err != nil {
		t.Fatalf("deliver enrolments: %v", err)
	}

	offers := worldOffers(t, world)
	listing, stillSold := offers["fresh-4090"]
	if !stillSold || listing.Kind != domain.OfferKindProvisionable || !listing.Capacity.Available {
		t.Fatalf("the product is now %+v, and a product goes on being sold however many machines it yielded", listing)
	}
	if len(offers) != 3 {
		t.Fatalf("the world publishes %d offers, want the product and one machine per lease: %v", len(offers), offers)
	}
	for _, rentalID := range []string{"rnt_first", "rnt_second"} {
		machine, published := offers["mch_"+rentalID]
		if !published {
			t.Fatalf("Rental %q has no machine of its own: %v", rentalID, offers)
		}
		if machine.RentalID != rentalID {
			t.Errorf("machine %q is held under Rental %q, want %q", machine.ID, machine.RentalID, rentalID)
		}
	}
}

// TestWorldStopsPublishingAMachineItDestroyed is the inverse of the publication,
// and the reclaim half of a machine's life. Without it a provider went on
// advertising available standing reusable capacity that no longer existed while
// ListOwnedCapacity in the same world reported nothing owned, so a later Run
// could be placed on, and recorded as having successfully executed on, a host
// nobody is billed for. The listing comes back with it, because the product is on
// sale again the moment the lease ends.
func TestWorldStopsPublishingAMachineItDestroyed(t *testing.T) {
	world, _ := listedWorld(t)
	provisionAndEnrol(t, world, "rnt_first", "own-first")

	if _, err := world.TerminateCapacity(context.Background(), capability.CapacityCommand{
		CapacityRef: capability.CapacityRef{RentalID: "rnt_first"},
	}); err != nil {
		t.Fatalf("terminate: %v", err)
	}

	offers := worldOffers(t, world)
	if machine, published := offers[listedMachine]; published {
		t.Errorf("a destroyed machine still publishes %+v", machine)
	}
	if listing := offers["reusable-4090"]; !listing.Capacity.Available {
		t.Errorf("the listing is still refused as sold, and Mercator gave the machine back")
	}
}

// TestWorldSellsNoCapacityItHasAlreadyLeased holds ListCapacity to what it is.
// The fleet and the catalogue are two questions: ListOffers answers what this
// world can be asked to run work on, and ListCapacity answers what is for sale.
// Returning the fleet from both offered a workspace a machine it is already
// paying for, under a lease it already holds.
func TestWorldSellsNoCapacityItHasAlreadyLeased(t *testing.T) {
	world, _ := listedWorld(t)
	provisionAndEnrol(t, world, "rnt_first", "own-first")

	forSale, err := world.ListCapacity(context.Background(), capability.CapacityQuery{WorkspaceID: "ws_fake"})
	if err != nil {
		t.Fatalf("list capacity: %v", err)
	}

	for _, offer := range forSale {
		if offer.RentalID != "" {
			t.Errorf("capacity for sale includes %q, held under Rental %q", offer.ID, offer.RentalID)
		}
	}
	if len(forSale) != 1 || forSale[0].ID != "reusable-4090" {
		t.Fatalf("capacity for sale = %+v, want the listing alone", forSale)
	}
}

// TestWorldRunsAListingsWorkloadOnTheMachineItWasAllocatedInto is what makes the
// machine above worth publishing. A Run placed on a listing is launched at the
// listing, and it runs on the machine that listing was allocated into for that
// very attempt, so what it fetched is on the machine the next Run can find and
// not on a product nobody can run anything on.
func TestWorldRunsAListingsWorkloadOnTheMachineItWasAllocatedInto(t *testing.T) {
	world, _ := listedWorld(t)
	provisionAndEnrol(t, world, "rnt_first", "own-first")

	launch := worldLaunch("reusable-4090", "trainer:v1")
	launch.OwnershipToken = "own-first"
	if _, err := world.Launch(context.Background(), launch); err != nil {
		t.Fatalf("launch: %v", err)
	}
	world.Clock().Advance(time.Hour)

	offers := worldOffers(t, world)
	if held := offers[listedMachine].Images; !held.HoldsLayer(domain.ImageLayer{Digest: "layer-base"}) {
		t.Fatalf("the machine that ran the image holds %+v, and it is the only host there was", held)
	}
	if held := offers["reusable-4090"].Images; held.Known {
		t.Errorf("the listing enumerates %+v, and nothing of Mercator's runs on a product", held)
	}
}

// TestWorldEndsOnlyTheWorkloadsAFixtureTimed pins both halves of the one way a
// workload here ever finishes. How long work takes is a fact about the work, so
// a Blueprint that states one gets a workload that exits and a machine that is
// free again afterwards, and a Blueprint that states none gets what this world
// has always given it: a workload still running for as long as the scenario
// lasts.
func TestWorldEndsOnlyTheWorkloadsAFixtureTimed(t *testing.T) {
	world, _ := listedWorld(t)
	provisionAndEnrol(t, world, "rnt_first", "own-first")
	world.DefineRuntime("run-timed", "reusable-4090", 10*time.Minute)

	for _, workload := range []struct {
		name  string
		runID string
		want  adapter.ExternalPhase
	}{
		{name: "a workload the Blueprint timed", runID: "run-timed", want: adapter.ExternalPhaseSucceeded},
		{name: "a workload nobody timed", runID: "run-open", want: adapter.ExternalPhaseRunning},
	} {
		t.Run(workload.name, func(t *testing.T) {
			launch := worldLaunch("reusable-4090", "trainer:v1")
			launch.RunID = workload.runID
			launch.LaunchKey = "launch-" + workload.runID
			launch.OperationKey = "launch:" + workload.runID
			launch.OwnershipToken = "own-first"
			if _, err := world.Launch(context.Background(), launch); err != nil {
				t.Fatalf("launch: %v", err)
			}
			world.Clock().Advance(time.Hour)

			observation, err := world.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: launch.LaunchKey})
			if err != nil {
				t.Fatalf("observe: %v", err)
			}
			if observation.Phase != workload.want {
				t.Fatalf("%s reports %q an hour later, want %q", workload.name, observation.Phase, workload.want)
			}
		})
	}
}
