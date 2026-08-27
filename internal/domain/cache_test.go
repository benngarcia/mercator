package domain_test

import (
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

const compilerCache = "compiler-cache"

// TestCacheWarmthAnswersPerGeneration is the whole comparison in one table. A
// cache is warm only for the generation the application says it can use, and
// the two ways of being cold are
// recorded differently because an operator acts on them differently: one machine
// has never done this work, the other is holding the generation before.
func TestCacheWarmthAnswersPerGeneration(t *testing.T) {
	looked := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	wanted := domain.CacheMountRequirement{Name: compilerCache, CompatibilityKey: "cuda-12.4"}
	held := func(mounts ...domain.CacheMount) domain.CacheInventory {
		return domain.CacheInventory{Known: true, ObservedAt: looked, Mounts: mounts}
	}
	current := domain.CacheMount{Name: compilerCache, CompatibilityKey: "cuda-12.4", CreatedAt: looked}
	previous := domain.CacheMount{Name: compilerCache, CompatibilityKey: "cuda-11.8", CreatedAt: looked}

	for name, testCase := range map[string]struct {
		inventory    domain.CacheInventory
		wantLocality domain.LocalityState
		wantHeld     string
	}{
		"the declared generation is warm": {
			inventory:    held(current),
			wantLocality: domain.LocalityHot,
			wantHeld:     "cuda-12.4",
		},
		"the generation the application replaced is recorded as what is there": {
			inventory:    held(previous),
			wantLocality: domain.LocalityCold,
			wantHeld:     "cuda-11.8",
		},
		"a machine that has never done this work holds nothing to name": {
			inventory:    held(),
			wantLocality: domain.LocalityCold,
		},
		// A cache is best-effort, so silence costs nothing and buys nothing. What
		// it must never do is read as warmth on a machine nobody asked.
		"a holder that could not enumerate says nothing either way": {
			inventory:    domain.CacheInventory{},
			wantLocality: domain.LocalityUnknown,
		},
	} {
		t.Run(name, func(t *testing.T) {
			found := domain.CacheWarmth([]domain.CacheMountRequirement{wanted}, testCase.inventory)

			if len(found) != 1 {
				t.Fatalf("recorded %d entries for one declared cache", len(found))
			}
			if found[0].Locality != testCase.wantLocality {
				t.Errorf("locality = %q, want %q", found[0].Locality, testCase.wantLocality)
			}
			if found[0].HeldCompatibilityKey != testCase.wantHeld {
				t.Errorf("held generation = %q, want %q", found[0].HeldCompatibilityKey, testCase.wantHeld)
			}
		})
	}
}

// TestACacheStillCountsWhenTheApplicationHasMovedOn is the case a host holding
// several generations creates. What is under the name now is the newest, and that
// is what the evidence reports; content the application still says it can use is
// still warm, whatever it wrote afterwards.
func TestACacheStillCountsWhenTheApplicationHasMovedOn(t *testing.T) {
	first := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	inventory := domain.CacheInventory{Known: true, ObservedAt: first.Add(time.Hour), Mounts: []domain.CacheMount{
		{Name: compilerCache, CompatibilityKey: "cuda-12.4", CreatedAt: first},
		{Name: compilerCache, CompatibilityKey: "cuda-13.0", CreatedAt: first.Add(time.Hour)},
	}}

	newest, holds := inventory.Held(compilerCache)

	if !holds || newest.CompatibilityKey != "cuda-13.0" {
		t.Fatalf("what is under the name is %+v, want the generation written last", newest)
	}
	older := domain.CacheMountRequirement{Name: compilerCache, CompatibilityKey: "cuda-12.4"}
	if !inventory.Holds(older) {
		t.Fatal("a generation the application still asks for is on this machine and was not found")
	}
}

// TestACacheVolumeNameSeparatesEveryPartOfTheIdentity is the production half of
// cache generation isolation.
func TestACacheVolumeNameSeparatesEveryPartOfTheIdentity(t *testing.T) {
	cache := domain.CacheMountRequirement{Name: compilerCache, CompatibilityKey: "cuda-12.4"}
	nextGeneration := domain.CacheMountRequirement{Name: compilerCache, CompatibilityKey: "cuda-13.0"}

	mine := domain.CacheVolumeName(cache)

	if mine == domain.CacheVolumeName(nextGeneration) {
		t.Error("two generations of one cache derive one volume")
	}
	if mine != domain.CacheVolumeName(cache) {
		t.Error("one cache identity derives two volumes, so nothing would ever be found warm")
	}
}

// TestAWorkloadDeclaringAnUnusableCacheIsRefused keeps the name a name and the
// generation something a holder can write down. Both are identity, and both are
// stamped into a container runtime's own record of the storage they name, so a
// declaration nothing can address or record is refused where it enters rather
// than escaped wherever a volume gets built from it.
func TestAWorkloadDeclaringAnUnusableCacheIsRefused(t *testing.T) {
	for name, declared := range map[string][]domain.CacheMountRequirement{
		"a name no volume can carry": {{Name: "Compiler Cache"}},
		"no name at all":             {{CompatibilityKey: "cuda-12.4"}},
		"one cache declared twice":   {{Name: compilerCache}, {Name: compilerCache, CompatibilityKey: "cuda-12.4"}},
		"room stated as negative":    {{Name: compilerCache, SizeBytes: -1}},
		// A key carrying the separators a container runtime's own option list is
		// parsed on. Stamped as it stands it would be read as further options
		// rather than as a generation, so what a cache under it holds could
		// never be established.
		"a generation no holder can record": {{Name: compilerCache, CompatibilityKey: "cuda-12.4,volume-nocopy=true"}},
	} {
		t.Run(name, func(t *testing.T) {
			revision := workloadRevisionWithCaches(declared)

			violations := domain.ValidateWorkloadRevision(revision)

			if len(violations) == 0 {
				t.Fatalf("a workload declaring %+v was accepted", declared)
			}
		})
	}
}

func workloadRevisionWithCaches(caches []domain.CacheMountRequirement) domain.WorkloadRevision {
	return domain.WorkloadRevision{
		ID:         "wrev_cache",
		WorkloadID: "wrk_cache",
		Digest:     "sha256:cache",
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "builder@sha256:3c9d5f1a7e2b4c6d8f0a1b3c5d7e9f0a2b4c6d8e0f1a3b5c7d9e1f3a5b7c9d1e",
				Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
			}},
			Caches: caches,
		},
	}
}
