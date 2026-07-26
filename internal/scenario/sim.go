package scenario

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/gpunorm"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
)

// Host inventory defaults: a generous GPU-box shape so fixtures state only
// the resources their scenario is about.
const (
	defaultHostCPUMillis   = int64(8000)
	defaultHostMemoryBytes = int64(32e9)
	defaultHostDiskBytes   = int64(200e9)
)

const simWorkspace = "ws_scenario"

// SimBackend executes scenarios against simulated capacity: the fake
// adapter's World under the real orchestrator, scheduler, and a real SQLite
// event log. Decision correctness only; no network, no machines.
type SimBackend struct{}

func (SimBackend) StartWorld(spec WorldSpec) (Session, error) {
	clock := fake.NewClock(spec.Start())
	world := fake.NewWorld(clock)
	session := &simSession{
		world: world,
		runs:  map[string]string{},
	}
	for ref, image := range spec.Images {
		layers := make([]fake.Layer, 0, len(image.Layers))
		for _, layer := range image.Layers {
			layers = append(layers, fake.Layer{Digest: layer.Digest, DiffID: layer.DiffID, Bytes: int64(layer.Size)})
		}
		world.DefineImage(ref, fake.Image{Layers: layers, Registry: fake.RegistryAnswer(image.Registry)})
	}
	for _, artifact := range spec.Artifacts {
		version := artifact.Version(simWorkspace)
		if artifact.Prepublished() {
			// A publication is what records where content was produced, so a
			// version this world starts out holding was published from whatever
			// machine the fixture says wrote it, before the clock started.
			version.PublishedAt = spec.Start()
			version.ProducedOnRentalID = artifact.ProducedOn
		} else {
			session.note("Artifact %q waits on Run %q to publish it, and a placement world has no publication moment", artifact.ID, artifact.ProducedBy)
		}
		world.DefineArtifact(version)
	}
	for _, rental := range spec.Rentals {
		schedule := spec.rentalSchedule(rental.ID)
		if err := world.AddMachine(simMachine(spec, rental, schedule, clock)); err != nil {
			return nil, err
		}
		if len(schedule.Queued) > 0 {
			session.note("rental %q starts with QueuedBookings, but the scenario backend cannot seed Broker RentalSchedule state yet", rental.ID)
		}
	}
	for _, host := range spec.Hosts {
		if err := world.AddMachine(simHost(spec, host, clock.Now())); err != nil {
			return nil, err
		}
	}
	for _, offer := range spec.Marketplace {
		if err := world.AddMachine(&fake.Machine{Offer: simMarketplaceOffer(offer)}); err != nil {
			return nil, err
		}
		if len(offer.Facts) > 0 {
			session.note("offer %q declares host facts, but no offer field can carry them yet", offer.ID)
		}
	}
	log, err := eventlog.OpenSQLite(context.Background(), "file:scenario-"+uuid.NewString()+"?mode=memory&cache=shared")
	if err != nil {
		return nil, err
	}
	session.log = log
	session.orch = orchestrator.New(
		alwaysActiveWorkspaceLog{log},
		scheduler.New(),
		world,
		orchestrator.WithClock(clock.Now),
		orchestrator.WithImageManifests(world),
		orchestrator.WithArtifactCatalog(world),
	)
	return session, nil
}

func simMachine(spec WorldSpec, rental RentalSpec, schedule RentalScheduleSpec, clock *fake.Clock) *fake.Machine {
	start := clock.Now()
	machine := &fake.Machine{
		Offer:               simRentalOffer(rental),
		HeldLayers:          map[string]int64{},
		HeldDiffIDs:         map[string]bool{},
		ReportsDiffIDs:      rental.ReportsDiffIDs,
		HeldImages:          map[string]bool{},
		ArtifactReplicas:    simArtifactReplicas(spec, rental.ArtifactReplicas, start),
		NoArtifactInventory: !rental.ListsArtifacts(),
		HeldCaches:          simHeldCaches(rental.CacheMounts, start),
	}
	for _, ref := range rental.CachedImages {
		for _, layer := range spec.Images[ref].Layers {
			machine.Hold(fake.Layer{Digest: layer.Digest, DiffID: layer.DiffID, Bytes: int64(layer.Size)})
			if !rental.IsUnpacked() {
				machine.Pack(layer.Digest, layer.DiffID)
			}
		}
		machine.HeldImages[domain.ReferenceDigest(ref)] = true
		if !rental.IsUnpacked() {
			machine.Pack(domain.ReferenceDigest(ref))
		}
	}
	for _, digest := range rental.CachedLayers {
		layer := findLayer(spec, digest)
		machine.Hold(layer)
		if !rental.IsUnpacked() {
			machine.Pack(layer.Digest, layer.DiffID)
		}
	}
	if running := schedule.Running; running != nil {
		machine.BusyUntil = start.Add(running.RemainingMaxRuntime.Duration())
		machine.ExpectedBusyUntil = start.Add(running.expectedRemaining().Duration())
		if running.CompletesAfter != nil {
			machine.FreesAt = start.Add(running.CompletesAfter.Duration())
		}
	}
	if rental.IdleLeaseExpiresIn != nil {
		machine.LeaseExpiresAt = start.Add(rental.IdleLeaseExpiresIn.Duration())
	}
	return machine
}

