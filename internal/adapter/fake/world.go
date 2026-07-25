package fake

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/ociresolver"
)

// registryMbps is how fast this world moves image content onto a machine. The
// arithmetic below it is the world's own transfer model, deliberately
// independent of the scheduler's prediction: a fixture is only meaningful when
// the actual pull and the predicted pull are produced by different code. The
// speed itself is the standing assumption about an unmeasured link, which is
// stated once so the two models cannot disagree about what they never measured.
const registryMbps = domain.DefaultRegistryDownloadMbps

// Clock is a scripted wall clock shared by a World, its machines, and the
// orchestrator under test. Time only moves when a scenario advances it, so
// placement decisions, scheduled-start deadlines, and lease expiries are exact.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

func NewClock(start time.Time) *Clock {
	return &Clock{t: start.UTC()}
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Layer is one content-addressed slice of an image, named in both digest
// spaces: Digest is the compressed blob a registry serves, DiffID the
// uncompressed content a container daemon enumerates. Shared layers across
// images are what make warm-rental affinity worth modeling, and carrying both
// names is what lets a host that speaks one space be matched against a manifest
// written in the other.
type Layer struct {
	Digest string
	DiffID string
	Bytes  int64
}

// Image is what this world's registry knows about one image: its layers, and
// whether it will say so. An image can exist in the world and be unreadable
// from the registry, which is the difference between what is running and what
// can be looked up about it.
type Image struct {
	Layers   []Layer
	Registry RegistryAnswer
}

// RegistryAnswer is how the simulated registry responds to a resolution. A real
// registry says no five distinguishable ways and an operator acts on each
// differently, so collapsing them into one empty manifest is a fidelity bug.
type RegistryAnswer string

const (
	RegistryResolves     RegistryAnswer = ""
	RegistryUnresolvable RegistryAnswer = "unresolvable"
	RegistryUnauthorized RegistryAnswer = "unauthorized"
	RegistryThrottled    RegistryAnswer = "throttled"
	RegistryUnreachable  RegistryAnswer = "unreachable"
)

// Machine is one host in the simulated world: a Rental Mercator holds, or a
// marketplace offer naming capacity that does not exist yet. It holds image
// layers and named data caches, may be busy with running work, and may sit
// inside an idle lease. Whether what it runs is still there afterwards is the
// offer's answer, not the struct's.
type Machine struct {
	// Offer is the capacity the machine advertises: resources, pricing,
	// platform, capabilities. Kind and Lane are fixed when the machine is
	// registered, because they are what it is; ObservedAt, ExpiresAt, Queue,
	// Capacity, and Images are what the world answers with at listing time.
	Offer domain.OfferSnapshot
	// HeldLayers maps compressed layer blob digest to bytes already present on
	// the machine.
	HeldLayers map[string]int64
	// HeldDiffIDs is the same content named the way a container daemon names it.
	// A machine that reports diff IDs holds no less than one that reports blob
	// digests; it just has a different word for the same bytes.
	HeldDiffIDs map[string]bool
	// ReportsDiffIDs makes this machine enumerate its layers the way a Docker
	// daemon does, which is the only vocabulary a real one has.
	ReportsDiffIDs bool
	// HeldImages is every image reference the machine holds whole, which is what
	// lets a repeat of the same image skip layer arithmetic entirely.
	HeldImages map[string]bool
	// Packed is content this machine fetched and never unpacked, keyed by layer
	// blob digest and by image digest. It holds those bytes and cannot start a
	// container on them, so it can name no layer identity for them: a runtime
	// learns a layer's identity by unpacking it. Everything else it holds is
	// assembled, because a pull that lands unpacks as it goes.
	Packed map[string]bool
	// InventoryValidUntil is how long this machine stands behind what it says
	// it holds, and InventoryObservedAt when it last looked. Zero on both is a
	// machine that re-enumerates whenever it is asked and states no bound.
	InventoryObservedAt time.Time
	InventoryValidUntil time.Time
	// HeldCaches maps named data cache key (e.g. a dataset GID) to bytes
	// materialized on the machine's local disk. No offer field carries this
	// today; the world holds it so cache-evidence milestones can surface it.
	HeldCaches map[string]int64
	// BusyUntil is when the running work's enforced maximum runtime elapses;
	// zero means idle. It is the hard ceiling behind latest-start guarantees.
	BusyUntil time.Time
	// ExpectedBusyUntil is when the running work is expected (p50) to finish,
	// defaulting to BusyUntil. The expected remaining time is what queue-delay
	// scoring weighs.
	ExpectedBusyUntil time.Time
	// FreesAt is when the machine is actually observed free again. It defaults
	// to ExpectedBusyUntil; another value models a run finishing early, or
	// overrunning its estimate up to the enforced bound, which lets a scenario
	// hold a Rental busy past a queued Booking's latest start.
	FreesAt time.Time
	// LeaseExpiresAt is when the machine's idle lease ends; zero means no
	// lease bound. An expired machine stops being offered, standing in for
	// janitor termination until the rental lifecycle exists.
	LeaseExpiresAt time.Time

	// fetching is content this machine is still pulling. A host holds an image
	// when its bytes have arrived, not when the container was dispatched.
	fetching []transfer
}

// transfer is one image pull in flight: what it will leave on the machine, and
// when the bytes have finished moving.
type transfer struct {
	image       string
	layers      []Layer
	completesAt time.Time
}

// startPull begins fetching an image onto this machine. Capacity Mercator does
// not keep is left out of it: there is no host there to be holding the content
// when the next Run asks.
func (m *Machine) startPull(image string, layers []Layer, now time.Time) {
	if !m.Offer.KeepsWhatItRuns() {
		return
	}
	var bytes int64
	for _, layer := range layers {
		if _, held := m.HeldLayers[layer.Digest]; !held {
			bytes += layer.Bytes
		}
	}
	m.fetching = append(m.fetching, transfer{
		image:       image,
		layers:      layers,
		completesAt: now.Add(transferDuration(bytes)),
	})
}

// settle applies every pull whose bytes have arrived by now. Until then the
// machine holds what it held before, because an image that is still being
// fetched is not on the host.
func (m *Machine) settle(now time.Time) {
	arrived := m.fetching[:0]
	for _, pull := range m.fetching {
		if now.Before(pull.completesAt) {
			arrived = append(arrived, pull)
			continue
		}
		m.keep(pull.image, pull.layers)
	}
	m.fetching = arrived
}

// Hold puts one layer on this machine under every name that content answers
// to, so a fixture that seeds a machine and a pull that lands on one leave the
// same machine behind.
func (m *Machine) Hold(layer Layer) {
	if m.HeldLayers == nil {
		m.HeldLayers = map[string]int64{}
	}
	if m.HeldDiffIDs == nil {
		m.HeldDiffIDs = map[string]bool{}
	}
	m.HeldLayers[layer.Digest] = layer.Bytes
	delete(m.Packed, layer.Digest)
	if layer.DiffID != "" {
		m.HeldDiffIDs[layer.DiffID] = true
		delete(m.Packed, layer.DiffID)
	}
}

// Pack marks content this machine holds and has not unpacked, under every name
// that content answers to. Unpacking is what a pull does on arrival, so this is
// only ever a fixture's statement about a machine it found in that condition.
func (m *Machine) Pack(names ...string) {
	if m.Packed == nil {
		m.Packed = map[string]bool{}
	}
	for _, name := range names {
		if name != "" {
			m.Packed[name] = true
		}
	}
}

func (m *Machine) keep(image string, layers []Layer) {
	if m.HeldLayers == nil {
		m.HeldLayers = map[string]int64{}
	}
	if m.HeldDiffIDs == nil {
		m.HeldDiffIDs = map[string]bool{}
	}
	if m.HeldImages == nil {
		m.HeldImages = map[string]bool{}
	}
	for _, layer := range layers {
		m.Hold(layer)
	}
	// The name a machine holds an image under is the digest it pulled by, which
	// is the only name a resolved manifest and a real host can both say.
	m.HeldImages[domain.ReferenceDigest(image)] = true
	delete(m.Packed, domain.ReferenceDigest(image))
}

// inventory is what this machine says it holds, whatever Run is being placed,
// in the digest space its runtime can enumerate. What a Run would still have to
// fetch is the scheduler's subtraction against the manifest, so the world
// asserts no answer about an image the offer does not name.
func (m *Machine) inventory(now time.Time) domain.ImageInventory {
	observed := now
	if !m.InventoryObservedAt.IsZero() {
		observed = m.InventoryObservedAt
	}
	inventory := domain.ImageInventory{Known: true, ObservedAt: observed, ValidUntil: m.InventoryValidUntil}
	for _, image := range slices.Sorted(maps.Keys(m.HeldImages)) {
		if m.Packed[image] {
			inventory.PulledImageDigests = append(inventory.PulledImageDigests, image)
			continue
		}
		inventory.ImageDigests = append(inventory.ImageDigests, image)
	}
	for _, digest := range slices.Sorted(maps.Keys(m.HeldLayers)) {
		if m.Packed[digest] {
			continue
		}
		inventory.LayerDigests = append(inventory.LayerDigests, digest)
	}
	if !m.ReportsDiffIDs {
		return inventory
	}
	// A Docker host has no word for the compressed blob a registry served it,
	// so it answers about the same content in the only vocabulary it has.
	inventory.LayerDigests = nil
	for _, diffID := range slices.Sorted(maps.Keys(m.HeldDiffIDs)) {
		if !m.Packed[diffID] {
			inventory.LayerDiffIDs = append(inventory.LayerDiffIDs, diffID)
		}
	}
	return inventory
}

func transferDuration(bytes int64) time.Duration {
	if bytes <= 0 {
		return 0
	}
	seconds := float64(bytes*8) / 1_000_000 / registryMbps
	return time.Duration(seconds * float64(time.Second))
}

func (m *Machine) busyAt(now time.Time) bool {
	frees := m.FreesAt
	if frees.IsZero() {
		frees = m.expectedBusyUntil()
	}
	return now.Before(frees)
}

func (m *Machine) expectedBusyUntil() time.Time {
	if !m.ExpectedBusyUntil.IsZero() {
		return m.ExpectedBusyUntil
	}
	return m.BusyUntil
}

func (m *Machine) expectedRemainingAt(now time.Time) time.Duration {
	if remaining := m.expectedBusyUntil().Sub(now); remaining > 0 {
		return remaining
	}
	return 0
}

func (m *Machine) leaseExpiredAt(now time.Time) bool {
	return !m.LeaseExpiresAt.IsZero() && !now.Before(m.LeaseExpiresAt)
}

// World extends the fake adapter with simulated capacity state: machines with
// image-layer and named-cache contents, scripted running work, a marketplace of
// provisionable offers, and a scripted clock. Observe and cleanup reuse the
// embedded fake adapter unchanged; launching and offer listing are derived from
// world state.
type World struct {
	*Adapter
	clock  *Clock
	mu     sync.Mutex
	images map[string]Image
	// machines is every offer this world can list, keyed by offer ID. A Rental
	// and a marketplace offer are the same kind of entry: what separates them
	// is whether the offer names capacity Mercator keeps.
	machines map[string]*Machine
}

func NewWorld(clock *Clock, options ...Option) *World {
	options = append([]Option{WithNow(clock.Now)}, options...)
	return &World{
		Adapter:  New(options...),
		clock:    clock,
		images:   map[string]Image{},
		machines: map[string]*Machine{},
	}
}

func (w *World) Clock() *Clock { return w.clock }

// DefineImage registers an image as its ordered layers and what the registry
// will say about it. Layer digests are shared identity across images: two
// images listing the same digest share that layer, which is what layer-affinity
// scenarios exercise.
func (w *World) DefineImage(ref string, image Image) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.images[ref] = Image{Layers: slices.Clone(image.Layers), Registry: image.Registry}
}

