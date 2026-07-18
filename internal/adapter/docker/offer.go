package docker

import (
	"context"
	"log"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

// EndpointIdentity is the identity Mercator advertises for a Docker endpoint:
// the connection/offer ids and a native ref naming the host. It is derived
// from the endpoint (context or host), not assumed to be local.
type EndpointIdentity struct {
	ConnectionID string
	OfferID      string
	NativeRef    string
	Host         string
	Context      string
}

// DeriveIdentity derives the connection/offer identity for a Docker endpoint.
// A docker context name wins; otherwise the host portion of a DOCKER_HOST URL
// (ssh://user@HOST, tcp://HOST:port); otherwise "loopback".
func DeriveIdentity(host, dockerContext string) EndpointIdentity {
	label := endpointLabel(host, dockerContext)
	return EndpointIdentity{
		ConnectionID: "conn_docker_" + label,
		OfferID:      "offer_docker_" + label,
		NativeRef:    label,
		Host:         host,
		Context:      dockerContext,
	}
}

// endpointLabel produces a short, human-readable token identifying the
// endpoint, used in the connection/offer ids and native ref.
func endpointLabel(host, dockerContext string) string {
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

// NewOffering wraps the endpoint's Adapter so ListOffers probes the endpoint
// at call time — capacity, ObservedAt, and ExpiresAt are fresh on every
// placement decision. Building the offer once at adapter construction froze
// those timestamps: after the one-hour expiry window every placement failed
// with OFFER_EXPIRED until the process restarted. A non-empty archOverride
// wins over the probed architecture (useful for forcing emulated platforms).
func NewOffering(client *CLIClient, id EndpointIdentity, archOverride string) adapter.Adapter {
	return offeringAdapter{
		Adapter: New(client),
		client:  client,
		id:      id,
		arch:    archOverride,
		disk:    &probeFact[int64]{},
		gpus:    &probeFact[[]domain.AcceleratorInventory]{},
	}
}

type offeringAdapter struct {
	adapter.Adapter
	client *CLIClient
	id     EndpointIdentity
	arch   string
	disk   *probeFact[int64]
	gpus   *probeFact[[]domain.AcceleratorInventory]
}

func (a offeringAdapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	now := time.Now().UTC()
	info := a.probe()
	diskFree := a.disk.value(a.id.NativeRef, "disk", a.client.DiskFreeBytes, now)
	// Only a daemon with the NVIDIA runtime can satisfy `--gpus`, so CPU-only
	// endpoints skip the GPU probe container entirely and advertise none.
	var accelerators []domain.AcceleratorInventory
	if info.HasNvidiaRuntime() {
		accelerators = a.gpus.value(a.id.NativeRef, "gpu", a.client.AcceleratorInventory, now)
	}
	return []domain.OfferSnapshot{StandingOffer(a.id, a.arch, info, diskFree, accelerators, now)}, nil
}

// probeFact caches a container-probe measurement per endpoint. Offers are
// otherwise rebuilt fresh on every ListOffers call (see NewOffering), but the
// disk and GPU probes launch a one-shot container each, which is too heavy to
// run per placement decision or offers-endpoint poll. Both facts move slowly
// (free disk drifts, GPU inventory is fixed hardware), so a short TTL keeps
// the offer honest without container churn. A failed probe caches the zero
// value: StandingOffer falls back conservatively for disk, and a zero GPU
// inventory means the offer honestly advertises no accelerators.
type probeFact[T any] struct {
	mu         sync.Mutex
	cached     T
	measuredAt time.Time
}

const probeFactTTL = time.Minute

func (p *probeFact[T]) value(nativeRef, fact string, measure func(context.Context) (T, error), now time.Time) T {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.measuredAt.IsZero() && now.Sub(p.measuredAt) < probeFactTTL {
		return p.cached
	}
	// Generous timeout: the first probe on a fresh host also pulls the tiny
	// probe image.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	value, err := measure(ctx)
	if err != nil {
		log.Printf("docker endpoint %q %s probe failed; using fallback: %v", nativeRef, fact, err)
		var zero T
		value = zero
	}
	p.cached = value
	p.measuredAt = now
	return p.cached
}

// probe queries the endpoint's `docker info` best-effort. A failed or
// unreachable probe (e.g. a remote host that is down) is not fatal: it returns
// a zero HostInfo and StandingOffer falls back to conservative defaults.
func (a offeringAdapter) probe() HostInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := a.client.Info(ctx)
	if err != nil {
		log.Printf("docker endpoint %q probe failed; using fallback capacity: %v", a.id.NativeRef, err)
		return HostInfo{}
	}
	return info
}

// StandingOffer builds the offer Mercator advertises for a Docker endpoint.
// Capacity (arch/cpu/mem/disk) comes from the probed endpoint when available,
// falling back to conservative defaults when the probe was empty (unreachable
// endpoint). A non-empty archOverride always wins.
//
// diskFreeBytes is the container-probed free disk (see CLIClient.DiskFreeBytes);
// zero means unmeasured and falls back to 16 GiB. Advertising a hardcoded
// 16 GiB regardless of the real host silently made every workload requesting
// more infeasible ("no feasible offers") even on hosts with hundreds of free
// GiB — bucket's model_training dispatches request >= 20 GiB.
//
// accelerators is the container-probed GPU inventory (see
// CLIClient.AcceleratorInventory); empty means the endpoint advertises none,
// so GPU-requiring workloads are rejected for it, never mis-scheduled.
func StandingOffer(id EndpointIdentity, archOverride string, info HostInfo, diskFreeBytes int64, accelerators []domain.AcceleratorInventory, now time.Time) domain.OfferSnapshot {
	arch := archOverride
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
	ephemeralDiskBytes := int64(16 * 1024 * 1024 * 1024)
	if diskFreeBytes > 0 {
		ephemeralDiskBytes = diskFreeBytes
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
			EphemeralDiskBytes: ephemeralDiskBytes,
			Accelerators:       accelerators,
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
			Resources: domain.ResourceCapabilities{GPUVendors: acceleratorVendors(accelerators)},
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
			RatePerSecondUSD:     0,
			MinimumChargeSeconds: 0,
			GranularitySeconds:   1,
			Known:                true,
		},
		ImageCache: domain.ImageCacheEvidence{Known: true},
		Capacity:   domain.CapacityEvidence{Available: true, Confidence: 1},
	}
}

// acceleratorVendors lists the distinct vendors present in the probed
// inventory, preserving first-seen order (mirrors what the runpod adapter
// advertises in Capabilities.Resources.GPUVendors).
func acceleratorVendors(accelerators []domain.AcceleratorInventory) []string {
	var vendors []string
	for _, accelerator := range accelerators {
		if !slices.Contains(vendors, accelerator.Vendor) {
			vendors = append(vendors, accelerator.Vendor)
		}
	}
	return vendors
}
