package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/reporting"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/workload"
)

// TestReportEndpointExemptFromOperatorAuth verifies that POST /v1/runs/<id>/report
// is NOT subject to the operator-token gate (it will be authed by a per-run token
// in a later task). Every other /v1/ path is unchanged.
func TestReportEndpointExemptFromOperatorAuth(t *testing.T) {
	handler := newHTTPTestServerWithOptions(t, WithBearerAuth("tok"))

	// POST /v1/runs/run_x/report without any Authorization header must NOT be
	// rejected by the operator gate. Until the handler is registered it will
	// 404 from the mux — that is fine; we only assert it was not the operator 401.
	t.Run("report_not_rejected_by_operator_gate", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/runs/run_x/report", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized && strings.Contains(rec.Body.String(), "UNAUTHORIZED") {
			t.Fatalf("operator gate must not reject POST /report; got 401 UNAUTHORIZED: %s", rec.Body.String())
		}
	})

	// Regression: GET /v1/runs without a token must still be 401.
	t.Run("list_runs_still_requires_token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id=ws_1", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET /v1/runs without token: expected 401, got %d body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "UNAUTHORIZED") {
			t.Fatalf("GET /v1/runs without token: expected UNAUTHORIZED body, got %s", rec.Body.String())
		}
	})

	// Regression: POST /v1/runs/run_x/cancel without a token must still be 401.
	// The carve-out must NOT accidentally exempt /cancel or any other action.
	t.Run("cancel_still_requires_token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/runs/run_x/cancel", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("POST /cancel without token: expected 401, got %d body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "UNAUTHORIZED") {
			t.Fatalf("POST /cancel without token: expected UNAUTHORIZED body, got %s", rec.Body.String())
		}
	})
}

// newReportingTestServer builds a server wired with a real orchestrator and
// event log, plus a WithReportSigner and optional operator bearer auth.
type reportingTestHarness struct {
	handler http.Handler
	orch    *orchestrator.Orchestrator
	adapter *fake.Adapter
}

func newReportingTestHarness(t *testing.T, signerKey []byte, extra ...Option) reportingTestHarness {
	t.Helper()
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Fatalf("close event log: %v", err)
		}
	})
	now := time.Now().UTC()
	offer := httpOffer("off_rep", now)
	offer.Kind = domain.OfferKindProvisionable
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{offer}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	)
	return newReportingTestHarnessWithProvider(t, signerKey, log, ad, ad, extra...)
}

func newReportingTestHarnessWithProvider(t *testing.T, signerKey []byte, log eventlog.EventLog, provider adapter.Provider, ad *fake.Adapter, extra ...Option) reportingTestHarness {
	t.Helper()
	sched := scheduler.New()
	workspaceLog := workspaceTestLog{EventLog: log}
	orch := orchestrator.New(workspaceLog, sched, provider)
	signer := reporting.NewSigner(signerKey)
	opts := append([]Option{
		WithReportSigner(signer),
		WithBearerAuth("op-token"),
	}, extra...)
	return reportingTestHarness{
		handler: New(Deps{Orchestrator: orch, Offers: singleProviderOffers{provider: provider}, Workloads: workload.New(workspaceLog)}, opts...),
		orch:    orch,
		adapter: ad,
	}
}

func newReportingTestServer(t *testing.T, signerKey []byte, extra ...Option) http.Handler {
	t.Helper()
	return newReportingTestHarness(t, signerKey, extra...).handler
}

func reportFixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "reports", name))
	if err != nil {
		t.Fatalf("read report fixture %q: %v", name, err)
	}
	return payload
}

