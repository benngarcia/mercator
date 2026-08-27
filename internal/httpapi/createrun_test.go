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
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/workload"
)

// B+C+D end-to-end at the HTTP layer: the minimal create body
// (just {"image":"busybox"}, NO run_id, NO pre-pinned digest) succeeds, the
// server generates a run id, resolves the tag to a pinned digest, and the run
// reaches a terminal succeeded state with exit_code exposed.
func TestCreateRunMinimalImageShorthandSucceeds(t *testing.T) {
	handler := newMinimalCreateServer(t, adapter.ExternalPhaseSucceeded)

	body := []byte(`{"image":"busybox","args":["echo","hi"]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_minimal")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var created RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Run.ID == "" || !strings.HasPrefix(created.Run.ID, "run_") {
		t.Fatalf("expected a generated run_ id, got %q", created.Run.ID)
	}
	if created.Run.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("expected succeeded outcome, got %q", created.Run.Outcome)
	}
	if created.Run.ExitCode == nil || *created.Run.ExitCode != 0 {
		t.Fatalf("expected exit_code 0, got %+v", created.Run.ExitCode)
	}

	// The stored revision image must be digest-pinned (resolved server-side).
	req = httptest.NewRequest(http.MethodGet, "/v1/runs/"+created.Run.ID+"/events", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "@sha256:") {
		t.Fatalf("expected a pinned @sha256: image in events, got %s", rec.Body.String())
	}
}

// D idempotency invariant at the HTTP layer: replaying the SAME Idempotency-Key
// (with no run_id, so each request would otherwise generate a new one) returns
// the ORIGINAL generated run_id, not a fresh one.
func TestCreateRunReplaySameKeyReturnsOriginalRunID(t *testing.T) {
	handler := newMinimalCreateServer(t, adapter.ExternalPhaseSucceeded)

	post := func() RunResponse {
		body := []byte(`{"image":"busybox"}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", "idem_replay_http")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
		}
		var resp RunResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}

	first := post()
	second := post()
	if !second.Duplicate {
		t.Fatalf("expected the replay to report duplicate=true, got %+v", second)
	}
	if second.Run.ID != first.Run.ID {
		t.Fatalf("replay returned a new run id %q; want original %q", second.Run.ID, first.Run.ID)
	}
}

