package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	modernsqlite "modernc.org/sqlite"
)

func TestConnectionCredentialWritePreservesStorageCause(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "mercator.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	resolver := credential.NewResolver(nil, storage.CredentialStore(), []byte("0123456789abcdef0123456789abcdef"))
	repository, err := storage.Connections(resolver)
	if err != nil {
		t.Fatalf("open connection repository: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE connection_secret`); err != nil {
		t.Fatalf("remove credential table: %v", err)
	}

	_, err = connection.NewWithCredentials(repository).Create(ctx, connection.CreateRequest{
		ConnectionID: "conn_1",
		AdapterType:  "runpod",
		Credential:   credential.Credential{Source: credential.SourceMercator},
		Secret:       []byte("secret"),
	})

	if !errors.Is(err, connection.ErrSecretStore) {
		t.Fatalf("create error = %v, want connection.ErrSecretStore", err)
	}
	var sqliteErr *modernsqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("create error = %v, want preserved SQLite cause", err)
	}
}

func TestOpenMigratesLegacyPlacementDecisionEvents(t *testing.T) {
	ctx, db := openLegacyEventFixture(t)

	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	assertMigratedLegacyBooking(t, ctx, storage)
}

func TestOpenCompletesV052BookingDecisionMigration(t *testing.T) {
	ctx, db := openLegacyEventFixture(t)
	if _, err := db.ExecContext(ctx, `
		UPDATE events
		SET event_type = 'compute.run.booking_decided.v1'
		WHERE event_type = 'compute.run.placement_decided.v1'
	`); err != nil {
		t.Fatalf("apply v0.5.2 migration: %v", err)
	}

	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	assertMigratedLegacyBooking(t, ctx, storage)
}

