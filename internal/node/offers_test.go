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
		held        []capability.ImageLocality
		wantWhole   bool
		wantPulled  bool
		wantUnknown bool
		wantLayer   bool
	}{
		"a node holding the exact image ready to run reports it whole": {
			held: []capability.ImageLocality{{
				ManifestDigest: trainerV2,
				Platform:       hostPlatform,
				State:          domain.LocalityHot,
				ContentPresent: true,
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
				State:          domain.LocalityPartial,
				ContentPresent: true,
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
				ContentPresent: true,
				LayerDigests:   []string{baseLayer},
			}},
			wantWhole:  false,
			wantPulled: true,
			wantLayer:  true,
		},
		// The bytes of some layer are missing, so the machine owes a transfer
		// for it. Filing it as pulled would charge local assembly for content
		// nobody has fetched and block the Run on a pull the decision said was
		// disk work.
		"a node missing part of an image reports its layers and not the image": {
			held: []capability.ImageLocality{{
				ManifestDigest: trainerV2,
				Platform:       hostPlatform,
				State:          domain.LocalityPartial,
				LayerDigests:   []string{baseLayer},
			}},
			wantWhole:  false,
			wantPulled: false,
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
				ContentPresent: true,
				LayerDigests:   []string{armBaseLayer},
			}},
			wantWhole: false,
			wantLayer: false,
		},
		// An image the daemon listed and would not describe. It is here and
		// nothing can say what it is, which is uncertainty rather than warmth.
		// Reported nowhere it would be absence: everything else in this
		// inventory was enumerated, so an image missing from all of it reads as
		// the confident claim that the machine holds none of it, at the full
		// confidence of a node that measured its own link.
		"a node holding an image it could not describe reports that it could not": {
			held:        []capability.ImageLocality{{ManifestDigest: trainerV2, State: domain.LocalityUnknown}},
			wantWhole:   false,
			wantUnknown: true,
			wantLayer:   false,
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
			if inventory.Undescribed(trainerV2) != testCase.wantUnknown {
				t.Errorf("looked at and not accounted for = %v, want %v", inventory.Undescribed(trainerV2), testCase.wantUnknown)
			}
			if got := inventory.HoldsLayer(domain.ImageLayer{Digest: baseLayer}); got != testCase.wantLayer {
				t.Errorf("holds the base layer = %v, want %v", got, testCase.wantLayer)
			}
		})
	}
}

