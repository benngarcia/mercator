package fake

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
)

// Clock is a scripted wall clock shared by a World, its daemons, and the
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

// Daemon is a simulated standing Docker endpoint the broker owns: the rental
// of the warm-rentals program. It holds image layers and named data caches,
// may be busy with running work, and may sit inside an idle lease.
type Daemon struct {
	// Offer is the capacity the daemon advertises: resources, pricing,
	// platform, capabilities. Kind, ObservedAt, ExpiresAt, Queue, Capacity,
	// and ImageCache are overwritten by the world at listing time.
	Offer domain.OfferSnapshot
	// HeldLayers maps layer digest to bytes already present on the daemon.
	HeldLayers map[string]int64
	// HeldImages is every image reference the daemon holds whole, which is what
	// lets a repeat of the same image skip layer arithmetic entirely.
	HeldImages map[string]bool
	// HeldCaches maps named data cache key (e.g. a dataset GID) to bytes
	// materialized on the daemon's local disk. No offer field carries this
	// today; the world holds it so cache-evidence milestones can surface it.
	HeldCaches map[string]int64
	// BusyUntil is when the running work's enforced maximum runtime elapses;
	// zero means idle. It is the hard ceiling behind latest-start guarantees.
	BusyUntil time.Time
	// ExpectedBusyUntil is when the running work is expected (p50) to finish,
	// defaulting to BusyUntil. The expected remaining time is what queue-delay
	// scoring weighs.
	ExpectedBusyUntil time.Time
	// FreesAt is when the daemon is actually observed free again. It defaults
	// to ExpectedBusyUntil; another value models a run finishing early, or
	// overrunning its estimate up to the enforced bound, which lets a scenario
	// hold a Rental busy past a queued Booking's latest start.
	FreesAt time.Time
	// LeaseExpiresAt is when the daemon's idle lease ends; zero means no
	// lease bound. An expired daemon stops being offered, standing in for
	// janitor termination until the rental lifecycle exists.
	LeaseExpiresAt time.Time
}

// lane is what this daemon becomes once allocated. A daemon stands in for
// capacity the broker holds, so an unstated lane is reusable.
func (d *Daemon) lane() domain.ExecutionLane {
	if d.Offer.Lane == "" {
		return domain.LaneReusable
	}
	return d.Offer.Lane
}

// ran records that this host executed an image: the layers the workload
// fetched, and the image itself, stay on the machine. Capacity outside the
// reusable lane keeps nothing, because there is no machine left to keep it.
func (d *Daemon) ran(image string, layers []Layer) {
	if !d.lane().Reusable() {
		return
	}
	if d.HeldLayers == nil {
		d.HeldLayers = map[string]int64{}
	}
	if d.HeldImages == nil {
		d.HeldImages = map[string]bool{}
	}
	for _, layer := range layers {
		d.HeldLayers[layer.Digest] = layer.Bytes
	}
	d.HeldImages[image] = true
}

func (d *Daemon) busyAt(now time.Time) bool {
	frees := d.FreesAt
	if frees.IsZero() {
		frees = d.expectedBusyUntil()
	}
	return now.Before(frees)
}

func (d *Daemon) expectedBusyUntil() time.Time {
	if !d.ExpectedBusyUntil.IsZero() {
		return d.ExpectedBusyUntil
	}
	return d.BusyUntil
}

func (d *Daemon) expectedRemainingAt(now time.Time) time.Duration {
	if remaining := d.expectedBusyUntil().Sub(now); remaining > 0 {
		return remaining
	}
	return 0
}

func (d *Daemon) leaseExpiredAt(now time.Time) bool {
	return !d.LeaseExpiresAt.IsZero() && !now.Before(d.LeaseExpiresAt)
}

// World extends the fake adapter with simulated capacity state: daemons
// (rentals) with image-layer and named-cache contents, scripted running work,
// a marketplace of provisionable offers, and a scripted clock. Launch,
// observe, and cleanup reuse the embedded fake adapter unchanged; only offer
// listing is derived from world state.
type World struct {
	*Adapter
	clock       *Clock
	mu          sync.Mutex
	images      map[string][]Layer
	daemons     map[string]*Daemon
	marketplace map[string]domain.OfferSnapshot
	// placementImage is the image the next placement asks for. ListOffers
	// receives resources but not image identity, so honest per-image layer
	// evidence needs the caller to state the image ahead of evaluation.
	placementImage string
}

