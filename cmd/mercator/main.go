package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
	runpodadapter "github.com/benngarcia/mercator/internal/adapter/runpod"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/cli"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/httpapi"
	"github.com/benngarcia/mercator/internal/janitor"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/reporting"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/sinks"
	"github.com/benngarcia/mercator/internal/webauth"
	"github.com/benngarcia/mercator/internal/workload"
)

func main() {
	os.Exit(run(context.Background(), os.Args, environ(), os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, env map[string]string, stdout, stderr io.Writer) int {
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
		stdlog.Fatalf("load api token: %v", err)
	}
	if generatedToken {
		stdlog.Printf("generated MERCATOR_API_TOKEN for this server process: %s", apiToken)
	}
	// serve always runs the registry-backed Docker broker path. Mirror
	// HandlerForSQLite's internal construction but over the SHARED event log,
	// connection.Service, and Broker (the Broker is the adapter), plus the
	// secret store, credential resolver, and verifier.
	deps := buildServerDeps(env)
	sched := scheduler.New()
	orchOpts := []orchestrator.Option{}
	if deps.signer != nil && deps.signer.Enabled() && deps.publicURL != "" {
		orchOpts = append(orchOpts, orchestrator.WithReporting(deps.publicURL, deps.signer))
	}
	orch := orchestrator.New(deps.log, sched, deps.broker, orchOpts...)
	// No synthetic digests in the served path: a mutable tag must be rejected
	// at create time (registry tag resolution is not implemented), never
	// silently rewritten to a fabricated digest the daemon can't pull.
	imageResolver := ociresolver.NewStaticResolver(nil)
	serverOpts := []httpapi.Option{
		httpapi.WithBearerAuth(apiToken, authWorkspaces(env)),
		httpapi.WithSecretStore(deps.secretStore),
		httpapi.WithCredentialResolver(deps.resolver),
		httpapi.WithVerifier(deps.broker),
	}
	if deps.signer != nil && deps.signer.Enabled() {
		serverOpts = append(serverOpts, httpapi.WithReportSigner(deps.signer))
	}
	// Human login: fail-closed OIDC config. Absent config means no login
	// surface (token-only, exactly as before); partial config refuses to boot;
	// full config must reach the issuer at startup.
	webauthCfg, err := webauth.FromEnv(env)
	if err != nil {
		stdlog.Fatalf("configure OIDC login: %v", err)
	}
	if webauthCfg.Enabled() {
		authenticator, err := webauth.New(ctx, webauthCfg)
		if err != nil {
			stdlog.Fatalf("initialize OIDC login: %v", err)
		}
		serverOpts = append(serverOpts, httpapi.WithWebAuth(authenticator))
		stdlog.Printf("OIDC login enabled: issuer=%s", webauthCfg.Issuer)
	}
	handler := httpapi.New(httpapi.Deps{
		Orchestrator: orch,
		Scheduler:    sched,
		Adapter:      deps.broker,
		Workloads:    workload.New(deps.log),
		Sinks:        sinks.NewManager(deps.log, map[string]sinks.Sink{"audit": sinks.DiscardSink{}}),
		Connections:  deps.conns,
		Resolver:     imageResolver,
	}, serverOpts...)
	closeFn := deps.close
	defer func() {
		if err := closeFn(); err != nil {
			stdlog.Printf("close event log: %v", err)
		}
	}()

	// Background reconciliation: each tick advances every open run's lifecycle
	// (observe container exits, record terminal outcomes, request and confirm
	// cleanup, close the run) and then reclaims orphaned external objects, so
	// runs converge to closed even if no client ever polls them again.
	reconcileCtx, stopReconcile := context.WithCancel(ctx)
	defer stopReconcile()
	go runReconcileSweeps(reconcileCtx, orch, janitor.New(deps.broker, janitor.WithEventLog(deps.log)), bootstrapWorkspaces(env))

	warnIfNonLoopback(addr)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// WriteTimeout must comfortably exceed the 30s :wait long-poll window.
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	stdlog.Printf("mercator listening on %s", addr)
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			stdlog.Printf("serve: %v", err)
			return 1
		}
	case sig := <-stop:
		// Graceful shutdown so in-flight requests finish and the deferred
		// event-log close (WAL checkpoint) actually runs.
		stdlog.Printf("received %s; shutting down", sig)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			stdlog.Printf("shutdown: %v", err)
		}
	}
	return 0
}

