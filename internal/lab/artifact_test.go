package lab

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
// The producer writes its checkpoint on the host it ran on, and the object store
// takes it a transfer later. Between those two moments the bytes exist on a
// machine and the Artifact does not exist, and where Mercator placed the consumer
// is the test of which of the two it was waiting for. The consumer entered the
// control plane at virtual zero, which is the point: admission is a decision
// Mercator holds a Run through, not a door the Run was kept outside of.
func TestAConsumerWaitsForDurabilityAndNotForACopy(t *testing.T) {
	execution := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 80)

	effects := execution.runtime.world.effectRecords()
	writtenAt := effectTime(t, effects, OperationArtifactWritten, checkpointArtifact)
	publishedAt := effectTime(t, effects, OperationArtifactPublished, checkpointArtifact)
	requestedAt := runRequestedAt(t, execution, "run-checkpoint-consumer")
	placedAt := runPlacedAt(t, execution, "run-checkpoint-consumer")

	// The checkpoint is 10GB and this world moves Artifact content at 500 Mbps,
	// so the upload takes exactly 160 seconds. Asserting the gap is what keeps
	// the fixture able to tell a copy from a publication at all: if the two
	// happened at once there would be no moment at which the two admission rules
	// disagree.
	if gap := publishedAt.Sub(writtenAt); gap != 160*time.Second {
		t.Fatalf(
			"the checkpoint was written locally at %s and durable %s later, and 10GB crosses a 500 Mbps link in 160s",
			writtenAt, gap,
		)
	}
	if !requestedAt.Before(writtenAt) {
		t.Fatalf(
			"the consumer entered Mercator at %s, and this case is about a Run Mercator was holding while its input was produced",
			requestedAt,
		)
	}
	if placedAt.Before(publishedAt) {
		t.Fatalf(
			"Mercator placed the consumer at %s, and its input became durable at %s",
			placedAt, publishedAt,
		)
	}
}

// TestARunHeldByAdmissionIsVisible is half of what makes the gate safe to have.
// A Run waiting on a publication is a Run Mercator has accepted and not placed:
// it is in the projection and it has no Booking Decision. A gate that kept the
// Run out of Mercator instead would hide it from every rule that watches
// admitted work make progress. The other half, that the rule actually fires
// when the publication never lands, is the case below.
func TestARunHeldByAdmissionIsVisible(t *testing.T) {
	execution := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 1)

	observation, err := execution.runtime.invariantObservation(
		context.Background(),
		execution.config.Tape,
		execution.transitions,
	)
	if err != nil {
		t.Fatalf("observe the control plane: %v", err)
	}
	held := runRecord(t, observation, "run-checkpoint-consumer")
	if held.Phase != "requested" {
		t.Fatalf("the Run waiting on a publication is %q, and Mercator has accepted it and not placed it", held.Phase)
	}
	if _, err := execution.runtime.orchestrator.GetBookingDecision(
		context.Background(),
		labWorkspace,
		"run-checkpoint-consumer",
	); err == nil {
		t.Fatal("Mercator placed a Run whose input is not durable")
	}
}

// TestAPublicationThatNeverLandsIsNotAGreenExecution is the other half. The
// producer's only launch is rejected, so nothing publishes the checkpoint and
// its consumer is held by admission forever. Nothing is executing and nothing
// is uploading, so the world owes nothing, and a driver that ended there would
// export a bundle with every invariant passing and a declared arrival that
// never ran. Driving this Blueprint has to reach the liveness bound and come
// back with the violation.
func TestAPublicationThatNeverLandsIsNotAGreenExecution(t *testing.T) {
	execution := openBlueprintExecution(t, "testdata/blueprints/a-publication-that-never-lands.json", DefaultLimits())
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	_, err := execution.DriveToCompletion(context.Background())

	var violation *InvariantViolationError
	if !errors.As(err, &violation) {
		t.Fatalf("driving a Blueprint whose publication never lands gave %v, and its consumer never ran", err)
	}
	if violation.Result.ID != "liveness.admitted_run_progress" {
		t.Fatalf("the execution failed on %q, and a Run that never moved is what this Blueprint is about", violation.Result.ID)
	}
	if !strings.Contains(violation.Result.Violation, "run-checkpoint-consumer") {
		t.Fatalf("the violation names %q", violation.Result.Violation)
	}
}

