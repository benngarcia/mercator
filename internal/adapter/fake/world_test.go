package fake

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

var worldStart = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func newLayeredWorld(t *testing.T) *World {
	t.Helper()
	world := NewWorld(NewClock(worldStart))
	world.DefineImage("trainer:v1", []Layer{
		{Digest: "layer-base", Bytes: 1000},
		{Digest: "layer-top", Bytes: 10},
	})
	return world
}

func worldOffers(t *testing.T, world *World) map[string]domain.OfferSnapshot {
	t.Helper()
	offers, err := world.ListOffers(context.Background(), adapter.OfferRequest{})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	byID := make(map[string]domain.OfferSnapshot, len(offers))
	for _, offer := range offers {
		byID[offer.ID] = offer
	}
	return byID
}

func TestWorldIdleMachineAdvertisesHonestLayerEvidence(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddMachine(&Machine{
		Offer:      domain.OfferSnapshot{ID: "rental-warm"},
		HeldLayers: map[string]int64{"layer-base": 1000},
	}); err != nil {
		t.Fatalf("add machine: %v", err)
	}

	offer, ok := worldOffers(t, world)["rental-warm"]
	if !ok {
		t.Fatalf("expected rental-warm to be offered")
	}
	if offer.Kind != domain.OfferKindStanding {
		t.Fatalf("machine offer kind = %q, want standing", offer.Kind)
	}
	if !offer.Capacity.Available {
		t.Fatalf("idle machine must advertise available capacity")
	}
	// The machine holds the shared base layer and not the top one, which is what
	// makes the next version of the same image cheap to start here.
	if got := offer.Images; !got.Known || !got.HoldsLayer("layer-base") || got.HoldsLayer("layer-top") {
		t.Fatalf("inventory = %+v, want the base layer held and the top layer missing", got)
	}
	if !offer.ExpiresAt.After(worldStart) {
		t.Fatalf("offer must expire in the scripted future, got %v", offer.ExpiresAt)
	}
}

