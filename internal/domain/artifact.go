package domain

import "time"

// This file is the Artifact half of locality. An Artifact is immutable,
// versioned content one Run produced and other Runs read. Its durable authority
// is an object store, never a host: a machine holding a copy makes a Run
// faster and is never what makes the content exist. Mercator therefore records
// two different facts about one Artifact, and nothing may substitute either for
// the other:
//
//   - ArtifactVersion is the catalog entry: what this version is, what it
//     hashes to, where the durable copy lives, and which Run published it.
//   - ArtifactReplica is one host's local copy of that version, worth exactly
//     what its verification against the catalog digest says it is worth.
//
// Modelling a replica as presence is the distributed-filesystem answer this
// architecture refuses: it makes a Run's dependency satisfiable by whichever
// machine happens to be holding bytes, and unsatisfiable the moment that
// machine goes away.

// ArtifactVersion is the catalog entry for one immutable version of an
// Artifact. The version is the identity: content never changes under a version
// ID, so a consumer that names one names exactly the bytes it will read.
type ArtifactVersion struct {
	// ID is the version identity a workload declares, unique in its workspace.
	ID string `json:"id"`
	// WorkspaceID is the scope this version belongs to. Artifacts are never
	// shared across workspaces, whatever their content.
	WorkspaceID string `json:"workspace_id"`
	// ContentDigest is what the bytes hash to. It is what a local copy is
	// checked against, which is the only thing that makes a copy trustworthy.
	ContentDigest string `json:"content_digest"`
	SizeBytes     int64  `json:"size_bytes"`
	// Location is the durable copy in the object store, as a URI. Its presence
	// is what makes this version consumable at all.
	Location string `json:"location"`
	// ProducedByRunID is the Run that published this version. Empty on content
	// that existed before Mercator saw it.
	ProducedByRunID string `json:"produced_by_run_id,omitempty"`
	// PublishedAt is when the durable copy landed. A version with no
	// publication is a name for content nothing can yet read.
	PublishedAt time.Time `json:"published_at,omitzero"`
}

// Durable answers whether this version's bytes are in the object store. It is
// the only admissible answer to "can a consumer run": a host-local copy is an
// optimisation over this and never a replacement for it.
func (version ArtifactVersion) Durable() bool {
	return version.Location != "" && !version.PublishedAt.IsZero()
}

// ArtifactReplicaState is what one host-local copy of an Artifact is worth.
type ArtifactReplicaState string

const (
	// ArtifactReplicaVerified is a copy whose bytes were hashed and matched the
	// catalog entry's content digest. Only a verified replica may be read in
	// place of the object store.
	ArtifactReplicaVerified ArtifactReplicaState = "verified"
	// ArtifactReplicaUnverified is a copy that is present and has not been
	// checked against the catalog. It is not evidence that the right bytes are
	// here, so a Run that needs the Artifact still owes a fetch.
	ArtifactReplicaUnverified ArtifactReplicaState = "unverified"
)

func (state ArtifactReplicaState) Valid() bool {
	return state == ArtifactReplicaVerified || state == ArtifactReplicaUnverified
}

// Usable answers whether a Run may read this copy instead of the object store.
func (state ArtifactReplicaState) Usable() bool { return state == ArtifactReplicaVerified }

// ArtifactReplica is one host's local copy of one Artifact version. It carries
// the digest the copy claims and when that claim was last checked, because a
// copy nobody verified and a copy that matches the catalog are different facts
// and pricing them alike is how a Run reads the wrong bytes quickly.
type ArtifactReplica struct {
	ArtifactID    string               `json:"artifact_id"`
	ContentDigest string               `json:"content_digest"`
	SizeBytes     int64                `json:"size_bytes"`
	State         ArtifactReplicaState `json:"state"`
	VerifiedAt    time.Time            `json:"verified_at,omitzero"`
}

// ArtifactInventory is the Artifact content one host says it holds, as of the
// moment it looked. Like ImageInventory it states what is here and separately
// whether anything enumerated at all: capacity Mercator runs nothing of its own
// on holds whatever it holds and can report none of it, and an empty list from
// such a machine is silence rather than absence.
type ArtifactInventory struct {
	Known      bool              `json:"known"`
	ObservedAt time.Time         `json:"observed_at,omitzero"`
	Replicas   []ArtifactReplica `json:"replicas,omitempty"`
}

// Replica answers what this host holds of one Artifact version.
func (inventory ArtifactInventory) Replica(artifactID string) (ArtifactReplica, bool) {
	for _, replica := range inventory.Replicas {
		if replica.ArtifactID == artifactID {
			return replica, true
		}
	}
	return ArtifactReplica{}, false
}

// ArtifactRequirements is what a workload reads and what it publishes, by
// version ID. Consuming is a dependency with blocked-until-durable semantics:
// a Run is not admitted until every version it names is in the object store.
type ArtifactRequirements struct {
	Consumes []string `json:"consumes,omitempty"`
	Produces []string `json:"produces,omitempty"`
}
