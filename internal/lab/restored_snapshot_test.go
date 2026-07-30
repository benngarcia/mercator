package lab

import (
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
)

// TestARestoredSnapshotIsReadOutOfTheObjectStore is the copy nothing can vouch
// for, at L1, where the estimate and the execution have to be the same fact.
// The host reports a checked copy of the dataset and the bytes under that name
// are the version before it, which is the state restoring a volume snapshot
// leaves a machine in. Placement charges the whole read for it, and so does the
// world: the Run reads 40GB out of the object store.
//
// What the machine holds afterwards is the restored snapshot, untouched. A
// workload reads its inputs into its own container, and nothing of Mercator's
// hashed or filed those bytes, so this host stays exactly as wrong as it was and
// the next Run sent here is priced the same 640 seconds. Repairing it is a
// preparation Mercator issues, and nothing was ever queued here to ask for one.
//
// A placement corpus cannot reach this. Every predicate in the control plane
// already compares the copy's digest against the catalog, so a Booking Decision
// looks right whatever the world would then do, and only an execution can show
// which bytes the workload was handed.
func TestARestoredSnapshotIsReadOutOfTheObjectStore(t *testing.T) {
	execution := openConformanceExecution(t, "a-restored-snapshot-is-not-a-copy")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 20)

	candidate := candidateFor(t, bookingDecisions(t, execution)["run-consumer"], restoredHost)
	if len(candidate.ArtifactEvidence) != 1 || candidate.ArtifactEvidence[0].Locality != domain.LocalityCold {
		t.Fatalf("the machine holding another version's bytes records %+v", candidate.ArtifactEvidence)
	}
	// 40GB across a 500 Mbps link is 640 seconds, which is the read this
	// candidate owes however much content it is sitting on.
	if seconds := candidate.Estimates.Stages.ArtifactFetch.Expected; seconds != 640 {
		t.Errorf("the decision priced %v seconds of Artifact read for a copy of other content", seconds)
	}
	if source := artifactReadSource(t, execution, "run-consumer", imagenetArtifact); source != "object_store" {
		t.Fatalf("the Run read its input from %q, and that copy is another version's bytes", source)
	}
	replica := replicaOf(t, execution, imagenetArtifact, restoredHost)
	catalog := worldFactsOf(execution).ArtifactCatalog[imagenetArtifact]
	if replica.ContentDigest == catalog.ContentDigest {
		t.Fatalf("the machine holds %+v after the Run read the object store, and no fetch of Mercator's ever landed here",
			replica)
	}
}

const (
	imagenetArtifact = "artifact:imagenet:v2.41"
	restoredHost     = "rental-restored-snapshot"
)
