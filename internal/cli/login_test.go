package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

func runCLI(t *testing.T, cfg Config, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cfg.Args = args
	cfg.Stdout = &stdout
	cfg.Stderr = &stderr
	code := Run(context.Background(), cfg)
	return code, stdout.String(), stderr.String()
}

func tempConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.json")
}

func TestContextCommandsRoundTrip(t *testing.T) {
	path := tempConfigPath(t)
	cfg := Config{ConfigPath: path}

	code, out, errOut := runCLI(t, cfg, "context", "set", "staging",
		"--api-url", "https://staging.example.com", "--workspace-id", "ws_stage", "--token", "static-token")
	if code != 0 {
		t.Fatalf("context set failed: %s", errOut)
	}
	var set contextSummary
	if err := json.Unmarshal([]byte(out), &set); err != nil {
		t.Fatalf("context set output not JSON: %q", out)
	}
	if !set.Current || set.APIURL != "https://staging.example.com" || set.Credential != "api-token" {
		t.Fatalf("unexpected context set output: %+v", set)
	}

	code, _, _ = runCLI(t, cfg, "context", "set", "production", "--api-url", "https://prod.example.com")
	if code != 0 {
		t.Fatalf("second context set failed")
	}

	code, out, _ = runCLI(t, cfg, "context", "list")
	if code != 0 {
		t.Fatalf("context list failed")
	}
	var listed struct {
		Current  string           `json:"current"`
		Contexts []contextSummary `json:"contexts"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("context list output not JSON: %q", out)
	}
	if listed.Current != "staging" || len(listed.Contexts) != 2 {
		t.Fatalf("unexpected list: %+v", listed)
	}

	code, out, _ = runCLI(t, cfg, "context", "use", "production")
	if code != 0 || out != "{\"current\":\"production\"}\n" {
		t.Fatalf("context use failed: code=%d out=%q", code, out)
	}

	code, _, errOut = runCLI(t, cfg, "context", "use", "missing")
	if code == 0 {
		t.Fatalf("context use of a missing context must fail, got %s", errOut)
	}

	code, _, _ = runCLI(t, cfg, "context", "delete", "production")
	if code != 0 {
		t.Fatalf("context delete failed")
	}
	file, err := loadFileConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if file.CurrentContext != "" || len(file.Contexts) != 1 {
		t.Fatalf("delete should drop the context and clear current: %+v", file)
	}
}

func TestContextSuppliesCredentialsAndEnvWins(t *testing.T) {
	var gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[]}`))
	}))
	t.Cleanup(server.Close)

	path := tempConfigPath(t)
	cfg := Config{ConfigPath: path}
	if code, _, errOut := runCLI(t, cfg, "context", "set", "test",
		"--api-url", server.URL, "--workspace-id", "ws_ctx", "--token", "ctx-token"); code != 0 {
		t.Fatalf("context set failed: %s", errOut)
	}

	// With no env at all, the context supplies url, token, and workspace.
	code, _, errOut := runCLI(t, cfg, "run", "list")
	if code != 0 {
		t.Fatalf("run list via context failed: %s", errOut)
	}
	if gotAuth != "Bearer ctx-token" {
		t.Fatalf("expected context token, got %q", gotAuth)
	}
	if gotPath != "/v1/runs?workspace_id=ws_ctx" {
		t.Fatalf("expected context workspace, got %q", gotPath)
	}

	// Environment values (Config fields) win over the context.
	envCfg := cfg
	envCfg.Token = "env-token"
	envCfg.WorkspaceID = "ws_env"
	if code, _, errOut := runCLI(t, envCfg, "run", "list"); code != 0 {
		t.Fatalf("run list via env failed: %s", errOut)
	}
	if gotAuth != "Bearer env-token" {
		t.Fatalf("env token must win, got %q", gotAuth)
	}
	if gotPath != "/v1/runs?workspace_id=ws_env" {
		t.Fatalf("env workspace must win, got %q", gotPath)
	}
}

