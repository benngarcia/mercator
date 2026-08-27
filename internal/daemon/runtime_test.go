package daemon_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/runprojection"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
)

func TestRuntimeBootstrapsLocalDockerOnlyWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	if runtime.GOOS == "windows" {
		t.Skip("the fake Docker executable is a POSIX shell script")
	}
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nprintf '%s\\n' '{\"Architecture\":\"amd64\",\"OSType\":\"linux\",\"NCPU\":4,\"ID\":\"fake-docker\",\"MemTotal\":8589934592}'\n"), 0o700); err != nil {
		t.Fatalf("write fake Docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			runtime, err := daemon.New(t.Context(), daemon.Config{
				SQLiteDSN:            "file:" + filepath.Join(t.TempDir(), "mercator.db"),
				OperatorToken:        "operator-token",
				MasterKey:            []byte("0123456789abcdef0123456789abcdef"),
				BootstrapLocalDocker: enabled,
			})
			if err != nil {
				t.Fatalf("new runtime: %v", err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			served := make(chan error, 1)
			go func() { served <- runtime.Serve(listener) }()

			request, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/v1/connections", nil)
			if err != nil {
				t.Fatalf("build connection request: %v", err)
			}
			request.Header.Set("Authorization", "Bearer operator-token")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("list connections: %v", err)
			}
			var body struct {
				Connections []struct {
					ID string `json:"id"`
				} `json:"connections"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode connections: %v", err)
			}
			_ = response.Body.Close()

			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			if err := runtime.Shutdown(shutdownCtx); err != nil {
				cancel()
				t.Fatalf("shutdown runtime: %v", err)
			}
			cancel()
			if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Fatalf("serve returned: %v", err)
			}

			want := 0
			if enabled {
				want = 1
			}
			if len(body.Connections) != want {
				t.Fatalf("connections = %+v, want %d", body.Connections, want)
			}
			if enabled && body.Connections[0].ID != daemon.DefaultDockerConnectionID {
				t.Fatalf("connection = %+v, want local Docker bootstrap", body.Connections[0])
			}
		})
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
	var readiness struct {
		Status                string   `json:"status"`
		StorageEpoch          string   `json:"storage_epoch"`
		APIEpoch              string   `json:"api_epoch"`
		SupportedClientEpochs []string `json:"supported_client_epochs"`
		CompatibilityFeatures []string `json:"compatibility_features"`
	}
	if err := json.Unmarshal(body, &readiness); err != nil {
		t.Fatalf("decode readiness: %v", err)
	}
	if response.StatusCode != http.StatusOK || readiness.Status != "ready" || readiness.StorageEpoch != "single-scope-v1" || readiness.APIEpoch != "single-scope-v2" {
		t.Fatalf("readiness response = %d %q, want single-scope readiness metadata", response.StatusCode, body)
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

// TestRuntimeRebuildsTheRunProjectionARenameMadeStale is the migration and the read
// model derived from it, through the whole startup path a real installation takes:
// the SQLite file on disk, every migration in order, and the rebuild pass that reads
// what they left behind.
//
// The service class migration rewrites the vocabulary inside the event log, and the
// Run projection is stored rather than recomputed, so without the rebuild every Run
// recorded before a Run stated its class reads back with no class at all. The
// storage package can say the projection was marked stale; only this says the daemon
// acts on it, and the class an operator is served comes from the migrated log.
//
// Docker is left alone here rather than stubbed on PATH: the runtime probes the local
// daemon at startup, and this case is about what a real startup does.
func TestRuntimeRebuildsTheRunProjectionARenameMadeStale(t *testing.T) {
	// Arrange: an installation whose events and Run projection both predate the
	// service class, which is the state the rename leaves behind.
	dsn := "file:" + filepath.Join(t.TempDir(), "mercator.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "storage", "sqlite", "testdata", "legacy_objective_event.sql"))
	if err != nil {
		t.Fatalf("read the pre-migration fixture: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), string(fixture)); err != nil {
		t.Fatalf("load the pre-migration fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the arranged database: %v", err)
	}

	// Act: start the production runtime over it, and stop it again.
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

	// Assert: the projection an API reader is served states the class the migrated
	// history now carries.
	storage, err := sqlitestore.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("reopen storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	page, err := storage.Runs().List(t.Context(), runprojection.PageRequest{})
	if err != nil {
		t.Fatalf("list the Run projection: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("the projection holds %d Runs, want the one the fixture recorded", len(page.Records))
	}
	if page.Records[0].ServiceClass != domain.ClassInteractive {
		t.Errorf("the projected Run reads class %q after a real startup", page.Records[0].ServiceClass)
	}
}
