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

func TestRemovedWorkspaceSelectorsFailInsteadOfWideningTheRequest(t *testing.T) {
	handler := newHTTPTestServer(t)

	query := httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id=retired", nil)
	queryRecorder := httptest.NewRecorder()
	handler.ServeHTTP(queryRecorder, query)
	if queryRecorder.Code != http.StatusBadRequest || !bytes.Contains(queryRecorder.Body.Bytes(), []byte("REMOVED_WORKSPACE_SELECTOR")) {
		t.Fatalf("legacy query selector response = %d %s", queryRecorder.Code, queryRecorder.Body.String())
	}

	payload := map[string]any{
		"run_id":       "run_must_not_exist",
		"workspace_id": "retired",
		"workload":     httpRevision(),
	}
	body := mustMarshal(t, payload)
	command := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	command.Header.Set("Content-Type", "application/json")
	command.Header.Set("Idempotency-Key", "removed-workspace-selector")
	commandRecorder := httptest.NewRecorder()
	handler.ServeHTTP(commandRecorder, command)
	if commandRecorder.Code != http.StatusBadRequest || !bytes.Contains(commandRecorder.Body.Bytes(), []byte("REMOVED_WORKSPACE_SELECTOR")) {
		t.Fatalf("legacy body selector response = %d %s", commandRecorder.Code, commandRecorder.Body.String())
	}

	read := httptest.NewRequest(http.MethodGet, "/v1/runs/run_must_not_exist", nil)
	readRecorder := httptest.NewRecorder()
	handler.ServeHTTP(readRecorder, read)
	if readRecorder.Code != http.StatusNotFound {
		t.Fatalf("rejected command still created a Run: %d %s", readRecorder.Code, readRecorder.Body.String())
	}

	commandWithoutContentType := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	commandWithoutContentType.Header.Set("Idempotency-Key", "removed-workspace-selector-no-content-type")
	commandWithoutContentTypeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(commandWithoutContentTypeRecorder, commandWithoutContentType)
	if commandWithoutContentTypeRecorder.Code != http.StatusBadRequest || !bytes.Contains(commandWithoutContentTypeRecorder.Body.Bytes(), []byte("REMOVED_WORKSPACE_SELECTOR")) {
		t.Fatalf("legacy body selector without content type = %d %s", commandWithoutContentTypeRecorder.Code, commandWithoutContentTypeRecorder.Body.String())
	}
}

func TestApplicationWorkspaceMetadataRemainsOpaque(t *testing.T) {
	handler := newHTTPTestServer(t)
	revision := httpRevision()
	revision.Spec.Metadata = map[string]string{"workspace_id": "application-tenant"}
	envValue := "application-tenant"
	revision.Spec.Containers[0].Env = map[string]domain.EnvBinding{"WORKSPACE_ID": {Value: &envValue}}
	body := mustMarshal(t, CreateRunRequest{RunId: "run_application_metadata", Workload: revision})
	command := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	command.Header.Set("Content-Type", "application/json")
	command.Header.Set("Idempotency-Key", "application-workspace-metadata")
	commandRecorder := httptest.NewRecorder()
	handler.ServeHTTP(commandRecorder, command)
	if commandRecorder.Code != http.StatusAccepted {
		t.Fatalf("application-owned workspace metadata was interpreted by Mercator: %d %s", commandRecorder.Code, commandRecorder.Body.String())
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

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_exitcode", nil)
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

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_casing/events", nil)
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

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_wait_open/wait", nil)
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
