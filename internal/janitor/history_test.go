package janitor

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/eventlog"
)

func TestJanitorSeesCleanupRequestedBeyondOneStreamPage(t *testing.T) {
	ctx := context.Background()
	provider := fake.New()
	_, err := provider.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_history",
		RequestHash:        "sha256:history",
		WorkspaceID:        "ws_1",
		RunID:              "run_history",
		AttemptID:          "att_history",
		OwnershipToken:     "own_history",
		LaunchKey:          "launch_history",
		CleanupLocator:     "cleanup_history",
		WorkloadID:         "wrk_history",
		WorkloadRevisionID: "wrev_history",
	})
	if err != nil {
		t.Fatalf("seed owned object: %v", err)
	}

	log := openJanitorTestLog(t)
	events := make([]eventlog.NewEvent, 1001)
	for i := 0; i < 1000; i++ {
		events[i] = eventlog.NewEvent{
			ID:            fmt.Sprintf("evt_run_history_%04d", i+1),
			Type:          "compute.run.requested.v1",
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Data:          json.RawMessage(`{}`),
		}
	}
	events[1000] = eventlog.NewEvent{
		ID:            "evt_run_history_cleanup_requested",
		Type:          "compute.run.cleanup_requested.v1",
		SchemaVersion: 1,
		OccurredAt:    time.Now().UTC(),
		Data:          json.RawMessage(`{}`),
	}
	if _, err := log.Append(ctx, eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: "ws_1", Type: "run", ID: "run_history"},
		ExpectedStreamVersion: 0,
		CommandKey:            "seed:janitor-history",
		RequestHash:           "sha256:janitor-history",
		Events:                events,
	}); err != nil {
		t.Fatalf("append run history: %v", err)
	}

	result, err := New(provider, WithEventLog(log)).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Released != 1 {
		t.Fatalf("sweep result = %+v, want one released object", result)
	}
}
