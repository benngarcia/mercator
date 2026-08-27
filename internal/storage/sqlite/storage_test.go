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

	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/runprojection"
	"github.com/benngarcia/mercator/internal/scheduler"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workload"
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

// TestAMigratedBookingDecisionAsksForARunProjectionRebuild is the other
// migration that rewrites the events the Run projection is reduced from.
//
// The service class rename was the one this was noticed on, and closing it there
// left the general statement untrue everywhere else: renaming a placement
// decision to a booking decision rewrites the same events, on the same run
// streams, and nothing marked the projection stale for it. The stored read model
// kept answering with Runs reduced from an event type that no longer exists in
// the log while RequiresRebuild called itself current, which is the identical
// failure with no crash needed to reach it.
func TestAMigratedBookingDecisionAsksForARunProjectionRebuild(t *testing.T) {
	// Arrange: a pre-rename database whose projection sits at the current schema
	// version, which is what every deployment's does before an upgrade. Its
	// events already speak the service class vocabulary, so the rename under
	// test is the only migration here with anything to rewrite and the only one
	// that can be marking the projection stale.
	ctx, db := openLegacyEventFixture(t)
	if _, err := db.ExecContext(ctx, `
		UPDATE events
		SET data_json = json_remove(
			json_set(data_json, '$.decision.policy.service_class', 'standard'),
			'$.decision.policy.objective'
		)
	`); err != nil {
		t.Fatalf("state the fixture in the current placement vocabulary: %v", err)
	}
	writeCurrentRunProjection(t, ctx, db)

	// Act
	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	// Assert
	rebuild, err := storage.Runs().RequiresRebuild(ctx)
	if err != nil {
		t.Fatalf("inspect the Run projection: %v", err)
	}
	if !rebuild {
		t.Fatal("the migration rewrote the log the Run projection is derived from and nothing asked for a rebuild")
	}
}

// writeCurrentRunProjection puts the projection metadata a database that has
// been serving carries into a fixture that predates it.
//
// The version it writes is read out of a database Mercator built rather than
// written down here, so an arrangement that means "this deployment's projection
// is current" cannot quietly stop meaning it the day the version changes.
func writeCurrentRunProjection(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE run_projection_metadata (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			schema_version INTEGER NOT NULL
		)
	`); err != nil {
		t.Fatalf("create the fixture's Run projection metadata: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO run_projection_metadata (singleton, schema_version) VALUES (1, ?)`,
		currentRunProjectionVersion(t),
	); err != nil {
		t.Fatalf("mark the fixture's Run projection current: %v", err)
	}
}

