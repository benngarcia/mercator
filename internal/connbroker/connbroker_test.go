package connbroker

import (
	"context"
	"testing"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/eventlog"
)

func TestListMapsRecordsToConnRefs(t *testing.T) {
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil { t.Fatalf("open: %v", err) }
	t.Cleanup(func() { _ = log.Close() })
	svc := connection.New(log)
	if _, err := svc.Create(context.Background(), connection.CreateRequest{
		WorkspaceID: "ws_1", ConnectionID: "conn_a", AdapterType: "docker",
		Config: map[string]string{"host": "loopback"},
		Credential: credential.Credential{Source: "env", Ref: "K"},
	}); err != nil { t.Fatalf("create: %v", err) }

	refs, err := New(svc).List(context.Background(), "ws_1")
	if err != nil { t.Fatalf("list: %v", err) }
	if len(refs) != 1 || refs[0].ID != "conn_a" || refs[0].AdapterType != "docker" ||
		refs[0].Config["host"] != "loopback" || refs[0].Credential.Ref != "K" {
		t.Fatalf("unexpected ref mapping: %+v", refs)
	}
}
