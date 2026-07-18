package docker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scheduler"
)

func TestDeriveIdentityDefaultsToLoopbackNotLocal(t *testing.T) {
	id := DeriveIdentity("", "")
	if id.ConnectionID != "conn_docker_loopback" {
		t.Errorf("ConnectionID = %q, want conn_docker_loopback", id.ConnectionID)
	}
	if id.OfferID != "offer_docker_loopback" {
		t.Errorf("OfferID = %q, want offer_docker_loopback", id.OfferID)
	}
	if id.NativeRef != "loopback" {
		t.Errorf("NativeRef = %q, want loopback", id.NativeRef)
	}
}

func TestDeriveIdentityFromContext(t *testing.T) {
	id := DeriveIdentity("", "dockerhost")
	if id.Context != "dockerhost" {
		t.Errorf("Context = %q, want dockerhost", id.Context)
	}
	if id.ConnectionID != "conn_docker_dockerhost" {
		t.Errorf("ConnectionID = %q, want conn_docker_dockerhost", id.ConnectionID)
	}
	if id.NativeRef != "dockerhost" {
		t.Errorf("NativeRef = %q, want dockerhost", id.NativeRef)
	}
}

func TestDeriveIdentityLabelFromRemoteHost(t *testing.T) {
	id := DeriveIdentity("ssh://user@dockerhost", "")
	if id.Host != "ssh://user@dockerhost" {
		t.Errorf("Host = %q, want ssh://user@dockerhost", id.Host)
	}
	if id.ConnectionID != "conn_docker_dockerhost" {
		t.Errorf("ConnectionID = %q, want conn_docker_dockerhost (host label)", id.ConnectionID)
	}
	if id.NativeRef != "dockerhost" {
		t.Errorf("NativeRef = %q, want dockerhost", id.NativeRef)
	}
}

func TestStandingOfferUsesProbedCapacity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	id := DeriveIdentity("", "dockerhost")
	info := HostInfo{Architecture: "x86_64", OSType: "linux", NCPU: 8, MemTotalBytes: 16 * 1024 * 1024 * 1024, Name: "dockerhost"}

	offer := StandingOffer(id, "", info, 500*1024*1024*1024, nil, now)

	if offer.AdapterType != "docker" {
		t.Errorf("AdapterType = %q, want docker", offer.AdapterType)
	}
	if offer.ID != "offer_docker_dockerhost" || offer.ConnectionID != "conn_docker_dockerhost" {
		t.Errorf("identity not applied: id=%q conn=%q", offer.ID, offer.ConnectionID)
	}
	if offer.NativeRef != "dockerhost" {
		t.Errorf("NativeRef = %q, want dockerhost", offer.NativeRef)
	}
	if offer.Platform.Architecture != "amd64" {
		t.Errorf("Architecture = %q, want amd64 (normalized from x86_64)", offer.Platform.Architecture)
	}
	if offer.Resources.CPUMillis != 8000 {
		t.Errorf("CPUMillis = %d, want 8000 (NCPU*1000)", offer.Resources.CPUMillis)
	}
	if offer.Resources.MemoryBytes != 16*1024*1024*1024 {
		t.Errorf("MemoryBytes = %d, want 16GiB", offer.Resources.MemoryBytes)
	}
	if offer.Resources.EphemeralDiskBytes != 500*1024*1024*1024 {
		t.Errorf("EphemeralDiskBytes = %d, want 500GiB (probed free disk)", offer.Resources.EphemeralDiskBytes)
	}
}

func TestStandingOfferAdvertisesProbedFreeDisk(t *testing.T) {
	// A workload asking for 25 GiB must be able to schedule on a host that
	// really has that much free disk: the offer advertises the measured free
	// bytes, not a hardcoded constant.
	now := time.Unix(1_700_000_000, 0).UTC()
	diskFree := int64(120 * 1024 * 1024 * 1024)

	offer := StandingOffer(DeriveIdentity("", ""), "", HostInfo{NCPU: 4, MemTotalBytes: 1 << 30}, diskFree, nil, now)

	if offer.Resources.EphemeralDiskBytes != diskFree {
		t.Errorf("EphemeralDiskBytes = %d, want probed %d", offer.Resources.EphemeralDiskBytes, diskFree)
	}
}

func TestStandingOfferFallsBackWhenDiskUnmeasured(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	offer := StandingOffer(DeriveIdentity("", ""), "", HostInfo{NCPU: 4, MemTotalBytes: 1 << 30}, 0, nil, now)

	if offer.Resources.EphemeralDiskBytes != 16*1024*1024*1024 {
		t.Errorf("EphemeralDiskBytes = %d, want conservative 16GiB fallback", offer.Resources.EphemeralDiskBytes)
	}
}

func TestProbeFactCachesMeasurementWithinTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	calls := 0
	measure := func(context.Context) (int64, error) {
		calls++
		return int64(calls) * 1024, nil
	}
	fact := &probeFact[int64]{}

	first := fact.value("loopback", "disk", measure, now)
	within := fact.value("loopback", "disk", measure, now.Add(probeFactTTL/2))
	after := fact.value("loopback", "disk", measure, now.Add(probeFactTTL+time.Second))

	if first != 1024 || within != 1024 {
		t.Errorf("within TTL: got %d then %d, want cached 1024", first, within)
	}
	if after != 2048 {
		t.Errorf("after TTL: got %d, want fresh 2048", after)
	}
	if calls != 2 {
		t.Errorf("measure calls = %d, want 2 (one probe per TTL window)", calls)
	}
}

func TestProbeFactFailedProbeYieldsZeroAndIsCached(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	calls := 0
	measure := func(context.Context) (int64, error) {
		calls++
		return 0, errors.New("endpoint down")
	}
	fact := &probeFact[int64]{}

	if got := fact.value("loopback", "disk", measure, now); got != 0 {
		t.Errorf("failed probe: got %d, want 0 (unmeasured)", got)
	}
	if got := fact.value("loopback", "disk", measure, now.Add(time.Second)); got != 0 {
		t.Errorf("failed probe within TTL: got %d, want cached 0", got)
	}
	if calls != 1 {
		t.Errorf("measure calls = %d, want 1 (failures are cached too)", calls)
	}
}

func TestStandingOfferFallsBackWhenProbeEmpty(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	// Empty HostInfo and zero disk simulate an unreachable endpoint / failed probe.
	offer := StandingOffer(DeriveIdentity("", ""), "", HostInfo{}, 0, nil, now)

	if offer.Platform.Architecture == "" {
		t.Error("Architecture must fall back to a default, got empty")
	}
	if offer.Resources.CPUMillis <= 0 || offer.Resources.MemoryBytes <= 0 {
		t.Errorf("capacity must fall back to positive defaults, got cpu=%d mem=%d", offer.Resources.CPUMillis, offer.Resources.MemoryBytes)
	}
	if !offer.Capabilities.Container.SupportsDigestRefs {
		t.Error("docker offer must advertise digest-ref support")
	}
}

// The docker adapter shares the daemon filesystem and cannot reserve disk,
// so its offer must never reject a workload on ephemeral disk at placement:
// a fabricated 16 GiB quota made every >=16 GiB request (e.g. bucket-rails
// model-training's 20 GiB minimum) fail RESOURCE_INSUFFICIENT on an
// otherwise healthy daemon.
func TestStandingOfferDoesNotConstrainEphemeralDisk(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	offer := StandingOffer(DeriveIdentity("", ""), "", HostInfo{}, now)
	if offer.Resources.EphemeralDiskBytes != ephemeralDiskUnconstrained {
		t.Errorf("EphemeralDiskBytes = %d, want the non-constraining %d", offer.Resources.EphemeralDiskBytes, ephemeralDiskUnconstrained)
	}
}

func TestStandingOfferArchOverrideWinsOverProbe(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	info := HostInfo{Architecture: "x86_64", NCPU: 4, MemTotalBytes: 1 << 30}
	offer := StandingOffer(DeriveIdentity("", ""), "arm64", info, 0, nil, now)
	if offer.Platform.Architecture != "arm64" {
		t.Errorf("explicit arch override should win: got %q, want arm64", offer.Platform.Architecture)
	}
}

func TestOfferingAdapterServesFreshOffersPerCall(t *testing.T) {
	// The offer must be rebuilt on every ListOffers call: a snapshot frozen at
	// adapter construction expires one hour in and permanently fails placement.
	client := NewCLIClient("false") // probe fails instantly; capacity falls back
	ad := NewOffering(client, DeriveIdentity("", ""), "")

	first, err := ad.ListOffers(t.Context(), adapter.OfferRequest{})
	if err != nil || len(first) != 1 {
		t.Fatalf("first ListOffers: offers=%v err=%v", first, err)
	}
	time.Sleep(5 * time.Millisecond)
	second, err := ad.ListOffers(t.Context(), adapter.OfferRequest{})
	if err != nil || len(second) != 1 {
		t.Fatalf("second ListOffers: offers=%v err=%v", second, err)
	}
	if !second[0].ObservedAt.After(first[0].ObservedAt) {
		t.Fatalf("offer is frozen: first ObservedAt=%v, second ObservedAt=%v", first[0].ObservedAt, second[0].ObservedAt)
	}
	if !second[0].ExpiresAt.After(time.Now().Add(30 * time.Minute)) {
		t.Fatalf("offer expiry did not refresh: %v", second[0].ExpiresAt)
	}
}

