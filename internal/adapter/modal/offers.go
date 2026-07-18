package modal

import (
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/gpunorm"
)

const (
	gib = int64(1024) * 1024 * 1024
	mib = int64(1024) * 1024
)

// Modal publishes fixed on-demand rates (https://modal.com/pricing, July
// 2026); there is no pricing API. Rates are USD per second. A GPU type
// missing here still yields an offer, priced Known=false so the default
// scheduling policy rejects it unless the workload allows unknown pricing.
var gpuRatePerSecondUSD = map[string]float64{
	"t4":        0.000164,
	"l4":        0.000222,
	"a10":       0.000306,
	"a10g":      0.000306,
	"l40s":      0.000542,
	"a100":      0.000583,
	"a100-40gb": 0.000583,
	"a100-80gb": 0.000694,
	"h100":      0.001097,
	"h200":      0.001261,
	"b200":      0.001736,
}

const (
	cpuRatePerCoreSecondUSD  = 0.0000131
	memoryRatePerGiBSecond   = 0.00000222
	defaultOfferCPUMillis    = int64(8000)
	defaultOfferMemoryBytes  = 16 * gib
	defaultOfferDiskBytes    = 20 * gib
	offerTTL                 = 5 * time.Minute
	maxSandboxEnvironmentLen = 32768
)

// gpuMemoryBytes is the advertised VRAM per Modal GPU type. Unknown types
// report 0 (unknown) rather than a guess.
var gpuMemoryBytes = map[string]int64{
	"t4":        16 * gib,
	"l4":        24 * gib,
	"a10":       24 * gib,
	"a10g":      24 * gib,
	"l40s":      48 * gib,
	"a100":      40 * gib,
	"a100-40gb": 40 * gib,
	"a100-80gb": 80 * gib,
	"h100":      80 * gib,
	"h200":      141 * gib,
	"b200":      180 * gib,
}

func isCPURef(ref string) bool { return strings.EqualFold(strings.TrimSpace(ref), "cpu") }

func wantsAccelerator(res domain.ResourceRequirements) bool {
	for _, acc := range res.Accelerators {
		if acc.Count > 0 {
			return true
		}
	}
	return false
}

func requestedGPUCount(res domain.ResourceRequirements) int {
	for _, acc := range res.Accelerators {
		if acc.Count > 0 {
			return acc.Count
		}
	}
	return 1
}

func requestedCPUMillis(res domain.ResourceRequirements) int64 {
	if res.CPU.MinMillis > 0 {
		return res.CPU.MinMillis
	}
	return defaultOfferCPUMillis
}

func requestedMemoryBytes(res domain.ResourceRequirements) int64 {
	if res.Memory.MinBytes > 0 {
		return res.Memory.MinBytes
	}
	return defaultOfferMemoryBytes
}

func requestedDiskBytes(res domain.ResourceRequirements) int64 {
	if res.EphemeralDisk.MinBytes > 0 {
		return res.EphemeralDisk.MinBytes
	}
	return defaultOfferDiskBytes
}

// buildOffers synthesizes one catalog offer per configured GPU type (plus the
// special "cpu" entry for CPU-only sandboxes). Modal is serverless: these are
// catalog entries describing what a launch would provision, not live capacity.
func buildOffers(gpuTypes []string, res domain.ResourceRequirements, now time.Time) []domain.OfferSnapshot {
	gpuCount := requestedGPUCount(res)
	cpuMillis := requestedCPUMillis(res)
	memoryBytes := requestedMemoryBytes(res)
	diskBytes := requestedDiskBytes(res)

	offers := make([]domain.OfferSnapshot, 0, len(gpuTypes))
	for _, gpuType := range gpuTypes {
		key := strings.ToLower(strings.TrimSpace(gpuType))
		cpuOnly := isCPURef(gpuType)

		rate, rateKnown := 0.0, false
		if cpuOnly {
			rate, rateKnown = 0, true
		} else if gpuRate, ok := gpuRatePerSecondUSD[key]; ok {
			rate, rateKnown = gpuRate*float64(gpuCount), true
		}
		if rateKnown {
			rate += cpuRatePerCoreSecondUSD * float64(cpuMillis) / 1000
			rate += memoryRatePerGiBSecond * float64(memoryBytes) / float64(gib)
		}

		var accelerators []domain.AcceleratorInventory
		var gpuVendors []string
		if !cpuOnly {
			accelerators = []domain.AcceleratorInventory{{
				Vendor:         "NVIDIA",
				Model:          gpuType,
				CanonicalModel: gpunorm.Canonical("NVIDIA", gpuType),
				Count:          gpuCount,
				MemoryBytes:    gpuMemoryBytes[key],
			}}
			gpuVendors = []string{"NVIDIA"}
		}

		offers = append(offers, domain.OfferSnapshot{
			ID:         "off_modal_" + offerSlug(gpuType),
			Kind:       domain.OfferKindProvisionable,
			NativeRef:  gpuType,
			ObservedAt: now,
			ExpiresAt:  now.Add(offerTTL),
			Platform:   domain.Platform{OS: "linux", Architecture: "amd64"},
			Resources: domain.ResourceInventory{
				CPUMillis:          cpuMillis,
				MemoryBytes:        memoryBytes,
				EphemeralDiskBytes: diskBytes,
				Accelerators:       accelerators,
			},
			Capabilities: domain.CapabilityProfile{
				Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: maxSandboxEnvironmentLen},
				Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "launch_key", ListOwned: true, ProviderTTL: true, CancelQueued: true},
				Resources: domain.ResourceCapabilities{GPUVendors: gpuVendors},
				Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone},
				Pricing:   domain.PricingCapabilities{Known: rateKnown},
			},
			Pricing: domain.PriceModel{
				Currency:           "USD",
				RatePerSecondUSD:   rate,
				GranularitySeconds: 1,
				Known:              rateKnown,
			},
			// Serverless: Modal provisions on demand, so availability is a
			// property of the platform rather than observed stock.
			Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
			// Modal builds and caches the image on its own infrastructure; the
			// fact must be KNOWN or the scheduler policy rejects the offer. A
			// first launch pulls, so report a known "not cached" fact.
			ImageCache: domain.ImageCacheEvidence{Known: true},
		})
	}
	return offers
}

func offerSlug(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
