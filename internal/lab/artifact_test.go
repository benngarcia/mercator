package lab

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

const (
	checkpointArtifact   = "artifact:checkpoint:v1"
	referenceSetArtifact = "artifact:reference-set:v1"
	staleSetArtifact     = "artifact:stale-set:v1"
)

// TestAConsumerWaitsForDurabilityAndNotForACopy is the durability claim at L1.
// The producer writes its checkpoint onto the host it ran on, and the object
// store takes it a transfer later. Between those two moments a copy of the
// content exists on a machine and the Artifact does not exist, and the consumer
// is the test of which of the two Mercator was waiting for.
func TestAConsumerWaitsForDurabilityAndNotForACopy(t *testing.T) {
	execution := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 80)

	effects := execution.runtime.world.effectRecords()
	writtenAt := effectTime(t, effects, OperationArtifactReplicated, checkpointArtifact)
	publishedAt := effectTime(t, effects, OperationArtifactPublished, checkpointArtifact)
	requestedAt := runRequestedAt(t, execution, "run-checkpoint-consumer")

	// The checkpoint is 10GB and this world moves Artifact content at 500 Mbps,
	// so the upload takes 160 seconds. Asserting the gap is what keeps the
	// fixture able to tell a copy from a publication at all: if the two happened
	// at once there would be no moment at which the two admission rules disagree.
	if gap := publishedAt.Sub(writtenAt); gap < 160*time.Second {
		t.Fatalf(
			"the checkpoint was written locally at %s and durable %s later, and 10GB does not reach an object store that fast",
			writtenAt, gap,
		)
	}
	if requestedAt.Before(publishedAt) {
		t.Fatalf(
			"the consumer entered Mercator at %s, and its input became durable at %s",
			requestedAt, publishedAt,
		)
	}
	if !requestedAt.After(writtenAt) {
		t.Fatalf(
			"the consumer entered Mercator at %s, which is when the producer's local copy appeared rather than when the Artifact existed",
			requestedAt,
		)
	}
}

// TestAConsumerRunsWhenTheOnlyCopyIsGone is the other half. The Rental holding
// the only copy of this Artifact is gone by the time the Run that needs it
// arrives. Nothing about the Artifact changed, because a copy was never what
// made it exist, so the Run is admitted and reads from the object store.
func TestAConsumerRunsWhenTheOnlyCopyIsGone(t *testing.T) {
	execution := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 80)

	truth := execution.runtime.world.truthSnapshot()
	for _, replica := range truth.ArtifactReplicas {
		if replica.ArtifactID == referenceSetArtifact && replica.OfferID == "doomed-rental" {
			t.Fatalf("the Rental holding the only copy is still here: %+v", truth.ArtifactReplicas)
		}
	}
	decision := bookingDecisions(t, execution)["run-reference-consumer"]
	if decision.SelectedOfferSnapshotID == "" {
		t.Fatalf("the Run whose only local copy is gone was never placed: %+v", decision)
	}
	if source := artifactReadSource(t, execution, "run-reference-consumer", referenceSetArtifact); source != "object_store" {
		t.Fatalf("the Run read its input from %q, and no machine holds a copy of it", source)
	}
}

// TestAConsumerReadsTheCopyItsHostAlreadyHolds is what a replica is for. The
// checkpoint consumer lands on the host that produced its input and reads the
// copy there, which is the optimisation the object store makes safe rather than
// the thing that made the Run possible.
func TestAConsumerReadsTheCopyItsHostAlreadyHolds(t *testing.T) {
	execution := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 80)

	if source := artifactReadSource(t, execution, "run-checkpoint-consumer", checkpointArtifact); source != "replica" {
		t.Fatalf("the consumer read its input from %q, and its host wrote that content itself", source)
	}
}

// TestAnUncheckedCopySavesNothing is what verification is for. The host this Run
// lands on is already holding a copy of one of its inputs, and nothing ever
// checked those bytes against the catalog, so the Run fetches from the object
// store as if the copy were not there. A copy nobody vouched for is not a faster
// way to read an Artifact; it is bytes of unknown provenance on a disk.
func TestAnUncheckedCopySavesNothing(t *testing.T) {
	execution := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	before := replicaOf(t, execution, staleSetArtifact, "producer-rental")
	if before.State != domain.ArtifactReplicaUnverified {
		t.Fatalf("the fixture seeded a %q copy, and this case is about an unchecked one", before.State)
	}

	driveInMinuteSteps(t, execution, 80)

	if source := artifactReadSource(t, execution, "run-reference-consumer", staleSetArtifact); source != "object_store" {
		t.Fatalf("the Run read its input from %q, and the copy on that host was never checked", source)
	}
	after := replicaOf(t, execution, staleSetArtifact, "producer-rental")
	if after.State != domain.ArtifactReplicaVerified || after.VerifiedAt.IsZero() {
		t.Fatalf("the fetch left a %+v copy behind, and a fetch checks what it downloaded", after)
	}
}

