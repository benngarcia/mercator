package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
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

// listeningPhrase is how a started server announces the address it serves.
const listeningPhrase = "mercator listening on "

// operatorToken is the bearer token the servers a case starts are configured
// with. It is fixed rather than generated so that nothing writes a CLI context
// on the machine running the tests.
const operatorToken = "operator-token"

// mercatorServer is one real `mercator serve` command, running, on a real
// database. A case takes one when it needs to state what happens to something
// else while a server is up, or what a restored database serves.
type mercatorServer struct {
	baseURL  string
	stopOnce sync.Once
	cancel   context.CancelFunc
	exited   chan int
	t        *testing.T
}

// serveDatabase starts the serve command on dsn and returns once it is
// listening. The server is stopped when the case ends, or earlier if the case
// stops it.
func serveDatabase(t *testing.T, dsn, masterKey string) *mercatorServer {
	t.Helper()
	startup := captureStartupLog(t)
	serveCtx, cancel := context.WithCancel(context.Background())
	server := &mercatorServer{cancel: cancel, exited: make(chan int, 1), t: t}
	go func() {
		server.exited <- run(serveCtx, []string{"mercator", "serve"}, map[string]string{
			"MERCATOR_ADDR":       "127.0.0.1:0",
			"MERCATOR_API_TOKEN":  operatorToken,
			"MERCATOR_SECRET_KEY": masterKey,
			"MERCATOR_SQLITE_DSN": dsn,
		}, io.Discard, io.Discard)
	}()
	t.Cleanup(server.stop)
	startup.waitFor(t, listeningPhrase)
	server.baseURL = announcedURL(t, startup.String())
	return server
}

// stop shuts the server down and reports an exit code that says it failed.
func (s *mercatorServer) stop() {
	s.stopOnce.Do(func() {
		s.cancel()
		if exitCode := <-s.exited; exitCode != 0 {
			s.t.Errorf("serve exited %d", exitCode)
		}
	})
}

// get reads a path from the server with the operator's bearer token.
func (s *mercatorServer) get(t *testing.T, path string) string {
	t.Helper()
	return s.send(t, http.MethodGet, path, "", nil)
}

// post writes to the server, carrying the idempotency key every mutation route
// requires.
func (s *mercatorServer) post(t *testing.T, path, idempotencyKey string, body any) string {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode %s body: %v", path, err)
	}
	return s.send(t, http.MethodPost, path, idempotencyKey, encoded)
}

func (s *mercatorServer) send(t *testing.T, method, path, idempotencyKey string, body []byte) string {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, s.baseURL+path, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	request.Header.Set("Authorization", "Bearer "+operatorToken)
	request.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	answered, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	if response.StatusCode >= http.StatusBadRequest {
		t.Fatalf("%s %s = %d: %s", method, path, response.StatusCode, answered)
	}
	return string(answered)
}

// announcedURL reads the address out of what the server said, so a case reaches
// the port the kernel chose without guessing one.
func announcedURL(t *testing.T, startupLog string) string {
	t.Helper()
	_, announced, found := strings.Cut(startupLog, listeningPhrase)
	if !found {
		t.Fatalf("startup log = %q, want %q", startupLog, listeningPhrase)
	}
	url, _, _ := strings.Cut(announced, "\n")
	return strings.TrimSpace(url)
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

// The Lab's containment is no longer a flag check: parsing accepts whatever
// address the operator names and the Lab server itself refuses to serve one
// another machine could reach. See TestTheLabRefusesAListenerAnotherMachineCouldReach.
func TestLabServeParsesAnyAddressAndLeavesContainmentToTheServer(t *testing.T) {
	// Arrange
	args := []string{
		"mercator", "lab", "serve",
		"--blueprint", "demo.json",
		"--addr", "0.0.0.0:9090",
	}

	// Act
	options, err := parseLabServeOptions(args)

	// Assert
	if err != nil {
		t.Fatalf("parse Lab serve options: %v", err)
	}
	if options.addr != "0.0.0.0:9090" {
		t.Fatalf("addr = %q, want the address as given", options.addr)
	}
}

func TestServeRefusesAPublicAddressWithNoAdministrativeAddress(t *testing.T) {
	// Arrange
	env := map[string]string{
		"MERCATOR_ADDR":            "0.0.0.0:0",
		"MERCATOR_API_TOKEN":       "operator-token",
		"MERCATOR_SECRET_KEY":      "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"MERCATOR_SQLITE_DSN":      "file:" + t.Name() + "?mode=memory&cache=shared",
		"MERCATOR_TLS_CERT_FILE":   "cert.pem",
		"MERCATOR_TLS_KEY_FILE":    "key.pem",
		"MERCATOR_PUBLIC_URL":      "https://mercator.example.com",
		"MERCATOR_ADMIN_ADDR":      "",
		"MERCATOR_OIDC_CLIENT_ID":  "",
		"MERCATOR_OIDC_ISSUER":     "",
		"MERCATOR_OIDC_SECRET_KEY": "",
	}

	// Act
	exitCode := run(context.Background(), []string{"mercator", "serve"}, env, io.Discard, io.Discard)

	// Assert
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 from a public deployment with no administrative address", exitCode)
	}
}

