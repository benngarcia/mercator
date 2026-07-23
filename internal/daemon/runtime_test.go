package daemon_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/daemon"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workspace"
)

func TestRuntimeStartsWithAnExistingNonDefaultWorkspace(t *testing.T) {
	// Arrange: an existing installation whose workspace catalog predates the
	// local quickstart defaults.
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	storage, err := sqlitestore.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	_, err = storage.Workspaces().Create(t.Context(), workspace.Create{
		ID:          "ws_existing",
		DisplayName: "Existing",
		CreatedAt:   time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC),
		CreatedBy:   "operator:test",
	})
	if err != nil {
		t.Fatalf("create existing workspace: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("close arranged storage: %v", err)
	}
	dockerPath, err := filepath.Abs(filepath.Join("testdata", "docker-reachable"))
	if err != nil {
		t.Fatalf("resolve Docker fixture path: %v", err)
	}
	t.Setenv("PATH", dockerPath)

	// Act: start the production runtime against that catalog.
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:     dsn,
		OperatorToken: "operator-token",
		MasterKey:     []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	if err := runtime.Shutdown(t.Context()); err != nil {
		t.Fatalf("shutdown runtime: %v", err)
	}

	// Assert: startup preserves the operator-owned workspace catalog.
	storage, err = sqlitestore.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("reopen storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	workspaces, err := storage.Workspaces().List(t.Context(), workspace.ListOptions{IncludeArchived: true})
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].ID != "ws_existing" {
		t.Fatalf("workspaces = %+v, want only ws_existing", workspaces)
	}
}

func TestRuntimeServesProductionHandlerOnCallerListener(t *testing.T) {
	// Arrange: a production runtime backed by private, temporary SQLite and a
	// caller-owned ephemeral listener.
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:     "file:" + filepath.Join(t.TempDir(), "mercator.db"),
		OperatorToken: "operator-token",
		MasterKey:     []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.Serve(listener) }()

	// Act: exercise the real HTTP server and then shut down the whole runtime.
	response, err := http.Get("http://" + listener.Addr().String() + "/health/ready")
	if err != nil {
		t.Fatalf("get readiness: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read readiness: %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown runtime: %v", err)
	}

	// Assert: production readiness was served and Serve stopped normally.
	if response.StatusCode != http.StatusOK || string(body) != "{\"status\":\"ready\"}\n" {
		t.Fatalf("readiness response = %d %q, want 200 ready", response.StatusCode, body)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve returned: %v", err)
	}
}

func TestLocalAuthRuntimeRejectsNonLoopbackHosts(t *testing.T) {
	// Arrange: a --dev runtime, which auto-mints browser sessions and must
	// therefore refuse requests addressed by a non-loopback hostname (the DNS
	// rebinding defense).
	runtime, err := daemon.New(t.Context(), daemon.Config{
		SQLiteDSN:      "file:" + filepath.Join(t.TempDir(), "mercator.db"),
		OperatorToken:  "operator-token",
		MasterKey:      []byte("0123456789abcdef0123456789abcdef"),
		LocalAuthEmail: "developer@localhost",
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := runtime.Shutdown(shutdownCtx); err != nil {
			t.Fatalf("shutdown runtime: %v", err)
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve returned: %v", err)
		}
	}()

	// Act: the same TCP endpoint addressed by a loopback name and by a
	// rebindable external name.
	loopback, err := http.Get("http://" + listener.Addr().String() + "/health/ready")
	if err != nil {
		t.Fatalf("get readiness via loopback: %v", err)
	}
	_ = loopback.Body.Close()
	rebound, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/auth/session", nil)
	if err != nil {
		t.Fatalf("build rebound request: %v", err)
	}
	rebound.Host = "attacker.example"
	response, err := http.DefaultClient.Do(rebound)
	if err != nil {
		t.Fatalf("get session via rebound host: %v", err)
	}
	_ = response.Body.Close()

	// Assert: loopback requests are served, rebound hostnames are refused
	// before any handler (session minting included) runs.
	if loopback.StatusCode != http.StatusOK {
		t.Fatalf("loopback readiness = %d, want 200", loopback.StatusCode)
	}
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("rebound host response = %d, want 421", response.StatusCode)
	}
	if cookies := response.Cookies(); len(cookies) != 0 {
		t.Fatalf("rebound host received cookies: %v", cookies)
	}
}
