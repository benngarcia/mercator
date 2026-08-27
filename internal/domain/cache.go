package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"time"
)

// This file is the mutable half of locality. A Cache Mount is application-owned
// state that survives a workload and has no content identity at all: only the
// application knows what is in it, and Mercator promises nothing about it beyond
// where it is. Three rules separate it from an Artifact:
//
//   - its identity is its broker-scoped name and compatibility key;
//   - the compatibility key is the application's own statement of which
//     generation of content belongs under that name. Mercator compares it and
//     never interprets it: content declared under another generation is worth
//     what no content is worth;
//   - it is best-effort. A missing cache is never a reason to refuse a Run,
//     because the application can always rebuild what was in it.

// cacheNamePattern is what a Cache Mount may be called. The name is identity,
// and it also names a durable volume on whatever host holds the cache, so it is
// constrained at the source rather than escaped at every place that composes
// one.
var cacheNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidCacheName reports whether this is a name a Cache Mount may carry.
func ValidCacheName(name string) bool { return cacheNamePattern.MatchString(name) }

// cacheKeyPattern is what a compatibility key may be. Mercator never interprets
// the key, and it still has to be written down: a container runtime records it
// beside the storage it names, and that record is the only way a host can say
// which generation it is holding. So it is constrained where it enters, for the
// same reason the name is, rather than escaped at every place that stamps one.
var cacheKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]{0,127}$`)

// ValidCacheCompatibilityKey reports whether this is a generation a holder can
// record. An empty key is a cache with one generation, which is why it passes.
func ValidCacheCompatibilityKey(key string) bool {
	return key == "" || cacheKeyPattern.MatchString(key)
}

// CacheMountRequirement is what a workload declares it wants mounted: a name,
// the generation of content it expects to find under it, and how much room it
// expects to use. The broker deployment scopes every cache it names.
type CacheMountRequirement struct {
	Name string `json:"name"`
	// CompatibilityKey is the application's statement of which generation of
	// cached content it can use. An empty key is a cache with one generation.
	CompatibilityKey string `json:"compatibility_key,omitempty"`
	// SizeBytes is how much room the application expects this cache to take. It
	// is a declaration rather than a measurement, and it is what disk
	// reservation will read when prewarming exists.
	SizeBytes int64 `json:"size_bytes,omitempty"`
}

// CacheIdentity is the one string that names a cache. It is derived from all
// two parts of the identity rather than from the name alone, so incompatible
// generations never name one volume.
func CacheIdentity(requirement CacheMountRequirement) string {
	return requirement.Name + "/" + requirement.CompatibilityKey
}

// CacheVolumeName is what a container runtime calls the durable volume holding
// one cache. The name is spelled out so an operator reading `docker volume ls`
// can see which cache this is, and the digest of the full
// identity is appended because a compatibility key is an application's own
// string that no volume name can carry literally.
func CacheVolumeName(requirement CacheMountRequirement) string {
	sum := sha256.Sum256([]byte(CacheIdentity(requirement)))
	return "mercator-cache-" + requirement.Name + "-" + hex.EncodeToString(sum[:4])
}

// CacheMountPath is where a container sees one cache. It is derived rather than
// declared: nothing in this tree needs a workload-chosen path yet, and one
// derivation is one fewer thing for two records to disagree about.
func CacheMountPath(name string) string { return "/mercator/cache/" + name }

// CacheMount is one mutable cache on one host, as the holder reports it. It
// carries no digest and no verification state, because there is nothing to check
// it against: the content is whatever the application last wrote.
//
// It carries no size either. Nothing measures a cache's size on a real node:
// moby prices a volume only by walking every volume on the host, which took 4.8
// seconds for 342 volumes on the machine this was written on and is unbounded on
// a real one, so it is not a read a heartbeat may make. A size reported here
// would therefore be a number the simulated worlds could state and no production
// node could, and prewarming's disk reservation is the slice that earns a
// measured one.
type CacheMount struct {
	Name string `json:"name"`
	// CompatibilityKey is the generation of content under this name, as the
	// application stated it when the cache was written.
	CompatibilityKey string `json:"compatibility_key,omitempty"`
	// CreatedAt is when this generation of the cache started existing here. It
	// is the freshness a container runtime can actually state: a holder that
	// makes a new cache for each compatibility key can say when this one began,
	// and cannot say when anything last read it.
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Identity is the one string naming this cache.
func (mount CacheMount) Identity() string {
	return CacheIdentity(CacheMountRequirement{
		Name:             mount.Name,
		CompatibilityKey: mount.CompatibilityKey,
	})
}

// CacheInventory is the mutable caches one host says it holds, and separately
// whether it enumerated at all. Silence and emptiness are different facts here
// exactly as they are for images and Artifacts: capacity Mercator runs nothing
// of its own on holds whatever it holds and can report none of it.
type CacheInventory struct {
	Known      bool         `json:"known"`
	ObservedAt time.Time    `json:"observed_at,omitzero"`
	Mounts     []CacheMount `json:"mounts,omitempty"`
}

// Held answers which generation is under one broker-scoped name now. A host
// can be holding several: a new compatibility key gets its own storage, and the
// generation before it stays there until something reclaims it. The newest is
// the one under the name, and the older ones are what garbage collection is for.
//
// The generation is the caller's comparison to make, because a cache of the
// wrong generation is a materially different report from no cache at all: one
// says the application changed its own key, the other says this machine has never
// done this work.
func (inventory CacheInventory) Held(name string) (CacheMount, bool) {
	var newest CacheMount
	found := false
	for _, mount := range inventory.Mounts {
		if mount.Name != name {
			continue
		}
		if !found || mount.CreatedAt.After(newest.CreatedAt) {
			newest, found = mount, true
		}
	}
	return newest, found
}

// Holds reports whether this host has a cache a Run may reuse: the same name
// and the generation the application asked for. It is
// asked of every cache here rather than of the newest one, because content the
// application still says it can use is worth reusing whatever it wrote since.
func (inventory CacheInventory) Holds(requirement CacheMountRequirement) bool {
	for _, mount := range inventory.Mounts {
		if mount.Name == requirement.Name &&
			mount.CompatibilityKey == requirement.CompatibilityKey {
			return true
		}
	}
	return false
}

// CacheEvidence is what one candidate was found holding of one cache the Run
// declared. It is recorded and never priced: what a warm cache saves is the
// application's own work, which nothing here has measured, and turning it into
// seconds would be an exchange rate nobody established.
type CacheEvidence struct {
	Name     string        `json:"name"`
	Locality LocalityState `json:"locality"`
	// HeldCompatibilityKey is the generation this host actually holds under the
	// name, when it holds one. It is what separates the two ways a cache can be
	// cold: a machine that has never done this work, and a machine holding the
	// generation before the one the application now asks for.
	HeldCompatibilityKey string `json:"held_compatibility_key,omitempty"`
}

// CacheWarmth is what each declared cache was found to be on one candidate.
// There is no partial: a cache is the application's own state under a name, and
// Mercator has no way to know how much of it is useful.
func CacheWarmth(required []CacheMountRequirement, inventory CacheInventory) []CacheEvidence {
	if len(required) == 0 {
		return nil
	}
	evidence := make([]CacheEvidence, 0, len(required))
	for _, requirement := range required {
		found := CacheEvidence{Name: requirement.Name, Locality: LocalityCold}
		switch held, isHeld := inventory.Held(requirement.Name); {
		case !inventory.Known:
			found.Locality = LocalityUnknown
		case inventory.Holds(requirement):
			found.Locality = LocalityHot
			found.HeldCompatibilityKey = requirement.CompatibilityKey
		case isHeld:
			found.HeldCompatibilityKey = held.CompatibilityKey
		}
		evidence = append(evidence, found)
	}
	return evidence
}

// CacheLandBytes is the room the caches a Run declared would take on one host:
// every generation this host does not already hold, at the size the application
// said it expects it to take. A cache is storage the container runtime creates
// when it attaches the mount, so a machine that has to make one has to have
// somewhere to make it.
//
// The size is a declaration and never a measurement, because no container
// runtime prices a volume without walking every volume on the host. A Run that
// declares nothing asks for nothing, which is the same statement about its own
// state that a Run declaring no ephemeral disk makes.
func CacheLandBytes(required []CacheMountRequirement, inventory CacheInventory) int64 {
	bytes := int64(0)
	for _, requirement := range required {
		if !inventory.Holds(requirement) {
			bytes += requirement.SizeBytes
		}
	}
	return bytes
}
