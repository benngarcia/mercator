package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/tlsmaterial"
)

func TestRunDelegatesVerifyToTheConformanceRunner(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"mercator", "verify"}, map[string]string{}, &stdout, &stderr)
	if exitCode != 2 || !bytes.Contains(stderr.Bytes(), []byte("--spec")) {
		t.Fatalf("run() = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunPrintsVerifyHelpWithoutAnAPIBaseURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"mercator", "help", "verify"}, map[string]string{}, &stdout, &stderr)
	if exitCode != 0 || !bytes.Contains(stdout.Bytes(), []byte("mercator verify --spec FILE")) {
		t.Fatalf("run() = %d, stdout = %s, stderr = %s", exitCode, stdout.String(), stderr.String())
	}
}

func TestRunDelegatesJSONCLICommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/runs" || request.URL.Query().Get("workspace_id") != "ws_1" {
			t.Fatalf("request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer cli-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[]}`))
	}))
	t.Cleanup(server.Close)
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"mercator", "run", "list", "--workspace-id", "ws_1"}, map[string]string{
		"MERCATOR_API_URL": server.URL, "MERCATOR_API_TOKEN": "cli-token",
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run() = %d, stderr = %s", exitCode, stderr.String())
	}
	var response map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("stdout = %q: %v", stdout.String(), err)
	}
}

