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

	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
	runpodadapter "github.com/benngarcia/mercator/internal/adapter/runpod"
	shadeformadapter "github.com/benngarcia/mercator/internal/adapter/shadeform"
	vastadapter "github.com/benngarcia/mercator/internal/adapter/vast"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/cli"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/httpapi"
	"github.com/benngarcia/mercator/internal/janitor"
	"github.com/benngarcia/mercator/internal/keymaterial"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/reporting"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/sinks"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/webauth"
	"github.com/benngarcia/mercator/internal/workload"
	"github.com/benngarcia/mercator/internal/workspace"
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
		stdlog.Printf("load api token: %v", err)
		return 1
	}
	if generatedToken {
		stdlog.Printf("generated MERCATOR_API_TOKEN for this server process: %s", apiToken)
	}
	// Human login: fail-closed OIDC config. Absent config means no login
	// surface (token-only, exactly as before); partial config refuses to boot;
	// full config must reach the issuer at startup.
	webauthCfg, err := webauth.FromEnv(env)
	if err != nil {
		stdlog.Printf("configure OIDC login: %v", err)
		return 1
	}
	// serve always runs the registry-backed Docker broker path. Mirror
	// HandlerForSQLite's internal construction but over the SHARED event log,
	// connection.Service, and Broker (the Broker is the adapter), plus the
	// secret store, credential resolver, and verifier.
	deps, err := buildServerDeps(env)
	if err != nil {
		stdlog.Printf("configure server: %v", err)
		return 1
	}
	defer func() {
		if err := deps.close(); err != nil {
			stdlog.Printf("close event log: %v", err)
		}
	}()
	sched := scheduler.New()
	orchOpts := []orchestrator.Option{}
	if deps.signer != nil && deps.signer.Enabled() && deps.publicURL != "" {
		orchOpts = append(orchOpts, orchestrator.WithReporting(deps.publicURL, deps.signer))
	}
	orch := orchestrator.New(deps.log, sched, deps.broker, deps.workspaces, orchOpts...)
	// No synthetic digests in the served path: a mutable tag must be rejected
	// at create time (registry tag resolution is not implemented), never
	// silently rewritten to a fabricated digest the daemon can't pull.
	imageResolver := ociresolver.NewStaticResolver(nil)
	serverOpts := []httpapi.Option{
		httpapi.WithBearerAuth(apiToken),
		httpapi.WithVerifier(deps.broker),
		httpapi.WithAdapterManifests(deps.broker.Manifests),
	}
	if deps.signer != nil && deps.signer.Enabled() {
		serverOpts = append(serverOpts, httpapi.WithReportSigner(deps.signer))
	}
	if webauthCfg.Enabled() {
		authenticator, err := webauth.New(ctx, webauthCfg)
		if err != nil {
			stdlog.Printf("initialize OIDC login: %v", err)
			return 1
		}
		serverOpts = append(serverOpts, httpapi.WithWebAuth(authenticator))
		stdlog.Printf("OIDC login enabled: issuer=%s", webauthCfg.Issuer)
	}
	handler := httpapi.New(httpapi.Deps{
		Orchestrator: orch,
		Offers:       deps.broker,
		Workloads:    workload.New(deps.log, deps.workspaces),
		Sinks:        sinks.NewManager(deps.log, map[string]sinks.Sink{"audit": sinks.DiscardSink{}}),
		Connections:  deps.conns,
		Resolver:     imageResolver,
		Workspaces:   deps.workspaces,
	}, serverOpts...)
	// Background reconciliation: each tick advances every open run's lifecycle
	// (observe container exits, record terminal outcomes, request and confirm
	// cleanup, close the run) and then reclaims orphaned external objects, so
	// runs converge to closed even if no client ever polls them again.
	reconcileCtx, stopReconcile := context.WithCancel(ctx)
	defer stopReconcile()
	go runReconcileSweeps(reconcileCtx, orch, janitor.New(deps.broker, janitor.WithEventLog(deps.log)))

	warnIfNonLoopback(addr)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// WriteTimeout must comfortably exceed the 30s /wait long-poll window.
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

// runReconcileSweeps converges every workspace recorded in run history on a fixed cadence
// until ctx is cancelled. Sweep errors are logged, never fatal: the next tick
// retries. Run advancement shares the janitor's one-minute cadence: both are
// backstops for the same convergence loop, and a second timer would only add a
// knob without making either sweep more correct.
func runReconcileSweeps(ctx context.Context, orch *orchestrator.Orchestrator, jan *janitor.Janitor) {
	const interval = time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileWorkspaces(ctx, orch, jan)
		}
	}
}

