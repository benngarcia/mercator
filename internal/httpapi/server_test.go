package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/workload"
)

func TestCreateRunRequiresIdempotencyKey(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_1", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateRunAndListEvents(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_1", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_create")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var created RunResponse
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
	var listed EventListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(listed.Events) == 0 || listed.Events[0].Type != orchestrator.EventRunRequested {
		t.Fatalf("unexpected events response: %+v", listed)
	}
}

func TestCreateRunDrivesFakeAdapterFastPath(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_fast", Workload: httpRevision()})
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
	var listed EventListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if !hasEventType(listed.Events, orchestrator.EventLaunchIntentRecorded) || !hasEventType(listed.Events, orchestrator.EventRunClosed) {
		t.Fatalf("create run should drive fake fast path through closure, got %+v", listed.Events)
	}
}

func TestCreateRunReturnsAcceptedTerminalRunAfterLaunchFailure(t *testing.T) {
	provider := newLaunchErrorAdapter(errors.New("provider rejected launch"))
	handler := newHTTPTestServerForAdapter(t, provider)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_launch_failed", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_launch_failed")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		getReq := httptest.NewRequest(http.MethodGet, "/v1/runs/run_launch_failed?workspace_id=ws_1", nil)
		getRec := httptest.NewRecorder()
		handler.ServeHTTP(getRec, getReq)
		t.Fatalf("expected 202, got %d body=%s; follow-up GET got %d body=%s", rec.Code, rec.Body.String(), getRec.Code, getRec.Body.String())
	}
	var accepted RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if accepted.RunId != "run_launch_failed" || accepted.Run.ID != accepted.RunId {
		t.Fatalf("unexpected accepted run: %+v", accepted)
	}
	if !accepted.Run.Closed || accepted.Run.Outcome != domain.RunOutcomeFailed {
		t.Fatalf("expected terminal failed run, got %+v", accepted.Run)
	}

	replayReq := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	replayReq.Header.Set("Idempotency-Key", "idem_launch_failed")
	replayRec := httptest.NewRecorder()
	handler.ServeHTTP(replayRec, replayReq)
	if replayRec.Code != http.StatusAccepted {
		t.Fatalf("replay expected 202, got %d body=%s", replayRec.Code, replayRec.Body.String())
	}
	var replayed RunResponse
	if err := json.Unmarshal(replayRec.Body.Bytes(), &replayed); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if !replayed.Duplicate || replayed.RunId != accepted.RunId {
		t.Fatalf("unexpected replay response: %+v", replayed)
	}
	if provider.launchCalls != 1 {
		t.Fatalf("idempotent replay repeated provider launch: calls=%d", provider.launchCalls)
	}
}

func TestCreateRunReturnsAcceptedOpenRunAfterIndeterminateLaunch(t *testing.T) {
	provider := newLaunchErrorAdapter(adapter.ErrLaunchIndeterminate)
	handler := newHTTPTestServerForAdapter(t, provider)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_launch_indeterminate", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_launch_indeterminate")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var accepted RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if accepted.RunId != "run_launch_indeterminate" || accepted.Run.ID != accepted.RunId {
		t.Fatalf("unexpected accepted run: %+v", accepted)
	}
	if accepted.Run.Closed || accepted.Run.Phase != "running" {
		t.Fatalf("expected open reconciling run, got %+v", accepted.Run)
	}
	eventsReq := httptest.NewRequest(http.MethodGet, "/v1/runs/run_launch_indeterminate/events?workspace_id=ws_1", nil)
	eventsRec := httptest.NewRecorder()
	handler.ServeHTTP(eventsRec, eventsReq)
	if eventsRec.Code != http.StatusOK {
		t.Fatalf("events expected 200, got %d body=%s", eventsRec.Code, eventsRec.Body.String())
	}
	var events EventListResponse
	if err := json.Unmarshal(eventsRec.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if !hasEventType(events.Events, orchestrator.EventLaunchIndeterminate) {
		t.Fatalf("accepted run lost reconciliation marker: %+v", events.Events)
	}
}