func TestStandingOfferAdvertisesProbedAccelerators(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	accelerators := []domain.AcceleratorInventory{{
		Vendor: "NVIDIA", Model: "NVIDIA GeForce RTX 5090", CanonicalModel: "nvidia-rtx-5090", Count: 1, MemoryBytes: 32 << 30,
	}}

	offer := StandingOffer(DeriveIdentity("ssh://root@ws", ""), "", HostInfo{NCPU: 16, MemTotalBytes: 64 << 30}, 500<<30, accelerators, now)

	if len(offer.Resources.Accelerators) != 1 || offer.Resources.Accelerators[0].CanonicalModel != "nvidia-rtx-5090" {
		t.Fatalf("offer must advertise the probed GPU inventory, got %+v", offer.Resources.Accelerators)
	}
	if len(offer.Capabilities.Resources.GPUVendors) != 1 || offer.Capabilities.Resources.GPUVendors[0] != "NVIDIA" {
		t.Errorf("GPUVendors = %v, want [NVIDIA]", offer.Capabilities.Resources.GPUVendors)
	}
}

func TestStandingOfferWithoutAcceleratorsAdvertisesNone(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	offer := StandingOffer(DeriveIdentity("", ""), "", HostInfo{NCPU: 8, MemTotalBytes: 32 << 30}, 0, nil, now)

	if len(offer.Resources.Accelerators) != 0 {
		t.Errorf("CPU-only offer must advertise no accelerators, got %+v", offer.Resources.Accelerators)
	}
	if len(offer.Capabilities.Resources.GPUVendors) != 0 {
		t.Errorf("CPU-only offer must advertise no GPU vendors, got %v", offer.Capabilities.Resources.GPUVendors)
	}
}

// Encodes the item's acceptance criterion at the scheduling layer: a workload
// requesting one nvidia accelerator schedules onto the GPU-backed remote
// docker offer (inventory straight from the nvidia-smi probe), and the
// CPU-only endpoint's offer is rejected for the same spec.
func TestGPUSpecSchedulesOnGPUDockerOfferAndRejectsCPUOnlyOffer(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	probed, err := parseNvidiaSMIInventory("NVIDIA GeForce RTX 5090, 32607\n")
	if err != nil {
		t.Fatalf("parseNvidiaSMIInventory: %v", err)
	}
	hostInfo := HostInfo{Architecture: "x86_64", NCPU: 16, MemTotalBytes: 64 << 30, Runtimes: []string{"io.containerd.runc.v2", "nvidia", "runc"}}
	gpuOffer := StandingOffer(DeriveIdentity("ssh://root@ws", ""), "", hostInfo, 500<<30, probed, now)
	cpuOffer := StandingOffer(DeriveIdentity("", ""), "", HostInfo{Architecture: "x86_64", NCPU: 8, MemTotalBytes: 32 << 30}, 500<<30, nil, now)

	revision := domain.WorkloadRevision{ID: "wrev_gpu", Spec: domain.WorkloadSpec{
		Containers: []domain.ContainerSpec{{
			Name:     "train",
			Image:    "ghcr.io/acme/train@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
		}},
		Resources: domain.ResourceRequirements{
			CPU:          domain.CPURequirement{MinMillis: 1000},
			Memory:       domain.MemoryRequirement{MinBytes: 1 << 30},
			Accelerators: []domain.AcceleratorRequirement{{Vendor: "nvidia", ModelAnyOf: []string{"nvidia-rtx-5090"}, Count: 1, MemoryMinBytes: 24 << 30}},
		},
	}}

	decision, err := scheduler.New().Evaluate(context.Background(), scheduler.SchedulingInput{
		RunID: "run_gpu", Workload: revision, Offers: []domain.OfferSnapshot{gpuOffer, cpuOffer}, ModelVersion: "latency-v1", EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != gpuOffer.ID {
		t.Fatalf("GPU spec must schedule on the GPU-backed docker offer %q, got %q (candidates: %+v)", gpuOffer.ID, decision.SelectedOfferSnapshotID, decision.Candidates)
	}
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID != cpuOffer.ID {
			continue
		}
		if candidate.Feasible {
			t.Fatal("CPU-only offer must be infeasible for a GPU spec")
		}
		for _, rejection := range candidate.Rejections {
			if rejection.Code == "RESOURCE_INSUFFICIENT" && rejection.Path == "resources.accelerators" {
				return
			}
		}
		t.Fatalf("CPU-only offer must be rejected on resources.accelerators, got %+v", candidate.Rejections)
	}
	t.Fatalf("CPU-only candidate missing from decision: %+v", decision.Candidates)
}