func runRecord(t *testing.T, observation InvariantObservation, runID string) domain.RunRecord {
	t.Helper()
	for _, run := range observation.Runs {
		if run.ID == runID {
			return run
		}
	}
	t.Fatalf("Run %q is in none of Mercator's records: %+v", runID, observation.Runs)
	return domain.RunRecord{}
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

// TestTheMachineThatWroteTheContentStillReadsTheObjectStore is what a workload's
// own output is worth to the next Run: nothing. The checkpoint consumer lands on
// the very host the checkpoint was written on and reads the object store anyway,
// because a workload writes its output inside its own container and no runtime in
// the tree enumerates, hashes, or files that content. A real node on a real
// daemon reports no copy of what its own workload just wrote, which is what
// internal/nodeagent proves live, and this is the same fact where Placement can
// see it.
//
// Which machine that is comes out of the ledger rather than out of the fixture's
// names. The write says where it happened and the decision says where the
// consumer went, and the case is only about a producing host at all if those two
// are the same machine: an assertion naming "producer-rental" would hold in a
// world where the producer ran somewhere else entirely.
func TestTheMachineThatWroteTheContentStillReadsTheObjectStore(t *testing.T) {
	execution := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 80)

	producer := effectOffer(t, execution.runtime.world.effectRecords(), OperationArtifactWritten, checkpointArtifact)
	decision := bookingDecisions(t, execution)["run-checkpoint-consumer"]
	if decision.SelectedOfferSnapshotID != producer {
		t.Fatalf(
			"the checkpoint was written on %q and its consumer was placed on %q, and this case is a Run landing on the machine holding its input",
			producer, decision.SelectedOfferSnapshotID,
		)
	}
	if source := artifactReadSource(t, execution, "run-checkpoint-consumer", checkpointArtifact); source != "object_store" {
		t.Fatalf("the consumer read its input from %q on the machine that wrote it, and nothing checked those bytes", source)
	}
	candidate := candidateFor(t, decision, producer)
	if len(candidate.ArtifactEvidence) != 1 || candidate.ArtifactEvidence[0].Locality != domain.LocalityCold {
		t.Fatalf("the host that wrote the checkpoint records %+v of it", candidate.ArtifactEvidence)
	}
	// 10GB at 500 Mbps is 160 seconds, which is the read this candidate owes on
	// content it produced itself.
	if seconds := candidate.Estimates.ArtifactSeconds.Expected; seconds != 160 {
		t.Errorf("the decision priced %v seconds of read on content this machine wrote, want 160", seconds)
	}
}

