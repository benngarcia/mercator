package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/scenario"
)

const (
	labWorkspace  = "ws_lab"
	labConnection = "connection:lab"
	// labProvider is the backend a listing comes from when the Blueprint names no
	// provider for it.
	labProvider = "lab"
)

type externalExecution struct {
	ExternalID     string                `json:"external_id"`
	RunID          string                `json:"run_id"`
	AttemptID      string                `json:"attempt_id"`
	LaunchKey      string                `json:"launch_key"`
	OwnershipToken string                `json:"ownership_token"`
	RequestHash    string                `json:"request_hash"`
	OfferID        string                `json:"offer_id"`
	WorkspaceID    string                `json:"workspace_id"`
	Disposition    domain.Disposition    `json:"disposition"`
	Phase          adapter.ExternalPhase `json:"phase"`
	// CacheMounts is the mutable state Mercator asked this launch to attach. It
	// is taken from the launch command rather than from the arrival, so what the
	// world reads and writes is what the control plane actually declared.
	CacheMounts []domain.CacheMountRequirement `json:"cache_mounts,omitempty"`
	// AcceptedAt is when the provider took the launch. ReadyAt is when there was a
	// machine here able to run a container, which for a machine that did not exist
	// yet is after the world has spent acquisition, boot, and agent enrollment on
	// making one. StartedAt is when the container actually began, which cannot
	// precede either that machine existing or the arrival of the image it runs: a
	// process cannot execute bytes that have not landed, and it cannot execute on
	// a host nobody has built. The gap between accepted and started is the start
	// latency Mercator predicted and now has an actual for.
	AcceptedAt time.Time `json:"accepted_at"`
	ReadyAt    time.Time `json:"ready_at"`
	// ImageAt and ArtifactsAt are when each transfer this launch was waiting on
	// finished. They are two moments because they are two stages over two kinds of
	// content from two authorities, and a reader given only the later of them
	// could not tell a machine that has to fetch an image from one that has to
	// read a dataset forty times its size.
	ImageAt     time.Time `json:"image_at"`
	ArtifactsAt time.Time `json:"artifacts_at"`
	// AssembledAt is when the content that landed had become a layer chain a
	// container can start on. A machine holding every byte of an image it never
	// applied is past the transfer and not past this.
	AssembledAt time.Time `json:"assembled_at"`
	StartedAt   time.Time `json:"started_at"`
	// ApplicationReadyAt is when this workload reports that it can do work. It is
	// after StartedAt by however long the world says an application here takes to
	// come up, which is the whole point of separating the two: a running process
	// is not a serving one.
	ApplicationReadyAt time.Time `json:"application_ready_at"`
	// ReadinessReported is whether Mercator has already been told. A workload
	// reports its readiness once, and a world that reported it on every look would
	// append the same fact forever.
	ReadinessReported bool `json:"readiness_reported"`
	// CachesAttached is whether the container was created and its caches opened
	// with it. It happens at StartedAt rather than at the end, because creating
	// the container is what creates the storage: a workload cancelled halfway
	// leaves the cache it was attached to, and one that never started leaves
	// nothing.
	CachesAttached bool      `json:"caches_attached"`
	CompletesAt    time.Time `json:"completes_at"`
	OutputsStored  bool      `json:"outputs_stored"`
	// ReservedDiskBytes is the ephemeral disk this Run declared it needs. The
	// machine holds it back for as long as the workload is running, because a
	// floor two Runs were both promised is a floor neither of them has.
	ReservedDiskBytes int64 `json:"reserved_disk_bytes,omitempty"`
}

// ArtifactReplica is one host-local copy in World Truth: which machine holds
// it, and what that copy is worth. It is deliberately not a boolean. A copy
// nobody checked against the catalog is not evidence that the right bytes are
// there, and a copy that exists at all is not evidence that the Artifact does.
type ArtifactReplica struct {
	OfferID string `json:"offer_id"`
	domain.ArtifactReplica
}

// CacheMountState is one mutable, application-owned cache in World Truth: which
// machine holds it, whose it is, which generation of content is under the name,
// and how many times a workload has written it. The identity is stated beside
// the three parts it is derived from rather than left for every reader to
// recompute, because a rule about workspace isolation has to be able to catch a
// world that keyed a cache by something narrower than its own identity.
type CacheMountState struct {
	OfferID  string `json:"offer_id"`
	Identity string `json:"identity"`
	// WorkspaceID is who owns this cache. Two workspaces naming one cache on
	// one host hold two caches, and this is the field that makes them two.
	WorkspaceID      string `json:"workspace_id"`
	Name             string `json:"name"`
	CompatibilityKey string `json:"compatibility_key,omitempty"`
	Revision         uint64 `json:"revision"`
	// CreatedAt is when this generation started existing here. A host can hold
	// several generations under one name, so which of them is the one under the
	// name now is a question only the moments can answer.
	CreatedAt time.Time `json:"created_at"`
	// SizeBytes is how much room this cache takes here. It is world truth a
	// fixture states, because nothing on a real node measures it.
	SizeBytes int64 `json:"size_bytes"`
}

// mount is this cache as the machine holding it would report it.
func (state CacheMountState) mount() domain.CacheMount {
	return domain.CacheMount{
		WorkspaceID:      state.WorkspaceID,
		Name:             state.Name,
		CompatibilityKey: state.CompatibilityKey,
		CreatedAt:        state.CreatedAt,
	}
}

type WorldTruthSnapshot struct {
	At               time.Time              `json:"at"`
	Offers           []domain.OfferSnapshot `json:"offers"`
	ActiveExecutions []externalExecution    `json:"active_executions"`
	ArtifactReplicas []ArtifactReplica      `json:"artifact_replicas"`
	// PublishedPaths is every network fact each machine in this world published
	// about the paths it crosses, by offer, including machines this world has since
	// retired. Offers above are the fleet as it stands; this is what the fleet has
	// said, which is what a rule about a decision already taken has to ask.
	PublishedPaths map[string][]domain.NetworkFact `json:"published_paths,omitempty"`
	CacheMounts    []CacheMountState               `json:"cache_mounts"`
	// Disk is what each machine's content is taking up, stated as World Truth
	// because an offer states only what is left. A rule that read the remainder
	// could never catch a world that lost track of the difference.
	Disk []DiskLedger `json:"disk"`
}

// DiskLedger is one machine's account of its own disk: the room it has, every
// item of content taking some of it, and the bytes promised to content that is
// still arriving. Disk is a real resource here for the same reason it is on a
// machine: content Mercator puts somewhere has to fit, and an offer that stated
// a fixed number whatever it was holding is an offer that never says no.
type DiskLedger struct {
	OfferID string `json:"offer_id"`
	// CapacityBytes is the machine's whole disk, which is what the Blueprint
	// declared for it.
	CapacityBytes int64 `json:"capacity_bytes"`
	// Resident is every item of content on this machine, each named by the
	// content it is. It is stated item by item rather than as a total, because a
	// sum cannot be checked for counting one thing twice.
	Resident []ResidentContent `json:"resident,omitempty"`
	// ReservedBytes is room committed to content still moving onto this machine.
	// A transfer in flight has not landed and its room is already spoken for, so
	// a ledger that counted only what arrived would let a host promise the same
	// gigabytes to two Runs.
	ReservedBytes int64 `json:"reserved_bytes,omitempty"`
}

// ResidentBytes is what this machine's content adds up to.
func (ledger DiskLedger) ResidentBytes() int64 {
	total := int64(0)
	for _, item := range ledger.Resident {
		total += item.SizeBytes
	}
	return total
}

// FreeBytes is the room left for anything else, which is what an offer for this
// machine states.
func (ledger DiskLedger) FreeBytes() int64 {
	return max(ledger.CapacityBytes-ledger.ResidentBytes()-ledger.ReservedBytes, 0)
}

// holds reports whether this machine's account has room set aside for one item
// of content.
func (ledger DiskLedger) holds(kind ResidentKind, name string) bool {
	for _, item := range ledger.Resident {
		if item.Kind == kind && item.Name == name {
			return true
		}
	}
	return false
}

// ResidentContent is one item taking room on one machine, named by whatever
// makes it that content: a layer's blob digest, an Artifact version's ID, or a
// Cache Mount's identity.
type ResidentContent struct {
	Kind      ResidentKind `json:"kind"`
	Name      string       `json:"name"`
	SizeBytes int64        `json:"size_bytes"`
}

type ResidentKind string

const (
	ResidentLayer    ResidentKind = "layer"
	ResidentArtifact ResidentKind = "artifact"
	ResidentCache    ResidentKind = "cache"
)

type hostState struct {
	offer domain.OfferSnapshot
	// heldLayers is content on this machine, keyed by the compressed blob
	// digest a registry serves it under and carrying every other name the same
	// bytes have. What a host holds is the bytes; which name it can pronounce
	// is a property of its runtime.
	heldLayers map[string]scenario.LayerSpec
	heldImages map[string]bool
	// packed is content this host fetched and never unpacked, keyed by layer
	// blob digest and by image digest. It holds those bytes and cannot start a
	// container on them, so it can name no layer identity for them: a runtime
	// learns a layer's identity by unpacking it. Everything else it holds is
	// assembled, because a pull that lands unpacks as it goes.
	packed map[string]bool
	// reportsDiffIDs makes this host enumerate its layers the way a Docker
	// daemon does: uncompressed diff IDs, never compressed blob digests.
	reportsDiffIDs bool
	// leaseExpiresAt is when this machine's idle lease ends and the Rental stops
	// existing. Zero means the lease outlives the scenario.
	leaseExpiresAt time.Time
	// provisioning is what this world really takes to turn a listing into a
	// machine that can run a container, stage by stage. It is World Truth and
	// never the estimate the offer publishes, which is a claim the scheduler
	// predicts from, and it is kept per stage rather than as one total because
	// each stage has its own prediction to be measured against. Standing capacity
	// spends none of it, because the machine is already there.
	provisioning scenario.ProvisioningSpec
	// clockAhead is how far this machine's wall clock runs ahead of Mercator's. It
	// changes nothing about when anything here happens and everything about the
	// moment this machine states when asked: a host does not know its clock is
	// wrong, so it reports its container's start off the clock it has.
	clockAhead time.Duration
}

// missing is what launching this image here would still have to fetch, and how
// many bytes have to move to fetch it. A host that already holds the image
// whole moves nothing, which is the difference between a warm start and a cold
// one that the effect ledger has to be able to show.
func (state hostState) missing(image string, layers []scenario.LayerSpec) ([]scenario.LayerSpec, int64) {
	fetched := make([]scenario.LayerSpec, 0, len(layers))
	var bytes int64
	for _, layer := range layers {
		if _, held := state.heldLayers[layer.Digest]; held {
			continue
		}
		fetched = append(fetched, layer)
		bytes += int64(layer.Size)
	}
	return fetched, bytes
}

// owesAssembly reports whether this host is holding any of this image's content
// that it has not applied. A machine in that state owes the unpack stage without
// owing a byte of transfer, which is what makes the two stages different work.
func (state hostState) owesAssembly(image string, layers []scenario.LayerSpec) bool {
	if state.packed[domain.ReferenceDigest(image)] {
		return true
	}
	for _, layer := range layers {
		if state.packed[layer.Digest] {
			return true
		}
	}
	return false
}

// keep records content that finished arriving on this host: the layers the pull
// moved, and the image itself, under the name a machine reports holding it by.
func (state *hostState) keep(image string, layers []scenario.LayerSpec) {
	for _, layer := range layers {
		state.heldLayers[layer.Digest] = layer
		delete(state.packed, layer.Digest)
	}
	state.heldImages[domain.ReferenceDigest(image)] = true
	delete(state.packed, domain.ReferenceDigest(image))
}

// seededDigests is everything the World Tape put on this host before any Run
// executed. The provenance invariant reads it to tell content a scenario
// declared from content an execution left behind. A layer is seeded under every
// name it has, because a host that reports diff IDs would otherwise look like a
// host holding content nothing delivered.
func (state hostState) seededDigests() map[string]bool {
	digests := make(map[string]bool, 2*len(state.heldLayers)+len(state.heldImages))
	for _, layer := range state.heldLayers {
		for _, name := range layerNames(layer) {
			digests[name] = true
		}
	}
	for image := range state.heldImages {
		digests[image] = true
	}
	return digests
}

