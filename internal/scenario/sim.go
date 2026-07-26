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
	"github.com/benngarcia/mercator/internal/rentalschedule"
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
			version.PublishedAt = spec.Start()
		} else {
			session.note("Artifact %q waits on Run %q to publish it, and a placement world has no publication moment", artifact.ID, artifact.ProducedBy)
		}
		world.DefineArtifact(version)
	}
	world.ApplicationReadySpend = spec.Launch.ApplicationReadySpend()
	for _, rental := range spec.Rentals {
		if err := world.AddMachine(simMachine(spec, rental, spec.rentalSchedule(rental.ID), clock)); err != nil {
			return nil, err
		}
	}
	for _, host := range spec.Hosts {
		if err := world.AddMachine(simHost(spec, host, clock.Now())); err != nil {
			return nil, err
		}
	}
	for _, offer := range spec.Marketplace {
		machine := &fake.Machine{
			Offer: simMarketplaceOffer(spec, offer),
			// What this world spends making the machine, read from the stages the
			// Blueprint states rather than from the estimate the offer publishes.
			ProvisionSpend:      offer.Provisioning.Spend(),
			UnpackSpend:         spec.Launch.UnpackSpend(),
			ContainerStartSpend: spec.Launch.ContainerStartSpend(),
		}
		if err := world.AddMachine(machine); err != nil {
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
	schedules, err := simSchedules(spec, alwaysActiveWorkspaceLog{log})
	if err != nil {
		return nil, err
	}
	session.schedules = schedules
	session.orch = orchestrator.New(
		alwaysActiveWorkspaceLog{log},
		scheduler.New(),
		world,
		orchestrator.WithClock(clock.Now),
		orchestrator.WithImageManifests(world),
		orchestrator.WithArtifactCatalog(world),
		orchestrator.WithRentalSchedules(schedules.store),
	)
	return session, nil
}

// simSchedules is the Broker state this world starts with: the Bookings a
// fixture says are already assigned to each Rental, in the store Placement and
// dispatch read. A world whose Rentals hold nothing seeds an empty store, which
// is the same store, so a scenario about queueing and one about idle capacity
// read Broker state through one path.
func simSchedules(spec WorldSpec, log eventlog.WorkspaceEventLog) (*simSeededSchedules, error) {
	seeded := &simSeededSchedules{store: rentalschedule.NewMemory(log)}
	for _, rental := range spec.Rentals {
		declared := spec.rentalSchedule(rental.ID)
		if declared.Running == nil {
			continue
		}
		schedule, err := simRentalSchedule(declared, spec.Start())
		if err != nil {
			return nil, fmt.Errorf("seed Rental Schedule for %q: %w", rental.ID, err)
		}
		if err := seeded.seed(schedule, simSeededBookingEnd(declared, spec.Start())); err != nil {
			return nil, err
		}
	}
	return seeded, nil
}

// simRentalSchedule is one Rental's declared Bookings reserved through the same
// domain transitions production reserves through, so a fixture can only state a
// schedule Mercator could have reached. Runtimes a fixture states are what each
// Booking has left rather than what its Run declared, so each is seeded as a
// Booking owing exactly that much at the world's start.
//
// A fixture that says a Rental is running work says its workload is running, which
// is the moment the runtimes above are measured from and the reason the Booking
// holding the machine is seeded through Started as well as Reserve. Without it the
// world would state a machine occupied by a container nothing has launched, and
// every projection off it would report the whole declared runtime forever.
//
// The version is the fixture's own, and it is the one thing these transitions
// cannot supply: a schedule's version counts every transition it has seen,
// including the Bookings that have already finished, so a fixture stating five
// Bookings at version nine is a Rental that has done more work than the world can
// see.
func simRentalSchedule(declared RentalScheduleSpec, start time.Time) (domain.RentalSchedule, error) {
	schedule, _, err := domain.NewRentalSchedule(declared.RentalID).Reserve(domain.BookingRequest{
		BookingID:              declared.Running.BookingID,
		RunID:                  declared.Running.RunID,
		ExpectedRuntimeSeconds: declared.Running.expectedRemaining().Duration().Seconds(),
		MaxRuntimeSeconds:      declared.Running.RemainingMaxRuntime.Duration().Seconds(),
		ReservedAt:             start,
	})
	if err != nil {
		return domain.RentalSchedule{}, err
	}
	schedule, err = schedule.Started(declared.Running.BookingID, start)
	if err != nil {
		return domain.RentalSchedule{}, err
	}
	for _, queued := range declared.Queued {
		schedule, _, err = schedule.Reserve(domain.BookingRequest{
			BookingID:              queued.BookingID,
			RunID:                  queued.RunID,
			ExpectedRuntimeSeconds: queued.expected().Duration().Seconds(),
			MaxRuntimeSeconds:      queued.MaxRuntime.Duration().Seconds(),
			ReservedAt:             start,
		})
		if err != nil {
			return domain.RentalSchedule{}, err
		}
	}
	schedule.Version = declared.Version
	return schedule, nil
}

// simSeededSchedules is the Broker's store plus the one thing a placement world
// owes it about Bookings it did not commit. A seeded Booking belongs to a Run
// this harness never created, so nothing here observes that Run exit and the
// Booking would otherwise hold its Rental for the whole scenario. The fixture
// states when it ends, and the store learns it the moment the scripted clock
// passes that point, which is the same moment the world frees the machine.
type simSeededSchedules struct {
	store *rentalschedule.Memory
	ends  []simBookingEnd
}

// simBookingEnd is one seeded Booking finishing.
type simBookingEnd struct {
	rentalID  string
	bookingID string
	at        time.Time
}

// simSeededBookingEnd is when the Booking a fixture says is running ends: the moment
// completion is observed, defaulting to the expected remaining runtime, exactly
// as the world reads it when deciding the machine is free again. Only the
// running Booking states one. When a waiting Booking would finish depends on
// when it starts, and a world whose queue drains further than one Booking is a
// world neither simulator can state: the machine has one busy window.
func simSeededBookingEnd(declared RentalScheduleSpec, start time.Time) simBookingEnd {
	over := declared.Running.expectedRemaining()
	if declared.Running.CompletesAfter != nil {
		over = *declared.Running.CompletesAfter
	}
	return simBookingEnd{
		rentalID:  declared.RentalID,
		bookingID: declared.Running.BookingID,
		at:        start.Add(over.Duration()),
	}
}

func (seeded *simSeededSchedules) seed(schedule domain.RentalSchedule, end simBookingEnd) error {
	if err := seeded.store.Seed(simWorkspace, schedule); err != nil {
		return err
	}
	seeded.ends = append(seeded.ends, end)
	slices.SortFunc(seeded.ends, func(a, b simBookingEnd) int { return a.at.Compare(b.at) })
	return nil
}

// elapsed completes every seeded Booking the clock has now passed, oldest
// first, and promotes whatever was waiting behind it. Dispatching that promoted
// Booking stays the Broker's own work on its Run's next advancement; what
// happens here is only the Rental coming free.
func (seeded *simSeededSchedules) elapsed(ctx context.Context, now time.Time) error {
	for len(seeded.ends) > 0 && !now.Before(seeded.ends[0].at) {
		end := seeded.ends[0]
		seeded.ends = seeded.ends[1:]
		schedules, err := seeded.store.List(ctx, simWorkspace)
		if err != nil {
			return err
		}
		next, _, err := schedules[end.rentalID].Complete(end.bookingID, end.at)
		if err != nil {
			return fmt.Errorf("complete seeded Booking %q: %w", end.bookingID, err)
		}
		if err := seeded.store.Seed(simWorkspace, next); err != nil {
			return err
		}
	}
	return nil
}

func simMachine(spec WorldSpec, rental RentalSpec, schedule RentalScheduleSpec, clock *fake.Clock) *fake.Machine {
	start := clock.Now()
	machine := &fake.Machine{
		Offer:            simRentalOffer(spec, rental),
		HeldLayers:       map[string]int64{},
		HeldDiffIDs:      map[string]bool{},
		ReportsDiffIDs:   rental.ReportsDiffIDs,
		HeldImages:       map[string]bool{},
		ArtifactReplicas: simArtifactReplicas(spec, rental.ArtifactReplicas, start),
		HeldCaches:       simHeldCaches(rental.CacheMounts, start),
		// What a launch here costs once its content has arrived. A standing machine
		// owes no provisioning and still owes both of these.
		UnpackSpend:         spec.Launch.UnpackSpend(),
		ContainerStartSpend: spec.Launch.ContainerStartSpend(),
		// How far this host's own clock runs ahead of the control plane's, which is
		// nothing for every machine no fixture says otherwise about.
		ClockAhead: rental.Skew(),
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
func simRentalOffer(spec WorldSpec, rental RentalSpec) domain.OfferSnapshot {
	offer := simOffer(spec, rental.ID, "conn_rentals", rental.RatePerHourUSD, rental.Resources)
	offer.Kind = domain.OfferKindStanding
	offer.Lane = domain.LaneReusable
	// Whether the capacity is there is answered when the offer is read, because
	// the machine may be busy by then. How sure its publisher is of that answer is
	// a property of the publisher, so it is stated here and carried through.
	offer.Capacity.Confidence = rental.Confidence()
	if rental.Unpriced {
		offer.Pricing = domain.PriceModel{Currency: "USD"}
		offer.Capabilities.Pricing = domain.PricingCapabilities{}
	}
	return offer
}

// simHost is a machine Mercator has not enrolled. It may hold content, and
// nothing on it can be asked about it, so what it holds is world truth that no
// offer carries.
func simHost(spec WorldSpec, host HostSpec, at time.Time) *fake.Machine {
	machine := &fake.Machine{
		Offer:               simHostOffer(spec, host),
		HeldLayers:          map[string]int64{},
		HeldDiffIDs:         map[string]bool{},
		HeldImages:          map[string]bool{},
		ArtifactReplicas:    simArtifactReplicas(spec, host.ArtifactReplicas, at),
		UnpackSpend:         spec.Launch.UnpackSpend(),
		ContainerStartSpend: spec.Launch.ContainerStartSpend(),
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
func simHostOffer(spec WorldSpec, host HostSpec) domain.OfferSnapshot {
	offer := simOffer(spec, host.ID, "conn_hosts", host.RatePerHourUSD, host.Resources)
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
func simMarketplaceOffer(world WorldSpec, spec MarketplaceOfferSpec) domain.OfferSnapshot {
	offer := simOffer(world, spec.ID, "conn_marketplace", spec.RatePerHourUSD, spec.Resources)
	offer.Kind = domain.OfferKindProvisionable
	provisioning := &domain.Estimate{
		Expected: spec.Provisioning.Expected.Duration().Seconds(),
		Source:   "scenario",
	}
	if spec.Provisioning.P90 != nil {
		provisioning.P90 = spec.Provisioning.P90.Duration().Seconds()
	}
	offer.Provisioning = provisioning
	// What the provider of this listing has measured about the machine behind it is
	// a property of that history rather than of the moment the offer was read, so
	// it is stated once here and carried through, exactly as a Rental's capacity
	// confidence is. A listing no fixture states a history for publishes none.
	offer.Reliability = spec.Risk()
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

func simOffer(world WorldSpec, id, connectionID string, ratePerHourUSD float64, resources *ResourcesSpec) domain.OfferSnapshot {
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
		Network: simPathFacts(world, id),
	}
}

// simPathFacts is what this machine has published about the links it crosses. A
// Blueprint's paths are a statement about the world that has to reach Mercator:
// this harness dropped them, so a fixture could declare a machine's measured
// throughput and be scored against the standing assumption instead, with nothing
// saying its statement went nowhere.
func simPathFacts(world WorldSpec, offerID string) domain.NetworkFacts {
	facts := domain.NetworkFacts{}
	for _, path := range world.Paths {
		if path.From != offerID {
			continue
		}
		facts.Download = append(facts.Download, domain.NetworkFact{
			Scope:       domain.NetworkScope(path.Scope),
			Statistic:   "p10",
			ValueMbps:   path.P10Mbps,
			Source:      "scenario-world",
			SampleCount: 1,
			ObservedAt:  world.Start(),
			ValidUntil:  world.Start().Add(24 * time.Hour),
			// How much the host stands behind its own measurement is the fixture's
			// to state, and a host that disowns its number has published nothing.
			Confidence: path.Confidence(),
		})
	}
	return facts
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
	world     *fake.World
	log       *eventlog.SQLiteEventLog
	orch      *orchestrator.Orchestrator
	schedules *simSeededSchedules
	runs      map[string]string
	notes     []string
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

func (s *simSession) AdvanceClock(d time.Duration) error {
	s.world.Clock().Advance(d)
	if err := s.deliverReadiness(); err != nil {
		return err
	}
	return s.schedules.elapsed(context.Background(), s.world.Clock().Now())
}

// deliverReadiness is the applications in this world calling Mercator to say they
// can do work. It is an inbound call rather than something read off an
// observation, because the workload is the only authority on readiness: routing
// it through the provider seam would make a running process and a serving one the
// same fact again.
func (s *simSession) deliverReadiness() error {
	for _, report := range s.world.DueReadinessReports() {
		ready, err := orchestrator.NewApplicationReadyReport(report.ReadyAt)
		if err != nil {
			return err
		}
		if err := s.orch.RecordReport(context.Background(), report.WorkspaceID, report.RunID, ready); err != nil {
			return fmt.Errorf("report readiness for Run %q: %w", report.RunID, err)
		}
	}
	return nil
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
	// A fixture that says nothing about the kind of work its Run is gets whatever
	// a caller who says nothing gets, which is normalisation's business rather
	// than this translation's. A class Mercator does not know is carried through
	// verbatim, because a Blueprint stating one is a fixture about the refusal.
	spec.Placement.Class = domain.ServiceClass(req.ServiceClass)
	spec.Placement.AllowUnknownPricing = req.AllowUnknownPricing
	if req.ExpectedRuntime != nil {
		spec.Placement.ExpectedRuntimeSeconds = req.ExpectedRuntime.Duration().Seconds()
	}
	if req.ExpectedReady != nil {
		spec.Placement.ExpectedReadySeconds = req.ExpectedReady.Duration().Seconds()
	}
	if req.MaxRuntime != nil {
		spec.Execution.MaxRuntimeSeconds = int64(req.MaxRuntime.Duration().Seconds())
	}
	if req.MaxStartLatency != nil {
		spec.Placement.MaxP90StartSeconds = req.MaxStartLatency.Duration().Seconds()
	}
	if req.Download != nil {
		spec.Network.Download = req.Download.Requirement()
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