// TestANodeOfferStopsBeingSelectableBeforeItsFactsGoStale is the one bound on
// how old an answer about a machine may be. The inventory and the capacity claim
// come out of one heartbeat, so an offer built from facts nobody has refreshed
// expires whole rather than half: there is no moment at which Placement may
// choose this machine and disbelieve what it said it holds.
func TestANodeOfferStopsBeingSelectableBeforeItsFactsGoStale(t *testing.T) {
	registry := readyNode(t, []capability.ImageLocality{{
		ManifestDigest: trainerV2,
		Platform:       hostPlatform,
		State:          domain.LocalityHot,
		ContentPresent: true,
	}})
	offers, err := registry.Offers(context.Background(), nodeWorkspace)
	if err != nil {
		t.Fatalf("list node offers: %v", err)
	}
	offer := offers[0]

	if !offer.ExpiresAt.After(offer.Images.ObservedAt) {
		t.Fatal("a node offer expired at the instant its facts were observed")
	}
	if offer.Images.ObservedAt != offer.ObservedAt {
		t.Fatalf("the inventory was observed at %s and the offer at %s, so the two could disagree about the same machine",
			offer.Images.ObservedAt, offer.ObservedAt)
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
		// Silence is not warmth. The image has to arrive from somewhere and
		// nothing here says any of it is already on this machine, so a host
		// nobody can describe owes what a host holding nothing owes.
		"a host that cannot say owes the whole image rather than nothing": {
			inventory:    domain.ImageInventory{Known: false},
			wantWork:     domain.ImageWork{TransferBytes: 18_080_000_000},
			wantLocality: domain.LocalityUnknown,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			work, locality := manifest.StartWork(testCase.inventory)

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

	_, hit := layerless.StartWork(domain.ImageInventory{Known: true, ImageDigests: []string{trainerV2}})
	_, miss := layerless.StartWork(domain.ImageInventory{Known: true})

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
	warm := domain.ImageInventory{Known: true, ImageDigests: []string{trainerV2}}
	cold := domain.ImageInventory{Known: true}

	warmWork, warmLocality := unresolved.StartWork(warm)
	coldWork, coldLocality := unresolved.StartWork(cold)

	if warmLocality != domain.LocalityUnknown || coldLocality != domain.LocalityUnknown {
		t.Fatalf("an unresolved manifest produced %q and %q, want unknown for everyone", warmLocality, coldLocality)
	}
	if warmWork != coldWork {
		t.Fatalf("a warm host owes %+v and a cold one %+v under an unresolved manifest", warmWork, coldWork)
	}
}

const nodeWorkspace = "ws_offers"

// TestANodeOffersTheCopiesItHolds is the Artifact half of the same contract, and
// the same defect one layer along: the node reported its copies and the offer
// projection dropped them, so on the only reusable lane that exists every
// candidate was recorded holding nothing anybody could describe and charged the
// whole read for content already on its disk.
func TestANodeOffersTheCopiesItHolds(t *testing.T) {
	version := domain.ArtifactVersion{
		ID:            "artifact:imagenet:v2.41",
		WorkspaceID:   nodeWorkspace,
		ContentDigest: "sha256:1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a",
		SizeBytes:     40_000_000_000,
	}
	looked := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	reported := func(replica domain.ArtifactReplica) domain.ArtifactInventory {
		return domain.ArtifactInventory{
			Known:      true,
			ObservedAt: looked,
			Replicas:   []domain.ArtifactReplica{replica},
		}
	}
	cases := map[string]struct {
		held         domain.ArtifactInventory
		wantKnown    bool
		wantLocality domain.LocalityState
	}{
		"a node holding a checked copy of this version owes nothing for it": {
			held: reported(domain.ArtifactReplica{
				ArtifactID:    version.ID,
				ContentDigest: version.ContentDigest,
				SizeBytes:     version.SizeBytes,
				State:         domain.ArtifactReplicaVerified,
			}),
			wantKnown:    true,
			wantLocality: domain.LocalityHot,
		},
		"a copy nobody checked is worth what no copy is worth": {
			held: reported(domain.ArtifactReplica{
				ArtifactID:    version.ID,
				ContentDigest: version.ContentDigest,
				SizeBytes:     version.SizeBytes,
				State:         domain.ArtifactReplicaUnverified,
			}),
			wantKnown:    true,
			wantLocality: domain.LocalityCold,
		},
		// The machine an operator restored an older snapshot onto. Its index
		// still names this version and the bytes under it are another version's,
		// so reading the name alone promises the Run the wrong dataset quickly.
		"a checked copy of other content under this name is worth nothing": {
			held: reported(domain.ArtifactReplica{
				ArtifactID:    version.ID,
				ContentDigest: "sha256:2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b",
				SizeBytes:     version.SizeBytes,
				State:         domain.ArtifactReplicaVerified,
			}),
			wantKnown:    true,
			wantLocality: domain.LocalityCold,
		},
		"a node that enumerated and found nothing says it holds no copy": {
			held:         domain.ArtifactInventory{Known: true, ObservedAt: looked},
			wantKnown:    true,
			wantLocality: domain.LocalityCold,
		},
		// The only runtime in the tree. It has no replica store to look in, so
		// it claims nothing, and the offer must not claim it for it: every
		// enrolled node would otherwise assert that it holds no copy of content
		// it never looked for, which a start bound then strikes it out over.
		"a node whose runtime does not enumerate copies says nothing": {
			held:         domain.ArtifactInventory{},
			wantKnown:    false,
			wantLocality: domain.LocalityUnknown,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			registry := readyNodeHolding(t, nil, testCase.held)

			offers, err := registry.Offers(context.Background(), nodeWorkspace)
			if err != nil {
				t.Fatalf("list node offers: %v", err)
			}

			inventory := offers[0].Artifacts
			if inventory.Known != testCase.wantKnown {
				t.Fatalf("the offer says enumerated = %v, want %v", inventory.Known, testCase.wantKnown)
			}
			if inventory.Holds(version) != (testCase.wantLocality == domain.LocalityHot) {
				t.Errorf("holds a readable copy = %v, want %v", inventory.Holds(version), testCase.wantLocality == domain.LocalityHot)
			}
			fetch, evidence := domain.ArtifactFetchWork([]domain.ArtifactVersion{version}, inventory)
			wantFetch := version.SizeBytes
			if testCase.wantLocality == domain.LocalityHot {
				wantFetch = 0
			}
			if fetch != wantFetch {
				t.Errorf("owes %d bytes, want %d", fetch, wantFetch)
			}
			if len(evidence) != 1 || evidence[0].Locality != testCase.wantLocality {
				t.Errorf("recorded %+v, want locality %q", evidence, testCase.wantLocality)
			}
		})
	}
}

// TestANodeOffersTheCachesItHoldsUnderTheWorkspaceThatOwnsThem is the mutable
// half of the same projection. One machine is offered to every workspace, so an
// inventory that dropped the workspace off each cache would let one tenant read
// another's warmth off a shared disk, which is the one thing a Cache Mount's
// identity exists to prevent.
func TestANodeOffersTheCachesItHoldsUnderTheWorkspaceThatOwnsThem(t *testing.T) {
	looked := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	wanted := domain.CacheMountRequirement{Name: "compiler-cache", CompatibilityKey: "cuda-12.4"}
	registry := readyNodeHoldingCaches(t, domain.CacheInventory{
		Known:      true,
		ObservedAt: looked,
		Mounts: []domain.CacheMount{
			{WorkspaceID: nodeWorkspace, Name: wanted.Name, CompatibilityKey: wanted.CompatibilityKey, CreatedAt: looked},
			{WorkspaceID: "ws_neighbour", Name: wanted.Name, CompatibilityKey: wanted.CompatibilityKey, CreatedAt: looked},
		},
	})

	offers, err := registry.Offers(context.Background(), nodeWorkspace)
	if err != nil {
		t.Fatalf("list node offers: %v", err)
	}

	caches := offers[0].Caches
	if !caches.Known || len(caches.Mounts) != 2 {
		t.Fatalf("the offer carries %+v, and this node reported two tenants' caches", caches)
	}
	if !caches.Holds(nodeWorkspace, wanted) {
		t.Errorf("the workspace that owns a cache was not offered it: %+v", caches.Mounts)
	}
	if caches.Holds("ws_stranger", wanted) {
		t.Errorf("a workspace holding nothing here was offered someone else's cache: %+v", caches.Mounts)
	}
	if found := domain.CacheWarmth("ws_stranger", []domain.CacheMountRequirement{wanted}, caches); found[0].Locality != domain.LocalityCold {
		t.Errorf("a stranger's evidence for this cache is %q, want cold", found[0].Locality)
	}
}

// TestANodeThatCannotEnumerateCachesOffersNoCacheClaim keeps a node's silence its
// own. An inventory marked enumerated because the node answered about its host
// would publish "I hold no cache" for every machine that never looked.
func TestANodeThatCannotEnumerateCachesOffersNoCacheClaim(t *testing.T) {
	registry := readyNodeHoldingCaches(t, domain.CacheInventory{})

	offers, err := registry.Offers(context.Background(), nodeWorkspace)
	if err != nil {
		t.Fatalf("list node offers: %v", err)
	}

	if offers[0].Caches.Known {
		t.Fatalf("the offer claims this node enumerated its caches: %+v", offers[0].Caches)
	}
	wanted := domain.CacheMountRequirement{Name: "compiler-cache"}
	found := domain.CacheWarmth(nodeWorkspace, []domain.CacheMountRequirement{wanted}, offers[0].Caches)
	if found[0].Locality != domain.LocalityUnknown {
		t.Fatalf("a cache nothing enumerated was recorded %q, want unknown", found[0].Locality)
	}
}

func readyNode(t *testing.T, held []capability.ImageLocality) *node.Registry {
	t.Helper()
	return readyNodeHolding(t, held, domain.ArtifactInventory{})
}

func readyNodeHoldingCaches(t *testing.T, caches domain.CacheInventory) *node.Registry {
	t.Helper()
	return readyNodeReporting(t, nil, domain.ArtifactInventory{}, caches)
}

func readyNodeHolding(t *testing.T, images []capability.ImageLocality, copies domain.ArtifactInventory) *node.Registry {
	t.Helper()
	return readyNodeReporting(t, images, copies, domain.CacheInventory{})
}

func readyNodeReporting(
	t *testing.T,
	images []capability.ImageLocality,
	copies domain.ArtifactInventory,
	caches domain.CacheInventory,
) *node.Registry {
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
			Images:     images,
			Artifacts:  copies,
			Caches:     caches,
		},
	}
	if _, err := registry.Enroll(context.Background(), request); err != nil {
		t.Fatalf("enroll node: %v", err)
	}
	return registry
}
