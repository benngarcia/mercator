package scenario

import (
	"context"
	"fmt"
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
// event log. Decision correctness only; no network, no daemons.
type SimBackend struct{}

func (SimBackend) StartWorld(spec WorldSpec) (Session, error) {
	clock := fake.NewClock(spec.Start())
	world := fake.NewWorld(clock)
	session := &simSession{
		world:     world,
		runs:      map[string]string{},
		images:    map[string]string{},
		hasImages: len(spec.Images) > 0,
	}
	for ref, image := range spec.Images {
		layers := make([]fake.Layer, 0, len(image.Layers))
		for _, layer := range image.Layers {
			layers = append(layers, fake.Layer{Digest: layer.Name, Bytes: int64(layer.Size)})
		}
		world.DefineImage(ref, layers)
	}
	for _, rental := range spec.Rentals {
		schedule := spec.rentalSchedule(rental.ID)
		if err := world.AddDaemon(simDaemon(spec, rental, schedule, clock)); err != nil {
			return nil, err
		}
		if len(schedule.Scheduled) > 0 {
			session.note("rental %q starts with ScheduledPlacements, but the Broker has no RentalSchedule state yet", rental.ID)
		}
		if len(rental.NamedCaches) > 0 {
			session.note("rental %q holds named caches, but no offer field can advertise them yet", rental.ID)
		}
	}
	for _, offer := range spec.Marketplace {
		if err := world.AddMarketplaceOffer(simMarketplaceOffer(offer)); err != nil {
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
	)
	return session, nil
}

func simDaemon(spec WorldSpec, rental RentalSpec, schedule RentalScheduleSpec, clock *fake.Clock) *fake.Daemon {
	start := clock.Now()
	daemon := &fake.Daemon{
		Offer:      simOffer(rental.ID, "conn_rentals", rental.RatePerHourUSD, rental.Resources),
		HeldLayers: map[string]int64{},
		HeldCaches: map[string]int64{},
	}
	for _, ref := range rental.CachedImages {
		for _, layer := range spec.Images[ref].Layers {
			daemon.HeldLayers[layer.Name] = int64(layer.Size)
		}
	}
	for _, name := range rental.CachedLayers {
		daemon.HeldLayers[name] = int64(layerSize(spec, name))
	}
	for key, size := range rental.NamedCaches {
		daemon.HeldCaches[key] = int64(size)
	}
	if running := schedule.Running; running != nil {
		daemon.BusyUntil = start.Add(running.RemainingMaxRuntime.Duration())
		daemon.FreesAt = daemon.BusyUntil
		if running.CompletesAfter != nil {
			daemon.FreesAt = start.Add(running.CompletesAfter.Duration())
		}
	}
	if rental.IdleLeaseExpiresIn != nil {
		daemon.LeaseExpiresAt = start.Add(rental.IdleLeaseExpiresIn.Duration())
	}
	return daemon
}

func simMarketplaceOffer(spec MarketplaceOfferSpec) domain.OfferSnapshot {
	offer := simOffer(spec.ID, "conn_marketplace", spec.RatePerHourUSD, spec.Resources)
	provisioning := &domain.Estimate{
		Expected: spec.Provisioning.Expected.Duration().Seconds(),
		Source:   "scenario",
	}
	if spec.Provisioning.P90 != nil {
		provisioning.P90 = spec.Provisioning.P90.Duration().Seconds()
	}
	offer.Provisioning = provisioning
	return offer
}

func simOffer(id, connectionID string, ratePerHourUSD float64, resources *ResourcesSpec) domain.OfferSnapshot {
	inventory := domain.ResourceInventory{
		CPUMillis:          defaultHostCPUMillis,
		MemoryBytes:        defaultHostMemoryBytes,
		EphemeralDiskBytes: defaultHostDiskBytes,
	}
	if resources != nil {
		if resources.CPUMillis > 0 {
			inventory.CPUMillis = resources.CPUMillis
		}
		if resources.Memory > 0 {
			inventory.MemoryBytes = int64(resources.Memory)
		}
		if resources.Disk > 0 {
			inventory.EphemeralDiskBytes = int64(resources.Disk)
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
	}
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

func layerSize(spec WorldSpec, name string) ByteSize {
	for _, image := range spec.Images {
		for _, layer := range image.Layers {
			if layer.Name == name {
				return layer.Size
			}
		}
	}
	return 0
}

type simSession struct {
	world *fake.World
	log   *eventlog.SQLiteEventLog
	orch  *orchestrator.Orchestrator
	runs  map[string]string
	// images remembers each run's image so reevaluation lists offers with the
	// same honest layer evidence.
	images    map[string]string
	hasImages bool
	notes     []string
}

func (s *simSession) note(format string, args ...any) {
	s.notes = append(s.notes, fmt.Sprintf(format, args...))
}

func (s *simSession) Submit(name string, req RequestSpec) error {
	if err := s.preparePlacement(req.Image); err != nil {
		return err
	}
	if len(req.CacheMounts) > 0 {
		s.note("run %q declares cache mounts, but the container spec cannot carry them yet", name)
	}
	runID := "run-" + name
	s.runs[name] = runID
	s.images[name] = req.Image
	_, err := s.orch.CreateRun(context.Background(), orchestrator.CreateRunRequest{
		WorkspaceID:    simWorkspace,
		RunID:          runID,
		IdempotencyKey: "create:" + runID,
		Workload:       simWorkload(runID, req),
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
	if err := s.preparePlacement(s.images[name]); err != nil {
		return err
	}
	return s.orch.AdvanceRun(context.Background(), simWorkspace, runID)
}

// preparePlacement declares which image the coming evaluation is for, so the
// world's offers carry honest layer evidence. A world with no image catalog
// places layerless images and needs no declaration.
func (s *simSession) preparePlacement(image string) error {
	if image == "" {
		return fmt.Errorf("requests need an image")
	}
	if !s.hasImages {
		return nil
	}
	return s.world.SetPlacementImage(image)
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

func simWorkload(runID string, req RequestSpec) domain.WorkloadRevision {
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
			EphemeralDisk: domain.DiskRequirement{MinBytes: int64(resources.Disk)},
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
	return domain.WorkloadRevision{
		ID:          "wrev_" + runID,
		WorkspaceID: simWorkspace,
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