// reconcileWorkspaces performs one reconcile tick. Run advancement goes first
// so a freshly exited container's cleanup is requested, confirmed, and its run
// closed before the janitor looks for leftovers; the janitor stays the backstop
// for objects whose runs cannot advance (or lost their run entirely). A line is
// logged per workspace only when something closed, was reclaimed, or errored,
// so an idle broker does not spam its own log every tick.
func reconcileWorkspaces(ctx context.Context, orch *orchestrator.Orchestrator, jan *janitor.Janitor) {
	workspaces, err := orch.ListRunWorkspaces(ctx)
	if err != nil {
		stdlog.Printf("discover run workspaces: %v", err)
		return
	}
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
// connections listed by the server stay consistent. Connection events and
// sealed credentials share the event log's SQLite transaction boundary.
type serverDeps struct {
	broker     *broker.Broker
	conns      *connection.Service
	log        eventlog.EventLog
	workspaces *workspace.SQLiteCatalog
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
// credential resolver. Connections are event-sourced domain records created
// and authorized through the API; process startup never invents one.
func buildServerDeps(values map[string]string) (deps serverDeps, err error) {
	ctx := context.Background()
	dsn := envValue(values, "MERCATOR_SQLITE_DSN", "file:/data/mercator.db")

	storage, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		return serverDeps{}, fmt.Errorf("open sqlite storage: %w", err)
	}
	defer func() {
		if err != nil {
			_ = storage.Close()
		}
	}()
	log := storage.EventLog()
	store := storage.CredentialStore()

	// An absent master key disables stored credentials and reporting. A present
	// key is configuration and therefore must decode successfully at startup.
	var masterKey []byte
	if raw := values["MERCATOR_SECRET_KEY"]; raw != "" {
		masterKey, err = keymaterial.Decode("MERCATOR_SECRET_KEY", raw, 32)
		if err != nil {
			return serverDeps{}, err
		}
	}
	// Sealed blobs are keyed by an HKDF subkey of the master key. Re-seal any
	// pre-HKDF rows; a row no key opens means the configured MERCATOR_SECRET_KEY
	// is not the key this store was written with — refuse to boot rather than
	// fail at first credential use.
	var migrated int
	migrated, err = store.MigrateSealKey(ctx, masterKey)
	if err != nil {
		return serverDeps{}, fmt.Errorf("credential store: %w", err)
	}
	if migrated > 0 {
		stdlog.Printf("credential store: re-sealed %d credential(s) under the derived sealing key", migrated)
	}

	resolver := credential.NewResolver(
		func(k string) string { return values[k] },
		store,
		masterKey,
	)
	connections, err := storage.Connections(resolver)
	if err != nil {
		return serverDeps{}, fmt.Errorf("init connection storage: %w", err)
	}
	svc := connection.NewWithCredentials(connections, storage.Workspaces())

	// Build the report-token signer from a domain-separated subkey. The signer
	// is always constructed (so its Enabled() reflects key presence); the
	// orchestrator and server only wire it in when publicURL is also set.
	signer := reporting.NewSigner(reporting.DeriveKey(masterKey))
	publicURL := values["MERCATOR_PUBLIC_URL"]

	factory := broker.NewFactory()
	// Build a fresh adapter from each connection's own config: memoizing one
	// instance would route every docker connection to whichever endpoint was
	// built first, silently launching containers on the wrong host.
	factory.Register(dockeradapter.Manifest(), func(config map[string]string, secret string) (adapter.Provider, error) {
		return newDockerAdapter(config, secret)
	})

	factory.Register(runpodadapter.Manifest(), func(config map[string]string, secret string) (adapter.Provider, error) {
		return runpodadapter.New(secret, config)
	})

	factory.Register(shadeformadapter.Manifest(), func(config map[string]string, secret string) (adapter.Provider, error) {
		return shadeformadapter.New(secret, config)
	})

	factory.Register(vastadapter.Manifest(), func(config map[string]string, secret string) (adapter.Provider, error) {
		return vastadapter.New(secret, config)
	})

	br := broker.NewBroker(svc, factory, resolver)

	return serverDeps{
		broker:     br,
		conns:      svc,
		log:        log,
		workspaces: storage.Workspaces(),
		signer:     signer,
		publicURL:  publicURL,
		close:      storage.Close,
	}, nil
}

func newDockerAdapter(config map[string]string, secret string) (adapter.Provider, error) {
	registry, err := dockeradapter.NewRegistryCredential(config["registry_server"], config["registry_username"], secret)
	if err != nil {
		return nil, err
	}
	client := dockeradapter.NewCLIClient(config["bin"])
	client.Host = config["host"]
	client.Context = config["context"]
	client.Registry = registry
	return dockeradapter.NewOffering(client, dockerIdentityForConfig(config), config["arch"]), nil
}

// dockerIdentityForConfig derives the offer identity from the connection's
// endpoint, so distinct Docker connections cannot advertise the same offer.
func dockerIdentityForConfig(config map[string]string) dockeradapter.EndpointIdentity {
	return dockeradapter.DeriveIdentity(config["host"], config["context"])
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
