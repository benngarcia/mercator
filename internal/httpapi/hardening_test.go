package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// Item 7: create returns the unified {run_id, run:{...}, links} envelope —
// a convenience top-level run_id AND the full run record, with room for a
// metadata object. run_id must equal run.id.
func TestCreateRunReturnsUnifiedEnvelope(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_env", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_env")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var bare map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &bare); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := bare["run"]; !ok {
		t.Fatalf("create response missing top-level run object: %s", rec.Body.String())
	}
	if _, ok := bare["links"]; !ok {
		t.Fatalf("create response missing links: %s", rec.Body.String())
	}
	// The convenience top-level run_id must be present alongside run{}.
	if _, ok := bare["run_id"]; !ok {
		t.Fatalf("create response missing convenience top-level run_id: %s", rec.Body.String())
	}
	var resp RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if resp.Run.ID != "run_env" {
		t.Fatalf("unexpected run id: %+v", resp.Run)
	}
	if resp.RunId != resp.Run.ID {
		t.Fatalf("top-level run_id %q must equal run.id %q", resp.RunId, resp.Run.ID)
	}
}

// Item 1 (HTTP): exit_code present on the create envelope and on GET.
func TestCreateAndGetRunExposeExitCode(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_exitcode", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_exitcode")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var created RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Run.ExitCode == nil || *created.Run.ExitCode != 0 {
		t.Fatalf("create envelope missing exit_code=0, got %+v", created.Run)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_exitcode?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var got RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.Run.ExitCode == nil || *got.Run.ExitCode != 0 {
		t.Fatalf("GET run missing exit_code=0, got %+v", got.Run)
	}
	if got.Run.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("expected succeeded outcome, got %q", got.Run.Outcome)
	}
}

// Item 3: no public event payload may expose a PascalCase key.
func TestPublicEventPayloadsAreSnakeCase(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_casing", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_casing")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_casing/events?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var listed EventListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(listed.Events) == 0 {
		t.Fatalf("expected public events")
	}
	for _, event := range listed.Events {
		if len(event.Data) == 0 {
			continue
		}
		var payload any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatalf("event %s data not JSON: %v", event.Type, err)
		}
		if bad := findPascalCaseKey(payload); bad != "" {
			t.Fatalf("public event %s exposes PascalCase key %q in data: %s", event.Type, bad, string(event.Data))
		}
	}
}

// findPascalCaseKey walks an arbitrary decoded JSON value and returns the first
// object key whose first letter is uppercase (an indication that a Go struct
// leaked through without snake_case json tags). Empty string means clean.
func findPascalCaseKey(value any) string {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if len(key) > 0 && key[0] >= 'A' && key[0] <= 'Z' {
				return key
			}
			if bad := findPascalCaseKey(child); bad != "" {
				return bad
			}
		}
	case []any:
		for _, child := range v {
			if bad := findPascalCaseKey(child); bad != "" {
				return bad
			}
		}
	}
	return ""
}

// Item 5: waitRun long-polls a run that stays open past one internal poll
// window all the way to a terminal state.
func TestWaitRunDrivesOpenRunToTerminal(t *testing.T) {
	prevInterval := waitPollInterval
	prevDeadline := waitDeadline
	waitPollInterval = time.Millisecond
	waitDeadline = 5 * time.Second
	t.Cleanup(func() {
		waitPollInterval = prevInterval
		waitDeadline = prevDeadline
	})

	handler := newHTTPTestServerWithOpenObservations(t, 3)
	body := mustMarshal(t, CreateRunRequest{RunId: "run_wait_open", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_wait_open")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	// After create+advance the run must still be open (stayed past first poll).
	var created RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Run.Closed {
		t.Fatalf("precondition: run should be open after first advance, got %+v", created.Run)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_wait_open/wait?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("wait should reach terminal with 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var waited RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &waited); err != nil {
		t.Fatalf("decode wait: %v", err)
	}
	if !waited.Run.Closed {
		t.Fatalf("wait should return a closed run, got %+v", waited.Run)
	}
	if waited.Run.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("expected succeeded outcome after wait, got %q", waited.Run.Outcome)
	}
	if waited.Run.ExitCode == nil || *waited.Run.ExitCode != 0 {
		t.Fatalf("expected exit_code 0 after wait, got %+v", waited.Run.ExitCode)
	}
}

// Item 6: workspace-scoped requests always name their durable partition;
// authentication never supplies a default workspace.
func TestAuthenticatedRequestsRequireExplicitWorkspaceID(t *testing.T) {
	handler := newHTTPTestServerWithOptions(t, WithBearerAuth("test-token"))

	// Create without any workspace_id (not in query, body, or nested workload).
	rev := httpRevision()
	rev.WorkspaceID = ""
	body := mustMarshal(t, CreateRunRequest{RunId: "run_default_ws", Workload: rev})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_default_ws")
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create without workspace expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}

	// GET without workspace_id should resolve to the same single workspace.
	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_default_ws", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("get without workspace expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func newHTTPTestServerWithOpenObservations(t *testing.T, openObserves int) http.Handler {
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
		fake.WithOpenObservations(openObserves),
	)
	sched := scheduler.New()
	orch := orchestrator.New(log, sched, ad)
	resolver := ociresolver.NewStaticResolver(nil)
	return New(Deps{Orchestrator: orch, Offers: singleProviderOffers{provider: ad}, Workloads: workload.New(log), Resolver: resolver})
}

func TestOversizedRequestBodyIsRejected(t *testing.T) {
	handler := newHTTPTestServer(t)
	// Slightly over the 1 MiB server-wide body cap.
	huge := bytes.Repeat([]byte("x"), maxRequestBodyBytes+1)
	body := []byte(`{"image":"` + string(huge) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_huge")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized body, got %d body=%s", rec.Code, rec.Body.String())
	}
}
