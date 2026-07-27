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
		// The top layer has to arrive and then be applied, so a partial host owes
		// both over the same bytes. Charging the transfer alone said a machine about
		// to fetch a layer owed no assembly of it.
		"a host holding the shared base fetches the top layer and applies it": {
			inventory:    domain.ImageInventory{Known: true, LayerDigests: []string{baseLayer}},
			wantWork:     domain.ImageWork{TransferBytes: 80_000_000, UnpackBytes: 80_000_000},
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
		"a host holding nothing fetches all of it and applies all of it": {
			inventory:    domain.ImageInventory{Known: true},
			wantWork:     domain.ImageWork{TransferBytes: 18_080_000_000, UnpackBytes: 18_080_000_000},
			wantLocality: domain.LocalityCold,
		},
		// Silence is not warmth. The image has to arrive from somewhere and
		// nothing here says any of it is already on this machine, so a host
		// nobody can describe owes what a host holding nothing owes.
		"a host that cannot say owes the whole image rather than nothing": {
			inventory:    domain.ImageInventory{Known: false},
			wantWork:     domain.ImageWork{TransferBytes: 18_080_000_000, UnpackBytes: 18_080_000_000},
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

// TestANodeOffersTheRoomItActuallyHas is the capacity half of the same defect.
// A node offer asserted that capacity was available, at full confidence, for
// every machine inside its lease, including one the machine itself said was mid
// workload. The reusable lane is the only source of standing offers in
// production and every rule Placement has about waiting is written against an
// offer that says it is occupied, so none of them were reachable: a node was
// read as idle this instant, its queue priced at no waiting at all, and a
// Booking behind it promised a start the machine had already missed.
//
// This runtime runs one workload at a time, so one container is the whole
// machine.
func TestTwoMachinesOnOneLeaseOfferTwoMachines(t *testing.T) {
	registry, clock := newRegistry(t)
	enrollOn(t, registry, clock, "nod_first", "rnt_shared")
	enrollOn(t, registry, clock, "nod_second", "rnt_shared")

	offers, err := registry.Offers(context.Background(), nodeWorkspace)
	if err != nil {
		t.Fatalf("offers: %v", err)
	}

	if len(offers) != 2 {
		t.Fatalf("the workspace offered %d machines, want the two enrolled nodes", len(offers))
	}
	first := domain.CandidateIdentityOf(offers[0], "sha256:image")
	second := domain.CandidateIdentityOf(offers[1], "sha256:image")
	if first.Machine != offers[0].ID || second.Machine != offers[1].ID {
		t.Fatalf("a node offer named a machine that is not the node: %q and %q", first.Machine, second.Machine)
	}
	if first.Candidate(true) == second.Candidate(true) {
		t.Fatalf("two machines sharing a lease share the key %q", first.Candidate(true))
	}
}

// TestAnEnrolledMachineStatesNoPlaceAndSaysSo is the ladder an enrolled machine
// really has, stated where the offer is built rather than assumed from a fixture.
//
// The region a machine sits in is its operator's to state and nothing enrols one,
// so this projection publishes none. A launch history of a node therefore has two
// rungs and not three: this machine, and then every machine this control plane has
// enrolled. The middle rung is a target the Lab's worlds can already describe and
// production cannot reach, and the test that says so is here so a reader of those
// worlds is not left thinking a backend states a place today.
//
// What may not happen instead is a region guessed from the endpoint Mercator
// reaches the machine through. A blank region is a rung skipped, which the estimator
// already handles; a guessed one is two machines filed as neighbours because of how
// an operator's network is addressed.
func TestAnEnrolledMachineStatesNoPlaceAndSaysSo(t *testing.T) {
	registry, clock := newRegistry(t)
	enrollOn(t, registry, clock, "nod_first", "rnt_first")

	offers, err := registry.Offers(context.Background(), nodeWorkspace)
	if err != nil {
		t.Fatalf("offers: %v", err)
	}

	if len(offers) != 1 {
		t.Fatalf("the workspace offered %d machines, want the one enrolled node", len(offers))
	}
	if offers[0].Region != "" {
		t.Fatalf("an enrolled machine published the region %q, and nothing enrols one", offers[0].Region)
	}
	identity := domain.CandidateIdentityOf(offers[0], "sha256:image")
	if identity.ProviderAndRegion(true) != "" {
		t.Fatalf("a machine in no stated place has the region key %q", identity.ProviderAndRegion(true))
	}
	if identity.Candidate(true) == "" || identity.ProviderKey(true) == "" {
		t.Fatalf("the two rungs this machine does have are %q and %q",
			identity.Candidate(true), identity.ProviderKey(true))
	}
}

// enrollOn brings one named machine up on a stated lease, which is what makes two
// machines on one lease a world a test can build.
func enrollOn(t *testing.T, registry *node.Registry, clock *testClock, nodeID, rentalID string) {
	t.Helper()
	bootstrap, err := registry.Invite(context.Background(), node.Invitation{
		WorkspaceID: nodeWorkspace, NodeID: nodeID, RentalID: rentalID, Generation: 1,
		ShadowPriceUSDPerHour: 2,
	})
	if err != nil {
		t.Fatalf("invite %s: %v", nodeID, err)
	}
	if _, err := registry.Enroll(context.Background(), capability.EnrollmentRequest{
		NodeID:          bootstrap.NodeID,
		RentalID:        bootstrap.RentalID,
		Generation:      bootstrap.Generation,
		EnrollmentToken: bootstrap.EnrollmentToken,
		AgentVersion:    "test",
		Facts: capability.NodeFacts{
			ObservedAt: clock.Now(),
			Host:       capability.HostFacts{OS: "linux", Architecture: "amd64", ContainerRuntime: "docker"},
		},
	}); err != nil {
		t.Fatalf("enroll %s: %v", nodeID, err)
	}
}

func TestANodeOffersTheRoomItActuallyHas(t *testing.T) {
	registry, session := readyNodeReporting(t, nil, domain.ArtifactInventory{}, domain.CacheInventory{})
	if !capacityOf(t, registry).Available {
		t.Fatal("a node running nothing offered no capacity")
	}

	reportWorkload(t, registry, session, "evt-running", capability.WorkloadPhaseRunning, nil)

	capacity := capacityOf(t, registry)
	if capacity.Available {
		t.Fatal("a node executing a workload offered capacity for another")
	}
	if capacity.Confidence != 1 {
		t.Fatalf("the offer states %v confidence in a machine Mercator can see, want the full point it owns",
			capacity.Confidence)
	}
}

// TestANodeWhoseWorkloadExitedOffersItsCapacityBack is the other direction, and
// the reason the claim is read off the node's own report rather than off anything
// Mercator intended: the slot is free when the container is gone, which the
// machine says before the control plane has finished the Booking that put it
// there.
func TestANodeWhoseWorkloadExitedOffersItsCapacityBack(t *testing.T) {
	registry, session := readyNodeReporting(t, nil, domain.ArtifactInventory{}, domain.CacheInventory{})
	reportWorkload(t, registry, session, "evt-running", capability.WorkloadPhaseRunning, nil)

	exited := 0
	reportWorkload(t, registry, session, "evt-exited", capability.WorkloadPhaseExited, &exited)

	if !capacityOf(t, registry).Available {
		t.Fatal("a node whose container exited still offered no capacity")
	}
}

func capacityOf(t *testing.T, registry *node.Registry) domain.CapacityEvidence {
	t.Helper()
	offers, err := registry.Offers(context.Background(), nodeWorkspace)
	if err != nil {
		t.Fatalf("list node offers: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("the workspace offered %d machines, want the one enrolled node", len(offers))
	}
	return offers[0].Capacity
}

// reportWorkload is the node saying what its container is doing, over the same
// authenticated path its agent reports through.
func reportWorkload(
	t *testing.T,
	registry *node.Registry,
	session capability.Enrollment,
	eventID string,
	phase capability.WorkloadPhase,
	exitCode *int,
) {
	t.Helper()
	observed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	err := registry.RecordEvents(context.Background(), session.NodeID, session.SessionToken, []node.Event{{
		ID:         eventID,
		Kind:       node.EventWorkload,
		ObservedAt: observed,
		Workload: &capability.WorkloadObservation{
			RunID:      "run-holding-the-machine",
			AttemptID:  "attempt-1",
			Phase:      phase,
			ObservedAt: observed,
			ExitCode:   exitCode,
		},
	}})
	if err != nil {
		t.Fatalf("record %s: %v", phase, err)
	}
}

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
	registry, _ := readyNodeReporting(t, nil, domain.ArtifactInventory{}, caches)
	return registry
}

func readyNodeHolding(t *testing.T, images []capability.ImageLocality, copies domain.ArtifactInventory) *node.Registry {
	t.Helper()
	registry, _ := readyNodeReporting(t, images, copies, domain.CacheInventory{})
	return registry
}

// readyNodeReporting is one enrolled machine and the session it reports through,
// because what a node is running is a fact it states rather than one anybody can
// state for it.
func readyNodeReporting(
	t *testing.T,
	images []capability.ImageLocality,
	copies domain.ArtifactInventory,
	caches domain.CacheInventory,
) (*node.Registry, capability.Enrollment) {
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
	enrollment, err := registry.Enroll(context.Background(), request)
	if err != nil {
		t.Fatalf("enroll node: %v", err)
	}
	return registry, enrollment
}

// TestANodeOffersTheTermsItWasBoughtOn is the economics half of a node offer. A
// machine an operator holds is not one number: it is bought in blocks of time, it
// may be held for particular work, and it may go back to its owner at a stated
// moment. All three reach Placement through the offer, and none of them could be
// stated before.
//
// The committed interval is the part that has to be derived rather than repeated.
// An operator states how long a block is; where the current block ends is a
// question about the clock, counted from the moment Mercator started paying for
// this machine.
func TestANodeOffersTheTermsItWasBoughtOn(t *testing.T) {
	registry, clock := newRegistry(t)
	enrolledAt := clock.Now()
	inviteWithTerms(t, registry, node.Invitation{
		WorkspaceID:           nodeWorkspace,
		NodeID:                "nod_terms",
		RentalID:              "rnt_terms",
		Generation:            1,
		ShadowPriceUSDPerHour: 1.5,
		Purchase: node.Purchase{
			BillingIntervalSeconds: 3600,
			EligibleClasses:        []domain.ServiceClass{domain.ClassInteractive, domain.ClassStandard},
			AvailableUntil:         enrolledAt.Add(6 * time.Hour),
		},
	}, clock)

	// A minute in, which is as far as a node can be carried without another
	// heartbeat: the machine has to still be inside its lease to be offered at all.
	clock.Advance(time.Minute)
	offers, err := registry.Offers(context.Background(), nodeWorkspace)
	if err != nil {
		t.Fatalf("read offers: %v", err)
	}

	if len(offers) != 1 {
		t.Fatalf("one enrolled machine published %d offers", len(offers))
	}
	terms := offers[0].Terms
	if want := enrolledAt.Add(time.Hour); !terms.CommittedUntil.Equal(want) {
		t.Fatalf("a minute into an hourly machine's first hour, the offer says Mercator owes rent until %s, and the hour ends at %s",
			terms.CommittedUntil, want)
	}
	if terms.Admits(domain.ClassBatch) || !terms.Admits(domain.ClassStandard) {
		t.Fatalf("a machine held for %v admits batch work: %v", terms.EligibleClasses, terms.Admits(domain.ClassBatch))
	}
	if !terms.AvailableUntil.Equal(enrolledAt.Add(6 * time.Hour)) {
		t.Fatalf("the window this machine is Mercator's for closes at %s", terms.AvailableUntil)
	}
	// The increment reaches the price model too, because that is what a placement's
	// dollars are rounded up to. An operator who says the machine is bought by the
	// hour has said that twenty minutes of it costs an hour.
	if offers[0].Pricing.GranularitySeconds != 3600 {
		t.Fatalf("the price of an hourly machine is billed in blocks of %ds", offers[0].Pricing.GranularitySeconds)
	}
	if offers[0].Pricing.BilledSeconds(1200) != 3600 {
		t.Fatalf("twenty minutes on an hourly machine is billed as %.0fs", offers[0].Pricing.BilledSeconds(1200))
	}
}

// TestAMachineBoughtInNoIncrementsOwesNoInterval is the other answer, and it is an
// answer rather than a default. An operator's own hardware is not bought in blocks:
// Mercator holds it continuously, so no second of it is a fresh commitment, there
// is no tail of an increment to charge, and the offer says so by stating no
// interval at all.
func TestAMachineBoughtInNoIncrementsOwesNoInterval(t *testing.T) {
	registry, clock := newRegistry(t)
	inviteWithTerms(t, registry, node.Invitation{
		WorkspaceID:           nodeWorkspace,
		NodeID:                "nod_owned",
		RentalID:              "rnt_owned",
		Generation:            1,
		ShadowPriceUSDPerHour: 1.5,
	}, clock)

	offers, err := registry.Offers(context.Background(), nodeWorkspace)
	if err != nil {
		t.Fatalf("read offers: %v", err)
	}

	if !offers[0].Terms.CommittedUntil.IsZero() {
		t.Fatalf("a machine bought in no increments owes rent until %s", offers[0].Terms.CommittedUntil)
	}
	if offers[0].Pricing.GranularitySeconds != 0 {
		t.Fatalf("a machine bought in no increments is billed in blocks of %ds", offers[0].Pricing.GranularitySeconds)
	}
	if offers[0].Pricing.BilledSeconds(1200) != 1200 {
		t.Fatalf("twenty minutes on a continuously held machine is billed as %.0fs", offers[0].Pricing.BilledSeconds(1200))
	}
}

// inviteWithTerms enrolls one machine on the terms an operator stated for it, so a
// case about economics states the sale rather than only the price.
func inviteWithTerms(t *testing.T, registry *node.Registry, invitation node.Invitation, clock *testClock) {
	t.Helper()
	bootstrap, err := registry.Invite(context.Background(), invitation)
	if err != nil {
		t.Fatalf("invite node: %v", err)
	}
	if _, err := registry.Enroll(context.Background(), capability.EnrollmentRequest{
		NodeID:          bootstrap.NodeID,
		RentalID:        bootstrap.RentalID,
		Generation:      bootstrap.Generation,
		EnrollmentToken: bootstrap.EnrollmentToken,
		AgentVersion:    "test",
		Facts: capability.NodeFacts{
			ObservedAt: clock.Now(),
			Host:       capability.HostFacts{OS: "linux", Architecture: "amd64", ContainerRuntime: "docker"},
		},
	}); err != nil {
		t.Fatalf("enroll node: %v", err)
	}
}
