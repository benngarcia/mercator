package connection

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/eventlog"
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

func TestCreateRoundTripsConfigAndCredential(t *testing.T) {
	svc := New(openConnectionTestLog(t))
	_, err := svc.Create(context.Background(), CreateRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_rp",
		AdapterType:  "runpod",
		Config:       map[string]string{"region": "us"},
		Credential:   credential.Credential{Source: "mercator", Ref: "conn_rp"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := svc.Get(context.Background(), "ws_1", "conn_rp")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Config["region"] != "us" {
		t.Errorf("config not round-tripped: %+v", got.Config)
	}
	if got.Credential.Source != "mercator" || got.Credential.Ref != "conn_rp" {
		t.Errorf("credential not round-tripped: %+v", got.Credential)
	}
}

func TestSameConnectionIDCanExistInMultipleWorkspaces(t *testing.T) {
	ctx := context.Background()
	svc := New(openConnectionTestLog(t))

	for _, workspaceID := range []string{"ws_1", "bucket-worktree"} {
		if _, err := svc.Create(ctx, CreateRequest{
			WorkspaceID:  workspaceID,
			ConnectionID: "conn_docker_loopback",
			AdapterType:  "docker",
		}); err != nil {
			t.Fatalf("create %s: %v", workspaceID, err)
		}
		if err := svc.UpdateAuthorization(ctx, UpdateAuthorizationRequest{
			WorkspaceID:  workspaceID,
			ConnectionID: "conn_docker_loopback",
			Authorized:   true,
		}); err != nil {
			t.Fatalf("authorize %s: %v", workspaceID, err)
		}
	}

	for _, workspaceID := range []string{"ws_1", "bucket-worktree"} {
		got, err := svc.Get(ctx, workspaceID, "conn_docker_loopback")
		if err != nil {
			t.Fatalf("get %s: %v", workspaceID, err)
		}
		if !got.Authorized {
			t.Fatalf("connection in %s was not authorized: %+v", workspaceID, got)
		}
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

// Connection create/authorize commands record the acting principal on the
// event envelope and surface it on the reduced record, without disturbing the
// idempotency hashes of actorless (bootstrap) commands.
func TestConnectionSurfacesActingPrincipals(t *testing.T) {
	ctx := context.Background()
	svc := New(openConnectionTestLog(t))

	if _, err := svc.Create(ctx, CreateRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_audit",
		AdapterType:  "docker",
		Actor:        json.RawMessage(`{"subject":"operator@example.com"}`),
	}); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := svc.UpdateAuthorization(ctx, UpdateAuthorizationRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_audit",
		Authorized:   true,
		Actor:        json.RawMessage(`{"subject":"admin@example.com"}`),
	}); err != nil {
		t.Fatalf("authorize connection: %v", err)
	}

	record, err := svc.Get(ctx, "ws_1", "conn_audit")
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	if record.CreatedBy != "operator@example.com" {
		t.Fatalf("expected created_by=operator@example.com, got %q", record.CreatedBy)
	}
	if record.AuthorizedBy != "admin@example.com" {
		t.Fatalf("expected authorized_by=admin@example.com, got %q", record.AuthorizedBy)
	}
}

// A boot-time (actorless) authorization followed by the same authorization
// from a signed-in principal must replay idempotently: WHO issued the command
// is not part of WHAT was commanded.
func TestAuthorizationIdempotencyIgnoresActor(t *testing.T) {
	ctx := context.Background()
	svc := New(openConnectionTestLog(t))

	if _, err := svc.Create(ctx, CreateRequest{WorkspaceID: "ws_1", ConnectionID: "conn_boot", AdapterType: "docker"}); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := svc.UpdateAuthorization(ctx, UpdateAuthorizationRequest{WorkspaceID: "ws_1", ConnectionID: "conn_boot", Authorized: true}); err != nil {
		t.Fatalf("bootstrap authorize: %v", err)
	}
	if err := svc.UpdateAuthorization(ctx, UpdateAuthorizationRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_boot",
		Authorized:   true,
		Actor:        json.RawMessage(`{"subject":"operator@example.com"}`),
	}); err != nil {
		t.Fatalf("re-authorize with a principal must replay idempotently: %v", err)
	}
}
