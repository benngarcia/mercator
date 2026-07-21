package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

func TestConsoleRunsNavigation(t *testing.T) {
	if os.Getenv("MERCATOR_BROWSER_TEST") != "1" {
		t.Skip("set MERCATOR_BROWSER_TEST=1 to run the console browser acceptance flow")
	}
	if !uiBuilt() {
		t.Fatal("embedded console is not built; run bun run build in web/app first")
	}

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve browser test path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	appDir := filepath.Join(repoRoot, "web", "app")
	fixture := filepath.Join(appDir, "test", "fixtures", "runs-navigation.json")
	output := os.Getenv("MERCATOR_BROWSER_OUTPUT")
	if output == "" {
		output = t.TempDir()
	}
	dsn := "file:" + filepath.Join(t.TempDir(), "runs-navigation.db")

	handler, closeHandler, err := HandlerForSQLite(
		context.Background(),
		dsn,
		[]domain.OfferSnapshot{httpOffer("offer_runs_navigation", time.Now().UTC())},
		WithBearerAuth("runs-navigation-token"),
	)
	if err != nil {
		t.Fatalf("build browser handler: %v", err)
	}
	t.Cleanup(func() {
		if err := closeHandler(); err != nil {
			t.Errorf("close browser handler: %v", err)
		}
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	seedConsoleBrowserRun(t, server.URL)

	cmd := exec.Command("bun", "run", "test:browser:runs-navigation")
	cmd.Dir = appDir
	cmd.Env = append(os.Environ(),
		"MERCATOR_BROWSER_BASE_URL="+server.URL,
		"MERCATOR_BROWSER_FIXTURE="+fixture,
		"MERCATOR_BROWSER_OUTPUT="+output,
	)
	result, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run browser acceptance: %v\n%s", err, result)
	}
	t.Logf("browser acceptance: %s", result)
}

func seedConsoleBrowserRun(t *testing.T, serverURL string) {
	t.Helper()

	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		serverURL+"/v1/runs?workspace_id=ws_runs_navigation",
		bytes.NewBufferString(`{"image":"busybox:latest"}`),
	)
	if err != nil {
		t.Fatalf("build seed run request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer runs-navigation-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "runs-navigation-seed")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("seed run: got %s, want 202 Accepted", response.Status)
	}
}
