package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubWebAuth authenticates any request carrying the X-Test-Session header as
// that header's value, standing in for the real cookie-verifying webauth.
type stubWebAuth struct{}

func (stubWebAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/auth/session" {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": true})
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (stubWebAuth) SessionEmail(r *http.Request) (string, bool) {
	email := r.Header.Get("X-Test-Session")
	return email, email != ""
}

// VerifyCLIToken accepts tokens of the form "cli:<email>".
func (stubWebAuth) VerifyCLIToken(token string) (string, bool) {
	email, ok := strings.CutPrefix(token, "cli:")
	return email, ok && email != ""
}

func TestSessionGrantsAPIAccessAlongsideBearer(t *testing.T) {
	handler := newHTTPTestServerWithOptions(t,
		WithBearerAuth("secret-token", []string{"ws_1"}), WithWebAuth(stubWebAuth{}))

	unauthenticated := httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id=ws_1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, unauthenticated)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials expected 401, got %d", rec.Code)
	}

	viaSession := httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id=ws_1", nil)
	viaSession.Header.Set("X-Test-Session", "operator@example.com")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, viaSession)
	if rec.Code != http.StatusOK {
		t.Fatalf("session expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	viaBearer := httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id=ws_1", nil)
	viaBearer.Header.Set("Authorization", "Bearer secret-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, viaBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCLITokenAuthenticatesAsItsEmail(t *testing.T) {
	handler := newHTTPTestServerWithOptions(t,
		WithBearerAuth("secret-token", []string{"ws_1"}), WithWebAuth(stubWebAuth{}))

	req := httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id=ws_1", nil)
	req.Header.Set("Authorization", "Bearer cli:operator@example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("CLI token expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	body := mustMarshal(t, CreateRunRequest{RunId: "run_cli_audit", Workload: httpRevision()})
	create := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	create.Header.Set("Idempotency-Key", "idem_cli_audit")
	create.Header.Set("Authorization", "Bearer cli:operator@example.com")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, create)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var created RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Run.CreatedBy != "operator@example.com" {
		t.Fatalf("CLI-token create should record the email, got %q", created.Run.CreatedBy)
	}
}

func TestWrongBearerDoesNotDowngradeToSession(t *testing.T) {
	handler := newHTTPTestServerWithOptions(t,
		WithBearerAuth("secret-token", []string{"ws_1"}), WithWebAuth(stubWebAuth{}))

	req := httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id=ws_1", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	req.Header.Set("X-Test-Session", "operator@example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong bearer token must fail even with a valid session, got %d", rec.Code)
	}
}

func TestUnauthenticatedConsoleLoadRedirectsToLogin(t *testing.T) {
	handler := newHTTPTestServerWithOptions(t,
		WithBearerAuth("secret-token", []string{"ws_1"}), WithWebAuth(stubWebAuth{}))

	req := httptest.NewRequest(http.MethodGet, "/runs?workspace_id=ws_1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("unauthenticated console load expected 302, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/auth/login?next=%2Fruns%3Fworkspace_id%3Dws_1" {
		t.Fatalf("redirect should carry the deep link, got %q", got)
	}

	authed := httptest.NewRequest(http.MethodGet, "/runs", nil)
	authed.Header.Set("X-Test-Session", "operator@example.com")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, authed)
	if rec.Code == http.StatusFound {
		t.Fatalf("signed-in console load must not redirect")
	}
}

func TestSessionEndpointReportsDisabledWithoutWebAuth(t *testing.T) {
	handler := newHTTPTestServer(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/session", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Enabled {
		t.Fatalf("without OIDC, /auth/session must report enabled=false: %s err=%v", rec.Body.String(), err)
	}
}

func TestRunRecordsActingPrincipals(t *testing.T) {
	handler := newHTTPTestServerWithOptions(t,
		WithBearerAuth("secret-token", []string{"ws_1"}), WithWebAuth(stubWebAuth{}))

	// A machine-token create records "bearer".
	body := mustMarshal(t, CreateRunRequest{RunId: "run_audit", Workload: httpRevision()})
	create := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	create.Header.Set("Idempotency-Key", "idem_audit")
	create.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, create)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var created RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Run.CreatedBy != "bearer" {
		t.Fatalf("machine create should record created_by=bearer, got %q", created.Run.CreatedBy)
	}

	// A signed-in human's cancel records their email.
	cancel := httptest.NewRequest(http.MethodPost, "/v1/runs/run_audit/cancel?workspace_id=ws_1", nil)
	cancel.Header.Set("X-Test-Session", "operator@example.com")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, cancel)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var cancelled RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &cancelled); err != nil {
		t.Fatalf("decode cancel: %v", err)
	}
	if cancelled.Run.CreatedBy != "bearer" {
		t.Fatalf("created_by should survive cancel, got %q", cancelled.Run.CreatedBy)
	}
	// The fake adapter succeeds runs immediately, so the cancel may arrive after
	// close and record nothing — accept either the email or empty, never
	// another principal.
	if cancelled.Run.CancelledBy != "" && cancelled.Run.CancelledBy != "operator@example.com" {
		t.Fatalf("cancelled_by should be the signed-in human, got %q", cancelled.Run.CancelledBy)
	}
}

func TestPublicRunEventsDoNotLeakActorEmails(t *testing.T) {
	handler := newHTTPTestServerWithOptions(t,
		WithBearerAuth("secret-token", []string{"ws_1"}), WithWebAuth(stubWebAuth{}))

	body := mustMarshal(t, CreateRunRequest{RunId: "run_actor_leak", Workload: httpRevision()})
	create := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	create.Header.Set("Idempotency-Key", "idem_actor_leak")
	create.Header.Set("X-Test-Session", "operator@example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, create)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	events := httptest.NewRequest(http.MethodGet, "/v1/runs/run_actor_leak/events?workspace_id=ws_1", nil)
	events.Header.Set("Authorization", "Bearer secret-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, events)
	if rec.Code != http.StatusOK {
		t.Fatalf("events expected 200, got %d", rec.Code)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("operator@example.com")) {
		t.Fatalf("public run events must not embed the acting principal's email: %s", rec.Body.String())
	}
}
