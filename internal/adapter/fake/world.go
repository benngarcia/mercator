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
	// ArtifactReplicas is the local copy of each immutable Artifact version this
	// machine holds, by version ID. A copy is placement evidence and never a
	// dependency's authority: what makes an Artifact consumable is its durable
	// publication in the object store, which no machine owns.
	ArtifactReplicas map[string]domain.ArtifactReplica
	// HeldCaches is the mutable, application-owned state this machine holds,
	// keyed by cache identity. The identity carries the workspace, because that
	// is what makes two tenants naming one cache two caches on one host.
	HeldCaches map[string]domain.CacheMount
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
	// ProvisionSpend is what this world takes to turn a listing into a machine
	// that can run a container: acquisition, boot, and agent enrollment together.
	// It is deliberately not the estimate the offer publishes, which is a claim
	// the scheduler predicts from; a world that spent its own published
	// expectation would make that expectation right by construction. Standing
	// capacity spends none of it, because the machine is already there.
	ProvisionSpend time.Duration
	// UnpackSpend is what this machine takes to turn content on its disk into a
	// layer chain a container can start on, and ContainerStartSpend is what its
	// runtime takes to create the container and hold a process in it. Both are
	// stated durations for the reason ProvisionSpend is: a world computing the
	// arithmetic the predictor computes would make the prediction right by
	// construction.
	UnpackSpend         time.Duration
	ContainerStartSpend time.Duration
	// ClockAhead is how far this machine's wall clock runs ahead of the control
	// plane's. It changes nothing about when anything here happens and everything
	// about the moment this machine states when asked: a host does not know its
	// clock is wrong, so it reads its container's start off the clock it has.
	ClockAhead time.Duration

	// fetching is content this machine is still pulling. A host holds an image
	// when its bytes have arrived, not when the container was dispatched.
	fetching []transfer
}

// transfer is one execution's arrival on this machine: the image it had to
// fetch, the mutable caches the workload declared, and when the bytes have
// finished moving, which is when the workload starts.
type transfer struct {
	image  string
	layers []Layer
	caches []domain.CacheMount
	// bytes is the room this transfer has already claimed on the machine.
	// Content in flight is not resident and its space is not free either.
	bytes       int64
	completesAt time.Time
}

// startExecution begins one launch on this machine: it fetches what the image
// still needs, and it opens the caches the workload declared. Both are what the
// machine holds afterwards, and both land when the bytes have arrived, because a
// workload cannot have opened anything before it started.
//
// Capacity Mercator does not keep is left out of both: there is no host there to
// be holding anything when the next Run asks.
//
// A cache is opened at the start of the execution rather than at its end,
// because creating the container is what creates the storage. The Lab attaches
// one at that same moment and a container runtime reports one on the same
// evidence, so all three say a machine holds a cache from when a workload of
// that tenant and generation started here: one cancelled halfway leaves the
// cache it was attached to, and one that never started leaves nothing.
// It answers when the container starts, which is when the machine exists and the
// bytes it needs have landed on it. That answer is made for every machine,
// including the ones that keep nothing: what such a machine does not do is hold
// the content afterwards, and that is a different question from when its workload
// began.
func (m *Machine) startExecution(image string, layers []Layer, caches []domain.CacheMount, now time.Time) time.Time {
	var bytes int64
	for _, layer := range layers {
		if _, held := m.HeldLayers[layer.Digest]; !held {
			bytes += layer.Bytes
		}
	}
	// Nothing can be fetched onto a machine that does not exist yet, so the world
	// spends acquisition, boot, and agent enrollment before the pull begins. Bytes
	// that land are then applied, and only then does a runtime hand back a
	// process: a launch is a waterfall, and a stage that costs nothing here is a
	// stage no prediction of it could ever be measured against.
	readyAt := now.Add(m.ProvisionSpend)
	startsAt := readyAt.
		Add(transferDuration(bytes)).
		Add(m.assemblySpend(bytes, layers)).
		Add(m.ContainerStartSpend)
	if !m.Offer.KeepsWhatItRuns() {
		return startsAt
	}
	m.fetching = append(m.fetching, transfer{
		image:       image,
		layers:      layers,
		caches:      caches,
		bytes:       bytes,
		completesAt: startsAt,
	})
	return startsAt
}

