package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workspace"
)

func TestOpenPurgesCredentialsForDeletedConnections(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	masterKey := []byte("0123456789abcdef0123456789abcdef")
	storage, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	if _, err := storage.Workspaces().Create(ctx, workspace.Create{
		ID:          "ws_1",
		DisplayName: "Test workspace",
		CreatedAt:   time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		CreatedBy:   "test:storage",
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	resolver := credential.NewResolver(nil, storage.CredentialStore(), masterKey)
	connections, err := storage.Connections(resolver)
	if err != nil {
		t.Fatalf("open connection storage: %v", err)
	}
	service := connection.NewWithCredentials(connections, storage.Workspaces())
	if _, err := service.Create(ctx, connection.CreateRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_deleted",
		AdapterType:  "runpod",
		Credential:   credential.Credential{Source: credential.SourceMercator},
		Secret:       []byte("original-secret"),
	}); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if _, err := storage.Workspaces().Archive(ctx, "ws_1", time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("archive workspace: %v", err)
	}
	if err := service.Delete(ctx, connection.DeleteRequest{WorkspaceID: "ws_1", ConnectionID: "conn_deleted"}); err != nil {
		t.Fatalf("delete connection in archived workspace: %v", err)
	}
	orphaned, err := credential.Seal(credential.DeriveSealKey(masterKey), []byte("orphaned-secret"))
	if err != nil {
		t.Fatalf("seal orphaned credential: %v", err)
	}
	if err := storage.CredentialStore().Put(ctx, "ws_1", "conn_deleted", orphaned); err != nil {
		t.Fatalf("arrange orphaned credential: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("close storage: %v", err)
	}

	reopened, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("reopen storage: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	_, err = reopened.CredentialStore().Get(ctx, "ws_1", "conn_deleted")
	if !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("orphaned credential lookup error = %v, want credential.ErrNotFound", err)
	}
}