// AddMachine registers one entry in the world's capacity: a Rental Mercator
// holds, a marketplace template for a machine that does not exist yet, or a host
// that exists and lends out a slot. The caller states the kind and the lane,
// because that pair is what the capacity is; the world only refuses a claim it
// would then have to correct, and grants Rental identity to the capacity that
// earns it, exactly as capability.StampLane does in production.
func (w *World) AddMachine(m *Machine) error {
	if m == nil || m.Offer.ID == "" {
		return fmt.Errorf("fake: machine requires an offer with an ID")
	}
	if m.Offer.Kind == "" || m.Offer.Lane == "" {
		return fmt.Errorf("fake: machine %q needs an offer kind and an execution lane", m.Offer.ID)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	m.Offer.RentalID = ""
	if m.Offer.KeepsWhatItRuns() {
		m.Offer.RentalID = m.Offer.ID
	}
	w.machines[m.Offer.ID] = m
	return nil
}

// Machine returns the registered machine by offer ID, for scenario scripts that
// mutate world state mid-timeline.
func (w *World) Machine(id string) (*Machine, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	machine, ok := w.machines[id]
	return machine, ok
}

// ListOffers derives the current offer set from world state at the scripted
// clock's now: machines whose lease has not expired, each stating what it holds
// and whether it is busy. Busy machines advertise unavailable capacity and
// their remaining maximum runtime as queue evidence.
func (w *World) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.clock.Now()
	var offers []domain.OfferSnapshot
	for _, machine := range w.machines {
		if machine.leaseExpiredAt(now) {
			continue
		}
		offers = append(offers, w.machineOffer(machine, now))
	}
	sort.Slice(offers, func(i, j int) bool { return offers[i].ID < offers[j].ID })
	return offers, nil
}

