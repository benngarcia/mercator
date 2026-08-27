package eventlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestSQLiteEventLogFlattensLegacyWorkspacePartitions(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE events (
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
		);
		CREATE TABLE command_appends (
			workspace_id TEXT NOT NULL,
			command_key TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			first_position INTEGER NOT NULL,
			last_position INTEGER NOT NULL,
			PRIMARY KEY(workspace_id, command_key)
		);
		INSERT INTO events (
			event_id, workspace_id, stream_type, stream_id, stream_version,
			event_type, schema_version, occurred_at, command_key, request_hash,
			visibility, data_json
		) VALUES
			('evt_released', 'staging', 'run', 'run_released', 1,
			 'compute.run.requested.v1', 1, '2026-08-26T00:00:00Z',
			 'create:run_released', 'sha256:released', 'public', '{}'),
			('evt_experiment', 'staging-experiments', 'run', 'run_experiment', 1,
			 'compute.run.requested.v1', 1, '2026-08-26T00:00:01Z',
			 'create:run_experiment', 'sha256:experiment', 'public', '{}');
		INSERT INTO command_appends (
			workspace_id, command_key, request_hash, first_position, last_position
		) VALUES
			('staging', 'create:run_released', 'sha256:released', 1, 1),
			('staging-experiments', 'create:run_experiment', 'sha256:experiment', 2, 2);
	`); err != nil {
		t.Fatalf("seed legacy database: %v", err)
	}

	log, err := NewSQLite(ctx, db)
	if err != nil {
		t.Fatalf("open legacy event log: %v", err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Fatalf("close migrated event log: %v", err)
		}
	})

	for _, table := range []string{"events", "command_appends"} {
		if columns := sqliteColumns(t, db, table); slices.Contains(columns, "workspace_id") {
			t.Fatalf("%s columns = %v; workspace_id survived migration", table, columns)
		}
	}
	var events int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatalf("count migrated events: %v", err)
	}
	if events != 2 {
		t.Fatalf("migrated events = %d, want 2", events)
	}
}

func TestSQLiteEventLogFlattensPartitionedEventsWithGlobalCommands(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE events (
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
		);
		CREATE TABLE command_appends (
			command_key TEXT PRIMARY KEY,
			request_hash TEXT NOT NULL,
			first_position INTEGER NOT NULL,
			last_position INTEGER NOT NULL
		);
		INSERT INTO events (
			event_id, workspace_id, stream_type, stream_id, stream_version,
			event_type, schema_version, occurred_at, command_key, request_hash,
			visibility, data_json
		) VALUES
			('evt_workspace', 'staging', 'workspace', 'staging', 1,
			 'compute.workspace.created.v1', 1, '2026-08-26T00:00:00Z',
			 'create:workspace', 'sha256:workspace', 'public', '{}'),
			('evt_run', 'staging', 'run', 'run_1', 1,
			 'compute.run.requested.v1', 1, '2026-08-26T00:00:01Z',
			 'create:run_1', 'sha256:run', 'public', '{}');
		INSERT INTO command_appends (command_key, request_hash, first_position, last_position)
		VALUES
			('create:workspace', 'sha256:workspace', 1, 1),
			('create:run_1', 'sha256:run', 2, 2);
	`); err != nil {
		t.Fatalf("seed legacy database: %v", err)
	}

	log, err := NewSQLite(ctx, db)
	if err != nil {
		t.Fatalf("open legacy event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	if columns := sqliteColumns(t, db, "events"); slices.Contains(columns, "workspace_id") {
		t.Fatalf("events columns = %v; workspace_id survived migration", columns)
	}
	var events, commands int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatalf("count migrated events: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM command_appends`).Scan(&commands); err != nil {
		t.Fatalf("count migrated commands: %v", err)
	}
	if events != 1 || commands != 1 {
		t.Fatalf("migrated events=%d commands=%d, want only the surviving Run command", events, commands)
	}
}

func sqliteColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("inspect %s columns: %v", table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan %s column: %v", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("inspect %s columns: %v", table, err)
	}
	return columns
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

func TestSQLiteEventLogFiltersStreamAndEventTypes(t *testing.T) {
	ctx := context.Background()
	log := openTestLog(t)
	appendRun := func(runID string) {
		t.Helper()
		_, err := log.Append(ctx, AppendRequest{
			Stream:                StreamKey{Type: "run", ID: runID},
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
	appendRun("run_experiment")
	appendRun("run_one")
	appendRun("run_two")
	_, err := log.Append(ctx, AppendRequest{
		Stream:                StreamKey{Type: "connection", ID: "conn_1"},
		ExpectedStreamVersion: 0,
		CommandKey:            "create:conn_1",
		RequestHash:           "sha256:conn_1",
		Events:                []NewEvent{{ID: "evt_conn_1", Type: "compute.connection.created.v1", SchemaVersion: 1, OccurredAt: time.Now().UTC()}},
	})
	if err != nil {
		t.Fatalf("append connection: %v", err)
	}

	events, err := log.ReadAll(ctx, 0, 10, EventFilter{StreamTypes: []string{"run"}, EventTypes: []string{"compute.run.requested.v1"}})
	if err != nil {
		t.Fatalf("read filtered events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("filtered events = %d, want 3", len(events))
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
	filter := EventFilter{StreamTypes: []string{"run"}}
	head, err := log.LatestPosition(ctx, filter)
	if err != nil {
		t.Fatalf("capture global head: %v", err)
	}
	for event, err := range ScanAll(ctx, log, head, filter) {
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
