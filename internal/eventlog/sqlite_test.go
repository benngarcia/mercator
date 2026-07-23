package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSQLiteEventLogAppendReadAndSubscribe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := openTestLog(t)

	sub, err := log.Subscribe(ctx, SubscriptionRequest{
		SubscriptionID: "sub-runs",
		After:          0,
		Filter: EventFilter{
			StreamTypes: []string{"run"},
		},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	result, err := log.Append(ctx, AppendRequest{
		Stream:                StreamKey{WorkspaceID: "ws_1", Type: "run", ID: "run_1"},
		ExpectedStreamVersion: 0,
		CommandKey:            "cmd-create-run",
		RequestHash:           "sha256:request",
		Actor:                 json.RawMessage(`{"principal":"user_1"}`),
		CorrelationID:         "run_1",
		CausationID:           "cmd_1",
		Events: []NewEvent{
			{
				ID:            "evt_1",
				Type:          "compute.run.requested.v1",
				SchemaVersion: 1,
				OccurredAt:    time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC),
				Visibility:    VisibilityPublic,
				Data:          json.RawMessage(`{"run_id":"run_1"}`),
			},
			{
				ID:            "evt_2",
				Type:          "compute.run.launch_intent_recorded.v1",
				SchemaVersion: 1,
				OccurredAt:    time.Date(2026, 6, 20, 18, 31, 23, 0, time.UTC),
				Visibility:    VisibilityPublic,
				Data:          json.RawMessage(`{"attempt_id":"att_1"}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if result.FirstPosition != 1 || result.LastPosition != 2 || result.NextStreamVersion != 2 {
		t.Fatalf("unexpected append result: %+v", result)
	}

	stream, err := log.ReadStream(ctx, StreamKey{WorkspaceID: "ws_1", Type: "run", ID: "run_1"}, 0, 10)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if len(stream) != 2 {
		t.Fatalf("expected 2 stream events, got %d", len(stream))
	}
	if stream[0].StreamVersion != 1 || stream[1].StreamVersion != 2 {
		t.Fatalf("unexpected stream versions: %+v", stream)
	}
	if stream[0].CloudEvent().Source != "compute-control-plane/workspaces/ws_1" {
		t.Fatalf("unexpected cloudevent source: %+v", stream[0].CloudEvent())
	}

	all, err := log.ReadAll(ctx, 0, 10, EventFilter{})
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(all) != 2 || all[1].GlobalPosition != 2 {
		t.Fatalf("unexpected global read: %+v", all)
	}

	select {
	case delivery := <-sub:
		if delivery.Event.ID != "evt_1" {
			t.Fatalf("unexpected first delivery: %+v", delivery)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not wake after append")
	}
}

func TestSQLiteEventLogFiltersPublicEventsAndReportsTheirHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := openTestLog(t)
	_, err := log.Append(ctx, AppendRequest{
		Stream:                StreamKey{WorkspaceID: "ws_1", Type: "run", ID: "run_1"},
		ExpectedStreamVersion: 0,
		CommandKey:            "cmd-create-run",
		RequestHash:           "sha256:request",
		Events: []NewEvent{
			{ID: "evt_public", Type: "compute.run.requested.v1", SchemaVersion: 1, OccurredAt: time.Now().UTC(), Visibility: VisibilityPublic, Data: json.RawMessage(`{"run_id":"run_1"}`)},
			{ID: "evt_private", Type: "compute.run.secret.v1", SchemaVersion: 1, OccurredAt: time.Now().UTC(), Visibility: VisibilityPrivate, Data: json.RawMessage(`{"secret":"redacted"}`)},
		},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	filter := EventFilter{WorkspaceID: "ws_1", Visibility: VisibilityPublic}
	events, err := log.ReadAll(ctx, 0, 10, filter)
	if err != nil {
		t.Fatalf("read public events: %v", err)
	}
	if len(events) != 1 || events[0].ID != "evt_public" {
		t.Fatalf("public events = %+v, want only evt_public", events)
	}
	head, err := log.LatestPosition(ctx, filter)
	if err != nil {
		t.Fatalf("latest public position: %v", err)
	}
	if head != events[0].GlobalPosition {
		t.Fatalf("public head = %d, want %d", head, events[0].GlobalPosition)
	}
}

func TestSQLiteEventLogIdempotencyAndConcurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := openTestLog(t)
	req := AppendRequest{
		Stream:                StreamKey{WorkspaceID: "ws_1", Type: "run", ID: "run_1"},
		ExpectedStreamVersion: 0,
		CommandKey:            "cmd-create-run",
		RequestHash:           "sha256:same",
		Actor:                 json.RawMessage(`{"principal":"user_1"}`),
		CorrelationID:         "run_1",
		CausationID:           "cmd_1",
		Events: []NewEvent{{
			ID:            "evt_1",
			Type:          "compute.run.requested.v1",
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Visibility:    VisibilityPublic,
			Data:          json.RawMessage(`{"run_id":"run_1"}`),
		}},
	}

	first, err := log.Append(ctx, req)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	replay, err := log.Append(ctx, req)
	if err != nil {
		t.Fatalf("idempotent append: %v", err)
	}
	if replay.LastPosition != first.LastPosition || !replay.Duplicate {
		t.Fatalf("expected duplicate result matching first append, got first=%+v replay=%+v", first, replay)
	}

	conflictReq := req
	conflictReq.RequestHash = "sha256:different"
	conflictReq.Events[0].ID = "evt_2"
	_, err = log.Append(ctx, conflictReq)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}

	wrongVersion := req
	wrongVersion.CommandKey = "cmd-wrong-version"
	wrongVersion.RequestHash = "sha256:wrong-version"
	wrongVersion.Events[0].ID = "evt_3"
	wrongVersion.ExpectedStreamVersion = 0
	_, err = log.Append(ctx, wrongVersion)
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("expected concurrency conflict, got %v", err)
	}
}

func TestSQLiteEventLogListsDistinctWorkspaceIDsFromEventIndex(t *testing.T) {
	ctx := context.Background()
	log := openTestLog(t)
	appendWorkspaceRun := func(workspaceID, runID string) {
		t.Helper()
		_, err := log.Append(ctx, AppendRequest{
			Stream:                StreamKey{WorkspaceID: workspaceID, Type: "run", ID: runID},
			ExpectedStreamVersion: 0,
			CommandKey:            "create:" + runID,
			RequestHash:           "sha256:" + runID,
			Events: []NewEvent{{
				ID:            "evt_" + runID,
				Type:          "compute.run.requested.v1",
				SchemaVersion: 1,
				OccurredAt:    time.Now().UTC(),
			}},
		})
		if err != nil {
			t.Fatalf("append %s: %v", runID, err)
		}
	}
	appendWorkspaceRun("staging-experiments", "run_experiment")
	appendWorkspaceRun("staging", "run_one")
	appendWorkspaceRun("staging", "run_two")
	_, err := log.Append(ctx, AppendRequest{
		Stream:                StreamKey{WorkspaceID: "ignored", Type: "connection", ID: "conn_1"},
		ExpectedStreamVersion: 0,
		CommandKey:            "create:conn_1",
		RequestHash:           "sha256:conn_1",
		Events:                []NewEvent{{ID: "evt_conn_1", Type: "compute.connection.created.v1", SchemaVersion: 1, OccurredAt: time.Now().UTC()}},
	})
	if err != nil {
		t.Fatalf("append connection: %v", err)
	}

	workspaceIDs, err := log.ListWorkspaceIDs(ctx, EventFilter{StreamTypes: []string{"run"}, EventTypes: []string{"compute.run.requested.v1"}})
	if err != nil {
		t.Fatalf("list workspace IDs: %v", err)
	}
	if got, want := strings.Join(workspaceIDs, ","), "staging,staging-experiments"; got != want {
		t.Fatalf("workspace IDs = %q, want %q", got, want)
	}
}

func TestCompleteHistoryReadsPastOnePage(t *testing.T) {
	ctx := context.Background()
	log := openTestLog(t)
	eventIDs := make([]string, 1001)
	for i := range eventIDs {
		eventIDs[i] = fmt.Sprintf("evt_history_%04d", i+1)
	}
	appendTestEvents(t, log, "run_history", "cmd-history", eventIDs)

	stream, err := ReadFullStream(ctx, log, StreamKey{WorkspaceID: "ws_1", Type: "run", ID: "run_history"})
	if err != nil {
		t.Fatalf("read full stream: %v", err)
	}
	if len(stream.Events) != 1001 || stream.LastVersion != 1001 {
		t.Fatalf("stream history = %d events at version %d, want 1001", len(stream.Events), stream.LastVersion)
	}

	var global []StoredEvent
	for event, err := range ScanAll(ctx, log, EventFilter{WorkspaceID: "ws_1", StreamTypes: []string{"run"}}) {
		if err != nil {
			t.Fatalf("scan all: %v", err)
		}
		global = append(global, event)
	}
	if len(global) != 1001 || global[len(global)-1].GlobalPosition != 1001 {
		t.Fatalf("global history = %d events at position %d, want 1001", len(global), global[len(global)-1].GlobalPosition)
	}
}

func TestSQLiteSubscribeResumesFromStoredOffset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := openTestLog(t)
	appendTestEvents(t, log, "run_offset", "cmd-offset", []string{"evt_offset_1", "evt_offset_2"})
	if err := log.Ack(ctx, "sub-runs", 1); err != nil {
		t.Fatalf("ack offset: %v", err)
	}

	sub, err := log.Subscribe(ctx, SubscriptionRequest{
		SubscriptionID: "sub-runs",
		After:          0,
		Filter: EventFilter{
			StreamTypes: []string{"run"},
		},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	select {
	case delivery := <-sub:
		if delivery.Event.ID != "evt_offset_2" {
			t.Fatalf("expected delivery after stored offset, got %+v", delivery.Event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not replay from stored offset")
	}
}

func TestSQLiteAckDoesNotMoveStoredOffsetBackward(t *testing.T) {
	ctx := context.Background()
	log := openTestLog(t)
	if err := log.Ack(ctx, "sub-runs", 100); err != nil {
		t.Fatalf("ack newer offset: %v", err)
	}

	if err := log.Ack(ctx, "sub-runs", 90); err != nil {
		t.Fatalf("ack older offset: %v", err)
	}

	offset, ok, err := log.Offset(ctx, "sub-runs")
	if err != nil {
		t.Fatalf("read offset: %v", err)
	}
	if !ok || offset != 100 {
		t.Fatalf("stored offset = %d, %t; want 100, true", offset, ok)
	}
}

func openTestLog(t *testing.T) *SQLiteEventLog {
	t.Helper()
	log, err := OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
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

func appendTestEvents(t *testing.T, log *SQLiteEventLog, runID, commandKey string, eventIDs []string) {
	t.Helper()
	events := make([]NewEvent, 0, len(eventIDs))
	for _, eventID := range eventIDs {
		events = append(events, NewEvent{
			ID:            eventID,
			Type:          "compute.run.requested.v1",
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Visibility:    VisibilityPublic,
			Data:          json.RawMessage(`{"run_id":"` + runID + `"}`),
		})
	}
	if _, err := log.Append(context.Background(), AppendRequest{
		Stream:                StreamKey{WorkspaceID: "ws_1", Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            commandKey,
		RequestHash:           "sha256:" + commandKey,
		Events:                events,
	}); err != nil {
		t.Fatalf("append test events: %v", err)
	}
}
