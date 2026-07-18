package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// loginTimeout bounds how long `mercator login` waits for the browser
// round-trip before giving up.
var loginTimeout = 3 * time.Minute

// runLogin implements `mercator login`: the standard native-app flow. The CLI
// binds a loopback listener, sends the browser through the server's OIDC
// login with the loopback port, receives a single-use code on the redirect,
// exchanges it for a CLI token, and stores the token in the named context.
func runLogin(ctx context.Context, cfg Config, args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	contextName := fs.String("context", "", "context to store the credential in (defaults to the current context, else \"default\")")
	apiURL := fs.String("api-url", "", "API base URL (defaults to MERCATOR_API_URL or the context's api_url)")
	if err := fs.Parse(args); err != nil {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", err.Error())
		return 2
	}

	file, err := loadFileConfig(cfg.ConfigPath)
	if err != nil {
		writeCLIError(cfg.Stderr, "CONFIG_INVALID", err.Error())
		return 1
	}
	name := *contextName
	if name == "" {
		name = file.CurrentContext
	}
	if name == "" {
		name = "default"
	}
	target := file.Contexts[name]
	if target == nil {
		target = &ContextConfig{}
	}
	baseURL := *apiURL
	if baseURL == "" {
		baseURL = cfg.BaseURL
	}
	if baseURL == "" {
		baseURL = target.APIURL
	}
	if baseURL == "" {
		writeCLIError(cfg.Stderr, "BASE_URL_REQUIRED", "login needs a server: pass --api-url, set MERCATOR_API_URL, or configure the context first")
		return 2
	}
	baseURL = strings.TrimRight(baseURL, "/")

	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	if code := checkLoginAvailable(ctx, cfg, client, baseURL); code != 0 {
		return code
	}

	grant, errCode := browserLogin(ctx, cfg, client, baseURL)
	if errCode != 0 {
		return errCode
	}

	target.APIURL = baseURL
	target.CLIToken = grant.Token
	target.CLITokenEmail = grant.Email
	target.CLITokenExpiresAt = grant.ExpiresAt
	file.Contexts[name] = target
	if file.CurrentContext == "" {
		file.CurrentContext = name
	}
	if err := saveFileConfig(cfg.ConfigPath, file); err != nil {
		writeCLIError(cfg.Stderr, "CONFIG_WRITE_FAILED", err.Error())
		return 1
	}
	writeJSONLine(cfg.Stdout, map[string]any{
		"context":    name,
		"email":      grant.Email,
		"expires_at": grant.ExpiresAt,
	})
	return 0
}

// runLogout clears the login credential from the named (or current) context.
// The stored token is stateless and short-lived server-side; clearing the
// local copy is the logout.
func runLogout(cfg Config, args []string) int {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	contextName := fs.String("context", "", "context to log out of (defaults to the current context)")
	if err := fs.Parse(args); err != nil {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", err.Error())
		return 2
	}
	file, err := loadFileConfig(cfg.ConfigPath)
	if err != nil {
		writeCLIError(cfg.Stderr, "CONFIG_INVALID", err.Error())
		return 1
	}
	name := *contextName
	if name == "" {
		name = file.CurrentContext
	}
	target := file.Contexts[name]
	if name == "" || target == nil {
		writeCLIError(cfg.Stderr, "CONTEXT_NOT_FOUND", "no context to log out of")
		return 1
	}
	target.CLIToken = ""
	target.CLITokenEmail = ""
	target.CLITokenExpiresAt = ""
	if err := saveFileConfig(cfg.ConfigPath, file); err != nil {
		writeCLIError(cfg.Stderr, "CONFIG_WRITE_FAILED", err.Error())
		return 1
	}
	writeJSONLine(cfg.Stdout, map[string]any{"context": name, "logged_out": true})
	return 0
}

