package runpod

import (
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

const gib = int64(1024) * 1024 * 1024

func stockAvailable(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s != "" && s != "none" && s != "unavailable"
}

func buildOffers(gpus []gpuType, allowlist []string, now time.Time) []domain.OfferSnapshot {
	allowed := make(map[string]bool, len(allowlist))
	for _, a := range allowlist {
		allowed[strings.ToLower(strings.TrimSpace(a))] = true
	}
	offers := make([]domain.OfferSnapshot, 0, len(gpus))
	for _, g := range gpus {
		if !allowed[strings.ToLower(strings.TrimSpace(g.ID))] {
			continue
		}
		if !stockAvailable(g.StockStatus) || g.CommunityPrice == nil {
			continue
		}
		offers = append(offers, domain.OfferSnapshot{
			ID:         "off_runpod_" + offerSlug(g.ID),
			Kind:       domain.OfferKindProvisionable,
			NativeRef:  g.ID,
			ObservedAt: now,
			ExpiresAt:  now.Add(5 * time.Minute),
			Platform:   domain.Platform{OS: "linux", Architecture: "amd64"},
			Resources: domain.ResourceInventory{
				CPUMillis:          8000,
				MemoryBytes:        16 * gib,
				EphemeralDiskBytes: 20 * gib,
				Accelerators: []domain.AcceleratorInventory{{
					Vendor:      "NVIDIA",
					Model:       g.DisplayName,
					Count:       1,
					MemoryBytes: int64(g.MemoryInGb) * gib,
				}},
			},
			Capabilities: domain.CapabilityProfile{
				Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: 32768},
				Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "launch_key", ListOwned: true},
				Resources: domain.ResourceCapabilities{GPUVendors: []string{"NVIDIA"}},
				Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkPublicPort, PublicIPv4: true},
				Pricing:   domain.PricingCapabilities{Known: true},
			},
			Pricing: domain.PriceModel{
				Currency:           "USD",
				RatePerSecondUSD:   *g.CommunityPrice / 3600.0,
				GranularitySeconds: 1,
				Known:              true,
			},
			Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
			// RunPod pulls the image fresh on the provisioned host. We don't know
			// the host's cache state, but the fact must be KNOWN (not "unknown")
			// or the scheduler policy rejects the offer with UNKNOWN_FACT. Report
			// a known "not cached" fact (a pull is expected).
			ImageCache: domain.ImageCacheEvidence{Known: true},
		})
	}
	return offers
}

func offerSlug(id string) string {
	s := strings.ToLower(id)
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