// inventory is what this host HOLDS, in the digest space its runtime can
// enumerate, and separating what it can start on from what it has only fetched.
// A Docker host names its layers by uncompressed diff ID and has no way to name
// the compressed blob a registry served it, so answering in both spaces would be
// the world lending a machine knowledge it does not have.
//
// This is World Truth and every machine answers it, including the ones nothing
// of Mercator's runs on. Whether Placement gets to read the answer is a separate
// question, decided once at publication: a world that suppressed it here would
// leave the laws that check what borrowed capacity accumulated with nothing to
// read, and they are the reason the distinction is worth drawing.
func (state hostState) inventory(at time.Time) domain.ImageInventory {
	inventory := domain.ImageInventory{Known: true, ObservedAt: at}
	for _, image := range slices.Sorted(maps.Keys(state.heldImages)) {
		if state.packed[image] {
			inventory.PulledImageDigests = append(inventory.PulledImageDigests, image)
			continue
		}
		inventory.ImageDigests = append(inventory.ImageDigests, image)
	}
	for _, digest := range slices.Sorted(maps.Keys(state.heldLayers)) {
		layer := state.heldLayers[digest]
		if state.packed[digest] {
			continue
		}
		if state.reportsDiffIDs {
			if layer.DiffID != "" {
				inventory.LayerDiffIDs = append(inventory.LayerDiffIDs, layer.DiffID)
			}
			continue
		}
		inventory.LayerDigests = append(inventory.LayerDigests, layer.Digest)
	}
	return inventory
}

// layerNames is every identity one layer answers to. The ledger records all of
// them, because a host that only speaks one digest space is still holding the
// bytes the other space names.
func layerNames(layer scenario.LayerSpec) []string {
	if layer.DiffID == "" {
		return []string{layer.Digest}
	}
	return []string{layer.Digest, layer.DiffID}
}

type worldOperation struct {
	hash          string
	correlationID string
	receipt       any
}

// pendingPull is image content still moving onto a host. Locality is a fact
// about bytes that have arrived, so a host holds nothing of an image until the
// pull that fetches it completes: the alternative is telling the next Run that
// a candidate starts instantly while the content is provably still in flight.
// The pull is named by the launch that asked for it, because an execution that
// is released or terminated mid-transfer leaves nothing behind.
type pendingPull struct {
	offerID string
	runID   string
	// launchKey names the thing waiting on these bytes: an execution for a pull
	// a launch dispatched, and the prefetch's own identity for one Mercator
	// asked for speculatively. It is what a cancellation is addressed by.
	launchKey string
	// source is why this content is moving: a launch that needs it now, or a
	// prefetch for a Run that has not been admitted. It travels onto the
	// retention the ledger records, because a host warmed by preparation and a
	// host warmed by execution are different facts about that machine.
	source  string
	image   string
	layers  []scenario.LayerSpec
	fetched []string
	// bytes is the room this pull has already claimed on the host. Content in
	// flight is not resident and its space is not free either.
	bytes       int64
	completesAt time.Time
}

// pendingReplica is Artifact content still moving from the object store onto a
// host. A copy exists when its bytes have landed and their digest matched, not
// when the launch that wanted it was accepted. Like a pull it is named by the
// launch, because an execution released mid-transfer leaves nothing behind.
type pendingReplica struct {
	offerID   string
	runID     string
	launchKey string
	// source is why these bytes are moving, exactly as it is on a pull.
	source     string
	artifactID string
	// bytes is the room this copy has already claimed on the host, for the same
	// reason a pull claims its own.
	bytes       int64
	completesAt time.Time
}

// pendingPublication is a producer's output still uploading to the object
// store. Until it lands, the Artifact is bytes on one machine, which is exactly
// what a consumer may not be admitted on. It survives the producing execution's
// release, because an upload in flight is not the machine's business.
type pendingPublication struct {
	artifactID  string
	runID       string
	completesAt time.Time
}

type simulatedWorld struct {
	mu sync.Mutex

	seed string
	now  time.Time
	// images is this world's registry: what each image contains, and what the
	// registry answers when asked. An image can exist here and still be
	// unreadable, which is the difference between what is running and what can
	// be looked up about it.
	images map[string]scenario.ImageSpec
	// truth is world state, keyed by offer. Mercator never reads it: everything
	// it learns about capacity arrives through the observed catalog below.
	truth map[string]hostState
	// observed is what the provider has published about its own capacity, and
	// the only thing the offer seam can answer with. An offer therefore states
	// the age of the answer rather than the instant of the read, and a world that
	// changed since the last publication is a stale observation a fixture can
	// write down. Feeding World Truth straight into adapter reads is the
	// alternative ADR 0004 rejects.
	observed   map[string]hostState
	observedAt time.Time
	// pulls is image content still moving onto a host.
	pulls []pendingPull
	runs  map[string]RunArrival
	// store is the durable authority for Artifacts. Nothing else answers
	// whether a consumer may run.
	store *objectStore
	// replicas is the local copy of each version each host holds, keyed by
	// version ID then offer. A copy makes a Run faster and never makes it
	// possible, which is why it is stated separately from the store.
	replicas map[string]map[string]domain.ArtifactReplica
	// replicating is Artifact content still landing on a host, and publishing
	// is producer output still reaching the object store.
	replicating []pendingReplica
	publishing  []pendingPublication
	// seededLocality is the image content each host held when the world was
	// built, keyed by offer. Everything outside it has to be explained by an
	// accepted image pull recorded against that same host.
	seededLocality map[string]map[string]bool
	// seededReplicas is the Artifact copies each host was declared holding
	// before any Run executed, keyed by offer. It is the Artifact half of the
	// same question: a copy outside it has to be explained by content the ledger
	// says landed on that host.
	seededReplicas map[string]map[string]bool
	// cacheMounts is the mutable state each host holds, keyed by offer and then
	// by cache identity. The identity carries the workspace, which is what makes
	// a cross-workspace leak a thing this world can be caught doing rather than
	// a thing it cannot express.
	cacheMounts map[string]map[string]CacheMountState

	// prewarm is what this world lets the control plane have in flight for work
	// it has not admitted. The world does not enforce it: a machine asked for
	// six transfers performs six, which is the whole reason the bound is a rule
	// about Mercator rather than a property of the capacity.
	prewarm *scenario.PrewarmSpec

	// publishedPaths is what every machine in this world has told Mercator about
	// the paths it crosses, kept by offer and never removed. A machine is retired
	// when its lease elapses and the decisions taken about it stay in the record, so
	// a rule about one of those decisions has to be able to read what the machine
	// published while it was here.
	publishedPaths map[string][]domain.NetworkFact

	// paths is what this world declared about the links its machines cross, read
	// for the world's own transfer model rather than off the offers Mercator sees.
	// How fast a path is and how much its publisher stands behind having measured
	// it are different statements: a host that disowns its own number has published
	// nothing to Mercator and still crosses the path at the speed the path is.
	paths scenario.PathSpecs

	// launch is what this world spends on the stages of a launch that happen
	// after its content has arrived: assembling that content, creating the
	// container, and the application reporting that it can do work. They are
	// stated durations rather than arithmetic over rates, because a world that
	// computed what the predictor computes would make every prediction right by
	// construction.
	launch scenario.LaunchSpec

	// prepared is every preparation identity this world has already taken on,
	// so a redelivered desired set changes nothing. It is never cleared: a
	// prefetch Mercator abandoned was abandoned because it stopped wanting the
	// content, and content it wants again arrives with the launch that needs it.
	prepared map[string]bool

	executions  map[string]externalExecution
	operations  map[string]worldOperation
	launchCount map[string]int
	faults      []scenario.FaultSpec
	usedFaults  map[string]bool

	effectSequence uint64
	effects        []EffectRecord
}

func newSimulatedWorld(tape WorldTape) (*simulatedWorld, error) {
	world := &simulatedWorld{
		seed:           tape.Seed,
		now:            tape.Start,
		images:         make(map[string]scenario.ImageSpec, len(tape.InitialWorld.Images)),
		truth:          map[string]hostState{},
		observed:       map[string]hostState{},
		runs:           map[string]RunArrival{},
		store:          newObjectStore(labWorkspace, tape.InitialWorld.Artifacts, tape.Start),
		replicas:       map[string]map[string]domain.ArtifactReplica{},
		seededLocality: map[string]map[string]bool{},
		seededReplicas: map[string]map[string]bool{},
		cacheMounts:    map[string]map[string]CacheMountState{},
		prewarm:        tape.InitialWorld.Prewarm,
		publishedPaths: map[string][]domain.NetworkFact{},
		paths:          slices.Clone(tape.InitialWorld.Paths),
		launch:         tape.InitialWorld.Launch,
		prepared:       map[string]bool{},
		executions:     map[string]externalExecution{},
		operations:     map[string]worldOperation{},
		launchCount:    map[string]int{},
		faults:         slices.Clone(tape.Faults),
		usedFaults:     map[string]bool{},
	}
	for reference, image := range tape.InitialWorld.Images {
		world.images[reference] = scenario.ImageSpec{Layers: slices.Clone(image.Layers), Registry: image.Registry}
	}
	for _, artifact := range tape.InitialWorld.Artifacts {
		world.replicas[artifact.ID] = map[string]domain.ArtifactReplica{}
	}
	for index, rental := range tape.InitialWorld.Rentals {
		state := hostState{
			offer: labOffer(
				rental.ID,
				domain.OfferKindStanding,
				domain.LaneReusable,
				// A Rental is a machine this world keeps, so it names one. The machine is
				// not the lease and not the listing: the fixture's ID is both of those,
				// and the handle the backend has for the machine behind them is minted
				// by the world exactly as a node ID is minted by the registry.
				labCandidate{provider: rental.Provider, region: rental.Region, machine: scenario.NodeHandle(index)},
				rental.RatePerHourUSD,
				rental.Resources,
			),
			heldLayers:     map[string]scenario.LayerSpec{},
			heldImages:     map[string]bool{},
			packed:         map[string]bool{},
			reportsDiffIDs: rental.ReportsDiffIDs,
			clockAhead:     rental.Skew(),
		}
		if rental.IdleLeaseExpiresIn != nil {
			state.leaseExpiresAt = tape.Start.Add(rental.IdleLeaseExpiresIn.Duration())
		}
		state.offer.Capacity.Confidence = rental.Confidence()
		// The term this capacity was sold on. It is a property of the sale rather
		// than of the moment an offer was read, so it is stated once here and
		// republished with every snapshot the provider takes.
		state.offer.Reclaimable = rental.Reclaimable
		if rental.Unpriced {
			// Nobody quoted this machine. That is a statement about the world, so it
			// is carried through as one rather than becoming a rate of zero, which
			// would be a machine somebody says is free.
			state.offer.Pricing = domain.PriceModel{Currency: "USD"}
			state.offer.Capabilities.Pricing = domain.PricingCapabilities{}
		}
		world.publishOfferFacts(&state.offer, tape.InitialWorld, rental.ID, nil, rental.Billing)
		for _, reference := range rental.CachedImages {
			for _, layer := range tape.InitialWorld.Images[reference].Layers {
				state.heldLayers[layer.Digest] = layer
				state.packed[layer.Digest] = !rental.IsUnpacked()
			}
			state.heldImages[domain.ReferenceDigest(reference)] = true
			state.packed[domain.ReferenceDigest(reference)] = !rental.IsUnpacked()
		}
		for _, digest := range rental.CachedLayers {
			state.heldLayers[digest] = findLayer(tape.InitialWorld, digest)
			state.packed[digest] = !rental.IsUnpacked()
		}
		world.seededLocality[rental.ID] = state.seededDigests()
		world.truth[rental.ID] = cloneHostState(state)
		world.seedReplicas(rental.ID, rental.ArtifactReplicas, tape.InitialWorld, tape.Start)
		world.seedCaches(rental.ID, rental.CacheMounts, tape.Start)
	}
	for index, host := range tape.InitialWorld.Hosts {
		state := hostState{
			offer: labOffer(
				host.ID,
				domain.OfferKindStanding,
				domain.LaneEphemeral,
				// A borrowed host holds nothing for Mercator and is the same machine next
				// time, which is the position an operator's own Docker daemon is in: it
				// names itself with the daemon's own ID rather than with the endpoint
				// Mercator reached it through.
				labCandidate{machine: scenario.DaemonHandle(index)},
				host.RatePerHourUSD,
				host.Resources,
			),
			heldLayers: map[string]scenario.LayerSpec{},
			heldImages: map[string]bool{},
			packed:     map[string]bool{},
		}
		for _, reference := range host.CachedImages {
			for _, layer := range tape.InitialWorld.Images[reference].Layers {
				state.heldLayers[layer.Digest] = layer
			}
			state.heldImages[domain.ReferenceDigest(reference)] = true
		}
		world.publishOfferFacts(&state.offer, tape.InitialWorld, host.ID, nil, host.Billing)
		world.seededLocality[host.ID] = state.seededDigests()
		world.truth[host.ID] = cloneHostState(state)
		world.seedReplicas(host.ID, host.ArtifactReplicas, tape.InitialWorld, tape.Start)
	}
	for _, marketplace := range tape.InitialWorld.Marketplace {
		state := hostState{
			offer: labOffer(
				marketplace.ID,
				domain.OfferKindProvisionable,
				marketplace.ExecutionLane(),
				// A listing names no machine, because the machine does not exist yet. What
				// recurs about it is the provider, the place, and the product name, which is
				// what the Blueprint states and what the identity falls back to.
				labCandidate{
					provider:     marketplace.Provider,
					region:       marketplace.Region,
					instanceType: marketplace.InstanceType,
				},
				marketplace.RatePerHourUSD,
				marketplace.Resources,
			),
			heldLayers: map[string]scenario.LayerSpec{},
			heldImages: map[string]bool{},
			// What this world spends turning the listing into a machine. It is read
			// from the stages the Blueprint states rather than from the estimate
			// published below, because a world that spent its provider's own
			// expectation would make that expectation right by construction.
			provisioning: marketplace.Provisioning,
		}
		world.publishOfferFacts(&state.offer, tape.InitialWorld, marketplace.ID, marketplace.Available, marketplace.Billing)
		world.seededLocality[marketplace.ID] = state.seededDigests()
		state.offer.Provisioning = &domain.Estimate{
			Expected: marketplace.Provisioning.Expected.Duration().Seconds(),
			Source:   "lab-world",
		}
		if marketplace.Provisioning.P90 != nil {
			state.offer.Provisioning.P90 = marketplace.Provisioning.P90.Duration().Seconds()
		}
		// The history this listing's provider publishes about the machine behind it.
		// It is a fact about the machine rather than about the moment an offer was
		// read, so it is stated once and republished with every snapshot, and a
		// listing no fixture measured publishes none of it.
		state.offer.Reliability = marketplace.Risk()
		world.truth[marketplace.ID] = cloneHostState(state)
	}
	if err := world.refuseOversubscribedDisks(); err != nil {
		return nil, err
	}
	world.publishObservations()
	return world, nil
}