// fakeLoginServer stands in for a Mercator server with OIDC configured: it
// reports login enabled, immediately redirects the "browser" to the CLI's
// loopback listener with a code, and exchanges that code for a token.
func fakeLoginServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/session", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true}`))
	})
	mux.HandleFunc("GET /auth/login", func(w http.ResponseWriter, r *http.Request) {
		port := r.URL.Query().Get("cli_port")
		state := r.URL.Query().Get("cli_state")
		http.Redirect(w, r, fmt.Sprintf("http://127.0.0.1:%s/?code=%s&state=%s",
			port, url.QueryEscape("code-1"), url.QueryEscape(state)), http.StatusSeeOther)
	})
	mux.HandleFunc("POST /auth/cli/exchange", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code != "code-1" {
			http.Error(w, "bad code", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":      "cli-token-1",
			"email":      "operator@example.com",
			"expires_at": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestLoginStoresCredentialAndAuthenticatesAPICommands(t *testing.T) {
	server := fakeLoginServer(t)
	path := tempConfigPath(t)

	// The test "browser" just follows the login URL; the redirect lands on the
	// CLI's loopback listener.
	browser := func(target string) error {
		resp, err := http.Get(target)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	cfg := Config{ConfigPath: path, OpenBrowser: browser}
	code, out, errOut := runCLI(t, cfg, "login", "--api-url", server.URL)
	if code != 0 {
		t.Fatalf("login failed: %s", errOut)
	}
	var granted struct {
		Context string `json:"context"`
		Email   string `json:"email"`
	}
	if err := json.Unmarshal([]byte(out), &granted); err != nil {
		t.Fatalf("login output not JSON: %q", out)
	}
	if granted.Context != "default" || granted.Email != "operator@example.com" {
		t.Fatalf("unexpected login output: %+v", granted)
	}

	file, err := loadFileConfig(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	stored := file.Contexts["default"]
	if stored == nil || stored.CLIToken != "cli-token-1" || !stored.cliTokenValid(time.Now()) {
		t.Fatalf("login did not store a valid credential: %+v", stored)
	}
	if file.CurrentContext != "default" {
		t.Fatalf("login should make the new context current, got %q", file.CurrentContext)
	}

	// The stored credential now authenticates API commands.
	var gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[]}`))
	}))
	t.Cleanup(api.Close)
	if code, _, errOut := runCLI(t, cfg, "context", "set", "default", "--api-url", api.URL); code != 0 {
		t.Fatalf("repoint context: %s", errOut)
	}
	if code, _, errOut := runCLI(t, cfg, "run", "list", "--workspace-id", "ws_1"); code != 0 {
		t.Fatalf("run list failed: %s", errOut)
	}
	if gotAuth != "Bearer cli-token-1" {
		t.Fatalf("expected the login token, got %q", gotAuth)
	}

	// Logout clears it.
	if code, _, errOut := runCLI(t, cfg, "logout"); code != 0 {
		t.Fatalf("logout failed: %s", errOut)
	}
	file, _ = loadFileConfig(path)
	if file.Contexts["default"].CLIToken != "" {
		t.Fatalf("logout must clear the stored token")
	}
}

func TestLoginRefusesServerWithoutOIDC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":false}`))
	}))
	t.Cleanup(server.Close)

	cfg := Config{ConfigPath: tempConfigPath(t)}
	code, _, errOut := runCLI(t, cfg, "login", "--api-url", server.URL)
	if code == 0 {
		t.Fatalf("login against a token-only server must fail")
	}
	if !bytes.Contains([]byte(errOut), []byte("OIDC_NOT_CONFIGURED")) {
		t.Fatalf("expected OIDC_NOT_CONFIGURED, got %q", errOut)
	}
}

func TestExpiredLoginWarnsAndFallsBack(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[]}`))
	}))
	t.Cleanup(server.Close)

	path := tempConfigPath(t)
	file := FileConfig{
		CurrentContext: "stale",
		Contexts: map[string]*ContextConfig{
			"stale": {
				APIURL:            server.URL,
				WorkspaceID:       "ws_1",
				CLIToken:          "expired-token",
				CLITokenEmail:     "operator@example.com",
				CLITokenExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			},
		},
	}
	if err := saveFileConfig(path, file); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	cfg := Config{ConfigPath: path}
	code, _, errOut := runCLI(t, cfg, "run", "list")
	if code != 0 {
		t.Fatalf("run list should still issue the request: %s", errOut)
	}
	if gotAuth != "" {
		t.Fatalf("an expired login token must not be sent, got %q", gotAuth)
	}
	if !bytes.Contains([]byte(errOut), []byte("login expired")) {
		t.Fatalf("expected an expiry warning, got %q", errOut)
	}
}
