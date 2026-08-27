package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/runprojection"
)

func TestWorkspaceSchemaFlatteningPreservesDeploymentHistory(t *testing.T) {
	ctx := context.Background()
	db := legacyWorkspaceDatabase(t, legacyCollisions{})

	storage, err := New(ctx, db)
	if err != nil {
		t.Fatalf("open legacy deployment: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	for _, table := range []string{"events", "command_appends", "connection_secret", "runs"} {
		partitioned, err := tableHasColumn(ctx, db, table, "workspace_id")
		if err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if partitioned {
			t.Errorf("%s retained its workspace partition", table)
		}
	}
	for _, table := range []string{"workspaces", "workspace_members"} {
		if databaseTableExists(t, db, table) {
			t.Errorf("legacy catalog %s survived", table)
		}
	}

	for _, runID := range []string{"run_released", "run_experiment"} {
		events, err := storage.EventLog().ReadStream(ctx, eventlog.StreamKey{Type: "run", ID: runID}, 0, 10)
		if err != nil || len(events) != 1 {
			t.Fatalf("read retained stream %s: events=%+v err=%v", runID, events, err)
		}
	}
	page, err := storage.Runs().List(ctx, runprojection.PageRequest{Limit: 10})
	if err != nil {
		t.Fatalf("read retained Run projection: %v", err)
	}
	if len(page.Records) != 2 || page.Records[0].ID != "run_experiment" || page.Records[1].ID != "run_released" {
		t.Fatalf("retained Runs = %+v", page.Records)
	}
	for _, connectionID := range []string{"released", "experiment"} {
		secret, err := storage.CredentialStore().Get(ctx, connectionID)
		if err != nil || string(secret) != connectionID+"-secret" {
			t.Fatalf("read retained credential %s: secret=%q err=%v", connectionID, secret, err)
		}
	}
}

func TestWorkspaceSchemaCollisionLeavesTheCompleteDatabaseUntouched(t *testing.T) {
	for name, collision := range map[string]legacyCollisions{
		"credential discovered after event migration":          {connection: true},
		"Run projection discovered after credential migration": {run: true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := legacyWorkspaceDatabase(t, collision)
			t.Cleanup(func() { _ = db.Close() })

			err := flattenWorkspaceSchema(ctx, db)
			if err == nil || !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("collision error = %v", err)
			}

			for _, table := range []string{"events", "command_appends", "connection_secret", "runs"} {
				partitioned, inspectErr := tableHasColumn(ctx, db, table, "workspace_id")
				if inspectErr != nil {
					t.Fatalf("inspect rolled-back %s: %v", table, inspectErr)
				}
				if !partitioned {
					t.Errorf("%s changed despite refused migration", table)
				}
			}
			if !databaseTableExists(t, db, "workspaces") || !databaseTableExists(t, db, "workspace_members") {
				t.Fatal("workspace catalog changed despite refused migration")
			}
			if databaseTableExists(t, db, "events_workspace_legacy") {
				t.Fatal("transaction rollback left an intermediate event table")
			}
			var events int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&events); err != nil || events != 2 {
				t.Fatalf("legacy events changed: count=%d err=%v", events, err)
			}
		})
	}
}

type legacyCollisions struct {
	connection bool
	run        bool
}

func legacyWorkspaceDatabase(t *testing.T, collision legacyCollisions) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.Exec(`
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
		CREATE TABLE connection_secret (
			workspace_id TEXT NOT NULL,
			connection_id TEXT NOT NULL,
			blob BLOB NOT NULL,
			PRIMARY KEY(workspace_id, connection_id)
		);
		CREATE TABLE runs (
			workspace_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			closed INTEGER NOT NULL,
			record_json BLOB NOT NULL,
			PRIMARY KEY(workspace_id, run_id)
		);
		CREATE TABLE workspaces (workspace_id TEXT PRIMARY KEY);
		CREATE TABLE workspace_members (workspace_id TEXT NOT NULL, subject TEXT NOT NULL);
	`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	workspaces := []string{"staging", "staging-experiments"}
	runIDs := []string{"run_released", "run_experiment"}
	connectionIDs := []string{"released", "experiment"}
	if collision.run {
		runIDs[1] = runIDs[0]
	}
	if collision.connection {
		connectionIDs[1] = connectionIDs[0]
	}
	for index, workspaceID := range workspaces {
		streamID := []string{"run_released", "run_experiment"}[index]
		commandKey := "create:" + streamID
		result, err := db.Exec(`INSERT INTO events (
			workspace_id, event_id, stream_type, stream_id, stream_version,
			event_type, schema_version, occurred_at, actor_json, correlation_id,
			causation_id, command_key, request_hash, visibility, data_json
		) VALUES (?, ?, 'run', ?, 1, 'compute.run.requested.v1', 1,
			'2026-08-26T00:00:00Z', '{}', ?, 'seed', ?, ?, 'public', '{}')`,
			workspaceID, "evt_"+streamID, streamID, streamID, commandKey, "sha256:"+streamID)
		if err != nil {
			t.Fatalf("seed legacy event: %v", err)
		}
		position, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("read legacy position: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO command_appends VALUES (?, ?, ?, ?, ?)`, workspaceID, commandKey, "sha256:"+streamID, position, position); err != nil {
			t.Fatalf("seed legacy command: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO connection_secret VALUES (?, ?, ?)`, workspaceID, connectionIDs[index], []byte(connectionIDs[index]+"-secret")); err != nil {
			t.Fatalf("seed legacy credential: %v", err)
		}
		record, err := json.Marshal(domain.RunRecord{ID: runIDs[index]})
		if err != nil {
			t.Fatalf("encode legacy Run: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO runs VALUES (?, ?, 0, ?)`, workspaceID, runIDs[index], record); err != nil {
			t.Fatalf("seed legacy Run projection: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO workspaces VALUES (?)`, workspaceID); err != nil {
			t.Fatalf("seed workspace catalog: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO workspace_members VALUES (?, ?)`, workspaceID, "operator@example.com"); err != nil {
			t.Fatalf("seed workspace member: %v", err)
		}
	}
	return db
}

func databaseTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, table).Scan(&exists); err != nil {
		t.Fatalf("inspect table %s: %v", table, err)
	}
	return exists
}