// refuseOversubscribedDisks is this world declining to exist as described. A
// Blueprint that puts more content on a machine than the machine has disk is
// stating something no machine can be in, and letting it through would produce
// offers with negative room and a corpus that proves placements against a world
// that cannot happen.
func (world *simulatedWorld) refuseOversubscribedDisks() error {
	for _, ledger := range world.diskLedgers() {
		if resident := ledger.ResidentBytes(); resident > ledger.CapacityBytes {
			return fmt.Errorf(
				"Lab world machine %q was seeded with %d bytes of content and has %d bytes of disk",
				ledger.OfferID, resident, ledger.CapacityBytes,
			)
		}
	}
	return nil
}

// seedReplicas is what one machine was already holding when this world began.
// It is stated for any machine a Blueprint declares, whether Mercator controls
// it or borrows a slot on it, because a copy can genuinely be on either: what
// differs is whether an offer can say so, and that is decided at publication.
//
// The copy carries the digest the machine claims rather than the catalog's,
// which is how a fixture states a host whose local index still names this
// version after an operator restored an older snapshot underneath it.
func (world *simulatedWorld) seedReplicas(offerID string, held []scenario.ArtifactReplicaSpec, spec scenario.WorldSpec, at time.Time) {
	world.seededReplicas[offerID] = make(map[string]bool, len(held))
	for _, declared := range held {
		replica := world.store.replicaOf(declared.Artifact, at)
		replica.ContentDigest = declared.Digest(spec.Artifact(declared.Artifact))
		replica.State = declared.State
		if !replica.State.Usable() {
			replica.VerifiedAt = time.Time{}
		}
		world.replicas[declared.Artifact][offerID] = replica
		world.seededReplicas[offerID][declared.Artifact] = true
	}
}

// workspaceID is the Lab's own identity for one Blueprint workspace label. A
// Blueprint names tenants and this world names workspaces: keeping the two apart
// is what lets a fixture say "another tenant" without writing the Lab's naming
// into the public contract.
func workspaceID(label string) string {
	if label == "" {
		return labWorkspace
	}
	return labWorkspace + "_" + label
}

// seedCaches is the mutable state one machine was already holding when this
// world began, each entry under the workspace that owns it. A seeded cache
// starts at revision one: a fixture stating a cache is stating that some
// workload wrote it, whatever happened before this world's clock started.
func (world *simulatedWorld) seedCaches(offerID string, held []scenario.HeldCacheSpec, at time.Time) {
	world.cacheMounts[offerID] = make(map[string]CacheMountState, len(held))
	for _, declared := range held {
		state := CacheMountState{
			OfferID:          offerID,
			WorkspaceID:      workspaceID(declared.Workspace),
			Name:             declared.Name,
			CompatibilityKey: declared.CompatibilityKey,
			Revision:         1,
			CreatedAt:        at,
			SizeBytes:        int64(declared.Size),
		}
		state.Identity = domain.CacheIdentity(state.WorkspaceID, declared.Requirement())
		world.cacheMounts[offerID][state.Identity] = state
	}
}

// publishOfferFacts is one machine being handed what this world says about it,
// and this world writing down what it handed over. The publication outlives the
// machine on purpose: a placement is decided at one moment and judged at a later
// one, and a rule that asked only the machines still standing would read a
// correct decision about capacity since retired as a decision priced from
// nothing.
func (world *simulatedWorld) publishOfferFacts(offer *domain.OfferSnapshot, spec scenario.WorldSpec, offerID string, available *bool, billing scenario.BillingSpec) {
	if available != nil {
		offer.Capacity.Available = *available
	}
	offer.Pricing.SetupFeeUSD = billing.SetupFeeUSD
	if billing.MinimumCharge != nil {
		offer.Pricing.MinimumChargeSeconds = int64(billing.MinimumCharge.Duration().Seconds())
	}
	offer.Network = spec.Paths.PublishedFacts(offerID, spec.Start())
	world.publishedPaths[offerID] = slices.Clone(offer.Network.Download)
}

// prepareRun is the world learning about a Run it will be asked to execute. An
// arrival naming an image this world does not define is a broken fixture, and it
// is refused here rather than at the first read that happens to need it.
func (world *simulatedWorld) prepareRun(runID string, arrival RunArrival) error {
	world.mu.Lock()
	defer world.mu.Unlock()
	if _, defined := world.images[arrival.Request.Image]; !defined {
		return fmt.Errorf("Lab world image %q is not defined", arrival.Request.Image)
	}
	world.runs[runID] = arrival
	return nil
}

// ArtifactVersion is the object store answering what one version is and whether
// its bytes are here. It is the only durability answer Mercator gets, and it is
// deliberately blind to what any machine holds: an answer that counted replicas
// would make a Run admissible because some host happens to have bytes, and
// inadmissible the moment that host goes away, which is the
// distributed-filesystem model this architecture refuses.
//
// The catalog belongs to the workspace that declared it, which is the Blueprint's
// default one. Another tenant asking about one of those versions gets nothing,
// because an Artifact never crosses a workspace; a Blueprint that tried to state
// otherwise is refused when its arrival plan is validated.
func (world *simulatedWorld) ArtifactVersion(_ context.Context, workspaceID, artifactID string) (domain.ArtifactVersion, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	if workspaceID != labWorkspace {
		return domain.ArtifactVersion{}, nil
	}
	version, _ := world.store.entry(artifactID)
	return version, nil
}

// setNow moves virtual time through everything this world scheduled between
// here and there, settling each deadline at its own instant, and publishes what
// the provider can see once it arrives. Content lands, containers exit, uploads
// complete and leases elapse when the world's own model says they do, so how
// often Mercator looks changes what it has seen and never changes what happened.
func (world *simulatedWorld) setNow(now time.Time) {
	world.mu.Lock()
	defer world.mu.Unlock()
	target := now.UTC()
	world.settleDeadlines()
	for {
		deadline, scheduled := world.nextDeadline(target)
		if !scheduled {
			break
		}
		world.now = deadline
		world.settleDeadlines()
	}
	world.now = target
	world.settleDeadlines()
	world.publishObservations()
}

// nextDeadline is the earliest moment after now and at or before target at
// which this world still owes something it started: a container exiting,
// content landing, an upload reaching the object store, or an idle lease
// elapsing. Every answer is strictly later than the one before it, which is
// what makes settling them a walk forward rather than a loop.
func (world *simulatedWorld) nextDeadline(target time.Time) (time.Time, bool) {
	var earliest time.Time
	consider := func(at time.Time) {
		if !at.After(world.now) || at.After(target) || !earliest.IsZero() && !at.Before(earliest) {
			return
		}
		earliest = at
	}
	for _, execution := range world.executions {
		if !execution.CachesAttached {
			consider(execution.StartedAt)
		}
		if !execution.OutputsStored {
			consider(execution.CompletesAt)
		}
	}
	for _, pull := range world.pulls {
		consider(pull.completesAt)
	}
	for _, fetch := range world.replicating {
		consider(fetch.completesAt)
	}
	for _, upload := range world.publishing {
		consider(upload.completesAt)
	}
	for _, state := range world.truth {
		consider(state.leaseExpiresAt)
	}
	return earliest, !earliest.IsZero()
}

// settleDeadlines lets everything due at the current instant happen, in the
// order one thing can cause another: a container is created and opens its
// caches, it exits and writes its output, content lands, an upload becomes
// durable, and capacity nobody is using expires.
func (world *simulatedWorld) settleDeadlines() {
	world.settleCacheAttachments()
	world.settleExecutions()
	world.settlePulls()
	world.settleReplicas()
	world.settlePublications()
	world.retireExpiredCapacity()
}

// settleExecutions is every container that has exited leaving behind what it
// computed. It runs on the world's clock rather than on an observation, because
// when a process wrote its output is a fact about the process: a control plane
// that polled less often would otherwise move the moment an Artifact was
// written, and with it the moment every consumer of that Artifact could start.
func (world *simulatedWorld) settleExecutions() {
	for _, launchKey := range slices.Sorted(maps.Keys(world.executions)) {
		execution := world.executions[launchKey]
		if execution.OutputsStored || world.now.Before(execution.CompletesAt) {
			continue
		}
		if world.roomForOutputs(execution) {
			world.storeRunOutputs(execution, execution.CompletesAt)
		} else {
			execution.Phase = adapter.ExternalPhaseFailed
		}
		execution.OutputsStored = true
		world.executions[launchKey] = execution
	}
}

// publishObservations is the provider taking a fresh look at its own capacity.
// Everything Mercator reads about an offer comes from the last publication. The
// provider publishes when virtual time advances and after every command it
// carries out itself, because it knows what it did to its own machines; what a
// scenario can leave unpublished is what the world did behind its back.
func (world *simulatedWorld) publishObservations() {
	observed := make(map[string]hostState, len(world.truth))
	for id, state := range world.truth {
		observed[id] = cloneHostState(state)
	}
	world.observed = observed
	world.observedAt = world.now
}