// runReconcileSweeps converges every bootstrap workspace on a fixed cadence
// until ctx is cancelled. Sweep errors are logged, never fatal: the next tick
// retries. Run advancement shares the janitor's one-minute cadence: both are
// backstops for the same convergence loop, and a second timer would only add a
// knob without making either sweep more correct.
func runReconcileSweeps(ctx context.Context, orch *orchestrator.Orchestrator, jan *janitor.Janitor, workspaces []string) {
	const interval = time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileWorkspaces(ctx, orch, jan, workspaces)
		}
	}
}

// reconcileWorkspaces performs one reconcile tick. Run advancement goes first
// so a freshly exited container's cleanup is requested, confirmed, and its run
// closed before the janitor looks for leftovers; the janitor stays the backstop
// for objects whose runs cannot advance (or lost their run entirely). A line is
// logged per workspace only when something closed, was reclaimed, or errored,
// so an idle broker does not spam its own log every tick.
func reconcileWorkspaces(ctx context.Context, orch *orchestrator.Orchestrator, jan *janitor.Janitor, workspaces []string) {
	for _, ws := range workspaces {
		advanced, err := orch.AdvanceOpenRuns(ctx, ws)
		if err != nil {
			stdlog.Printf("run advancement sweep %s: %v", ws, err)
		}
		if advanced.Closed > 0 {
			stdlog.Printf("run advancement sweep %s: closed %d of %d open runs", ws, advanced.Closed, advanced.Open)
		}
		result, err := jan.Sweep(ctx, ws)
		if err != nil {
			stdlog.Printf("janitor sweep %s: %v", ws, err)
			continue
		}
		if result.Released > 0 {
			stdlog.Printf("janitor sweep %s: reclaimed %d of %d owned objects", ws, result.Released, result.Found)
		}
	}
}

// warnIfNonLoopback logs a loud warning when the server binds beyond loopback:
// Mercator serves plaintext HTTP, so on any other interface the bearer token
// and run data cross the network unencrypted unless a TLS proxy sits in front.
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

// serverDeps holds the shared backing services for the docker server path. The
// Broker's connection registry and the HTTP server's connection.Service are the
// SAME service over the SAME event log, so offers served by the Broker and
// connections listed by the server stay consistent. The secret store is a
// SECOND *sql.DB opened on the same DSN (the event log's *sql.DB is private).
type serverDeps struct {
	broker      *broker.Broker
	conns       *connection.Service
	secretStore credential.SecretStore
	resolver    *credential.Resolver
	log         eventlog.EventLog
	secretDB    *sql.DB
	// signer is non-nil when MERCATOR_SECRET_KEY is set. It signs per-run
	// reporting tokens using a domain-separated subkey derived from the master key.
	signer *reporting.Signer
	// publicURL is the value of MERCATOR_PUBLIC_URL. Reporting is only enabled
	// when both signer.Enabled() and publicURL != "".
	publicURL string
	close     func() error
}

