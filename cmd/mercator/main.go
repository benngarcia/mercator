package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"io"
	stdlog "log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
	runpodadapter "github.com/benngarcia/mercator/internal/adapter/runpod"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/cli"
	"github.com/benngarcia/mercator/internal/connbroker"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/httpapi"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/offers"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/reporting"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/sinks"
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
			Args:        args[1:],
			Stdout:      stdout,
			Stderr:      stderr,
		})
	}
	addr := envValue(env, "MERCATOR_ADDR", "127.0.0.1:8080")
	dsn := envValue(env, "MERCATOR_SQLITE_DSN", "file:/data/mercator.db")
	apiToken, generatedToken, err := apiTokenFromEnv(env)
	if err != nil {
		stdlog.Fatalf("load api token: %v", err)
	}
	if generatedToken {
		stdlog.Printf("generated MERCATOR_API_TOKEN for this server process: %s", apiToken)
	}
	var handler http.Handler
	var closeFn func() error
	if deps, ok := buildServerDeps(env); ok {
		// Mirror HandlerForSQLiteWithAdapter's internal construction but over the
		// SHARED event log, connection.Service, and Broker (the Broker is the
		// adapter), plus the secret store, credential resolver, and verifier.
		sched := scheduler.New()
		orchOpts := []orchestrator.Option{}
		if deps.signer != nil && deps.signer.Enabled() && deps.publicURL != "" {
			orchOpts = append(orchOpts, orchestrator.WithReporting(deps.publicURL, deps.signer))
		}
		orch := orchestrator.New(deps.log, sched, deps.broker, orchOpts...)
		imageResolver := ociresolver.NewStaticResolver(nil, ociresolver.WithSyntheticDigests())
		serverOpts := []httpapi.Option{
			httpapi.WithBearerAuth(apiToken, authWorkspaces(env)),
			httpapi.WithSecretStore(deps.secretStore),
			httpapi.WithCredentialResolver(deps.resolver),
			httpapi.WithVerifier(deps.broker),
		}
		if deps.signer != nil && deps.signer.Enabled() {
			serverOpts = append(serverOpts, httpapi.WithReportSigner(deps.signer))
		}
		handler = httpapi.NewWithAllServices(
			orch, sched, deps.broker,
			workload.New(deps.log),
			sinks.NewManager(deps.log, map[string]sinks.Sink{"audit": sinks.DiscardSink{}}),
			deps.conns,
			offers.New(deps.log),
			imageResolver,
			serverOpts...,
		)
		closeFn = deps.close
	} else {
		handler, closeFn, err = httpapi.HandlerForSQLiteWithOptions(context.Background(), dsn, nil, httpapi.WithBearerAuth(apiToken, authWorkspaces(env)))
		if err != nil {
			stdlog.Fatalf("start mercator: %v", err)
		}
	}
	defer func() {
		if err := closeFn(); err != nil {
			stdlog.Printf("close event log: %v", err)
		}
	}()
	stdlog.Printf("mercator listening on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		stdlog.Fatal(err)
	}
	return 0
}

type offeringAdapter struct {
	adapter.Adapter
	offers []domain.OfferSnapshot
}

