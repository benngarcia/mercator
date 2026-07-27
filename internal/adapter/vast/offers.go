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
			ID:   "off_vast_" + strconv.FormatInt(o.ID, 10),
			Kind: domain.OfferKindProvisionable,
			// The ask ID above is a fresh integer for every search of the same
			// machine, so it is the one field a launch history must never be filed
			// under. The machine behind it is not: Vast sells asks against hardware
			// somebody already owns and publishes that owner's machine ID on every
			// ask, so this is the handle that comes back tomorrow and it is what a
			// history about this candidate is filed under. An ask publishing none
			// falls to the place and the card below, which is all that recurs about
			// a listing whose machine nobody named.
			MachineID: machineHandle(o.MachineID),
			// Where the machine is, which is the level a candidate nobody has
			// measured falls to. Vast sells asks rather than named instance types and
			// so states no InstanceType at all; the accelerator inventory below
			// states the product.
			Region:     o.Geolocation,
			NativeRef:  strconv.FormatInt(o.ID, 10),
			ObservedAt: now,
			ExpiresAt:  now.Add(5 * time.Minute),
			Platform:   domain.Platform{OS: "linux", Architecture: "amd64"},
			Resources: domain.ResourceInventory{
				CPUMillis:          int64(o.CPUCoresEffective * 1000),
				MemoryBytes:        int64(o.CPURAMMb) * mib,
				EphemeralDiskBytes: int64(diskGB) * gib,
				EphemeralDiskKnown: true,
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
			// What Vast has measured about this machine, and only that. The decision
			// records it and nothing prices it: what an interruption costs is a
			// probability times a predicted start, and the flat weight this comment
			// used to name was deleted for being an invented exchange rate.
			Reliability: interruptionHistory(o.Reliability),
			// A catalog listing says this machine type can be had. Its publisher states no
			// confidence in that, and neither does Mercator on their behalf: capacity that
			// may be gone by launch, asserted certain, is a claim nobody made. What would
			// state it here is a measurement of how often provisioning this listing
			// actually succeeds, which nothing collects yet.
			Capacity: domain.CapacityEvidence{Available: true},
			// Vast pulls the image on the rented host; cache state is unknown
			// but the fact must be KNOWN or the scheduler rejects the offer.
			// A fresh instance reports nothing about what it holds, so its inventory
			// is silent rather than empty.
			Images: domain.ImageInventory{Known: false},
		})
	}
	return snapshots
}

// interruptionHistory is what Vast publishes about how a machine behaves.
// reliability2 is its empirical uptime score in [0,1] and its complement is the
// chance the host drops out mid-run, so this history states one rate and not two:
// Vast measures nothing about how often a machine refuses to start, and a zero
// stated at full confidence for that is a claim its publisher never made.
//
// An ask that reports no score at all publishes no history. Silence decoded as a
// score of zero says this machine drops every run and is certain of it, which is
// the worst answer in the catalog invented out of a missing field.
func interruptionHistory(uptimeScore *float64) domain.ReliabilityEvidence {
	if uptimeScore == nil {
		return domain.ReliabilityEvidence{}
	}
	return domain.ReliabilityEvidence{
		Interruptions: domain.StatedRate{Rate: clamp01(1 - *uptimeScore), Confidence: 1},
	}
}

// machineHandle is Vast's own name for the hardware behind an ask, as a handle
// or as nothing at all.
//
// An ask that publishes no machine ID publishes none, and it says so rather than
// being filed under the zero its JSON leaves behind: every unattributed ask in
// the catalog would share that name, and a history under it would answer out of
// a hundred strangers' machines while claiming to be about this exact candidate.
func machineHandle(machineID int64) string {
	if machineID <= 0 {
		return ""
	}
	return strconv.FormatInt(machineID, 10)
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
