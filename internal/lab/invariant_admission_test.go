package lab

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

// TestBackfillMayTakeCapacityGoingSpare and the test below it are the two halves
// of the one exemption in safety.service_class_admission_order. The registry's
// deliberate case drives the rule itself; these drive the carve-out, because a
// carve-out nothing exercises is either dead or a hole.
//
// Capacity going spare may be taken by a class that declared itself eligible for
// it, even while a class that outranks it is waiting. That is what backfill is
// for, and a rule that refused it would leave a machine idle beside work that
// says it will take whatever is free.
func TestBackfillMayTakeCapacityGoingSpare(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, map[string]domain.WorkloadRevision{
		"run-spare": classedWorkload(domain.ClassOpportunistic),
	}, []eventlog.CloudEvent{
		admissionDeferredEvent("run-watched", now, domain.ClassInteractive),
		admittedDecisionEvent("run-spare", now.Add(time.Minute)),
	})

	if err := serviceClassAdmissionOrder(observation); err != nil {
		t.Fatalf("backfill onto spare capacity is what the exemption is for, and the rule refused it: %v", err)
	}
}

// TestBackfillMayNotTakeTheSlotAStarvedRunIsWaitingFor is the deliberate failure
// of the same clause. Six minutes is past the five an interactive Run's class
// allows it to wait, so the machine coming free is not capacity going spare: it
// is the capacity that Run is owed, and taking it is the starvation the aging
// rule exists to make impossible.
func TestBackfillMayNotTakeTheSlotAStarvedRunIsWaitingFor(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	observation := admissionObservation(now, map[string]domain.WorkloadRevision{
		"run-spare": classedWorkload(domain.ClassOpportunistic),
	}, []eventlog.CloudEvent{
		admissionDeferredEvent("run-watched", now, domain.ClassInteractive),
		admittedDecisionEvent("run-spare", now.Add(6*time.Minute)),
	})

	err := serviceClassAdmissionOrder(observation)
	if err == nil {
		t.Fatalf("a backfill took the slot a starved interactive Run was waiting for and the rule allowed it")
	}
	t.Logf("violation: %v", err)
}

// TestAnImpossibleAskEmptiesNoFleetUnderTheRealControlPlane is the queue's second
// law at L1. The placement corpus can show admission ordering two Runs; only this
// shows the machines actually running the other two, through the offer catalog, with
// the real orchestrator, event log, and Run projection in the loop, while the Run
// nothing can hold goes on waiting beside them.
//
// The fleet is busy while the impossible ask is weighed, which is what makes the
// case. One machine is five hours into work of its own, so a classification read off
// the Bookings Mercator holds rather than off what each machine refused calls the
// impossible ask a wait for capacity to come free, keeps the queue with it, and
// leaves the idle machine standing beside the Run that fits it.
//
// It is driven to completion because the impossible Run never becomes placeable. The
// execution has to reach the moment its class says the answer stopped being worth
// having and come back with that Run closed and nothing else stopped by it.
func TestAnImpossibleAskEmptiesNoFleetUnderTheRealControlPlane(t *testing.T) {
	execution := openBlueprintExecution(t, "../scenario/scenarios/conformance/an-impossible-ask-empties-no-fleet.json", DefaultLimits())
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive a fleet holding one Run it can never place: %v", err)
	}

	decisions := bookingDecisions(t, execution)
	if selected := decisions["run-occupies"].SelectedOfferSnapshotID; selected != "rental-big" {
		t.Fatalf("the Run needing 150GB was placed on %q, and only the 200GB machine has the room", selected)
	}
	if selected := decisions["run-fits"].SelectedOfferSnapshotID; selected != "rental-small" {
		t.Fatalf("the Run that fits this fleet was placed on %q, and the idle machine here has room for it", selected)
	}
	// The Run nothing could place is answered and placed nowhere, which are two
	// statements now. Before, a Run that found no feasible offer recorded no
	// decision at all, so its whole account of itself was the reason code and the
	// two counts asserted below, and every rule that reads Booking Decisions had
	// nothing to read on the one Run in this fleet whose refusal is the point.
	refusal, answered := decisions["run-impossible"]
	if !answered {
		t.Fatal("the Run nothing could place has no recorded decision to be explained from")
	}
	if refusal.SelectedOfferSnapshotID != "" {
		t.Fatalf("a Run asking for 900GB was placed on %q, and the largest machine in this fleet has 200GB", refusal.SelectedOfferSnapshotID)
	}
	if len(refusal.Candidates) != 2 {
		t.Fatalf("the recorded refusal weighed %d machines, and this fleet has two", len(refusal.Candidates))
	}
	for _, candidate := range refusal.Candidates {
		if candidate.Feasible || len(candidate.Rejections) == 0 {
			t.Fatalf("the recorded refusal has nothing to say about why %q could not take the Run: %+v", candidate.OfferSnapshotID, candidate)
		}
	}
	// It was never told to wait at all. A Run ordered behind the impossible ask is
	// the defect, and it is visible as one deferral of a Run the fleet had room for
	// the moment it arrived.
	if deferral, waited := admissionRecord(t, execution, "run-fits"); waited {
		t.Fatalf("the Run that fits was told to wait for %q behind %v", deferral.Reason, deferral.Behind)
	}
	waiting, _ := admissionRecord(t, execution, "run-impossible")
	if waiting.Reason != domain.DeferredNoCapacityFits {
		t.Fatalf("the impossible Run's record says it waited for %q, and every machine in this fleet was weighed against it", waiting.Reason)
	}
	if waiting.Fleet == nil {
		t.Fatal("the impossible Run's record says nothing at all about the fleet it was measured against")
	}
	if waiting.Fleet.Weighed != 2 || waiting.Fleet.CouldHold != 0 {
		t.Fatalf("the record says %d machines were weighed and %d of them could hold this Run, and the fleet has two machines and neither can",
			waiting.Fleet.Weighed, waiting.Fleet.CouldHold)
	}
}

// admissionRecord is the first thing admission said about one Run, read off the
// public log the way an operator reads it, and whether it said anything at all: a
// Run the fleet had room for on arrival was never told to wait.
func admissionRecord(t *testing.T, execution *Execution, runID string) (domain.AdmissionDeferral, bool) {
	t.Helper()
	events, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	for _, event := range events {
		if event.Type != orchestrator.EventAdmissionDeferred || !strings.HasSuffix(event.StreamID, runID) {
			continue
		}
		var payload struct {
			Deferral domain.AdmissionDeferral `json:"deferral"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatalf("read the deferral: %v", err)
		}
		return payload.Deferral, true
	}
	return domain.AdmissionDeferral{}, false
}

func admissionObservation(now time.Time, workloads map[string]domain.WorkloadRevision, events []eventlog.CloudEvent) InvariantObservation {
	return InvariantObservation{
		StartedAt:      now,
		Now:            now,
		World:          WorldTruthSnapshot{At: now},
		Workloads:      workloads,
		MercatorEvents: events,
	}
}