// settle applies every execution whose bytes have arrived by now. Until then the
// machine holds what it held before, because an image that is still being
// fetched is not on the host and a workload waiting on it has opened nothing.
func (m *Machine) settle(now time.Time) {
	arrived := m.fetching[:0]
	for _, pull := range m.fetching {
		if now.Before(pull.completesAt) {
			arrived = append(arrived, pull)
			continue
		}
		m.keep(pull.image, pull.layers)
		m.openCaches(pull.caches, pull.completesAt)
	}
	m.fetching = arrived
}

// openCaches is the mutable state a workload that started here left behind,
// filed under the full identity so a second tenant naming one cache gets its
// own. The generation this machine already had keeps the moment it began: a
// holder can say when a cache started existing here and nothing about what was
// written into it since.
func (m *Machine) openCaches(caches []domain.CacheMount, at time.Time) {
	if len(caches) == 0 {
		return
	}
	if m.HeldCaches == nil {
		m.HeldCaches = map[string]domain.CacheMount{}
	}
	for _, cache := range caches {
		if held, existing := m.HeldCaches[cache.Identity()]; existing {
			cache.CreatedAt = held.CreatedAt
		} else {
			cache.CreatedAt = at
		}
		m.HeldCaches[cache.Identity()] = cache
	}
}

// publishedInventory is what an offer for this machine can carry. Mercator
// enumerates a machine only where it runs something of its own, so a slot it
// borrows and a machine that does not exist yet report nothing, which is what
// every provider adapter in the tree publishes. What such a machine holds stays
// true and stays readable as world state; no offer says it.
// capacityConfidence is how sure this machine's publisher is that the capacity it
// is claiming is there. A machine registered without an opinion is certain, which
// is what a simulated provider can honestly say about a machine it can see: it
// answers from world state rather than from a stale catalog.
func (m *Machine) capacityConfidence() float64 {
	if m.Offer.Capacity.Confidence == 0 {
		return 1
	}
	return m.Offer.Capacity.Confidence
}

func (m *Machine) publishedInventory(now time.Time) domain.ImageInventory {
	if !m.Offer.KeepsWhatItRuns() {
		return domain.ImageInventory{}
	}
	return m.inventory(now)
}

// publishedArtifacts is the Artifact content an offer for this machine can
// carry, in version-ID order so one world state produces one offer. It follows
// the same rule as the image inventory: a machine nothing of Mercator's runs on
// enumerates nothing, so its silence is never read as an empty disk.
func (m *Machine) publishedArtifacts(now time.Time) domain.ArtifactInventory {
	if !m.Offer.KeepsWhatItRuns() {
		return domain.ArtifactInventory{}
	}
	inventory := domain.ArtifactInventory{Known: true, ObservedAt: now}
	for _, id := range slices.Sorted(maps.Keys(m.ArtifactReplicas)) {
		inventory.Replicas = append(inventory.Replicas, m.ArtifactReplicas[id])
	}
	return inventory
}

// publishedCaches is the mutable state an offer for this machine can carry, in
// identity order so one world state produces one offer. It follows the same rule
// as the other two inventories: capacity nothing of Mercator's runs on
// enumerates nothing, and its silence is never read as an empty disk.
func (m *Machine) publishedCaches(now time.Time) domain.CacheInventory {
	if !m.Offer.KeepsWhatItRuns() {
		return domain.CacheInventory{}
	}
	inventory := domain.CacheInventory{Known: true, ObservedAt: now}
	for _, identity := range slices.Sorted(maps.Keys(m.HeldCaches)) {
		inventory.Mounts = append(inventory.Mounts, m.HeldCaches[identity])
	}
	return inventory
}