// settlePulls puts the content of every completed pull on the host that fetched
// it and records the retention that explains it. A pull still in flight leaves
// the host exactly as warm as it was, and records nothing.
func (world *simulatedWorld) settlePulls() {
	remaining := world.pulls[:0]
	for _, pull := range world.pulls {
		if world.now.Before(pull.completesAt) {
			remaining = append(remaining, pull)
			continue
		}
		state := world.truth[pull.offerID]
		state.keep(pull.image, pull.layers)
		world.truth[pull.offerID] = state
		world.recordEffect(
			OperationImageRetained,
			"image-retained/"+pull.launchKey+"/"+pull.image,
			EffectCommandAccepted,
			EffectResponseDelivered,
			pull.runID,
			pull.launchKey,
			"",
			map[string]any{"image": pull.image, "offer_id": pull.offerID, "source": pull.source},
			map[string]any{"retained_digests": pull.fetched},
			"",
		)
	}
	world.pulls = remaining
}

// settleReplicas puts every Artifact copy whose bytes have landed on the host
// that fetched it, verified against the catalog because that is what the fetch
// checked on arrival. A transfer still in flight leaves the host holding what it
// held.
func (world *simulatedWorld) settleReplicas() {
	remaining := world.replicating[:0]
	for _, fetch := range world.replicating {
		if world.now.Before(fetch.completesAt) {
			remaining = append(remaining, fetch)
			continue
		}
		world.keepReplica(fetch.artifactID, fetch.offerID, fetch.runID, fetch.launchKey, fetch.source, fetch.completesAt)
	}
	world.replicating = remaining
}

// settlePublications makes durable every producer output whose upload finished.
// This is the moment an Artifact starts existing for anyone but the machine that
// wrote it, and therefore the moment its consumers may be admitted.
func (world *simulatedWorld) settlePublications() {
	remaining := world.publishing[:0]
	for _, upload := range world.publishing {
		if world.now.Before(upload.completesAt) {
			remaining = append(remaining, upload)
			continue
		}
		version := world.store.publish(upload.artifactID, upload.runID, upload.completesAt)
		world.recordEffect(
			OperationArtifactPublished,
			"artifact-published/"+upload.artifactID,
			EffectCommandAccepted,
			EffectResponseDelivered,
			upload.runID,
			"publish",
			"",
			map[string]any{"artifact_id": version.ID, "workspace_id": version.WorkspaceID},
			map[string]any{
				"location":       version.Location,
				"content_digest": version.ContentDigest,
				"size_bytes":     version.SizeBytes,
			},
			"",
		)
	}
	world.publishing = remaining
}

// keepReplica records a verified local copy landing on one host, when it landed
// and where it came from. Both origins produce the same fact about the machine,
// which is why they produce one effect: the ledger says which it was.
func (world *simulatedWorld) keepReplica(artifactID, offerID, runID, launchKey, source string, at time.Time) {
	// Capacity that keeps nothing keeps no Artifact copy either, for the same
	// two reasons it keeps no image: a provisionable offer is a machine that
	// does not exist yet, and a one-shot product is gone once its workload
	// exits. A copy written there would be locality outliving its own host.
	if world.replicas[artifactID] == nil || !world.truth[offerID].offer.KeepsWhatItRuns() {
		return
	}
	replica := world.store.replicaOf(artifactID, at)
	world.replicas[artifactID][offerID] = replica
	world.recordEffect(
		OperationArtifactReplicated,
		"artifact-replicated/"+launchKey+"/"+artifactID,
		EffectCommandAccepted,
		EffectResponseDelivered,
		runID,
		launchKey,
		"",
		map[string]any{"artifact_id": artifactID, "offer_id": offerID, "source": source},
		map[string]any{
			"state":          replica.State,
			"content_digest": replica.ContentDigest,
			"size_bytes":     replica.SizeBytes,
		},
		"",
	)
}

// retireExpiredCapacity removes machines whose idle lease elapsed. A Rental that
// is gone takes everything local with it: its image content and its Artifact
// copies were on that disk, and a copy is exactly what does not survive the
// machine. What survives is what the object store holds, which is the point.
func (world *simulatedWorld) retireExpiredCapacity() {
	for id, state := range world.truth {
		if state.leaseExpiresAt.IsZero() || world.now.Before(state.leaseExpiresAt) || world.busy(id) {
			continue
		}
		world.retireCapacity(id)
	}
}

// preemptCapacity is the provider taking a machine back. It is the same removal an
// elapsed lease performs and it is not the same act: a lease waits for the machine
// to be idle, and this does not, which is the whole of what reclaimable capacity
// means. Whatever was running here stops running here, and the ledger says so
// before the machine is gone, because after it there is nothing left to name.
func (world *simulatedWorld) preemptCapacity(rentalID string) error {
	world.mu.Lock()
	defer world.mu.Unlock()
	if _, exists := world.truth[rentalID]; !exists {
		return fmt.Errorf("Lab world cannot preempt %q, which is not capacity it holds", rentalID)
	}
	interrupted := []map[string]any{}
	for _, launchKey := range slices.Sorted(maps.Keys(world.executions)) {
		execution := world.executions[launchKey]
		if execution.OfferID != rentalID {
			continue
		}
		interrupted = append(interrupted, map[string]any{
			"run_id":       execution.RunID,
			"launch_key":   execution.LaunchKey,
			"workspace_id": execution.WorkspaceID,
			// Whether the process had begun at all. A launch reclaimed before its
			// container started lost the machine and no work, which is a different
			// fact about the same event and one nothing else in the record could
			// recover once the execution is gone.
			"started": !execution.StartedAt.IsZero() && !world.now.Before(execution.StartedAt),
		})
		delete(world.executions, launchKey)
		world.cancelTransfers(launchKey)
	}
	world.recordEffect(
		OperationCapacityPreempted,
		"capacity-preempted/"+rentalID,
		EffectCommandAccepted,
		EffectResponseDelivered,
		rentalID,
		"capacity-preempted/"+rentalID,
		"",
		map[string]any{"offer_id": rentalID},
		map[string]any{"interrupted": interrupted},
		"",
	)
	world.retireCapacity(rentalID)
	world.publishObservations()
	return nil
}

// retireCapacity is a machine ceasing to exist, whichever of the two reasons took
// it. Everything local goes with it: the content on its disk and the Artifact
// copies on it were on that disk, and a copy is exactly what does not survive the
// machine. What survives is what the object store holds, which is the point.
func (world *simulatedWorld) retireCapacity(id string) {
	delete(world.truth, id)
	delete(world.seededLocality, id)
	delete(world.seededReplicas, id)
	delete(world.cacheMounts, id)
	for _, hosts := range world.replicas {
		delete(hosts, id)
	}
}

func (world *simulatedWorld) busy(offerID string) bool {
	for _, execution := range world.executions {
		if execution.OfferID == offerID {
			return true
		}
	}
	return false
}

// cancelTransfers drops content that was still moving onto a host when the
// execution that asked for it was released or terminated. This world moves
// content whole or not at all, so a transfer nothing is waiting on leaves
// nothing behind. An upload to the object store is not cancelled with it: the
// bytes left the machine, and durability is not the machine's business.
func (world *simulatedWorld) cancelTransfers(launchKey string) {
	world.pulls = slices.DeleteFunc(world.pulls, func(pull pendingPull) bool {
		return pull.launchKey == launchKey
	})
	world.replicating = slices.DeleteFunc(world.replicating, func(fetch pendingReplica) bool {
		return fetch.launchKey == launchKey
	})
}

// executionHorizon is the latest moment the world still owes something it has
// already started: a running execution its completion, or a producer's output
// its durability. Both can admit a Run that is waiting, so a driver that
// advanced only to the last execution would stop with a consumer parked behind
// an upload that was always going to land.
func (world *simulatedWorld) executionHorizon() time.Time {
	world.mu.Lock()
	defer world.mu.Unlock()
	var horizon time.Time
	for _, execution := range world.executions {
		if execution.CompletesAt.After(horizon) {
			horizon = execution.CompletesAt
		}
	}
	for _, upload := range world.publishing {
		if upload.completesAt.After(horizon) {
			horizon = upload.completesAt
		}
	}
	return horizon
}

func (world *simulatedWorld) nowTime() time.Time {
	world.mu.Lock()
	defer world.mu.Unlock()
	return world.now
}

// setOfferAvailable is the world changing its mind about capacity, which a
// scenario does to model a provider reclaiming a machine.
func (world *simulatedWorld) setOfferAvailable(id string, available bool) {
	world.mu.Lock()
	defer world.mu.Unlock()
	state := world.truth[id]
	// Only whether the capacity is there changes. How sure its publisher is of
	// that answer is a property of the publisher, so a world that reclaims a
	// machine does not also become certain about one it was unsure of.
	state.offer.Capacity.Available = available
	world.truth[id] = state
}

func (world *simulatedWorld) truthSnapshot() WorldTruthSnapshot {
	world.mu.Lock()
	defer world.mu.Unlock()
	executions := make([]externalExecution, 0, len(world.executions))
	for _, execution := range world.executions {
		executions = append(executions, execution)
	}
	sort.Slice(executions, func(i, j int) bool {
		return executions[i].LaunchKey < executions[j].LaunchKey
	})
	return WorldTruthSnapshot{
		At:               world.now,
		Offers:           world.offerSnapshots(world.truth, world.now),
		ActiveExecutions: executions,
		ArtifactReplicas: world.artifactReplicas(),
		CacheMounts:      world.cacheMountStates(),
		PublishedPaths:   maps.Clone(world.publishedPaths),
		Disk:             world.diskLedgers(),
	}
}

func (world *simulatedWorld) effectRecords() []EffectRecord {
	world.mu.Lock()
	defer world.mu.Unlock()
	return cloneEffects(world.effects)
}

// worldFacts is what the world knows that the event log cannot say: the Runs it
// was asked to execute, and what it was seeded with before any of them ran.
type worldFacts struct {
	Runs map[string]RunArrival
	// ArtifactCatalog is what the object store says each version is and when it
	// became durable. It is what every replica is checked against, and what
	// tells a copy of known content from a copy of a name nobody defined.
	ArtifactCatalog map[string]domain.ArtifactVersion
	SeededLocality  map[string]map[string]bool
	SeededReplicas  map[string]map[string]bool
	// Prewarm is the bound this world states on speculative preparation.
	Prewarm *scenario.PrewarmSpec
}

func (world *simulatedWorld) invariantFacts() worldFacts {
	world.mu.Lock()
	defer world.mu.Unlock()
	facts := worldFacts{
		Runs:            cloneMap(world.runs),
		ArtifactCatalog: world.store.versions(),
		Prewarm:         world.prewarm,
		SeededLocality:  make(map[string]map[string]bool, len(world.seededLocality)),
		SeededReplicas:  make(map[string]map[string]bool, len(world.seededReplicas)),
	}
	for offerID, digests := range world.seededLocality {
		facts.SeededLocality[offerID] = cloneMap(digests)
	}
	for offerID, artifacts := range world.seededReplicas {
		facts.SeededReplicas[offerID] = cloneMap(artifacts)
	}
	return facts
}

func (world *simulatedWorld) recordControlPlaneRestart(ordinal uint64) {
	world.mu.Lock()
	defer world.mu.Unlock()
	world.recordEffect(
		OperationControlPlaneRestart,
		fmt.Sprintf("control-plane-restart/%d", ordinal),
		EffectCommandAccepted,
		EffectResponseDelivered,
		labWorkspace,
		"restart",
		"",
		map[string]any{"ordinal": ordinal},
		map[string]any{"external_resources_preserved": len(world.executions)},
		"",
	)
}

// observeOffers reads the offers a client can see without recording an effect.
// ListOffers is the control plane's placement read and belongs in the ledger;
// an operator refreshing a page does not.
func (world *simulatedWorld) observeOffers() []domain.OfferSnapshot {
	world.mu.Lock()
	defer world.mu.Unlock()
	return world.publishedOffers()
}

