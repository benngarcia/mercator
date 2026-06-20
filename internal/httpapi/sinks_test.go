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

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/adapter/fake"
	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
	"github.com/bengarcia/mercator/internal/ociresolver"
	"github.com/bengarcia/mercator/internal/orchestrator"
	"github.com/bengarcia/mercator/internal/scheduler"
	"github.com/bengarcia/mercator/internal/secrets"
	"github.com/bengarcia/mercator/internal/sinks"
	"github.com/bengarcia/mercator/internal/workload"
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
	handler := NewWithAllServices(orch, sched, ad, workload.New(log), secrets.New(log, []byte("01234567890123456789012345678901")), sinks.NewManager(log, map[string]sinks.Sink{"audit": failingSink{}}), nil, nil, ociresolver.NewStaticResolver(nil))

	body := mustMarshal(t, createRunBody{RunID: "run_sink_api", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_sink_api")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create run expected 202 despite failing sink, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/sinks/audit:replay", strings.NewReader(`{"from_exclusive":0,"limit":1,"replay_id":"replay_api"}`))
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