func TestServeRejectsInvalidMasterKeyBeforeOpeningStorage(t *testing.T) {
	exitCode := run(context.Background(), []string{"mercator", "serve"}, map[string]string{
		"MERCATOR_ADDR": "127.0.0.1:0", "MERCATOR_API_TOKEN": "operator-token", "MERCATOR_SECRET_KEY": "invalid",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
}

// TestServeRefusesANonLoopbackAddressWithoutTLSMaterial states the rule that
// replaced the old warning: a listener that would carry bearer tokens over the
// public interface in the clear does not come up at all. The refusal names the
// variables an operator has to set.
func TestServeRefusesANonLoopbackAddressWithoutTLSMaterial(t *testing.T) {
	// Arrange
	log := captureStartupLog(t)

	// Act
	exitCode := run(context.Background(), []string{"mercator", "serve"}, map[string]string{
		"MERCATOR_ADDR":       "0.0.0.0:8080",
		"MERCATOR_API_TOKEN":  "operator-token",
		"MERCATOR_SECRET_KEY": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"MERCATOR_SQLITE_DSN": "file:" + filepath.Join(t.TempDir(), "mercator.db"),
	}, &bytes.Buffer{}, &bytes.Buffer{})

	// Assert
	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
	if !strings.Contains(log.String(), tlsmaterial.CertFileVar) {
		t.Fatalf("startup log = %q, want %s named", log.String(), tlsmaterial.CertFileVar)
	}
}

// TestServeRequiresAMasterKey: credential sealing, run-report tokens and node
// identity all derive from MERCATOR_SECRET_KEY and all disable themselves
// without it, so an absent key stops startup rather than starting a server with
// three security features quietly off.
func TestServeRequiresAMasterKey(t *testing.T) {
	// Arrange
	log := captureStartupLog(t)

	// Act
	exitCode := run(context.Background(), []string{"mercator", "serve"}, map[string]string{
		"MERCATOR_ADDR":       "127.0.0.1:0",
		"MERCATOR_API_TOKEN":  "operator-token",
		"MERCATOR_SQLITE_DSN": "file:" + filepath.Join(t.TempDir(), "mercator.db"),
	}, &bytes.Buffer{}, &bytes.Buffer{})

	// Assert
	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
	if !strings.Contains(log.String(), "MERCATOR_SECRET_KEY is required") {
		t.Fatalf("startup log = %q, want the missing key named", log.String())
	}
}

// captureStartupLog redirects the standard logger, which is where startup
// refusals and the listening announcement are written, and restores it when the
// case ends.
func captureStartupLog(t *testing.T) *startupLog {
	t.Helper()
	captured := &startupLog{}
	stdlog.SetOutput(captured)
	t.Cleanup(func() { stdlog.SetOutput(os.Stderr) })
	return captured
}

// startupLog is what a starting server says. It is read while that server is
// still running, so it holds its own lock rather than leaving the case to race
// the goroutine writing to it.
type startupLog struct {
	mu      sync.Mutex
	written strings.Builder
}

func (l *startupLog) Write(said []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.written.Write(said)
}

func (l *startupLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.written.String()
}

// waitFor blocks until the server has said phrase, or fails the case saying
// everything it did say.
func (l *startupLog) waitFor(t *testing.T, phrase string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(l.String(), phrase) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("startup log = %q, want %q said within 30s", l.String(), phrase)
}

func TestServeOptionsEnableLocalAuthentication(t *testing.T) {
	// Arrange
	args := []string{"mercator", "serve", "--dev"}

	// Act
	options, err := parseServeOptions(args)

	// Assert
	if err != nil {
		t.Fatalf("parse serve options: %v", err)
	}
	if options.localAuthEmail != localDeveloperEmail {
		t.Fatalf("local email = %q, want %q", options.localAuthEmail, localDeveloperEmail)
	}
}

func TestServeRejectsLocalAuthenticationOnNonLoopbackAddress(t *testing.T) {
	// Arrange
	env := map[string]string{
		"MERCATOR_ADDR":       "0.0.0.0:8080",
		"MERCATOR_API_TOKEN":  "operator-token",
		"MERCATOR_SQLITE_DSN": "file:unused.db",
	}

	// Act
	exitCode := run(context.Background(), []string{"mercator", "serve", "--dev"}, env, &bytes.Buffer{}, &bytes.Buffer{})

	// Assert
	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
}

func TestServeRejectsUnknownFlags(t *testing.T) {
	// Arrange
	args := []string{"mercator", "serve", "--trust-everyone"}

	// Act
	_, err := parseServeOptions(args)

	// Assert
	if err == nil {
		t.Fatal("unknown serve flag should fail")
	}
}

func TestLabServeOptionsRequireABlueprint(t *testing.T) {
	// Arrange
	args := []string{"mercator", "lab", "serve"}

	// Act
	_, err := parseLabServeOptions(args)

	// Assert
	if err == nil {
		t.Fatal("missing Blueprint should fail")
	}
}

func TestLabServeOptionsNameEveryExecutionInput(t *testing.T) {
	// Arrange
	args := []string{
		"mercator", "lab", "serve",
		"--blueprint", "demo.json",
		"--addr", "127.0.0.1:9090",
		"--seed", "browser-proof",
		"--policy", "cost-aware",
	}

	// Act
	options, err := parseLabServeOptions(args)

	// Assert
	if err != nil {
		t.Fatalf("parse Lab serve options: %v", err)
	}
	if options.blueprint != "demo.json" ||
		options.addr != "127.0.0.1:9090" ||
		options.seed != "browser-proof" ||
		options.policy != "cost-aware" {
		t.Fatalf("options = %+v", options)
	}
}

func TestLabServeRejectsNonLoopbackAddress(t *testing.T) {
	// Arrange
	args := []string{
		"mercator", "lab", "serve",
		"--blueprint", "demo.json",
		"--addr", "0.0.0.0:9090",
	}

	// Act
	_, err := parseLabServeOptions(args)

	// Assert
	if err == nil {
		t.Fatal("non-loopback Lab address should fail")
	}
}

func TestServeClosesStorageWhenOIDCDiscoveryFails(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "discovery unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(issuer.Close)
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	exitCode := run(context.Background(), []string{"mercator", "serve"}, map[string]string{
		"MERCATOR_ADDR":                "127.0.0.1:0",
		"MERCATOR_API_TOKEN":           "operator-token",
		"MERCATOR_SECRET_KEY":          "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"MERCATOR_SQLITE_DSN":          dsn,
		"MERCATOR_OIDC_ISSUER":         issuer.URL,
		"MERCATOR_OIDC_CLIENT_ID":      "client-id",
		"MERCATOR_OIDC_CLIENT_SECRET":  "client-secret",
		"MERCATOR_OIDC_ALLOWED_DOMAIN": "example.com",
		"MERCATOR_SESSION_KEY":         "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
		"MERCATOR_PUBLIC_URL":          "https://mercator.example.com",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open database after run: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var tables int
	if err := database.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		t.Fatalf("inspect database after run: %v", err)
	}
	if tables != 0 {
		t.Fatalf("database retained %d tables after run returned", tables)
	}
}
