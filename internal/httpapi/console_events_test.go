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
	"github.com/benngarcia/mercator/internal/scenario"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/workload"
)

func TestConsoleEventStreamRunsTheGoDashboardScenario(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	playback := scenario.NewDashboardPlayback()
	handler := New(Deps{Scenarios: playback})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/console/events?workspace_id=ws_scenario&scenario="+scenario.DashboardScenarioName+"&play=1", nil)
	if err != nil {
		t.Fatalf("new scenario stream request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("open scenario stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("scenario stream response = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}

	reader := bufio.NewReader(response.Body)
	reset := readSSEFrame(t, reader)
	if reset.Event != "reset" || !bytes.Contains(reset.Data, []byte(`"offer_source":"sanitized_recordings"`)) ||
		!bytes.Contains(reset.Data, []byte(`"adapter_type":"runpod"`)) || !bytes.Contains(reset.Data, []byte(`"adapter_type":"shadeform"`)) ||
		!bytes.Contains(reset.Data, []byte(`"adapter_type":"vast"`)) {
		t.Fatalf("initial scenario reset = %s", reset.Data)
	}

	commandBody := bytes.NewBufferString(`{"type":"pause"}`)
	command, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/dev/scenario-sessions/ws_scenario/commands", commandBody)
	if err != nil {
		t.Fatalf("new scenario command: %v", err)
	}
	command.Header.Set("Content-Type", "application/json")
	commandResponse, err := server.Client().Do(command)
	if err != nil {
		t.Fatalf("pause scenario: %v", err)
	}
	defer commandResponse.Body.Close()
	if commandResponse.StatusCode != http.StatusOK {
		t.Fatalf("pause response = %d", commandResponse.StatusCode)
	}
	paused := readSSEFrame(t, reader)
	if paused.Event != "playback" || !bytes.Contains(paused.Data, []byte(`"status":"paused"`)) {
		t.Fatalf("paused scenario frame = %+v", paused)
	}
}

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
		Orchestrator: orchestrator.New(workspaceTestLog{EventLog: logStore}, scheduler.New(), provider),
		Offers:       singleProviderOffers{provider: provider},
		Workloads:    workload.New(workspaceTestLog{EventLog: logStore}),
		Events:       logStore,
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/console/events?workspace_id=ws_1", nil)
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

	first := catalog.Subscribe(firstContext, "ws_1")
	second := catalog.Subscribe(secondContext, "ws_1")
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
	snapshot := catalog.snapshot(t.Context(), "ws_1")
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
