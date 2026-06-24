package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestReportEndpointExemptFromOperatorAuth verifies that POST /v1/runs/<id>:report
// is NOT subject to the operator-token gate (it will be authed by a per-run token
// in a later task). Every other /v1/ path is unchanged.
func TestReportEndpointExemptFromOperatorAuth(t *testing.T) {
	handler := newHTTPTestServerWithOptions(t, WithBearerAuth("tok", []string{"*"}))

	// POST /v1/runs/run_x:report without any Authorization header must NOT be
	// rejected by the operator gate. Until the handler is registered it will
	// 404 from the mux — that is fine; we only assert it was not the operator 401.
	t.Run("report_not_rejected_by_operator_gate", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/runs/run_x:report", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized && strings.Contains(rec.Body.String(), "UNAUTHORIZED") {
			t.Fatalf("operator gate must not reject POST :report; got 401 UNAUTHORIZED: %s", rec.Body.String())
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

	// Regression: POST /v1/runs/run_x:cancel without a token must still be 401.
	// The carve-out must NOT accidentally exempt :cancel or any other action.
	t.Run("cancel_still_requires_token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/runs/run_x:cancel", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("POST :cancel without token: expected 401, got %d body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "UNAUTHORIZED") {
			t.Fatalf("POST :cancel without token: expected UNAUTHORIZED body, got %s", rec.Body.String())
		}
	})
}

// newReportingTestServer builds a server wired with a real orchestrator and
// event log, plus a WithReportSigner and optional operator bearer auth.
func newReportingTestServer(t *testing.T, signerKey []byte, extra ...Option) http.Handler {
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
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_rep", now)}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	)
	sched := scheduler.New()
	orch := orchestrator.New(log, sched, ad)
	signer := reporting.NewSigner(signerKey)
	opts := append([]Option{
		WithReportSigner(signer),
		WithBearerAuth("op-token", []string{"*"}),
	}, extra...)
	return NewWithServices(orch, sched, ad, workload.New(log), nil, opts...)
}

// createReportingRun creates a run via POST /v1/runs and returns its run_id.
// The server must be wired with operator bearer auth "op-token".
func createReportingRun(t *testing.T, handler http.Handler, runID string) string {
	t.Helper()
	body := mustMarshal(t, createRunBody{RunID: runID, Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_report_"+runID)
	req.Header.Set("Authorization", "Bearer op-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create run: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp runResponse
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

	// POST :report with the correct run token — should 202.
	reportPayload := mustMarshal(t, map[string]any{
		"type": "progress",
		"data": map[string]any{"pct": 50},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+":report?workspace_id=ws_1", bytes.NewReader(reportPayload))
	req.Header.Set("Authorization", "Bearer "+signer.Token(runID))
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
	var listed eventListResponse
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

// TestReportIngestEndpointWrongToken verifies that a token valid for a DIFFERENT run
// id is rejected with 401.
func TestReportIngestEndpointWrongToken(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	signer := reporting.NewSigner(key32)
	handler := newReportingTestServer(t, key32)

	runID := createReportingRun(t, handler, "run_report_wrong_tok")

	otherRunToken := signer.Token("run_completely_different")
	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+":report?workspace_id=ws_1",
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

	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+":report",
		bytes.NewReader([]byte(`{"type":"progress"}`)))
	req.Header.Set("Authorization", "Bearer "+signer.Token(runID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing workspace: expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "WORKSPACE_REQUIRED") {
		t.Fatalf("expected WORKSPACE_REQUIRED, got %s", rec.Body.String())
	}
}

// TestReportIngestEndpointDisabledWithoutSigner verifies that a server without
// WithReportSigner returns 501 REPORTING_DISABLED.
func TestReportIngestEndpointDisabledWithoutSigner(t *testing.T) {
	// Build a server WITHOUT WithReportSigner.
	handler := newHTTPTestServerWithOptions(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/runs/run_x:report?workspace_id=ws_1",
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

// TestReportEndpointRejectsOperatorToken verifies that POST /v1/runs/{id}:report
// rejects the operator token and requires a valid RUN token instead.
func TestReportEndpointRejectsOperatorToken(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	handler := newReportingTestServer(t, key32)

	runID := createReportingRun(t, handler, "run_report_op_token_reject")

	// POST :report with the operator token (not the run token) — should 401.
	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+runID+":report?workspace_id=ws_1",
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

// TestReportEndpointRunNotFound verifies that POSTing :report for a
// non-existent run (with valid token and workspace_id) returns 404 RUN_NOT_FOUND.
func TestReportEndpointRunNotFound(t *testing.T) {
	key32 := []byte("0123456789abcdef0123456789abcdef")
	signer := reporting.NewSigner(key32)
	handler := newReportingTestServer(t, key32)

	// Don't create a run; just try to report for a non-existent runID.
	nonExistentRunID := "run_nonexistent_12345"

	req := httptest.NewRequest(http.MethodPost, "/v1/runs/"+nonExistentRunID+":report?workspace_id=ws_1",
		bytes.NewReader([]byte(`{"type":"progress"}`)))
	req.Header.Set("Authorization", "Bearer "+signer.Token(nonExistentRunID))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("report non-existent run: expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "RUN_NOT_FOUND") {
		t.Fatalf("expected RUN_NOT_FOUND, got %s", rec.Body.String())
	}
}
