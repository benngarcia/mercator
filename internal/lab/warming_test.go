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

func openConformanceExecution(t *testing.T, name string) *Execution {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/conformance/" + name + ".json")
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
		Limits:           testLimits(),
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
