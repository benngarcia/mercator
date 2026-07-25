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
)

// TestANodeOffersTheContentItActuallyHolds is the guard on the defect this
// contract exists to delete. An offer used to report zero missing bytes
// whatever the machine held, so a node holding nothing at all looked fully
// warm and was priced as an instant start.
func TestANodeOffersTheContentItActuallyHolds(t *testing.T) {
	cases := map[string]struct {
		held      []capability.ImageLocality
		wantWhole bool
		wantLayer bool
	}{
		"a node holding the exact image reports it whole": {
			held:      []capability.ImageLocality{{ManifestDigest: trainerV2, State: capability.LocalityHot, LayerDigests: []string{baseLayer, topLayer}}},
			wantWhole: true,
			wantLayer: true,
		},
		"a node part way through a pull reports its layers and not the image": {
			held:      []capability.ImageLocality{{ManifestDigest: trainerV2, State: capability.LocalityPartial, LayerDigests: []string{baseLayer}}},
			wantWhole: false,
			wantLayer: true,
		},
		"a node holding nothing reports nothing rather than looking warm": {
			held:      nil,
			wantWhole: false,
			wantLayer: false,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			registry, clock := readyNode(t, testCase.held)

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
			if inventory.HoldsLayer(baseLayer) != testCase.wantLayer {
				t.Errorf("holds the base layer = %v, want %v", inventory.HoldsLayer(baseLayer), testCase.wantLayer)
			}
			_ = clock
		})
	}
}

// TestWhatANodeHoldsDecidesWhatItStillHasToFetch is the join this contract
// moved to the scheduler: the offer says what is here, the manifest says what
// is needed, and the subtraction happens where both are known.
func TestWhatANodeHoldsDecidesWhatItStillHasToFetch(t *testing.T) {
	manifest := domain.ImageManifest{
		Known:  true,
		Digest: trainerV2,
		Layers: []domain.ImageLayer{
			{Digest: baseLayer, CompressedBytes: 18_000_000_000},
			{Digest: topLayer, CompressedBytes: 80_000_000},
		},
	}
	cases := map[string]struct {
		inventory domain.ImageInventory
		want      int64
		wantKnown bool
	}{
		"a host holding the whole image fetches nothing": {
			inventory: domain.ImageInventory{Known: true, ImageDigests: []string{trainerV2}},
			want:      0,
			wantKnown: true,
		},
		"a host holding the shared base fetches only the top layer": {
			inventory: domain.ImageInventory{Known: true, LayerDigests: []string{baseLayer}},
			want:      80_000_000,
			wantKnown: true,
		},
		"a host holding nothing fetches all of it": {
			inventory: domain.ImageInventory{Known: true},
			want:      18_080_000_000,
			wantKnown: true,
		},
		"a host that cannot say is unknown rather than free": {
			inventory: domain.ImageInventory{Known: false},
			want:      0,
			wantKnown: false,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			bytes, known := manifest.TransferBytes(testCase.inventory)

			if bytes != testCase.want || known != testCase.wantKnown {
				t.Fatalf("transfer = %d bytes (known %v), want %d (known %v)", bytes, known, testCase.want, testCase.wantKnown)
			}
		})
	}
}

// TestAnUnresolvedManifestLeavesEveryCandidateIndistinguishable states why an
// unknown manifest is safe: nobody can be told apart on locality, so the term
// is absent for everyone rather than favouring whoever happens to report most.
func TestAnUnresolvedManifestLeavesEveryCandidateIndistinguishable(t *testing.T) {
	unresolved := domain.ImageManifest{}
	warm := domain.ImageInventory{Known: true, ImageDigests: []string{trainerV2}}
	cold := domain.ImageInventory{Known: true}

	warmBytes, warmKnown := unresolved.TransferBytes(warm)
	coldBytes, coldKnown := unresolved.TransferBytes(cold)

	if warmKnown || coldKnown {
		t.Fatal("an unresolved manifest cannot produce a known transfer for anyone")
	}
	if warmBytes != coldBytes {
		t.Fatalf("a warm host and a cold one differ by %d bytes under an unresolved manifest", warmBytes-coldBytes)
	}
}

const nodeWorkspace = "ws_offers"

func readyNode(t *testing.T, held []capability.ImageLocality) (*node.Registry, time.Time) {
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
	return registry, clock.Now()
}