func TestCreateRunRejectsInvalidWorkloadBeforeAcceptance(t *testing.T) {
	provider := newLaunchErrorAdapter(errors.New("provider must not be called"))
	handler := newHTTPTestServerForAdapter(t, provider)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_invalid", Workload: domain.WorkloadRevision{WorkspaceID: "ws_1"}})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_invalid")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	getReq := httptest.NewRequest(http.MethodGet, "/v1/runs/run_invalid?workspace_id=ws_1", nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("invalid run was durably accepted: GET got %d body=%s", getRec.Code, getRec.Body.String())
	}
	if provider.launchCalls != 0 {
		t.Fatalf("pre-acceptance validation reached provider launch: calls=%d", provider.launchCalls)
	}
}

func TestCreateRunReturnsInternalErrorWhenInitialPersistenceFails(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenSQLite(ctx, "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	provider := fake.New(fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_1", time.Now().UTC())}))
	handler := New(Deps{
		Orchestrator: orchestrator.New(workspaceTestLog{EventLog: log}, scheduler.New(), provider),
		Offers:       singleProviderOffers{provider: provider},
		Workloads:    workload.New(workspaceTestLog{EventLog: log}),
	})
	if err := log.Close(); err != nil {
		t.Fatalf("close event log: %v", err)
	}
	body := mustMarshal(t, CreateRunRequest{RunId: "run_persistence_failed", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_persistence_failed")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunReadListWaitDecisionAndRefreshEndpoints(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_read", Workload: httpRevision()})
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
		"/v1/runs/run_read/wait?workspace_id=ws_1",
		"/v1/runs/run_read/decision?workspace_id=ws_1",
	} {
		req = httptest.NewRequest(http.MethodGet, target, nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d body=%s", target, rec.Code, rec.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/runs/run_read/refresh?workspace_id=ws_1", nil)
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

func TestBearerAuthProtectsEveryExplicitWorkspace(t *testing.T) {
	handler := newHTTPTestServerWithOptions(t, WithBearerAuth("test-token"))

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
	if rec.Code != http.StatusOK {
		t.Fatalf("second explicit workspace expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateRunIdempotencyConflictReturns409(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_conflict", Workload: httpRevision()})
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
	body = mustMarshal(t, CreateRunRequest{RunId: "run_conflict", Workload: other})
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
	rev.Spec.Containers[0].Env = map[string]domain.EnvBinding{
		"LOG_LEVEL":      {Value: ptr("debug")},
		"SECRET_LITERAL": {Value: &literal},
	}
	body := mustMarshal(t, CreateRunRequest{RunId: "run_redacted_events", Workload: rev})
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
	for _, forbidden := range []string{literal, `"value":"debug"`} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("events response exposed %q in %s", forbidden, rec.Body.String())
		}
	}
}

