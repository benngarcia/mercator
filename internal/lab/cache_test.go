package lab

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

const compilerCache = "compiler-cache"

// TestACacheIsWarmOnlyForTheWorkspaceAndGenerationThatOwnsIt is the isolation
// claim at L1, driven through the real orchestrator, event log, and Run
// projection. One machine holds two caches called compiler-cache because two
// tenants named one, and the recorded decisions say so: the neighbour's cache is
// never warmth, and neither is the generation the application has replaced.
//
// The hot row is what makes the cold rows mean anything. Without it, a scheduler
// that never found a cache warm at all would satisfy every other assertion here.
func TestACacheIsWarmOnlyForTheWorkspaceAndGenerationThatOwnsIt(t *testing.T) {
	execution := openConformanceExecution(t, "cache-mounts-never-cross-a-workspace")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	// Runs arrive twenty minutes apart and each occupies the only Rental for a
	// pull plus a runtime, so this drives at the cadence a control plane polls at
	// rather than in one jump.
	for range 16 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrivals: %v", err)
		}
	}

	decisions := bookingDecisions(t, execution)
	for _, expected := range []struct {
		run      string
		selected string
		locality domain.LocalityState
		held     string
		why      string
	}{
		{
			run:      "run-alpha-first",
			selected: "shared-builder",
			locality: domain.LocalityCold,
			why:      "the first Run in this world found a cache nobody had written",
		},
		{
			run:      "run-beta-first",
			selected: "shared-builder",
			locality: domain.LocalityCold,
			why:      "a second tenant naming compiler-cache must not inherit the first tenant's cache",
		},
		{
			run:      "run-alpha-second",
			selected: "shared-builder",
			locality: domain.LocalityHot,
			held:     "cuda-12.4",
			why:      "the tenant that filled its own cache finds it on the machine that holds it",
		},
		{
			run:      "run-alpha-next-generation",
			selected: "shared-builder",
			locality: domain.LocalityCold,
			held:     "cuda-12.4",
			why:      "the application declared a new generation, so what is under the name is not usable",
		},
		{
			// The generation this Run needs was attached to a container a minute
			// ago and that container is still running. Creating the container is
			// what creates its storage, so the machine holds this cache from the
			// moment the workload started rather than from the moment one
			// finished, which is exactly what a container runtime reports.
			run:      "run-alpha-while-it-runs",
			selected: "spare-builder",
			locality: domain.LocalityHot,
			held:     "cuda-13.0",
			why:      "a workload of this tenant and generation is attached to that cache right now",
		},
	} {
		t.Run(expected.run, func(t *testing.T) {
			decision := decisions[expected.run]
			if decision.SelectedOfferSnapshotID != expected.selected {
				t.Fatalf("the Run landed on %q, want %q", decision.SelectedOfferSnapshotID, expected.selected)
			}
			found := cacheEvidence(t, decision, "shared-builder", compilerCache)
			if found.Locality != expected.locality {
				t.Fatalf("the decision recorded %q for %s: %s", found.Locality, compilerCache, expected.why)
			}
			if found.HeldCompatibilityKey != expected.held {
				t.Fatalf(
					"the decision says this host holds generation %q under %s, want %q",
					found.HeldCompatibilityKey, compilerCache, expected.held,
				)
			}
		})
	}

	// World Truth carries both tenants' caches on the shared machine, under
	// identities that differ only by workspace, plus the generation the fourth Run
	// started. Three caches on one disk is what "two tenants, one name" actually
	// looks like. The fifth Run put a fourth cache on the spare machine it was sent
	// to, which is the same identity on another host and not the same cache.
	truth := execution.runtime.world.truthSnapshot()
	if len(truth.CacheMounts) != 4 {
		t.Fatalf("World Truth holds %d caches, want three on the shared machine and one on the spare: %+v", len(truth.CacheMounts), truth.CacheMounts)
	}
	owners := map[string]string{}
	shared := 0
	for _, mount := range truth.CacheMounts {
		if mount.Name != compilerCache {
			t.Fatalf("World Truth holds a cache named %q that no Run declared", mount.Name)
		}
		if mount.OfferID == "shared-builder" {
			shared++
		}
		key := mount.OfferID + "/" + mount.Identity
		if owner, seen := owners[key]; seen {
			t.Fatalf("cache identity %q on %q is held twice, by %q and %q", mount.Identity, mount.OfferID, owner, mount.WorkspaceID)
		}
		owners[key] = mount.WorkspaceID
	}
	if shared != 3 {
		t.Fatalf("the shared machine holds %d caches, want alpha's two generations and beta's one: %+v", shared, truth.CacheMounts)
	}
}

