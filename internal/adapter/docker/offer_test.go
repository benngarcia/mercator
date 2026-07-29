package docker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
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

	offer := StandingOffer(id, "", info, 500*1024*1024*1024, capability.AcceleratorFacts{}, now)

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

// TestTwoDaemonsOnOneBoxAreTwoMachines is why the offer states the daemon's own
// ID. A rootful daemon on /var/run/docker.sock and a rootless one on
// /run/user/1000/docker.sock are two machines with two image stores, and every
// identifier derived from the endpoint calls them both "loopback": so does every
// pair of ports on one host, because the label drops the port. A launch history
// keyed on that label served one daemon's warm-pull timings as evidence about the
// other, which holds none of those layers.
func TestTwoDaemonsOnOneBoxAreTwoMachines(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	rootful := HostInfo{ID: "daemon-rootful", NCPU: 4, MemTotalBytes: 1 << 30}
	rootless := HostInfo{ID: "daemon-rootless", NCPU: 4, MemTotalBytes: 1 << 30}

	first := StandingOffer(DeriveIdentity("unix:///var/run/docker.sock", ""), "", rootful, 0, capability.AcceleratorFacts{}, now)
	second := StandingOffer(DeriveIdentity("unix:///run/user/1000/docker.sock", ""), "", rootless, 0, capability.AcceleratorFacts{}, now)

	if first.ID != second.ID {
		t.Fatalf("this case is about two endpoints one label cannot tell apart; got %q and %q", first.ID, second.ID)
	}
	firstKey := domain.CandidateIdentityOf(aggregated(first), "sha256:image").Candidate(true)
	secondKey := domain.CandidateIdentityOf(aggregated(second), "sha256:image").Candidate(true)
	if firstKey == secondKey {
		t.Fatalf("two daemons on one box share the candidate key %q", firstKey)
	}
}

// TestOneDaemonReachedTwoWaysIsOneMachine is the other direction, and the reason
// no identifier built from the endpoint may stand in for the machine: an operator
// who moves this host from a DOCKER_HOST URL to a docker context has changed
// nothing about it, and a key that changed with them would orphan every sample the
// machine had accumulated.
func TestOneDaemonReachedTwoWaysIsOneMachine(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	info := HostInfo{ID: "daemon-a", NCPU: 4, MemTotalBytes: 1 << 30}

	byHost := StandingOffer(DeriveIdentity("tcp://10.0.0.5:2375", ""), "", info, 0, capability.AcceleratorFacts{}, now)
	byContext := StandingOffer(DeriveIdentity("", "gpu-ws"), "", info, 0, capability.AcceleratorFacts{}, now)

	byHostKey := domain.CandidateIdentityOf(aggregated(byHost), "sha256:image").Candidate(true)
	byContextKey := domain.CandidateIdentityOf(aggregated(byContext), "sha256:image").Candidate(true)
	// The machine is named, before the two are compared. Two keys that agree because
	// neither exists is the way this case passes while saying nothing.
	if !strings.Contains(byHostKey, "machine="+info.ID) {
		t.Fatalf("key %q does not name the daemon %q that answered", byHostKey, info.ID)
	}
	if byHostKey != byContextKey {
		t.Fatalf("one machine keyed two ways:\n%s\n%s", byHostKey, byContextKey)
	}
}

// TestAnUnreachableDaemonNamesNoMachine holds the loud half. A probe that failed
// yields a zero HostInfo, and an endpoint Mercator could not ask has nothing to
// file a history under: inventing one from the endpoint label is exactly the
// collision above.
func TestAnUnreachableDaemonNamesNoMachine(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	offer := StandingOffer(DeriveIdentity("tcp://10.0.0.5:2375", ""), "", HostInfo{}, 0, capability.AcceleratorFacts{}, now)

	if offer.MachineID != "" {
		t.Fatalf("an endpoint that answered nothing named the machine %q", offer.MachineID)
	}
	if key := domain.CandidateIdentityOf(aggregated(offer), "sha256:image").Candidate(true); key != "" {
		t.Fatalf("an endpoint that answered nothing produced the key %q", key)
	}
}

func TestStandingOfferAdvertisesProbedFreeDisk(t *testing.T) {
	// A workload asking for 25 GiB must be able to schedule on a host that
	// really has that much free disk: the offer advertises the measured free
	// bytes, not a hardcoded constant.
	now := time.Unix(1_700_000_000, 0).UTC()
	diskFree := int64(120 * 1024 * 1024 * 1024)

	offer := StandingOffer(DeriveIdentity("", ""), "", HostInfo{NCPU: 4, MemTotalBytes: 1 << 30}, diskFree, capability.AcceleratorFacts{}, now)

	if offer.Resources.EphemeralDiskBytes != diskFree {
		t.Errorf("EphemeralDiskBytes = %d, want probed %d", offer.Resources.EphemeralDiskBytes, diskFree)
	}
}

