package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/workload"
)

func TestConsoleEventStreamSnapsThenDeliversActualRunEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logStore, err := eventlog.OpenSQLite(ctx, "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = logStore.Close() })
	provider := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_console", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	handler := New(Deps{
		Orchestrator: orchestrator.New(logStore, scheduler.New(), provider),
		Offers:       singleProviderOffers{provider: provider},
		Workloads:    workload.New(logStore),
		Events:       logStore,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/console/events", nil)
	if err != nil {
		t.Fatalf("new stream request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream response = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}

	reader := bufio.NewReader(response.Body)
	offers := readSSEFrame(t, reader)
	if offers.Event != "offers_replaced" || !bytes.Contains(offers.Data, []byte(`"rental_id":"off_console"`)) {
		t.Fatalf("initial Offer replacement = %+v", offers)
	}
	ready := readSSEFrame(t, reader)
	if ready.Event != "ready" || !bytes.Contains(ready.Data, []byte(`"through_global_position":0`)) {
		t.Fatalf("ready frame = %+v", ready)
	}

	createRunThroughHTTP(t, server.Client(), server.URL, "run_console")
	var requested eventlog.CloudEvent
	var decided eventlog.CloudEvent
	deadline := time.After(3 * time.Second)
	for requested.ID == "" || decided.ID == "" {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for live run events; requested=%+v decided=%+v", requested, decided)
		default:
		}
		frame := readSSEFrame(t, reader)
		if frame.Event != "domain_event" {
			continue
		}
		var event eventlog.CloudEvent
		if err := json.Unmarshal(frame.Data, &event); err != nil {
			t.Fatalf("decode domain event: %v", err)
		}
		switch event.Type {
		case orchestrator.EventRunRequested:
			requested = event
		case orchestrator.EventBookingDecided:
			decided = event
		}
	}
	if requested.GlobalPosition == 0 || requested.Subject != "runs/run_console" {
		t.Fatalf("requested event = %+v", requested)
	}
	if !bytes.Contains(decided.Data, []byte(`"booking":{"id":"bkg_`)) || !bytes.Contains(decided.Data, []byte(`"rental_id":"off_console"`)) {
		t.Fatalf("booking decision does not carry Rental and Booking identity: %s", decided.Data)
	}
}

func TestOfferCatalogSharesOneObservationAcrossSubscribers(t *testing.T) {
	aggregator := &countingOfferAggregator{offer: httpOffer("off_shared", time.Now().UTC())}
	catalog := newOfferCatalog(aggregator, time.Hour)
	firstContext, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	secondContext, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()

	first := catalog.Subscribe(firstContext)
	second := catalog.Subscribe(secondContext)
	firstSnapshot := <-first
	secondSnapshot := <-second

	if firstSnapshot.Revision == "" || firstSnapshot.Revision != secondSnapshot.Revision {
		t.Fatalf("shared catalog revisions = %q and %q", firstSnapshot.Revision, secondSnapshot.Revision)
	}
	if aggregator.Calls() != 1 {
		t.Fatalf("provider observations = %d, want one shared observation", aggregator.Calls())
	}
}

func TestOfferCatalogEncodesEmptyOffersAsAnArray(t *testing.T) {
	catalog := newOfferCatalog(emptyOfferAggregator{}, time.Hour)
	snapshot := catalog.snapshot(t.Context())
	var wire bytes.Buffer

	err := writeConsoleMessage(&wire, "offers_replaced", "", snapshot)

	if err != nil {
		t.Fatalf("encode empty Offer catalog: %v", err)
	}
	if !bytes.Contains(wire.Bytes(), []byte(`"offers":[]`)) {
		t.Fatalf("empty Offer catalog = %s, want offers array", wire.String())
	}
}

type sseFrame struct {
	ID    string
	Event string
	Data  json.RawMessage
}

func readSSEFrame(t *testing.T, reader *bufio.Reader) sseFrame {
	t.Helper()
	frame := sseFrame{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			return frame
		}
		switch {
		case strings.HasPrefix(line, "id: "):
			frame.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			frame.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			frame.Data = json.RawMessage(strings.TrimPrefix(line, "data: "))
		}
	}
}

