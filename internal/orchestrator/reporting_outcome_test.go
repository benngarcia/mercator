package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

	if err := orch.RecordReport(ctx, "ws_1", "run_1", mustRunReport(t, "exit", nil, intPtr(0))); err != nil {
		t.Fatalf("record report: %v", err)
	}
	if _, err := orch.AdvanceOpenRuns(ctx, "ws_1"); err != nil {
		t.Fatalf("reconcile report: %v", err)
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

	if err := orch.RecordReport(ctx, "ws_1", "run_1", mustRunReport(t, "exit", nil, intPtr(2))); err != nil {
		t.Fatalf("record report: %v", err)
	}
	if _, err := orch.AdvanceOpenRuns(ctx, "ws_1"); err != nil {
		t.Fatalf("reconcile report: %v", err)
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

	if err := orch.RecordReport(ctx, "ws_1", "run_1", mustRunReport(t, "progress", []byte(`{"pct":50}`), nil)); err != nil {
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

func TestFirstTerminalFactDeterminesOutcome(t *testing.T) {
	ctx := context.Background()
	orch, ad := runningProvisionableRun(t, ctx)

	if err := orch.RecordReport(ctx, "ws_1", "run_1", mustRunReport(t, "exit", nil, intPtr(0))); err != nil {
		t.Fatalf("record report: %v", err)
	}
	record, err := orch.CancelRun(ctx, "ws_1", "run_1", nil)
	if err != nil {
		t.Fatalf("cancel after terminal report: %v", err)
	}

	if !record.Closed || record.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("run = %+v, want first terminal report to preserve successful outcome", record)
	}
	if ad.TerminateCount() != 1 {
		t.Fatalf("terminate count = %d, want 1", ad.TerminateCount())
	}
}

func TestCleanupFailureIsDurableBlockedEvidenceAndRefreshRetries(t *testing.T) {
	ctx := context.Background()
	base := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchProvisionableOffer("off_cleanup_failure", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	)
	ad := &terminateFailsOnceAdapter{Adapter: base}
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance to running: %v", err)
	}
	if err := orch.RecordReport(ctx, "ws_1", "run_1", mustRunReport(t, "exit", nil, intPtr(0))); err != nil {
		t.Fatalf("record report: %v", err)
	}

	if _, err := orch.AdvanceOpenRuns(ctx, "ws_1"); !errors.Is(err, adapter.ErrRetryableFailure) {
		t.Fatalf("reconcile error = %v, want retryable cleanup failure", err)
	}
	blocked, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get blocked run: %v", err)
	}
	if blocked.Closed || blocked.Outcome != domain.RunOutcomeSucceeded || blocked.Cleanup != domain.CleanupBlocked {
		t.Fatalf("blocked run = %+v, want open succeeded run with blocked cleanup", blocked)
	}
	encoded, err := json.Marshal(blocked)
	if err != nil {
		t.Fatalf("marshal blocked run: %v", err)
	}
	for _, expected := range []string{`"cleanup_error"`, `"ADAPTER_RETRYABLE_FAILURE"`, `"terminate"`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("blocked run JSON %s does not contain %s", encoded, expected)
		}
	}

	events, err := orch.GetRunEvents(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get cleanup events: %v", err)
	}
	failures := 0
	for _, event := range events {
		if event.Type != "compute.run.cleanup_failed.v1" {
			continue
		}
		failures++
		if strings.Contains(string(event.Data), "provider secret") || !strings.Contains(string(event.Data), "ADAPTER_RETRYABLE_FAILURE") {
			t.Fatalf("cleanup failure is not stable and redacted: %s", event.Data)
		}
	}
	if failures != 1 {
		t.Fatalf("cleanup failure event count = %d, want 1", failures)
	}

	closed, err := orch.RefreshRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("refresh blocked cleanup: %v", err)
	}
	if !closed.Closed || closed.Cleanup != domain.CleanupConfirmed || closed.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("refreshed run = %+v, want closed succeeded run", closed)
	}
	if ad.terminateCalls != 2 || base.TerminateCount() != 1 {
		t.Fatalf("terminate calls: wrapper=%d provider=%d, want one failed attempt and one successful delete", ad.terminateCalls, base.TerminateCount())
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
	if err := orch.RecordReport(ctx, "ws_1", "run_1", mustRunReport(t, "exit", nil, intPtr(0))); err != nil {
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

func mustRunReport(t *testing.T, reportType string, data json.RawMessage, exitCode *int) RunReport {
	t.Helper()
	report, err := NewRunReport(reportType, data, exitCode)
	if err != nil {
		t.Fatalf("construct run report: %v", err)
	}
	return report
}

type terminateFailsOnceAdapter struct {
	*fake.Adapter
	terminateCalls int
}

func (a *terminateFailsOnceAdapter) Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	a.terminateCalls++
	if a.terminateCalls == 1 {
		return adapter.TerminateReceipt{}, errors.Join(adapter.ErrRetryableFailure, errors.New("provider secret"))
	}
	return a.Adapter.Terminate(ctx, req)
}
