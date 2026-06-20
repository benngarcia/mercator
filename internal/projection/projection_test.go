package projection

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bengarcia/mercator/internal/eventlog"
)

func TestRunnerResumesFromDurableGlobalPosition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := openProjectionLog(t)
	appendProjectionEvents(t, log, "run_projection_1", "cmd-projection-1", []string{"evt_projection_1", "evt_projection_2"})

	var first []string
	runner := Runner{
		Log:          log,
		ProjectionID: "runs_projection",
		Handler: func(_ context.Context, event eventlog.StoredEvent) error {
			first = append(first, event.ID)
			return nil
		},
	}
	result, err := runner.RunOnce(ctx)
	if err != nil {
		t.Fatalf("run projection: %v", err)
	}
	if result.Processed != 2 || result.LastPosition != 2 {
		t.Fatalf("unexpected first result: %+v", result)
	}

	appendProjectionEvents(t, log, "run_projection_2", "cmd-projection-2", []string{"evt_projection_3"})
	var resumed []string
	runner.Handler = func(_ context.Context, event eventlog.StoredEvent) error {
		resumed = append(resumed, event.ID)
		return nil
	}
	result, err = runner.RunOnce(ctx)
	if err != nil {
		t.Fatalf("resume projection: %v", err)
	}
	if result.Processed != 1 || result.LastPosition != 3 {
		t.Fatalf("unexpected resume result: %+v", result)
	}
	if len(resumed) != 1 || resumed[0] != "evt_projection_3" {
		t.Fatalf("projection did not resume from durable offset: %v", resumed)
	}
}

func TestDisposableRunnerRebuildsFromBeginning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log := openProjectionLog(t)
	appendProjectionEvents(t, log, "run_rebuild_1", "cmd-rebuild-1", []string{"evt_rebuild_1", "evt_rebuild_2"})
	if err := log.Ack(ctx, "runs_projection", 2); err != nil {
		t.Fatalf("ack projection offset: %v", err)
	}

	var rebuilt []string
	result, err := Runner{
		Log:          log,
		ProjectionID: "runs_projection",
		Disposable:   true,
		Handler: func(_ context.Context, event eventlog.StoredEvent) error {
			rebuilt = append(rebuilt, event.ID)
			return nil
		},
	}.RunOnce(ctx)
	if err != nil {
		t.Fatalf("rebuild projection: %v", err)
	}
	if result.Processed != 2 || result.LastPosition != 2 {
		t.Fatalf("unexpected rebuild result: %+v", result)
	}
	if len(rebuilt) != 2 || rebuilt[0] != "evt_rebuild_1" || rebuilt[1] != "evt_rebuild_2" {
		t.Fatalf("disposable projection did not rebuild from the beginning: %v", rebuilt)
	}
}

func openProjectionLog(t *testing.T) *eventlog.SQLiteEventLog {
	t.Helper()
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
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

func appendProjectionEvents(t *testing.T, log *eventlog.SQLiteEventLog, runID, commandKey string, eventIDs []string) {
	t.Helper()
	events := make([]eventlog.NewEvent, 0, len(eventIDs))
	for _, eventID := range eventIDs {
		events = append(events, eventlog.NewEvent{
			ID:            eventID,
			Type:          "compute.run.requested.v1",
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          json.RawMessage(`{"run_id":"` + runID + `"}`),
		})
	}
	if _, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: "ws_1", Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            commandKey,
		RequestHash:           "sha256:" + commandKey,
		Events:                events,
	}); err != nil {
		t.Fatalf("append projection events: %v", err)
	}
}
