package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/runprojection"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
)

func TestRunProjectionPaginatesByStableRunIdentity(t *testing.T) {
	ctx, storage, _ := openRunProjectionStorage(t)
	for _, runID := range []string{"run_c", "run_a", "run_b"} {
		record := domain.RunRecord{ID: runID, Phase: "requested"}
		if _, err := storage.Runs().Append(ctx, runProjectionAppend(runID), record); err != nil {
			t.Fatalf("append %s: %v", runID, err)
		}
	}

	first, err := storage.Runs().List(ctx, runprojection.PageRequest{Limit: 2})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first.Records) != 2 || first.Records[0].ID != "run_a" || first.Records[1].ID != "run_b" {
		t.Fatalf("first page = %+v, want run_a and run_b", first.Records)
	}
	if first.NextCursor != "run_b" {
		t.Fatalf("next cursor = %q, want run_b", first.NextCursor)
	}

	second, err := storage.Runs().List(ctx, runprojection.PageRequest{After: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second.Records) != 1 || second.Records[0].ID != "run_c" || second.NextCursor != "" {
		t.Fatalf("second page = %+v cursor %q, want terminal run_c page", second.Records, second.NextCursor)
	}
}

func TestRunProjectionListsOnlyOpenRuns(t *testing.T) {
	ctx, storage, _ := openRunProjectionStorage(t)
	for _, record := range []domain.RunRecord{
		{ID: "run_open", Phase: "running"},
		{ID: "run_closed", Phase: "closed", Closed: true},
	} {
		if _, err := storage.Runs().Append(ctx, runProjectionAppend(record.ID), record); err != nil {
			t.Fatalf("append %s: %v", record.ID, err)
		}
	}

	runIDs, err := storage.Runs().ListOpenIDs(ctx)
	if err != nil {
		t.Fatalf("list open Runs: %v", err)
	}
	if len(runIDs) != 1 || runIDs[0] != "run_open" {
		t.Fatalf("open Runs = %v, want run_open", runIDs)
	}
}

func TestRunProjectionAndEventsRollBackTogether(t *testing.T) {
	ctx, storage, db := openRunProjectionStorage(t)
	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER reject_run_projection
		BEFORE INSERT ON runs
		BEGIN
			SELECT RAISE(ABORT, 'projection rejected');
		END
	`); err != nil {
		t.Fatalf("create rejecting trigger: %v", err)
	}

	_, err := storage.Runs().Append(
		ctx,
		runProjectionAppend("run_rejected"),
		domain.RunRecord{ID: "run_rejected", Phase: "requested"},
	)
	if err == nil {
		t.Fatal("append succeeded despite rejected projection")
	}

	events, readErr := storage.EventLog().ReadStream(ctx, eventlog.StreamKey{

		Type: "run",
		ID:   "run_rejected",
	}, 0, 10)
	if readErr != nil {
		t.Fatalf("read rejected Run stream: %v", readErr)
	}
	if len(events) != 0 {
		t.Fatalf("rolled-back append left %d events", len(events))
	}
}

func openRunProjectionStorage(t *testing.T) (context.Context, *sqlitestore.Storage, *sql.DB) {
	t.Helper()
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
	return ctx, storage, db
}

func runProjectionAppend(runID string) eventlog.AppendRequest {
	return eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            "create:" + runID,
		RequestHash:           "sha256:" + runID,
		Events: []eventlog.NewEvent{{
			ID:            "evt_" + runID,
			Type:          "compute.run.requested.v1",
			SchemaVersion: 1,
			Data:          []byte(`{}`),
		}},
	}
}
