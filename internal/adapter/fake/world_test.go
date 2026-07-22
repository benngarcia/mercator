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
	if err := world.SetPlacementImage("trainer:v1"); err != nil {
		t.Fatalf("set placement image: %v", err)
	}
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

func TestWorldIdleDaemonAdvertisesHonestLayerEvidence(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddDaemon(&Daemon{
		Offer:      domain.OfferSnapshot{ID: "rental-warm"},
		HeldLayers: map[string]int64{"layer-base": 1000},
	}); err != nil {
		t.Fatalf("add daemon: %v", err)
	}

	offer, ok := worldOffers(t, world)["rental-warm"]
	if !ok {
		t.Fatalf("expected rental-warm to be offered")
	}
	if offer.Kind != domain.OfferKindStanding {
		t.Fatalf("daemon offer kind = %q, want standing", offer.Kind)
	}
	if !offer.Capacity.Available {
		t.Fatalf("idle daemon must advertise available capacity")
	}
	if got := offer.ImageCache; !got.Known || got.MissingBytes != 10 || got.ManifestCached {
		t.Fatalf("image cache evidence = %+v, want known with 10 missing bytes", got)
	}
	if !offer.ExpiresAt.After(worldStart) {
		t.Fatalf("offer must expire in the scripted future, got %v", offer.ExpiresAt)
	}
}

func TestWorldBusyDaemonAdvertisesRemainingMaxRuntime(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddDaemon(&Daemon{
		Offer:     domain.OfferSnapshot{ID: "rental-busy"},
		BusyUntil: worldStart.Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("add daemon: %v", err)
	}

	offer := worldOffers(t, world)["rental-busy"]
	if offer.Capacity.Available {
		t.Fatalf("busy daemon must advertise unavailable capacity")
	}
	if offer.Queue == nil || offer.Queue.QueuedWorkSeconds != 600 {
		t.Fatalf("queue evidence = %+v, want 600 remaining seconds", offer.Queue)
	}

	world.Clock().Advance(10 * time.Minute)
	offer = worldOffers(t, world)["rental-busy"]
	if !offer.Capacity.Available {
		t.Fatalf("daemon must free once its running work's max runtime elapses")
	}
}

func TestWorldFreesAtHoldsDaemonBusyPastMaxRuntime(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddDaemon(&Daemon{
		Offer:     domain.OfferSnapshot{ID: "rental-lagged"},
		BusyUntil: worldStart.Add(5 * time.Minute),
		FreesAt:   worldStart.Add(20 * time.Minute),
	}); err != nil {
		t.Fatalf("add daemon: %v", err)
	}

	world.Clock().Advance(6 * time.Minute)
	offer := worldOffers(t, world)["rental-lagged"]
	if offer.Capacity.Available {
		t.Fatalf("daemon must stay busy until FreesAt")
	}
	if offer.Queue.QueuedWorkSeconds != 0 {
		t.Fatalf("remaining max runtime already elapsed, queue evidence = %+v", offer.Queue)
	}
}

func TestWorldExpiredLeaseRemovesDaemonFromOffers(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddDaemon(&Daemon{
		Offer:          domain.OfferSnapshot{ID: "rental-leased"},
		LeaseExpiresAt: worldStart.Add(time.Minute),
	}); err != nil {
		t.Fatalf("add daemon: %v", err)
	}

	if _, ok := worldOffers(t, world)["rental-leased"]; !ok {
		t.Fatalf("daemon inside its idle lease must be offered")
	}
	world.Clock().Advance(time.Minute)
	if _, ok := worldOffers(t, world)["rental-leased"]; ok {
		t.Fatalf("daemon past its idle lease must stop being offered")
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
	if got := offer.ImageCache; !got.Known || got.MissingBytes != 1010 {
		t.Fatalf("image cache evidence = %+v, want the full 1010 bytes missing", got)
	}
}

func TestWorldRequiresPlacementImageWhenImagesDefined(t *testing.T) {
	world := NewWorld(NewClock(worldStart))
	world.DefineImage("trainer:v1", []Layer{{Digest: "layer-base", Bytes: 1000}})

	if _, err := world.ListOffers(context.Background(), adapter.OfferRequest{}); err == nil {
		t.Fatalf("listing offers without a placement image must fail loudly")
	}
}
