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

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/adapter/fake"
	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
	"github.com/bengarcia/mercator/internal/ociresolver"
	"github.com/bengarcia/mercator/internal/orchestrator"
	"github.com/bengarcia/mercator/internal/scheduler"
	"github.com/bengarcia/mercator/internal/secrets"
	"github.com/bengarcia/mercator/internal/workload"
)

func TestCreateRunRequiresIdempotencyKey(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, createRunBody{RunID: "run_1", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateRunAndListEvents(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, createRunBody{RunID: "run_1", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_create")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var created runResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.Run.ID != "run_1" {
		t.Fatalf("unexpected create response: %+v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_1/events?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var listed eventListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(listed.Events) == 0 || listed.Events[0].Type != orchestrator.EventRunRequested {
		t.Fatalf("unexpected events response: %+v", listed)
	}
}

func TestCreateRunDrivesFakeAdapterFastPath(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, createRunBody{RunID: "run_fast", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_fast")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_fast/events?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var listed eventListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if !hasEventType(listed.Events, orchestrator.EventLaunchIntentRecorded) || !hasEventType(listed.Events, orchestrator.EventRunClosed) {
		t.Fatalf("create run should drive fake fast path through closure, got %+v", listed.Events)
	}
}

func TestRunReadListWaitDecisionAndRefreshEndpoints(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, createRunBody{RunID: "run_read", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_read")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	for _, target := range []string{
		"/v1/runs/run_read?workspace_id=ws_1",
		"/v1/runs?workspace_id=ws_1",
		"/v1/runs/run_read:wait?workspace_id=ws_1",
		"/v1/runs/run_read/decision?workspace_id=ws_1",
	} {
		req = httptest.NewRequest(http.MethodGet, target, nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d body=%s", target, rec.Code, rec.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/runs/run_read:refresh?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunEndpointsRequireExplicitWorkspace(t *testing.T) {
	handler := newHTTPTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/runs/run_1/events", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected explicit workspace 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBearerAuthEnforcesWorkspaceScope(t *testing.T) {
	handler := newHTTPTestServerWithOptions(t, WithBearerAuth("test-token", []string{"ws_1"}))

	req := httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id=ws_1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id=ws_1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorized workspace expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id=ws_2", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden workspace expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateRunIdempotencyConflictReturns409(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, createRunBody{RunID: "run_conflict", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_conflict")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected first 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	// A logically different create (different image) reusing the same
	// idempotency key must conflict. The revision ID is cosmetic and excluded
	// from the hash, so a substantive field is changed here.
	other := httpRevision()
	other.Spec.Containers[0].Image = "ghcr.io/acme/inference@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	body = mustMarshal(t, createRunBody{RunID: "run_conflict", Workload: other})
	req = httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_conflict")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatalf("expected machine-readable conflict, got %s", rec.Body.String())
	}
}

func TestRunEventsRedactEnvironmentBindings(t *testing.T) {
	handler := newHTTPTestServer(t)
	literal := "literal-api-token-that-must-not-leak"
	rev := httpRevision()
	secretName := "provider-secret-ref-that-must-not-leak"
	rev.Spec.Containers[0].Env = map[string]domain.EnvBinding{
		"LOG_LEVEL": {Value: ptr("debug")},
		"API_TOKEN": {SecretRef: &domain.SecretReference{
			Name:    secretName,
			Version: 1,
		}},
		"SECRET_LITERAL": {Value: &literal},
	}
	createSecretVersionForHTTPTest(t, handler, secretName, "idem_redacted_secret")
	grantSecretForHTTPTest(t, handler, secretName, 1, "run_redacted_events")
	body := mustMarshal(t, createRunBody{RunID: "run_redacted_events", Workload: rev})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_redacted_events")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_redacted_events/events?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	for _, forbidden := range []string{literal, secretName, `"value":"debug"`} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("events response exposed %q in %s", forbidden, rec.Body.String())
		}
	}
}

func TestCreateRunRejectsSecretRefWithoutGrant(t *testing.T) {
	handler := newHTTPTestServer(t)
	rev := httpRevision()
	rev.Spec.Containers[0].Env = map[string]domain.EnvBinding{
		"API_TOKEN": {SecretRef: &domain.SecretReference{Name: "sec_api", Version: 1}},
	}
	body := mustMarshal(t, createRunBody{RunID: "run_without_secret_grant", Workload: rev})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_no_secret_grant")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "SECRET_GRANT_REQUIRED") {
		t.Fatalf("expected missing grant 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateRunValidationErrorDoesNotEchoEnvironmentValues(t *testing.T) {
	handler := newHTTPTestServer(t)
	rev := httpRevision()
	secret := "literal-secret-that-must-not-echo"
	rev.Spec.Containers[0].Env = map[string]domain.EnvBinding{
		"bad-name": {Value: &secret},
	}
	body := mustMarshal(t, createRunBody{RunID: "run_invalid_env", Workload: rev})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_invalid_env")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("validation error echoed env value: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ENV_NAME_INVALID") {
		t.Fatalf("validation error should include stable code, got %s", rec.Body.String())
	}
}

func TestRefreshRejectsStoredSecretRefWithoutGrant(t *testing.T) {
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	sched := scheduler.New()
	orch := orchestrator.New(log, sched, ad)
	handler := NewWithServices(orch, sched, ad, workload.New(log), secrets.New(log, []byte("01234567890123456789012345678901")), ociresolver.NewStaticResolver(nil))
	rev := httpRevision()
	rev.Spec.Containers[0].Env = map[string]domain.EnvBinding{
		"API_TOKEN": {SecretRef: &domain.SecretReference{Name: "sec_api", Version: 1}},
	}
	if _, err := orch.CreateRun(context.Background(), orchestrator.CreateRunRequest{WorkspaceID: "ws_1", RunID: "run_refresh_secret", IdempotencyKey: "idem_seed_refresh_secret", Workload: rev}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/runs/run_refresh_secret:refresh?workspace_id=ws_1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "SECRET_GRANT_REQUIRED") {
		t.Fatalf("expected refresh missing grant 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func ptr(value string) *string {
	return &value
}

func createSecretVersionForHTTPTest(t *testing.T, handler http.Handler, secretID, idempotencyKey string) {
	t.Helper()
	body := mustMarshal(t, createSecretVersionBody{WorkspaceID: "ws_1", SecretID: secretID, Value: "plaintext-secret"})
	req := httptest.NewRequest(http.MethodPost, "/v1/secrets/"+secretID+"/versions", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", idempotencyKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create secret %s expected 202, got %d body=%s", secretID, rec.Code, rec.Body.String())
	}
}

func grantSecretForHTTPTest(t *testing.T, handler http.Handler, secretID string, version int, runID string) {
	t.Helper()
	body := mustMarshal(t, grantSecretBody{WorkspaceID: "ws_1", SecretID: secretID, Version: version, ScopeType: "run", ScopeID: runID})
	req := httptest.NewRequest(http.MethodPost, "/v1/secrets/"+secretID+"/grants", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("grant secret %s expected 202, got %d body=%s", secretID, rec.Code, rec.Body.String())
	}
}

func TestCreateRunRejectsWorkspaceMismatch(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, createRunBody{WorkspaceID: "ws_other", RunID: "run_workspace_mismatch", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_workspace_mismatch")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "WORKSPACE_MISMATCH") {
		t.Fatalf("workspace mismatch should include stable code, got %s", rec.Body.String())
	}
}

func TestPlacementPreviewAndOpenAPI(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, placementPreviewBody{RunID: "run_preview", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/placements:preview", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var preview placementPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if preview.Decision.SelectedOfferSnapshotID == "" || len(preview.Decision.Candidates) != 1 {
		t.Fatalf("unexpected preview decision: %+v", preview.Decision)
	}

	for _, path := range []string{"/openapi.json", "/health/live", "/health/ready"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d body=%s", path, rec.Code, rec.Body.String())
		}
		if path == "/openapi.json" {
			for _, required := range []string{"/v1/runs/{run_id}", "/v1/runs/{run_id}/decision", "/v1/runs/{run_id}/events", "/v1/workloads", "/v1/images:resolve", "/v1/secrets", "PlacementPreviewRequest", "PlacementPreviewResponse", "IdempotencyConflict", `"409"`, "exit_code"} {
				if !strings.Contains(rec.Body.String(), required) {
					t.Fatalf("OpenAPI missing %s: %s", required, rec.Body.String())
				}
			}
		}
	}
}

func TestOpenAPIDocumentIsValidJSON(t *testing.T) {
	if !json.Valid([]byte(OpenAPIJSON)) {
		t.Fatalf("OpenAPIJSON is not valid JSON")
	}
}

func TestPlacementPreviewValidatesWorkload(t *testing.T) {
	handler := newHTTPTestServer(t)
	rev := httpRevision()
	rev.Spec.Containers = nil
	body := mustMarshal(t, placementPreviewBody{RunID: "run_preview_invalid", Workload: rev})
	req := httptest.NewRequest(http.MethodPost, "/v1/placements:preview", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "V1_ONE_CONTAINER") {
		t.Fatalf("expected validation code, got %s", rec.Body.String())
	}
}

func TestWorkloadRevisionAndImageResolverEndpoints(t *testing.T) {
	handler := newHTTPTestServer(t)
	createBody := mustMarshal(t, createWorkloadBody{WorkspaceID: "ws_1", WorkloadID: "wrk_1", Name: "trainer"})
	req := httptest.NewRequest(http.MethodPost, "/v1/workloads", bytes.NewReader(createBody))
	req.Header.Set("Idempotency-Key", "idem_workload")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create workload expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	rev := httpRevision()
	body := mustMarshal(t, createRevisionBody{Revision: rev})
	req = httptest.NewRequest(http.MethodPost, "/v1/workloads/wrk_1/revisions?workspace_id=ws_1", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_revision")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create revision expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	for _, target := range []string{
		"/v1/workloads/wrk_1/revisions?workspace_id=ws_1",
		"/v1/workloads/wrk_1/revisions/wrev_1?workspace_id=ws_1",
	} {
		req = httptest.NewRequest(http.MethodGet, target, nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d body=%s", target, rec.Code, rec.Body.String())
		}
	}

	body = mustMarshal(t, resolveImageBody{Image: "ghcr.io/acme/trainer:latest", Platform: "linux/amd64"})
	req = httptest.NewRequest(http.MethodPost, "/v1/images:resolve", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "@sha256:") {
		t.Fatalf("resolve expected digest response, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateRunCanReferenceStoredWorkloadRevision(t *testing.T) {
	handler := newHTTPTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/workloads", bytes.NewReader(mustMarshal(t, createWorkloadBody{WorkspaceID: "ws_1", WorkloadID: "wrk_1", Name: "trainer"})))
	req.Header.Set("Idempotency-Key", "idem_workload_ref")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create workload expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/workloads/wrk_1/revisions?workspace_id=ws_1", bytes.NewReader(mustMarshal(t, createRevisionBody{Revision: httpRevision()})))
	req.Header.Set("Idempotency-Key", "idem_revision_ref")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create revision expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	body := mustMarshal(t, createRunBody{WorkspaceID: "ws_1", RunID: "run_from_revision", WorkloadID: "wrk_1", WorkloadRevisionID: "wrev_1"})
	req = httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_run_from_revision")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("create run from revision expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSecretMetadataAndGrantEndpointsDoNotExposePlaintext(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, createSecretVersionBody{WorkspaceID: "ws_1", SecretID: "sec_api", Value: "plaintext-secret"})
	req := httptest.NewRequest(http.MethodPost, "/v1/secrets/sec_api/versions", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create secret version expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "plaintext-secret") {
		t.Fatalf("secret create leaked plaintext: %s", rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/secrets/sec_api/versions", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("secret version idempotent replay expected version 1, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/secrets?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "plaintext-secret") {
		t.Fatalf("metadata expected 200 without plaintext, got %d body=%s", rec.Code, rec.Body.String())
	}

	runBody := mustMarshal(t, createRunBody{RunID: "run_1", Workload: httpRevision()})
	req = httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(runBody))
	req.Header.Set("Idempotency-Key", "idem_secret_grant_run")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create grant target run expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	grantBody := mustMarshal(t, grantSecretBody{WorkspaceID: "ws_1", SecretID: "sec_api", Version: 1, ScopeType: "run", ScopeID: "run_1"})
	req = httptest.NewRequest(http.MethodPost, "/v1/secrets/sec_api/grants", bytes.NewReader(grantBody))
	req.Header.Set("Idempotency-Key", "idem_grant")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("grant expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSecretEndpointsRejectPathBodyMismatch(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, createSecretVersionBody{WorkspaceID: "ws_1", SecretID: "sec_body", Value: "plaintext-secret"})
	req := httptest.NewRequest(http.MethodPost, "/v1/secrets/sec_path/versions", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_secret_mismatch")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "SECRET_ID_MISMATCH") {
		t.Fatalf("secret mismatch expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}

	grantBody := mustMarshal(t, grantSecretBody{WorkspaceID: "ws_1", SecretID: "sec_body", Version: 1, ScopeType: "run", ScopeID: "run_1"})
	req = httptest.NewRequest(http.MethodPost, "/v1/secrets/sec_path/grants", bytes.NewReader(grantBody))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "SECRET_ID_MISMATCH") {
		t.Fatalf("grant mismatch expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func newHTTPTestServer(t *testing.T) http.Handler {
	t.Helper()
	return newHTTPTestServerWithOptions(t)
}

func newHTTPTestServerWithOptions(t *testing.T, options ...Option) http.Handler {
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
		fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_1", now)}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	sched := scheduler.New()
	orch := orchestrator.New(log, sched, ad)
	resolver := ociresolver.NewStaticResolver(map[string]ociresolver.ResolvedImage{
		"ghcr.io/acme/trainer:latest|linux/amd64": {
			Image:    "ghcr.io/acme/trainer@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Digest:   "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Platform: "linux/amd64",
		},
	})
	return NewWithServices(orch, sched, ad, workload.New(log), secrets.New(log, []byte("01234567890123456789012345678901")), resolver, options...)
}

func httpRevision() domain.WorkloadRevision {
	return domain.WorkloadRevision{
		ID:          "wrev_1",
		WorkspaceID: "ws_1",
		WorkloadID:  "wrk_1",
		Digest:      "sha256:revision",
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "ghcr.io/acme/inference@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
			}},
			Resources: domain.ResourceRequirements{
				CPU:           domain.CPURequirement{MinMillis: 1000},
				Memory:        domain.MemoryRequirement{MinBytes: 1 << 30},
				EphemeralDisk: domain.DiskRequirement{MinBytes: 1 << 30},
			},
			Network:   domain.NetworkRequirements{Inbound: domain.InboundNetworkNone},
			Placement: domain.PlacementPolicy{Objective: domain.ObjectiveBalanced, ExpectedRuntimeSeconds: 60},
			Execution: domain.ExecutionPolicy{MaxRuntimeSeconds: 120, MaxPreStartAttempts: 3},
		},
	}
}

func httpOffer(id string, now time.Time) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:           id,
		ConnectionID: "conn_1",
		AdapterType:  "fake",
		Kind:         domain.OfferKindStanding,
		ObservedAt:   now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Minute),
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          2000,
			MemoryBytes:        2 << 30,
			EphemeralDiskBytes: 2 << 30,
		},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: 32768},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone, Protocols: []string{"tcp"}},
			Pricing:   domain.PricingCapabilities{Known: true},
			Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "deterministic_name", ListOwned: true},
			Secrets:   domain.SecretDeliveryCapabilities{Delivery: "direct_env", CleanupSupported: true},
		},
		Pricing:  domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.0001, Known: true},
		Queue:    &domain.QueueSnapshot{},
		Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
		ImageCache: domain.ImageCacheEvidence{
			ManifestCached: true,
			MissingBytes:   0,
			Known:          true,
		},
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func hasEventType(events []eventlog.CloudEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