// simArtifactReplicas is the local copy of each Artifact this machine was found
// holding: the digest the copy claims and what the fixture says checking it was
// worth. Both are the machine's own bookkeeping rather than the catalog's, which
// is what lets a fixture state a copy nobody checked and a copy that was checked
// against content this version does not have.
func simArtifactReplicas(spec WorldSpec, declared []ArtifactReplicaSpec, at time.Time) map[string]domain.ArtifactReplica {
	catalog := spec.artifactsByID()
	replicas := make(map[string]domain.ArtifactReplica, len(declared))
	for _, held := range declared {
		artifact := catalog[held.Artifact]
		replica := domain.ArtifactReplica{
			ArtifactID:    artifact.ID,
			ContentDigest: held.Digest(artifact),
			SizeBytes:     int64(artifact.Size),
			State:         held.State,
		}
		if replica.State.Usable() {
			replica.VerifiedAt = at
		}
		replicas[artifact.ID] = replica
	}
	return replicas
}

// simHeldCaches is the mutable state one machine was already holding, keyed by
// the identity that carries its workspace. A placement world has one workspace,
// so a fixture that labels a cache with another one is stating a neighbour's
// cache: content on this host that this backend's Runs must never be told about.
func simHeldCaches(held []HeldCacheSpec, at time.Time) map[string]domain.CacheMount {
	caches := make(map[string]domain.CacheMount, len(held))
	for _, declared := range held {
		workspaceID := simWorkspace
		if declared.Workspace != "" {
			workspaceID = simWorkspace + "_" + declared.Workspace
		}
		mount := domain.CacheMount{
			WorkspaceID:      workspaceID,
			Name:             declared.Name,
			CompatibilityKey: declared.CompatibilityKey,
			CreatedAt:        at,
		}
		caches[mount.Identity()] = mount
	}
	return caches
}

// simRentalOffer builds the offer for a Rental: standing capacity Mercator holds
// across Runs, which is what makes it reusable and the only thing warmth can
// accumulate on.
func simRentalOffer(rental RentalSpec) domain.OfferSnapshot {
	offer := simOffer(rental.ID, "conn_rentals", rental.RatePerHourUSD, rental.Resources)
	offer.Kind = domain.OfferKindStanding
	offer.Lane = domain.LaneReusable
	return offer
}

// simHost is a machine Mercator has not enrolled. It may hold content, and
// nothing on it can be asked about it, so what it holds is world truth that no
// offer carries.
func simHost(spec WorldSpec, host HostSpec, at time.Time) *fake.Machine {
	machine := &fake.Machine{
		Offer:            simHostOffer(host),
		HeldLayers:       map[string]int64{},
		HeldDiffIDs:      map[string]bool{},
		HeldImages:       map[string]bool{},
		ArtifactReplicas: simArtifactReplicas(spec, host.ArtifactReplicas, at),
	}
	for _, ref := range host.CachedImages {
		for _, layer := range spec.Images[ref].Layers {
			machine.Hold(fake.Layer{Digest: layer.Digest, DiffID: layer.DiffID, Bytes: int64(layer.Size)})
		}
		machine.HeldImages[domain.ReferenceDigest(ref)] = true
	}
	return machine
}

