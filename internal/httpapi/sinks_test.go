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
	"github.com/benngarcia/mercator/internal/sinks"
	"github.com/benngarcia/mercator/internal/workload"
)

func TestSinkReplayAPIAndFailureIsolation(t *testing.T) {
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite log: %v", err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Fatalf("close log: %v", err)
		}
	})
	sched := scheduler.New()
	ad := fake.New(fake.WithOffers([]domain.OfferSnapshot{httpOffer("offer_sink_api", time.Now().UTC())}), fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded))
	orch := orchestrator.New(log, sched, ad)
	handler := New(Deps{Orchestrator: orch, Scheduler: sched, Adapter: ad, Workloads: workload.New(log), Sinks: sinks.NewManager(log, map[string]sinks.Sink{"audit": failingSink{}}), Resolver: ociresolver.NewStaticResolver(nil)})

	body := mustMarshal(t, createRunBody{RunID: "run_sink_api", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_sink_api")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create run expected 202 despite failing sink, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/sinks/audit/replay", strings.NewReader(`{"from_exclusive":0,"limit":1,"replay_id":"replay_api"}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("failing replay expected 502, got %d body=%s", rec.Code, rec.Body.String())
	}
	var decoded errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if decoded.Code != "SINK_REPLAY_FAILED" {
		t.Fatalf("unexpected sink error: %+v", decoded)
	}
}

type failingSink struct{}

func (failingSink) Deliver(context.Context, eventlog.StoredEvent) error {
	return errors.New("sink unavailable")
}