func TestCreateRunRejectsSecretRefEnvironmentBinding(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := []byte(`{
		"run_id":"run_secret_ref_rejected",
		"workload":{
			"id":"wrev_1",
			"workspace_id":"ws_1",
			"workload_id":"wrk_1",
			"digest":"sha256:revision",
			"spec":{
				"containers":[{
					"name":"main",
					"image":"ghcr.io/acme/inference@sha256:0000000000000000000000000000000000000000000000000000000000000000",
					"platform":{"os":"linux","architecture":"amd64"},
					"env":{"API_TOKEN":{"secret_ref":{"name":"sec_api","version":1}}}
				}],
				"resources":{"cpu":{"min_millis":1000},"memory":{"min_bytes":1073741824},"ephemeral_disk":{"min_bytes":1073741824}},
				"network":{"inbound":"none"},
				"placement":{"objective":"balanced","expected_runtime_seconds":60},
				"execution":{"max_runtime_seconds":120,"max_pre_start_attempts":3}
			}
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_secret_ref_rejected")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ENV_VALUE_REQUIRED") {
		t.Fatalf("expected secret_ref binding to be rejected as invalid env input, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateRunValidationErrorDoesNotEchoEnvironmentValues(t *testing.T) {
	handler := newHTTPTestServer(t)
	rev := httpRevision()
	secret := "literal-secret-that-must-not-echo"
	rev.Spec.Containers[0].Env = map[string]domain.EnvBinding{
		"bad-name": {Value: &secret},
	}
	body := mustMarshal(t, CreateRunRequest{RunId: "run_invalid_env", Workload: rev})
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

func ptr(value string) *string {
	return &value
}

func TestCreateRunRejectsWorkspaceMismatch(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, CreateRunRequest{WorkspaceId: "ws_other", RunId: "run_workspace_mismatch", Workload: httpRevision()})
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
	body := mustMarshal(t, PlacementPreviewRequest{RunId: "run_preview", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/placements:preview", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var preview PlacementPreviewResponse
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
			for _, required := range []string{"/v1/runs/{run_id}", "/v1/runs/{run_id}/decision", "/v1/runs/{run_id}/events", "/v1/workloads", "/v1/images:resolve", "PlacementPreviewRequest", "PlacementPreviewResponse", "IdempotencyConflict", `"409"`, "exit_code"} {
				if !strings.Contains(rec.Body.String(), required) {
					t.Fatalf("OpenAPI missing %s: %s", required, rec.Body.String())
				}
			}
			if strings.Contains(rec.Body.String(), "/v1/secrets") || strings.Contains(rec.Body.String(), "secret_ref") {
				t.Fatalf("OpenAPI still exposes secret vault surface: %s", rec.Body.String())
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
	body := mustMarshal(t, PlacementPreviewRequest{RunId: "run_preview_invalid", Workload: rev})
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
	createBody := mustMarshal(t, CreateWorkloadRequest{WorkspaceId: "ws_1", WorkloadId: "wrk_1", Name: "trainer"})
	req := httptest.NewRequest(http.MethodPost, "/v1/workloads", bytes.NewReader(createBody))
	req.Header.Set("Idempotency-Key", "idem_workload")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create workload expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	rev := httpRevision()
	body := mustMarshal(t, CreateRevisionRequest{Revision: rev})
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

	body = mustMarshal(t, ResolveImageRequest{Image: "ghcr.io/acme/trainer:latest", Platform: "linux/amd64"})
	req = httptest.NewRequest(http.MethodPost, "/v1/images:resolve", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "@sha256:") {
		t.Fatalf("resolve expected digest response, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateWorkloadRequiresWorkspaceInTheGeneratedBody(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, CreateWorkloadRequest{WorkloadId: "wrk_missing_workspace", Name: "trainer"})
	request := httptest.NewRequest(http.MethodPost, "/v1/workloads?workspace_id=ws_1", bytes.NewReader(body))
	request.Header.Set("Idempotency-Key", "idem_missing_workspace")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "WORKSPACE_ID_REQUIRED") {
		t.Fatalf("expected WORKSPACE_ID_REQUIRED, got %s", response.Body.String())
	}
}

func TestCreateRunCanReferenceStoredWorkloadRevision(t *testing.T) {
	handler := newHTTPTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/workloads", bytes.NewReader(mustMarshal(t, CreateWorkloadRequest{WorkspaceId: "ws_1", WorkloadId: "wrk_1", Name: "trainer"})))
	req.Header.Set("Idempotency-Key", "idem_workload_ref")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create workload expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/workloads/wrk_1/revisions?workspace_id=ws_1", bytes.NewReader(mustMarshal(t, CreateRevisionRequest{Revision: httpRevision()})))
	req.Header.Set("Idempotency-Key", "idem_revision_ref")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create revision expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	body := mustMarshal(t, CreateRunRequest{WorkspaceId: "ws_1", RunId: "run_from_revision", WorkloadId: "wrk_1", WorkloadRevisionId: "wrev_1"})
	req = httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_run_from_revision")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("create run from revision expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSecretVaultRoutesAreNotRegistered(t *testing.T) {
	handler := newHTTPTestServer(t)
	for _, target := range []string{"/v1/secrets?workspace_id=ws_1", "/v1/secrets/sec_path/versions", "/v1/secrets/sec_path/grants"} {
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{}`))
		if strings.Contains(target, "?") {
			req = httptest.NewRequest(http.MethodGet, target, nil)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s expected unavailable secret vault route, got %d body=%s", target, rec.Code, rec.Body.String())
		}
	}
}

