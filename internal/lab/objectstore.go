package lab

import (
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scenario"
)

// labObjectStoreMbps is how fast this world moves Artifact content in and out
// of the object store. It is the world's own transfer model, deliberately
// independent of anything Mercator predicts, and it is what makes publication a
// moment rather than an instant: a producer's local copy exists before the
// durable one does, and a consumer that could not tell those apart would be
// admitted on the strength of bytes sitting on one machine.
const labObjectStoreMbps = domain.DefaultObjectStoreDownloadMbps

// objectStore is the durable authority for Artifacts in this world. It holds
// two facts that are constantly confused and are not the same: what an Artifact
// version IS, which the catalog states from the start, and whether its bytes
// are HERE, which only a publication establishes. A host-local replica answers
// neither: it is a copy of a publication and never a substitute for one.
type objectStore struct {
	// catalog is every version this world defines, by version ID.
	catalog map[string]domain.ArtifactVersion
	// publishedAt is when each version's bytes landed here. A version missing
	// from this map is a name for content nothing can read yet, however many
	// machines are holding something.
	publishedAt map[string]time.Time
}

func newObjectStore(workspaceID string, artifacts []scenario.ArtifactSpec, start time.Time) *objectStore {
	store := &objectStore{
		catalog:     make(map[string]domain.ArtifactVersion, len(artifacts)),
		publishedAt: map[string]time.Time{},
	}
	for _, artifact := range artifacts {
		store.catalog[artifact.ID] = artifact.Version(workspaceID)
		// An Artifact no Run in this Blueprint produces is content that existed
		// before the world started, which is what makes it consumable at once.
		// It goes in through the same publication as everything else, because
		// where content was produced is part of publishing it: a fixture stating
		// the machine is stating history, and history reaches the catalog the one
		// way anything reaches it.
		if artifact.Prepublished() {
			store.publish(artifact.ID, publication{onRentalID: artifact.ProducedOn}, start)
		}
	}
	return store
}

// entry is what the catalog says this version is, whether or not it is durable.
// It is what the store answers Mercator's admission question with: a version
// nothing published comes back with no publication time, which is the same
// answer as a name the store never heard of, because from a consumer's side
// those are one fact. Presence on some machine is not part of the answer, which
// is why nothing here can be asked about a host.
func (store *objectStore) entry(artifactID string) (domain.ArtifactVersion, bool) {
	version, known := store.catalog[artifactID]
	if !known {
		return domain.ArtifactVersion{}, false
	}
	version.PublishedAt = store.publishedAt[artifactID]
	return version, true
}

// publication is who a version came from: the Run that wrote it and the reusable
// capacity it was written on. The machine is empty for content published from
// capacity that keeps nothing, because a one-shot execution's host is gone once
// its workload exits and no later Run can be sent there.
type publication struct {
	byRunID    string
	onRentalID string
}

// publish records that a version's bytes reached the object store, and where they
// came from. A version is immutable, so the first publication is the only one and
// a second is ignored rather than allowed to rewrite when the content became
// readable or which machine wrote it.
func (store *objectStore) publish(artifactID string, from publication, at time.Time) domain.ArtifactVersion {
	version := store.catalog[artifactID]
	if _, published := store.publishedAt[artifactID]; !published {
		store.publishedAt[artifactID] = at
		version.ProducedByRunID = from.byRunID
		version.ProducedOnRentalID = from.onRentalID
		store.catalog[artifactID] = version
	}
	entry, _ := store.entry(artifactID)
	return entry
}

// replicaOf is the local copy that fetching this version leaves on a host: the
// catalog's own digest and size, checked on arrival, which is what makes the
// copy worth reading instead of the object store.
func (store *objectStore) replicaOf(artifactID string, at time.Time) domain.ArtifactReplica {
	version := store.catalog[artifactID]
	return domain.ArtifactReplica{
		ArtifactID:    version.ID,
		ContentDigest: version.ContentDigest,
		SizeBytes:     version.SizeBytes,
		State:         domain.ArtifactReplicaVerified,
		VerifiedAt:    at,
	}
}

// transferDuration is how long this world takes to move one version to or from
// the object store.
func (store *objectStore) transferDuration(artifactID string) time.Duration {
	return transferDuration(store.catalog[artifactID].SizeBytes, labObjectStoreMbps)
}

// versions is the whole catalog, which is what an invariant reads to check a
// copy against the content its version is supposed to be.
func (store *objectStore) versions() map[string]domain.ArtifactVersion {
	versions := make(map[string]domain.ArtifactVersion, len(store.catalog))
	for id := range store.catalog {
		versions[id], _ = store.entry(id)
	}
	return versions
}
