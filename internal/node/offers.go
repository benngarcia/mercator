package node

import (
	"context"
	"fmt"
	"slices"
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
	platform := domain.Platform{OS: hostOS(host.OS), Architecture: host.Architecture}
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
		Platform:  platform,
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
		Network:  domain.NetworkFacts{Download: host.Network},
		Pricing:  shadowPrice(record),
		Queue:    &domain.QueueSnapshot{},
		Images:   imageInventory(record.Facts, platform),
		Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
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

// imageInventory projects what the node reported holding, in whichever digest
// space its runtime can enumerate. It states what is here rather than what is
// missing, because the node cannot know the size of an image it never pulled,
// and answering that question with a zero is what made every node look fully
// warm. What a Run still owes is the scheduler's subtraction against the
// image's manifest, and only the scheduler holds both halves.
//
// An image counts as held whole only when the build this machine holds is the
// build this machine runs, and only when the machine has unpacked it. A
// multi-platform image is listed under one index digest whichever platform was
// fetched, so a host that pulled the arm64 build reports the same name an amd64
// Run is pinned to, and reading that name alone would price a full 18GB fetch
// as nothing to do. Layers need no such test: they are content-addressed, so
// another platform's layers simply do not match.
//
// An image whose content is here and which cannot start is projected as pulled
// rather than held. Counting only hot images and dropping the rest made partial
// reuse invisible: a host halfway through assembling an image looked exactly
// like one that had never heard of it, and the decision sent an operator after
// a network problem for local work.
func imageInventory(facts capability.NodeFacts, host domain.Platform) domain.ImageInventory {
	inventory := domain.ImageInventory{
		// An enrolled node always enumerates. An empty inventory from a node is
		// the truthful claim that it holds nothing, which is a different fact
		// from a provider that cannot look.
		Known:      !facts.ObservedAt.IsZero(),
		ObservedAt: facts.ObservedAt,
	}
	for _, image := range facts.Images {
		if image.ManifestDigest != "" && image.Platform == host {
			inventory = recordImage(inventory, image)
		}
		inventory.LayerDigests = addNew(inventory.LayerDigests, image.LayerDigests)
		inventory.LayerDiffIDs = addNew(inventory.LayerDiffIDs, image.LayerDiffIDs)
	}
	return inventory
}

// recordImage files one image under what the node established about it: ready
// to run, here and not assembled, or neither. Only a node that says the bytes
// are here files an image as pulled, because that list is what makes the
// scheduler charge local assembly instead of a transfer, and a machine that
// cannot account for content it does not hold would be billed for work it
// cannot do. An image the runtime could not describe says nothing about this
// machine's identity for it, so it is filed nowhere and priced as the pull it
// may well be.
func recordImage(inventory domain.ImageInventory, image capability.ImageLocality) domain.ImageInventory {
	switch {
	case image.State == domain.LocalityHot:
		inventory.ImageDigests = append(inventory.ImageDigests, image.ManifestDigest)
	case image.ContentPresent:
		inventory.PulledImageDigests = append(inventory.PulledImageDigests, image.ManifestDigest)
	}
	return inventory
}

// addNew appends the digests this node reported that are not already listed.
// Layers are shared between images, so the same one arrives once per image
// holding it.
func addNew(known, reported []string) []string {
	for _, digest := range reported {
		if digest != "" && !slices.Contains(known, digest) {
			known = append(known, digest)
		}
	}
	return known
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
