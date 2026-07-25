package node_test

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/node"
)

const (
	baseLayer = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	topLayer  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	trainerV2 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// What the arm64 build of the same image is made of. One index digest names
	// one image per platform, so two builds share a name and no bytes.
	armBaseLayer = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

// hostPlatform is the build this node runs, and therefore the build a Run
// placed here is pinned to.
var hostPlatform = domain.Platform{OS: "linux", Architecture: "amd64"}

// TestANodeOffersTheContentItActuallyHolds is the guard on the defect this
// contract exists to delete. An offer used to report zero missing bytes
// whatever the machine held, so a node holding nothing at all looked fully
// warm and was priced as an instant start.
func TestANodeOffersTheContentItActuallyHolds(t *testing.T) {
	cases := map[string]struct {
		held       []capability.ImageLocality
		wantWhole  bool
		wantPulled bool
		wantLayer  bool
	}{
		"a node holding the exact image ready to run reports it whole": {
			held: []capability.ImageLocality{{
				ManifestDigest: trainerV2,
				Platform:       hostPlatform,
				State:          domain.LocalityHot,
				Unpacked:       true,
				LayerDigests:   []string{baseLayer, topLayer},
			}},
			wantWhole: true,
			wantLayer: true,
		},
		// Every byte is on the machine and no container can be started on it.
		// Reported as held whole it would be priced an instant start; reported
		// nowhere it would be priced a pull of content that is already here.
		"a node that fetched an image and never unpacked it reports it pulled": {
			held: []capability.ImageLocality{{
				ManifestDigest: trainerV2,
				Platform:       hostPlatform,
				State:          domain.LocalityCold,
			}},
			wantWhole:  false,
			wantPulled: true,
			wantLayer:  false,
		},
		"a node part way through unpacking reports its layers and the image as pulled": {
			held: []capability.ImageLocality{{
				ManifestDigest: trainerV2,
				Platform:       hostPlatform,
				State:          domain.LocalityPartial,
				LayerDigests:   []string{baseLayer},
			}},
			wantWhole:  false,
			wantPulled: true,
			wantLayer:  true,
		},
		// The same digest, another machine's build of it. Reading the name alone
		// would price this host as holding an image it cannot run and has none
		// of the bytes of.
		"a node holding another platform's build of that digest does not hold the image": {
			held: []capability.ImageLocality{{
				ManifestDigest: trainerV2,
				Platform:       domain.Platform{OS: "linux", Architecture: "arm64"},
				State:          domain.LocalityHot,
				Unpacked:       true,
				LayerDigests:   []string{armBaseLayer},
			}},
			wantWhole: false,
			wantLayer: false,
		},
		// An image the daemon listed and would not describe. It is here and
		// nothing can say what it is, which is uncertainty rather than warmth.
		"a node holding an image it could not describe does not report holding it": {
			held:      []capability.ImageLocality{{ManifestDigest: trainerV2, State: domain.LocalityUnknown}},
			wantWhole: false,
			wantLayer: false,
		},
		"a node holding nothing reports nothing rather than looking warm": {
			held:      nil,
			wantWhole: false,
			wantLayer: false,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			registry := readyNode(t, testCase.held)

			offers, err := registry.Offers(context.Background(), nodeWorkspace)
			if err != nil {
				t.Fatalf("list node offers: %v", err)
			}

			if len(offers) != 1 {
				t.Fatalf("offers = %d, want the one ready node", len(offers))
			}
			inventory := offers[0].Images
			if !inventory.Known {
				t.Fatal("an enrolled node always enumerates, so its inventory is never silent")
			}
			if inventory.Holds(trainerV2) != testCase.wantWhole {
				t.Errorf("holds the whole image = %v, want %v", inventory.Holds(trainerV2), testCase.wantWhole)
			}
			if inventory.Pulled(trainerV2) != testCase.wantPulled {
				t.Errorf("pulled and not assembled = %v, want %v", inventory.Pulled(trainerV2), testCase.wantPulled)
			}
			if got := inventory.HoldsLayer(domain.ImageLayer{Digest: baseLayer}); got != testCase.wantLayer {
				t.Errorf("holds the base layer = %v, want %v", got, testCase.wantLayer)
			}
		})
	}
}

// TestANodeStopsStandingBehindWhatItSaidWhenItStopsLooking is why an inventory
// carries a validity and not only an age. A node states what it holds as of a
// heartbeat; a decision made long enough after that heartbeat is made against a
// machine nobody has heard from, and reading the old answer as warmth is
// betting a placement on content that may have been reclaimed an hour ago.
func TestANodeStopsStandingBehindWhatItSaidWhenItStopsLooking(t *testing.T) {
	registry := readyNode(t, []capability.ImageLocality{{
		ManifestDigest: trainerV2,
		Platform:       hostPlatform,
		State:          domain.LocalityHot,
		Unpacked:       true,
	}})
	offers, err := registry.Offers(context.Background(), nodeWorkspace)
	if err != nil {
		t.Fatalf("list node offers: %v", err)
	}
	inventory := offers[0].Images

	if !inventory.Answers(inventory.ObservedAt.Add(time.Second)) {
		t.Fatal("a node's own heartbeat is an answer a second after it arrives")
	}
	if inventory.Answers(inventory.ValidUntil.Add(time.Second)) {
		t.Fatalf("the node still answered past %s, so nothing bounds how stale a warm claim may be", inventory.ValidUntil)
	}
}