// ListOffers is the provider quoting its own capacity. It is told a workspace
// and a shape, and never which Run is being placed: a provider has no Run
// identity to answer with, and a world that resolved one would be reading
// Mercator's mind about a decision it has not made yet.
func (world *simulatedWorld) ListOffers(_ context.Context, request adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	offers := world.publishedOffers()
	// A marketplace listing is a search result, so this world answers the shape it
	// was asked about and capacity Mercator holds is listed whole. Returning the
	// whole inventory whatever was asked meant no world here could answer one ask
	// with nothing while publishing machines for another, which is the case that
	// empties a workspace. See domain.OfferSnapshot.PublishedTo.
	offers = slices.DeleteFunc(offers, func(offer domain.OfferSnapshot) bool {
		return !offer.PublishedTo(request.Resources)
	})
	world.recordEffect(
		OperationProviderListOffers,
		"list-offers/"+request.WorkspaceID,
		EffectCommandAccepted,
		EffectResponseDelivered,
		request.WorkspaceID,
		"placement",
		"",
		map[string]any{"workspace_id": request.WorkspaceID},
		map[string]any{"offer_ids": offerIDs(offers)},
		"",
	)
	return offers, nil
}

// CollectOffers is this world answering as the whole fleet: the offers it
// publishes and the one connection it is. The census is stated rather than read
// off the offers, because a world that published nothing would otherwise record
// that nobody was asked, and admission reads those as different facts.
func (world *simulatedWorld) CollectOffers(ctx context.Context, request adapter.OfferRequest) (adapter.OfferCollection, error) {
	offers, err := world.ListOffers(ctx, request)
	if err != nil {
		return adapter.OfferCollection{}, err
	}
	return adapter.OfferCollection{Offers: offers, Queried: []string{labConnection}}, nil
}

func (world *simulatedWorld) Launch(_ context.Context, request adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	if request.OperationKey == "" || request.RequestHash == "" {
		return adapter.LaunchReceipt{}, fmt.Errorf("Lab provider launch needs operation key and request hash")
	}
	if operation, exists := world.operations[request.OperationKey]; exists {
		if operation.hash != request.RequestHash {
			world.recordLaunchEffect(request, EffectCommandRejected, EffectResponseDelivered, nil, "")
			return adapter.LaunchReceipt{}, adapter.ErrIdempotencyConflict
		}
		receipt := operation.receipt.(adapter.LaunchReceipt)
		receipt.Duplicate = true
		world.recordLaunchEffect(request, EffectCommandDuplicate, EffectResponseDelivered, receipt, "")
		return receipt, nil
	}
	world.launchCount[request.RunID]++
	fault := world.matchOperationFault(
		OperationProviderLaunch,
		request.RunID,
		world.launchCount[request.RunID],
	)
	if fault != nil && fault.Action == scenario.FaultRejectCommand {
		world.recordLaunchEffect(request, EffectCommandRejected, EffectResponseDelivered, nil, fault.ID)
		return adapter.LaunchReceipt{}, &adapter.ProviderFailure{
			Kind:       adapter.ProviderFailureCapacityUnavailable,
			Retryable:  true,
			SideEffect: adapter.SideEffectNone,
		}
	}
	offer, exists := world.truth[request.SelectedOfferSnapshotID]
	if !exists || !offer.offer.Capacity.Available {
		world.recordLaunchEffect(request, EffectCommandRejected, EffectResponseDelivered, nil, "")
		return adapter.LaunchReceipt{}, &adapter.ProviderFailure{
			Kind:       adapter.ProviderFailureCapacityUnavailable,
			Retryable:  true,
			SideEffect: adapter.SideEffectNone,
		}
	}
	arrival, exists := world.runs[request.RunID]
	if !exists {
		world.recordLaunchEffect(request, EffectCommandRejected, EffectResponseDelivered, nil, "")
		return adapter.LaunchReceipt{}, &adapter.ProviderFailure{
			Kind:       adapter.ProviderFailureInvalidRequest,
			SideEffect: adapter.SideEffectNone,
		}
	}
	// A machine with nowhere to put this refuses it rather than filling up
	// partway through, and says so the way it says anything else it cannot take
	// right now: the disk it is short of may be free again once something else
	// here finishes.
	if !world.launchFitsOnDisk(request, arrival) {
		world.recordLaunchEffect(request, EffectCommandRejected, EffectResponseDelivered, nil, "")
		return adapter.LaunchReceipt{}, &adapter.ProviderFailure{
			Kind:       adapter.ProviderFailureCapacityUnavailable,
			Retryable:  true,
			SideEffect: adapter.SideEffectNone,
		}
	}
	execution := externalExecution{
		ExternalID:        "lab-" + request.AttemptID,
		RunID:             request.RunID,
		AttemptID:         request.AttemptID,
		LaunchKey:         request.LaunchKey,
		OwnershipToken:    request.OwnershipToken,
		RequestHash:       request.RequestHash,
		OfferID:           request.SelectedOfferSnapshotID,
		WorkspaceID:       request.WorkspaceID,
		CacheMounts:       slices.Clone(request.CacheMounts),
		Disposition:       request.Disposition,
		Phase:             adapter.ExternalPhaseRunning,
		AcceptedAt:        world.now,
		ReadyAt:           world.now.Add(offer.provisioning.Spend()),
		ReservedDiskBytes: request.Resources.EphemeralDisk.MinBytes,
	}
	if offer.offer.Kind == domain.OfferKindStanding {
		offer.offer.Capacity.Available = false
		world.truth[request.SelectedOfferSnapshotID] = offer
	}
	// A process cannot execute bytes that have not landed, and that is as true
	// of the Artifacts it reads as of the image it runs. Neither can be fetched
	// before there is a machine to fetch them onto, so both transfers start when
	// the world has finished acquiring, booting, and enrolling this host.
	execution.ImageAt = world.pullRunImage(execution, request.Image, execution.ReadyAt)
	execution.ArtifactsAt = world.readRunArtifacts(execution, arrival.Request.ConsumesArtifacts, execution.ReadyAt)
	// Bytes on a disk are not a layer chain, and a container runtime asked for a
	// process does not hand one back instantly. Both stages cost time here, so a
	// prediction of either has an actual to be measured against.
	execution.AssembledAt = later(execution.ImageAt, execution.ArtifactsAt).
		Add(world.unpackSpend(execution.OfferID, request.Image))
	execution.StartedAt = execution.AssembledAt.Add(world.launch.ContainerStartSpend())
	// A world whose applications never come up leaves this absent rather than equal
	// to the start, which is the difference between a process that is serving the
	// instant it exists and one that never serves at all.
	if world.launch.ApplicationBecomesReady() {
		execution.ApplicationReadyAt = execution.StartedAt.Add(world.launch.ApplicationReadySpend())
	}
	execution.CompletesAt = execution.StartedAt.Add(actualRuntimeForOffer(arrival, request.SelectedOfferSnapshotID))
	world.executions[request.LaunchKey] = execution
	// The caches this launch declared are opened with the container, which is at
	// StartedAt and may be now.
	world.settleCacheAttachments()
	world.publishObservations()
	receipt := adapter.LaunchReceipt{
		ExternalID:     execution.ExternalID,
		LaunchKey:      execution.LaunchKey,
		OwnershipToken: execution.OwnershipToken,
		CleanupLocator: request.CleanupLocator,
		Phase:          execution.Phase,
		AcceptedAt:     world.now,
	}
	world.operations[request.OperationKey] = worldOperation{
		hash:          request.RequestHash,
		correlationID: request.RunID,
		receipt:       receipt,
	}
	if fault != nil && fault.Action == scenario.FaultLoseResponse {
		world.recordLaunchEffect(request, EffectCommandAccepted, EffectResponseLost, receipt, fault.ID)
		return adapter.LaunchReceipt{}, adapter.ErrLaunchIndeterminate
	}
	if fault != nil && fault.Action == scenario.FaultDelayResponse {
		world.recordLaunchEffect(request, EffectCommandAccepted, EffectResponseDelayed, receipt, fault.ID)
		return adapter.LaunchReceipt{}, adapter.ErrLaunchIndeterminate
	}
	world.recordLaunchEffect(request, EffectCommandAccepted, EffectResponseDelivered, receipt, "")
	if fault != nil && fault.Action == scenario.FaultDuplicateResponse {
		world.recordLaunchEffect(request, EffectCommandAccepted, EffectResponseDuplicate, receipt, fault.ID)
	}
	return receipt, nil
}

// unpackSpend is what this launch spends turning content on the disk into a
// layer chain a container can start on. A machine with nothing to apply spends
// nothing, which is what tells a warm host from one that has just fetched
// eighteen gigabytes: unpacking is work over bytes, and a host holding the image
// assembled has none of it to do.
// ReadinessReport is one workload telling Mercator it can do work. It is the
// application's own callback rather than anything the machine holding it says: a
// provider, a node, and a container runtime can all see a process running, and
// none of them can see whether it is serving.
type ReadinessReport struct {
	WorkspaceID string
	RunID       string
	// ReadyAt is the application's own moment. It travels in the report because a
	// readiness stamped when Mercator got round to recording it would move with the
	// control plane's polling cadence, which is the defect the observed start
	// moment was fixed for one stage over.
	ReadyAt time.Time
}

// dueReadinessReports is every workload here that has become ready and has not
// said so yet. A workload reports once, so the world remembers which have
// spoken: a report appended on every look would say the same thing forever.
func (world *simulatedWorld) dueReadinessReports() []ReadinessReport {
	world.mu.Lock()
	defer world.mu.Unlock()
	var due []ReadinessReport
	for _, launchKey := range slices.Sorted(maps.Keys(world.executions)) {
		execution := world.executions[launchKey]
		if execution.ReadinessReported ||
			execution.ApplicationReadyAt.IsZero() ||
			world.now.Before(execution.ApplicationReadyAt) {
			continue
		}
		execution.ReadinessReported = true
		world.executions[launchKey] = execution
		due = append(due, ReadinessReport{
			WorkspaceID: execution.WorkspaceID,
			RunID:       execution.RunID,
			// The application reads the clock of the host it runs on, so the moment
			// it states is world truth read on that clock. It is the same clock for
			// every machine in this corpus but one, and the exception is the point: a
			// workload on a host running ahead publishes a readiness Mercator has not
			// reached, which Mercator has to refuse rather than file as an hour of
			// ready latency nothing measured.
			ReadyAt: execution.ApplicationReadyAt.Add(world.truth[execution.OfferID].clockAhead),
		})
	}
	return due
}

func (world *simulatedWorld) unpackSpend(offerID, image string) time.Duration {
	state := world.truth[offerID]
	layers := world.images[image].Layers
	if _, fetched := state.missing(image, layers); fetched == 0 && !state.owesAssembly(image, layers) {
		return 0
	}
	return world.launch.UnpackSpend()
}

func actualRuntimeForOffer(arrival RunArrival, offerID string) time.Duration {
	if runtime := arrival.ActualRuntimeByOffer[offerID]; runtime.Duration() > 0 {
		return runtime.Duration()
	}
	return arrival.ActualRuntime.Duration()
}

func (world *simulatedWorld) Observe(_ context.Context, request adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	execution, exists := world.executions[request.LaunchKey]
	if !exists {
		observation := adapter.ExternalObservation{
			LaunchKey:  request.LaunchKey,
			Phase:      adapter.ExternalPhaseReleased,
			ObservedAt: world.now,
		}
		world.recordObservationEffect(request, EffectCommandAccepted, observation)
		return observation, nil
	}
	if request.OwnershipToken != "" && request.OwnershipToken != execution.OwnershipToken ||
		request.RequestHash != "" && request.RequestHash != execution.RequestHash {
		world.recordObservationEffect(request, EffectCommandRejected, nil)
		return adapter.ExternalObservation{}, adapter.ErrIdempotencyConflict
	}
	if !world.now.Before(execution.CompletesAt) && execution.Phase == adapter.ExternalPhaseRunning {
		execution.Phase = adapter.ExternalPhaseSucceeded
		world.executions[request.LaunchKey] = execution
	}
	observation := adapter.ExternalObservation{
		ExternalID: execution.ExternalID,
		LaunchKey:  execution.LaunchKey,
		Phase:      execution.Phase,
		ObservedAt: world.now,
		NativeJSON: fmt.Sprintf(`{"adapter":"lab","external_id":%q}`, execution.ExternalID),
	}
	// This provider says running from the moment it accepts a launch, which is what
	// every provider in the tree does and why Mercator cannot learn a start from a
	// phase. What it can say is when the container actually began, and only once it
	// has: a moment that has not arrived is reported as the absence it is rather
	// than as the moment the launch was taken.
	//
	// The moment is stated on the machine's own clock. That is the same clock for
	// every host in this corpus but one, and the exception is the whole point: a
	// machine running ahead publishes a start that has arrived here and not there,
	// which Mercator has to refuse rather than adopt as a fact about its own future.
	if !world.now.Before(execution.StartedAt) {
		startedAt := execution.StartedAt.Add(world.truth[execution.OfferID].clockAhead)
		observation.StartedAt = &startedAt
	}
	if observation.Phase == adapter.ExternalPhaseSucceeded {
		exitCode := 0
		observation.ExitCode = &exitCode
	}
	if observation.Phase == adapter.ExternalPhaseFailed {
		exitCode := 1
		observation.ExitCode = &exitCode
	}
	world.recordObservationEffect(request, EffectCommandAccepted, observation)
	return observation, nil
}

