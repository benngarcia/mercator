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
	"syscall"
	"time"

	"github.com/benngarcia/mercator/internal/cli"
	"github.com/benngarcia/mercator/internal/conformance"
	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/keymaterial"
	"github.com/benngarcia/mercator/internal/webauth"
)

const localDeveloperEmail = "developer@localhost"

type serveOptions struct {
	localAuthEmail string
}

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
	options, err := parseServeOptions(args)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	addr := envValue(env, "MERCATOR_ADDR", "127.0.0.1:8080")
	if options.localAuthEmail != "" && !isLoopbackAddress(addr) {
		stdlog.Printf("configure local login: --dev requires a loopback MERCATOR_ADDR, got %s", addr)
		return 1
	}
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
	if options.localAuthEmail != "" && webauthCfg.Enabled() {
		stdlog.Printf("configure local login: --dev cannot be combined with MERCATOR_OIDC_*")
		return 1
	}
	masterKey, err := masterKeyFromEnv(env)
	if err != nil {
		stdlog.Printf("load secret key: %v", err)
		return 1
	}
	warnIfNonLoopback(addr)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		stdlog.Printf("listen: %v", err)
		return 1
	}
	runtime, err := daemon.New(ctx, daemon.Config{
		SQLiteDSN:      envValue(env, "MERCATOR_SQLITE_DSN", "file:/data/mercator.db"),
		OperatorToken:  apiToken,
		MasterKey:      masterKey,
		PublicURL:      env["MERCATOR_PUBLIC_URL"],
		Getenv:         func(name string) string { return env[name] },
		WebAuth:        webauthCfg,
		LocalAuthEmail: options.localAuthEmail,
	})
	if err != nil {
		_ = listener.Close()
		stdlog.Printf("configure server: %v", err)
		return 1
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

func parseServeOptions(args []string) (serveOptions, error) {
	if len(args) <= 2 {
		return serveOptions{}, nil
	}
	if len(args) == 3 && args[1] == "serve" && args[2] == "--dev" {
		return serveOptions{localAuthEmail: localDeveloperEmail}, nil
	}
	return serveOptions{}, fmt.Errorf("usage: mercator serve [--dev]")
}

func warnIfNonLoopback(addr string) {
	if isLoopbackAddress(addr) {
		return
	}
	stdlog.Printf("WARNING: listening on non-loopback address %s over plaintext HTTP; bearer tokens and run data are unencrypted in transit — put a TLS-terminating proxy in front for anything beyond local evaluation", addr)
}

func isLoopbackAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
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

func envValue(values map[string]string, key, fallback string) string {
	if value := values[key]; value != "" {
		return value
	}
	return fallback
}