func openLegacyEventFixture(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "mercator.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	fixture, err := os.ReadFile("testdata/legacy_placement_event.sql")
	if err != nil {
		t.Fatalf("read legacy event fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(fixture)); err != nil {
		t.Fatalf("load legacy event fixture: %v", err)
	}
	return ctx, db
}

func assertMigratedLegacyBooking(t *testing.T, ctx context.Context, storage *sqlitestore.Storage) {
	t.Helper()
	events, err := storage.EventLog().ReadStream(ctx, eventlog.StreamKey{
		Type: "run",
		ID:   "run_1",
	}, 0, 10)
	if err != nil {
		t.Fatalf("read migrated run: %v", err)
	}
	if len(events) != 2 || events[0].Type != "compute.run.booking_decided.v1" {
		t.Fatalf("migrated events = %+v", events)
	}
	var payload struct {
		Decision struct {
			Booking *domain.Booking `json:"booking"`
		} `json:"decision"`
	}
	if err := json.Unmarshal(events[0].Data, &payload); err != nil {
		t.Fatalf("decode migrated decision: %v", err)
	}
	want := domain.Booking{
		ID:              "booking_legacy_decision_1",
		RunID:           "run_1",
		RentalID:        "rental_legacy_offer_1",
		State:           domain.BookingStateRunning,
		ScheduleVersion: 1,
	}
	if payload.Decision.Booking == nil || *payload.Decision.Booking != want {
		t.Fatalf("migrated Booking = %+v, want %+v", payload.Decision.Booking, want)
	}
}

func TestOpenRejectsOpenLegacyPlacementDecisionEvents(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "mercator.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	fixture, err := os.ReadFile("testdata/legacy_placement_event.sql")
	if err != nil {
		t.Fatalf("read legacy event fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(fixture)); err != nil {
		t.Fatalf("load legacy event fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM events WHERE event_type = 'compute.run.closed.v1'`); err != nil {
		t.Fatalf("reopen legacy run: %v", err)
	}

	_, err = sqlitestore.New(ctx, db)

	if err == nil || !strings.Contains(err.Error(), "open legacy placement decisions") {
		t.Fatalf("open storage error = %v", err)
	}
}

func TestOpenPurgesCredentialsForDeletedConnections(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	masterKey := []byte("0123456789abcdef0123456789abcdef")
	storage, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	resolver := credential.NewResolver(nil, storage.CredentialStore(), masterKey)
	connections, err := storage.Connections(resolver)
	if err != nil {
		t.Fatalf("open connection storage: %v", err)
	}
	service := connection.NewWithCredentials(connections)
	if _, err := service.Create(ctx, connection.CreateRequest{
		ConnectionID: "conn_deleted",
		AdapterType:  "runpod",
		Credential:   credential.Credential{Source: credential.SourceMercator},
		Secret:       []byte("original-secret"),
	}); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := service.Delete(ctx, connection.DeleteRequest{ConnectionID: "conn_deleted"}); err != nil {
		t.Fatalf("delete connection: %v", err)
	}
	orphaned, err := credential.Seal(credential.DeriveSealKey(masterKey), []byte("orphaned-secret"))
	if err != nil {
		t.Fatalf("seal orphaned credential: %v", err)
	}
	if err := storage.CredentialStore().Put(ctx, "conn_deleted", orphaned); err != nil {
		t.Fatalf("arrange orphaned credential: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("close storage: %v", err)
	}

	reopened, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("reopen storage: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	_, err = reopened.CredentialStore().Get(ctx, "conn_deleted")
	if !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("orphaned credential lookup error = %v, want credential.ErrNotFound", err)
	}
}

func TestRentalScheduleCommitSurvivesStorageRestart(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	storage, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	schedule, booking, err := domain.NewRentalSchedule("rental-warm").Reserve(domain.BookingRequest{
		BookingID: "booking-active", RunID: "run-active", ExpectedRuntimeSeconds: 60, MaxRuntimeSeconds: 90, ReservedAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("reserve Booking: %v", err)
	}
	request := eventlog.AppendRequest{
		Stream:     eventlog.StreamKey{Type: "run", ID: "run-active"},
		CommandKey: "run-active:place", RequestHash: "sha256:place", CorrelationID: "run-active", CausationID: "place",
		Events: []eventlog.NewEvent{{ID: "evt_booking_active", Type: "compute.run.booking_decided.v1", SchemaVersion: 1, OccurredAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC), Data: json.RawMessage(`{}`)}},
	}
	if _, err := storage.RentalSchedules().Commit(ctx, request, 0, schedule); err != nil {
		t.Fatalf("commit Rental Schedule: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("close storage: %v", err)
	}

	reopened, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("reopen storage: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	schedules, err := reopened.RentalSchedules().List(ctx)
	if err != nil {
		t.Fatalf("list Rental Schedules: %v", err)
	}
	stored := schedules["rental-warm"]
	if stored.Version != 1 || len(stored.Bookings) != 1 || stored.Bookings[0].Booking.ID != booking.ID {
		t.Fatalf("stored Rental Schedule = %+v", stored)
	}
}

func TestStorageConstructionDoesNotChangeEventLogAppendContract(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "mercator.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	log, err := eventlog.NewSQLite(ctx, db)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	appendLifecycleEvent(t, log, "run_before_storage")
	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	appendLifecycleEvent(t, log, "run_after_storage")
}

func TestOpenRemovesLegacyWorkspacePartitionsAsOneMigration(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	db := openDatabase(t, dsn)
	loadLegacyWorkspaceStorage(t, ctx, db, false)

	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	for _, table := range []string{"events", "command_appends", "connection_secret", "rental_schedules"} {
		if columnExists(t, ctx, db, table, "workspace_id") {
			t.Fatalf("%s still has workspace partition", table)
		}
	}
	if tableExists(t, ctx, db, "workspaces") {
		t.Fatal("workspace catalog still exists")
	}
	if _, err := storage.CredentialStore().Get(ctx, "conn_1"); err != nil {
		t.Fatalf("load migrated credential: %v", err)
	}
	schedules, err := storage.RentalSchedules().List(ctx)
	if err != nil {
		t.Fatalf("list migrated rental schedules: %v", err)
	}
	if schedules["rental_1"].Version != 1 {
		t.Fatalf("migrated rental schedule = %+v", schedules["rental_1"])
	}
	events, err := storage.EventLog().ReadStream(ctx, eventlog.StreamKey{Type: "connection", ID: "conn_1"}, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("migrated connection events = %+v, error = %v", events, err)
	}
}

func TestOpenAcceptsAlreadyFlatStagingStorage(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	db := openDatabase(t, dsn)
	if _, err := db.ExecContext(ctx, `CREATE TABLE deployment_projections (deployment_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create staging-only table: %v", err)
	}

	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open already-flat storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if !tableExists(t, ctx, db, "deployment_projections") {
		t.Fatal("workspace removal changed an unrelated staging table")
	}
}

func TestOpenRollsBackEveryWorkspaceRemovalWhenIdentityCollides(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	db := openDatabase(t, dsn)
	loadLegacyWorkspaceStorage(t, ctx, db, true)

	_, err := sqlitestore.New(ctx, db)
	if err == nil || !strings.Contains(err.Error(), `duplicate connection credential "conn_1"`) {
		t.Fatalf("open storage error = %v", err)
	}

	db = openDatabase(t, dsn)
	t.Cleanup(func() { _ = db.Close() })
	if !columnExists(t, ctx, db, "events", "workspace_id") {
		t.Fatal("event migration committed before credential collision")
	}
	if !columnExists(t, ctx, db, "connection_secret", "workspace_id") {
		t.Fatal("credential migration changed the collided table")
	}
	if !tableExists(t, ctx, db, "workspaces") {
		t.Fatal("workspace catalog was removed despite migration rollback")
	}
}

func openDatabase(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func loadLegacyWorkspaceStorage(t *testing.T, ctx context.Context, db *sql.DB, duplicateCredential bool) {
	t.Helper()
	statements := []string{
		`CREATE TABLE events (
			global_position INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE,
			workspace_id TEXT NOT NULL, stream_type TEXT NOT NULL, stream_id TEXT NOT NULL,
			stream_version INTEGER NOT NULL, event_type TEXT NOT NULL, schema_version INTEGER NOT NULL,
			occurred_at TEXT NOT NULL, actor_json BLOB, correlation_id TEXT, causation_id TEXT,
			command_key TEXT NOT NULL, request_hash TEXT NOT NULL, visibility TEXT NOT NULL,
			data_json BLOB, private_data BLOB,
			UNIQUE(workspace_id, stream_type, stream_id, stream_version))`,
		`CREATE TABLE command_appends (
			workspace_id TEXT NOT NULL, command_key TEXT NOT NULL, request_hash TEXT NOT NULL,
			first_position INTEGER NOT NULL, last_position INTEGER NOT NULL,
			PRIMARY KEY(workspace_id, command_key))`,
		`CREATE TABLE connection_secret (
			workspace_id TEXT NOT NULL, connection_id TEXT NOT NULL, blob BLOB NOT NULL,
			PRIMARY KEY(workspace_id, connection_id))`,
		`CREATE TABLE rental_schedules (
			workspace_id TEXT NOT NULL, rental_id TEXT NOT NULL, version INTEGER NOT NULL,
			schedule_json BLOB NOT NULL, PRIMARY KEY(workspace_id, rental_id))`,
		`CREATE TABLE workspaces (workspace_id TEXT PRIMARY KEY, display_name TEXT NOT NULL,
			created_at TEXT NOT NULL, created_by TEXT NOT NULL, archived_at TEXT)`,
		`INSERT INTO workspaces VALUES ('ws_1', 'One', '2030-01-01T00:00:00Z', 'test', NULL)`,
		`INSERT INTO events (event_id, workspace_id, stream_type, stream_id, stream_version,
			event_type, schema_version, occurred_at, correlation_id, causation_id,
			command_key, request_hash, visibility, data_json)
		 VALUES ('evt_1', 'ws_1', 'connection', 'conn_1', 1, 'compute.connection.created.v1', 1,
			'2030-01-01T00:00:00Z', '', '', 'cmd_1', 'hash_1', 'public', '{}')`,
		`INSERT INTO command_appends VALUES ('ws_1', 'cmd_1', 'hash_1', 1, 1)`,
		`INSERT INTO connection_secret VALUES ('ws_1', 'conn_1', X'0102')`,
		`INSERT INTO rental_schedules VALUES ('ws_1', 'rental_1', 1,
			'{"rental_id":"rental_1","version":1,"bookings":[]}')`,
	}
	if duplicateCredential {
		statements = append(statements,
			`INSERT INTO workspaces VALUES ('ws_2', 'Two', '2030-01-01T00:00:00Z', 'test', NULL)`,
			`INSERT INTO connection_secret VALUES ('ws_2', 'conn_1', X'0304')`)
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("load legacy workspace storage: %v", err)
		}
	}
}

func columnExists(t *testing.T, ctx context.Context, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("inspect %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan %s columns: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("inspect %s columns: %v", table, err)
	}
	return false
}

func tableExists(t *testing.T, ctx context.Context, db *sql.DB, table string) bool {
	t.Helper()
	var found string
	err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("inspect table %s: %v", table, err)
	}
	return true
}

func appendLifecycleEvent(t *testing.T, log eventlog.EventLog, runID string) {
	t.Helper()
	_, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            "run:refresh:" + runID,
		RequestHash:           "sha256:" + runID,
		Events: []eventlog.NewEvent{{
			ID:            "evt_" + runID,
			Type:          "compute.run.external_state_observed.v1",
			SchemaVersion: 1,
		}},
	})
	if err != nil {
		t.Fatalf("append lifecycle event for %s: %v", runID, err)
	}
}
