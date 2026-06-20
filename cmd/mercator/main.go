package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bengarcia/mercator/internal/adapter"
	dockeradapter "github.com/bengarcia/mercator/internal/adapter/docker"
	"github.com/bengarcia/mercator/internal/cli"
	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/httpapi"
)

func main() {
	os.Exit(run(context.Background(), os.Args, environ(), os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, env map[string]string, stdout, stderr io.Writer) int {
	if len(args) > 1 && args[1] != "serve" {
		return cli.Run(ctx, cli.Config{
			BaseURL: envValue(env, "MERCATOR_API_URL", ""),
			Token:   envValue(env, "MERCATOR_API_TOKEN", ""),
			Args:    args[1:],
			Stdout:  stdout,
			Stderr:  stderr,
		})
	}
	addr := envValue(env, "MERCATOR_ADDR", "127.0.0.1:8080")
	dsn := envValue(env, "MERCATOR_SQLITE_DSN", "file:/data/mercator.db")
	secretKey, err := secretKeyFromEnv(env)
	if err != nil {
		log.Fatalf("load secret key: %v", err)
	}
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
		handler, closeFn, err = httpapi.HandlerForSQLiteWithAdapter(context.Background(), dsn, ad, secretKey, httpapi.WithBearerAuth(apiToken, authWorkspaces(env)))
	} else {
		handler, closeFn, err = httpapi.HandlerForSQLiteWithOptions(context.Background(), dsn, fakeOffers(env), secretKey, httpapi.WithBearerAuth(apiToken, authWorkspaces(env)))
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
		return offeringAdapter{Adapter: dockeradapter.New(dockeradapter.NewCLIClient(values["MERCATOR_DOCKER_BIN"])), offers: []domain.OfferSnapshot{dockerOffer(values)}}
	default:
		return nil
	}
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

func secretKeyFromEnv(values map[string]string) ([]byte, error) {
	encoded := values["MERCATOR_SECRET_KEY_HEX"]
	if encoded == "" {
		return nil, nil
	}
	key, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func dockerOffer(values map[string]string) domain.OfferSnapshot {
	now := time.Now().UTC()
	nativeRef := envValue(values, "MERCATOR_DOCKER_NATIVE_REF", "local")
	return domain.OfferSnapshot{
		ID:           envValue(values, "MERCATOR_DOCKER_OFFER_ID", "offer_local_docker"),
		ConnectionID: envValue(values, "MERCATOR_DOCKER_CONNECTION_ID", "conn_local_docker"),
		AdapterType:  "docker",
		Kind:         domain.OfferKindStanding,
		NativeRef:    nativeRef,
		ObservedAt:   now,
		ExpiresAt:    now.Add(time.Hour),
		Platform:     domain.Platform{OS: "linux", Architecture: envValue(values, "MERCATOR_DOCKER_ARCH", "amd64")},
		Resources: domain.ResourceInventory{
			CPUMillis:          2000,
			MemoryBytes:        4 * 1024 * 1024 * 1024,
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
	if values["MERCATOR_FAKE_OFFER"] == "" {
		return nil
	}
	now := time.Now().UTC()
	return []domain.OfferSnapshot{{
		ID:           "offer_local_fake",
		ConnectionID: "conn_local_fake",
		AdapterType:  "fake",
		Kind:         domain.OfferKindStanding,
		NativeRef:    "fake://local",
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