func currentRunProjectionVersion(t *testing.T) int {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "rebuilt.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = db.Close() }()
	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer func() { _ = storage.Close() }()
	if err := storage.Runs().MarkRebuilt(ctx); err != nil {
		t.Fatalf("mark a freshly built Run projection: %v", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `
		SELECT schema_version FROM run_projection_metadata WHERE singleton = 1
	`).Scan(&version); err != nil {
		t.Fatalf("read the current Run projection version: %v", err)
	}
	return version
}

// TestOpenRenamesLegacyPlacementObjectives is the service class rename on
// history. A Run that stated an objective carries a word nothing reads, in three
// places: the public request, the private one, and the policy its Booking Decision
// was taken under. A stored workload revision is the fourth, and the one that
// repriced work rather than only leaving a stale word behind. Each becomes the
// class that prices waiting the way that objective ranked it, and the objective is
// gone rather than kept beside it, because two vocabularies for one answer is how
// they drift.
func TestOpenRenamesLegacyPlacementObjectives(t *testing.T) {
	ctx, db := openLegacyObjectiveFixture(t)

	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	for _, migrated := range []struct {
		column string
		path   string
		want   domain.ServiceClass
	}{
		{"data_json", "$.workload_revision.spec.placement.service_class", domain.ClassInteractive},
		{"private_data", "$.workload_revision.spec.placement.service_class", domain.ClassInteractive},
		{"data_json", "$.decision.policy.service_class", domain.ClassBatch},
		{"data_json", "$.revision.spec.placement.service_class", domain.ClassExperimental},
	} {
		var class string
		query := "SELECT json_extract(" + migrated.column + ", '" + migrated.path + "') FROM events WHERE json_type(" + migrated.column + ", '" + migrated.path + "') = 'text'"
		if err := db.QueryRowContext(ctx, query).Scan(&class); err != nil {
			t.Fatalf("read %s at %s: %v", migrated.column, migrated.path, err)
		}
		if domain.ServiceClass(class) != migrated.want {
			t.Errorf("%s at %s = %q, want %q", migrated.column, migrated.path, class, migrated.want)
		}
	}
	// The rates each old decision was actually scored at, which were nearly all
	// zero: nothing populated the weights in production, so a Run that asked for
	// the cheapest capacity was scored on price and nothing else.
	var weights string
	if err := db.QueryRowContext(ctx, `
		SELECT json_extract(data_json, '$.decision.weights') FROM events
		WHERE json_type(data_json, '$.decision.weights') IS NOT NULL
	`).Scan(&weights); err != nil {
		t.Fatalf("read migrated weights: %v", err)
	}
	if weights != "{}" {
		t.Errorf("migrated weights = %s, and the cheapest objective was scored on price alone", weights)
	}
	// Completeness is asked of the whole payload rather than of the paths the
	// migration writes. Counting objectives at the sites it already rewrote is a
	// tautology that passes while a site nobody listed keeps the old vocabulary,
	// which is exactly how the stored workload revision was missed.
	var objectives int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM events
		WHERE data_json LIKE '%"objective"%' OR private_data LIKE '%"objective"%'
	`).Scan(&objectives); err != nil {
		t.Fatalf("count remaining objectives: %v", err)
	}
	if objectives != 0 {
		t.Errorf("%d events still state a placement objective somewhere in their payload", objectives)
	}
	// The class a caller stored, read back through the door a caller reads it
	// through. A revision left speaking the old vocabulary reads back with no
	// class at all, and the next Run created from it is normalised to standard.
	revision, err := workload.New(storage.EventLog()).GetRevision(ctx, "wl_1", "wrev_9")
	if err != nil {
		t.Fatalf("read the migrated revision: %v", err)
	}
	if revision.Spec.Placement.Class != domain.ClassExperimental {
		t.Errorf("the stored revision reads class %q, and a Run created from it is priced at whatever it says", revision.Spec.Placement.Class)
	}
}

// TestAMigratedRunReadsBackWithTheClassItsHistoryNowStates is the other half of
// migrating a vocabulary: the read model derived from the log Mercator rewrote.
//
// The Run projection is what GET /v1/runs serves, and it is stored rather than
// recomputed, so renaming the field inside the events left every historical Run
// answering with no class at all and nothing able to notice. Its schema version
// was already the current one, which is the only thing that asks for a rebuild,
// so the projection would have kept the old vocabulary for the life of the
// database. The migration marks it stale, and the rebuild the daemon already
// performs for that reason reduces the migrated log into the record a reader
// reads.
func TestAMigratedRunReadsBackWithTheClassItsHistoryNowStates(t *testing.T) {
	ctx, db := openLegacyObjectiveFixture(t)

	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	rebuild, err := storage.Runs().RequiresRebuild(ctx)
	if err != nil {
		t.Fatalf("inspect the Run projection: %v", err)
	}
	if !rebuild {
		t.Fatalf("the migration rewrote the log the Run projection is derived from and nothing asked for a rebuild")
	}
	orch := orchestrator.New(
		storage.EventLog(),
		scheduler.New(),
		fake.New(),
		orchestrator.WithRunProjection(storage.Runs()),
	)
	if err := orch.RebuildRunProjection(ctx); err != nil {
		t.Fatalf("rebuild the Run projection: %v", err)
	}

	page, err := storage.Runs().List(ctx, runprojection.PageRequest{})
	if err != nil {
		t.Fatalf("list the Run projection: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("the projection holds %d Runs, want the one the fixture recorded", len(page.Records))
	}
	if page.Records[0].ServiceClass != domain.ClassInteractive {
		t.Errorf("the projected Run reads class %q, and this is the copy every API reader is served",
			page.Records[0].ServiceClass)
	}
}

// TestOpenLeavesTheRunProjectionAloneWithNoHistoryToMigrate keeps the staleness
// specific to a log that was rewritten. A database Mercator has nothing to
// migrate must not ask for a rebuild of the deployment on every
// startup, which is what invalidating the projection unconditionally would do.
func TestOpenLeavesTheRunProjectionAloneWithNoHistoryToMigrate(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	storage, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	if err := storage.Runs().MarkRebuilt(ctx); err != nil {
		t.Fatalf("mark the Run projection current: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("close storage: %v", err)
	}

	reopened, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("reopen storage: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	rebuild, err := reopened.Runs().RequiresRebuild(ctx)
	if err != nil {
		t.Fatalf("inspect the Run projection: %v", err)
	}
	if rebuild {
		t.Errorf("a database with nothing to migrate asked for a Run projection rebuild")
	}
}

// TestOpenRefusesToRenameObjectivesWhileARunIsOpen is why the rename is safe on
// history and nowhere else. A Run still open is a Run whose next event Mercator
// appends from state it has already read, so rewriting the vocabulary underneath
// it leaves one stream speaking both.
func TestOpenRefusesToRenameObjectivesWhileARunIsOpen(t *testing.T) {
	ctx, db := openLegacyObjectiveFixture(t)
	if _, err := db.ExecContext(ctx, `DELETE FROM events WHERE event_type = 'compute.run.closed.v1'`); err != nil {
		t.Fatalf("reopen the legacy run: %v", err)
	}

	_, err := sqlitestore.New(ctx, db)

	if err == nil || !strings.Contains(err.Error(), "open legacy placement objectives") {
		t.Fatalf("open storage error = %v", err)
	}
}

// TestOpenRefusesAnObjectiveWithNoServiceClass keeps the mapping from guessing.
// An objective outside the four that existed prices a historical Run's waiting at
// a rate nobody chose, and a decision record is not a place to guess.
func TestOpenRefusesAnObjectiveWithNoServiceClass(t *testing.T) {
	ctx, db := openLegacyObjectiveFixture(t)
	if _, err := db.ExecContext(ctx, `
		UPDATE events
		SET data_json = json_set(data_json, '$.decision.policy.objective', 'whatever_is_free')
		WHERE json_type(data_json, '$.decision.policy.objective') = 'text'
	`); err != nil {
		t.Fatalf("write an objective nothing maps: %v", err)
	}

	_, err := sqlitestore.New(ctx, db)

	if err == nil || !strings.Contains(err.Error(), `objective "whatever_is_free" has no service class`) {
		t.Fatalf("open storage error = %v", err)
	}
}

func openLegacyObjectiveFixture(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "mercator.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	fixture, err := os.ReadFile("testdata/legacy_objective_event.sql")
	if err != nil {
		t.Fatalf("read legacy objective fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(fixture)); err != nil {
		t.Fatalf("load legacy objective fixture: %v", err)
	}
	return ctx, db
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
	run := domain.RunRecord{ID: "run-active", Phase: "launching"}
	if _, err := storage.RentalSchedules().Commit(ctx, request, 0, schedule, run); err != nil {
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
	page, err := reopened.Runs().List(ctx, runprojection.PageRequest{})
	if err != nil {
		t.Fatalf("list Run projection: %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != "run-active" {
		t.Fatalf("stored Run projection = %+v, want run-active", page.Records)
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
	appendLifecycleEvent(t, log, "before-storage", 0)
	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	appendLifecycleEvent(t, log, "after-storage", 1)
}

func appendLifecycleEvent(t *testing.T, log eventlog.EventLog, label string, expectedVersion uint64) {
	t.Helper()
	_, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{Type: "run", ID: "run_existing"},
		ExpectedStreamVersion: expectedVersion,
		CommandKey:            "run:refresh:" + label,
		RequestHash:           "sha256:" + label,
		Events: []eventlog.NewEvent{{
			ID:            "evt_" + label,
			Type:          "compute.run.external_state_observed.v1",
			SchemaVersion: 1,
		}},
	})
	if err != nil {
		t.Fatalf("append lifecycle event in %s: %v", label, err)
	}
}

// TestOpenMovesAStoredRevisionsSecretsOutOfThePublicPayload is the history half of
// the revision door's redaction. The door wrote the whole revision, environment
// values included, into a public event, and the console streams every public event
// to every reader of it, so fixing the door leaves the tokens in the
// records those readers read. Opening the database rewrites them: the public
// payload states each value's kind, the private payload is the revision, and the
// revision a Run would be created from is unchanged.
func TestOpenMovesAStoredRevisionsSecretsOutOfThePublicPayload(t *testing.T) {
	ctx, db := openStoredRevisionSecretFixture(t)

	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer func() { _ = storage.Close() }()

	events, err := storage.EventLog().ReadStream(ctx, eventlog.StreamKey{

		Type: "workload",
		ID:   "wrk_1",
	}, 0, 10)
	if err != nil {
		t.Fatalf("read the migrated workload: %v", err)
	}
	var migrated eventlog.StoredEvent
	for _, event := range events {
		if event.Type == "compute.workload.revision_created.v1" {
			migrated = event
		}
	}
	if migrated.ID == "" {
		t.Fatalf("the migrated stream has no revision event: %+v", events)
	}
	if strings.Contains(string(migrated.Data), "hf_live_SECRETVALUE") {
		t.Fatalf("the public payload still carries the token: %s", migrated.Data)
	}
	if !strings.Contains(string(migrated.Data), `"kind":"literal"`) || !strings.Contains(string(migrated.Data), `"kind":"empty"`) {
		t.Fatalf("the public payload says nothing about the variables at all: %s", migrated.Data)
	}
	if !strings.Contains(string(migrated.PrivateData), "hf_live_SECRETVALUE") {
		t.Fatalf("the private payload lost the value the caller stored: %s", migrated.PrivateData)
	}

	revision, err := workload.New(storage.EventLog()).GetRevision(ctx, "wrk_1", "wrev_1")
	if err != nil {
		t.Fatalf("read the migrated revision back: %v", err)
	}
	value := revision.Spec.Containers[0].Env["HF_TOKEN"].Value
	if value == nil || *value != "hf_live_SECRETVALUE" {
		t.Fatalf("the migrated revision reads back with %v, and a Run created from it would run with that", value)
	}
}

func openStoredRevisionSecretFixture(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "mercator.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	fixture, err := os.ReadFile("testdata/stored_revision_secret.sql")
	if err != nil {
		t.Fatalf("read the stored revision fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(fixture)); err != nil {
		t.Fatalf("load the stored revision fixture: %v", err)
	}
	return ctx, db
}
