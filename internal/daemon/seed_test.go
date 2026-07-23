package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
)

func reachable(context.Context) error   { return nil }
func unreachable(context.Context) error { return errors.New("docker daemon not running") }

func TestSeedDockerConnectionAuthorizesWhenReachable(t *testing.T) {
	ctx := context.Background()
	conns := arrangeConnectionService(t)

	if err := seedDockerConnection(ctx, conns, reachable); err != nil {
		t.Fatalf("seed docker connection: %v", err)
	}

	record, err := conns.Get(ctx, DefaultWorkspaceID, DefaultDockerConnectionID)
	if err != nil {
		t.Fatalf("get seeded connection: %v", err)
	}
	if record.AdapterType != "docker" || !record.Authorized {
		t.Fatalf("seeded connection = %+v, want authorized docker connection", record)
	}
}

func TestSeedDockerConnectionSkipsWhenDockerUnreachable(t *testing.T) {
	ctx := context.Background()
	conns := arrangeConnectionService(t)

	if err := seedDockerConnection(ctx, conns, unreachable); err != nil {
		t.Fatalf("seed docker connection: %v", err)
	}

	inUse, err := conns.IDInUse(ctx, DefaultWorkspaceID, DefaultDockerConnectionID)
	if err != nil {
		t.Fatalf("check id in use: %v", err)
	}
	if inUse {
		t.Fatal("seeded a connection despite an unreachable Docker endpoint")
	}
}

func TestSeedDockerConnectionNeverResurrectsADeletedConnection(t *testing.T) {
	ctx := context.Background()
	conns := arrangeConnectionService(t)
	if err := seedDockerConnection(ctx, conns, reachable); err != nil {
		t.Fatalf("initial seed: %v", err)
	}
	if err := conns.Delete(ctx, connection.DeleteRequest{
		WorkspaceID:  DefaultWorkspaceID,
		ConnectionID: DefaultDockerConnectionID,
	}); err != nil {
		t.Fatalf("delete seeded connection: %v", err)
	}

	if err := seedDockerConnection(ctx, conns, reachable); err != nil {
		t.Fatalf("second seed: %v", err)
	}

	if _, err := conns.Get(ctx, DefaultWorkspaceID, DefaultDockerConnectionID); !errors.Is(err, connection.ErrNotFound) {
		t.Fatalf("deleted connection came back: err = %v", err)
	}
}

func arrangeConnectionService(t *testing.T) *connection.Service {
	t.Helper()
	storage, err := sqlitestore.Open(t.Context(), "file:"+filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := seedFirstWorkspace(t.Context(), storage.Workspaces()); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	resolver := credential.NewResolver(nil, storage.CredentialStore(), []byte("0123456789abcdef0123456789abcdef"))
	repository, err := storage.Connections(resolver)
	if err != nil {
		t.Fatalf("init connection storage: %v", err)
	}
	return connection.NewWithCredentials(repository)
}