// TestAConsumerReadsTheCopyAFetchLeftBehind is what a replica is for, on the one
// copy anybody may be placed on: one a fetch of Mercator's landed and checked
// against the catalog. The reference consumer fetched the stale set at 45m, and
// the Run behind it reads what that fetch left rather than crossing the link
// again.
func TestAConsumerReadsTheCopyAFetchLeftBehind(t *testing.T) {
	execution := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 80)

	if source := artifactReadSource(t, execution, "run-warm-consumer", staleSetArtifact); source != "replica" {
		t.Fatalf("the consumer read its input from %q, and its host holds a copy a fetch checked", source)
	}
	candidate := candidateFor(t, bookingDecisions(t, execution)["run-warm-consumer"], "producer-rental")
	if len(candidate.ArtifactEvidence) != 1 || candidate.ArtifactEvidence[0].Locality != domain.LocalityHot {
		t.Fatalf("the host holding a checked copy records %+v of it", candidate.ArtifactEvidence)
	}
	if seconds := candidate.Estimates.ArtifactSeconds.Expected; seconds != 0 {
		t.Errorf("the host already holding a checked copy was priced %v seconds to read it", seconds)
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

// TestTheDecisionRecordsWhatEachCandidateHoldsOfTheRunsInputs is Artifact
// locality reaching the record a placement is explained from, under the real
// control plane. The reference consumer reads two Artifacts and lands on a host
// holding an unchecked copy of one of them, so the decision has to say that the
// host holds neither as far as Placement is concerned and price it both reads.
// This is the same claim TestAnUncheckedCopySavesNothing makes about the world,
// asserted where an operator would read it.
func TestTheDecisionRecordsWhatEachCandidateHoldsOfTheRunsInputs(t *testing.T) {
	execution := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 80)

	decisions := bookingDecisions(t, execution)
	consumer := decisions["run-reference-consumer"]
	candidate := candidateFor(t, consumer, "producer-rental")
	if len(candidate.ArtifactEvidence) != 2 {
		t.Fatalf("the candidate records %+v, and this Run reads two Artifacts", candidate.ArtifactEvidence)
	}
	for _, found := range candidate.ArtifactEvidence {
		if found.Locality != domain.LocalityCold || found.FetchBytes == 0 {
			t.Errorf("Artifact %q was recorded %+v on the host holding only an unchecked copy of it", found.ArtifactID, found)
		}
	}
	// 5GB and 2GB at 500 Mbps is 80 + 32 seconds, which is what this candidate
	// still owes on content none of which was ever checked here.
	if seconds := candidate.Estimates.ArtifactSeconds.Expected; seconds != 112 {
		t.Errorf("the decision priced %v seconds of Artifact fetch, and 7GB crosses a 500 Mbps link in 112s", seconds)
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

// driveInMinuteSteps polls at the cadence a control plane would.
func driveInMinuteSteps(t *testing.T, execution *Execution, steps int) {
	t.Helper()
	driveInSteps(t, execution, time.Minute, steps)
}

func driveInSteps(t *testing.T, execution *Execution, step time.Duration, steps int) {
	t.Helper()
	for range steps {
		if _, err := execution.Drive(context.Background(), Advance(step)); err != nil {
			t.Fatalf("drive the execution: %v", err)
		}
	}
}

// TestWhenAnArtifactBecameDurableDoesNotDependOnPolling is the world clock
// claim. Two executions of one Blueprint are driven at cadences ten minutes
// apart, and the two facts an Artifact's identity rests on, when the producer
// wrote it and when the object store had it, are the same instants in both.
// Mercator learns those facts later in the slow execution, which is what
// polling less often is allowed to change; what a machine did and when is not.
func TestWhenAnArtifactBecameDurableDoesNotDependOnPolling(t *testing.T) {
	fast := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := fast.Close(); err != nil {
			t.Fatalf("close the fast execution: %v", err)
		}
	}()
	slow := openConformanceExecution(t, "artifact-must-be-durable-before-a-consumer-runs")
	defer func() {
		if err := slow.Close(); err != nil {
			t.Fatalf("close the slow execution: %v", err)
		}
	}()

	driveInSteps(t, fast, time.Minute, 20)
	driveInSteps(t, slow, 10*time.Minute, 2)

	fastPublished := worldFactsOf(fast).ArtifactCatalog[checkpointArtifact].PublishedAt
	slowPublished := worldFactsOf(slow).ArtifactCatalog[checkpointArtifact].PublishedAt
	if fastPublished.IsZero() || !fastPublished.Equal(slowPublished) {
		t.Fatalf(
			"the checkpoint became durable at %s when Mercator looked every minute and at %s when it looked every ten",
			fastPublished, slowPublished,
		)
	}
	fastWritten := effectTime(t, fast.runtime.world.effectRecords(), OperationArtifactWritten, checkpointArtifact)
	slowWritten := effectTime(t, slow.runtime.world.effectRecords(), OperationArtifactWritten, checkpointArtifact)
	if fastWritten.IsZero() || !fastWritten.Equal(slowWritten) {
		t.Fatalf(
			"the producer wrote its output at %s under one cadence and %s under the other",
			fastWritten, slowWritten,
		)
	}
}

func worldFactsOf(execution *Execution) worldFacts {
	return execution.runtime.world.invariantFacts()
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
	return runEventTime(t, execution, orchestrator.EventRunRequested, runID)
}

// runPlacedAt is when Mercator decided where this Run would go, which is the
// moment admission let it through.
func runPlacedAt(t *testing.T, execution *Execution, runID string) time.Time {
	t.Helper()
	return runEventTime(t, execution, orchestrator.EventBookingDecided, runID)
}

func runEventTime(t *testing.T, execution *Execution, eventType, runID string) time.Time {
	t.Helper()
	stored, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	for _, event := range stored {
		cloud := event.CloudEvent()
		if cloud.Type != eventType || cloud.Subject != "runs/"+runID {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, cloud.Time)
		if err != nil {
			t.Fatalf("parse %s %s time: %v", runID, eventType, err)
		}
		return at
	}
	t.Fatalf("Run %q never recorded %s", runID, eventType)
	return time.Time{}
}

func effectTime(t *testing.T, effects []EffectRecord, operation, artifactID string) time.Time {
	t.Helper()
	return artifactEffect(t, effects, operation, artifactID).At
}

// effectOffer is the machine an Artifact effect happened on, which is how a test
// asks the ledger where a workload ran instead of assuming it from a fixture.
func effectOffer(t *testing.T, effects []EffectRecord, operation, artifactID string) string {
	t.Helper()
	var request struct {
		OfferID string `json:"offer_id"`
	}
	effect := artifactEffect(t, effects, operation, artifactID)
	if err := json.Unmarshal(effect.Request, &request); err != nil {
		t.Fatalf("decode %s request: %v", operation, err)
	}
	if request.OfferID == "" {
		t.Fatalf("the %s of Artifact %q names no machine: %s", operation, artifactID, effect.Request)
	}
	return request.OfferID
}

func artifactEffect(t *testing.T, effects []EffectRecord, operation, artifactID string) EffectRecord {
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
			return effect
		}
	}
	t.Fatalf("the ledger records no %s for Artifact %q", operation, artifactID)
	return EffectRecord{}
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
