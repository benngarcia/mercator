package janitor

import (
	"context"
	"testing"
	"time"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/adapter/fake"
	"github.com/bengarcia/mercator/internal/eventlog"
)

func TestJanitorReleasesOwnedResources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_orphan",
		RequestHash:        "sha256:orphan",
		WorkspaceID:        "ws_1",
		RunID:              "run_orphan",
		AttemptID:          "att_orphan",
		OwnershipToken:     "own_orphan",
		LaunchKey:          "launch_orphan",
		CleanupLocator:     "cleanup_orphan",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed orphan: %v", err)
	}

	log := openJanitorTestLog(t)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Found != 1 || result.Released != 1 {
		t.Fatalf("unexpected sweep result: %+v", result)
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("expected owned resources released, got %+v", owned)
	}
}

func TestJanitorSkipsActiveRunResources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_active",
		RequestHash:        "sha256:active",
		WorkspaceID:        "ws_1",
		RunID:              "run_active",
		AttemptID:          "att_active",
		OwnershipToken:     "own_active",
		LaunchKey:          "launch_active",
		CleanupLocator:     "cleanup_active",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed active object: %v", err)
	}
	log := openJanitorTestLog(t)
	appendRunEvent(t, log, "ws_1", "run_active", "compute.run.requested.v1")

	result, err := New(ad, WithEventLog(log)).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Found != 1 || result.Released != 0 {
		t.Fatalf("active resource should be found but not released: %+v", result)
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 1 {
		t.Fatalf("expected active resource to remain, got %+v", owned)
	}
}

func TestJanitorRequiresEventLog(t *testing.T) {
	t.Parallel()
	_, err := New(fake.New()).Sweep(context.Background(), "ws_1")
	if err == nil {
		t.Fatalf("expected missing event log error")
	}
}

func openJanitorTestLog(t *testing.T) *eventlog.SQLiteEventLog {
	t.Helper()
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func appendRunEvent(t *testing.T, log eventlog.EventLog, workspaceID, runID, eventType string) {
	t.Helper()
	_, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: workspaceID, Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            "seed:" + eventType,
		RequestHash:           "sha256:seed",
		CorrelationID:         runID,
		CausationID:           "seed",
		Events: []eventlog.NewEvent{{
			ID:            "evt_" + workspaceID + "_" + runID + "_seed",
			Type:          eventType,
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          []byte(`{}`),
		}},
	})
	if err != nil {
		t.Fatalf("append run event: %v", err)
	}
}