// residentBytes is what this machine's content takes up: the layers it holds
// and the Artifact copies on it. A Cache Mount is missing from the sum on
// purpose, because nothing in this world's vocabulary can size one: a container
// runtime prices a volume only by walking every volume on the host, so the
// record a machine reports carries no size and this world reports exactly what a
// machine could. The Lab states cache sizes where it states World Truth, which
// is a different claim made by a different authority.
func (m *Machine) residentBytes() int64 {
	resident := int64(0)
	for _, bytes := range m.HeldLayers {
		resident += bytes
	}
	for _, replica := range m.ArtifactReplicas {
		resident += replica.SizeBytes
	}
	return resident
}

// freeDiskBytes is the room this machine has left: its disk, less what is on it,
// less what it has promised to content still arriving. It is what an offer for
// this machine states, because a machine that advertised its whole disk however
// full it was could never turn work away.
func (m *Machine) freeDiskBytes() int64 {
	free := m.Offer.Resources.EphemeralDiskBytes - m.residentBytes()
	for _, pull := range m.fetching {
		free -= pull.bytes
	}
	return max(free, 0)
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

// inventory is what this machine HOLDS, whatever Run is being placed, in the
// digest space its runtime can enumerate. What a Run would still have to fetch
// is the scheduler's subtraction against the manifest, so the world asserts no
// answer about an image the offer does not name.
//
// Every machine answers, including the ones nothing of Mercator's runs on.
// Whether that answer reaches Placement is decided once, where the offer is
// published.
func (m *Machine) inventory(now time.Time) domain.ImageInventory {
	inventory := domain.ImageInventory{Known: true, ObservedAt: now}
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

// assemblySpend is what applying this launch's content costs here. A machine with
// nothing to apply spends nothing: unpacking is work over bytes, and a host
// holding the image assembled has none of it to do.
func (m *Machine) assemblySpend(fetched int64, layers []Layer) time.Duration {
	if fetched > 0 {
		return m.UnpackSpend
	}
	for _, layer := range layers {
		if m.Packed[layer.Digest] || m.Packed[layer.DiffID] {
			return m.UnpackSpend
		}
	}
	return 0
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
	// artifacts is this world's object store: what each immutable version is,
	// and whether its bytes are there. It is the durable authority a consumer's
	// admission is answered from, and it is deliberately not assembled from what
	// machines report holding.
	artifacts map[string]domain.ArtifactVersion
	// machines is every offer this world can list, keyed by offer ID. A Rental
	// and a marketplace offer are the same kind of entry: what separates them
	// is whether the offer names capacity Mercator keeps.
	machines map[string]*Machine
	// startsAt is when each launch's container actually begins, keyed by launch
	// key. The provider reports running from the moment it accepts, exactly as
	// every provider in the tree does, so this is the only place the world holds
	// the moment a process began and the only thing an observation can report it
	// from once it has arrived.
	startsAt map[string]time.Time
	// readyAt is when each workload here really begins serving, which is what makes
	// a report due. The moment the report carries is the same one read on its host's
	// clock, and for one machine in this corpus those are not the same moment.
	readyAt map[string]time.Time
	// statedStarts is the same moment as each machine holding it would report it,
	// which is world truth read on that host's own clock. The two are one map for
	// every machine that keeps Mercator's clock and two for a machine that does
	// not, and a world that held only the truth could not state the case where a
	// host publishes a moment Mercator has not reached.
	statedStarts map[string]time.Time
	// ApplicationReadySpend is how long after its process starts a workload here
	// reports that it can do work. It is a fact about the applications in this
	// world rather than about any machine: a runtime that started a container
	// cannot see whether the thing inside it is serving.
	ApplicationReadySpend time.Duration
	// readiness is every workload that will report itself ready and has not done
	// so yet, keyed by launch key. A workload reports once, so an entry is dropped
	// when it is handed over: a report re-delivered on every look would tell
	// Mercator the same thing forever.
	readiness map[string]ReadinessReport
}

// ReadinessReport is one workload telling Mercator it can do work, with the
// moment it could. The moment travels in the report because it is the
// application's own: a readiness stamped when the control plane recorded it would
// move with how often the control plane looks.
type ReadinessReport struct {
	WorkspaceID string
	RunID       string
	ReadyAt     time.Time
}

func NewWorld(clock *Clock, options ...Option) *World {
	options = append([]Option{WithNow(clock.Now)}, options...)
	return &World{
		Adapter:      New(options...),
		clock:        clock,
		images:       map[string]Image{},
		artifacts:    map[string]domain.ArtifactVersion{},
		machines:     map[string]*Machine{},
		startsAt:     map[string]time.Time{},
		readyAt:      map[string]time.Time{},
		statedStarts: map[string]time.Time{},
		readiness:    map[string]ReadinessReport{},
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

// DefineArtifact records one immutable version in this world's object store.
// Whether its bytes are there is the version's own PublishedAt: a name the store
// holds and has not received is content nothing can yet read, however many
// machines are sitting on a copy.
func (w *World) DefineArtifact(version domain.ArtifactVersion) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.artifacts[version.ID] = version
}

// ArtifactVersion is the object store answering what one version is. A name it
// never heard of, and a name belonging to another workspace, both come back
// zero, which is not durable: Artifacts are never shared across workspaces, and
// from a consumer's side an unknown version and an unpublished one are one fact.
func (w *World) ArtifactVersion(_ context.Context, workspaceID, artifactID string) (domain.ArtifactVersion, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	version, known := w.artifacts[artifactID]
	if !known || version.WorkspaceID != workspaceID {
		return domain.ArtifactVersion{}, nil
	}
	return version, nil
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
	if resident := m.residentBytes(); resident > m.Offer.Resources.EphemeralDiskBytes {
		return fmt.Errorf(
			"fake: machine %q holds %d bytes of content and has %d bytes of disk",
			m.Offer.ID, resident, m.Offer.Resources.EphemeralDiskBytes,
		)
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

// Launch runs the workload and leaves what it fetched and what it opened on the
// machine that ran it. Running is how a host becomes warm, for an image and for
// a cache alike; capacity Mercator does not keep is cold again on the next Run.
func (w *World) Launch(ctx context.Context, request adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	receipt, err := w.Adapter.Launch(ctx, request)
	if err != nil {
		return receipt, err
	}
	w.recordExecution(request)
	return receipt, nil
}

// recordExecution starts the pull the launch implies and declares the caches it
// will open. What the machine keeps afterwards is its own answer: only capacity
// Mercator keeps is still holding any of it when the next Run asks.
//
// It also records when the container will begin, which is what makes the world's
// own start moment a fact an observation can report rather than something a
// reader has to infer from the launch being accepted.
func (w *World) recordExecution(request adapter.LaunchRequest) {
	w.mu.Lock()
	defer w.mu.Unlock()
	machine, exists := w.machines[request.SelectedOfferSnapshotID]
	if !exists {
		return
	}
	now := w.clock.Now()
	machine.settle(now)
	startsAt := machine.startExecution(
		request.Image, w.images[request.Image].Layers, declaredCaches(request), now,
	)
	w.startsAt[request.LaunchKey] = startsAt
	// What the machine will say when asked, which is the moment above read on its
	// own clock. World truth stays above: this world knows when the container
	// really began, and the host reporting it does not know its clock is wrong.
	w.statedStarts[request.LaunchKey] = startsAt.Add(machine.ClockAhead)
	readyAt := startsAt.Add(w.ApplicationReadySpend)
	w.readyAt[request.LaunchKey] = readyAt
	w.readiness[request.LaunchKey] = ReadinessReport{
		WorkspaceID: request.WorkspaceID,
		RunID:       request.RunID,
		// The application reads the clock of the host it runs on, so what it states
		// is the moment above on that machine's clock. World truth stays in readyAt:
		// this world knows when the workload really began serving, and the workload
		// reporting it does not know its host's clock is wrong.
		ReadyAt: readyAt.Add(machine.ClockAhead),
	}
}

// DueReadinessReports is every workload here that has become ready and has not
// said so yet. It is an inbound callback rather than something an observation
// carries, because the workload is the only authority on whether it can do work.
func (w *World) DueReadinessReports() []ReadinessReport {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.clock.Now()
	var due []ReadinessReport
	for _, launchKey := range slices.Sorted(maps.Keys(w.readiness)) {
		report := w.readiness[launchKey]
		// Due on the world's own clock rather than on the clock the report states,
		// because when a workload becomes ready is a fact about the world and the
		// moment it names is a fact about its host.
		if now.Before(w.readyAt[launchKey]) {
			continue
		}
		delete(w.readiness, launchKey)
		due = append(due, report)
	}
	return due
}

// Observe is the embedded adapter's answer with the world's own start moment on
// it. The phase says running from the moment the launch was accepted, which is
// what every provider in this tree does and why a phase can never establish a
// start; the moment the container began is a separate fact, reported once it has
// happened and left absent until then.
func (w *World) Observe(ctx context.Context, request adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	observation, err := w.Adapter.Observe(ctx, request)
	if err != nil {
		return observation, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	startsAt, launched := w.startsAt[request.LaunchKey]
	if !launched || w.clock.Now().Before(startsAt) {
		return observation, nil
	}
	// The machine states the moment on its own clock. That is the same clock the
	// control plane keeps for every machine in this corpus but one, and the
	// exception is the point: a host running ahead publishes a start that has
	// arrived here and not there.
	stated := w.statedStarts[request.LaunchKey]
	observation.StartedAt = &stated
	return observation, nil
}

// declaredCaches is the mutable state this launch asks its host to attach, named
// by the identity that carries the workspace the Run belongs to. A workload
// cannot choose its own workspace, so the scoping is the request's and never the
// declaration's.
func declaredCaches(request adapter.LaunchRequest) []domain.CacheMount {
	caches := make([]domain.CacheMount, 0, len(request.CacheMounts))
	for _, mount := range request.CacheMounts {
		caches = append(caches, domain.CacheMount{
			WorkspaceID:      request.WorkspaceID,
			Name:             mount.Name,
			CompatibilityKey: mount.CompatibilityKey,
		})
	}
	return caches
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
	// What a machine offers is the room it has left rather than the disk it was
	// built with, because content it is already holding is not room a Run can
	// have.
	offer.Resources.EphemeralDiskBytes = machine.freeDiskBytes()
	offer.Images = machine.publishedInventory(now)
	offer.Artifacts = machine.publishedArtifacts(now)
	offer.Caches = machine.publishedCaches(now)
	if machine.busyAt(now) {
		// Today's offer vocabulary marks a busy Rental unavailable. The target
		// Broker-owned RentalSchedule will keep it feasible and create a
		// queued Booking instead. It remains visible now so the decision records
		// the running Booking's expected (p50) remaining runtime as
		// queue-delay evidence; the enforced max bound backs latest-start math.
		offer.Capacity = domain.CapacityEvidence{Available: false, Confidence: machine.capacityConfidence()}
		offer.Queue = &domain.QueueSnapshot{QueuedWorkSeconds: machine.expectedRemainingAt(now).Seconds(), ActiveSlots: 1}
		return offer
	}
	offer.Capacity = domain.CapacityEvidence{Available: true, Confidence: machine.capacityConfidence()}
	offer.Queue = &domain.QueueSnapshot{}
	return offer
}
