package lab

import (
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
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

func admissionObservation(now time.Time, workloads map[string]domain.WorkloadRevision, events []eventlog.CloudEvent) InvariantObservation {
	return InvariantObservation{
		StartedAt:      now,
		Now:            now,
		World:          WorldTruthSnapshot{At: now},
		Workloads:      workloads,
		MercatorEvents: events,
	}
}
