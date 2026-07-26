package lab

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

// TestAMachineWithNoRoomRefusesTheWork is the world being a machine about disk.
// Content has to fit somewhere: a host asked to put fifty gigabytes onto twenty
// does not run the workload slowly, it fills up and fails with nothing to show,
// so this world refuses the launch instead of creating content its own ledger
// could not hold. Without the refusal the machine ends up holding more than it
// has, which is the state safety.disk_reservation_respected exists to catch.
//
// The machine is capacity Mercator borrows a slot on, which is what makes this
// reachable at all. Placement refuses a candidate whose content provably will
// not fit, so the only launch that can arrive at a machine with no room is one
// aimed at a machine nobody could enumerate: silence is priced and never
// refused, so Mercator selects it holding no evidence either way, and World
// Truth is what says no.
func TestAMachineWithNoRoomRefusesTheWork(t *testing.T) {
	execution := openBlueprintExecution(t, "testdata/blueprints/a-machine-with-no-room-refuses-the-work.json", DefaultLimits())
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive the Blueprint: %v", err)
	}

	candidate := recordedCandidate(t, execution, "run-too-big-to-land", "cramped-host")
	if !candidate.Feasible {
		t.Fatalf("a machine that could say nothing about what it holds was refused: %+v", candidate.Rejections)
	}
	if candidate.Disk.EstablishedLandBytes != 0 {
		t.Fatalf("Placement established %d bytes of content against a machine that enumerated nothing", candidate.Disk.EstablishedLandBytes)
	}
	ledger := diskLedgerFor(t, execution, "cramped-host")
	if len(ledger.Resident) > 0 {
		t.Fatalf("the machine that refused the work is holding %+v", ledger.Resident)
	}
	if ledger.ReservedBytes != 0 {
		t.Fatalf("the machine that refused the work reserved %d bytes for it", ledger.ReservedBytes)
	}
	if free := ledger.FreeBytes(); free != ledger.CapacityBytes {
		t.Fatalf("the machine offers %d of its %d bytes, and it took none of the work", free, ledger.CapacityBytes)
	}
}

// recordedCandidate is what one Booking Decision said about one candidate. Cases
// about what Placement decided read the record rather than the world: the
// decision is the thing under judgment, and it is the only place a candidate
// Mercator refused leaves any trace at all.
func recordedCandidate(t *testing.T, execution *Execution, runID, offerID string) domain.CandidateDecision {
	t.Helper()
	stored, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	for _, event := range stored {
		cloud := event.CloudEvent()
		if cloud.Type != orchestrator.EventBookingDecided || cloud.Subject != "runs/"+runID {
			continue
		}
		var payload struct {
			Decision domain.BookingDecision `json:"decision"`
		}
		if err := json.Unmarshal(cloud.Data, &payload); err != nil {
			t.Fatalf("decode the Booking Decision for %s: %v", runID, err)
		}
		for _, candidate := range payload.Decision.Candidates {
			if candidate.OfferSnapshotID == offerID {
				return candidate
			}
		}
		t.Fatalf("Run %q was decided over %d candidates and %q is not one of them", runID, len(payload.Decision.Candidates), offerID)
	}
	t.Fatalf("Run %q recorded no Booking Decision", runID)
	return domain.CandidateDecision{}
}

func diskLedgerFor(t *testing.T, execution *Execution, offerID string) DiskLedger {
	t.Helper()
	for _, ledger := range execution.runtime.world.truthSnapshot().Disk {
		if ledger.OfferID == offerID {
			return ledger
		}
	}
	t.Fatalf("no machine %q in this world's disk ledgers", offerID)
	return DiskLedger{}
}

// TestARunThatCannotWriteItsOutputFails is disk being a resource on the way out
// as well as on the way in. Content a Run produces lands on the machine that
// computed it, and nobody could have priced it: a Run declares which Artifacts
// it publishes and never how large they will be, so the room it gets is the room
// it reserved. A workload that writes past it does not publish a smaller
// Artifact, it fails with its disk full. Without that this world creates forty
// gigabytes of content on a twenty gigabyte machine and reports it as a Mercator
// safety violation, which is a rule about Mercator answering for a fact about a
// disk.
func TestARunThatCannotWriteItsOutputFails(t *testing.T) {
	execution := openBlueprintExecution(t, "testdata/blueprints/a-run-that-cannot-write-its-output-fails.json", DefaultLimits())
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.Drive(context.Background(), Advance(2*time.Minute)); err != nil {
		t.Fatalf("advance into the Run: %v", err)
	}
	running := diskLedgerFor(t, execution, "producer-rental")
	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive the Blueprint: %v", err)
	}

	if running.ReservedBytes < 5_000_000_000 {
		t.Fatalf("the machine reserved %d bytes while running a Run that asked for five gigabytes", running.ReservedBytes)
	}
	world := execution.runtime.world
	if version, _ := world.store.entry("artifact:checkpoint:v1"); version.Durable() {
		t.Fatalf("a checkpoint the machine had nowhere to write became durable: %+v", version)
	}
	if replicas := world.artifactReplicas(); len(replicas) != 0 {
		t.Fatalf("the machine holds %+v of an output it had no room for", replicas)
	}
	ledger := diskLedgerFor(t, execution, "producer-rental")
	if used := ledger.ResidentBytes() + ledger.ReservedBytes; used > ledger.CapacityBytes {
		t.Fatalf("the machine accounts for %d bytes on a %d byte disk", used, ledger.CapacityBytes)
	}
}
