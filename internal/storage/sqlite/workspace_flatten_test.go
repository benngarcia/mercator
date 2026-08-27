package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

	for _, table := range legacyWorkspacePartitionedTables {
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
	assertLegacyCapacityRows(t, ctx, db)
}

func TestWorkspaceSchemaFlatteningDiscardsDuplicateDeletedConnections(t *testing.T) {
	ctx := context.Background()
	db := legacyWorkspaceDatabase(t, legacyCollisions{})
	seedLegacyConnectionCollision(t, db, "compute.connection.deleted.v1")

	storage, err := New(ctx, db)
	if err != nil {
		t.Fatalf("open legacy deployment: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	events, err := storage.EventLog().ReadStream(ctx, eventlog.StreamKey{Type: "connection", ID: "conn_docker_loopback"}, 0, 10)
	if err != nil {
		t.Fatalf("read discarded connection stream: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("discarded connection events = %+v", events)
	}
	var commands int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM command_appends`).Scan(&commands); err != nil {
		t.Fatalf("count retained commands: %v", err)
	}
	if commands != 2 {
		t.Fatalf("retained commands = %d, want only the unrelated Run commands", commands)
	}
	for _, runID := range []string{"run_released", "run_experiment"} {
		events, err := storage.EventLog().ReadStream(ctx, eventlog.StreamKey{Type: "run", ID: runID}, 0, 10)
		if err != nil || len(events) != 1 {
			t.Fatalf("read retained stream %s: events=%+v err=%v", runID, events, err)
		}
	}
}

func TestWorkspaceSchemaFlatteningRefusesDuplicateConnectionUnlessEveryCopyIsDeleted(t *testing.T) {
	ctx := context.Background()
	db := legacyWorkspaceDatabase(t, legacyCollisions{})
	t.Cleanup(func() { _ = db.Close() })
	seedLegacyConnectionCollision(t, db, "compute.connection.authorized.v1")

	err := flattenWorkspaceSchema(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "duplicate active connection conn_docker_loopback") {
		t.Fatalf("collision error = %v", err)
	}

	partitioned, inspectErr := tableHasColumn(ctx, db, "events", "workspace_id")
	if inspectErr != nil {
		t.Fatalf("inspect rolled-back events: %v", inspectErr)
	}
	if !partitioned {
		t.Fatal("events changed despite refused migration")
	}
	var events int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&events); err != nil || events != 8 {
		t.Fatalf("legacy events changed: count=%d err=%v", events, err)
	}
}

func TestWorkspaceSchemaFlatteningRefusesDisjointActiveConnectionCopies(t *testing.T) {
	ctx := context.Background()
	db := legacyWorkspaceDatabase(t, legacyCollisions{})
	t.Cleanup(func() { _ = db.Close() })
	seedLegacyDisjointActiveConnectionCollision(t, db)

	err := flattenWorkspaceSchema(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "duplicate active connection conn_docker_loopback") {
		t.Fatalf("collision error = %v", err)
	}

	partitioned, inspectErr := tableHasColumn(ctx, db, "events", "workspace_id")
	if inspectErr != nil {
		t.Fatalf("inspect rolled-back events: %v", inspectErr)
	}
	if !partitioned {
		t.Fatal("events changed despite refused migration")
	}
	var credentials int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM connection_secret WHERE connection_id = 'conn_docker_loopback'`).Scan(&credentials); err != nil || credentials != 1 {
		t.Fatalf("legacy credential changed: count=%d err=%v", credentials, err)
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

			for _, table := range legacyWorkspacePartitionedTables {
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
			for _, table := range legacyWorkspacePartitionedTables {
				if databaseTableExists(t, db, table+"_workspace_legacy") {
					t.Fatalf("transaction rollback left intermediate table %s", table)
				}
			}
			for _, table := range legacyWorkspacePartitionedTables {
				var rows int
				if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil || rows != 2 {
					t.Fatalf("legacy %s changed: count=%d err=%v", table, rows, err)
				}
			}
		})
	}
}

type legacyCollisions struct {
	connection bool
	run        bool
}

var legacyWorkspacePartitionedTables = []string{
	"events", "command_appends", "connection_secret", "runs", "rental_schedules",
	"nodes", "node_operations", "node_events", "node_workloads", "rentals",
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
		CREATE TABLE rental_schedules (
			workspace_id TEXT NOT NULL,
			rental_id TEXT NOT NULL,
			version INTEGER NOT NULL,
			schedule_json BLOB NOT NULL,
			PRIMARY KEY(workspace_id, rental_id)
		);
		CREATE TABLE nodes (
			workspace_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			rental_id TEXT NOT NULL,
			generation INTEGER NOT NULL,
			state TEXT NOT NULL,
			fencing_token INTEGER NOT NULL,
			enrollment_token_id TEXT NOT NULL,
			enrollment_expires TEXT NOT NULL,
			enrolled_at TEXT NOT NULL,
			lease_expires TEXT NOT NULL,
			last_heartbeat_at TEXT NOT NULL,
			agent_version TEXT NOT NULL,
			facts_json BLOB NOT NULL,
			shadow_price_usd_per_hour REAL NOT NULL DEFAULT 0,
			purchase_json BLOB NOT NULL DEFAULT '{}',
			PRIMARY KEY(workspace_id, node_id)
		);
		CREATE TABLE node_operations (
			workspace_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			operation_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			fencing_token INTEGER NOT NULL,
			state TEXT NOT NULL,
			issued_at TEXT NOT NULL,
			settled_at TEXT NOT NULL,
			failure TEXT NOT NULL,
			payload BLOB NOT NULL,
			sequence INTEGER NOT NULL,
			PRIMARY KEY(workspace_id, node_id, operation_id)
		);
		CREATE TABLE node_events (
			workspace_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			event_id TEXT NOT NULL,
			PRIMARY KEY(workspace_id, node_id, event_id)
		);
		CREATE TABLE node_workloads (
			workspace_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			attempt_id TEXT NOT NULL,
			observed_at TEXT NOT NULL,
			observation_json BLOB NOT NULL,
			PRIMARY KEY(workspace_id, node_id, run_id, attempt_id)
		);
		CREATE TABLE rentals (
			workspace_id TEXT NOT NULL,
			rental_id TEXT NOT NULL,
			version INTEGER NOT NULL,
			rental_json BLOB NOT NULL,
			PRIMARY KEY(workspace_id, rental_id)
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
		rentalID := []string{"rental_released", "rental_experiment"}[index]
		nodeID := []string{"node_released", "node_experiment"}[index]
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
		schedule, err := json.Marshal(domain.RentalSchedule{RentalID: rentalID, Version: 1, Bookings: []domain.ScheduledBooking{}})
		if err != nil {
			t.Fatalf("encode legacy Rental Schedule: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO rental_schedules VALUES (?, ?, 1, ?)`, workspaceID, rentalID, schedule); err != nil {
			t.Fatalf("seed legacy Rental Schedule: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO nodes VALUES (?, ?, ?, 1, 'ready', 1, '', ?, ?, ?, ?, 'fixture', '{}', 1.5, '{}')`, workspaceID, nodeID, rentalID, "2026-08-27T00:00:00Z", "2026-08-26T00:00:00Z", "2026-08-27T00:00:00Z", "2026-08-26T00:00:00Z"); err != nil {
			t.Fatalf("seed legacy Node: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO node_operations VALUES (?, ?, ?, 'launch', 1, 'settled', ?, ?, '', '{}', 1)`, workspaceID, nodeID, "op_"+nodeID, "2026-08-26T00:00:00Z", "2026-08-26T00:00:01Z"); err != nil {
			t.Fatalf("seed legacy Node operation: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO node_events VALUES (?, ?, ?)`, workspaceID, nodeID, "node_event_"+nodeID); err != nil {
			t.Fatalf("seed legacy Node event: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO node_workloads VALUES (?, ?, ?, ?, ?, '{}')`, workspaceID, nodeID, runIDs[index], "attempt_"+nodeID, "2026-08-26T00:00:00Z"); err != nil {
			t.Fatalf("seed legacy Node workload: %v", err)
		}
		rental, err := json.Marshal(domain.Rental{ID: rentalID, ConnectionID: connectionIDs[index], OwnershipToken: "owner_" + rentalID, Version: 1, Generations: []domain.RentalGeneration{}})
		if err != nil {
			t.Fatalf("encode legacy Rental: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO rentals VALUES (?, ?, 1, ?)`, workspaceID, rentalID, rental); err != nil {
			t.Fatalf("seed legacy Rental: %v", err)
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

func seedLegacyConnectionCollision(t *testing.T, db *sql.DB, secondTerminalEvent string) {
	t.Helper()
	for workspaceIndex, workspaceID := range []string{"staging", "staging-experiments"} {
		terminalEvent := "compute.connection.deleted.v1"
		if workspaceIndex == 1 {
			terminalEvent = secondTerminalEvent
		}
		for eventIndex, eventType := range []string{
			"compute.connection.created.v1",
			"compute.connection.authorized.v1",
			terminalEvent,
		} {
			version := eventIndex + 1
			commandKey := fmt.Sprintf("connection:%d", version)
			result, err := db.Exec(`INSERT INTO events (
				workspace_id, event_id, stream_type, stream_id, stream_version,
				event_type, schema_version, occurred_at, actor_json, correlation_id,
				causation_id, command_key, request_hash, visibility, data_json
			) VALUES (?, ?, 'connection', 'conn_docker_loopback', ?, ?, 1,
				'2026-08-27T00:00:00Z', '{}', 'conn_docker_loopback', 'seed', ?, ?, 'public', '{}')`,
				workspaceID, fmt.Sprintf("evt_%d_%d", workspaceIndex, version), version,
				eventType, commandKey, fmt.Sprintf("sha256:%d:%d", workspaceIndex, version))
			if err != nil {
				t.Fatalf("seed legacy connection event: %v", err)
			}
			position, err := result.LastInsertId()
			if err != nil {
				t.Fatalf("read legacy connection position: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO command_appends VALUES (?, ?, ?, ?, ?)`,
				workspaceID, commandKey, fmt.Sprintf("sha256:%d:%d", workspaceIndex, version), position, position); err != nil {
				t.Fatalf("seed legacy connection command: %v", err)
			}
		}
	}
}

func seedLegacyDisjointActiveConnectionCollision(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, event := range []struct {
		workspaceID   string
		eventID       string
		streamVersion int
		eventType     string
	}{
		{"staging", "evt_deleted", 1, "compute.connection.deleted.v1"},
		{"staging-experiments", "evt_active", 4, "compute.connection.authorized.v1"},
	} {
		commandKey := "command:" + event.eventID
		result, err := db.Exec(`INSERT INTO events (
			workspace_id, event_id, stream_type, stream_id, stream_version,
			event_type, schema_version, occurred_at, actor_json, correlation_id,
			causation_id, command_key, request_hash, visibility, data_json
		) VALUES (?, ?, 'connection', 'conn_docker_loopback', ?, ?, 1,
			'2026-08-27T00:00:00Z', '{}', 'conn_docker_loopback', 'seed', ?, ?, 'public', '{}')`,
			event.workspaceID, event.eventID, event.streamVersion, event.eventType, commandKey, "sha256:"+event.eventID)
		if err != nil {
			t.Fatalf("seed disjoint connection event: %v", err)
		}
		position, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("read disjoint connection position: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO command_appends VALUES (?, ?, ?, ?, ?)`,
			event.workspaceID, commandKey, "sha256:"+event.eventID, position, position); err != nil {
			t.Fatalf("seed disjoint connection command: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO connection_secret VALUES ('staging-experiments', 'conn_docker_loopback', 'sealed')`); err != nil {
		t.Fatalf("seed active connection credential: %v", err)
	}
}

func assertLegacyCapacityRows(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"rental_schedules", "nodes", "node_operations", "node_events", "node_workloads", "rentals"} {
		var rows int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil || rows != 2 {
			t.Fatalf("retained %s rows = %d, err=%v", table, rows, err)
		}
	}
}

func databaseTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, table).Scan(&exists); err != nil {
		t.Fatalf("inspect table %s: %v", table, err)
	}
	return exists
}
