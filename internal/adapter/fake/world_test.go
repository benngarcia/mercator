package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/ociresolver"
)

var worldStart = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// wideImage is digest-pinned, like every reference Mercator places. A tag names
// no content, so a host could not report holding it and a manifest could not
// name it.
const wideImage = "wide@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

func newLayeredWorld(t *testing.T) *World {
	t.Helper()
	world := NewWorld(NewClock(worldStart))
	world.DefineImage("trainer:v1", Image{Layers: []Layer{
		{Digest: "layer-base", Bytes: 1000},
		{Digest: "layer-top", Bytes: 10},
	}})
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
		Offer:      rentalOffer("rental-warm"),
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
	if got := offer.Images; !got.Known || !got.HoldsLayer(domain.ImageLayer{Digest: "layer-base"}) || got.HoldsLayer(domain.ImageLayer{Digest: "layer-top"}) {
		t.Fatalf("inventory = %+v, want the base layer held and the top layer missing", got)
	}
	if !offer.ExpiresAt.After(worldStart) {
		t.Fatalf("offer must expire in the scripted future, got %v", offer.ExpiresAt)
	}
}

func TestWorldBusyMachineAdvertisesRemainingMaxRuntime(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddMachine(&Machine{
		Offer:     rentalOffer("rental-busy"),
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
		Offer:     rentalOffer("rental-lagged"),
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
		Offer:          rentalOffer("rental-leased"),
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

// TestWorldMarketplaceOfferOwesFullImagePull is the offer for a machine that
// does not exist yet. Nothing is running on it to be asked what it holds, so it
// enumerates nothing: "I looked and found nothing" is a confident claim, and a
// world that made it about a VM nobody has created would be lending Placement a
// certainty no deployment has.
func TestWorldMarketplaceOfferOwesFullImagePull(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddMachine(&Machine{Offer: domain.OfferSnapshot{
		ID:   "fresh-vm",
		Kind: domain.OfferKindProvisionable,
		Lane: domain.LaneReusable,
	}}); err != nil {
		t.Fatalf("add marketplace offer: %v", err)
	}

	offer := worldOffers(t, world)["fresh-vm"]
	if offer.Kind != domain.OfferKindProvisionable {
		t.Fatalf("marketplace offer kind = %q, want provisionable", offer.Kind)
	}
	if got := offer.Images; got.Known || len(got.LayerDigests) != 0 {
		t.Fatalf("inventory = %+v, want no enumeration at all of a machine that does not exist yet", got)
	}
}

// TestWorldHoldsARunningImageOnlyOnceItsBytesHaveArrived keeps the world from
// blessing locality the machine does not have yet. The pull is 1010 bytes at
// 500Mbps, so the image is on the host in well under a second and is provably
// absent at the instant the container was dispatched.
func TestWorldHoldsARunningImageOnlyOnceItsBytesHaveArrived(t *testing.T) {
	world := newLayeredWorld(t)
	world.DefineImage(wideImage, Image{Layers: []Layer{{Digest: "layer-wide", Bytes: 125_000_000}}})
	if err := world.AddMachine(&Machine{Offer: rentalOffer("rental-cold")}); err != nil {
		t.Fatalf("add machine: %v", err)
	}

	if _, err := world.Launch(context.Background(), worldLaunch("rental-cold", wideImage)); err != nil {
		t.Fatalf("launch: %v", err)
	}

	if held := worldOffers(t, world)["rental-cold"].Images; held.Holds(domain.ReferenceDigest(wideImage)) {
		t.Fatalf("the host holds the image at dispatch, before any byte moved: %+v", held)
	}
	world.Clock().Advance(2 * time.Second)
	if held := worldOffers(t, world)["rental-cold"].Images; !held.Holds(domain.ReferenceDigest(wideImage)) || !held.HoldsLayer(domain.ImageLayer{Digest: "layer-wide"}) {
		t.Fatalf("the host does not hold what it finished pulling: %+v", held)
	}
}

// TestWorldCapacityItDoesNotKeepHoldsNothingItRan covers both halves of the
// line, because each half is a different reason. A machine that does not exist
// yet has nowhere to put the content; a host Mercator borrows a slot on exists
// and keeps running, but nothing Mercator has enrolled on it can hold anything
// for the next Run.
//
// It reads the machine rather than its offer. No offer for capacity like this
// carries an inventory at all, because nothing of Mercator's is there to
// enumerate it, so an offer would look the same whether the machine kept
// everything or nothing. The Rental beside them runs the same image and keeps
// it, which is what makes this an answer about the lane and not about the pull.
func TestWorldCapacityItDoesNotKeepHoldsNothingItRan(t *testing.T) {
	for _, capacity := range []struct {
		name  string
		offer domain.OfferSnapshot
	}{
		{
			name:  "a machine that does not exist yet",
			offer: domain.OfferSnapshot{ID: "oneshot", Kind: domain.OfferKindProvisionable, Lane: domain.LaneEphemeral},
		},
		{
			name:  "a host Mercator has not enrolled",
			offer: domain.OfferSnapshot{ID: "local-docker", Kind: domain.OfferKindStanding, Lane: domain.LaneEphemeral},
		},
	} {
		t.Run(capacity.name, func(t *testing.T) {
			world := newLayeredWorld(t)
			borrowed := &Machine{Offer: capacity.offer}
			kept := &Machine{Offer: rentalOffer("rental-kept")}
			for _, machine := range []*Machine{borrowed, kept} {
				if err := world.AddMachine(machine); err != nil {
					t.Fatalf("add machine: %v", err)
				}
				if _, err := world.Launch(context.Background(), worldLaunch(machine.Offer.ID, "trainer:v1")); err != nil {
					t.Fatalf("launch on %s: %v", machine.Offer.ID, err)
				}
			}

			world.Clock().Advance(time.Hour)
			// Listing offers is what makes this world apply the pulls that have
			// landed, and what each machine kept is readable only afterwards.
			worldOffers(t, world)

			now := world.Clock().Now()
			if held := kept.inventory(now); !held.HoldsLayer(domain.ImageLayer{Digest: "layer-base"}) {
				t.Fatalf("the Rental that ran the image holds %+v, and capacity Mercator keeps keeps what it ran", held)
			}
			if held := borrowed.inventory(now); len(held.ImageDigests) > 0 || len(held.LayerDigests) > 0 {
				t.Fatalf("%s held %+v an hour after its workload", capacity.name, held)
			}
		})
	}
}

// TestWorldGrantsRentalIdentityOnlyToCapacityItKeeps mirrors the production
// stamp: an offer that cannot hold a second Run cannot name a Rental either.
func TestWorldGrantsRentalIdentityOnlyToCapacityItKeeps(t *testing.T) {
	world := newLayeredWorld(t)
	if err := world.AddMachine(&Machine{Offer: rentalOffer("rental-kept")}); err != nil {
		t.Fatalf("add rental: %v", err)
	}
	if err := world.AddMachine(&Machine{Offer: domain.OfferSnapshot{
		ID:       "local-docker",
		Kind:     domain.OfferKindStanding,
		Lane:     domain.LaneEphemeral,
		RentalID: "local-docker",
	}}); err != nil {
		t.Fatalf("add host: %v", err)
	}

	offers := worldOffers(t, world)

	if offers["rental-kept"].RentalID != "rental-kept" {
		t.Fatalf("a Rental must name itself, got %q", offers["rental-kept"].RentalID)
	}
	if offers["local-docker"].RentalID != "" {
		t.Fatalf("capacity Mercator does not keep claimed Rental %q", offers["local-docker"].RentalID)
	}
}

func TestWorldRefusesCapacityThatNamesNoKindOrLane(t *testing.T) {
	world := newLayeredWorld(t)

	err := world.AddMachine(&Machine{Offer: domain.OfferSnapshot{ID: "nameless"}})

	if err == nil {
		t.Fatal("capacity that states neither what it is nor what it does was accepted")
	}
}

// rentalOffer is capacity Mercator holds: a machine that exists, with an
// enrolled runtime that can execute successive workloads on it.
// rentalOffer is a Rental with room for whatever a case puts on it. The disk is
// stated because content has to fit somewhere: a machine holding layers on no
// disk is a machine this world refuses to build.
func rentalOffer(id string) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:        id,
		Kind:      domain.OfferKindStanding,
		Lane:      domain.LaneReusable,
		Resources: domain.ResourceInventory{EphemeralDiskBytes: 200 << 30, EphemeralDiskKnown: true},
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

// TestSimulatedRegistryRefusesTheSameThreeWaysARealOneDoes keeps this world
// honest about what it stands in for. Collapsing "nobody pushed this", "there
// is no build for this platform", and "your credentials were refused" into one
// empty manifest hides the failure an operator most often has to fix.
func TestSimulatedRegistryRefusesTheSameThreeWaysARealOneDoes(t *testing.T) {
	world := NewWorld(NewClock(worldStart))
	world.DefineImage("mystery:v1", Image{Registry: RegistryUnresolvable})
	world.DefineImage("private:v1", Image{Registry: RegistryUnauthorized})

	testCases := []struct {
		name  string
		image string
		want  error
	}{
		{"an image nobody pushed", "absent:v1", ociresolver.ErrImageUnknown},
		{"an image with no resolvable manifest", "mystery:v1", ociresolver.ErrManifestUnresolvable},
		{"an image the credentials cannot read", "private:v1", ociresolver.ErrUnauthorized},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := world.ResolveManifest(context.Background(), testCase.image, domain.Platform{OS: "linux", Architecture: "amd64"})

			if !errors.Is(err, testCase.want) {
				t.Fatalf("resolve error = %v, want %v", err, testCase.want)
			}
		})
	}
}

// TestADockerHostIsWarmAgainstAManifestItCannotPronounce is the digest-space
// bridge at the world seam. The machine reports only diff IDs, which is all a
// container daemon has once it has unpacked an image, and the manifest lists
// only compressed blob digests. Nothing transfers because the manifest carries
// both names for the same bytes.
func TestADockerHostIsWarmAgainstAManifestItCannotPronounce(t *testing.T) {
	world := NewWorld(NewClock(worldStart))
	world.DefineImage("trainer:v1", Image{Layers: []Layer{
		{Digest: "sha256:blob-base", DiffID: "sha256:diff-base", Bytes: 1000},
		{Digest: "sha256:blob-top", DiffID: "sha256:diff-top", Bytes: 10},
	}})
	machine := &Machine{Offer: rentalOffer("rental-docker"), ReportsDiffIDs: true}
	machine.Hold(Layer{Digest: "sha256:blob-base", DiffID: "sha256:diff-base", Bytes: 1000})
	machine.Hold(Layer{Digest: "sha256:blob-top", DiffID: "sha256:diff-top", Bytes: 10})
	if err := world.AddMachine(machine); err != nil {
		t.Fatalf("add machine: %v", err)
	}

	inventory := worldOffers(t, world)["rental-docker"].Images
	manifest, err := world.ResolveManifest(context.Background(), "trainer:v1", domain.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	work, locality := manifest.StartWork(inventory)

	if len(inventory.LayerDigests) != 0 {
		t.Fatalf("a Docker host reported compressed blob digests it cannot see: %+v", inventory.LayerDigests)
	}
	if locality != domain.LocalityHot || !work.None() {
		t.Fatalf("the host owes %+v and is %q, want nothing and hot: it holds every layer", work, locality)
	}
}