// TestWhatANodeHoldsDecidesWhatItStillHasToDo is the join this contract moved to
// the scheduler: the offer says what is here, the manifest says what is needed,
// and the subtraction happens where both are known. What comes back is two
// kinds of work, because bytes to fetch and bytes to assemble are answered by
// different machines at different speeds.
func TestWhatANodeHoldsDecidesWhatItStillHasToDo(t *testing.T) {
	manifest := domain.ImageManifest{
		Known:  true,
		Digest: trainerV2,
		Layers: []domain.ImageLayer{
			{Digest: baseLayer, CompressedBytes: 18_000_000_000},
			{Digest: topLayer, CompressedBytes: 80_000_000},
		},
	}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		inventory    domain.ImageInventory
		wantWork     domain.ImageWork
		wantLocality domain.LocalityState
	}{
		"a host holding the whole image does nothing": {
			inventory:    domain.ImageInventory{Known: true, ImageDigests: []string{trainerV2}},
			wantLocality: domain.LocalityHot,
		},
		"a host holding the shared base fetches only the top layer": {
			inventory:    domain.ImageInventory{Known: true, LayerDigests: []string{baseLayer}},
			wantWork:     domain.ImageWork{TransferBytes: 80_000_000},
			wantLocality: domain.LocalityPartial,
		},
		"a host that fetched the image and never assembled it unpacks and fetches nothing": {
			inventory:    domain.ImageInventory{Known: true, PulledImageDigests: []string{trainerV2}},
			wantWork:     domain.ImageWork{UnpackBytes: 18_080_000_000},
			wantLocality: domain.LocalityPartial,
		},
		"a host part way through assembly unpacks only what is left": {
			inventory: domain.ImageInventory{
				Known:              true,
				PulledImageDigests: []string{trainerV2},
				LayerDigests:       []string{baseLayer},
			},
			wantWork:     domain.ImageWork{UnpackBytes: 80_000_000},
			wantLocality: domain.LocalityPartial,
		},
		"a host holding nothing fetches all of it": {
			inventory:    domain.ImageInventory{Known: true},
			wantWork:     domain.ImageWork{TransferBytes: 18_080_000_000},
			wantLocality: domain.LocalityCold,
		},
		"a host that cannot say is unknown rather than free": {
			inventory:    domain.ImageInventory{Known: false},
			wantLocality: domain.LocalityUnknown,
		},
		"a host that stopped standing behind its answer is unknown rather than warm": {
			inventory: domain.ImageInventory{
				Known:        true,
				ImageDigests: []string{trainerV2},
				ValidUntil:   now.Add(-time.Second),
			},
			wantLocality: domain.LocalityUnknown,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			work, locality := manifest.StartWork(now, testCase.inventory)

			if work != testCase.wantWork || locality != testCase.wantLocality {
				t.Fatalf("start work = %+v (%q), want %+v (%q)", work, locality, testCase.wantWork, testCase.wantLocality)
			}
		})
	}
}

// TestAManifestWithoutLayersCanConfirmAHitAndCannotPriceAMiss holds the line
// against the same lie reappearing one level up: an empty layer set would
// charge a host holding nothing the same zero as one holding everything.
func TestAManifestWithoutLayersCanConfirmAHitAndCannotPriceAMiss(t *testing.T) {
	layerless := domain.ImageManifest{Known: true, Digest: trainerV2}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	_, hit := layerless.StartWork(now, domain.ImageInventory{Known: true, ImageDigests: []string{trainerV2}})
	_, miss := layerless.StartWork(now, domain.ImageInventory{Known: true})

	if hit != domain.LocalityHot {
		t.Fatalf("a host holding the image is %q, want hot", hit)
	}
	if miss != domain.LocalityUnknown {
		t.Fatalf("a host holding nothing is %q against a manifest that lists no layers, want unknown", miss)
	}
}

// TestAnUnresolvedManifestLeavesEveryCandidateIndistinguishable states why an
// unknown manifest is safe: nobody can be told apart on locality, so the term
// is absent for everyone rather than favouring whoever happens to report most.
func TestAnUnresolvedManifestLeavesEveryCandidateIndistinguishable(t *testing.T) {
	unresolved := domain.ImageManifest{}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	warm := domain.ImageInventory{Known: true, ImageDigests: []string{trainerV2}}
	cold := domain.ImageInventory{Known: true}

	warmWork, warmLocality := unresolved.StartWork(now, warm)
	coldWork, coldLocality := unresolved.StartWork(now, cold)

	if warmLocality != domain.LocalityUnknown || coldLocality != domain.LocalityUnknown {
		t.Fatalf("an unresolved manifest produced %q and %q, want unknown for everyone", warmLocality, coldLocality)
	}
	if warmWork != coldWork {
		t.Fatalf("a warm host owes %+v and a cold one %+v under an unresolved manifest", warmWork, coldWork)
	}
}

const nodeWorkspace = "ws_offers"

func readyNode(t *testing.T, held []capability.ImageLocality) *node.Registry {
	t.Helper()
	registry, clock := newRegistry(t)
	bootstrap, err := registry.Invite(context.Background(), node.Invitation{
		WorkspaceID: nodeWorkspace, NodeID: "nod_offers", RentalID: "rnt_offers", Generation: 1,
		ShadowPriceUSDPerHour: 2,
	})
	if err != nil {
		t.Fatalf("invite node: %v", err)
	}
	request := capability.EnrollmentRequest{
		NodeID:          bootstrap.NodeID,
		RentalID:        bootstrap.RentalID,
		Generation:      bootstrap.Generation,
		EnrollmentToken: bootstrap.EnrollmentToken,
		AgentVersion:    "test",
		Facts: capability.NodeFacts{
			ObservedAt: clock.Now(),
			Host:       capability.HostFacts{OS: "linux", Architecture: "amd64", ContainerRuntime: "docker"},
			Images:     held,
		},
	}
	if _, err := registry.Enroll(context.Background(), request); err != nil {
		t.Fatalf("enroll node: %v", err)
	}
	return registry
}