// simHostOffer builds the offer for a host Mercator has not enrolled. The
// machine exists, so the offer is standing and owes no provisioning; nothing on
// it can hold content or run a second workload for Mercator, so it is in the
// ephemeral lane and reports an inventory it cannot enumerate.
func simHostOffer(host HostSpec) domain.OfferSnapshot {
	offer := simOffer(host.ID, "conn_hosts", host.RatePerHourUSD, host.Resources)
	offer.Kind = domain.OfferKindStanding
	offer.Lane = domain.LaneEphemeral
	offer.Pricing.SetupFeeUSD = host.Billing.SetupFeeUSD
	if host.Billing.MinimumCharge != nil {
		offer.Pricing.MinimumChargeSeconds = int64(host.Billing.MinimumCharge.Duration().Seconds())
	}
	return offer
}

// simMarketplaceOffer builds the offer for a machine that does not exist yet, so
// nothing an execution fetches there is anywhere a later Run can see it.
func simMarketplaceOffer(spec MarketplaceOfferSpec) domain.OfferSnapshot {
	offer := simOffer(spec.ID, "conn_marketplace", spec.RatePerHourUSD, spec.Resources)
	offer.Kind = domain.OfferKindProvisionable
	provisioning := &domain.Estimate{
		Expected: spec.Provisioning.Expected.Duration().Seconds(),
		Source:   "scenario",
	}
	if spec.Provisioning.P90 != nil {
		provisioning.P90 = spec.Provisioning.P90.Duration().Seconds()
	}
	offer.Provisioning = provisioning
	offer.Lane = spec.ExecutionLane()
	return offer
}

// HostInventory is the machine a fixture described, defaulting to a generous
// GPU box wherever it described nothing. Both simulated worlds read it: a
// Blueprint that meant one machine in the placement corpus and another in the
// Lab would be two fixtures wearing one name, and the corpus statement about
// either would say nothing about the other.
func HostInventory(resources *ResourcesSpec) domain.ResourceInventory {
	inventory := domain.ResourceInventory{
		CPUMillis:          defaultHostCPUMillis,
		MemoryBytes:        defaultHostMemoryBytes,
		EphemeralDiskBytes: defaultHostDiskBytes,
	}
	if resources == nil {
		return inventory
	}
	if resources.CPUMillis > 0 {
		inventory.CPUMillis = resources.CPUMillis
	}
	if resources.Memory > 0 {
		inventory.MemoryBytes = int64(resources.Memory)
	}
	// Stated is stated, including zero: a machine with no room is a machine an
	// offer can carry, and it is what an enrolled node that could not measure
	// its disk advertises.
	if resources.Disk != nil {
		inventory.EphemeralDiskBytes = resources.Disk.Bytes()
	}
	if gpu := resources.GPU; gpu != nil {
		count := gpu.Count
		if count == 0 {
			count = 1
		}
		inventory.Accelerators = []domain.AcceleratorInventory{{
			Vendor:         "NVIDIA",
			Model:          gpu.Model,
			CanonicalModel: gpunorm.Canonical("NVIDIA", gpu.Model),
			Count:          count,
			MemoryBytes:    int64(gpu.Memory),
		}}
	}
	return inventory
}

func simOffer(id, connectionID string, ratePerHourUSD float64, resources *ResourcesSpec) domain.OfferSnapshot {
	inventory := HostInventory(resources)
	return domain.OfferSnapshot{
		ID:           id,
		ConnectionID: connectionID,
		AdapterType:  "fake",
		NativeRef:    id,
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources:    inventory,
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{
				MaxContainers:              1,
				SupportsDigestRefs:         true,
				SupportsEntrypointOverride: true,
				MaxEnvironmentBytes:        32768,
			},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone, Protocols: []string{"tcp"}},
			Pricing:   domain.PricingCapabilities{Known: true},
			Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "launch_key", ListOwned: true},
		},
		Pricing: domain.PriceModel{
			Currency:         "USD",
			RatePerSecondUSD: ratePerHourUSD / 3600,
			Known:            true,
		},
	}
}

// findLayer resolves a digest a fixture seeds directly onto a Rental back to
// the layer the image catalog defines, so the machine holds it under every name
// that content answers to.
func findLayer(spec WorldSpec, digest string) fake.Layer {
	for _, image := range spec.Images {
		for _, layer := range image.Layers {
			if layer.Digest == digest {
				return fake.Layer{Digest: layer.Digest, DiffID: layer.DiffID, Bytes: int64(layer.Size)}
			}
		}
	}
	return fake.Layer{Digest: digest}
}