func createRunThroughHTTP(t *testing.T, client *http.Client, baseURL, runID string) {
	t.Helper()
	body := mustMarshal(t, CreateRunRequest{RunId: runID, Workload: httpRevision()})
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/runs", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new create request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "create-"+runID)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		var responseBody bytes.Buffer
		_, _ = responseBody.ReadFrom(response.Body)
		t.Fatalf("create run status = %d body=%s", response.StatusCode, responseBody.String())
	}
}

type countingOfferAggregator struct {
	mu    sync.Mutex
	calls int
	offer domain.OfferSnapshot
}

type emptyOfferAggregator struct{}

func (emptyOfferAggregator) AggregateOffers(context.Context, adapter.OfferRequest) (broker.OfferAggregation, error) {
	return broker.OfferAggregation{Offers: []domain.OfferSnapshot{}, Failures: broker.ConnectionErrors{}}, nil
}

func (a *countingOfferAggregator) AggregateOffers(context.Context, adapter.OfferRequest) (broker.OfferAggregation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return broker.OfferAggregation{Offers: []domain.OfferSnapshot{a.offer}, Failures: broker.ConnectionErrors{}}, nil
}

func (a *countingOfferAggregator) Calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// TestConsoleEventStreamCarriesNoStoredEnvironmentValue is the redaction at the
// audience it exists for. The console subscribes to every public event in the
// deployment with no restriction on which stream it came from, so a workload
// revision stored with a token in a container's environment put that token on the
// wire to every reader. Both doors that write a revision into a public event are
// held to one rule now, and this is the one that reads what a reader actually
// receives.
func TestConsoleEventStreamCarriesNoStoredEnvironmentValue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logStore, err := eventlog.OpenSQLite(ctx, "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = logStore.Close() })
	provider := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_console_secret", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	handler := New(Deps{
		Orchestrator: orchestrator.New(logStore, scheduler.New(), provider),
		Offers:       singleProviderOffers{provider: provider},
		Workloads:    workload.New(logStore),
		Events:       logStore,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/console/events", nil)
	if err != nil {
		t.Fatalf("new stream request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readSSEFrame(t, reader)
	readSSEFrame(t, reader)

	storeRevisionThroughHTTP(t, server.Client(), server.URL, "hf_live_SECRETVALUE")

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the stored revision to reach the console")
		default:
		}
		frame := readSSEFrame(t, reader)
		if frame.Event != "domain_event" {
			continue
		}
		var event eventlog.CloudEvent
		if err := json.Unmarshal(frame.Data, &event); err != nil {
			t.Fatalf("decode domain event: %v", err)
		}
		if event.Type != workload.EventWorkloadRevisionCreated {
			continue
		}
		if bytes.Contains(event.Data, []byte("hf_live_SECRETVALUE")) {
			t.Fatalf("the console received the token verbatim: %s", event.Data)
		}
		if !bytes.Contains(event.Data, []byte(`"kind":"literal"`)) {
			t.Fatalf("the console learned nothing about the variable at all: %s", event.Data)
		}
		return
	}
}

func storeRevisionThroughHTTP(t *testing.T, client *http.Client, baseURL, secret string) {
	t.Helper()
	post := func(path, idempotencyKey string, body []byte) {
		request, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("new request for %s: %v", path, err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", idempotencyKey)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("post %s: %v", path, err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			var responseBody bytes.Buffer
			_, _ = responseBody.ReadFrom(response.Body)
			t.Fatalf("post %s status = %d body=%s", path, response.StatusCode, responseBody.String())
		}
	}
	post("/v1/workloads", "idem_workload_secret",
		mustMarshal(t, CreateWorkloadRequest{WorkloadId: "wrk_secret", Name: "secret"}))
	revision := httpRevision()
	revision.WorkloadID = "wrk_secret"
	revision.Spec.Containers[0].Env = map[string]domain.EnvBinding{"HF_TOKEN": {Value: &secret}}
	post("/v1/workloads/wrk_secret/revisions", "idem_revision_secret",
		mustMarshal(t, CreateRevisionRequest{Revision: revision}))
}
