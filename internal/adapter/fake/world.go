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
)

// registryMbps is how fast this world moves image content onto a machine. It is
// the world's own transfer model, deliberately independent of the scheduler's
// prediction: a fixture is only meaningful when the actual pull and the
// predicted pull are produced by different code.
const registryMbps = 500.0

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

// Layer is one content-addressed slice of an image: shared layers across
// images are what make warm-rental affinity worth modeling.
type Layer struct {
	Digest string
	Bytes  int64
}

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
	// HeldLayers maps layer digest to bytes already present on the machine.
	HeldLayers map[string]int64
	// HeldImages is every image reference the machine holds whole, which is what
	// lets a repeat of the same image skip layer arithmetic entirely.
	HeldImages map[string]bool
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

func (m *Machine) keep(image string, layers []Layer) {
	if m.HeldLayers == nil {
		m.HeldLayers = map[string]int64{}
	}
	if m.HeldImages == nil {
		m.HeldImages = map[string]bool{}
	}
	for _, layer := range layers {
		m.HeldLayers[layer.Digest] = layer.Bytes
	}
	m.HeldImages[image] = true
}

// inventory is what this machine says it holds, whatever Run is being placed.
// What a Run would still have to fetch is the scheduler's subtraction against
// the manifest, so the world asserts no answer about an image the offer does
// not name.
func (m *Machine) inventory(now time.Time) domain.ImageInventory {
	return domain.ImageInventory{
		Known:        true,
		ObservedAt:   now,
		ImageDigests: slices.Sorted(maps.Keys(m.HeldImages)),
		LayerDigests: slices.Sorted(maps.Keys(m.HeldLayers)),
	}
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
	images map[string][]Layer
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
		images:   map[string][]Layer{},
		machines: map[string]*Machine{},
	}
}

func (w *World) Clock() *Clock { return w.clock }

// DefineImage registers an image as its ordered layers. Layer digests are
// shared identity across images: two images listing the same digest share
// that layer, which is what layer-affinity scenarios exercise.
func (w *World) DefineImage(ref string, layers []Layer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.images[ref] = append([]Layer(nil), layers...)
}

// AddMachine registers a simulated rental: a machine Mercator holds and can run
// successive workloads on. The machine's offer ID is its identity in placement
// decisions, and its lane is reusable by construction, because that is what
// makes it a Rental rather than a one-shot execution product.
func (w *World) AddMachine(m *Machine) error {
	if m == nil || m.Offer.ID == "" {
		return fmt.Errorf("fake: machine requires an offer with an ID")
	}
	if m.Offer.Lane != "" && !m.Offer.Lane.Reusable() {
		return fmt.Errorf("fake: machine %q cannot be in the %q lane; a Rental is reusable capacity", m.Offer.ID, m.Offer.Lane)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	m.Offer.Kind = domain.OfferKindStanding
	m.Offer.Lane = domain.LaneReusable
	m.Offer.RentalID = m.Offer.ID
	w.machines[m.Offer.ID] = m
	return nil
}

// AddMarketplaceOffer registers a provisionable offer visible on the simulated
// marketplace: a machine that does not exist yet, and so holds nothing until
// something runs on it.
func (w *World) AddMarketplaceOffer(offer domain.OfferSnapshot) error {
	if offer.ID == "" {
		return fmt.Errorf("fake: marketplace offer requires an ID")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	offer.Kind = domain.OfferKindProvisionable
	offer.RentalID = ""
	if offer.Lane == "" {
		offer.Lane = domain.LaneReusable
	}
	w.machines[offer.ID] = &Machine{Offer: offer}
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
	machine.startPull(image, w.images[image], now)
}

// ResolveManifest answers what an image contains from this world's catalog. It
// is the simulated stand-in for a registry: the scheduler subtracts what a host
// holds from what this returns.
func (w *World) ResolveManifest(_ context.Context, imageDigest string, _ domain.Platform) (domain.ImageManifest, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	layers, known := w.images[imageDigest]
	if !known {
		return domain.ImageManifest{}, nil
	}
	manifest := domain.ImageManifest{Known: true, Digest: imageDigest}
	for _, layer := range layers {
		manifest.Layers = append(manifest.Layers, domain.ImageLayer{Digest: layer.Digest, CompressedBytes: layer.Bytes})
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