type simSession struct {
	world *fake.World
	log   *eventlog.SQLiteEventLog
	orch  *orchestrator.Orchestrator
	runs  map[string]string
	notes []string
}

func (s *simSession) note(format string, args ...any) {
	s.notes = append(s.notes, fmt.Sprintf(format, args...))
}

func (s *simSession) Submit(name string, req RequestSpec) error {
	if req.Image == "" {
		return fmt.Errorf("requests need an image")
	}
	runID := "run-" + name
	s.runs[name] = runID
	_, err := s.orch.CreateRun(context.Background(), orchestrator.CreateRunRequest{
		WorkspaceID:    simWorkspace,
		RunID:          runID,
		IdempotencyKey: "create:" + runID,
		Workload:       WorkloadForRun(simWorkspace, runID, req),
	})
	if err != nil {
		return err
	}
	return s.orch.AdvanceRun(context.Background(), simWorkspace, runID)
}

func (s *simSession) Reconcile(name string) error {
	runID, ok := s.runs[name]
	if !ok {
		return fmt.Errorf("run %q was never submitted", name)
	}
	return s.orch.AdvanceRun(context.Background(), simWorkspace, runID)
}

func (s *simSession) AdvanceClock(d time.Duration) {
	s.world.Clock().Advance(d)
}

func (s *simSession) RunEvents(name string) ([]eventlog.StoredEvent, error) {
	runID, ok := s.runs[name]
	if !ok {
		return nil, fmt.Errorf("run %q was never submitted", name)
	}
	return s.orch.GetRunEvents(context.Background(), simWorkspace, runID)
}

func (s *simSession) Notes() []string { return s.notes }

func (s *simSession) Close() {
	_ = s.log.Close()
}

// WorkloadForRun translates the canonical Blueprint request into the real
// orchestrator input shared by placement scenarios and Lab execution.
func WorkloadForRun(workspaceID, runID string, req RequestSpec) domain.WorkloadRevision {
	spec := domain.WorkloadSpec{
		Containers: []domain.ContainerSpec{{
			Name:     "main",
			Image:    req.Image,
			Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
		}},
	}
	if resources := req.Resources; resources != nil {
		spec.Resources = domain.ResourceRequirements{
			CPU:           domain.CPURequirement{MinMillis: resources.CPUMillis},
			Memory:        domain.MemoryRequirement{MinBytes: int64(resources.Memory)},
			EphemeralDisk: domain.DiskRequirement{MinBytes: resources.Disk.Bytes()},
		}
		if gpu := resources.GPU; gpu != nil {
			count := gpu.Count
			if count == 0 {
				count = 1
			}
			spec.Resources.Accelerators = []domain.AcceleratorRequirement{{
				Vendor:         "NVIDIA",
				ModelAnyOf:     []string{gpu.Model},
				Count:          count,
				MemoryMinBytes: int64(gpu.Memory),
			}}
		}
	}
	if req.Objective != "" {
		spec.Placement.Objective = domain.PlacementObjective(req.Objective)
	}
	if req.ExpectedRuntime != nil {
		spec.Placement.ExpectedRuntimeSeconds = req.ExpectedRuntime.Duration().Seconds()
	}
	if req.MaxRuntime != nil {
		spec.Execution.MaxRuntimeSeconds = int64(req.MaxRuntime.Duration().Seconds())
	}
	if req.MaxStartLatency != nil {
		spec.Placement.MaxP90StartSeconds = req.MaxStartLatency.Duration().Seconds()
	}
	spec.Artifacts = domain.ArtifactRequirements{
		Consumes: slices.Clone(req.ConsumesArtifacts),
		Produces: slices.Clone(req.ProducesArtifacts),
	}
	spec.Caches = req.CacheRequirements()
	return domain.WorkloadRevision{
		ID:          "wrev_" + runID,
		WorkspaceID: workspaceID,
		WorkloadID:  "wrk_" + runID,
		Digest:      "sha256:" + runID,
		Spec:        spec,
	}
}

// alwaysActiveWorkspaceLog treats every workspace as active: scenarios have
// no workspace lifecycle.
type alwaysActiveWorkspaceLog struct {
	eventlog.EventLog
}

func (l alwaysActiveWorkspaceLog) AppendIfWorkspaceActive(ctx context.Context, req eventlog.AppendRequest) (eventlog.AppendResult, error) {
	return l.EventLog.Append(ctx, req)
}