func TestWorldBusyMachineAdvertisesRemainingMaxRuntime(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddMachine(&Machine{
		Offer:     domain.OfferSnapshot{ID: "rental-busy"},
		BusyUntil: worldStart.Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("add machine: %v", err)
	}

	offer := worldOffers(t, world)["rental-busy"]
	if offer.Capacity.Available {
		t.Fatalf("busy machine must advertise unavailable capacity")
	}
	if offer.Queue == nil || offer.Queue.QueuedWorkSeconds != 600 {
		t.Fatalf("queue evidence = %+v, want 600 remaining seconds", offer.Queue)
	}

	world.Clock().Advance(10 * time.Minute)
	offer = worldOffers(t, world)["rental-busy"]
	if !offer.Capacity.Available {
		t.Fatalf("machine must free once its running work's max runtime elapses")
	}
}

func TestWorldFreesAtHoldsAMachineBusyPastMaxRuntime(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddMachine(&Machine{
		Offer:     domain.OfferSnapshot{ID: "rental-lagged"},
		BusyUntil: worldStart.Add(5 * time.Minute),
		FreesAt:   worldStart.Add(20 * time.Minute),
	}); err != nil {
		t.Fatalf("add machine: %v", err)
	}

	world.Clock().Advance(6 * time.Minute)
	offer := worldOffers(t, world)["rental-lagged"]
	if offer.Capacity.Available {
		t.Fatalf("machine must stay busy until FreesAt")
	}
	if offer.Queue.QueuedWorkSeconds != 0 {
		t.Fatalf("remaining max runtime already elapsed, queue evidence = %+v", offer.Queue)
	}
}

func TestWorldExpiredLeaseRemovesAMachineFromOffers(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddMachine(&Machine{
		Offer:          domain.OfferSnapshot{ID: "rental-leased"},
		LeaseExpiresAt: worldStart.Add(time.Minute),
	}); err != nil {
		t.Fatalf("add machine: %v", err)
	}

	if _, ok := worldOffers(t, world)["rental-leased"]; !ok {
		t.Fatalf("machine inside its idle lease must be offered")
	}
	world.Clock().Advance(time.Minute)
	if _, ok := worldOffers(t, world)["rental-leased"]; ok {
		t.Fatalf("machine past its idle lease must stop being offered")
	}
}

func TestWorldMarketplaceOfferOwesFullImagePull(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddMarketplaceOffer(domain.OfferSnapshot{ID: "fresh-vm"}); err != nil {
		t.Fatalf("add marketplace offer: %v", err)
	}

	offer := worldOffers(t, world)["fresh-vm"]
	if offer.Kind != domain.OfferKindProvisionable {
		t.Fatalf("marketplace offer kind = %q, want provisionable", offer.Kind)
	}
	if got := offer.Images; !got.Known || len(got.LayerDigests) != 0 {
		t.Fatalf("inventory = %+v, want a machine that does not exist yet holding nothing", got)
	}
}

// TestWorldHoldsARunningImageOnlyOnceItsBytesHaveArrived keeps the world from
// blessing locality the machine does not have yet. The pull is 1010 bytes at
// 500Mbps, so the image is on the host in well under a second and is provably
// absent at the instant the container was dispatched.
func TestWorldHoldsARunningImageOnlyOnceItsBytesHaveArrived(t *testing.T) {
	world := newLayeredWorld(t)
	world.DefineImage("wide:v1", []Layer{{Digest: "layer-wide", Bytes: 125_000_000}})
	if err := world.AddMachine(&Machine{Offer: domain.OfferSnapshot{ID: "rental-cold"}}); err != nil {
		t.Fatalf("add machine: %v", err)
	}

	if _, err := world.Launch(context.Background(), worldLaunch("rental-cold", "wide:v1")); err != nil {
		t.Fatalf("launch: %v", err)
	}

	if held := worldOffers(t, world)["rental-cold"].Images; held.Holds("wide:v1") {
		t.Fatalf("the host holds the image at dispatch, before any byte moved: %+v", held)
	}
	world.Clock().Advance(2 * time.Second)
	if held := worldOffers(t, world)["rental-cold"].Images; !held.Holds("wide:v1") || !held.HoldsLayer("layer-wide") {
		t.Fatalf("the host does not hold what it finished pulling: %+v", held)
	}
}

// TestWorldCapacityItDoesNotKeepHoldsNothingItRan is the lane's line. The same
// launch on a machine Mercator does not keep leaves nothing behind, because
// there is no host there to be holding it.
func TestWorldCapacityItDoesNotKeepHoldsNothingItRan(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddMarketplaceOffer(domain.OfferSnapshot{ID: "oneshot", Lane: domain.LaneEphemeral}); err != nil {
		t.Fatalf("add marketplace offer: %v", err)
	}

	if _, err := world.Launch(context.Background(), worldLaunch("oneshot", "trainer:v1")); err != nil {
		t.Fatalf("launch: %v", err)
	}
	world.Clock().Advance(time.Hour)

	held := worldOffers(t, world)["oneshot"].Images
	if len(held.ImageDigests) > 0 || len(held.LayerDigests) > 0 {
		t.Fatalf("capacity Mercator does not keep held %+v an hour after its workload", held)
	}
}

func TestWorldRefusesARentalOutsideTheReusableLane(t *testing.T) {
	world := newLayeredWorld(t)

	err := world.AddMachine(&Machine{Offer: domain.OfferSnapshot{ID: "rental-oneshot", Lane: domain.LaneEphemeral}})

	if err == nil {
		t.Fatal("a Rental in the ephemeral lane must be refused rather than silently corrected")
	}
}

func worldLaunch(offerID, image string) adapter.LaunchRequest {
	return adapter.LaunchRequest{
		OperationKey:            "launch:" + offerID + ":" + image,
		RequestHash:             "sha256:" + offerID,
		RunID:                   "run-" + offerID,
		AttemptID:               "attempt-" + offerID,
		LaunchKey:               "launch-" + offerID,
		OwnershipToken:          "owner-" + offerID,
		Image:                   image,
		SelectedOfferSnapshotID: offerID,
	}
}
