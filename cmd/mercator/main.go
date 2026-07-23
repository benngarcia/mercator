package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/benngarcia/mercator/internal/cli"
	"github.com/benngarcia/mercator/internal/conformance"
	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/keymaterial"
	"github.com/benngarcia/mercator/internal/webauth"
)

func main() {
	os.Exit(run(context.Background(), os.Args, environ(), os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, env map[string]string, stdout, stderr io.Writer) int {
	if len(args) > 2 && args[1] == "help" && args[2] == "verify" {
		return conformance.RunCommand(ctx, []string{"--help"}, env, stdout, stderr)
	}
	if len(args) > 1 && args[1] == "verify" {
		return conformance.RunCommand(ctx, args[2:], env, stdout, stderr)
	}
	if len(args) > 1 && args[1] != "serve" {
		return cli.Run(ctx, cli.Config{
			BaseURL:     envValue(env, "MERCATOR_API_URL", ""),
			Token:       envValue(env, "MERCATOR_API_TOKEN", ""),
			WorkspaceID: envValue(env, "MERCATOR_WORKSPACE_ID", ""),
			ConfigPath:  cli.DefaultConfigPath(env),
			Args:        args[1:],
			Stdout:      stdout,
			Stderr:      stderr,
		})
	}
	addr := envValue(env, "MERCATOR_ADDR", "127.0.0.1:8080")
	apiToken, generatedToken, err := apiTokenFromEnv(env)
	if err != nil {
		stdlog.Printf("load api token: %v", err)
		return 1
	}
	if generatedToken {
		stdlog.Printf("generated MERCATOR_API_TOKEN for this server process: %s", apiToken)
	}
	webauthCfg, err := webauth.FromEnv(env)
	if err != nil {
		stdlog.Printf("configure OIDC login: %v", err)
		return 1
	}
	masterKey, err := masterKeyFromEnv(env)
	if err != nil {
		stdlog.Printf("load secret key: %v", err)
		return 1
	}
	if !isLoopback(addr) {
		stdlog.Printf("WARNING: listening on non-loopback address %s over plaintext HTTP; bearer tokens and run data are unencrypted in transit — put a TLS-terminating proxy in front for anything beyond local evaluation", addr)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		stdlog.Printf("listen: %v", err)
		return 1
	}
	dsn, err := sqliteDSN(env)
	if err != nil {
		_ = listener.Close()
		stdlog.Printf("resolve database path: %v", err)
		return 1
	}
	runtime, err := daemon.New(ctx, daemon.Config{
		SQLiteDSN:     dsn,
		OperatorToken: apiToken,
		MasterKey:     masterKey,
		PublicURL:     env["MERCATOR_PUBLIC_URL"],
		Getenv:        func(name string) string { return env[name] },
		WebAuth:       webauthCfg,
	})
	if err != nil {
		_ = listener.Close()
		stdlog.Printf("configure server: %v", err)
		return 1
	}
	// A loopback broker holding a token only this process knows is unusable
	// until the CLI learns it. Write it down rather than making the operator
	// copy it out of the log.
	if generatedToken && isLoopback(addr) {
		shareLocalContext(env, addr, apiToken)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.Serve(listener) }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	stdlog.Printf("mercator listening on %s", addr)
	exitCode := 0
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			stdlog.Printf("serve: %v", err)
			exitCode = 1
		}
	case sig := <-stop:
		stdlog.Printf("received %s; shutting down", sig)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		stdlog.Printf("shutdown: %v", err)
		return 1
	}
	return exitCode
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// shareLocalContext hands this machine's CLI the address and token of the
// server just started. Failing to write it is not fatal: the token is already
// in the log above, so the operator can still export it by hand.
func shareLocalContext(env map[string]string, addr, token string) {
	path := cli.DefaultConfigPath(env)
	changed, err := cli.WriteLocalContext(path, "http://"+addr, token)
	if err != nil {
		stdlog.Printf("could not write the %q CLI context (%v); export MERCATOR_API_TOKEN instead", cli.LocalContextName, err)
		return
	}
	if changed {
		stdlog.Printf("wrote the %q CLI context to %s; mercator commands on this machine need no further setup", cli.LocalContextName, path)
	}
}

func masterKeyFromEnv(values map[string]string) ([]byte, error) {
	raw := values["MERCATOR_SECRET_KEY"]
	if raw == "" {
		return nil, nil
	}
	return keymaterial.Decode("MERCATOR_SECRET_KEY", raw, 32)
}

func apiTokenFromEnv(values map[string]string) (string, bool, error) {
	if token := values["MERCATOR_API_TOKEN"]; token != "" {
		return token, false, nil
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(bytes), true, nil
}

func environ() map[string]string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		for i, char := range entry {
			if char == '=' {
				values[entry[:i]] = entry[i+1:]
				break
			}
		}
	}
	return values
}

// sqliteDSN resolves where the event log lives. An operator who names a path
// gets exactly that path. Everyone else gets a per-user data directory, which
// this creates, because a server that cannot start until you invent a database
// location is a server nobody can try. The container image sets the variable
// explicitly, so it keeps its own /data volume.
func sqliteDSN(env map[string]string) (string, error) {
	if dsn := env["MERCATOR_SQLITE_DSN"]; dsn != "" {
		return dsn, nil
	}
	base := env["XDG_DATA_HOME"]
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("no MERCATOR_SQLITE_DSN and no home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(base, "mercator")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return "file:" + filepath.Join(dir, "mercator.db"), nil
}

func envValue(values map[string]string, key, fallback string) string {
	if value := values[key]; value != "" {
		return value
	}
	return fallback
}