// E: the failed / non-zero exit path. A run whose container exits non-zero must
// surface outcome=failed and the non-zero exit_code end-to-end.
func TestCreateRunFailedExitPath(t *testing.T) {
	handler := newMinimalCreateServer(t, adapter.ExternalPhaseFailed, fake.WithExitCode(42))

	body := []byte(`{"run_id":"run_failed","image":"busybox"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_failed")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_failed", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Run.Outcome != domain.RunOutcomeFailed {
		t.Fatalf("expected failed outcome, got %q", got.Run.Outcome)
	}
	if got.Run.ExitCode == nil || *got.Run.ExitCode != 42 {
		t.Fatalf("expected exit_code 42, got %+v", got.Run.ExitCode)
	}
}

// B precedence: when both a full workload spec and the image shorthand are
// supplied, the explicit workload spec wins and the shorthand is ignored.
func TestCreateRunFullWorkloadTakesPrecedenceOverShorthand(t *testing.T) {
	handler := newMinimalCreateServer(t, adapter.ExternalPhaseSucceeded)

	rev := httpRevision() // digest-pinned ghcr.io/acme/inference image
	payload := map[string]any{
		"run_id":   "run_precedence",
		"image":    "ignored-shorthand",
		"workload": rev,
	}
	body := mustMarshal(t, payload)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_precedence")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_precedence/events", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "ignored-shorthand") {
		t.Fatalf("shorthand image leaked despite explicit workload: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ghcr.io/acme/inference") {
		t.Fatalf("expected the explicit workload image, got %s", rec.Body.String())
	}
}

// TestCreateRunRefusesAnArtifactReadThisMercatorCannotEstablish is the answer a
// caller gets from a Mercator with no object store. Nothing here can establish
// that the version exists, and no later moment can either, so the request is
// refused with the reason rather than accepted into a phase it would never
// leave.
func TestCreateRunRefusesAnArtifactReadThisMercatorCannotEstablish(t *testing.T) {
	handler := newMinimalCreateServer(t, adapter.ExternalPhaseSucceeded)

	body := []byte(`{"run_id":"run_consumer","workload":{"spec":{"containers":[{"image":"busybox"}],"artifacts":{"consumes":["artifact:ds:v1"]}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_consumer")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("submitting a Run that reads an Artifact returned %d: %s", rec.Code, rec.Body.String())
	}
	var refusal ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("decode the refusal: %v", err)
	}
	if refusal.Code != "ARTIFACT_CATALOG_UNAVAILABLE" {
		t.Fatalf("the refusal is %+v, and the caller has to be able to tell why this Run cannot be taken", refusal)
	}
	if !strings.Contains(refusal.Message, "artifact:ds:v1") {
		t.Fatalf("the refusal says %q, and it has to name the Artifact nothing can establish", refusal.Message)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_consumer", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("the refused Run reads %d from the API, and Mercator never accepted it", rec.Code)
	}
}

func newMinimalCreateServer(t *testing.T, outcome adapter.ExternalPhase, extra ...fake.Option) http.Handler {
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
	opts := append([]fake.Option{
		fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_1", now)}),
		fake.WithLaunchOutcome(outcome),
	}, extra...)
	ad := fake.New(opts...)
	sched := scheduler.New()
	orch := orchestrator.New(log, sched, ad)
	resolver := ociresolver.NewStaticResolver(nil, ociresolver.WithSyntheticDigests(), ociresolver.WithAssumedPlatform("linux/amd64"))
	return New(Deps{Orchestrator: orch, Offers: singleProviderOffers{provider: ad}, Workloads: workload.New(log), Resolver: resolver})
}

// TestCreateRunRefusesAServiceClassMercatorCannotPrice is the answer a caller gets
// for a word Mercator has no exchange rate for. The class is what says a second of
// waiting is worth anything at all, so accepting the Run and ranking it on price
// alone would place work somebody is waiting on onto the slowest machine in the
// fleet and record a reason naming a class nothing declared. The caller learns it
// here instead of from the bill.
func TestCreateRunRefusesAServiceClassMercatorCannotPrice(t *testing.T) {
	handler := newMinimalCreateServer(t, adapter.ExternalPhaseSucceeded)

	body := []byte(`{"run_id":"run_urgent","workload":{"spec":{"containers":[{"image":"busybox"}],"placement":{"service_class":"urgent"}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_urgent")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("submitting a Run of an unknown class returned %d: %s", rec.Code, rec.Body.String())
	}
	var refusal ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("decode the refusal: %v", err)
	}
	if refusal.Code != "SERVICE_CLASS_UNKNOWN" {
		t.Fatalf("the refusal is %+v, and the caller has to be able to tell which field they got wrong", refusal)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_urgent", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("the refused Run reads %d from the API, and Mercator never accepted it", rec.Code)
	}
}

// TestBothDoorsFillTheOmittedServiceClass is the one body that reaches Mercator
// two ways. A caller who says nothing about the kind of work a Run is gets
// standard, which prices a second of waiting at what the machine doing the
// waiting costs, and the two doors have to agree about that: POST /v1/runs
// carries the whole revision inline, POST /v1/workloads/{id}/revisions stores one
// for later Runs to name, and a revision stored with no class at all is not a
// PlacementPolicy this API publishes.
func TestBothDoorsFillTheOmittedServiceClass(t *testing.T) {
	handler := newHTTPTestServer(t)
	createBody := mustMarshal(t, CreateWorkloadRequest{WorkloadId: "wrk_1", Name: "trainer"})
	req := httptest.NewRequest(http.MethodPost, "/v1/workloads", bytes.NewReader(createBody))
	req.Header.Set("Idempotency-Key", "idem_workload")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create workload returned %d: %s", rec.Code, rec.Body.String())
	}
	classless := httpRevision()
	classless.Spec.Placement = domain.PlacementPolicy{ExpectedRuntimeSeconds: 60}

	req = httptest.NewRequest(http.MethodPost, "/v1/workloads/wrk_1/revisions", bytes.NewReader(mustMarshal(t, CreateRevisionRequest{Revision: classless})))
	req.Header.Set("Idempotency-Key", "idem_revision")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("storing a revision that omits its class returned %d: %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/workloads/wrk_1/revisions/wrev_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var stored struct {
		Revision domain.WorkloadRevision `json:"revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode the stored revision: %v", err)
	}
	if stored.Revision.Spec.Placement.Class != domain.ClassStandard {
		t.Errorf("the stored revision reads class %q, and a revision the API serves has to state one", stored.Revision.Spec.Placement.Class)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(mustMarshal(t, CreateRunRequest{RunId: "run_classless", Workload: classless})))
	req.Header.Set("Idempotency-Key", "idem_run_classless")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submitting a Run that omits its class returned %d: %s", rec.Code, rec.Body.String())
	}
}