// TestARunsRecordedWorkloadCarriesItsArtifacts is the control plane's own
// record. What a Run consumes and produces reaches the public event log through
// the workload revision, which is what makes admission a decision about a
// declaration Mercator holds rather than about something only the world knows.
func TestARunsRecordedWorkloadCarriesItsArtifacts(t *testing.T) {
	execution := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 80)

	workloads := recordedWorkloadsOf(t, execution)
	producer := workloads["run-producer"].Spec.Artifacts
	consumer := workloads["run-checkpoint-consumer"].Spec.Artifacts
	if len(producer.Produces) != 1 || producer.Produces[0] != checkpointArtifact {
		t.Fatalf("the producer's recorded workload publishes %v", producer.Produces)
	}
	if len(consumer.Consumes) != 1 || consumer.Consumes[0] != checkpointArtifact {
		t.Fatalf("the consumer's recorded workload reads %v", consumer.Consumes)
	}
}

// driveInMinuteSteps polls at the cadence a control plane would. The step has to
// be shorter than the transfers this fixture is about, or the interval between
// observations would be what separates a local write from a publication and the
// world's own transfer model would decide nothing.
func driveInMinuteSteps(t *testing.T, execution *Execution, steps int) {
	t.Helper()
	for range steps {
		if _, err := execution.Drive(context.Background(), Advance(time.Minute)); err != nil {
			t.Fatalf("drive the execution: %v", err)
		}
	}
}

func replicaOf(t *testing.T, execution *Execution, artifactID, offerID string) domain.ArtifactReplica {
	t.Helper()
	for _, replica := range execution.runtime.world.truthSnapshot().ArtifactReplicas {
		if replica.ArtifactID == artifactID && replica.OfferID == offerID {
			return replica.ArtifactReplica
		}
	}
	t.Fatalf("offer %q holds no copy of Artifact %q", offerID, artifactID)
	return domain.ArtifactReplica{}
}

func recordedWorkloadsOf(t *testing.T, execution *Execution) map[string]domain.WorkloadRevision {
	t.Helper()
	stored, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	workloads, err := recordedWorkloads(stored)
	if err != nil {
		t.Fatalf("read recorded workloads: %v", err)
	}
	return workloads
}

func runRequestedAt(t *testing.T, execution *Execution, runID string) time.Time {
	t.Helper()
	stored, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	for _, event := range stored {
		cloud := event.CloudEvent()
		if cloud.Type != orchestrator.EventRunRequested || cloud.Subject != "runs/"+runID {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, cloud.Time)
		if err != nil {
			t.Fatalf("parse %s request time: %v", runID, err)
		}
		return at
	}
	t.Fatalf("Run %q never entered Mercator", runID)
	return time.Time{}
}

func effectTime(t *testing.T, effects []EffectRecord, operation, artifactID string) time.Time {
	t.Helper()
	for _, effect := range effects {
		if effect.Operation != operation || effect.Command != EffectCommandAccepted {
			continue
		}
		var request struct {
			ArtifactID string `json:"artifact_id"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			t.Fatalf("decode %s request: %v", operation, err)
		}
		if request.ArtifactID == artifactID {
			return effect.At
		}
	}
	t.Fatalf("the ledger records no %s for Artifact %q", operation, artifactID)
	return time.Time{}
}

func artifactReadSource(t *testing.T, execution *Execution, runID, artifactID string) string {
	t.Helper()
	for _, effect := range execution.runtime.world.effectRecords() {
		if effect.Operation != OperationArtifactRead || effect.CorrelationID != runID {
			continue
		}
		var request struct {
			ArtifactID string `json:"artifact_id"`
		}
		var read struct {
			Source string `json:"source"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			t.Fatalf("decode Artifact read request: %v", err)
		}
		if request.ArtifactID != artifactID {
			continue
		}
		if err := json.Unmarshal(effect.Consequence, &read); err != nil {
			t.Fatalf("decode Artifact read consequence: %v", err)
		}
		return read.Source
	}
	t.Fatalf("Run %q never read Artifact %q", runID, artifactID)
	return ""
}
