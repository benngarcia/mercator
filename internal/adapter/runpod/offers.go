package runpod

import (
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/gpunorm"
)

const gib = int64(1024) * 1024 * 1024

// RunPod runs two clouds: SECURE (vetted datacenters) and COMMUNITY (peer
// hosts). Mercator advertises and launches secure capacity only unless the
// connection explicitly opts community capacity in.
const (
	cloudSecure    = "SECURE"
	cloudCommunity = "COMMUNITY"
)

// offerNativeRef encodes the two dimensions a RunPod offer is made of — GPU
// type and cloud — so Launch lands the pod on exactly the capacity the
// scheduler selected.
func offerNativeRef(gpuID, cloud string) string { return gpuID + "|" + cloud }

// splitNativeRef parses an offer native ref back into GPU type and cloud. A
// ref without a cloud tag (e.g. an empty selected offer) defaults to the
// secure cloud — the safe direction; community capacity must be named
// explicitly.
func splitNativeRef(ref string) (gpuID, cloud string) {
	gpuID, cloud, ok := strings.Cut(ref, "|")
	if !ok || cloud == "" {
		return gpuID, cloudSecure
	}
	return gpuID, cloud
}

func stockAvailable(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s != "" && s != "none" && s != "unavailable"
}

func buildOffers(gpus []gpuType, allowlist []string, gpuCount, diskGB int, allowCommunity bool, now time.Time) []domain.OfferSnapshot {
	allowed := make(map[string]bool, len(allowlist))
	for _, a := range allowlist {
		allowed[strings.ToLower(strings.TrimSpace(a))] = true
	}
	offers := make([]domain.OfferSnapshot, 0, len(gpus))
	for _, g := range gpus {
		if !allowed[strings.ToLower(strings.TrimSpace(g.ID))] {
			continue
		}
		offers = appendCloudOffer(offers, g, cloudSecure, g.SecurePrice, g.SecureStockStatus, gpuCount, diskGB, now)
		if allowCommunity {
			offers = appendCloudOffer(offers, g, cloudCommunity, g.CommunityPrice, g.CommunityStockStatus, gpuCount, diskGB, now)
		}
	}
	return offers
}

func appendCloudOffer(offers []domain.OfferSnapshot, g gpuType, cloud string, pricePerGPUHour *float64, stockStatus string, gpuCount, diskGB int, now time.Time) []domain.OfferSnapshot {
	if pricePerGPUHour == nil || !stockAvailable(stockStatus) {
		return offers
	}
	return append(offers, domain.OfferSnapshot{
		ID:         "off_runpod_" + offerSlug(cloud) + "_" + offerSlug(g.ID),
		Kind:       domain.OfferKindProvisionable,
		NativeRef:  offerNativeRef(g.ID, cloud),
		ObservedAt: now,
		ExpiresAt:  now.Add(5 * time.Minute),
		Platform:   domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          8000,
			MemoryBytes:        16 * gib,
			EphemeralDiskBytes: int64(diskGB) * gib,
			Accelerators: []domain.AcceleratorInventory{{
				Vendor:         "NVIDIA",
				Model:          g.DisplayName,
				CanonicalModel: gpunorm.Canonical("NVIDIA", g.DisplayName),
				Count:          gpuCount,
				MemoryBytes:    int64(g.MemoryInGb) * gib,
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
			RatePerSecondUSD:   *pricePerGPUHour * float64(gpuCount) / 3600.0,
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

func offerSlug(id string) string {
	s := strings.ToLower(id)
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
