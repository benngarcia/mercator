package sinks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/eventlog"
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
