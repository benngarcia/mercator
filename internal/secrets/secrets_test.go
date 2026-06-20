package secrets

import (
	"bytes"
	"context"
	"testing"

	"github.com/bengarcia/mercator/internal/eventlog"
)

func TestVaultCreatesEncryptedSecretVersionsAndMetadataOnlyReads(t *testing.T) {
	ctx := context.Background()
	log := openSecretsTestLog(t)
	vault := New(log, bytes.Repeat([]byte{7}, 32))

	version, err := vault.CreateVersion(ctx, CreateVersionRequest{
		WorkspaceID: "ws_1",
		SecretID:    "sec_api",
		Plaintext:   []byte("super-secret-value"),
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
	metadata, err := vault.ListMetadata(ctx, "ws_1")
	if err != nil {
		t.Fatalf("list metadata: %v", err)
	}
	if len(metadata) != 1 || metadata[0].SecretID != "sec_api" || metadata[0].Version != 1 {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
}

func TestVaultGrantsAndRevokesSecretScopes(t *testing.T) {
	ctx := context.Background()
	vault := New(openSecretsTestLog(t), bytes.Repeat([]byte{9}, 32))
	if _, err := vault.CreateVersion(ctx, CreateVersionRequest{WorkspaceID: "ws_1", SecretID: "sec_api", Plaintext: []byte("secret")}); err != nil {
		t.Fatalf("create version: %v", err)
	}
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