func (world *simulatedWorld) Release(_ context.Context, request adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	receipt, duplicate, err := world.cleanup(
		OperationProviderRelease,
		request.OperationKey,
		request.RequestHash,
		request.LaunchKey,
		request.OwnershipToken,
		request.LaunchRequestHash,
	)
	if err != nil {
		return adapter.ReleaseReceipt{}, err
	}
	return adapter.ReleaseReceipt{Released: true, Duplicate: duplicate || receipt.Duplicate}, nil
}

func (world *simulatedWorld) Terminate(_ context.Context, request adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	receipt, duplicate, err := world.cleanup(
		OperationProviderTerminate,
		request.OperationKey,
		request.RequestHash,
		request.LaunchKey,
		request.OwnershipToken,
		request.LaunchRequestHash,
	)
	if err != nil {
		return adapter.TerminateReceipt{}, err
	}
	return adapter.TerminateReceipt{Terminated: true, Duplicate: duplicate || receipt.Duplicate}, nil
}

func (world *simulatedWorld) ListOwned(_ context.Context, request adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	objects := make([]adapter.OwnedExternalObject, 0, len(world.executions))
	for _, execution := range world.executions {
		// An owned-execution query is one tenant asking what of its own is still
		// out there. Answering with another workspace's executions would make one
		// tenant's work look like the asker's orphans.
		if request.WorkspaceID != "" && request.WorkspaceID != execution.WorkspaceID {
			continue
		}
		objects = append(objects, adapter.OwnedExternalObject{
			ExternalID:     execution.ExternalID,
			WorkspaceID:    execution.WorkspaceID,
			ConnectionID:   labConnection,
			RunID:          execution.RunID,
			AttemptID:      execution.AttemptID,
			OwnershipToken: execution.OwnershipToken,
			LaunchKey:      execution.LaunchKey,
			RequestHash:    execution.RequestHash,
			Phase:          execution.Phase,
		})
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].LaunchKey < objects[j].LaunchKey })
	world.recordEffect(
		OperationProviderListOwned,
		"list-owned/"+request.WorkspaceID,
		EffectCommandAccepted,
		EffectResponseDelivered,
		request.WorkspaceID,
		"reconcile-owned",
		"",
		map[string]any{"workspace_id": request.WorkspaceID},
		map[string]any{"launch_keys": ownedLaunchKeys(objects)},
		"",
	)
	return objects, nil
}

func (world *simulatedWorld) cleanup(operation, operationKey, requestHash, launchKey, ownershipToken, launchHash string) (adapter.ReleaseReceipt, bool, error) {
	if operationKey == "" || requestHash == "" {
		return adapter.ReleaseReceipt{}, false, fmt.Errorf("Lab provider cleanup needs operation key and request hash")
	}
	if existing, ok := world.operations[operationKey]; ok {
		if existing.hash != requestHash {
			world.recordCleanupEffect(operation, operationKey, requestHash, launchKey, existing.correlationID, EffectCommandRejected)
			return adapter.ReleaseReceipt{}, false, adapter.ErrIdempotencyConflict
		}
		world.recordCleanupEffect(operation, operationKey, requestHash, launchKey, existing.correlationID, EffectCommandDuplicate)
		return adapter.ReleaseReceipt{Released: true, Duplicate: true}, true, nil
	}
	execution, exists := world.executions[launchKey]
	if exists && (ownershipToken != "" && ownershipToken != execution.OwnershipToken ||
		launchHash != "" && launchHash != execution.RequestHash) {
		world.recordCleanupEffect(operation, operationKey, requestHash, launchKey, execution.RunID, EffectCommandRejected)
		return adapter.ReleaseReceipt{}, false, adapter.ErrIdempotencyConflict
	}
	if exists {
		delete(world.executions, launchKey)
		world.cancelTransfers(launchKey)
		if offer := world.truth[execution.OfferID]; offer.offer.Kind == domain.OfferKindStanding {
			offer.offer.Capacity.Available = true
			world.truth[execution.OfferID] = offer
		}
		world.publishObservations()
	}
	receipt := adapter.ReleaseReceipt{Released: true}
	world.operations[operationKey] = worldOperation{
		hash:          requestHash,
		correlationID: execution.RunID,
		receipt:       receipt,
	}
	world.recordCleanupEffect(operation, operationKey, requestHash, launchKey, execution.RunID, EffectCommandAccepted)
	return receipt, false, nil
}

// offerSnapshots projects host state into the only vocabulary Mercator gets to
// read, as of the moment that state was observed. Each host states everything it
// holds, whatever Run is being placed, because what a Run would still have to
// fetch is the scheduler's subtraction against the manifest and not an answer
// this world asserts about an image the offer does not name.
func (world *simulatedWorld) offerSnapshots(source map[string]hostState, at time.Time) []domain.OfferSnapshot {
	offers := make([]domain.OfferSnapshot, 0, len(source))
	for _, state := range source {
		offer := state.offer
		offer.ObservedAt = at
		offer.ExpiresAt = at.Add(5 * time.Minute)
		// What a machine offers is the room it has left, not the disk it was
		// built with. An offer that restated its capacity whatever it was
		// holding is an offer that can never say no, and every Run reading its
		// disk would be reading a number that stopped being true the first time
		// anything landed here. A machine this world declares cannot measure its
		// disk states no room and states that nobody measured it, which are two
		// facts rather than one number.
		offer.Resources.EphemeralDiskBytes = 0
		if offer.Resources.EphemeralDiskKnown {
			offer.Resources.EphemeralDiskBytes = world.diskLedger(state).FreeBytes()
		}
		offer.Images = state.inventory(at)
		offer.Artifacts = world.artifactInventory(offer.ID, at)
		offer.Caches = world.cacheInventory(offer.ID, at)
		offers = append(offers, offer)
	}
	sort.Slice(offers, func(i, j int) bool { return offers[i].ID < offers[j].ID })
	return offers
}

// diskLedgers is what every machine in this world has room for and what is
// using it, in offer order. It is World Truth: an offer states the room that is
// left, and the difference between the two is exactly what a rule about disk
// accounting has to be able to see.
func (world *simulatedWorld) diskLedgers() []DiskLedger {
	ledgers := make([]DiskLedger, 0, len(world.truth))
	for _, id := range slices.Sorted(maps.Keys(world.truth)) {
		ledgers = append(ledgers, world.diskLedger(world.truth[id]))
	}
	return ledgers
}

// diskLedger is one machine's room, everything resident on it, and everything
// on its way there. Layers are named by the blob digest that identifies the
// bytes, so content two images share takes room once here exactly as it takes
// room once on a disk.
func (world *simulatedWorld) diskLedger(state hostState) DiskLedger {
	offerID := state.offer.ID
	ledger := DiskLedger{
		OfferID:       offerID,
		CapacityBytes: state.offer.Resources.EphemeralDiskBytes,
		ReservedBytes: world.reservedBytes(offerID),
	}
	for _, digest := range slices.Sorted(maps.Keys(state.heldLayers)) {
		ledger.Resident = append(ledger.Resident, ResidentContent{
			Kind:      ResidentLayer,
			Name:      digest,
			SizeBytes: int64(state.heldLayers[digest].Size),
		})
	}
	for _, artifactID := range slices.Sorted(maps.Keys(world.replicas)) {
		replica, held := world.replicas[artifactID][offerID]
		if !held {
			continue
		}
		ledger.Resident = append(ledger.Resident, ResidentContent{
			Kind:      ResidentArtifact,
			Name:      artifactID,
			SizeBytes: replica.SizeBytes,
		})
	}
	for _, identity := range slices.Sorted(maps.Keys(world.cacheMounts[offerID])) {
		ledger.Resident = append(ledger.Resident, ResidentContent{
			Kind:      ResidentCache,
			Name:      identity,
			SizeBytes: world.cacheMounts[offerID][identity].SizeBytes,
		})
	}
	return ledger
}

// reservedBytes is room this machine has already promised to content that is
// still moving onto it. A transfer in flight occupies nothing yet and cannot be
// treated as free space either: two Runs told the same gigabytes were available
// is how a machine ends up with neither of them able to finish.
func (world *simulatedWorld) reservedBytes(offerID string) int64 {
	reserved := int64(0)
	for _, pull := range world.pulls {
		if pull.offerID == offerID {
			reserved += pull.bytes
		}
	}
	for _, fetch := range world.replicating {
		if fetch.offerID == offerID {
			reserved += fetch.bytes
		}
	}
	// A running workload holds the room it declared it needs, which is what
	// makes a disk floor a promise rather than a number two Runs can both be
	// told. It goes back to the machine when the process exits, because what a
	// workload wrote in its own scratch space goes with it.
	for _, execution := range world.executions {
		if execution.OfferID == offerID && !execution.OutputsStored {
			reserved += execution.ReservedDiskBytes
		}
	}
	return reserved
}

// launchFitsOnDisk is the machine deciding whether it has anywhere to put what
// this launch needs: the room the Run declared for its own working state, the
// image bytes the host does not hold, the inputs it would have to read out of
// the object store, and the caches the workload declared. A host
// that runs out of disk partway through does not run the workload slowly, it
// fails with nothing to show, so the world refuses rather than creating content
// its own ledger cannot hold. Mercator is meant to have priced such a candidate
// down long before this, and this is what makes that pricing matter.
func (world *simulatedWorld) launchFitsOnDisk(request adapter.LaunchRequest, arrival RunArrival) bool {
	state := world.truth[request.SelectedOfferSnapshotID]
	_, needed := state.missing(request.Image, world.images[request.Image].Layers)
	needed += request.Resources.EphemeralDisk.MinBytes
	for _, artifactID := range arrival.Request.ConsumesArtifacts {
		if replica, held := world.replicas[artifactID][state.offer.ID]; !held || !replica.State.Usable() {
			version, _ := world.store.entry(artifactID)
			needed += version.SizeBytes
		}
	}
	for _, mount := range request.CacheMounts {
		identity := domain.CacheIdentity(request.WorkspaceID, mount)
		if _, held := world.cacheMounts[state.offer.ID][identity]; !held {
			needed += mount.SizeBytes
		}
	}
	return needed <= world.diskLedger(state).FreeBytes()
}

// publishedOffers is what the provider can say about the machines it sells, as
// of its last look. Mercator enumerates a machine only where it runs something
// of its own, so a slot it borrows and a machine that does not exist yet carry
// no inventory at all, which is what every provider adapter in the tree
// publishes. What those machines hold is still World Truth, stated there and
// read by the laws about what capacity accumulates; erasing it at the source
// would leave those laws reading an inventory that is empty whatever the world
// did.
func (world *simulatedWorld) publishedOffers() []domain.OfferSnapshot {
	offers := world.offerSnapshots(world.observed, world.observedAt)
	for index, offer := range offers {
		if !offer.KeepsWhatItRuns() {
			offers[index].Images = domain.ImageInventory{}
			offers[index].Artifacts = domain.ArtifactInventory{}
			offers[index].Caches = domain.CacheInventory{}
		}
	}
	return offers
}

