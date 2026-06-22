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

// B+C+D end-to-end at the HTTP layer: the minimal create body
// (just {"image":"busybox"}, NO run_id, NO pre-pinned digest) succeeds, the
// server generates a run id, resolves the tag to a pinned digest, and the run
// reaches a terminal succeeded state with exit_code exposed.
func TestCreateRunMinimalImageShorthandSucceeds(t *testing.T) {
	handler := newMinimalCreateServer(t, adapter.ExternalPhaseSucceeded)

	body := []byte(`{"image":"busybox","args":["echo","hi"]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/runs?workspace_id=ws_1", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_minimal")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var created runResponse
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
	req = httptest.NewRequest(http.MethodGet, "/v1/runs/"+created.Run.ID+"/events?workspace_id=ws_1", nil)
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

	post := func() runResponse {
		body := []byte(`{"image":"busybox"}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/runs?workspace_id=ws_1", bytes.NewReader(body))
		req.Header.Set("Idempotency-Key", "idem_replay_http")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
		}
		var resp runResponse
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
	req := httptest.NewRequest(http.MethodPost, "/v1/runs?workspace_id=ws_1", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_failed")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_failed?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got runResponse
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
	req := httptest.NewRequest(http.MethodPost, "/v1/runs?workspace_id=ws_1", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_precedence")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/runs/run_precedence/events?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "ignored-shorthand") {
		t.Fatalf("shorthand image leaked despite explicit workload: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ghcr.io/acme/inference") {
		t.Fatalf("expected the explicit workload image, got %s", rec.Body.String())
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
	resolver := ociresolver.NewStaticResolver(nil, ociresolver.WithSyntheticDigests())
	return NewWithServices(orch, sched, ad, workload.New(log), secrets.New(log, []byte("01234567890123456789012345678901")), resolver)
}
