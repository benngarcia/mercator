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

// DefaultObjectStoreDownloadMbps is what a host is assumed to read Artifact
// content out of the object store at when nothing has measured that link. It
// stands beside DefaultRegistryDownloadMbps for the same reason and with the
// same standing: a stated assumption rather than a measurement, said once so a
// predictor and a simulated world cannot disagree about what neither measured.
const DefaultObjectStoreDownloadMbps = 500.0

// ArtifactLocation is where the durable copy of one version lives. Identity
// determines the address: a version is immutable, so there is exactly one place
// its bytes can be, and deriving it is what keeps two records from naming
// different homes for the same content.
func ArtifactLocation(artifactID string) string {
	return "mercator://artifacts/" + artifactID
}

// ArtifactVersion is the catalog entry for one immutable version of an
// Artifact. The version is the identity: content never changes under a version
// ID, so a consumer that names one names exactly the bytes it will read.
type ArtifactVersion struct {
	// ID is the version identity a workload declares, unique in this broker.
	ID string `json:"id"`
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

// Holds reports whether this host has a copy a Run may read in place of the
// object store: present, checked, and checked against THIS version's digest. A
// copy nobody verified is not evidence that the right bytes are here, and a copy
// of other content under the same name is worse than no copy at all, so both
// still owe the whole read.
func (inventory ArtifactInventory) Holds(version ArtifactVersion) bool {
	replica, held := inventory.Replica(version.ID)
	return held && replica.State.Usable() && replica.ContentDigest == version.ContentDigest
}

// ArtifactEvidence is what one candidate was found holding of one Artifact the
// Run reads, and what it would still have to read out of the object store. Only
// the control plane can state it: the host says which copy it has and what that
// copy was checked against, the catalog says what the version is, and the answer
// is whether those two agree.
//
// There is no partial. An Artifact version is one immutable object, so a host
// either has bytes that were checked against it or owes all of them.
type ArtifactEvidence struct {
	ArtifactID string        `json:"artifact_id"`
	Locality   LocalityState `json:"locality"`
	FetchBytes int64         `json:"fetch_bytes,omitempty"`
}

// ArtifactFetchWork is what this host still owes on the content a Run declared
// reading, and what was found of each version. Silence costs what absence costs,
// exactly as it does for images: a host that cannot enumerate its copies is not
// a host with nothing to fetch, and pricing it at zero would score a machine
// nobody can describe like one provably holding every byte.
//
// One host's replica store is the only place a copy a Run may read can be, which
// is why the inventory is the whole answer and a record of where content was
// produced is not part of it. The machine a version's bytes were written on is
// not thereby a machine holding a readable copy of them: a workload writes its
// output inside its own container, nothing files that content as a replica, and
// bytes no verification ever touched are bytes no consumer may be sent to read.
// A host that enumerated and found no copy of this version has answered about
// every copy anybody could use, and charging it the whole read is that answer
// taken at its word.
func ArtifactFetchWork(versions []ArtifactVersion, inventory ArtifactInventory) (int64, []ArtifactEvidence) {
	if len(versions) == 0 {
		return 0, nil
	}
	fetch := int64(0)
	evidence := make([]ArtifactEvidence, 0, len(versions))
	for _, version := range versions {
		found := ArtifactEvidence{ArtifactID: version.ID, Locality: LocalityCold, FetchBytes: version.SizeBytes}
		switch {
		case !inventory.Known:
			found.Locality = LocalityUnknown
		case inventory.Holds(version):
			found.Locality, found.FetchBytes = LocalityHot, 0
		}
		fetch += found.FetchBytes
		evidence = append(evidence, found)
	}
	return fetch, evidence
}

// ArtifactRequirements is what a workload reads and what it publishes, by
// version ID. Consuming is a dependency with blocked-until-durable semantics:
// a Run is not admitted until every version it names is in the object store.
type ArtifactRequirements struct {
	Consumes []string `json:"consumes,omitempty"`
	Produces []string `json:"produces,omitempty"`
}