// pullRunImage moves the image this launch needs onto the host that will run it
// and answers when the container can start, which is when the last of those
// bytes has landed. The ledger records what the pull fetched, which is nothing at
// all on a host that already holds the image; what the host keeps is recorded
// separately, when it keeps it.
//
// The pull starts at from rather than now, because bytes land on a machine and a
// machine that does not exist yet has to be made first.
func (world *simulatedWorld) pullRunImage(execution externalExecution, image string, from time.Time) time.Time {
	state := world.truth[execution.OfferID]
	layers := world.images[image].Layers
	fetchedLayers, bytes := state.missing(image, layers)
	fetched := fetchedNames(image, state.heldImages[domain.ReferenceDigest(image)], fetchedLayers)
	completesAt := from.Add(transferDuration(bytes, world.linkMbps(execution.OfferID, domain.NetworkScopeRegistry)))
	world.recordEffect(
		OperationImagePull,
		"image-pull/"+execution.LaunchKey+"/"+image,
		EffectCommandAccepted,
		EffectResponseDelivered,
		execution.RunID,
		execution.LaunchKey,
		"",
		map[string]any{"image": image, "offer_id": execution.OfferID},
		map[string]any{
			"fetched_digests": fetched,
			"fetched_bytes":   bytes,
			"completes_at":    completesAt,
		},
		"",
	)
	if len(fetched) > 0 && state.offer.KeepsWhatItRuns() {
		world.pulls = append(world.pulls, pendingPull{
			offerID:     execution.OfferID,
			runID:       execution.RunID,
			launchKey:   execution.LaunchKey,
			source:      "launch",
			image:       image,
			layers:      layers,
			fetched:     fetched,
			bytes:       bytes,
			completesAt: completesAt,
		})
		world.settlePulls()
	}
	return completesAt
}

// fetchedNames is everything this pull puts on the host, under every name that
// content answers to. The image reference joins the list when the host did not
// already hold it whole, because holding the whole image is its own fact.
func fetchedNames(image string, alreadyHeld bool, layers []scenario.LayerSpec) []string {
	names := make([]string, 0, 2*len(layers)+1)
	for _, layer := range layers {
		names = append(names, layerNames(layer)...)
	}
	if !alreadyHeld {
		names = append(names, domain.ReferenceDigest(image))
	}
	return names
}

// unmeasuredLinkMbps is how fast this world moves content over a path no fixture
// declared. It is this world's own number rather than the scheduler's assumption,
// deliberately the same figure: an unmeasured path is exactly the case where both
// halves are guessing about one thing, so a difference here would be a difference
// no Blueprint asked for. What separates the two halves is a fixture declaring a
// path, which this world then really spends and Mercator then really reads.
const unmeasuredLinkMbps = 500.0

// linkMbps is how fast this world moves content of one kind onto one machine. It
// reads the Blueprint's own declaration rather than the fact the machine
// published, which is what keeps the prediction and the actual separable: a
// fixture can state a path a host measured and then disowned, and this world
// still crosses it at the speed the fixture said it is.
func (world *simulatedWorld) linkMbps(offerID string, scope domain.NetworkScope) float64 {
	if declared, stated := world.paths.LinkMbps(offerID, scope); stated {
		return declared
	}
	return unmeasuredLinkMbps
}

// transferDuration is how long this world takes to move content, which is its
// own model rather than the scheduler's: a fixture is only worth anything when
// the actual pull and the predicted pull come from different code.
func transferDuration(bytes int64, bandwidthMbps float64) time.Duration {
	if bytes <= 0 {
		return 0
	}
	seconds := float64(bytes*8) / 1_000_000 / bandwidthMbps
	return time.Duration(seconds * float64(time.Second))
}

// readRunArtifacts resolves every input this execution declared and answers when
// the last of them is readable on the host. A verified local copy is read where
// there is one and costs nothing, which is the whole value of a replica; anything
// else is fetched from the object store, because a copy nobody checked is not
// evidence that the right bytes are here. The ledger records which it was, so
// what a Run read is a fact rather than an inference from world state.
//
// Reads start at from for the same reason a pull does: there is nowhere to read
// them onto until the machine exists.
func (world *simulatedWorld) readRunArtifacts(execution externalExecution, consumes []string, from time.Time) time.Time {
	ready := from
	for _, artifactID := range consumes {
		replica, held := world.replicas[artifactID][execution.OfferID]
		source := "replica"
		completesAt := from
		if !held || !replica.State.Usable() {
			source = "object_store"
			completesAt = from.Add(world.store.transferDuration(artifactID, world.linkMbps(execution.OfferID, domain.NetworkScopeObjectStore)))
			version, _ := world.store.entry(artifactID)
			world.replicating = append(world.replicating, pendingReplica{
				offerID:     execution.OfferID,
				runID:       execution.RunID,
				launchKey:   execution.LaunchKey,
				source:      source,
				artifactID:  artifactID,
				bytes:       version.SizeBytes,
				completesAt: completesAt,
			})
		}
		world.recordEffect(
			OperationArtifactRead,
			"artifact-read/"+execution.LaunchKey+"/"+artifactID,
			EffectCommandAccepted,
			EffectResponseDelivered,
			execution.RunID,
			execution.LaunchKey,
			"",
			map[string]any{"artifact_id": artifactID, "offer_id": execution.OfferID},
			map[string]any{"source": source, "state": replica.State, "completes_at": completesAt},
			"",
		)
		ready = later(ready, completesAt)
	}
	world.settleReplicas()
	return ready
}

// storeRunOutputs is what a finished producer leaves behind, in the two places
// it leaves it. The output is written to the host that computed it, where it is
// a local copy like any other, and then uploaded to the object store, where it
// becomes an Artifact anyone can depend on. The gap between the two is the whole
// point: a consumer admitted on the first has been admitted on bytes that live
// on one machine. Both moments are measured from when the process exited, which
// is the only clock a workload's output has.
// roomForOutputs is the machine deciding whether what this workload computed
// will fit on it. Content a Run produces is content on somebody's disk, and no
// one else could have accounted for it: a Run declares which Artifacts it
// publishes and never how large they will be, so the only room it can be given
// is the room it reserved. A workload that writes past it does not publish a
// smaller Artifact, it fails with its disk full, which is what this world does
// rather than creating content its own ledger cannot hold.
func (world *simulatedWorld) roomForOutputs(execution externalExecution) bool {
	arrival := world.runs[execution.RunID]
	produced := int64(0)
	for _, artifactID := range arrival.Request.ProducesArtifacts {
		version, _ := world.store.entry(artifactID)
		produced += version.SizeBytes
	}
	// The reservation is released by the same exit that writes the output, so
	// the room the Run had is the room it still has.
	return produced <= world.diskLedger(world.truth[execution.OfferID]).FreeBytes()+execution.ReservedDiskBytes
}

func (world *simulatedWorld) storeRunOutputs(execution externalExecution, at time.Time) {
	arrival := world.runs[execution.RunID]
	for _, artifactID := range arrival.Request.ProducesArtifacts {
		world.keepReplica(artifactID, execution.OfferID, execution.RunID, execution.LaunchKey, "run_output", at)
		world.publishing = append(world.publishing, pendingPublication{
			artifactID:  artifactID,
			runID:       execution.RunID,
			completesAt: at.Add(world.store.transferDuration(artifactID, world.linkMbps(execution.OfferID, domain.NetworkScopeObjectStore))),
		})
	}
}

// settleCacheAttachments creates the container for every execution whose image
// has landed, which is the act that opens the caches it declared. Attaching and
// filling a cache are one moment here for the same reason they are on a real
// node: a container runtime makes the storage the mount point names and can say
// nothing whatsoever about what the application then puts in it.
func (world *simulatedWorld) settleCacheAttachments() {
	for _, launchKey := range slices.Sorted(maps.Keys(world.executions)) {
		execution := world.executions[launchKey]
		if execution.CachesAttached || world.now.Before(execution.StartedAt) {
			continue
		}
		for _, mount := range execution.CacheMounts {
			world.attachCache(execution, mount)
		}
		execution.CachesAttached = true
		world.executions[launchKey] = execution
	}
}

// attachCache is one cache opened for one execution: the storage the container
// mounts, made here if this tenant and generation had none, and whatever the
// workload found in it. A cache costs no time either way, because it is the
// application's own state and this world has no model of what the application
// does with it.
//
// It is addressed by its full identity, which carries the workspace this
// execution belongs to, so an attachment can only ever land in the cache the
// Run's own tenant owns.
//
// Capacity that keeps nothing keeps no cache either, for the same two reasons it
// keeps no image: a provisionable offer is a machine that does not exist yet,
// and a one-shot product is gone once its workload exits. A cache opened there
// would be mutable state outliving its own host.
func (world *simulatedWorld) attachCache(execution externalExecution, mount domain.CacheMountRequirement) {
	if !world.truth[execution.OfferID].offer.KeepsWhatItRuns() {
		return
	}
	if world.cacheMounts[execution.OfferID] == nil {
		world.cacheMounts[execution.OfferID] = map[string]CacheMountState{}
	}
	identity := domain.CacheIdentity(execution.WorkspaceID, mount)
	previous, found := world.cacheMounts[execution.OfferID][identity]
	createdAt := previous.CreatedAt
	if !found {
		createdAt = execution.StartedAt
	}
	state := CacheMountState{
		OfferID:          execution.OfferID,
		Identity:         identity,
		WorkspaceID:      execution.WorkspaceID,
		Name:             mount.Name,
		CompatibilityKey: mount.CompatibilityKey,
		Revision:         previous.Revision + 1,
		CreatedAt:        createdAt,
		SizeBytes:        max(previous.SizeBytes, mount.SizeBytes),
	}
	world.cacheMounts[execution.OfferID][identity] = state
	world.recordEffect(
		OperationCacheMountAttach,
		"cache-mount-attach/"+execution.LaunchKey+"/"+identity,
		EffectCommandAccepted,
		EffectResponseDelivered,
		execution.RunID,
		execution.LaunchKey,
		"",
		map[string]any{
			"identity":          identity,
			"workspace_id":      execution.WorkspaceID,
			"name":              mount.Name,
			"compatibility_key": mount.CompatibilityKey,
			"offer_id":          execution.OfferID,
		},
		map[string]any{"found": found, "revision": state.Revision, "size_bytes": state.SizeBytes},
		"",
	)
}

func (world *simulatedWorld) artifactReplicas() []ArtifactReplica {
	var replicas []ArtifactReplica
	for _, hosts := range world.replicas {
		for offerID, replica := range hosts {
			replicas = append(replicas, ArtifactReplica{OfferID: offerID, ArtifactReplica: replica})
		}
	}
	sort.Slice(replicas, func(i, j int) bool {
		if replicas[i].ArtifactID == replicas[j].ArtifactID {
			return replicas[i].OfferID < replicas[j].OfferID
		}
		return replicas[i].ArtifactID < replicas[j].ArtifactID
	})
	return replicas
}

// artifactInventory is the Artifact content one host holds, in version-ID order
// so one world state produces one offer.
func (world *simulatedWorld) artifactInventory(offerID string, at time.Time) domain.ArtifactInventory {
	inventory := domain.ArtifactInventory{Known: true, ObservedAt: at}
	for _, artifactID := range slices.Sorted(maps.Keys(world.replicas)) {
		if replica, held := world.replicas[artifactID][offerID]; held {
			inventory.Replicas = append(inventory.Replicas, replica)
		}
	}
	return inventory
}

// later is the moment both of two things have happened.
func later(first, second time.Time) time.Time {
	if second.After(first) {
		return second
	}
	return first
}

func (world *simulatedWorld) cacheMountStates() []CacheMountState {
	var mounts []CacheMountState
	for _, held := range world.cacheMounts {
		for _, state := range held {
			mounts = append(mounts, state)
		}
	}
	sort.Slice(mounts, func(i, j int) bool {
		if mounts[i].OfferID == mounts[j].OfferID {
			return mounts[i].Identity < mounts[j].Identity
		}
		return mounts[i].OfferID < mounts[j].OfferID
	})
	return mounts
}

// cacheInventory is the mutable state one host holds, in identity order so one
// world state produces one offer. Every entry names the workspace that owns it,
// because an offer is read by every workspace's Runs and only the identity keeps
// them apart.
func (world *simulatedWorld) cacheInventory(offerID string, at time.Time) domain.CacheInventory {
	inventory := domain.CacheInventory{Known: true, ObservedAt: at}
	for _, identity := range slices.Sorted(maps.Keys(world.cacheMounts[offerID])) {
		inventory.Mounts = append(inventory.Mounts, world.cacheMounts[offerID][identity].mount())
	}
	return inventory
}

