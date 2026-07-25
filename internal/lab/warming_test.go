package lab

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scenario"
)

// TestExecutionWarmsARentalUnderTheRealControlPlane is the warming claim at L1.
// The placement harness can prove the scheduler prices a warm host correctly;
// only this proves the host got warm by running a workload, through the offer
// catalog, with the real orchestrator, event log, and Run projection in the
// loop.
func TestExecutionWarmsARentalUnderTheRealControlPlane(t *testing.T) {
	execution := openConformanceExecution(t, "execution-warms-a-rental")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	// The first Run lands, pulls, completes, and releases its Rental before the
	// second Run arrives at 30m.
	if _, err := execution.Drive(context.Background(), Advance(25*time.Minute)); err != nil {
		t.Fatalf("drive the first Run to completion: %v", err)
	}
	if _, err := execution.Drive(context.Background(), Advance(25*time.Minute)); err != nil {
		t.Fatalf("drive the second Run: %v", err)
	}

	decisions := bookingDecisions(t, execution)
	first := decisions["run-first"]
	second := decisions["run-second"]
	if first.SelectedOfferSnapshotID != "held-4090" {
		t.Fatalf("the first Run landed on %q, want the cheaper Rental", first.SelectedOfferSnapshotID)
	}
	if pull := candidatePullSeconds(t, first, "held-4090"); pull < 200 {
		t.Fatalf("the first Run was priced %.2fs of pull on a cold host, want the whole image", pull)
	}
	if pull := candidatePullSeconds(t, second, "held-4090"); pull != 0 {
		t.Fatalf("the Rental that ran the image is still priced %.2fs of pull", pull)
	}
	if pull := candidatePullSeconds(t, second, "spare-4090"); pull < 200 {
		t.Fatalf("the Rental that ran nothing is priced %.2fs of pull, want the whole image", pull)
	}
}

// TestABorrowedSlotIsPricedTheWholePullEveryTime is the lane's claim at L1. The
// machine exists and keeps running, so the offer is standing capacity; nothing
// Mercator has enrolled on it survives the container, so every Run there pays
// for the image again while the Rental beside it stays warm.
func TestABorrowedSlotIsPricedTheWholePullEveryTime(t *testing.T) {
	execution := openConformanceExecution(t, "borrowed-slot-holds-nothing")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	// Runs arrive on the half hour and each occupies its host for a pull plus a
	// runtime. The Lab reconciles only when it is driven, and liveness.stale_lease_expiry
	// allows an execution five minutes past its deadline, so this drives at the
	// cadence a control plane would poll at rather than in one jump.
	for range 16 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrivals: %v", err)
		}
	}

	decisions := bookingDecisions(t, execution)
	for _, name := range []string{"run-borrowed-first", "run-borrowed-second"} {
		decision := decisions[name]
		if decision.SelectedOfferSnapshotID != "local-docker" {
			t.Fatalf("%s landed on %q, want the cheap borrowed slot", name, decision.SelectedOfferSnapshotID)
		}
		if pull := candidatePullSeconds(t, decision, "local-docker"); pull < 200 {
			t.Fatalf("%s was priced %.2fs of pull on capacity Mercator keeps nothing on", name, pull)
		}
	}
	if pull := candidatePullSeconds(t, decisions["run-borrowed-second"], "held-4090"); pull != 0 {
		t.Fatalf("the Rental that ran the image is priced %.2fs of pull", pull)
	}
}

// TestWhatABorrowedMachineHoldsIsNotSomethingMercatorKnows is the other half of
// the lane's locality claim, and the one the retention half cannot make. This
// machine holds every byte of the image before the Run arrives. Nothing of
// Mercator's runs on it, so nothing enumerates it: the offer carries no
// inventory and the Run is priced the whole fetch, which is what every provider
// adapter in the tree produces for the machines it sells. The world still knows
// what the machine holds, and says so where World Truth is stated, because a
// world that erased it at the source would leave the laws about what capacity
// accumulates reading an inventory that is empty whatever happened.
func TestWhatABorrowedMachineHoldsIsNotSomethingMercatorKnows(t *testing.T) {
	execution := openConformanceExecution(t, "borrowed-warmth-is-invisible")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 6 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}

	truth := offerByID(t, execution.runtime.world.truthSnapshot().Offers, "local-docker")
	if !truth.Images.Holds(domain.ReferenceDigest(borrowedWarmthImage)) {
		t.Fatalf("World Truth says the borrowed machine holds %+v, and the Blueprint seeded it the whole image", truth.Images)
	}
	decision := bookingDecisions(t, execution)["run-borrowed"]
	if decision.SelectedOfferSnapshotID != "local-docker" {
		t.Fatalf("the Run landed on %q, want the cheap borrowed slot", decision.SelectedOfferSnapshotID)
	}
	if source := candidatePullSource(t, decision, "local-docker"); source != "inventory_unknown" {
		t.Fatalf("the decision recorded pull source %q for a machine nothing of Mercator's runs on, want its silence named", source)
	}
	if pull := candidatePullSeconds(t, decision, "local-docker"); pull < 200 {
		t.Fatalf("the Run was priced %.2fs of pull from content no offer could carry", pull)
	}
}

const borrowedWarmthImage = "trainer@sha256:5d7e0dc3bcc75e4b3639ed8b3badf9b610b97221c7f8013edc0beebcf34fbc58"

func openConformanceExecution(t *testing.T, name string) *Execution {
	t.Helper()
	return openBlueprintExecution(t, "../scenario/scenarios/conformance/"+name+".json", testLimits())
}

func openBlueprintExecution(t *testing.T, path string, limits Limits) *Execution {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint(path)
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	execution, err := Open(context.Background(), Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           limits,
		Policy:           "policy:test",
		MercatorRevision: "revision:test",
	})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	return execution
}

func bookingDecisions(t *testing.T, execution *Execution) map[string]domain.BookingDecision {
	t.Helper()
	stored, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	decisions := map[string]domain.BookingDecision{}
	for _, event := range stored {
		cloud := event.CloudEvent()
		if cloud.Type != "compute.run.booking_decided.v1" {
			continue
		}
		var payload struct {
			Decision domain.BookingDecision `json:"decision"`
		}
		if err := json.Unmarshal(cloud.Data, &payload); err != nil {
			t.Fatalf("decode Booking Decision: %v", err)
		}
		decisions[payload.Decision.RunID] = payload.Decision
	}
	if len(decisions) == 0 {
		t.Fatalf("no Booking Decision was recorded: %d events", len(stored))
	}
	return decisions
}

func candidatePullSource(t *testing.T, decision domain.BookingDecision, offerID string) string {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate.Estimates.PullSeconds.Source
		}
	}
	t.Fatalf("Run %q has no candidate for offer %q", decision.RunID, offerID)
	return ""
}

func candidatePullSeconds(t *testing.T, decision domain.BookingDecision, offerID string) float64 {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate.Estimates.PullSeconds.Expected
		}
	}
	t.Fatalf("Run %q has no candidate for offer %q", decision.RunID, offerID)
	return 0
}
