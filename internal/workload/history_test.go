package workload

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/eventlog"
)

func TestServiceSeesWorkloadRevisionBeyondOneStreamPage(t *testing.T) {
	ctx := context.Background()
	log := openWorkloadTestLog(t)
	svc := New(log)
	if err := svc.CreateWorkload(ctx, CreateWorkloadRequest{WorkspaceID: "ws_1", WorkloadID: "wrk_history", Name: "history"}); err != nil {
		t.Fatalf("create workload: %v", err)
	}

	events := make([]eventlog.NewEvent, 1000)
	for i := range events {
		revision := validRevision()
		revision.ID = fmt.Sprintf("wrev_%04d", i+1)
		revision.WorkloadID = "wrk_history"
		data, err := json.Marshal(revisionCreatedData{Revision: revision})
		if err != nil {
			t.Fatalf("marshal revision %d: %v", i+1, err)
		}
		events[i] = eventlog.NewEvent{
			ID:            fmt.Sprintf("evt_workload_history_%04d", i+1),
			Type:          EventWorkloadRevisionCreated,
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Data:          data,
		}
	}
	if _, err := log.Append(ctx, eventlog.AppendRequest{
		Stream:                workloadStream("ws_1", "wrk_history"),
		ExpectedStreamVersion: 1,
		CommandKey:            "seed:workload-history",
		RequestHash:           "sha256:workload-history",
		Events:                events,
	}); err != nil {
		t.Fatalf("append workload history: %v", err)
	}

	revisions, err := svc.ListRevisions(ctx, "ws_1", "wrk_history")
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revisions) != 1000 {
		t.Fatalf("listed %d revisions, want 1000", len(revisions))
	}
	if revisions[999].ID != "wrev_1000" {
		t.Fatalf("last revision = %q, want wrev_1000", revisions[999].ID)
	}

	duplicate := validRevision()
	duplicate.ID = "wrev_1000"
	_, err = svc.CreateRevision(ctx, CreateRevisionRequest{WorkspaceID: "ws_1", WorkloadID: "wrk_history", Revision: duplicate})
	if err == nil || !strings.Contains(err.Error(), "revision already exists") {
		t.Fatalf("create duplicate revision error = %v, want revision already exists", err)
	}
}
