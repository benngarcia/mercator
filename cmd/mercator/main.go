package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/cli"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/httpapi"
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
		log.Fatalf("load api token: %v", err)
	}
	if generatedToken {
		log.Printf("generated MERCATOR_API_TOKEN for this server process: %s", apiToken)
	}
	var handler http.Handler
	var closeFn func() error
	br := buildBroker(env)
	if br != nil {
		handler, closeFn, err = httpapi.HandlerForSQLiteWithAdapter(context.Background(), dsn, br, httpapi.WithBearerAuth(apiToken, authWorkspaces(env)))
	} else {
		handler, closeFn, err = httpapi.HandlerForSQLiteWithOptions(context.Background(), dsn, nil, httpapi.WithBearerAuth(apiToken, authWorkspaces(env)))
	}
	if err != nil {
		log.Fatalf("start mercator: %v", err)
	}
	defer func() {
		if err := closeFn(); err != nil {
			log.Printf("close event log: %v", err)
		}
	}()
	log.Printf("mercator listening on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
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

// staticConnections is a minimal broker.Connections that always returns the
// same fixed slice of connection refs, regardless of workspace. Used to
// bootstrap the single docker connection in 1A (the full connection.Service
// adapter lands in Plan 1B).
type staticConnections []broker.ConnRef

func (s staticConnections) List(_ context.Context, _ string) ([]broker.ConnRef, error) {
	return []broker.ConnRef(s), nil
}

// buildBroker constructs a Broker pre-loaded with the docker connection when
// MERCATOR_ADAPTER=docker. Returns nil for any other (or absent) adapter type.
func buildBroker(values map[string]string) *broker.Broker {
	if strings.ToLower(values["MERCATOR_ADAPTER"]) != "docker" {
		return nil
	}
	factory := broker.NewFactory()
	factory.Register("docker", func(config map[string]string, _ string) (adapter.Adapter, error) {
		client := dockeradapter.NewCLIClient(config["bin"])
		client.Host = config["host"]
		client.Context = config["context"]
		id := dockerIdentity(values)
		offer := dockerOfferFromInfo(values, id, probeDockerHost(client, id), time.Now().UTC())
		return offeringAdapter{Adapter: dockeradapter.New(client), offers: []domain.OfferSnapshot{offer}}, nil
	})

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
		credential.NewMemoryStore(),
		masterKey,
	)

	id := dockerIdentity(values)
	conn := broker.ConnRef{
		ID:          id.ConnectionID,
		AdapterType: "docker",
		Authorized:  true,
		Config: map[string]string{
			"bin":     values["MERCATOR_DOCKER_BIN"],
			"host":    values["MERCATOR_DOCKER_HOST"],
			"context": values["MERCATOR_DOCKER_CONTEXT"],
		},
	}
	return broker.NewBroker(staticConnections{conn}, factory, resolver)
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
		log.Printf("docker endpoint %q probe failed; using fallback capacity: %v", id.NativeRef, err)
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
