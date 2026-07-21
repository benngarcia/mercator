package connection

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

type workspaceTestLog struct {
	eventlog.EventLog
}

func (l workspaceTestLog) AppendNew(ctx context.Context, request eventlog.AppendRequest) (eventlog.AppendResult, error) {
	return l.Append(ctx, request)
}

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

func TestCreateReplaysConnectionsAcrossCredentialPresentationChange(t *testing.T) {
	for _, fixture := range []string{
		"v0_3_docker_connection_created.json",
		"v0_4_docker_connection_created.json",
	} {
		t.Run(fixture, func(t *testing.T) {
			ctx := context.Background()
			log := arrangeConnectionCreateFixture(t, fixture)

			_, err := New(log).Create(ctx, CreateRequest{
				WorkspaceID:  "staging",
				ConnectionID: "conn_docker_loopback",
				AdapterType:  "docker",
				Config:       map[string]string{"bin": "", "context": "", "host": ""},
			})

			if err != nil {
				t.Fatalf("replay %s connection: %v", fixture, err)
			}
			events, err := log.ReadStream(ctx, connectionStream("staging", "conn_docker_loopback"), 0, 10)
			if err != nil {
				t.Fatalf("read replayed connection: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("replay appended %d events, want the original event only", len(events))
			}
		})
	}
}

func TestCreateRejectsDifferentLegacyConnectionConfig(t *testing.T) {
	log := arrangeConnectionCreateFixture(t, "v0_3_docker_connection_created.json")

	_, err := New(log).Create(context.Background(), CreateRequest{
		WorkspaceID:  "staging",
		ConnectionID: "conn_docker_loopback",
		AdapterType:  "docker",
		Config:       map[string]string{"host": "ssh://different-host"},
	})

	if !errors.Is(err, eventlog.ErrIdempotencyConflict) {
		t.Fatalf("different config error = %v, want idempotency conflict", err)
	}
}

func arrangeConnectionCreateFixture(t *testing.T, fixture string) eventlog.WorkspaceEventLog {
	t.Helper()
	createdEvent, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		t.Fatalf("read connection fixture: %v", err)
	}
	requestHash, err := domain.CanonicalHash(json.RawMessage(createdEvent))
	if err != nil {
		t.Fatalf("hash connection fixture: %v", err)
	}
	log := openConnectionTestLog(t)
	_, err = log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                connectionStream("staging", "conn_docker_loopback"),
		ExpectedStreamVersion: 0,
		CommandKey:            "connection:create:conn_docker_loopback",
		RequestHash:           requestHash,
		Events: []eventlog.NewEvent{{
			ID:            "evt_connection_staging_conn_docker_loopback_created",
			Type:          EventConnectionCreated,
			SchemaVersion: 1,
			Data:          createdEvent,
		}},
	})
	if err != nil {
		t.Fatalf("arrange connection fixture: %v", err)
	}
	return log
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

func openConnectionTestLog(t *testing.T) eventlog.WorkspaceEventLog {
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
	return workspaceTestLog{EventLog: log}
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

// Delete makes the connection invisible to Get and List while the stream
// stays in the append-only log; a second delete is an idempotent no-op, and a
// boot-style Create replay (same command key and hash) must NOT resurrect it.
func TestDeleteHidesConnectionAndSurvivesBootReplay(t *testing.T) {
	ctx := context.Background()
	svc := New(openConnectionTestLog(t))

	if _, err := svc.Create(ctx, CreateRequest{WorkspaceID: "ws_1", ConnectionID: "conn_del", AdapterType: "docker"}); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := svc.UpdateAuthorization(ctx, UpdateAuthorizationRequest{WorkspaceID: "ws_1", ConnectionID: "conn_del", Authorized: true}); err != nil {
		t.Fatalf("authorize: %v", err)
	}

	if err := svc.Delete(ctx, DeleteRequest{WorkspaceID: "ws_1", ConnectionID: "conn_del", Actor: json.RawMessage(`{"subject":"operator@example.com"}`)}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.Get(ctx, "ws_1", "conn_del"); err != ErrNotFound {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
	records, err := svc.List(ctx, "ws_1")
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("list after delete: expected 0 records, got %d", len(records))
	}

	// Idempotent re-delete.
	if err := svc.Delete(ctx, DeleteRequest{WorkspaceID: "ws_1", ConnectionID: "conn_del"}); err != nil {
		t.Fatalf("re-delete must be a no-op: %v", err)
	}

	// Boot replay: identical Create + authorize commands replay by command key
	// without appending, so the deletion sticks across restarts.
	if _, err := svc.Create(ctx, CreateRequest{WorkspaceID: "ws_1", ConnectionID: "conn_del", AdapterType: "docker"}); err != nil {
		t.Fatalf("boot create replay: %v", err)
	}
	if err := svc.UpdateAuthorization(ctx, UpdateAuthorizationRequest{WorkspaceID: "ws_1", ConnectionID: "conn_del", Authorized: true}); err != nil {
		t.Fatalf("boot authorize replay: %v", err)
	}
	if _, err := svc.Get(ctx, "ws_1", "conn_del"); err != ErrNotFound {
		t.Fatalf("connection resurrected by boot replay: %v", err)
	}
}

// Deleting a connection that never existed is an error, not a silent no-op.
func TestDeleteUnknownConnectionFails(t *testing.T) {
	svc := New(openConnectionTestLog(t))
	if err := svc.Delete(context.Background(), DeleteRequest{WorkspaceID: "ws_1", ConnectionID: "conn_ghost"}); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