func (world *simulatedWorld) matchOperationFault(operation, runID string, attempt int) *scenario.FaultSpec {
	runName := strings.TrimPrefix(runID, "run-")
	for index := range world.faults {
		fault := &world.faults[index]
		if world.usedFaults[fault.ID] ||
			fault.Trigger.Operation != operation ||
			fault.Trigger.Run != "" && fault.Trigger.Run != runName ||
			fault.Trigger.Attempt != 0 && fault.Trigger.Attempt != attempt {
			continue
		}
		world.usedFaults[fault.ID] = true
		return fault
	}
	return nil
}

func (world *simulatedWorld) matchEventFault(eventType, runID string) *scenario.FaultSpec {
	world.mu.Lock()
	defer world.mu.Unlock()
	runName := strings.TrimPrefix(runID, "run-")
	for index := range world.faults {
		fault := &world.faults[index]
		if world.usedFaults[fault.ID] ||
			fault.Trigger.Event != eventType ||
			fault.Trigger.Run != "" && fault.Trigger.Run != runName {
			continue
		}
		world.usedFaults[fault.ID] = true
		return fault
	}
	return nil
}

// stageSeconds is what this launch spent on each of the eight stages, named by
// the stage. It is World Truth: every entry is the gap between two moments this
// world settled, so nothing here is derived from what Mercator predicted.
//
// The three stages a provider performs are reported as the world spent them,
// which is what the fixture declared, and not as a share of the moment the
// machine became ready: a fixture that says a machine boots for four minutes and
// enrolls for thirty seconds has stated two actuals, and folding them back into
// one would delete the fact the record exists to carry.
// stageSeconds is what this launch spent on each stage it reached. A stage the
// launch never reached carries no duration at all, because a stage that did not
// happen and a stage that happened instantly are opposite facts and a calibration
// reading the first as a zero would train on it.
func (world *simulatedWorld) stageSeconds(execution externalExecution) map[string]float64 {
	machine := world.truth[execution.OfferID].provisioning
	content := later(execution.ImageAt, execution.ArtifactsAt)
	spent := map[string]float64{
		string(domain.StageAcquisition):    machine.AcquisitionSpend().Seconds(),
		string(domain.StageBoot):           machine.BootSpend().Seconds(),
		string(domain.StageAgentReady):     machine.AgentReadySpend().Seconds(),
		string(domain.StageImageFetch):     execution.ImageAt.Sub(execution.ReadyAt).Seconds(),
		string(domain.StageArtifactFetch):  execution.ArtifactsAt.Sub(execution.ReadyAt).Seconds(),
		string(domain.StageUnpack):         execution.AssembledAt.Sub(content).Seconds(),
		string(domain.StageContainerStart): execution.StartedAt.Sub(execution.AssembledAt).Seconds(),
	}
	if !execution.ApplicationReadyAt.IsZero() {
		spent[string(domain.StageApplicationReady)] = execution.ApplicationReadyAt.Sub(execution.StartedAt).Seconds()
	}
	return spent
}

func (world *simulatedWorld) recordLaunchEffect(request adapter.LaunchRequest, command EffectCommand, response EffectResponse, consequence any, faultID string) {
	if receipt, ok := consequence.(adapter.LaunchReceipt); ok {
		execution := world.executions[receipt.LaunchKey]
		consequence = map[string]any{
			"external_id": receipt.ExternalID,
			"launch_key":  receipt.LaunchKey,
			"phase":       receipt.Phase,
			"accepted_at": receipt.AcceptedAt,
			"duplicate":   receipt.Duplicate,
			// What this launch really spent, stage by stage, and how long it then
			// ran. This is the only place the actuals a stage prediction is
			// calibrated against exist: Mercator can observe a container starting and
			// an application reporting ready, and nothing in production tells it when
			// a machine finished booting, so a Run Bundle reads the world's own
			// ledger for the rest of the waterfall.
			"start_latency_seconds":  execution.StartedAt.Sub(execution.AcceptedAt).Seconds(),
			"actual_runtime_seconds": execution.CompletesAt.Sub(execution.StartedAt).Seconds(),
			"stage_seconds":          world.stageSeconds(execution),
			// Whether the application behind this process ever comes up here. It is
			// stated rather than inferred from the missing duration, because a stage
			// with no actual is either a stage this world never reached or a stage
			// nothing timed, and only the world can say which.
			"application_becomes_ready": world.launch.ApplicationBecomesReady(),
		}
	}
	world.recordEffect(
		OperationProviderLaunch,
		request.OperationKey,
		command,
		response,
		request.RunID,
		request.OperationKey,
		request.RequestHash,
		map[string]any{
			"workspace_id": request.WorkspaceID,
			"run_id":       request.RunID,
			"attempt_id":   request.AttemptID,
			"launch_key":   request.LaunchKey,
			"image":        request.Image,
			"offer_id":     request.SelectedOfferSnapshotID,
			"disposition":  request.Disposition,
		},
		consequence,
		faultID,
	)
}

func (world *simulatedWorld) recordObservationEffect(request adapter.ObserveRequest, command EffectCommand, consequence any) {
	world.recordEffect(
		OperationProviderObserve,
		"observe/"+request.LaunchKey,
		command,
		EffectResponseDelivered,
		world.executions[request.LaunchKey].RunID,
		request.LaunchKey,
		request.RequestHash,
		map[string]any{"workspace_id": request.WorkspaceID, "launch_key": request.LaunchKey},
		consequence,
		"",
	)
}

func (world *simulatedWorld) recordCleanupEffect(operation, operationKey, requestHash, launchKey, correlationID string, command EffectCommand) {
	world.recordEffect(
		operation,
		operationKey,
		command,
		EffectResponseDelivered,
		correlationID,
		launchKey,
		requestHash,
		map[string]any{"launch_key": launchKey},
		map[string]any{"removed": command != EffectCommandRejected},
		"",
	)
}

func (world *simulatedWorld) recordEffect(
	operation string,
	operationID string,
	command EffectCommand,
	response EffectResponse,
	correlationID string,
	causationID string,
	requestHash string,
	request any,
	consequence any,
	faultID string,
) {
	world.effectSequence++
	world.effects = append(world.effects, EffectRecord{
		ID:            DeterministicID(world.seed, "effect", fmt.Sprintf("%020d/%s", world.effectSequence, operationID)),
		Sequence:      world.effectSequence,
		At:            world.now,
		Operation:     operation,
		OperationID:   operationID,
		Command:       command,
		Response:      response,
		CorrelationID: correlationID,
		CausationID:   causationID,
		RequestHash:   requestHash,
		Request:       mustJSON(request),
		Consequence:   mustJSON(consequence),
		FaultID:       faultID,
	})
}

// ResolveManifest answers what an image contains from the World Tape's catalog.
// It is the simulated registry: Placement subtracts what a host holds from what
// this returns, exactly as it would against a real one, and it says no the same
// five ways a real one does.
func (world *simulatedWorld) ResolveManifest(_ context.Context, imageDigest string, _ domain.Platform) (domain.ImageManifest, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	image, known := world.images[imageDigest]
	if !known {
		return domain.ImageManifest{}, fmt.Errorf("%w: %s", ociresolver.ErrImageUnknown, imageDigest)
	}
	switch image.Registry {
	case scenario.RegistryUnresolvable:
		return domain.ImageManifest{}, fmt.Errorf("%w: %s", ociresolver.ErrManifestUnresolvable, imageDigest)
	case scenario.RegistryUnauthorized:
		return domain.ImageManifest{}, fmt.Errorf("%w: %s", ociresolver.ErrUnauthorized, imageDigest)
	case scenario.RegistryThrottled:
		return domain.ImageManifest{}, fmt.Errorf("%w: %s", ociresolver.ErrThrottled, imageDigest)
	case scenario.RegistryUnreachable:
		return domain.ImageManifest{}, fmt.Errorf("%w: %s", ociresolver.ErrUnreachable, imageDigest)
	}
	// The image is named by the digest the reference pins, which is the name a
	// real machine reports holding it under. A registry that answered with the
	// whole reference would be handing the scheduler a name no host can say.
	manifest := domain.ImageManifest{Known: true, Digest: domain.ReferenceDigest(imageDigest)}
	for _, layer := range image.Layers {
		manifest.Layers = append(manifest.Layers, domain.ImageLayer{
			Digest:          layer.Digest,
			DiffID:          layer.DiffID,
			CompressedBytes: int64(layer.Size),
		})
	}
	return manifest, nil
}

// labCandidate is what a Blueprint says this capacity is, as opposed to how big it
// is: the provider selling it, where it sits, the product name it is sold under,
// and the machine it is wherever the Blueprint names a machine at all.
//
// It exists because a simulated fleet that stated none of it had one candidate key
// for every listing in the world. Two marketplaces in two regions were
// indistinguishable at every level of the key, so a prediction learned from one
// provider's launches would be served as evidence about the other's listing and no
// rule could see it, while the branch for a candidate that cannot recur was
// reachable only by a CPU-only listing nothing had named.
type labCandidate struct {
	provider     string
	region       string
	instanceType string
	// machine is the handle for capacity that is a machine rather than a listing for
	// one. A Rental and a borrowed host are machines this world keeps; a marketplace
	// listing describes a machine that does not exist yet and names none.
	machine string
}

// providerName is the backend this capacity comes from. A Blueprint that names no
// provider gets the Lab itself, which is what every simulated listing was before a
// fixture could say otherwise.
func (candidate labCandidate) providerName() string {
	if candidate.provider == "" {
		return labProvider
	}
	return candidate.provider
}

func labOffer(
	id string,
	kind domain.OfferKind,
	lane domain.ExecutionLane,
	candidate labCandidate,
	ratePerHourUSD float64,
	resources *scenario.ResourcesSpec,
) domain.OfferSnapshot {
	offer := domain.OfferSnapshot{
		ID:           id,
		MachineID:    candidate.machine,
		ConnectionID: labConnection,
		AdapterType:  candidate.providerName(),
		NativeRef:    id,
		Region:       candidate.region,
		InstanceType: candidate.instanceType,
		Kind:         kind,
		Lane:         lane,
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources:    scenario.HostInventory(resources),
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
		Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
	}
	// Only capacity Mercator keeps names a Rental, which is the same stamp
	// capability.StampLane applies to every offer in production.
	if offer.KeepsWhatItRuns() {
		offer.RentalID = id
	}
	return offer
}

// findLayer resolves a digest a fixture seeds directly onto a host back to the
// layer the image catalog defines, so the host holds the same content under
// every name that layer answers to.
func findLayer(world scenario.WorldSpec, digest string) scenario.LayerSpec {
	for _, image := range world.Images {
		for _, layer := range image.Layers {
			if layer.Digest == digest {
				return layer
			}
		}
	}
	return scenario.LayerSpec{Digest: digest}
}

func cloneHostState(state hostState) hostState {
	return hostState{
		offer:          state.offer,
		heldLayers:     cloneMap(state.heldLayers),
		heldImages:     cloneMap(state.heldImages),
		packed:         cloneMap(state.packed),
		reportsDiffIDs: state.reportsDiffIDs,
		leaseExpiresAt: state.leaseExpiresAt,
		provisioning:   state.provisioning,
		clockAhead:     state.clockAhead,
	}
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	cloned := make(map[K]V, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneEffects(effects []EffectRecord) []EffectRecord {
	cloned := slices.Clone(effects)
	for index := range cloned {
		cloned[index].Request = slices.Clone(cloned[index].Request)
		cloned[index].Consequence = slices.Clone(cloned[index].Consequence)
	}
	return cloned
}

func offerIDs(offers []domain.OfferSnapshot) []string {
	ids := make([]string, len(offers))
	for index, offer := range offers {
		ids[index] = offer.ID
	}
	return ids
}

func ownedLaunchKeys(objects []adapter.OwnedExternalObject) []string {
	keys := make([]string, len(objects))
	for index, object := range objects {
		keys[index] = object.LaunchKey
	}
	return keys
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
