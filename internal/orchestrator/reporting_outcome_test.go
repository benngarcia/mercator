package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
)

func intPtr(v int) *int { return &v }

// runningProvisionableRun drives a fresh run to RUNNING on a provisionable
// (terminate-disposition) offer and returns the orchestrator + fake adapter.
func runningProvisionableRun(t *testing.T, ctx context.Context) (*Orchestrator, *fake.Adapter) {
	t.Helper()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchProvisionableOffer("off_prov", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance to running: %v", err)
	}
	return orch, ad
}

func TestExitReportZeroRecordsSucceededAndTerminates(t *testing.T) {
	ctx := context.Background()
	orch, ad := runningProvisionableRun(t, ctx)

	if err := orch.RecordReport(ctx, "ws_1", "run_1", "exit", nil, intPtr(0)); err != nil {
		t.Fatalf("record report: %v", err)
	}

	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if !record.Closed || record.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("expected closed+succeeded, got closed=%v outcome=%q", record.Closed, record.Outcome)
	}
	if record.ExitCode == nil || *record.ExitCode != 0 {
		t.Fatalf("expected exit code 0 on record, got %v", record.ExitCode)
	}
	if ad.TerminateCount() != 1 {
		t.Fatalf("expected pod terminated once on exit report, got %d", ad.TerminateCount())
	}
}

func TestExitReportNonzeroRecordsFailed(t *testing.T) {
	ctx := context.Background()
	orch, ad := runningProvisionableRun(t, ctx)

	if err := orch.RecordReport(ctx, "ws_1", "run_1", "exit", nil, intPtr(2)); err != nil {
		t.Fatalf("record report: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Outcome != domain.RunOutcomeFailed {
		t.Fatalf("expected failed, got %q", record.Outcome)
	}
	if ad.TerminateCount() != 1 {
		t.Fatalf("expected terminate once, got %d", ad.TerminateCount())
	}
}

func TestProgressReportDoesNotFinalize(t *testing.T) {
	ctx := context.Background()
	orch, ad := runningProvisionableRun(t, ctx)

	if err := orch.RecordReport(ctx, "ws_1", "run_1", "progress", []byte(`{"pct":50}`), nil); err != nil {
		t.Fatalf("record report: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Closed {
		t.Fatalf("a progress report must not close the run")
	}
	if ad.TerminateCount() != 0 {
		t.Fatalf("a progress report must not terminate, got %d", ad.TerminateCount())
	}
}

func TestExitReportAfterRunClosedIsNoop(t *testing.T) {
	ctx := context.Background()
	// Drive a run to terminal via the normal succeeded path first.
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchProvisionableOffer("off_prov", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}
	beforeTerm := ad.TerminateCount()

	// A late exit report must not double-finalize or double-terminate.
	if err := orch.RecordReport(ctx, "ws_1", "run_1", "exit", nil, intPtr(0)); err != nil {
		t.Fatalf("late report: %v", err)
	}
	if ad.TerminateCount() != beforeTerm {
		t.Fatalf("late report must not terminate again: before=%d after=%d", beforeTerm, ad.TerminateCount())
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run after late report: %v", err)
	}
	if record.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("late report must not change already-recorded outcome: got %q, want %q", record.Outcome, domain.RunOutcomeSucceeded)
	}
}