// TestACacheIsAttachedUnderTheWorkspaceThatRanTheWorkload is the ledger half. The
// decision says what Placement found; this says what the world was actually asked
// to touch, which is where a leak would happen. Every attachment this execution
// recorded names the tenant it ran for, and no identity is touched by two.
func TestACacheIsAttachedUnderTheWorkspaceThatRanTheWorkload(t *testing.T) {
	execution := openConformanceExecution(t, "cache-mounts-never-cross-a-workspace")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 16 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrivals: %v", err)
		}
	}

	accesses := cacheAccesses(t, execution.runtime.world.effectRecords())
	if len(accesses) != 5 {
		t.Fatalf("the ledger records %d cache attachments, want one per Run: %+v", len(accesses), accesses)
	}
	tenants := map[string]string{}
	for _, access := range accesses {
		if access.WorkspaceID == "" {
			t.Fatalf("cache access %+v happened under no workspace", access)
		}
		if owner, seen := tenants[access.Identity]; seen && owner != access.WorkspaceID {
			t.Fatalf("cache %q was touched by %q and %q", access.Identity, owner, access.WorkspaceID)
		}
		tenants[access.Identity] = access.WorkspaceID
	}
	if len(tenants) != 3 {
		t.Fatalf("the ledger touched %d cache identities, want three: %+v", len(tenants), tenants)
	}
}

// TestCacheIsolationReadsWhatIsStoredAndNotOnlyWhatWasTouched is the World Truth
// half of safety.cache_mount_workspace_isolation. The ledger says what each Run
// reached for; this says what the machine ended up holding, and a host carrying
// one cache for two tenants is a leak whether or not anything has read it yet.
func TestCacheIsolationReadsWhatIsStoredAndNotOnlyWhatWasTouched(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	shared := func(workspaceID string) CacheMountState {
		return CacheMountState{
			OfferID:          "shared-builder",
			Identity:         compilerCache,
			WorkspaceID:      workspaceID,
			Name:             compilerCache,
			CompatibilityKey: "cuda-12.4",
			Revision:         1,
			CreatedAt:        now,
		}
	}

	err := cacheMountWorkspaceIsolation(InvariantObservation{
		StartedAt: now,
		Now:       now,
		World: WorldTruthSnapshot{At: now, CacheMounts: []CacheMountState{
			shared("ws_lab_alpha"),
			shared("ws_lab_beta"),
		}},
	})

	if err == nil {
		t.Fatal("one machine holding one cache identity for two tenants raised nothing")
	}
}

// TestLocalityProvenanceRejectsBorrowedCapacityHoldingACache is the third clause
// of safety.locality_provenance, which no execution can reach either: the world
// refuses to write a cache onto capacity that keeps nothing. The forbidden state
// is stated directly, because a rule nothing can construct a world for is a rule
// deleting it would not break.
func TestLocalityProvenanceRejectsBorrowedCapacityHoldingACache(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	borrowed := domain.OfferSnapshot{
		ID:   "local-docker",
		Kind: domain.OfferKindStanding,
		Lane: domain.LaneEphemeral,
		Caches: domain.CacheInventory{Known: true, ObservedAt: now, Mounts: []domain.CacheMount{{
			WorkspaceID: "ws_lab",
			Name:        compilerCache,
		}}},
	}

	err := localityProvenance(InvariantObservation{
		StartedAt: now,
		Now:       now,
		World:     WorldTruthSnapshot{At: now, Offers: []domain.OfferSnapshot{borrowed}},
	})

	if err == nil {
		t.Fatal("a machine that holds nothing once its workload exits reported a cache and nothing objected")
	}
}

func cacheEvidence(t *testing.T, decision domain.BookingDecision, offerID, name string) domain.CacheEvidence {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID != offerID {
			continue
		}
		for _, found := range candidate.CacheEvidence {
			if found.Name == name {
				return found
			}
		}
		t.Fatalf("Run %q candidate %q recorded no evidence for cache %q", decision.RunID, offerID, name)
	}
	t.Fatalf("Run %q has no candidate for offer %q", decision.RunID, offerID)
	return domain.CacheEvidence{}
}

// cacheAccess is one recorded attachment, as the ledger states it.
type cacheAccess struct {
	Identity         string `json:"identity"`
	WorkspaceID      string `json:"workspace_id"`
	Name             string `json:"name"`
	CompatibilityKey string `json:"compatibility_key"`
	OfferID          string `json:"offer_id"`
}

func cacheAccesses(t *testing.T, effects []EffectRecord) []cacheAccess {
	t.Helper()
	var accesses []cacheAccess
	for _, effect := range effects {
		if effect.Operation != OperationCacheMountAttach {
			continue
		}
		var access cacheAccess
		if err := json.Unmarshal(effect.Request, &access); err != nil {
			t.Fatalf("decode cache access %s: %v", effect.ID, err)
		}
		accesses = append(accesses, access)
	}
	return accesses
}
