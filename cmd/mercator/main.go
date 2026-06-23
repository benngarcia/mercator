package main

import (
	"context"
	"crypto/rand"
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
	"github.com/benngarcia/mercator/internal/cli"
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
	if ad := runtimeAdapter(env); ad != nil {
		handler, closeFn, err = httpapi.HandlerForSQLiteWithAdapter(context.Background(), dsn, ad, httpapi.WithBearerAuth(apiToken, authWorkspaces(env)))
	} else {
		handler, closeFn, err = httpapi.HandlerForSQLiteWithOptions(context.Background(), dsn, fakeOffers(env), httpapi.WithBearerAuth(apiToken, authWorkspaces(env)))
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

func runtimeAdapter(values map[string]string) adapter.Adapter {
	switch strings.ToLower(values["MERCATOR_ADAPTER"]) {
	case "docker":
		// The Docker adapter targets a Docker endpoint, which may be the loopback
		// socket or an arbitrary host reached over tcp:// or ssh://. The endpoint
		// is a property of the connection, not the adapter; "local" is just the
		// default. We probe the endpoint's `docker info` to build an honest offer
		// (arch/cpu/mem) instead of hardcoding it.
		client := dockeradapter.NewCLIClient(values["MERCATOR_DOCKER_BIN"])
		client.Host = values["MERCATOR_DOCKER_HOST"]
		client.Context = values["MERCATOR_DOCKER_CONTEXT"]
		id := dockerIdentity(values)
		offer := dockerOfferFromInfo(values, id, probeDockerHost(client, id), time.Now().UTC())
		return offeringAdapter{Adapter: dockeradapter.New(client), offers: []domain.OfferSnapshot{offer}}
	default:
		return nil
	}
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

func fakeOffers(values map[string]string) []domain.OfferSnapshot {
	mode := values["MERCATOR_FAKE_OFFER"]
	if mode == "" {
		return nil
	}
	now := time.Now().UTC()
	// The fake offer's Kind drives the recorded cleanup disposition end-to-end
	// with no network: a standing offer records disposition=release (the default)
	// and a provisionable offer records disposition=terminate. Set
	// MERCATOR_FAKE_OFFER=provisionable (or "terminate") to exercise the
	// terminate path; any other non-empty value yields the standing/release path.
	kind := domain.OfferKindStanding
	id := "offer_local_fake"
	nativeRef := "fake://local"
	switch mode {
	case "provisionable", "terminate":
		kind = domain.OfferKindProvisionable
		id = "offer_local_fake_provisionable"
		nativeRef = "fake://local/provisionable"
	}
	return []domain.OfferSnapshot{{
		ID:           id,
		ConnectionID: "conn_local_fake",
		AdapterType:  "fake",
		Kind:         kind,
		NativeRef:    nativeRef,
		ObservedAt:   now,
		ExpiresAt:    now.Add(time.Hour),
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          1000,
			MemoryBytes:        1024 * 1024 * 1024,
			EphemeralDiskBytes: 2 * 1024 * 1024 * 1024,
		},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{
				MaxContainers:       1,
				SupportsDigestRefs:  true,
				MaxEnvironmentBytes: 4096,
			},
			Lifecycle: domain.LifecycleCapabilities{
				IdempotentLaunch: "launch_key",
				ListOwned:        true,
				CancelQueued:     true,
			},
			Resources: domain.ResourceCapabilities{},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone},
			Pricing:   domain.PricingCapabilities{Known: true},
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
			RatePerSecondUSD:     0.001,
			MinimumChargeSeconds: 1,
			GranularitySeconds:   1,
			Known:                true,
		},
		ImageCache: domain.ImageCacheEvidence{Known: true, ManifestCached: true},
		Capacity:   domain.CapacityEvidence{Available: true, Confidence: 1},
	}}
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
