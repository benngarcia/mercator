package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/eventlog"
)

func TestServiceListsMoreThanOnePageOfConnections(t *testing.T) {
	ctx := context.Background()
	svc := New(openConnectionTestLog(t))
	for i := 1; i <= 1001; i++ {
		_, err := svc.Create(ctx, CreateRequest{
			WorkspaceID:  "ws_1",
			ConnectionID: fmt.Sprintf("conn_%04d", i),
			AdapterType:  "docker",
		})
		if err != nil {
			t.Fatalf("create connection %d: %v", i, err)
		}
	}

	records, err := svc.List(ctx, "ws_1")
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if len(records) != 1001 {
		t.Fatalf("listed %d connections, want 1001", len(records))
	}
	if records[1000].ID != "conn_1001" {
		t.Fatalf("last connection = %q, want conn_1001", records[1000].ID)
	}
}

func TestServiceListsConnectionsWithoutReadingEveryStream(t *testing.T) {
	ctx := context.Background()
	log := &streamReadCountingLog{WorkspaceEventLog: openConnectionTestLog(t)}
	svc := New(log)
	for _, connectionID := range []string{"conn_1", "conn_2"} {
		if _, err := svc.Create(ctx, CreateRequest{WorkspaceID: "ws_1", ConnectionID: connectionID, AdapterType: "docker"}); err != nil {
			t.Fatalf("create %s: %v", connectionID, err)
		}
	}
	if err := svc.UpdateAuthorization(ctx, UpdateAuthorizationRequest{WorkspaceID: "ws_1", ConnectionID: "conn_1", Authorized: true}); err != nil {
		t.Fatalf("authorize conn_1: %v", err)
	}
	if err := svc.Delete(ctx, DeleteRequest{WorkspaceID: "ws_1", ConnectionID: "conn_2"}); err != nil {
		t.Fatalf("delete conn_2: %v", err)
	}
	log.streamReads = 0

	records, err := svc.List(ctx, "ws_1")
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if len(records) != 1 || records[0].ID != "conn_1" || !records[0].Authorized {
		t.Fatalf("listed connections = %+v, want authorized conn_1", records)
	}
	if log.streamReads != 0 {
		t.Fatalf("list read %d individual streams, want 0", log.streamReads)
	}
}

func TestServiceReadsAndUpdatesConnectionPastOneStreamPage(t *testing.T) {
	ctx := context.Background()
	log := openConnectionTestLog(t)
	svc := New(log)
	if _, err := svc.Create(ctx, CreateRequest{WorkspaceID: "ws_1", ConnectionID: "conn_history", AdapterType: "docker"}); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	events := make([]eventlog.NewEvent, 1000)
	for i := range events {
		events[i] = eventlog.NewEvent{
			ID:            fmt.Sprintf("evt_conn_history_%04d", i+1),
			Type:          EventConnectionAuthorizationUpdated,
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Data:          json.RawMessage(`{"authorized":false}`),
		}
	}
	if _, err := log.Append(ctx, eventlog.AppendRequest{
		Stream:                connectionStream("ws_1", "conn_history"),
		ExpectedStreamVersion: 1,
		CommandKey:            "seed:connection-history",
		RequestHash:           "sha256:connection-history",
		Events:                events,
	}); err != nil {
		t.Fatalf("append connection history: %v", err)
	}

	if _, err := svc.Get(ctx, "ws_1", "conn_history"); err != nil {
		t.Fatalf("get connection past one page: %v", err)
	}
	if err := svc.UpdateAuthorization(ctx, UpdateAuthorizationRequest{WorkspaceID: "ws_1", ConnectionID: "conn_history", Authorized: true}); err != nil {
		t.Fatalf("update connection past one page: %v", err)
	}
}

type streamReadCountingLog struct {
	eventlog.WorkspaceEventLog
	streamReads int
}

func (l *streamReadCountingLog) ReadStream(ctx context.Context, stream eventlog.StreamKey, afterVersion uint64, limit int) ([]eventlog.StoredEvent, error) {
	l.streamReads++
	return l.WorkspaceEventLog.ReadStream(ctx, stream, afterVersion, limit)
}
