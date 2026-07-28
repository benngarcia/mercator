package fake

import (
	"context"
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
// the machine and against the lease the invitation named. The listing is
// untouched beside it, because a marketplace goes on selling the product a
// machine was allocated from.
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
	listing, stillSold := offers["reusable-4090"]
	if !stillSold || listing.Kind != domain.OfferKindProvisionable {
		t.Errorf("the listing is now %+v, and a marketplace goes on selling what it sold", listing)
	}
	if listing.RentalID != "" {
		t.Errorf("the listing names Rental %q, and a listing is a product rather than a lease", listing.RentalID)
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
