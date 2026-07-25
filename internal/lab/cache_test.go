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
		locality domain.LocalityState
		held     string
		why      string
	}{
		{
			run:      "run-alpha-first",
			locality: domain.LocalityCold,
			why:      "the first Run in this world found a cache nobody had written",
		},
		{
			run:      "run-beta-first",
			locality: domain.LocalityCold,
			why:      "a second tenant naming compiler-cache must not inherit the first tenant's cache",
		},
		{
			run:      "run-alpha-second",
			locality: domain.LocalityHot,
			held:     "cuda-12.4",
			why:      "the tenant that filled its own cache finds it on the machine that holds it",
		},
		{
			run:      "run-alpha-next-generation",
			locality: domain.LocalityCold,
			held:     "cuda-12.4",
			why:      "the application declared a new generation, so what is under the name is not usable",
		},
	} {
		t.Run(expected.run, func(t *testing.T) {
			decision := decisions[expected.run]
			if decision.SelectedOfferSnapshotID != "shared-builder" {
				t.Fatalf("the Run landed on %q, and there is one machine in this world", decision.SelectedOfferSnapshotID)
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

	// World Truth carries both caches on the one machine, under identities that
	// differ only by workspace, plus the generation the last Run started. Three
	// caches on one disk is what "two tenants, one name" actually looks like.
	truth := execution.runtime.world.truthSnapshot()
	if len(truth.CacheMounts) != 3 {
		t.Fatalf("World Truth holds %d caches, want alpha's two generations and beta's one: %+v", len(truth.CacheMounts), truth.CacheMounts)
	}
	owners := map[string]string{}
	for _, mount := range truth.CacheMounts {
		if mount.Name != compilerCache {
			t.Fatalf("World Truth holds a cache named %q that no Run declared", mount.Name)
		}
		if owner, seen := owners[mount.Identity]; seen {
			t.Fatalf("cache identity %q is held twice, by %q and %q", mount.Identity, owner, mount.WorkspaceID)
		}
		owners[mount.Identity] = mount.WorkspaceID
	}
}

// TestACacheIsWrittenUnderTheWorkspaceThatRanTheWorkload is the ledger half. The
// decision says what Placement found; this says what the world was actually asked
// to touch, which is where a leak would happen. Every access this execution
// recorded names the tenant it ran for, and no identity is touched by two.
func TestACacheIsWrittenUnderTheWorkspaceThatRanTheWorkload(t *testing.T) {
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
	if len(accesses) != 8 {
		t.Fatalf("the ledger records %d cache accesses, want one read and one write per Run: %+v", len(accesses), accesses)
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

// TestCacheIsolationReadsTheStorageAReadReached is the clause that makes the
// rule about the disk rather than about the reader's own arithmetic. Both
// identities in a read request are derived from the workspace the execution
// belongs to, so a leak can never show up there: it shows up in which storage
// the read resolved to. Here beta asks for its own cache and the world hands it
// alpha's, which is a cross-workspace read of mutable state, and the request
// half of the record agrees with itself throughout.
func TestCacheIsolationReadsTheStorageAReadReached(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	alpha := domain.CacheIdentity("ws_lab_alpha", domain.CacheMountRequirement{Name: compilerCache, CompatibilityKey: "cuda-12.4"})
	beta := domain.CacheIdentity("ws_lab_beta", domain.CacheMountRequirement{Name: compilerCache, CompatibilityKey: "cuda-12.4"})

	err := cacheMountWorkspaceIsolation(InvariantObservation{
		StartedAt: now,
		Now:       now,
		Effects: []EffectRecord{
			cacheAccessRecord(t, OperationCacheMountWrite, "ws_lab_alpha", alpha, ""),
			cacheAccessRecord(t, OperationCacheMountRead, "ws_lab_beta", beta, alpha),
		},
	})

	if err == nil {
		t.Fatal("a Run in one workspace read the storage another workspace's cache lives in, and nothing objected")
	}
}

// cacheAccessRecord is one ledger entry for a cache access: what the execution
// asked for, and which storage the world answered from.
func cacheAccessRecord(t *testing.T, operation, workspaceID, identity, reached string) EffectRecord {
	t.Helper()
	request, err := json.Marshal(map[string]any{
		"identity":     identity,
		"workspace_id": workspaceID,
		"offer_id":     "shared-builder",
	})
	if err != nil {
		t.Fatalf("encode cache access request: %v", err)
	}
	if reached == "" {
		reached = identity
	}
	consequence, err := json.Marshal(map[string]any{"found": true, "revision": 1, "reached_identity": reached})
	if err != nil {
		t.Fatalf("encode cache access consequence: %v", err)
	}
	return EffectRecord{
		ID:          operation + "/" + identity,
		Operation:   operation,
		Request:     request,
		Consequence: consequence,
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

// cacheAccess is one recorded read or write, as the ledger states it.
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
		if effect.Operation != OperationCacheMountRead && effect.Operation != OperationCacheMountWrite {
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