// Launch runs the workload and leaves what it fetched on the machine that ran
// it. Running an image is how a host becomes warm; capacity Mercator does not
// keep is cold again on the next Run.
func (w *World) Launch(ctx context.Context, request adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	receipt, err := w.Adapter.Launch(ctx, request)
	if err != nil {
		return receipt, err
	}
	w.recordExecution(request.SelectedOfferSnapshotID, request.Image)
	return receipt, nil
}

// recordExecution starts the image pull the launch implies. What the machine
// keeps afterwards is its own answer: only capacity Mercator keeps is still
// holding the content when the next Run asks.
func (w *World) recordExecution(offerID, image string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	machine, exists := w.machines[offerID]
	if !exists {
		return
	}
	now := w.clock.Now()
	machine.settle(now)
	machine.startPull(image, w.images[image].Layers, now)
}

// ResolveManifest answers what an image contains from this world's catalog. It
// is the simulated stand-in for a registry: the scheduler subtracts what a host
// holds from what this returns, and it says no the same five ways a real
// registry does.
func (w *World) ResolveManifest(_ context.Context, imageDigest string, _ domain.Platform) (domain.ImageManifest, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	image, known := w.images[imageDigest]
	if !known {
		return domain.ImageManifest{}, fmt.Errorf("%w: %s", ociresolver.ErrImageUnknown, imageDigest)
	}
	switch image.Registry {
	case RegistryUnresolvable:
		return domain.ImageManifest{}, fmt.Errorf("%w: %s", ociresolver.ErrManifestUnresolvable, imageDigest)
	case RegistryUnauthorized:
		return domain.ImageManifest{}, fmt.Errorf("%w: %s", ociresolver.ErrUnauthorized, imageDigest)
	case RegistryThrottled:
		return domain.ImageManifest{}, fmt.Errorf("%w: %s", ociresolver.ErrThrottled, imageDigest)
	case RegistryUnreachable:
		return domain.ImageManifest{}, fmt.Errorf("%w: %s", ociresolver.ErrUnreachable, imageDigest)
	}
	manifest := domain.ImageManifest{Known: true, Digest: domain.ReferenceDigest(imageDigest)}
	for _, layer := range image.Layers {
		manifest.Layers = append(manifest.Layers, domain.ImageLayer{
			Digest:          layer.Digest,
			DiffID:          layer.DiffID,
			CompressedBytes: layer.Bytes,
		})
	}
	return manifest, nil
}

func (w *World) machineOffer(machine *Machine, now time.Time) domain.OfferSnapshot {
	machine.settle(now)
	offer := machine.Offer
	offer.ObservedAt = now
	offer.ExpiresAt = now.Add(5 * time.Minute)
	offer.Images = machine.inventory(now)
	if machine.busyAt(now) {
		// Today's offer vocabulary marks a busy Rental unavailable. The target
		// Broker-owned RentalSchedule will keep it feasible and create a
		// queued Booking instead. It remains visible now so the decision records
		// the running Booking's expected (p50) remaining runtime as
		// queue-delay evidence; the enforced max bound backs latest-start math.
		offer.Capacity = domain.CapacityEvidence{Available: false, Confidence: 1}
		offer.Queue = &domain.QueueSnapshot{QueuedWorkSeconds: machine.expectedRemainingAt(now).Seconds(), ActiveSlots: 1}
		return offer
	}
	offer.Capacity = domain.CapacityEvidence{Available: true, Confidence: 1}
	offer.Queue = &domain.QueueSnapshot{}
	return offer
}