// createReportingRun creates a run via POST /v1/runs and returns its run_id.
// The server must be wired with operator bearer auth "op-token".
func createReportingRun(t *testing.T, handler http.Handler, runID string) string {
	t.Helper()
	body := mustMarshal(t, CreateRunRequest{RunId: runID, Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_report_"+runID)
	req.Header.Set("Authorization", "Bearer op-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create run: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create run response: %v", err)
	}
	return resp.Run.ID
}

// TestReportIngestEndpointRecordsEvent is the main TDD test for the report endpoint.
func TestReportIngestEndpointRecordsEvent(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	signer := reporting.NewSigner(key32)
	handler := newReportingTestServer(t, key32)

	// Create a run so its stream exists.
	runID := createReportingRun(t, handler, "run_report_e2e")

	// POST /report with the correct run token — should 202.
	reportPayload := mustMarshal(t, map[string]any{
		"type": "progress",
		"data": map[string]any{"pct": 50},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/report?workspace_id=ws_1", bytes.NewReader(reportPayload))
	req.Header.Set("Authorization", "Bearer "+signer.Token("ws_1", runID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("report: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var reportResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &reportResp); err != nil {
		t.Fatalf("decode report response: %v", err)
	}
	if reportResp["recorded"] != true {
		t.Fatalf("expected {recorded:true}, got %+v", reportResp)
	}

	// GET /v1/runs/{id}/events with the operator token should show the reported event.
	req = httptest.NewRequest(http.MethodGet, "/v1/runs/"+runID+"/events?workspace_id=ws_1", nil)
	req.Header.Set("Authorization", "Bearer op-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get events: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var listed EventListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	var found bool
	for _, ev := range listed.Events {
		if ev.Type == orchestrator.EventRunReported {
			found = true
			// The data must contain the type and payload.
			if !strings.Contains(string(ev.Data), "progress") {
				t.Fatalf("reported event data missing type: %s", string(ev.Data))
			}
			if !strings.Contains(string(ev.Data), "50") {
				t.Fatalf("reported event data missing pct payload: %s", string(ev.Data))
			}
		}
	}
	if !found {
		t.Fatalf("expected a %s event, got events: %+v", orchestrator.EventRunReported, listed.Events)
	}
}

func TestReportIngestRejectsContradictoryReportShapes(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	signer := reporting.NewSigner(key32)
	harness := newReportingTestHarness(t, key32)
	runID := createReportingRun(t, harness.handler, "run_report_shape")

	for _, fixture := range []string{"progress_with_exit_code.json", "exit_without_code.json"} {
		t.Run(fixture, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/report?workspace_id=ws_1", bytes.NewReader(reportFixture(t, fixture)))
			req.Header.Set("Authorization", "Bearer "+signer.Token("ws_1", runID))
			rec := httptest.NewRecorder()
			harness.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_REPORT") {
				t.Fatalf("report: expected 400 INVALID_REPORT, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}

	events, err := harness.orch.GetRunEvents(t.Context(), "ws_1", runID)
	if err != nil {
		t.Fatalf("get run events: %v", err)
	}
	for _, event := range events {
		if event.Type == orchestrator.EventRunReported {
			t.Fatalf("invalid report appended event: %+v", event)
		}
	}
}

func TestTerminalReportReturnsBeforeCleanupAndReconciles(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	signer := reporting.NewSigner(key32)
	harness := newReportingTestHarness(t, key32)
	runID := createReportingRun(t, harness.handler, "run_report_terminal_success")

	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/report?workspace_id=ws_1", bytes.NewReader(reportFixture(t, "exit_succeeded.json")))
	req.Header.Set("Authorization", "Bearer "+signer.Token("ws_1", runID))
	rec := httptest.NewRecorder()
	harness.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("report: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	reported, err := harness.orch.GetRun(t.Context(), "ws_1", runID)
	if err != nil {
		t.Fatalf("get reported run: %v", err)
	}
	if reported.Closed || harness.adapter.TerminateCount() != 0 {
		t.Fatalf("report response must precede cleanup: closed=%v terminate_count=%d", reported.Closed, harness.adapter.TerminateCount())
	}

	if _, err := harness.orch.AdvanceOpenRuns(t.Context(), "ws_1"); err != nil {
		t.Fatalf("reconcile reported run: %v", err)
	}
	closed, err := harness.orch.GetRun(t.Context(), "ws_1", runID)
	if err != nil {
		t.Fatalf("get reconciled run: %v", err)
	}
	if !closed.Closed || closed.Outcome != domain.RunOutcomeSucceeded || closed.Cleanup != domain.CleanupConfirmed {
		t.Fatalf("reconciled run = %+v, want closed succeeded with confirmed cleanup", closed)
	}
	if harness.adapter.TerminateCount() != 1 {
		t.Fatalf("terminate count = %d, want 1", harness.adapter.TerminateCount())
	}
}

func TestNonzeroTerminalReportFailsAndCleansUp(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	signer := reporting.NewSigner(key32)
	harness := newReportingTestHarness(t, key32)
	runID := createReportingRun(t, harness.handler, "run_report_terminal_failure")

	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/report?workspace_id=ws_1", bytes.NewReader(reportFixture(t, "exit_failed_7.json")))
	req.Header.Set("Authorization", "Bearer "+signer.Token("ws_1", runID))
	rec := httptest.NewRecorder()
	harness.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("report: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	if _, err := harness.orch.AdvanceOpenRuns(t.Context(), "ws_1"); err != nil {
		t.Fatalf("reconcile failed report: %v", err)
	}
	record, err := harness.orch.GetRun(t.Context(), "ws_1", runID)
	if err != nil {
		t.Fatalf("get failed run: %v", err)
	}
	if !record.Closed || record.Outcome != domain.RunOutcomeFailed || record.ExitCode == nil || *record.ExitCode != 7 || record.Cleanup != domain.CleanupConfirmed {
		t.Fatalf("failed run = %+v", record)
	}
	if harness.adapter.TerminateCount() != 1 {
		t.Fatalf("terminate count = %d, want 1", harness.adapter.TerminateCount())
	}
}

func TestCancelEndpointUsesRecordedDispositionCleanupOnce(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	harness := newReportingTestHarness(t, key32)
	runID := createReportingRun(t, harness.handler, "run_cancel_terminal")

	for attempt := 1; attempt <= 2; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/cancel?workspace_id=ws_1", nil)
		req.Header.Set("Authorization", "Bearer op-token")
		rec := httptest.NewRecorder()
		harness.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("cancel %d: expected 200, got %d body=%s", attempt, rec.Code, rec.Body.String())
		}
		var response RunResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode cancel %d: %v", attempt, err)
		}
		if !response.Run.Closed || response.Run.Outcome != domain.RunOutcomeCancelled || response.Run.Cleanup != domain.CleanupConfirmed {
			t.Fatalf("cancel %d run = %+v", attempt, response.Run)
		}
	}
	if harness.adapter.TerminateCount() != 1 {
		t.Fatalf("terminate count = %d, want 1", harness.adapter.TerminateCount())
	}
	events, err := harness.orch.GetRunEvents(t.Context(), "ws_1", runID)
	if err != nil {
		t.Fatalf("get cancellation events: %v", err)
	}
	for _, event := range events {
		if event.Type == orchestrator.EventCancelAccepted {
			t.Fatalf("new cancellation emitted historical cancel_accepted event")
		}
	}
}

func TestTerminalReportReplayIsIdempotentAndConflictIsRejected(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	signer := reporting.NewSigner(key32)
	harness := newReportingTestHarness(t, key32)
	runID := createReportingRun(t, harness.handler, "run_report_terminal_replay")

	postReport := func(name string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/report?workspace_id=ws_1", bytes.NewReader(reportFixture(t, name)))
		req.Header.Set("Authorization", "Bearer "+signer.Token("ws_1", runID))
		rec := httptest.NewRecorder()
		harness.handler.ServeHTTP(rec, req)
		return rec
	}

	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	var reports sync.WaitGroup
	for range 2 {
		reports.Add(1)
		go func() {
			defer reports.Done()
			<-start
			responses <- postReport("exit_succeeded.json")
		}()
	}
	close(start)
	reports.Wait()
	close(responses)
	for rec := range responses {
		if rec.Code != http.StatusAccepted {
			t.Fatalf("concurrent identical report: expected 202, got %d body=%s", rec.Code, rec.Body.String())
		}
	}
	conflict := postReport("exit_failed_7.json")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "TERMINAL_REPORT_CONFLICT") {
		t.Fatalf("conflicting report: expected 409 TERMINAL_REPORT_CONFLICT, got %d body=%s", conflict.Code, conflict.Body.String())
	}

	events, err := harness.orch.GetRunEvents(t.Context(), "ws_1", runID)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	reported := 0
	for _, event := range events {
		if event.Type == orchestrator.EventRunReported {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("reported event count = %d, want 1", reported)
	}

	if _, err := harness.orch.AdvanceOpenRuns(t.Context(), "ws_1"); err != nil {
		t.Fatalf("reconcile reported run: %v", err)
	}
	record, err := harness.orch.GetRun(t.Context(), "ws_1", runID)
	if err != nil {
		t.Fatalf("get reconciled run: %v", err)
	}
	if record.Outcome != domain.RunOutcomeSucceeded || record.ExitCode == nil || *record.ExitCode != 0 {
		t.Fatalf("reconciled run = %+v, want original successful terminal report", record)
	}
}

func TestCleanupFailureIsVisibleThroughRunAndEventAPIs(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	signer := reporting.NewSigner(key32)
	log, err := eventlog.OpenSQLite(t.Context(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	offer := httpOffer("off_cleanup_failure", time.Now().UTC())
	offer.Kind = domain.OfferKindProvisionable
	base := fake.New(fake.WithOffers([]domain.OfferSnapshot{offer}), fake.WithLaunchOutcome(adapter.ExternalPhaseRunning))
	provider := &httpTerminateFailsOnceProvider{Provider: base}
	harness := newReportingTestHarnessWithProvider(t, key32, log, provider, base)
	runID := createReportingRun(t, harness.handler, "run_report_cleanup_failure")

	report := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/report?workspace_id=ws_1", bytes.NewReader(reportFixture(t, "exit_succeeded.json")))
	report.Header.Set("Authorization", "Bearer "+signer.Token("ws_1", runID))
	reported := httptest.NewRecorder()
	harness.handler.ServeHTTP(reported, report)
	if reported.Code != http.StatusAccepted {
		t.Fatalf("report: expected 202, got %d body=%s", reported.Code, reported.Body.String())
	}

	refresh := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/refresh?workspace_id=ws_1", nil)
		req.Header.Set("Authorization", "Bearer op-token")
		rec := httptest.NewRecorder()
		harness.handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := refresh(); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "REFRESH_RUN_FAILED") {
		t.Fatalf("failed refresh: expected 502 REFRESH_RUN_FAILED, got %d body=%s", rec.Code, rec.Body.String())
	}

	getRun := httptest.NewRequest(http.MethodGet, "/v1/runs/"+runID+"?workspace_id=ws_1", nil)
	getRun.Header.Set("Authorization", "Bearer op-token")
	runResponse := httptest.NewRecorder()
	harness.handler.ServeHTTP(runResponse, getRun)
	if runResponse.Code != http.StatusOK {
		t.Fatalf("get blocked run: expected 200, got %d body=%s", runResponse.Code, runResponse.Body.String())
	}
	var blocked RunResponse
	if err := json.Unmarshal(runResponse.Body.Bytes(), &blocked); err != nil {
		t.Fatalf("decode blocked run: %v", err)
	}
	if blocked.Run.Closed || blocked.Run.Outcome != domain.RunOutcomeSucceeded || blocked.Run.Cleanup != domain.CleanupBlocked || blocked.Run.CleanupError == nil {
		t.Fatalf("blocked run = %+v", blocked.Run)
	}
	if blocked.Run.CleanupError.Code != "ADAPTER_RETRYABLE_FAILURE" || strings.Contains(blocked.Run.CleanupError.Message, "provider secret") {
		t.Fatalf("cleanup error is not stable and redacted: %+v", blocked.Run.CleanupError)
	}

	getEvents := httptest.NewRequest(http.MethodGet, "/v1/runs/"+runID+"/events?workspace_id=ws_1", nil)
	getEvents.Header.Set("Authorization", "Bearer op-token")
	eventResponse := httptest.NewRecorder()
	harness.handler.ServeHTTP(eventResponse, getEvents)
	if eventResponse.Code != http.StatusOK || !strings.Contains(eventResponse.Body.String(), orchestrator.EventCleanupFailed) || strings.Contains(eventResponse.Body.String(), "provider secret") {
		t.Fatalf("cleanup failure events: status=%d body=%s", eventResponse.Code, eventResponse.Body.String())
	}

	if rec := refresh(); rec.Code != http.StatusOK {
		t.Fatalf("successful refresh: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if provider.calls != 2 || base.TerminateCount() != 1 {
		t.Fatalf("terminate calls: wrapper=%d provider=%d", provider.calls, base.TerminateCount())
	}
}

// TestReportIngestEndpointWrongToken verifies that a token valid for a DIFFERENT run
// id is rejected with 401.
func TestReportIngestEndpointWrongToken(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	signer := reporting.NewSigner(key32)
	handler := newReportingTestServer(t, key32)

	runID := createReportingRun(t, handler, "run_report_wrong_tok")

	otherRunToken := signer.Token("ws_1", "run_completely_different")
	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/report?workspace_id=ws_1",
		bytes.NewReader([]byte(`{"type":"progress"}`)))
	req.Header.Set("Authorization", "Bearer "+otherRunToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "INVALID_RUN_TOKEN") {
		t.Fatalf("expected INVALID_RUN_TOKEN, got %s", rec.Body.String())
	}
}

// TestReportIngestEndpointMissingWorkspaceID verifies that omitting workspace_id
// returns 400 WORKSPACE_REQUIRED.
func TestReportIngestEndpointMissingWorkspaceID(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	signer := reporting.NewSigner(key32)
	handler := newReportingTestServer(t, key32)

	runID := createReportingRun(t, handler, "run_report_no_ws")

	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/report",
		bytes.NewReader([]byte(`{"type":"progress"}`)))
	req.Header.Set("Authorization", "Bearer "+signer.Token("ws_1", runID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing workspace: expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("expected INVALID_REQUEST, got %s", rec.Body.String())
	}
}

// TestReportIngestEndpointDisabledWithoutSigner verifies that a server without
// WithReportSigner returns 501 REPORTING_DISABLED.
func TestReportIngestEndpointDisabledWithoutSigner(t *testing.T) {
	// Build a server WITHOUT WithReportSigner.
	handler := newHTTPTestServerWithOptions(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/runs/run_x/report?workspace_id=ws_1",
		bytes.NewReader([]byte(`{"type":"progress"}`)))
	req.Header.Set("Authorization", "Bearer sometoken")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("no signer: expected 501, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "REPORTING_DISABLED") {
		t.Fatalf("expected REPORTING_DISABLED, got %s", rec.Body.String())
	}
}

// TestReportEndpointRejectsOperatorToken verifies that POST /v1/runs/{id}/report
// rejects the operator token and requires a valid RUN token instead.
func TestReportEndpointRejectsOperatorToken(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	handler := newReportingTestServer(t, key32)

	runID := createReportingRun(t, handler, "run_report_op_token_reject")

	// POST /report with the operator token (not the run token) — should 401.
	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+"/report?workspace_id=ws_1",
		bytes.NewReader([]byte(`{"type":"progress"}`)))
	req.Header.Set("Authorization", "Bearer op-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("operator token: expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "INVALID_RUN_TOKEN") {
		t.Fatalf("expected INVALID_RUN_TOKEN, got %s", rec.Body.String())
	}
}

// TestReportEndpointRunNotFound verifies that POSTing /report for a
// non-existent run (with valid token and workspace_id) returns 404 RUN_NOT_FOUND.
func TestReportEndpointRunNotFound(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	signer := reporting.NewSigner(key32)
	handler := newReportingTestServer(t, key32)

	// Don't create a run; just try to report for a non-existent runID.
	nonExistentRunID := "run_nonexistent_12345"

	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+nonExistentRunID+"/report?workspace_id=ws_1",
		bytes.NewReader([]byte(`{"type":"progress"}`)))
	req.Header.Set("Authorization", "Bearer "+signer.Token("ws_1", nonExistentRunID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("report non-existent run: expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "RUN_NOT_FOUND") {
		t.Fatalf("expected RUN_NOT_FOUND, got %s", rec.Body.String())
	}
}

type httpTerminateFailsOnceProvider struct {
	adapter.Provider
	calls int
}

func (p *httpTerminateFailsOnceProvider) Terminate(ctx context.Context, req adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	p.calls++
	if p.calls == 1 {
		return adapter.TerminateReceipt{}, errors.Join(adapter.ErrRetryableFailure, errors.New("provider secret"))
	}
	return p.Provider.Terminate(ctx, req)
}