// buildServerDeps composes the registry-backed Broker, the shared
// connection.Service over one event log, the SQLite secret store, and the
// credential resolver for the docker server path. It registers and authorizes
// the bootstrap docker connection into the service idempotently (Create is
// keyed on connection:create:<id>, so calling it on every boot is safe).
//
// Workspace decision: connections are workspace-scoped but the docker offer is
// global. The bootstrap connection is registered under each concrete workspace
// in authWorkspaces. When auth is the "*" wildcard (any workspace), it is
// registered under the default workspace "ws_1"; full multi-workspace bootstrap
// is future work.
//
// serve uses the Docker host adapter; Docker is a hard requirement.
func buildServerDeps(values map[string]string) serverDeps {
	ctx := context.Background()
	dsn := envValue(values, "MERCATOR_SQLITE_DSN", "file:/data/mercator.db")

	log, err := eventlog.OpenSQLite(ctx, dsn)
	if err != nil {
		stdlog.Fatalf("open event log: %v", err)
	}
	svc := connection.New(log)

	// Second *sql.DB on the same DSN for the secret store; the event log's
	// *sql.DB is private and not shared. This second *sql.DB must see the same data
	// as the event log. This works for on-disk DSNs and ?mode=memory&cache=shared, but
	// a bare ?mode=memory (without shared cache) would give it an isolated, empty DB.
	secretDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		stdlog.Fatalf("open secret store db: %v", err)
	}
	// This pool shares the file with the event log's pool: serialize it and
	// wait out the other writer instead of failing instantly with SQLITE_BUSY.
	secretDB.SetMaxOpenConns(1)
	if _, err := secretDB.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		stdlog.Fatalf("configure secret store db: %v", err)
	}
	store, err := credential.NewSQLiteStore(ctx, secretDB)
	if err != nil {
		stdlog.Fatalf("init secret store: %v", err)
	}

	// Decode master key: try hex then base64; empty env var → nil key.
	var masterKey []byte
	if raw := values["MERCATOR_SECRET_KEY"]; raw != "" {
		if b, err := hex.DecodeString(raw); err == nil {
			masterKey = b
		} else if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
			masterKey = b
		}
	}
	resolver := credential.NewResolver(
		func(k string) string { return values[k] },
		store,
		masterKey,
	)

	// Build the report-token signer from a domain-separated subkey. The signer
	// is always constructed (so its Enabled() reflects key presence); the
	// orchestrator and server only wire it in when publicURL is also set.
	signer := reporting.NewSigner(reporting.DeriveKey(masterKey))
	publicURL := values["MERCATOR_PUBLIC_URL"]

	factory := broker.NewFactory()
	// Build a fresh adapter from each connection's own config: memoizing one
	// instance would route every docker connection to whichever endpoint was
	// built first, silently launching containers on the wrong host.
	factory.Register("docker", func(config map[string]string, _ string) (adapter.Adapter, error) {
		client := dockeradapter.NewCLIClient(config["bin"])
		client.Host = config["host"]
		client.Context = config["context"]
		return dockeradapter.NewOffering(client, dockerIdentityForConfig(values, config), values["MERCATOR_DOCKER_ARCH"]), nil
	})

	factory.Register("runpod", func(config map[string]string, secret string) (adapter.Adapter, error) {
		return runpodadapter.New(secret, config)
	})

	br := broker.NewBroker(svc, factory, resolver)

	// Register + authorize the bootstrap docker connection under each workspace.
	// Create is idempotent by command key, so re-registering on every boot is safe.
	// However, if the docker connection config (bin/host/context) CHANGES between boots,
	// the request hash differs under the same command key → ErrIdempotencyConflict
	// (the server will fatal). This is intentional event-sourced semantics: you cannot
	// silently mutate a connection's config via Create.
	id := dockerIdentity(values)
	cfg := map[string]string{
		"bin":     values["MERCATOR_DOCKER_BIN"],
		"host":    values["MERCATOR_DOCKER_HOST"],
		"context": values["MERCATOR_DOCKER_CONTEXT"],
	}
	for _, ws := range bootstrapWorkspaces(values) {
		if _, err := svc.Create(ctx, connection.CreateRequest{
			WorkspaceID:  ws,
			ConnectionID: id.ConnectionID,
			AdapterType:  "docker",
			Config:       cfg,
		}); err != nil {
			if errors.Is(err, eventlog.ErrIdempotencyConflict) {
				stdlog.Fatalf("register docker connection in %s: the MERCATOR_DOCKER_{BIN,HOST,CONTEXT} config changed since this database registered %q. Connection configs are immutable; set MERCATOR_DOCKER_CONNECTION_ID to a new id for the new endpoint (or point MERCATOR_SQLITE_DSN at a fresh database)", ws, id.ConnectionID)
			}
			stdlog.Fatalf("register docker connection in %s: %v", ws, err)
		}
		if err := svc.UpdateAuthorization(ctx, connection.UpdateAuthorizationRequest{
			WorkspaceID:  ws,
			ConnectionID: id.ConnectionID,
			Authorized:   true,
		}); err != nil {
			stdlog.Fatalf("authorize docker connection in %s: %v", ws, err)
		}
	}

	closeFn := func() error {
		secretErr := secretDB.Close()
		logErr := log.Close()
		if logErr != nil {
			return logErr
		}
		return secretErr
	}

	return serverDeps{
		broker:      br,
		conns:       svc,
		secretStore: store,
		resolver:    resolver,
		log:         log,
		secretDB:    secretDB,
		signer:      signer,
		publicURL:   publicURL,
		close:       closeFn,
	}
}

