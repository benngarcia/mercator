package eventlog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/workspace"
)

func TestSQLiteAppendReportsUnknownWorkspace(t *testing.T) {
	ctx := context.Background()
	log, err := OpenSQLite(ctx, "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	if _, err := workspace.NewSQLiteCatalog(ctx, log.db); err != nil {
		t.Fatalf("open workspace catalog: %v", err)
	}

	_, err = log.Append(ctx, AppendRequest{
		Stream:                StreamKey{WorkspaceID: "ws_unknown", Type: "test", ID: "record_1"},
		ExpectedStreamVersion: 0,
		CommandKey:            "test:create:record_1",
		RequestHash:           "sha256:test",
		Events: []NewEvent{{
			ID:            "evt_test_record_1_created",
			Type:          "test.record.created.v1",
			SchemaVersion: 1,
			OccurredAt:    time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		}},
	})

	if !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("append error = %v, want %v", err, workspace.ErrNotFound)
	}
}
