package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUIServesEmbeddedAssets(t *testing.T) {
	handler := newHTTPTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("UI root expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Mercator") || !strings.Contains(rec.Body.String(), `id="app"`) {
		t.Fatalf("UI root missing app shell: %s", rec.Body.String())
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "secret value") {
		t.Fatalf("UI shell should not contain secret value copy: %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/ui/app.js", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "fetchJSON") {
		t.Fatalf("UI app asset expected 200 app JS, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUIBackedReadAPIs(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, createRunBody{RunID: "run_ui", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_ui")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create run expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	for _, target := range []string{
		"/v1/runs?workspace_id=ws_1",
		"/v1/runs/run_ui/events?workspace_id=ws_1",
		"/v1/runs/run_ui/decision?workspace_id=ws_1",
		"/v1/connections?workspace_id=ws_1",
		"/v1/offers?workspace_id=ws_1",
	} {
		req = httptest.NewRequest(http.MethodGet, target, nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d body=%s", target, rec.Code, rec.Body.String())
		}
	}
}
