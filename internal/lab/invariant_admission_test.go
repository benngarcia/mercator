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
// shows the machine actually running the second one, through the offer catalog, with
// the real orchestrator, event log, and Run projection in the loop, while the Run
// nothing can hold goes on waiting beside it.
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
	if selected := decisions["run-fits"].SelectedOfferSnapshotID; selected != "rental-only" {
		t.Fatalf("the Run that fits this fleet was placed on %q, and the one machine here has room for it", selected)
	}
	if _, placed := decisions["run-impossible"]; placed {
		t.Fatalf("a Run asking for 900GB was placed on a machine with 200GB")
	}
	waiting := recordedAdmission(t, execution, "run-impossible")
	if waiting.Reason != domain.DeferredNoCapacityFits && waiting.Reason != domain.RefusedDeadlineUnreachable {
		t.Fatalf("the impossible Run's record says it waited for %q", waiting.Reason)
	}
}

// recordedAdmission is the first thing admission said about one Run, read off the
// public log the way an operator reads it.
func recordedAdmission(t *testing.T, execution *Execution, runID string) domain.AdmissionDeferral {
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
		return payload.Deferral
	}
	t.Fatalf("admission recorded nothing at all about %q", runID)
	return domain.AdmissionDeferral{}
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
