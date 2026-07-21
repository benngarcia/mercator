package vast

import (
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/gpunorm"
)

const (
	gib = int64(1024) * 1024 * 1024
	mib = int64(1024) * 1024
)

// secureVerification is Vast's machine verification status required for every
// offer this adapter touches.
const secureVerification = "verified"

// secureOfferQuery builds the /bundles/ search for secure-tier on-demand
// capacity. The tier is HARD-CODED, not configurable: Vast's "Secure Cloud" is
// its certified-datacenter offering (datacenter=true), and machine
// verification must be "verified" on top. Community/peer capacity
// (datacenter=false or unverified machines) is never queried.
func secureOfferQuery(gpuNames []string, gpuCount, diskGB, limit int) map[string]any {
	q := map[string]any{
		"verified":   map[string]any{"eq": true},
		"datacenter": map[string]any{"eq": true},
		"external":   map[string]any{"eq": false},
		"rentable":   map[string]any{"eq": true},
		"rented":     map[string]any{"eq": false},
		"num_gpus":   map[string]any{"eq": gpuCount},
		"disk_space": map[string]any{"gte": float64(diskGB)},
		"type":       "ondemand",
		// allocated_storage sizes the disk the offer is priced for, so
		// dph_total already includes the disk we will request at launch.
		"allocated_storage": float64(diskGB),
		"order":             []any{[]any{"dph_total", "asc"}},
		"limit":             limit,
	}
	if len(gpuNames) > 0 {
		q["gpu_name"] = map[string]any{"in": gpuNames}
	}
	return q
}

func buildOffers(offers []offer, gpuCount, diskGB int, now time.Time) []domain.OfferSnapshot {
	snapshots := make([]domain.OfferSnapshot, 0, len(offers))
	for _, o := range offers {
		// The query already filters on verification; re-check per offer so a
		// server-side filter regression can never advertise unverified capacity.
		if o.Verification != secureVerification {
			continue
		}
		if o.DPHTotal == nil || o.NumGPUs != gpuCount {
			continue
		}
		vendor := gpuVendor(o.GPUArch)
		snapshots = append(snapshots, domain.OfferSnapshot{
			ID:         "off_vast_" + strconv.FormatInt(o.ID, 10),
			Kind:       domain.OfferKindProvisionable,
			NativeRef:  strconv.FormatInt(o.ID, 10),
			ObservedAt: now,
			ExpiresAt:  now.Add(5 * time.Minute),
			Platform:   domain.Platform{OS: "linux", Architecture: "amd64"},
			Resources: domain.ResourceInventory{
				CPUMillis:          int64(o.CPUCoresEffective * 1000),
				MemoryBytes:        int64(o.CPURAMMb) * mib,
				EphemeralDiskBytes: int64(diskGB) * gib,
				Accelerators: []domain.AcceleratorInventory{{
					Vendor:         vendor,
					Model:          o.GPUName,
					CanonicalModel: gpunorm.Canonical(vendor, o.GPUName),
					Count:          o.NumGPUs,
					MemoryBytes:    int64(o.GPURAMMb) * mib,
				}},
			},
			Capabilities: domain.CapabilityProfile{
				Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: 32768},
				Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "launch_key", ListOwned: true},
				Resources: domain.ResourceCapabilities{GPUVendors: []string{vendor}},
				Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkPublicPort, PublicIPv4: true},
				Pricing:   domain.PricingCapabilities{Known: true},
			},
			// dph_total is the all-in on-demand rate for this ask, GPU slice plus
			// the allocated_storage-sized disk, priced per offer by the host.
			Pricing: domain.PriceModel{
				Currency:           "USD",
				RatePerSecondUSD:   *o.DPHTotal / 3600.0,
				GranularitySeconds: 1,
				Known:              true,
			},
			// reliability2 is Vast's empirical machine uptime score in [0,1];
			// its complement is the chance the host drops out mid-run, which is
			// what the scheduler prices via InterruptionPenaltyUSD.
			Reliability: domain.ReliabilityEvidence{
				InterruptionRate: clamp01(1 - o.Reliability),
				Confidence:       1,
			},
			Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
			// Vast pulls the image on the rented host; cache state is unknown
			// but the fact must be KNOWN or the scheduler rejects the offer.
			ImageCache: domain.ImageCacheEvidence{Known: true},
		})
	}
	return snapshots
}

// gpuVendor maps Vast's gpu_arch ("nvidia", "amd") onto the inventory vendor
// spelling. Vast is overwhelmingly NVIDIA; an absent arch defaults there.
func gpuVendor(arch string) string {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "amd":
		return "AMD"
	default:
		return "NVIDIA"
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
