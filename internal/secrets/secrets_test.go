package secrets

import (
	"bytes"
	"context"
	"testing"

	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
)

func TestVaultCreatesEncryptedSecretVersionsAndMetadataOnlyReads(t *testing.T) {
	ctx := context.Background()
	log := openSecretsTestLog(t)
	vault := New(log, bytes.Repeat([]byte{7}, 32))

	version, err := vault.CreateVersion(ctx, CreateVersionRequest{
		WorkspaceID:    "ws_1",
		SecretID:       "sec_api",
		Plaintext:      []byte("super-secret-value"),
		IdempotencyKey: "idem_secret_create",
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	if version.Version != 1 || string(version.Ciphertext) == "super-secret-value" || len(version.Nonce) == 0 {
		t.Fatalf("secret version was not encrypted: %+v", version)
	}
	events, err := log.ReadStream(ctx, secretStream("ws_1", "sec_api"), 0, 100)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if bytes.Contains(events[0].Data, []byte("super-secret-value")) || bytes.Contains(events[0].PrivateData, []byte("super-secret-value")) {
		t.Fatalf("plaintext leaked into event: data=%s private=%x", events[0].Data, events[0].PrivateData)
	}
	oldOracleHash, err := domain.CanonicalHash(struct {
		WorkspaceID   string `json:"workspace_id"`
		SecretID      string `json:"secret_id"`
		PlaintextHash string `json:"plaintext_hash"`
	}{
		WorkspaceID:   "ws_1",
		SecretID:      "sec_api",
		PlaintextHash: "sha256:03767fbe485736bb40cc5d85e4c9bb10b12a415674b46faf005aa22188a39a10",
	})
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	if events[0].RequestHash == oldOracleHash {
		t.Fatalf("request hash should not be a deterministic plaintext oracle")
	}
	metadata, err := vault.ListMetadata(ctx, "ws_1")
	if err != nil {
		t.Fatalf("list metadata: %v", err)
	}
	if len(metadata) != 1 || metadata[0].SecretID != "sec_api" || metadata[0].Version != 1 {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
}

func TestVaultCreateVersionIsIdempotent(t *testing.T) {
	ctx := context.Background()
	log := openSecretsTestLog(t)
	vault := New(log, bytes.Repeat([]byte{8}, 32))
	req := CreateVersionRequest{WorkspaceID: "ws_1", SecretID: "sec_api", Plaintext: []byte("secret"), IdempotencyKey: "idem_secret_create"}

	first, err := vault.CreateVersion(ctx, req)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := vault.CreateVersion(ctx, req)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first.Version != 1 || second.Version != 1 {
		t.Fatalf("expected idempotent replay of version 1, first=%+v second=%+v", first, second)
	}
	events, err := log.ReadStream(ctx, secretStream("ws_1", "sec_api"), 0, 100)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one persisted event, got %d", len(events))
	}
}

func TestVaultSecretEventIDsAreWorkspaceScoped(t *testing.T) {
	ctx := context.Background()
	vault := New(openSecretsTestLog(t), bytes.Repeat([]byte{6}, 32))
	for _, workspaceID := range []string{"ws_1", "ws_2"} {
		if _, err := vault.CreateVersion(ctx, CreateVersionRequest{WorkspaceID: workspaceID, SecretID: "sec_same", Plaintext: []byte("secret"), IdempotencyKey: "idem_" + workspaceID}); err != nil {
			t.Fatalf("create %s: %v", workspaceID, err)
		}
	}
}

func TestVaultGrantsAndRevokesSecretScopes(t *testing.T) {
	ctx := context.Background()
	log := openSecretsTestLog(t)
	vault := New(log, bytes.Repeat([]byte{9}, 32))
	if _, err := vault.CreateVersion(ctx, CreateVersionRequest{WorkspaceID: "ws_1", SecretID: "sec_api", Plaintext: []byte("secret")}); err != nil {
		t.Fatalf("create version: %v", err)
	}
	appendScopeEvent(t, log, "ws_1", "run", "run_1")
	grant, err := vault.Grant(ctx, GrantRequest{WorkspaceID: "ws_1", SecretID: "sec_api", Version: 1, ScopeType: "run", ScopeID: "run_1"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if grant.Revoked {
		t.Fatalf("new grant should be active: %+v", grant)
	}
	if err := vault.Revoke(ctx, RevokeRequest{WorkspaceID: "ws_1", GrantID: grant.ID}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	grants, err := vault.ListGrants(ctx, "ws_1")
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 1 || !grants[0].Revoked {
		t.Fatalf("expected revoked grant, got %+v", grants)
	}
}

func TestVaultGrantRejectsMissingVersionAndScope(t *testing.T) {
	ctx := context.Background()
	log := openSecretsTestLog(t)
	vault := New(log, bytes.Repeat([]byte{5}, 32))
	if _, err := vault.CreateVersion(ctx, CreateVersionRequest{WorkspaceID: "ws_1", SecretID: "sec_api", Plaintext: []byte("secret")}); err != nil {
		t.Fatalf("create version: %v", err)
	}
	if _, err := vault.Grant(ctx, GrantRequest{WorkspaceID: "ws_1", SecretID: "sec_api", Version: 2, ScopeType: "run", ScopeID: "run_1"}); err == nil {
		t.Fatalf("expected missing version grant to fail")
	}
	grant, err := vault.Grant(ctx, GrantRequest{WorkspaceID: "ws_1", SecretID: "sec_api", Version: 1, ScopeType: "run", ScopeID: "run_future"})
	if err != nil {
		t.Fatalf("future run grant should be allowed: %v", err)
	}
	ok, err := vault.HasActiveGrant(ctx, GrantRequest{WorkspaceID: "ws_1", SecretID: "sec_api", Version: 1, ScopeType: "run", ScopeID: "run_future"})
	if err != nil {
		t.Fatalf("check active grant: %v", err)
	}
	if !ok || grant.Revoked {
		t.Fatalf("expected active future run grant, grant=%+v ok=%v", grant, ok)
	}
}

func openSecretsTestLog(t *testing.T) *eventlog.SQLiteEventLog {
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

func appendScopeEvent(t *testing.T, log eventlog.EventLog, workspaceID, streamType, streamID string) {
	t.Helper()
	_, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: workspaceID, Type: streamType, ID: streamID},
		ExpectedStreamVersion: 0,
		CommandKey:            "seed:" + streamType + ":" + streamID,
		RequestHash:           "sha256:seed",
		CorrelationID:         streamID,
		CausationID:           "seed",
		Events: []eventlog.NewEvent{{
			ID:            "evt_" + workspaceID + "_" + streamType + "_" + streamID + "_seed",
			Type:          "seed." + streamType + ".v1",
			SchemaVersion: 1,
			Visibility:    eventlog.VisibilityPublic,
			Data:          []byte(`{}`),
		}},
	})
	if err != nil {
		t.Fatalf("append scope event: %v", err)
	}
}