// checkLoginAvailable verifies the server actually has OIDC login configured
// before opening a browser at it.
func checkLoginAvailable(ctx context.Context, cfg Config, client *http.Client, baseURL string) int {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/auth/session", nil)
	if err != nil {
		writeCLIError(cfg.Stderr, "INVALID_ARGUMENTS", err.Error())
		return 2
	}
	resp, err := client.Do(req)
	if err != nil {
		writeCLIError(cfg.Stderr, "REQUEST_FAILED", err.Error())
		return 1
	}
	defer resp.Body.Close()
	var state struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil || !state.Enabled {
		writeCLIError(cfg.Stderr, "OIDC_NOT_CONFIGURED", "this server has no OIDC login configured; use MERCATOR_API_TOKEN or `mercator context set --token`")
		return 1
	}
	return 0
}

type cliGrant struct {
	Token     string `json:"token"`
	Email     string `json:"email"`
	ExpiresAt string `json:"expires_at"`
}

// browserLogin performs the loopback-redirect round-trip and the code
// exchange, returning the granted credential.
func browserLogin(ctx context.Context, cfg Config, client *http.Client, baseURL string) (cliGrant, int) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		writeCLIError(cfg.Stderr, "LOOPBACK_FAILED", err.Error())
		return cliGrant{}, 1
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	state := randomToken()

	type callback struct {
		code string
		err  string
	}
	results := make(chan callback, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			results <- callback{err: "loopback state mismatch"}
			return
		}
		code := query.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			results <- callback{err: "loopback callback carried no code"}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><title>Mercator</title><body style=\"font-family: system-ui; padding: 3rem\">Signed in. You can close this window and return to your terminal.</body>")
		results <- callback{code: code}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	authURL := fmt.Sprintf("%s/auth/login?cli_port=%d&cli_state=%s", baseURL, port, url.QueryEscape(state))
	fmt.Fprintf(cfg.Stderr, "Opening your browser to sign in. If it does not open, visit:\n  %s\n", authURL)
	opener := cfg.OpenBrowser
	if opener == nil {
		opener = openBrowser
	}
	if err := opener(authURL); err != nil {
		fmt.Fprintf(cfg.Stderr, "Could not open a browser automatically (%v); open the URL above manually.\n", err)
	}

	var received callback
	select {
	case received = <-results:
	case <-time.After(loginTimeout):
		writeCLIError(cfg.Stderr, "LOGIN_TIMEOUT", "no login completed within the wait window; retry `mercator login`")
		return cliGrant{}, 1
	case <-ctx.Done():
		writeCLIError(cfg.Stderr, "LOGIN_CANCELLED", ctx.Err().Error())
		return cliGrant{}, 1
	}
	if received.err != "" {
		writeCLIError(cfg.Stderr, "LOGIN_FAILED", received.err)
		return cliGrant{}, 1
	}

	body, err := json.Marshal(map[string]string{"code": received.code})
	if err != nil {
		writeCLIError(cfg.Stderr, "LOGIN_FAILED", err.Error())
		return cliGrant{}, 1
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/auth/cli/exchange", strings.NewReader(string(body)))
	if err != nil {
		writeCLIError(cfg.Stderr, "LOGIN_FAILED", err.Error())
		return cliGrant{}, 1
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		writeCLIError(cfg.Stderr, "REQUEST_FAILED", err.Error())
		return cliGrant{}, 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		writeCLIError(cfg.Stderr, "EXCHANGE_FAILED", strings.TrimSpace(string(payload)))
		return cliGrant{}, 1
	}
	var grant cliGrant
	if err := json.NewDecoder(resp.Body).Decode(&grant); err != nil || grant.Token == "" {
		writeCLIError(cfg.Stderr, "EXCHANGE_FAILED", "exchange returned no token")
		return cliGrant{}, 1
	}
	return grant, 0
}

// openBrowser launches the platform browser. Failure is non-fatal; the URL is
// always printed for manual use.
func openBrowser(target string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	default:
		return exec.Command("xdg-open", target).Start()
	}
}
