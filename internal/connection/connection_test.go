package connection

import (
	"context"
	"testing"

	"github.com/bengarcia/mercator/internal/eventlog"
)

func TestServiceCreatesGetsListsAndUpdatesConnectionAuthorization(t *testing.T) {
	ctx := context.Background()
	svc := New(openConnectionTestLog(t))
	created, err := svc.Create(ctx, CreateRequest{
		WorkspaceID:         "ws_1",
		ConnectionID:        "conn_1",
		AdapterType:         "fake",
		AuthorizationSchema: map[string]string{"token": "secret_ref"},
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if created.ID != "conn_1" || created.AdapterType != "fake" {
		t.Fatalf("unexpected created connection: %+v", created)
	}

	if err := svc.UpdateAuthorization(ctx, UpdateAuthorizationRequest{WorkspaceID: "ws_1", ConnectionID: "conn_1", Authorized: true}); err != nil {
		t.Fatalf("update authorization: %v", err)
	}
	got, err := svc.Get(ctx, "ws_1", "conn_1")
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	if !got.Authorized || got.AuthorizationSchema["token"] != "secret_ref" {
		t.Fatalf("unexpected connection after update: %+v", got)
	}
	list, err := svc.List(ctx, "ws_1")
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if len(list) != 1 || list[0].ID != "conn_1" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func openConnectionTestLog(t *testing.T) *eventlog.SQLiteEventLog {
	t.Helper()
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Fatalf("close event log: %v", err)
		}
	})
	return log
}
