package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scheduler"
)

// TestARunRecordsTheReadinessItsWorkloadReported is the last stage of a launch
// arriving from the only authority that can state it. The application posts a
// moment on a clock Mercator shares, after the container it belongs to, and the Run
// carries it.
func TestARunRecordsTheReadinessItsWorkloadReported(t *testing.T) {
	ctx := context.Background()
	orch, started := runningRun(t)
	ready := started

	reportReady(t, ctx, orch, ready)

	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.ReadyAt == nil || !record.ReadyAt.Equal(ready) {
		t.Fatalf("the workload reported ready at %s and the Run records %v",
			ready.Format(time.RFC3339Nano), record.ReadyAt)
	}
}

// TestAReadinessAheadOfTheReadThatCarriedItIsNotThisRunsReadiness is the workload
// on a host whose clock runs ahead. The application reads that clock, so its report
// is a moment Mercator has not reached, and filing it would put an hour of invented
// ready latency in the Run Bundle as the workload's own measurement. It is the same
// refusal the observed start moment gets one stage earlier.
func TestAReadinessAheadOfTheReadThatCarriedItIsNotThisRunsReadiness(t *testing.T) {
	ctx := context.Background()
	orch, started := runningRun(t)

	reportReady(t, ctx, orch, started.Add(time.Hour))

	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.ReadyAt != nil {
		t.Fatalf("the Run records a readiness of %s, which its workload published an hour ahead of the read that carried it",
			record.ReadyAt.Format(time.RFC3339Nano))
	}
	events, err := orch.log.ReadStream(ctx, runStream("ws_1", "run_1"), 0, 1000)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if countEvents(events, EventRunReported) != 1 {
		t.Fatalf("the report that carried the claim was not recorded: %v", eventTypes(events))
	}
}

// TestAReadinessBeforeItsContainerStartedIsNotThisRunsReadiness is an application
// serving before the process serving it existed. The two moments come from
// different authorities, a node states one and the workload the other, so nothing
// but the Run's own history can hold them in order.
func TestAReadinessBeforeItsContainerStartedIsNotThisRunsReadiness(t *testing.T) {
	ctx := context.Background()
	orch, started := runningRun(t)

	reportReady(t, ctx, orch, started.Add(-time.Minute))

	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.ReadyAt != nil {
		t.Fatalf("the Run records a readiness of %s against a container that started at %s",
			record.ReadyAt.Format(time.RFC3339Nano), started.Format(time.RFC3339Nano))
	}
}

// TestTheFirstReadinessAWorkloadReportsIsTheOneThatStands is why a second report
// cannot move it. A workload reports readiness once, so a later moment is a repeat
// rather than a correction, and letting it through would rewrite a measurement
// already recorded against a prediction.
func TestTheFirstReadinessAWorkloadReportsIsTheOneThatStands(t *testing.T) {
	ctx := context.Background()
	orch, started := runningRun(t)

	reportReady(t, ctx, orch, started)
	reportReady(t, ctx, orch, started.Add(30*time.Second))

	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.ReadyAt == nil || !record.ReadyAt.Equal(started) {
		t.Fatalf("the Run records a readiness of %v after a second report, and the first one it accepted was %s",
			record.ReadyAt, started.Format(time.RFC3339Nano))
	}
}

// runningRun is one Run whose container a provider has been seen starting, and the
// moment it started. Every readiness case needs both: a moment to order a report
// against, and a Run open enough to report to.
//
// The provider and the control plane read one scripted clock, which is the whole
// point: the law these cases state is about a moment that disagrees with Mercator's
// own reading, and a case whose two clocks were both wall clocks could not say which
// of them a refusal came from.
func runningRun(t *testing.T) (*Orchestrator, time.Time) {
	t.Helper()
	ctx := context.Background()
	scripted := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return scripted }
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", scripted)}),
		fake.WithPublishedStart(0),
		fake.WithNow(now),
	)
	orch := New(openOrchestratorLog(t), scheduler.New(), ad, WithClock(now), withTestCapacity())
	createRun(t, ctx, orch)
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.StartedAt == nil {
		t.Fatalf("this Run has no observed start to order a readiness against: %+v", record)
	}
	return orch, *record.StartedAt
}

func reportReady(t *testing.T, ctx context.Context, orch *Orchestrator, at time.Time) {
	t.Helper()
	report, err := NewApplicationReadyReport(at)
	if err != nil {
		t.Fatalf("build readiness report: %v", err)
	}
	if err := orch.RecordReport(ctx, "ws_1", "run_1", report); err != nil {
		t.Fatalf("record readiness report: %v", err)
	}
}
