package node

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// AdapterType names node-backed capacity in a Booking Decision. It reads
// differently from a provider's name on purpose: this offer is a machine
// Mercator holds through its own runtime, not a product a provider sells.
const AdapterType = "node"

// ConnectionID is the pseudo-connection every node offer is aggregated under.
// Nodes have no provider credential: their authorization is the enrollment
// they already completed.
const ConnectionID = "connection:nodes"

// Offers presents every ready node as reusable capacity. This is the only
// source of reusable-lane offers today: a node has, by definition, a host
// runtime that can execute a second workload, which is exactly what the lane
// claims.
//
// A node that has gone quiet is not offered. Its workloads still need
// reconciling, but nothing new should be sent to a machine Mercator has
// stopped hearing from.
func (registry *Registry) Offers(ctx context.Context, workspaceID string) ([]domain.OfferSnapshot, error) {
	records, err := registry.store.List(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	now := registry.now().UTC()
	offers := make([]domain.OfferSnapshot, 0, len(records))
	for _, record := range records {
		if !record.Alive(now) {
			continue
		}
		offers = append(offers, registry.offer(record, now))
	}
	return offers, nil
}

// offerFreshness is how long a node offer stays selectable without a newer
// heartbeat. It is a fraction of the lease, so Placement stops choosing a node
// well before the control plane gives up on it: choosing a machine on facts
// that are nearly a lease old is choosing on a guess.
func (registry *Registry) offerFreshness() time.Duration { return registry.lease / 3 }

// Ref resolves a node's current identity, including the generation a command
// must be stamped with. A node that has gone quiet is refused here rather than
// sent work, because the control plane cannot say what it is doing.
func (registry *Registry) Ref(ctx context.Context, workspaceID, nodeID string) (capability.NodeRef, error) {
	record, err := registry.store.Get(ctx, workspaceID, nodeID)
	if err != nil {
		return capability.NodeRef{}, err
	}
	if !record.Alive(registry.now().UTC()) {
		return capability.NodeRef{}, fmt.Errorf(
			"node: %q is %s, so Mercator cannot say what it is running and will not send it more work",
			nodeID, record.State,
		)
	}
	return record.Ref(), nil
}

func (registry *Registry) offer(record Record, now time.Time) domain.OfferSnapshot {
	host := record.Facts.Host
	support := registry.NodeSupport()
	return domain.OfferSnapshot{
		ID:           record.ID,
		RentalID:     record.RentalID,
		ConnectionID: ConnectionID,
		AdapterType:  AdapterType,
		Kind:         domain.OfferKindStanding,
		Lane:         domain.LaneReusable,
		NativeRef:    record.ID,
		ObservedAt:   record.Facts.ObservedAt,
		// An offer built from facts is only as good as the facts. Expiring it
		// on their age is what stops Placement from choosing a machine whose
		// last word is minutes old.
		ExpiresAt: record.Facts.ObservedAt.Add(registry.offerFreshness()),
		Platform:  domain.Platform{OS: hostOS(host.OS), Architecture: host.Architecture},
		Resources: domain.ResourceInventory{
			CPUMillis:          host.CPUMillis,
			MemoryBytes:        host.MemoryBytes,
			EphemeralDiskBytes: host.DiskFreeBytes,
			Accelerators:       host.Accelerators,
		},
		Capabilities: domain.CapabilityProfile{
			OfferKinds: []domain.OfferKind{domain.OfferKindStanding},
			Container: domain.ContainerCapabilities{
				MaxContainers:              support.MaxConcurrentWorkloads,
				SupportsDigestRefs:         true,
				SupportsEntrypointOverride: true,
				MaxEnvironmentBytes:        32768,
			},
			Lifecycle: domain.LifecycleCapabilities{
				// The node deduplicates by operation ID, which is a stronger
				// promise than a provider's launch key: it survives a restart
				// on either side.
				IdempotentLaunch: "operation_id",
				ListOwned:        true,
				CancelQueued:     true,
			},
			Network:       domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone, Protocols: []string{"tcp"}},
			Pricing:       domain.PricingCapabilities{Known: record.ShadowPriceUSDPerHour > 0},
			Observability: domain.ObservabilityCapabilities{Logs: "container"},
		},
		Network:    domain.NetworkFacts{Download: host.Network},
		Pricing:    shadowPrice(record),
		Queue:      &domain.QueueSnapshot{},
		ImageCache: imageCacheEvidence(record.Facts),
		Capacity:   domain.CapacityEvidence{Available: true, Confidence: 1},
	}
}

// shadowPrice is what an owned machine costs Mercator per second, from the
// price the operator configured for it. A node with no configured price has
// unknown pricing, and Placement refuses it loudly rather than treating a
// machine Mercator already pays for as free.
func shadowPrice(record Record) domain.PriceModel {
	if record.ShadowPriceUSDPerHour <= 0 {
		return domain.PriceModel{Currency: "USD", Known: false}
	}
	return domain.PriceModel{
		Currency:         "USD",
		RatePerSecondUSD: record.ShadowPriceUSDPerHour / 3600,
		Known:            true,
	}
}

// imageCacheEvidence summarizes what the node holds. Exact per-image locality
// is the next slice; today an offer states only that the inventory is known,
// which is already more than a provider that cannot say anything.
func imageCacheEvidence(facts capability.NodeFacts) domain.ImageCacheEvidence {
	return domain.ImageCacheEvidence{Known: len(facts.Images) > 0}
}

// hostOS normalizes what a container runtime reports about its host ("Docker
// Desktop", "Ubuntu 24.04") into the platform vocabulary a workload is pinned
// to.
func hostOS(reported string) string {
	lowered := strings.ToLower(reported)
	switch {
	case lowered == "":
		return ""
	case strings.Contains(lowered, "linux"), strings.Contains(lowered, "ubuntu"), strings.Contains(lowered, "debian"):
		return "linux"
	case strings.Contains(lowered, "darwin"), strings.Contains(lowered, "mac"):
		return "darwin"
	case strings.Contains(lowered, "windows"):
		return "windows"
	default:
		return reported
	}
}
