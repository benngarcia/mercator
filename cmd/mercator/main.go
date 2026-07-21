package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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

func main() {
	os.Exit(run(context.Background(), os.Args, environ(), os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, env map[string]string, stdout, stderr io.Writer) int {
	if len(args) > 1 && args[1] == "verify" {
		return conformance.RunCommand(ctx, conformance.CommandConfig{
			Args:        args[2:],
			Environment: env,
			Stdout:      stdout,
			Stderr:      stderr,
			RunTrial: func(ctx context.Context, spec conformance.TrialSpec) (conformance.TrialReport, error) {
				return conformance.Run(ctx, spec, conformance.WithEnvironment(env))
			},
		})
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
	return runServer(ctx, env)
}

func runServer(ctx context.Context, env map[string]string) int {
	addr := envValue(env, "MERCATOR_ADDR", "127.0.0.1:8080")
	apiToken, generatedToken, err := apiTokenFromEnv(env)
	if err != nil {
		stdlog.Printf("load api token: %v", err)
		return 1
	}
	if generatedToken {
		stdlog.Printf("generated MERCATOR_API_TOKEN for this server process: %s", apiToken)
	}
	webauthConfig, err := webauth.FromEnv(env)
	if err != nil {
		stdlog.Printf("configure OIDC login: %v", err)
		return 1
	}
	masterKey, err := masterKeyFromEnv(env)
	if err != nil {
		stdlog.Printf("configure server: %v", err)
		return 1
	}

	warnIfNonLoopback(addr)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		stdlog.Printf("listen: %v", err)
		return 1
	}
	runtime, err := daemon.New(ctx, daemon.Config{
		SQLiteDSN:     envValue(env, "MERCATOR_SQLITE_DSN", "file:/data/mercator.db"),
		OperatorToken: apiToken,
		MasterKey:     masterKey,
		PublicURL:     env["MERCATOR_PUBLIC_URL"],
		Getenv:        func(name string) string { return env[name] },
		WebAuth:       webauthConfig,
	})
	if err != nil {
		_ = listener.Close()
		stdlog.Printf("configure server: %v", err)
		return 1
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.Serve(listener) }()
	stopCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	stdlog.Printf("mercator listening on %s", listener.Addr())
	if webauthConfig.Enabled() {
		stdlog.Printf("OIDC login enabled: issuer=%s", webauthConfig.Issuer)
	}

	exitCode := 0
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			stdlog.Printf("serve: %v", err)
			exitCode = 1
		}
	case <-stopCtx.Done():
		stdlog.Printf("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		stdlog.Printf("shutdown: %v", err)
		exitCode = 1
	}
	return exitCode
}

func masterKeyFromEnv(values map[string]string) ([]byte, error) {
	if values["MERCATOR_SECRET_KEY"] == "" {
		return nil, nil
	}
	return keymaterial.Decode("MERCATOR_SECRET_KEY", values["MERCATOR_SECRET_KEY"], 32)
}

func warnIfNonLoopback(addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	if host == "localhost" {
		return
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return
	}
	stdlog.Printf("WARNING: listening on non-loopback address %s over plaintext HTTP; bearer tokens and run data are unencrypted in transit — put a TLS-terminating proxy in front for anything beyond local evaluation", addr)
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
