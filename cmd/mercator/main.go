package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"time"

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
			Args:    args[1:],
			Stdout:  stdout,
			Stderr:  stderr,
		})
	}
	addr := envValue(env, "MERCATOR_ADDR", ":8080")
	dsn := envValue(env, "MERCATOR_SQLITE_DSN", "file:/data/mercator.db")
	handler, closeFn, err := httpapi.HandlerForSQLite(context.Background(), dsn, fakeOffers(env))
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