func NewWorld(clock *Clock, options ...Option) *World {
	options = append([]Option{WithNow(clock.Now)}, options...)
	return &World{
		Adapter:     New(options...),
		clock:       clock,
		images:      map[string][]Layer{},
		daemons:     map[string]*Daemon{},
		marketplace: map[string]domain.OfferSnapshot{},
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

// AddDaemon registers a simulated rental. The daemon's offer ID is its
// identity in placement decisions.
func (w *World) AddDaemon(d *Daemon) error {
	if d == nil || d.Offer.ID == "" {
		return fmt.Errorf("fake: daemon requires an offer with an ID")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.daemons[d.Offer.ID] = d
	return nil
}

// AddMarketplaceOffer registers a provisionable offer visible on the
// simulated marketplace.
func (w *World) AddMarketplaceOffer(offer domain.OfferSnapshot) error {
	if offer.ID == "" {
		return fmt.Errorf("fake: marketplace offer requires an ID")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.marketplace[offer.ID] = offer
	return nil
}

// Daemon returns the registered daemon by offer ID, for scenario scripts that
// mutate world state mid-timeline.
func (w *World) Daemon(id string) (*Daemon, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	d, ok := w.daemons[id]
	return d, ok
}

// SetPlacementImage states which image the next placement is for, so listed
// offers carry honest image-layer evidence for that image. The offer request
// contract carries resources but not image identity; until it does, the
// simulation needs the image declared ahead of evaluation.
func (w *World) SetPlacementImage(ref string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.images[ref]; !ok {
		return fmt.Errorf("fake: placement image %q is not defined in the world", ref)
	}
	w.placementImage = ref
	return nil
}

// ListOffers derives the current offer set from world state at the scripted
// clock's now: daemons whose lease has not expired (busy ones advertise
// unavailable capacity and their remaining maximum runtime as queue
// evidence), plus every marketplace offer with a full-image pull ahead.
func (w *World) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.clock.Now()
	if w.placementImage == "" && len(w.images) > 0 {
		return nil, fmt.Errorf("fake: placement image not set; call SetPlacementImage before evaluating placement")
	}
	layers := w.images[w.placementImage]
	var offers []domain.OfferSnapshot
	for _, daemon := range w.daemons {
		if daemon.leaseExpiredAt(now) {
			continue
		}
		offers = append(offers, w.daemonOffer(daemon, now, layers))
	}
	for _, offer := range w.marketplace {
		offers = append(offers, w.marketplaceOffer(offer, now, layers))
	}
	return offers, nil
}

// Launch runs the workload and leaves what it fetched on the host that ran it.
// Running an image is how a machine becomes warm; capacity that does not
// survive its workload is cold again on the next Run.
func (w *World) Launch(ctx context.Context, request adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	receipt, err := w.Adapter.Launch(ctx, request)
	if err != nil {
		return receipt, err
	}
	w.recordExecution(request.SelectedOfferSnapshotID, request.Image)
	return receipt, nil
}

// recordExecution warms the host an execution landed on. Only a daemon is a
// host at all: a marketplace offer is a machine that does not exist yet and
// will not exist once its workload exits.
func (w *World) recordExecution(offerID, image string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	daemon, standing := w.daemons[offerID]
	if !standing {
		return
	}
	daemon.ran(image, w.images[image])
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

func (w *World) daemonOffer(daemon *Daemon, now time.Time, layers []Layer) domain.OfferSnapshot {
	offer := daemon.Offer
	offer.Kind = domain.OfferKindStanding
	// A daemon in the simulated world stands in for capacity Mercator holds,
	// so it keeps the Rental identity its lane entitles it to.
	offer.Lane = daemon.lane()
	if offer.Lane.Reusable() {
		offer.RentalID = offer.ID
	} else {
		offer.RentalID = ""
	}
	offer.ObservedAt = now
	offer.ExpiresAt = now.Add(5 * time.Minute)
	// The daemon states which layers it holds. What is missing is the
	// scheduler's subtraction, not a number this world asserts.
	offer.Images = domain.ImageInventory{
		Known:        true,
		ObservedAt:   now,
		ImageDigests: slices.Sorted(maps.Keys(daemon.HeldImages)),
	}
	for _, layer := range layers {
		if _, held := daemon.HeldLayers[layer.Digest]; held {
			offer.Images.LayerDigests = append(offer.Images.LayerDigests, layer.Digest)
		}
	}
	if daemon.busyAt(now) {
		// Today's offer vocabulary marks a busy Rental unavailable. The target
		// Broker-owned RentalSchedule will keep it feasible and create a
		// queued Booking instead. It remains visible now so the decision records
		// the running Booking's expected (p50) remaining runtime as
		// queue-delay evidence; the enforced max bound backs latest-start math.
		offer.Capacity = domain.CapacityEvidence{Available: false, Confidence: 1}
		offer.Queue = &domain.QueueSnapshot{QueuedWorkSeconds: daemon.expectedRemainingAt(now).Seconds(), ActiveSlots: 1}
	} else {
		offer.Capacity = domain.CapacityEvidence{Available: true, Confidence: 1}
		offer.Queue = &domain.QueueSnapshot{}
	}
	return offer
}

func (w *World) marketplaceOffer(offer domain.OfferSnapshot, now time.Time, layers []Layer) domain.OfferSnapshot {
	offer.Kind = domain.OfferKindProvisionable
	offer.RentalID = ""
	if offer.Lane == "" {
		offer.Lane = domain.LaneReusable
	}
	offer.ObservedAt = now
	offer.ExpiresAt = now.Add(5 * time.Minute)
	// A machine that does not exist yet holds nothing, and says so.
	offer.Images = domain.ImageInventory{Known: true, ObservedAt: now}
	offer.Capacity = domain.CapacityEvidence{Available: true, Confidence: 1}
	return offer
}