func TestCreateRunEnvOverridesStoredWorkloadRevision(t *testing.T) {
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	now := time.Now().UTC()
	ad := &captureHTTPLaunchAdapter{Adapter: fake.New(
		fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_1", now)}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)}
	sched := scheduler.New()
	orch := orchestrator.New(workspaceTestLog{EventLog: log}, sched, ad)
	handler := New(Deps{Orchestrator: orch, Offers: singleProviderOffers{provider: ad}, Workloads: workload.New(workspaceTestLog{EventLog: log}), Resolver: ociresolver.NewStaticResolver(nil)})

	req := httptest.NewRequest(http.MethodPost, "/v1/workloads", bytes.NewReader(mustMarshal(t, CreateWorkloadRequest{WorkspaceId: "ws_1", WorkloadId: "wrk_env", Name: "env"})))
	req.Header.Set("Idempotency-Key", "idem_workload_env")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create workload expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	rev := httpRevision()
	rev.WorkloadID = "wrk_env"
	rev.Spec.Containers[0].Env = map[string]domain.EnvBinding{
		"LOG_LEVEL": {Value: ptr("info")},
		"KEEP":      {Value: ptr("base")},
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/workloads/wrk_env/revisions?workspace_id=ws_1", bytes.NewReader(mustMarshal(t, CreateRevisionRequest{Revision: rev})))
	req.Header.Set("Idempotency-Key", "idem_revision_env")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create revision expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	runEnv := map[string]domain.EnvBinding{
		"LOG_LEVEL": {Value: ptr("debug")},
		"RUN_INPUT": {Value: ptr("s3://bucket/object")},
	}
	body := mustMarshal(t, CreateRunRequest{WorkspaceId: "ws_1", RunId: "run_env_override", WorkloadId: "wrk_env", WorkloadRevisionId: "wrev_1", Env: runEnv})
	req = httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_run_env_override")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create run expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	if got := mustLaunchEnvValue(t, ad.launchRequest.Environment, "LOG_LEVEL"); got != "debug" {
		t.Fatalf("LOG_LEVEL = %q, want override debug", got)
	}
	if got := mustLaunchEnvValue(t, ad.launchRequest.Environment, "KEEP"); got != "base" {
		t.Fatalf("KEEP = %q, want base", got)
	}
	if got := mustLaunchEnvValue(t, ad.launchRequest.Environment, "RUN_INPUT"); got != "s3://bucket/object" {
		t.Fatalf("RUN_INPUT = %q, want run override addition", got)
	}
}

type captureHTTPLaunchAdapter struct {
	*fake.Adapter
	launchRequest adapter.LaunchRequest
}

func (c *captureHTTPLaunchAdapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	c.launchRequest = req
	return c.Adapter.Launch(ctx, req)
}

func mustLaunchEnvValue(t *testing.T, bindings []adapter.EnvironmentBinding, name string) string {
	t.Helper()
	for _, binding := range bindings {
		if binding.Name == name {
			if binding.Value == nil {
				t.Fatalf("%s binding has nil value: %+v", name, binding)
			}
			return *binding.Value
		}
	}
	t.Fatalf("environment binding %s not found in %+v", name, bindings)
	return ""
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
	orch := orchestrator.New(workspaceTestLog{EventLog: log}, sched, ad)
	resolver := ociresolver.NewStaticResolver(map[string]ociresolver.ResolvedImage{
		"ghcr.io/acme/trainer:latest|linux/amd64": {
			Image:    "ghcr.io/acme/trainer@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Digest:   "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Platform: "linux/amd64",
		},
	})
	return New(Deps{Orchestrator: orch, Offers: singleProviderOffers{provider: ad}, Workloads: workload.New(workspaceTestLog{EventLog: log}), Resolver: resolver}, options...)
}

func newHTTPTestServerForAdapter(t *testing.T, provider adapter.Provider) http.Handler {
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
	return New(Deps{
		Orchestrator: orchestrator.New(workspaceTestLog{EventLog: log}, scheduler.New(), provider),
		Offers:       singleProviderOffers{provider: provider},
		Workloads:    workload.New(workspaceTestLog{EventLog: log}),
	})
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
		RentalID:     id,
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

type launchErrorAdapter struct {
	*fake.Adapter
	err         error
	launchCalls int
}

func newLaunchErrorAdapter(err error) *launchErrorAdapter {
	return &launchErrorAdapter{
		Adapter: fake.New(fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_1", time.Now().UTC())})),
		err:     err,
	}
}

func (a *launchErrorAdapter) Launch(context.Context, adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	a.launchCalls++
	return adapter.LaunchReceipt{}, a.err
}