func TestServeRefusesAProxiedLoopbackDeploymentWithNoAdministrativeAddress(t *testing.T) {
	// Arrange: the documented reverse-proxy topology. The bind address tells
	// this process nothing, because the proxy is what faces the internet.
	env := map[string]string{
		"MERCATOR_ADDR":       "127.0.0.1:0",
		"MERCATOR_API_TOKEN":  "operator-token",
		"MERCATOR_SECRET_KEY": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"MERCATOR_SQLITE_DSN": "file:" + t.Name() + "?mode=memory&cache=shared",
		"MERCATOR_PUBLIC_URL": "https://mercator.example.com",
		"MERCATOR_ADMIN_ADDR": "",
	}
	refusal := captureStandardLog(t)

	// Act
	exitCode := run(context.Background(), []string{"mercator", "serve"}, env, io.Discard, io.Discard)

	// Assert
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 from a proxied deployment with no administrative address", exitCode)
	}
	if !strings.Contains(refusal.String(), adminAddrVar) {
		t.Fatalf("the refusal did not name %s: %s", adminAddrVar, refusal)
	}
}

// captureStandardLog collects what startup refuses with. `serve` reports
// configuration failures through the standard logger, which is where an
// operator reads them from.
func captureStandardLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	captured := &bytes.Buffer{}
	stdlog.SetOutput(captured)
	t.Cleanup(func() { stdlog.SetOutput(os.Stderr) })
	return captured
}

// A public URL that names no host used to answer "announces nothing", which
// exempted the proxied deployment from the administrative listener entirely.
// Both of these parse without error, so neither reached the branch that fails
// closed.
func TestServeRefusesAPublicURLThatNamesNoHost(t *testing.T) {
	for name, publicURL := range map[string]string{
		"no scheme":            "mercator.example.com",
		"no scheme with port":  "mercator.example.com:8443",
		"one slash":            "https:/mercator.example.com",
		"scheme relative":      "//mercator.example.com",
		"not a URL at all":     "mercator dot example dot com",
		"unparseable":          "https://mercator.example.com\x7f",
		"scheme nothing dials": "ftp://mercator.example.com",
	} {
		t.Run(name, func(t *testing.T) {
			// Arrange: the proxied topology, with the public URL mistyped.
			env := map[string]string{
				"MERCATOR_ADDR":       "127.0.0.1:0",
				"MERCATOR_API_TOKEN":  "operator-token",
				"MERCATOR_SECRET_KEY": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				"MERCATOR_SQLITE_DSN": "file:" + t.Name() + "?mode=memory&cache=shared",
				"MERCATOR_PUBLIC_URL": publicURL,
				"MERCATOR_ADMIN_ADDR": "",
			}
			refusal := captureStandardLog(t)
			// A deployment that does not refuse serves until it is stopped, and a
			// cancelled context is what makes that return an exit code rather than
			// hang this test.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			// Act
			exitCode := run(ctx, []string{"mercator", "serve"}, env, io.Discard, io.Discard)

			// Assert
			if exitCode != 1 {
				t.Fatalf("exit code = %d, want 1 from a public URL naming no host", exitCode)
			}
			if !strings.Contains(refusal.String(), publicURLVar) {
				t.Fatalf("the refusal did not name %s: %s", publicURLVar, refusal)
			}
		})
	}
}

// The exemption the malformed cases above must not be confused with: a public
// URL naming this machine announces nothing, and that deployment still serves.
// Proved by driving startup past the administrative-listener check and failing
// it on the next thing, so the refusal names the issuer rather than the address.
func TestALoopbackDeploymentReportingToItselfNeedsNoAdministrativeAddress(t *testing.T) {
	// Arrange
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "discovery unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(issuer.Close)
	env := map[string]string{
		"MERCATOR_ADDR":                "127.0.0.1:0",
		"MERCATOR_API_TOKEN":           "operator-token",
		"MERCATOR_SECRET_KEY":          "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"MERCATOR_SQLITE_DSN":          "file:" + t.Name() + "?mode=memory&cache=shared",
		"MERCATOR_PUBLIC_URL":          "http://127.0.0.1:8080",
		"MERCATOR_ADMIN_ADDR":          "",
		"MERCATOR_OIDC_ISSUER":         issuer.URL,
		"MERCATOR_OIDC_CLIENT_ID":      "client-id",
		"MERCATOR_OIDC_CLIENT_SECRET":  "client-secret",
		"MERCATOR_OIDC_ALLOWED_DOMAIN": "example.com",
		"MERCATOR_SESSION_KEY":         "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
	}
	refusal := captureStandardLog(t)

	// Act
	exitCode := run(context.Background(), []string{"mercator", "serve"}, env, io.Discard, io.Discard)

	// Assert
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 from the unreachable issuer this deployment was pointed at", exitCode)
	}
	if strings.Contains(refusal.String(), adminAddrVar) {
		t.Fatalf("a deployment reporting to itself was asked for %s: %s", adminAddrVar, refusal)
	}
}

func TestABindAdministrativeListenerRefusesTheWildcard(t *testing.T) {
	// Arrange, Act
	_, err := bindListeners("127.0.0.1:0", "0.0.0.0:0")

	// Assert
	if err == nil {
		t.Fatal("an administrative listener on every interface is not a private one")
	}
}

func TestBindListenersAnswersTheBoundAdministrativeAddress(t *testing.T) {
	// Arrange, Act
	listeners, err := bindListeners("127.0.0.1:0", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind listeners: %v", err)
	}
	defer listeners.close()

	// Assert
	if listeners.adminAddress() != listeners.admin.Addr().String() {
		t.Fatalf("administrative address = %q, want the address the kernel gave", listeners.adminAddress())
	}
	if listeners.adminAddress() == "127.0.0.1:0" {
		t.Fatal("the administrative address still names the port that was asked for")
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
		// Announcing a public URL is what makes this a deployment reachable from
		// elsewhere, so it needs an administrative address to get as far as
		// opening storage at all.
		"MERCATOR_ADMIN_ADDR": "127.0.0.1:0",
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
