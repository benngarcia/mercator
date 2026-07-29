package docker

import (
	"context"
	"log"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
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
func NewOffering(client *CLIClient, id EndpointIdentity, archOverride string) capability.EphemeralExecutor {
	return offeringAdapter{
		EphemeralExecutor: New(client),
		client:            client,
		id:                id,
		arch:              archOverride,
		disk:              &probeFact[int64]{},
		gpus:              &probeFact[capability.AcceleratorFacts]{},
	}
}

type offeringAdapter struct {
	capability.EphemeralExecutor
	client *CLIClient
	id     EndpointIdentity
	arch   string
	disk   *probeFact[int64]
	gpus   *probeFact[capability.AcceleratorFacts]
}

func (a offeringAdapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	now := time.Now().UTC()
	info := a.probe()
	diskFree := a.disk.value(a.id.NativeRef, "disk", a.client.DiskFreeBytes, now)
	return []domain.OfferSnapshot{StandingOffer(a.id, a.arch, info, diskFree, a.acceleratorFacts(info, now), now)}, nil
}

// acceleratorFacts is what this endpoint established about the cards a container
// started here can be handed, which is three answers rather than two.
//
// Only a daemon with the NVIDIA runtime can satisfy `--gpus`, so a daemon that
// answered and registered no such runtime has established that nothing it starts
// reaches a card: an empty inventory somebody took, and a stated no rather than a
// silence. A daemon with the runtime is asked, and what it answers is the report.
// A daemon Mercator could not reach at all has established nothing, and publishing
// its silence as an empty inventory would strike a GPU host out of every
// accelerator placement for being unreachable for one poll.
func (a offeringAdapter) acceleratorFacts(info HostInfo, now time.Time) capability.AcceleratorFacts {
	switch {
	case info.HasNvidiaRuntime():
		return a.gpus.value(a.id.NativeRef, "gpu", a.client.AcceleratorFacts, now)
	case info.ID != "":
		return capability.AcceleratorFacts{Established: true}
	default:
		return capability.AcceleratorFacts{}
	}
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
// accelerators is what the endpoint established about its cards (see
// offeringAdapter.acceleratorFacts). A report that established nothing is
// published as the silence it was, so a GPU Run is refused here with the code
// that says go and look rather than with one that says this machine has no
// cards.
func StandingOffer(id EndpointIdentity, archOverride string, info HostInfo, diskFreeBytes int64, accelerators capability.AcceleratorFacts, now time.Time) domain.OfferSnapshot {
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
		RentalID:     id.OfferID,
		ConnectionID: id.ConnectionID,
		AdapterType:  "docker",
		Kind:         domain.OfferKindStanding,
		// What a Docker endpoint offers is one machine, and the machine is the
		// daemon: its image store is what a second Run finds warm, so it is the
		// machine a launch history belongs to. The daemon says which one it is, and
		// nothing derived from the endpoint can. Every identifier in EndpointIdentity
		// is built from the label, which is a DOCKER_HOST hostname or a context name:
		// a rootful and a rootless daemon on this box are both "loopback", two ports
		// on one host are both its hostname, and moving from a host URL to a context
		// renames the machine without touching it.
		//
		// A daemon that could not be reached states no machine, which is the honest
		// answer: an endpoint Mercator cannot ask has nothing to file a history
		// under, and inventing one from the label is how two machines came to share
		// a history.
		MachineID:  info.ID,
		NativeRef:  id.NativeRef,
		ObservedAt: now,
		ExpiresAt:  now.Add(time.Hour),
		Platform:   domain.Platform{OS: "linux", Architecture: arch},
		Resources: domain.ResourceInventory{
			CPUMillis:          cpuMillis,
			MemoryBytes:        memoryBytes,
			EphemeralDiskBytes: ephemeralDiskBytes,
			EphemeralDiskKnown: true,
			Accelerators:       accelerators.Devices,
			AcceleratorsKnown:  accelerators.Established,
		},
		// What this endpoint established about the substrate under a workload,
		// carried through the way an enrolled node carries it. The GPU probe is the
		// only thing here that has looked at a driver, and it looked by running a
		// container against it, which is the same act a Run's container performs. A
		// probe that listed the cards has therefore established the driver behind
		// them, and one that could not run has established nothing.
		Host: domain.HostFacts{Attested: accelerators.Attestations(), Driver: accelerators.Driver()},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{
				MaxContainers:              8,
				SupportsDigestRefs:         true,
				SupportsEntrypointOverride: true,
				MaxEnvironmentBytes:        32768,
			},
			Lifecycle: domain.LifecycleCapabilities{
				IdempotentLaunch: "launch_key",
				ListOwned:        true,
				CancelQueued:     true,
			},
			Resources: domain.ResourceCapabilities{GPUVendors: acceleratorVendors(accelerators.Devices)},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone},
			Pricing:   domain.PricingCapabilities{Known: true},
		},
		// No Network facts: nothing has measured this host's link to a registry.
		// The literal 100 Mbps that stood here stamped full confidence on every
		// transfer duration predicted for this endpoint, beside enrolled nodes
		// honestly recording an assumption. A workload requiring a minimum
		// registry bandwidth is now infeasible here unless it allows the
		// unknown, which is the loud version of the same truth.
		Pricing: domain.PriceModel{
			Currency:             "USD",
			RatePerSecondUSD:     0,
			MinimumChargeSeconds: 0,
			GranularitySeconds:   1,
			Known:                true,
		},
		// This daemon can enumerate its own content, and nothing here asks it
		// to yet. A silent inventory is the honest answer: claiming it holds
		// nothing would price every image as a full transfer, and claiming it
		// holds everything is the error this contract exists to delete.
		Images: domain.ImageInventory{Known: false},
		// This daemon was probed, so the capacity claim is Mercator's own
		// observation of a machine it can see rather than a catalog listing it was
		// handed, which is why it carries full confidence.
		Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
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
