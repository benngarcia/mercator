package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workload"
	"github.com/benngarcia/mercator/internal/workspace"
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
	if _, err := storage.Workspaces().Create(ctx, workspace.Create{
		ID:          "ws_1",
		DisplayName: "Test workspace",
		CreatedAt:   time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		CreatedBy:   "test:storage",
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	resolver := credential.NewResolver(nil, storage.CredentialStore(), []byte("0123456789abcdef0123456789abcdef"))
	repository, err := storage.Connections(resolver)
	if err != nil {
		t.Fatalf("open connection repository: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE connection_secret`); err != nil {
		t.Fatalf("remove credential table: %v", err)
	}

	_, err = connection.NewWithCredentials(repository).Create(ctx, connection.CreateRequest{
		WorkspaceID:  "ws_1",
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

func TestOpenPurgesCredentialsForDeletedConnections(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	masterKey := []byte("0123456789abcdef0123456789abcdef")
	storage, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	if _, err := storage.Workspaces().Create(ctx, workspace.Create{
		ID:          "ws_1",
		DisplayName: "Test workspace",
		CreatedAt:   time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		CreatedBy:   "test:storage",
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	resolver := credential.NewResolver(nil, storage.CredentialStore(), masterKey)
	connections, err := storage.Connections(resolver)
	if err != nil {
		t.Fatalf("open connection storage: %v", err)
	}
	service := connection.NewWithCredentials(connections)
	if _, err := service.Create(ctx, connection.CreateRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_deleted",
		AdapterType:  "runpod",
		Credential:   credential.Credential{Source: credential.SourceMercator},
		Secret:       []byte("original-secret"),
	}); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if _, err := storage.Workspaces().Archive(ctx, "ws_1", time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("archive workspace: %v", err)
	}
	if err := service.Delete(ctx, connection.DeleteRequest{WorkspaceID: "ws_1", ConnectionID: "conn_deleted"}); err != nil {
		t.Fatalf("delete connection in archived workspace: %v", err)
	}
	orphaned, err := credential.Seal(credential.DeriveSealKey(masterKey), []byte("orphaned-secret"))
	if err != nil {
		t.Fatalf("seal orphaned credential: %v", err)
	}
	if err := storage.CredentialStore().Put(ctx, "ws_1", "conn_deleted", orphaned); err != nil {
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

	_, err = reopened.CredentialStore().Get(ctx, "ws_1", "conn_deleted")
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
	if _, err := storage.Workspaces().Create(ctx, workspace.Create{
		ID: "ws_schedule", DisplayName: "Schedule workspace", CreatedAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC), CreatedBy: "test:storage",
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	schedule, booking, err := domain.NewRentalSchedule("rental-warm").Reserve(domain.BookingRequest{
		BookingID: "booking-active", RunID: "run-active", ExpectedRuntimeSeconds: 60, MaxRuntimeSeconds: 90, ReservedAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("reserve Booking: %v", err)
	}
	request := eventlog.AppendRequest{
		Stream:     eventlog.StreamKey{WorkspaceID: "ws_schedule", Type: "run", ID: "run-active"},
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
	schedules, err := reopened.RentalSchedules().List(ctx, "ws_schedule")
	if err != nil {
		t.Fatalf("list Rental Schedules: %v", err)
	}
	stored := schedules["rental-warm"]
	if stored.Version != 1 || len(stored.Bookings) != 1 || stored.Bookings[0].Booking.ID != booking.ID {
		t.Fatalf("stored Rental Schedule = %+v", stored)
	}
}

func TestConnectionCreateReplaySurvivesWorkspaceArchive(t *testing.T) {
	ctx := context.Background()
	storage, err := sqlitestore.Open(ctx, "file:"+filepath.Join(t.TempDir(), "mercator.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if _, err := storage.Workspaces().Create(ctx, workspace.Create{
		ID:          "ws_replay",
		DisplayName: "Replay workspace",
		CreatedAt:   time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		CreatedBy:   "test:storage",
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	service := connection.New(storage.EventLog())
	request := connection.CreateRequest{
		WorkspaceID:  "ws_replay",
		ConnectionID: "conn_replayed",
		AdapterType:  "runpod",
	}
	if _, err := service.Create(ctx, request); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if _, err := storage.Workspaces().Archive(ctx, "ws_replay", time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("archive workspace: %v", err)
	}

	replayed, err := service.Create(ctx, request)

	if err != nil {
		t.Fatalf("replay connection create: %v", err)
	}
	if replayed.ID != request.ConnectionID {
		t.Fatalf("replayed connection id = %q, want %q", replayed.ID, request.ConnectionID)
	}
}

func TestWorkloadRevisionReplaySurvivesWorkspaceArchive(t *testing.T) {
	ctx := context.Background()
	storage, err := sqlitestore.Open(ctx, "file:"+filepath.Join(t.TempDir(), "mercator.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if _, err := storage.Workspaces().Create(ctx, workspace.Create{
		ID:          "ws_revision_replay",
		DisplayName: "Revision replay workspace",
		CreatedAt:   time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		CreatedBy:   "test:storage",
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	service := workload.New(storage.EventLog())
	if err := service.CreateWorkload(ctx, workload.CreateWorkloadRequest{
		WorkspaceID: "ws_revision_replay",
		WorkloadID:  "wrk_replayed",
		Name:        "Replayed workload",
	}); err != nil {
		t.Fatalf("create workload: %v", err)
	}
	request := workload.CreateRevisionRequest{
		WorkspaceID: "ws_revision_replay",
		WorkloadID:  "wrk_replayed",
		Revision:    replayRevision(t),
	}
	if _, err := service.CreateRevision(ctx, request); err != nil {
		t.Fatalf("create revision: %v", err)
	}
	if _, err := storage.Workspaces().Archive(ctx, "ws_revision_replay", time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("archive workspace: %v", err)
	}

	replayed, err := service.CreateRevision(ctx, request)

	if err != nil {
		t.Fatalf("replay revision create: %v", err)
	}
	if replayed.ID != request.Revision.ID {
		t.Fatalf("replayed revision id = %q, want %q", replayed.ID, request.Revision.ID)
	}
}

func replayRevision(t *testing.T) domain.WorkloadRevision {
	t.Helper()
	contents, err := os.ReadFile("testdata/replay_revision.json")
	if err != nil {
		t.Fatalf("read replay revision fixture: %v", err)
	}
	var revision domain.WorkloadRevision
	if err := json.Unmarshal(contents, &revision); err != nil {
		t.Fatalf("decode replay revision fixture: %v", err)
	}
	return revision
}

func TestWorkspaceArchiveWaitsForInFlightConnectionCreate(t *testing.T) {
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
	if _, err := storage.Workspaces().Create(ctx, workspace.Create{
		ID:          "ws_race",
		DisplayName: "Race workspace",
		CreatedAt:   time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		CreatedBy:   "test:storage",
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	sealer := &blockingSealer{entered: make(chan struct{}), release: make(chan struct{})}
	repository, err := storage.Connections(sealer)
	if err != nil {
		t.Fatalf("open connection repository: %v", err)
	}
	service := connection.NewWithCredentials(repository)
	createDone := make(chan error, 1)
	go func() {
		_, err := service.Create(ctx, connection.CreateRequest{
			WorkspaceID:  "ws_race",
			ConnectionID: "conn_in_flight",
			AdapterType:  "runpod",
			Credential:   credential.Credential{Source: credential.SourceMercator},
			Secret:       []byte("secret"),
		})
		createDone <- err
	}()
	<-sealer.entered

	waitCount := db.Stats().WaitCount
	archiveDone := make(chan error, 1)
	go func() {
		_, err := storage.Workspaces().Archive(ctx, "ws_race", time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC))
		archiveDone <- err
	}()
	waitForDatabaseWaiter(t, db, waitCount)
	select {
	case err := <-archiveDone:
		t.Fatalf("archive completed inside create transaction: %v", err)
	default:
	}

	close(sealer.release)
	if err := <-createDone; err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := <-archiveDone; err != nil {
		t.Fatalf("archive workspace: %v", err)
	}
	_, err = service.Create(ctx, connection.CreateRequest{
		WorkspaceID:  "ws_race",
		ConnectionID: "conn_after_archive",
		AdapterType:  "runpod",
	})
	if !errors.Is(err, workspace.ErrArchived) {
		t.Fatalf("create after archive error = %v, want %v", err, workspace.ErrArchived)
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
	appendLifecycleEvent(t, log, "ws_before_storage")
	storage, err := sqlitestore.New(ctx, db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	appendLifecycleEvent(t, log, "ws_after_storage")
}

func appendLifecycleEvent(t *testing.T, log eventlog.EventLog, workspaceID string) {
	t.Helper()
	_, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: workspaceID, Type: "run", ID: "run_existing"},
		ExpectedStreamVersion: 0,
		CommandKey:            "run:refresh:" + workspaceID,
		RequestHash:           "sha256:" + workspaceID,
		Events: []eventlog.NewEvent{{
			ID:            "evt_" + workspaceID,
			Type:          "compute.run.external_state_observed.v1",
			SchemaVersion: 1,
		}},
	})
	if err != nil {
		t.Fatalf("append lifecycle event in %s: %v", workspaceID, err)
	}
}

type blockingSealer struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockingSealer) Seal(secret []byte) ([]byte, bool) {
	close(s.entered)
	<-s.release
	return secret, true
}

func waitForDatabaseWaiter(t *testing.T, db *sql.DB, after int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for db.Stats().WaitCount == after {
		if time.Now().After(deadline) {
			t.Fatal("archive did not wait for the create transaction")
		}
		runtime.Gosched()
	}
}
