package lab

import (
	"context"
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
)

// TestAConsumerFollowsTheMachineItsInputWasProducedOn is producer affinity at
// L1, through the real orchestrator, event log, and object store, and it is the
// only place the claim can be checked end to end: the record is written by a
// publication, read by a placement one Run later, and confirmed by what the
// consumer's own read then did.
//
// The world is built so that nothing else can decide the consumer's placement.
// Two Rentals at one price both hold the consumer's image whole, both have room,
// neither is queued, and neither can list the Artifact copies it holds, which is
// the position every runtime in this tree is in about content a workload wrote
// for itself. Only the producer's image separates them, and only for the
// producer.
func TestAConsumerFollowsTheMachineItsInputWasProducedOn(t *testing.T) {
	execution := openConformanceExecution(t, "a-consumer-follows-its-producer")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 40)

	decisions := bookingDecisions(t, execution)
	if produced := decisions["run-producer"].SelectedOfferSnapshotID; produced != producingRental {
		t.Fatalf("the producer ran on %q, and this fixture puts its image on %q alone", produced, producingRental)
	}
	version, err := execution.runtime.world.ArtifactVersion(context.Background(), labWorkspace, checkpointArtifact)
	if err != nil {
		t.Fatalf("ask the object store what it holds: %v", err)
	}
	if version.ProducedOnRentalID != producingRental {
		t.Fatalf("the catalog says the checkpoint was produced on %q, and the Run that published it ran on %q",
			version.ProducedOnRentalID, producingRental)
	}

	consumer := decisions["run-consumer"]
	if consumer.SelectedOfferSnapshotID != producingRental {
		t.Fatalf("the consumer landed on %q, want the machine its input was written on", consumer.SelectedOfferSnapshotID)
	}
	producer := candidateFor(t, consumer, producingRental)
	neighbour := candidateFor(t, consumer, "rental-a-neighbour")
	if len(producer.ArtifactEvidence) != 1 || !producer.ArtifactEvidence[0].ProducedHere {
		t.Fatalf("the decision records %+v and does not name the machine the checkpoint was produced on", producer.ArtifactEvidence)
	}
	if locality := producer.ArtifactEvidence[0].Locality; locality != domain.LocalityUnknown {
		t.Fatalf("the producing machine is recorded %q about a copy its runtime cannot list", locality)
	}
	if producer.Estimates.ArtifactSeconds.Expected != 0 {
		t.Fatalf("the producing machine was priced %.2fs of read for content written on it",
			producer.Estimates.ArtifactSeconds.Expected)
	}
	// The neighbour is priced the read it would actually make, and stays
	// selectable: affinity removes seconds from one candidate and never removes a
	// candidate.
	if neighbour.Estimates.ArtifactSeconds.Expected != 160 {
		t.Fatalf("the neighbour was priced %.2fs, and 10GB crosses a 500 Mbps link in 160s",
			neighbour.Estimates.ArtifactSeconds.Expected)
	}
	if !neighbour.Feasible || !producer.Feasible {
		t.Fatalf("affinity refused a candidate: producer %+v, neighbour %+v", producer.Rejections, neighbour.Rejections)
	}
	// What the estimate promised, read out of the world rather than out of the
	// prediction. The consumer's own read resolved against the copy on that
	// machine, so the 160 seconds it was not charged is a transfer that did not
	// happen.
	if source := artifactReadSource(t, execution, "run-consumer", checkpointArtifact); source != "replica" {
		t.Fatalf("the consumer read its input from %q on the machine that produced it", source)
	}
}

// TestAPreferredMachineIsStillOnlyPreferred is the negative direction at L1,
// read off the same execution. A preference that had grown into a constraint
// would show up as the neighbour being struck out, and an affinity that ignored
// which candidate the record names would show up as both machines being priced
// nothing. Neither is a thing a placement corpus can catch on its own, because
// the record here is written by a publication rather than by a fixture.
func TestAPreferredMachineIsStillOnlyPreferred(t *testing.T) {
	execution := openConformanceExecution(t, "a-consumer-follows-its-producer")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	driveInMinuteSteps(t, execution, 40)

	// Driving the execution fails outright on a violation, so what is left to
	// check is that the rule was asked at all: it is a law about recorded Booking
	// Decisions, and an execution nobody evaluated it over proves nothing.
	checked := 0
	for _, result := range execution.invariants {
		if result.ID != "safety.locality_is_never_infeasibility" {
			continue
		}
		checked++
		if result.Status != InvariantPassed {
			t.Fatalf("locality became infeasibility: %s", result.Violation)
		}
	}
	if checked == 0 {
		t.Fatal("no transition in this execution was checked against safety.locality_is_never_infeasibility")
	}
	consumer := bookingDecisions(t, execution)["run-consumer"]
	neighbour := candidateFor(t, consumer, "rental-a-neighbour")
	if len(neighbour.ArtifactEvidence) != 1 || neighbour.ArtifactEvidence[0].ProducedHere {
		t.Fatalf("the decision names %+v as having produced content that was written on another machine", neighbour.ArtifactEvidence)
	}
	if neighbour.Estimates.ArtifactSeconds.Expected <= 0 {
		t.Fatalf("a machine that never held this content was priced %.2fs to read it",
			neighbour.Estimates.ArtifactSeconds.Expected)
	}
}

const producingRental = "rental-b-producer"
