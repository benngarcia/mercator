package eventlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestSQLiteEventLogFlattensLegacyWorkspacePartitions(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE events (
			global_position INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id TEXT NOT NULL UNIQUE,
			workspace_id TEXT NOT NULL,
			stream_type TEXT NOT NULL,
			stream_id TEXT NOT NULL,
			stream_version INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			schema_version INTEGER NOT NULL,
			occurred_at TEXT NOT NULL,
			actor_json BLOB,
			correlation_id TEXT,
			causation_id TEXT,
			command_key TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			visibility TEXT NOT NULL,
			data_json BLOB,
			private_data BLOB,
			UNIQUE(workspace_id, stream_type, stream_id, stream_version)
		)`,
		`CREATE TABLE command_appends (
			workspace_id TEXT NOT NULL,
			command_key TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			first_position INTEGER NOT NULL,
			last_position INTEGER NOT NULL,
			PRIMARY KEY(workspace_id, command_key)
		)`,
		`INSERT INTO events (
			event_id, workspace_id, stream_type, stream_id, stream_version,
			event_type, schema_version, occurred_at, actor_json, correlation_id, causation_id,
			command_key, request_hash, visibility, data_json
		) VALUES
			('evt_workspace', 'ws_1', 'workspace', 'ws_1', 1, 'workspace.created.v1', 1, '2026-01-01T00:00:00Z', '{}', '', '', 'cmd_workspace', 'sha256:workspace', 'public', '{}'),
			('evt_run_1', 'ws_1', 'run', 'run_1', 1, 'compute.run.requested.v1', 1, '2026-01-01T00:00:01Z', '{}', '', '', 'cmd_run_1', 'sha256:run-1', 'public', '{}'),
			('evt_run_2', 'ws_2', 'run', 'run_2', 1, 'compute.run.requested.v1', 1, '2026-01-01T00:00:02Z', '{}', '', '', 'cmd_run_2', 'sha256:run-2', 'public', '{}')`,
		`INSERT INTO command_appends VALUES
			('ws_1', 'cmd_workspace', 'sha256:workspace', 1, 1),
			('ws_1', 'cmd_run_1', 'sha256:run-1', 2, 2),
			('ws_2', 'cmd_run_2', 'sha256:run-2', 3, 3)`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("prepare legacy database: %v", err)
		}
	}

	log, err := NewSQLite(t.Context(), db)
	if err != nil {
		t.Fatalf("open legacy event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	var workspaceColumns int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pragma_table_info('events') WHERE name = 'workspace_id'`).Scan(&workspaceColumns); err != nil {
		t.Fatalf("inspect migrated events: %v", err)
	}
	if workspaceColumns != 0 {
		t.Fatalf("workspace columns = %d, want none", workspaceColumns)
	}
	events, err := log.ReadAll(t.Context(), 0, 10, EventFilter{})
	if err != nil {
		t.Fatalf("read migrated events: %v", err)
	}
	if len(events) != 2 || events[0].ID != "evt_run_1" || events[1].ID != "evt_run_2" {
		t.Fatalf("migrated events = %#v, want both deployment events and no workspace event", events)
	}
}

func TestSQLiteEventLogUsesDeploymentGlobalStreamIdentity(t *testing.T) {
	t.Parallel()
	log := openTestLog(t)

	result, err := log.Append(t.Context(), AppendRequest{
		Stream:                StreamKey{Type: "run", ID: "run_global"},
		ExpectedStreamVersion: 0,
		CommandKey:            "cmd-global",
		RequestHash:           "sha256:global",
		Events: []NewEvent{{
			ID:            "evt_global",
			Type:          "compute.run.requested.v1",
			SchemaVersion: 1,
			Data:          json.RawMessage(`{"run_id":"run_global"}`),
		}},
	})
	if err != nil {
		t.Fatalf("append deployment-global stream: %v", err)
	}
	if result.Events[0].CloudEvent().Source != "compute-control-plane" {
		t.Fatalf("cloud event source = %q, want deployment identity", result.Events[0].CloudEvent().Source)
	}
}

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
		Stream:                StreamKey{Type: "run", ID: "run_1"},
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

	stream, err := log.ReadStream(ctx, StreamKey{Type: "run", ID: "run_1"}, 0, 10)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if len(stream) != 2 {
		t.Fatalf("expected 2 stream events, got %d", len(stream))
	}
	if stream[0].StreamVersion != 1 || stream[1].StreamVersion != 2 {
		t.Fatalf("unexpected stream versions: %+v", stream)
	}
	if stream[0].CloudEvent().Source != "compute-control-plane" {
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
		Stream:                StreamKey{Type: "run", ID: "run_1"},
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
	filter := EventFilter{Visibility: VisibilityPublic}
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
		Stream:                StreamKey{Type: "run", ID: "run_1"},
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

func TestCompleteHistoryReadsPastOnePage(t *testing.T) {
	ctx := context.Background()
	log := openTestLog(t)
	eventIDs := make([]string, 1001)
	for i := range eventIDs {
		eventIDs[i] = fmt.Sprintf("evt_history_%04d", i+1)
	}
	appendTestEvents(t, log, "run_history", "cmd-history", eventIDs)

	stream, err := ReadFullStream(ctx, log, StreamKey{Type: "run", ID: "run_history"})
	if err != nil {
		t.Fatalf("read full stream: %v", err)
	}
	if len(stream.Events) != 1001 || stream.LastVersion != 1001 {
		t.Fatalf("stream history = %d events at version %d, want 1001", len(stream.Events), stream.LastVersion)
	}

	var global []StoredEvent
	for event, err := range ScanAll(ctx, log, EventFilter{StreamTypes: []string{"run"}}) {
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
		Stream:                StreamKey{Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            commandKey,
		RequestHash:           "sha256:" + commandKey,
		Events:                events,
	}); err != nil {
		t.Fatalf("append test events: %v", err)
	}
}
