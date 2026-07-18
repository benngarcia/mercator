package docker

import (
	"context"
	"log"
	"net/url"
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
	return offeringAdapter{Adapter: New(client), client: client, id: id, arch: archOverride}
}

type offeringAdapter struct {
	adapter.Adapter
	client *CLIClient
	id     EndpointIdentity
	arch   string
}

func (a offeringAdapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return []domain.OfferSnapshot{StandingOffer(a.id, a.arch, a.probe(), time.Now().UTC())}, nil
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

// ephemeralDiskUnconstrained (1 TiB) is the docker offer's ephemeral-disk
// inventory: an explicit "not a quota" value large enough that no sane
// workload request is rejected at placement on a resource this adapter
// does not actually partition.
const ephemeralDiskUnconstrained = int64(1) << 40

// StandingOffer builds the offer Mercator advertises for a Docker endpoint.
// Capacity (arch/cpu/mem) comes from the probed `docker info` when available,
// falling back to conservative defaults when the probe was empty (unreachable
// endpoint). A non-empty archOverride always wins.
func StandingOffer(id EndpointIdentity, archOverride string, info HostInfo, now time.Time) domain.OfferSnapshot {
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
			CPUMillis:   cpuMillis,
			MemoryBytes: memoryBytes,
			// Docker containers share the daemon's filesystem: the adapter
			// cannot reserve or partition disk per container, and `docker
			// info` does not report free space, so the inventory is
			// deliberately non-constraining. The previous fabricated 16 GiB
			// quota rejected real workloads at placement
			// (RESOURCE_INSUFFICIENT) that the daemon could serve fine;
			// genuine disk exhaustion surfaces at pull/run time instead.
			EphemeralDiskBytes: ephemeralDiskUnconstrained,
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
