package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/httpapi"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workspace"
)

// A broker with one workspace has nothing to disambiguate, so naming it is
// ceremony the operator should not have to perform.
func TestRunListResolvesTheOnlyWorkspace(t *testing.T) {
	server := httptest.NewServer(newAuthenticatedCLIServer(t, t.Name()))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Config{
		BaseURL: server.URL,
		Token:   cliTestToken,
		Args:    []string{"run", "list"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})

	if code != 0 {
		t.Fatalf("run list failed code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"runs"`) {
		t.Fatalf("expected a run list, got %s", stdout.String())
	}
}

// Several workspaces is genuine ambiguity. The CLI must name the candidates
// rather than pick one, because picking wrong reads and writes the wrong
// tenant's runs.
func TestRunListRefusesToGuessAmongSeveralWorkspaces(t *testing.T) {
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	seedCLIWorkspace(t, dsn)
	addWorkspace(t, dsn, "ws_2")
	server := httptest.NewServer(handlerForDSN(t, dsn, httpapi.WithBearerAuth(cliTestToken)))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Config{
		BaseURL: server.URL,
		Token:   cliTestToken,
		Args:    []string{"run", "list"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})

	if code == 0 {
		t.Fatal("expected a loud failure when the workspace is ambiguous")
	}
	for _, want := range []string{"ws_1", "ws_2", "--workspace-id"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("error should mention %q, got %s", want, stderr.String())
		}
	}
}

// `run decision` right after `run create` should not make you paste back the id
// the previous command just printed.
func TestRunDecisionDefaultsToTheLatestRun(t *testing.T) {
	server := httptest.NewServer(newAuthenticatedCLIServer(t, t.Name()))
	t.Cleanup(server.Close)

	var created bytes.Buffer
	if code := Run(context.Background(), Config{
		BaseURL:     server.URL,
		Token:       cliTestToken,
		WorkspaceID: "ws_1",
		Args:        []string{"run", "create", "busybox", "--", "echo", "hi"},
		Stdout:      &created,
		Stderr:      &created,
	}); code != 0 {
		t.Fatalf("create failed: %s", created.String())
	}

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Config{
		BaseURL: server.URL,
		Token:   cliTestToken,
		Args:    []string{"run", "get"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})

	if code != 0 {
		t.Fatalf("run get failed code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"run"`) {
		t.Fatalf("expected the latest run, got %s", stdout.String())
	}
}

// Asking for one run in a workspace that has none must say so plainly instead
// of sending an empty run id to the server.
func TestRunGetSaysWhenThereIsNoRunToDefaultTo(t *testing.T) {
	server := httptest.NewServer(newAuthenticatedCLIServer(t, t.Name()))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Config{
		BaseURL: server.URL,
		Token:   cliTestToken,
		Args:    []string{"run", "get"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})

	if code == 0 {
		t.Fatal("expected a failure when the workspace has no runs")
	}
	if !strings.Contains(stderr.String(), "no runs yet") {
		t.Fatalf("error should explain there are no runs, got %s", stderr.String())
	}
}

// The first docker connection may as well be called "docker".
func TestConnectionCreateNamesTheConnectionAfterTheAdapter(t *testing.T) {
	server := httptest.NewServer(newAuthenticatedCLIServer(t, t.Name()))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Config{
		BaseURL: server.URL,
		Token:   cliTestToken,
		Args:    []string{"connection", "create", "--adapter-type", "docker"},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})

	if code != 0 {
		t.Fatalf("connection create failed code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"id":"docker"`) {
		t.Fatalf("expected the connection to be named after its adapter, got %s", stdout.String())
	}
}

// `mercator help run` must reach run help. It used to return root help,
// because "help" is itself a help argument and matched first.
func TestHelpReachesTheNamedTopic(t *testing.T) {
	var stdout bytes.Buffer

	code := Run(context.Background(), Config{Args: []string{"help", "run"}, Stdout: &stdout})

	if code != 0 {
		t.Fatalf("help run exited %d", code)
	}
	if !strings.Contains(stdout.String(), "Usage: mercator run") {
		t.Fatalf("expected run help, got %s", stdout.String())
	}
}

// cliTestToken stands in for the token `mercator serve` generates: every real
// broker authenticates, so the defaults have to work through auth.
const cliTestToken = "test-token"

func newAuthenticatedCLIServer(t *testing.T, name string) http.Handler {
	t.Helper()
	dsn := "file:" + name + "?mode=memory&cache=shared"
	seedCLIWorkspace(t, dsn)
	return handlerForDSN(t, dsn, httpapi.WithBearerAuth(cliTestToken))
}

func addWorkspace(t *testing.T, dsn, id string) {
	t.Helper()
	storage, err := sqlitestore.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open workspace storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if _, err := storage.Workspaces().Create(t.Context(), workspace.Create{
		ID:          id,
		DisplayName: id,
		CreatedAt:   time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		CreatedBy:   "test:cli",
	}); err != nil {
		t.Fatalf("create workspace %s: %v", id, err)
	}
}
