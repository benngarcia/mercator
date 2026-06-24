package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
