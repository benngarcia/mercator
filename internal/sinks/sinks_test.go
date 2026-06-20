package sinks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bengarcia/mercator/internal/eventlog"
)

func TestManagerDurableCursorRetryAndRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := openSinkLog(t)
	appendSinkEvents(t, log, "run_sink", "cmd-sink", []string{"evt_sink_1", "evt_sink_2"})

	sink := &recordingSink{failOn: "evt_sink_2"}
	manager := NewManager(log, map[string]Sink{"audit": sink})
	result, err := manager.DeliverOnce(ctx, "audit")
	if err == nil {
		t.Fatal("expected sink failure")
	}
	if result.Delivered != 1 || result.FailedEventID != "evt_sink_2" {
		t.Fatalf("unexpected failed delivery result: %+v", result)
	}
	offset, ok, err := log.Offset(ctx, "sink:audit")
	if err != nil || !ok || offset != 1 {
		t.Fatalf("expected cursor at first delivered event, offset=%d ok=%v err=%v", offset, ok, err)
	}

	restarted := &recordingSink{}
	manager = NewManager(log, map[string]Sink{"audit": restarted})
	result, err = manager.DeliverOnce(ctx, "audit")
	if err != nil {
		t.Fatalf("restart delivery: %v", err)
	}
	if result.Delivered != 1 || len(restarted.ids) != 1 || restarted.ids[0] != "evt_sink_2" {
		t.Fatalf("restart should retry only failed event, result=%+v ids=%v", result, restarted.ids)
	}
}

func TestReplayIsBoundedObservableAndDoesNotMoveDurableCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := openSinkLog(t)
	appendSinkEvents(t, log, "run_replay", "cmd-replay", []string{"evt_replay_1", "evt_replay_2"})
	if err := log.Ack(ctx, "sink:audit", 2); err != nil {
		t.Fatalf("ack sink cursor: %v", err)
	}

	sink := &recordingSink{}
	result, err := NewManager(log, map[string]Sink{"audit": sink}).Replay(ctx, ReplayRequest{
		SinkID:        "audit",
		FromExclusive: 0,
		Limit:         1,
		ReplayID:      "replay_1",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Delivered != 1 || result.LastPosition != 1 || result.ReplayID != "replay_1" {
		t.Fatalf("unexpected replay result: %+v", result)
	}
	offset, ok, err := log.Offset(ctx, "sink:audit")
	if err != nil || !ok || offset != 2 {
		t.Fatalf("replay moved durable cursor, offset=%d ok=%v err=%v", offset, ok, err)
	}
}

func TestWebhookSinkPostsCloudEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var got eventlog.CloudEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode webhook: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	event := eventlog.StoredEvent{
		GlobalPosition: 1,
		ID:             "evt_webhook",
		WorkspaceID:    "ws_1",
		StreamType:     "run",
		StreamID:       "run_1",
		StreamVersion:  1,
		Type:           "compute.run.requested.v1",
		SchemaVersion:  1,
		OccurredAt:     time.Now().UTC(),
		Visibility:     eventlog.VisibilityPublic,
		Data:           json.RawMessage(`{"run_id":"run_1"}`),
	}
	if err := NewWebhookSink(server.URL, nil).Deliver(ctx, event); err != nil {
		t.Fatalf("deliver webhook: %v", err)
	}
	if got.ID != "evt_webhook" || got.Type != "compute.run.requested.v1" {
		t.Fatalf("unexpected webhook payload: %+v", got)
	}
}

func TestKafkaAndPostgresSinksUseConfiguredBackends(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	event := eventlog.StoredEvent{ID: "evt_backend", Type: "compute.run.requested.v1", WorkspaceID: "ws_1", Data: json.RawMessage(`{}`)}
	producer := &recordingProducer{}
	if err := NewKafkaSink("events", producer).Deliver(ctx, event); err != nil {
		t.Fatalf("deliver kafka: %v", err)
	}
	if producer.topic != "events" || producer.eventID != "evt_backend" {
		t.Fatalf("unexpected kafka write: %+v", producer)
	}
	writer := &recordingWriter{}
	if err := NewPostgresSink(writer).Deliver(ctx, event); err != nil {
		t.Fatalf("deliver postgres: %v", err)
	}
	if writer.eventID != "evt_backend" {
		t.Fatalf("unexpected postgres write: %+v", writer)
	}
}

type recordingSink struct {
	ids    []string
	failOn string
}

func (s *recordingSink) Deliver(_ context.Context, event eventlog.StoredEvent) error {
	s.ids = append(s.ids, event.ID)
	if event.ID == s.failOn {
		return errors.New("sink unavailable")
	}
	return nil
}

type recordingProducer struct {
	topic   string
	eventID string
}

func (p *recordingProducer) Produce(_ context.Context, topic string, event eventlog.CloudEvent) error {
	p.topic = topic
	p.eventID = event.ID
	return nil
}

type recordingWriter struct {
	eventID string
}

func (w *recordingWriter) InsertEvent(_ context.Context, event eventlog.StoredEvent) error {
	w.eventID = event.ID
	return nil
}

func openSinkLog(t *testing.T) *eventlog.SQLiteEventLog {
	t.Helper()
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite log: %v", err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Fatalf("close sqlite log: %v", err)
		}
	})
	return log
}

func appendSinkEvents(t *testing.T, log *eventlog.SQLiteEventLog, runID, commandKey string, eventIDs []string) {
	t.Helper()
	events := make([]eventlog.NewEvent, 0, len(eventIDs))
	for _, eventID := range eventIDs {
		events = append(events, eventlog.NewEvent{
			ID:            eventID,
			Type:          "compute.run.requested.v1",
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          json.RawMessage(`{"run_id":"` + runID + `"}`),
		})
	}
	if _, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: "ws_1", Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            commandKey,
		RequestHash:           "sha256:" + commandKey,
		Events:                events,
	}); err != nil {
		t.Fatalf("append sink events: %v", err)
	}
}