func TestStandingOfferFallsBackWhenDiskUnmeasured(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	offer := StandingOffer(DeriveIdentity("", ""), "", HostInfo{NCPU: 4, MemTotalBytes: 1 << 30}, 0, capability.AcceleratorFacts{}, now)

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
	offer := StandingOffer(DeriveIdentity("", ""), "", HostInfo{}, 0, capability.AcceleratorFacts{}, now)

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

func TestStandingOfferArchOverrideWinsOverProbe(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	info := HostInfo{Architecture: "x86_64", NCPU: 4, MemTotalBytes: 1 << 30}
	offer := StandingOffer(DeriveIdentity("", ""), "arm64", info, 0, capability.AcceleratorFacts{}, now)
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
	probed := capability.AcceleratorFacts{
		Established:   true,
		Vendor:        "nvidia",
		DriverVersion: "595.71.05",
		Devices: []domain.AcceleratorInventory{{
			Vendor: "NVIDIA", Model: "NVIDIA GeForce RTX 5090", CanonicalModel: "nvidia-rtx-5090", Count: 1, MemoryBytes: 32 << 30,
		}},
	}

	offer := StandingOffer(DeriveIdentity("ssh://root@ws", ""), "", HostInfo{NCPU: 16, MemTotalBytes: 64 << 30}, 500<<30, probed, now)

	if len(offer.Resources.Accelerators) != 1 || offer.Resources.Accelerators[0].CanonicalModel != "nvidia-rtx-5090" {
		t.Fatalf("offer must advertise the probed GPU inventory, got %+v", offer.Resources.Accelerators)
	}
	if !offer.Resources.AcceleratorsKnown {
		t.Error("an endpoint whose probe listed a card published the inventory as one nobody took")
	}
	if len(offer.Capabilities.Resources.GPUVendors) != 1 || offer.Capabilities.Resources.GPUVendors[0] != "NVIDIA" {
		t.Errorf("GPUVendors = %v, want [NVIDIA]", offer.Capabilities.Resources.GPUVendors)
	}
}

// TestAnEndpointThatListedItsCardsAttestsTheDriverBehindThem is the fact the
// probe already proved and threw away. The GPU probe enumerates the cards by
// running a container against them with `--gpus all`, which is the same act a
// Run's own container performs and which no host without a loaded NVIDIA driver
// can complete. Publishing silence there refused a Run declaring
// facts: ["nvidia_driver"] with UNKNOWN_FACT, which means go and look, on an
// endpoint Mercator had looked at seconds earlier.
func TestAnEndpointThatListedItsCardsAttestsTheDriverBehindThem(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	probed, err := parseNvidiaSMIFacts("NVIDIA GeForce RTX 5090, 32607, 595.71.05\n")
	if err != nil {
		t.Fatalf("parseNvidiaSMIFacts: %v", err)
	}

	offer := StandingOffer(DeriveIdentity("ssh://root@ws", ""), "", HostInfo{NCPU: 16, MemTotalBytes: 64 << 30}, 500<<30, probed, now)

	needsADriver := domain.HostRequirements{Facts: []domain.HostFact{domain.HostFactNvidiaDriver}, MinDriverVersion: "550.0"}
	if refusals := offer.Host.Violations(needsADriver); len(refusals) != 0 {
		t.Fatalf("an endpoint that listed an RTX 5090 on driver 595.71.05 was refused %+v", refusals)
	}
}

// TestAnEndpointNobodyCouldReachEstablishesNoInventory keeps the two silences
// apart on this lane. A daemon that answered and registered no NVIDIA runtime
// cannot hand a container a card, which is an inventory of none somebody took. A
// daemon Mercator could not reach at all took none, and reading its silence as a
// measured zero strikes a GPU host out of every accelerator placement for one
// failed poll.
func TestAnEndpointNobodyCouldReachEstablishesNoInventory(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	answered := StandingOffer(DeriveIdentity("", ""), "", HostInfo{ID: "daemon-1", NCPU: 8, MemTotalBytes: 32 << 30}, 0, capability.AcceleratorFacts{Established: true}, now)
	unreachable := StandingOffer(DeriveIdentity("", ""), "", HostInfo{}, 0, capability.AcceleratorFacts{}, now)

	if len(answered.Resources.Accelerators) != 0 || !answered.Resources.AcceleratorsKnown {
		t.Errorf("a CPU-only daemon published %+v known=%v", answered.Resources.Accelerators, answered.Resources.AcceleratorsKnown)
	}
	if len(answered.Capabilities.Resources.GPUVendors) != 0 {
		t.Errorf("CPU-only offer must advertise no GPU vendors, got %v", answered.Capabilities.Resources.GPUVendors)
	}
	if unreachable.Resources.AcceleratorsKnown {
		t.Error("an endpoint nobody could reach published an inventory somebody took")
	}
	if unreachable.Host.Attested != nil {
		t.Errorf("an endpoint nobody could reach attested %+v", unreachable.Host.Attested)
	}
}

// Encodes the item's acceptance criterion at the scheduling layer: a workload
// requesting one nvidia accelerator schedules onto the GPU-backed remote
// docker offer (inventory straight from the nvidia-smi probe), and the
// CPU-only endpoint's offer is rejected for the same spec.
func TestGPUSpecSchedulesOnGPUDockerOfferAndRejectsCPUOnlyOffer(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	probed, err := parseNvidiaSMIFacts("NVIDIA GeForce RTX 5090, 32607, 595.71.05\n")
	if err != nil {
		t.Fatalf("parseNvidiaSMIFacts: %v", err)
	}
	hostInfo := HostInfo{Architecture: "x86_64", NCPU: 16, MemTotalBytes: 64 << 30, Runtimes: []string{"io.containerd.runc.v2", "nvidia", "runc"}}
	gpuOffer := StandingOffer(DeriveIdentity("ssh://root@ws", ""), "", hostInfo, 500<<30, probed, now)
	// A daemon that answered and registered no NVIDIA runtime, which is an
	// inventory of no cards somebody took rather than an endpoint nobody asked.
	cpuInfo := HostInfo{ID: "daemon-cpu", Architecture: "x86_64", NCPU: 8, MemTotalBytes: 32 << 30, Runtimes: []string{"runc"}}
	cpuOffer := StandingOffer(DeriveIdentity("", ""), "", cpuInfo, 500<<30, capability.AcceleratorFacts{Established: true}, now)

	revision := domain.WorkloadRevision{ID: "wrev_gpu", Spec: domain.WorkloadSpec{
		Containers: []domain.ContainerSpec{{
			Name:     "train",
			Image:    "ghcr.io/acme/train@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
		}},
		Placement: domain.PlacementPolicy{Class: domain.ClassStandard},
		Resources: domain.ResourceRequirements{
			CPU:          domain.CPURequirement{MinMillis: 1000},
			Memory:       domain.MemoryRequirement{MinBytes: 1 << 30},
			Accelerators: []domain.AcceleratorRequirement{{Vendor: "nvidia", ModelAnyOf: []string{"nvidia-rtx-5090"}, Count: 1, MemoryMinBytes: 24 << 30}},
		},
	}}

	// Placement sees offers as the Broker hands them over, with the lane
	// stamped from the connection's negotiated Declaration.
	offers := stampedLane(t, []domain.OfferSnapshot{gpuOffer, cpuOffer})
	decision, err := scheduler.New().Evaluate(context.Background(), scheduler.SchedulingInput{
		RunID: "run_gpu", Workload: revision, Offers: offers, ModelVersion: "latency-v1", EvaluatedAt: now,
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

// stampedLane applies the lane the Broker would stamp on this adapter's
// offers, so a placement assertion sees production-shaped input.
func stampedLane(t *testing.T, offers []domain.OfferSnapshot) []domain.OfferSnapshot {
	t.Helper()
	declaration, err := capability.Declare("docker", New(NewCLIClient("")))
	if err != nil {
		t.Fatalf("declare docker capabilities: %v", err)
	}
	return capability.StampLane(declaration, offers)
}

// TestStandingOfferPublishesNoThroughputNothingMeasured keeps this endpoint
// from asserting a link speed. The offer used to carry a literal 100 Mbps p10
// registry fact at full confidence, which is an assumption wearing a
// measurement's clothes: every transfer duration predicted for this host was
// then stamped certain, beside enrolled nodes honestly recording an assumption.
func TestStandingOfferPublishesNoThroughputNothingMeasured(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	offer := StandingOffer(DeriveIdentity("", ""), "", HostInfo{NCPU: 4, MemTotalBytes: 1 << 30}, 0, capability.AcceleratorFacts{}, now)

	if len(offer.Network.Download) != 0 {
		t.Fatalf("offer publishes %+v, want no throughput fact until something measures this link", offer.Network.Download)
	}
	link := offer.DownloadRate(domain.NetworkScopeRegistry, now)
	if link.Confidence != domain.AssumedLinkConfidence {
		t.Fatalf("registry link = %+v, want the standing assumption at %v confidence", link, domain.AssumedLinkConfidence)
	}
}

// aggregated is the offer as a scheduler receives it. The Broker stamps the adapter
// type from the connection the offer came through and the lane from the Declaration
// the backend negotiated, which is ephemeral for a Docker endpoint until an agent
// enrolls on the machine behind it. Capacity nobody classified has no key at any
// level, so a case deriving a key from an unstamped offer would be comparing two
// empty strings.
func aggregated(offer domain.OfferSnapshot) domain.OfferSnapshot {
	offer.AdapterType = "docker"
	offer.Lane = domain.LaneEphemeral
	return offer
}