func (a offeringAdapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return append([]domain.OfferSnapshot(nil), a.offers...), nil
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
// Returns ok=false for any non-docker adapter (the fake fallback path).
func buildServerDeps(values map[string]string) (serverDeps, bool) {
	if strings.ToLower(values["MERCATOR_ADAPTER"]) != "docker" {
		return serverDeps{}, false
	}
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
	var (
		dockerOnce    sync.Once
		dockerAdapter adapter.Adapter
	)
	factory.Register("docker", func(config map[string]string, _ string) (adapter.Adapter, error) {
		dockerOnce.Do(func() {
			client := dockeradapter.NewCLIClient(config["bin"])
			client.Host = config["host"]
			client.Context = config["context"]
			id := dockerIdentity(values)
			offer := dockerOfferFromInfo(values, id, probeDockerHost(client, id), time.Now().UTC())
			dockerAdapter = offeringAdapter{Adapter: dockeradapter.New(client), offers: []domain.OfferSnapshot{offer}}
		})
		return dockerAdapter, nil
	})

	factory.Register("runpod", func(config map[string]string, secret string) (adapter.Adapter, error) {
		return runpodadapter.New(secret, config)
	})

	br := broker.NewBroker(connbroker.New(svc), factory, resolver)

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
	}, true
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

// dockerEndpointIdentity is the identity Mercator advertises for a Docker
// endpoint: the connection/offer ids and a native ref naming the host. It is
// derived from the endpoint (context or host), not assumed to be local.
type dockerEndpointIdentity struct {
	ConnectionID string
	OfferID      string
	NativeRef    string
	Host         string
	Context      string
}

// dockerIdentity derives the connection/offer identity from the configured
// endpoint. A docker context name wins; otherwise the host portion of a
// DOCKER_HOST URL (ssh://user@HOST, tcp://HOST:port); otherwise "loopback".
// Explicit MERCATOR_DOCKER_{CONNECTION_ID,OFFER_ID,NATIVE_REF} always override.
func dockerIdentity(values map[string]string) dockerEndpointIdentity {
	host := values["MERCATOR_DOCKER_HOST"]
	dockerContext := values["MERCATOR_DOCKER_CONTEXT"]
	label := dockerEndpointLabel(host, dockerContext)
	return dockerEndpointIdentity{
		ConnectionID: envValue(values, "MERCATOR_DOCKER_CONNECTION_ID", "conn_docker_"+label),
		OfferID:      envValue(values, "MERCATOR_DOCKER_OFFER_ID", "offer_docker_"+label),
		NativeRef:    envValue(values, "MERCATOR_DOCKER_NATIVE_REF", label),
		Host:         host,
		Context:      dockerContext,
	}
}

// dockerEndpointLabel produces a short, human-readable token identifying the
// endpoint, used in the connection/offer ids and native ref.
func dockerEndpointLabel(host, dockerContext string) string {
	if dockerContext != "" {
		return dockerContext
	}
	if host == "" {
		return "loopback"
	}
	if u, err := url.Parse(host); err == nil {
		if u.Hostname() != "" {
			return u.Hostname()
		}
		return "loopback" // unix socket or otherwise hostless endpoint
	}
	return "loopback"
}

// probeDockerHost queries the endpoint's `docker info` best-effort. A failed or
// unreachable probe (e.g. a remote host that is down at startup) is not fatal:
// it returns a zero HostInfo and dockerOfferFromInfo falls back to defaults.
func probeDockerHost(client *dockeradapter.CLIClient, id dockerEndpointIdentity) dockeradapter.HostInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := client.Info(ctx)
	if err != nil {
		stdlog.Printf("docker endpoint %q probe failed; using fallback capacity: %v", id.NativeRef, err)
		return dockeradapter.HostInfo{}
	}
	return info
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

// dockerOfferFromInfo builds the offer Mercator advertises for a Docker
// endpoint. Capacity (arch/cpu/mem) comes from the probed `docker info` when
// available, falling back to conservative defaults when the probe was empty
// (unreachable endpoint). An explicit MERCATOR_DOCKER_ARCH always wins, which is
// useful for forcing emulated platforms.
func dockerOfferFromInfo(values map[string]string, id dockerEndpointIdentity, info dockeradapter.HostInfo, now time.Time) domain.OfferSnapshot {
	arch := envValue(values, "MERCATOR_DOCKER_ARCH", "")
	if arch == "" {
		arch = info.OCIArch()
	}
	if arch == "" {
		arch = "amd64"
	}
	cpuMillis := int64(2000)
	if info.NCPU > 0 {
		cpuMillis = int64(info.NCPU) * 1000
	}
	memoryBytes := int64(4 * 1024 * 1024 * 1024)
	if info.MemTotalBytes > 0 {
		memoryBytes = info.MemTotalBytes
	}
	return domain.OfferSnapshot{
		ID:           id.OfferID,
		ConnectionID: id.ConnectionID,
		AdapterType:  "docker",
		Kind:         domain.OfferKindStanding,
		NativeRef:    id.NativeRef,
		ObservedAt:   now,
		ExpiresAt:    now.Add(time.Hour),
		Platform:     domain.Platform{OS: "linux", Architecture: arch},
		Resources: domain.ResourceInventory{
			CPUMillis:          cpuMillis,
			MemoryBytes:        memoryBytes,
			EphemeralDiskBytes: 16 * 1024 * 1024 * 1024,
		},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{
				MaxContainers:       8,
				SupportsDigestRefs:  true,
				MaxEnvironmentBytes: 32768,
			},
			Lifecycle: domain.LifecycleCapabilities{
				IdempotentLaunch: "launch_key",
				ListOwned:        true,
				CancelQueued:     true,
			},
			Network: domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone},
			Pricing: domain.PricingCapabilities{Known: true},
		},
		Network: domain.NetworkFacts{Download: []domain.NetworkFact{{
			Scope:      domain.NetworkScopeRegistry,
			Statistic:  "p10",
			ValueMbps:  100,
			Source:     "local",
			ObservedAt: now,
			ValidUntil: now.Add(time.Hour),
			Confidence: 1,
		}}},
		Pricing: domain.PriceModel{
			Currency:             "USD",
			RatePerSecondUSD:     0,
			MinimumChargeSeconds: 0,
			GranularitySeconds:   1,
			Known:                true,
		},
		ImageCache: domain.ImageCacheEvidence{Known: true},
		Capacity:   domain.CapacityEvidence{Available: true, Confidence: 1},
	}
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