// bootstrapWorkspaces returns the concrete workspace ids under which the
// bootstrap docker connection is registered. Concrete ids from authWorkspaces
// are used directly; the "*" wildcard maps to the default workspace "ws_1".
func bootstrapWorkspaces(values map[string]string) []string {
	var out []string
	for _, ws := range authWorkspaces(values) {
		if ws == "*" {
			continue
		}
		out = append(out, ws)
	}
	if len(out) == 0 {
		return []string{"ws_1"}
	}
	return out
}

// dockerIdentityForConfig derives the offer identity for one docker
// connection's config. The bootstrap endpoint (registered from MERCATOR_DOCKER_*
// env on boot) keeps its env-driven identity, including the explicit
// MERCATOR_DOCKER_{CONNECTION_ID,OFFER_ID,NATIVE_REF} overrides; any other
// docker connection (added later via POST /v1/connections) derives its identity
// from its own endpoint so two connections never share an offer id.
func dockerIdentityForConfig(values, config map[string]string) dockeradapter.EndpointIdentity {
	if config["host"] == values["MERCATOR_DOCKER_HOST"] && config["context"] == values["MERCATOR_DOCKER_CONTEXT"] {
		return dockerIdentity(values)
	}
	return dockeradapter.DeriveIdentity(config["host"], config["context"])
}

// dockerIdentity derives the bootstrap endpoint's identity from the
// MERCATOR_DOCKER_{HOST,CONTEXT} env, honoring the explicit
// MERCATOR_DOCKER_{CONNECTION_ID,OFFER_ID,NATIVE_REF} overrides.
func dockerIdentity(values map[string]string) dockeradapter.EndpointIdentity {
	id := dockeradapter.DeriveIdentity(values["MERCATOR_DOCKER_HOST"], values["MERCATOR_DOCKER_CONTEXT"])
	id.ConnectionID = envValue(values, "MERCATOR_DOCKER_CONNECTION_ID", id.ConnectionID)
	id.OfferID = envValue(values, "MERCATOR_DOCKER_OFFER_ID", id.OfferID)
	id.NativeRef = envValue(values, "MERCATOR_DOCKER_NATIVE_REF", id.NativeRef)
	return id
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

func authWorkspaces(values map[string]string) []string {
	raw := values["MERCATOR_AUTH_WORKSPACES"]
	if raw == "" {
		return []string{"*"}
	}
	var workspaces []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			workspaces = append(workspaces, part)
		}
	}
	if len(workspaces) == 0 {
		return []string{"*"}
	}
	return workspaces
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
