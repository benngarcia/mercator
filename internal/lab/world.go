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
	"github.com/benngarcia/mercator/internal/gpunorm"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/scenario"
)

const (
	labWorkspace          = "ws_lab"
	labConnection         = "connection:lab"
	defaultLabCPUMillis   = int64(8000)
	defaultLabMemoryBytes = int64(32e9)
	defaultLabDiskBytes   = int64(200e9)
)

type externalExecution struct {
	ExternalID     string                `json:"external_id"`
	RunID          string                `json:"run_id"`
	AttemptID      string                `json:"attempt_id"`
	LaunchKey      string                `json:"launch_key"`
	OwnershipToken string                `json:"ownership_token"`
	RequestHash    string                `json:"request_hash"`
	OfferID        string                `json:"offer_id"`
	Disposition    domain.Disposition    `json:"disposition"`
	Phase          adapter.ExternalPhase `json:"phase"`
	// AcceptedAt is when the provider took the launch. StartedAt is when the
	// container actually began, which cannot precede the arrival of the image it
	// runs: a process cannot execute bytes that have not landed. The gap between
	// them is the start latency Mercator predicted and now has an actual for.
	AcceptedAt    time.Time `json:"accepted_at"`
	StartedAt     time.Time `json:"started_at"`
	CompletesAt   time.Time `json:"completes_at"`
	OutputsStored bool      `json:"outputs_stored"`
}

type ArtifactReplica struct {
	ArtifactID string `json:"artifact_id"`
	OfferID    string `json:"offer_id"`
	SizeBytes  int64  `json:"size_bytes"`
}

type CacheMountState struct {
	OfferID  string `json:"offer_id"`
	Name     string `json:"name"`
	Revision uint64 `json:"revision"`
}

type WorldTruthSnapshot struct {
	At               time.Time              `json:"at"`
	Offers           []domain.OfferSnapshot `json:"offers"`
	ActiveExecutions []externalExecution    `json:"active_executions"`
	ArtifactReplicas []ArtifactReplica      `json:"artifact_replicas"`
	CacheMounts      []CacheMountState      `json:"cache_mounts"`
}

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
	// verifiedAt is when this host last enumerated itself, and validUntil how
	// long it stands behind that answer. A host that keeps looking answers as
	// of now and states no bound; one the World Tape declared to have looked
	// once says so, and its answer stops being one when the bound passes.
	verifiedAt time.Time
	validUntil time.Time
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

// inventory is what this host says it holds, in the digest space its runtime
// can enumerate, and separating what it can start on from what it has only
// fetched. A Docker host names its layers by uncompressed diff ID and has no way
// to name the compressed blob a registry served it, so answering in both spaces
// would be the world lending a machine knowledge it does not have.
func (state hostState) inventory(at time.Time) domain.ImageInventory {
	observed := at
	if !state.verifiedAt.IsZero() {
		observed = state.verifiedAt
	}
	inventory := domain.ImageInventory{Known: true, ObservedAt: observed, ValidUntil: state.validUntil}
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
	offerID     string
	runID       string
	launchKey   string
	image       string
	layers      []scenario.LayerSpec
	fetched     []string
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
	pulls     []pendingPull
	activeRun string
	runs      map[string]RunArrival
	artifacts map[string]int64
	replicas  map[string]map[string]bool
	// seededArtifacts are the Artifacts a Rental already held when the world was
	// built. They are available to a consuming Run without any producer having
	// published them, so invariants that order launches against publication
	// treat them as present from virtual time zero.
	seededArtifacts map[string]bool
	// seededLocality is the image content each host held when the world was
	// built, keyed by offer. Everything outside it has to be explained by an
	// accepted image pull recorded against that same host.
	seededLocality map[string]map[string]bool
	cacheMounts    map[string]map[string]uint64

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
		seed:            tape.Seed,
		now:             tape.Start,
		images:          make(map[string]scenario.ImageSpec, len(tape.InitialWorld.Images)),
		truth:           map[string]hostState{},
		observed:        map[string]hostState{},
		runs:            map[string]RunArrival{},
		artifacts:       map[string]int64{},
		replicas:        map[string]map[string]bool{},
		seededArtifacts: map[string]bool{},
		seededLocality:  map[string]map[string]bool{},
		cacheMounts:     map[string]map[string]uint64{},
		executions:      map[string]externalExecution{},
		operations:      map[string]worldOperation{},
		launchCount:     map[string]int{},
		faults:          slices.Clone(tape.Faults),
		usedFaults:      map[string]bool{},
	}
	for reference, image := range tape.InitialWorld.Images {
		world.images[reference] = scenario.ImageSpec{Layers: slices.Clone(image.Layers), Registry: image.Registry}
	}
	for _, artifact := range tape.InitialWorld.Artifacts {
		world.artifacts[artifact.ID] = int64(artifact.Size)
		world.replicas[artifact.ID] = map[string]bool{}
	}
	for _, rental := range tape.InitialWorld.Rentals {
		state := hostState{
			offer:          labOffer(rental.ID, domain.OfferKindStanding, domain.LaneReusable, rental.RatePerHourUSD, rental.Resources),
			heldLayers:     map[string]scenario.LayerSpec{},
			heldImages:     map[string]bool{},
			packed:         map[string]bool{},
			reportsDiffIDs: rental.ReportsDiffIDs,
		}
		if rental.InventoryValidFor != nil {
			state.verifiedAt = tape.InitialWorld.Start()
			state.validUntil = state.verifiedAt.Add(rental.InventoryValidFor.Duration())
		}
		applyOfferWorldFacts(&state.offer, tape.InitialWorld, rental.ID, nil, rental.Billing)
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
		for _, artifactID := range rental.ArtifactReplicas {
			world.replicas[artifactID][rental.ID] = true
			world.seededArtifacts[artifactID] = true
		}
		world.cacheMounts[rental.ID] = map[string]uint64{}
		for _, name := range rental.CacheMounts {
			world.cacheMounts[rental.ID][name] = 1
		}
	}
	for _, host := range tape.InitialWorld.Hosts {
		state := hostState{
			offer:      labOffer(host.ID, domain.OfferKindStanding, domain.LaneEphemeral, host.RatePerHourUSD, host.Resources),
			heldLayers: map[string]scenario.LayerSpec{},
			heldImages: map[string]bool{},
			packed:     map[string]bool{},
		}
		applyOfferWorldFacts(&state.offer, tape.InitialWorld, host.ID, nil, host.Billing)
		world.seededLocality[host.ID] = state.seededDigests()
		world.truth[host.ID] = cloneHostState(state)
	}
	for _, marketplace := range tape.InitialWorld.Marketplace {
		state := hostState{
			offer: labOffer(
				marketplace.ID,
				domain.OfferKindProvisionable,
				marketplace.ExecutionLane(),
				marketplace.RatePerHourUSD,
				marketplace.Resources,
			),
			heldLayers: map[string]scenario.LayerSpec{},
			heldImages: map[string]bool{},
		}
		applyOfferWorldFacts(&state.offer, tape.InitialWorld, marketplace.ID, marketplace.Available, marketplace.Billing)
		world.seededLocality[marketplace.ID] = state.seededDigests()
		state.offer.Provisioning = &domain.Estimate{
			Expected: marketplace.Provisioning.Expected.Duration().Seconds(),
			Source:   "lab-world",
		}
		if marketplace.Provisioning.P90 != nil {
			state.offer.Provisioning.P90 = marketplace.Provisioning.P90.Duration().Seconds()
		}
		world.truth[marketplace.ID] = cloneHostState(state)
	}
	world.publishObservations()
	return world, nil
}

func applyOfferWorldFacts(offer *domain.OfferSnapshot, world scenario.WorldSpec, offerID string, available *bool, billing scenario.BillingSpec) {
	if available != nil {
		offer.Capacity.Available = *available
	}
	offer.Pricing.SetupFeeUSD = billing.SetupFeeUSD
	if billing.MinimumCharge != nil {
		offer.Pricing.MinimumChargeSeconds = int64(billing.MinimumCharge.Duration().Seconds())
	}
	for _, path := range world.Paths {
		if path.From != offerID {
			continue
		}
		offer.Network.Download = append(offer.Network.Download, domain.NetworkFact{
			Scope:       domain.NetworkScope(path.Scope),
			Statistic:   "p10",
			ValueMbps:   path.P10Mbps,
			Source:      "lab-world",
			SampleCount: 1,
			ObservedAt:  world.Start(),
			ValidUntil:  world.Start().Add(24 * time.Hour),
			Confidence:  1,
		})
	}
}

func (world *simulatedWorld) prepareRun(runID string, arrival RunArrival) {
	world.mu.Lock()
	defer world.mu.Unlock()
	world.activeRun = runID
	world.runs[runID] = arrival
}

func (world *simulatedWorld) artifactDependenciesAvailable(arrival RunArrival) bool {
	world.mu.Lock()
	defer world.mu.Unlock()
	for _, artifactID := range arrival.Request.ConsumesArtifacts {
		if !hasAnyReplica(world.replicas[artifactID]) {
			return false
		}
	}
	return true
}

// setNow moves virtual time, lets everything the world scheduled for that
// instant happen, and publishes what the provider can now see. Image content
// that finished arriving is on its host from then on, whether or not anyone has
// looked.
func (world *simulatedWorld) setNow(now time.Time) {
	world.mu.Lock()
	defer world.mu.Unlock()
	world.now = now.UTC()
	world.settlePulls()
	world.publishObservations()
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
			map[string]any{"image": pull.image, "offer_id": pull.offerID},
			map[string]any{"retained_digests": pull.fetched},
			"",
		)
	}
	world.pulls = remaining
}

// cancelPull drops content that was still moving onto a host when the execution
// that asked for it was released or terminated. This world moves an image whole
// or not at all, so a transfer nothing is waiting on leaves nothing behind.
func (world *simulatedWorld) cancelPull(launchKey string) {
	world.pulls = slices.DeleteFunc(world.pulls, func(pull pendingPull) bool {
		return pull.launchKey == launchKey
	})
}

// executionHorizon is the latest moment the world still owes a running
// execution its completion, and zero when nothing is running.
func (world *simulatedWorld) executionHorizon() time.Time {
	world.mu.Lock()
	defer world.mu.Unlock()
	var horizon time.Time
	for _, execution := range world.executions {
		if execution.CompletesAt.After(horizon) {
			horizon = execution.CompletesAt
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
	state.offer.Capacity = domain.CapacityEvidence{Available: available, Confidence: 1}
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
	Runs              map[string]RunArrival
	KnownArtifactIDs  map[string]bool
	SeededArtifactIDs map[string]bool
	SeededLocality    map[string]map[string]bool
}

func (world *simulatedWorld) invariantFacts() worldFacts {
	world.mu.Lock()
	defer world.mu.Unlock()
	facts := worldFacts{
		Runs:              cloneMap(world.runs),
		KnownArtifactIDs:  make(map[string]bool, len(world.artifacts)),
		SeededArtifactIDs: cloneMap(world.seededArtifacts),
		SeededLocality:    make(map[string]map[string]bool, len(world.seededLocality)),
	}
	for artifactID := range world.artifacts {
		facts.KnownArtifactIDs[artifactID] = true
	}
	for offerID, digests := range world.seededLocality {
		facts.SeededLocality[offerID] = cloneMap(digests)
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
	return world.offerSnapshots(world.observed, world.observedAt)
}

func (world *simulatedWorld) ListOffers(_ context.Context, request adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	if world.activeRun == "" {
		return nil, fmt.Errorf("Lab world has no active Run for offer observation")
	}
	arrival := world.runs[world.activeRun]
	if _, exists := world.images[arrival.Request.Image]; !exists {
		return nil, fmt.Errorf("Lab world image %q is not defined", arrival.Request.Image)
	}
	offers := world.offerSnapshots(world.observed, world.observedAt)
	world.recordEffect(
		OperationProviderListOffers,
		"list-offers/"+world.activeRun,
		EffectCommandAccepted,
		EffectResponseDelivered,
		world.activeRun,
		"placement",
		"",
		map[string]any{"workspace_id": request.WorkspaceID},
		map[string]any{"offer_ids": offerIDs(offers)},
		"",
	)
	return offers, nil
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
	execution := externalExecution{
		ExternalID:     "lab-" + request.AttemptID,
		RunID:          request.RunID,
		AttemptID:      request.AttemptID,
		LaunchKey:      request.LaunchKey,
		OwnershipToken: request.OwnershipToken,
		RequestHash:    request.RequestHash,
		OfferID:        request.SelectedOfferSnapshotID,
		Disposition:    request.Disposition,
		Phase:          adapter.ExternalPhaseRunning,
		AcceptedAt:     world.now,
	}
	if offer.offer.Kind == domain.OfferKindStanding {
		offer.offer.Capacity = domain.CapacityEvidence{Available: false, Confidence: 1}
		world.truth[request.SelectedOfferSnapshotID] = offer
	}
	execution.StartedAt = world.pullRunImage(execution, request.Image)
	execution.CompletesAt = execution.StartedAt.Add(actualRuntimeForOffer(arrival, request.SelectedOfferSnapshotID))
	world.fetchRunArtifacts(execution, arrival)
	world.executions[request.LaunchKey] = execution
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
	if !world.now.Before(execution.CompletesAt) {
		execution.Phase = adapter.ExternalPhaseSucceeded
		if !execution.OutputsStored {
			world.storeRunOutputs(execution)
			execution.OutputsStored = true
		}
		world.executions[request.LaunchKey] = execution
	}
	observation := adapter.ExternalObservation{
		ExternalID: execution.ExternalID,
		LaunchKey:  execution.LaunchKey,
		Phase:      execution.Phase,
		ObservedAt: world.now,
		NativeJSON: fmt.Sprintf(`{"adapter":"lab","external_id":%q}`, execution.ExternalID),
	}
	if observation.Phase == adapter.ExternalPhaseSucceeded {
		exitCode := 0
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
		if request.WorkspaceID != "" && request.WorkspaceID != labWorkspace {
			continue
		}
		objects = append(objects, adapter.OwnedExternalObject{
			ExternalID:     execution.ExternalID,
			WorkspaceID:    labWorkspace,
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
		world.cancelPull(launchKey)
		if offer := world.truth[execution.OfferID]; offer.offer.Kind == domain.OfferKindStanding {
			offer.offer.Capacity = domain.CapacityEvidence{Available: true, Confidence: 1}
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
		offer.Images = state.inventory(at)
		offers = append(offers, offer)
	}
	sort.Slice(offers, func(i, j int) bool { return offers[i].ID < offers[j].ID })
	return offers
}

// pullRunImage moves the image this launch needs onto the host that will run it
// and answers when the container can start, which is when the last of those
// bytes has landed. The ledger records what the pull fetched, which is nothing at
// all on a host that already holds the image; what the host keeps is recorded
// separately, when it keeps it.
func (world *simulatedWorld) pullRunImage(execution externalExecution, image string) time.Time {
	state := world.truth[execution.OfferID]
	layers := world.images[image].Layers
	fetchedLayers, bytes := state.missing(image, layers)
	fetched := fetchedNames(image, state.heldImages[domain.ReferenceDigest(image)], fetchedLayers)
	completesAt := world.now.Add(transferDuration(bytes, state.offer.RegistryDownloadMbps()))
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
			image:       image,
			layers:      layers,
			fetched:     fetched,
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

func (world *simulatedWorld) fetchRunArtifacts(execution externalExecution, arrival RunArrival) {
	for _, artifactID := range arrival.Request.ConsumesArtifacts {
		replicas := world.replicas[artifactID]
		if replicas == nil || replicas[execution.OfferID] || !hasAnyReplica(replicas) {
			continue
		}
		replicas[execution.OfferID] = true
		world.recordEffect(
			OperationArtifactGet,
			"artifact-get/"+execution.RunID+"/"+artifactID,
			EffectCommandAccepted,
			EffectResponseDelivered,
			execution.RunID,
			execution.LaunchKey,
			"",
			map[string]any{"artifact_id": artifactID, "offer_id": execution.OfferID},
			map[string]any{"replica_created": true, "size_bytes": world.artifacts[artifactID]},
			"",
		)
	}
}

func (world *simulatedWorld) storeRunOutputs(execution externalExecution) {
	arrival := world.runs[execution.RunID]
	for _, artifactID := range arrival.Request.ProducesArtifacts {
		replicas := world.replicas[artifactID]
		if replicas == nil {
			continue
		}
		created := !replicas[execution.OfferID]
		replicas[execution.OfferID] = true
		world.recordEffect(
			OperationArtifactPut,
			"artifact-put/"+execution.RunID+"/"+artifactID,
			EffectCommandAccepted,
			EffectResponseDelivered,
			execution.RunID,
			execution.LaunchKey,
			"",
			map[string]any{"artifact_id": artifactID, "offer_id": execution.OfferID},
			map[string]any{"replica_created": created, "size_bytes": world.artifacts[artifactID]},
			"",
		)
	}
	for _, mount := range arrival.Request.CacheMounts {
		if world.cacheMounts[execution.OfferID] == nil {
			world.cacheMounts[execution.OfferID] = map[string]uint64{}
		}
		world.cacheMounts[execution.OfferID][mount.Name]++
		world.recordEffect(
			OperationCacheMountWrite,
			"cache-mount-write/"+execution.RunID+"/"+mount.Name,
			EffectCommandAccepted,
			EffectResponseDelivered,
			execution.RunID,
			execution.LaunchKey,
			"",
			map[string]any{"name": mount.Name, "offer_id": execution.OfferID},
			map[string]any{"revision": world.cacheMounts[execution.OfferID][mount.Name]},
			"",
		)
	}
}

func (world *simulatedWorld) artifactReplicas() []ArtifactReplica {
	var replicas []ArtifactReplica
	for artifactID, offers := range world.replicas {
		for offerID, present := range offers {
			if !present {
				continue
			}
			replicas = append(replicas, ArtifactReplica{
				ArtifactID: artifactID,
				OfferID:    offerID,
				SizeBytes:  world.artifacts[artifactID],
			})
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

func (world *simulatedWorld) cacheMountStates() []CacheMountState {
	var mounts []CacheMountState
	for offerID, revisions := range world.cacheMounts {
		for name, revision := range revisions {
			mounts = append(mounts, CacheMountState{
				OfferID:  offerID,
				Name:     name,
				Revision: revision,
			})
		}
	}
	sort.Slice(mounts, func(i, j int) bool {
		if mounts[i].OfferID == mounts[j].OfferID {
			return mounts[i].Name < mounts[j].Name
		}
		return mounts[i].OfferID < mounts[j].OfferID
	})
	return mounts
}

func hasAnyReplica(replicas map[string]bool) bool {
	for _, present := range replicas {
		if present {
			return true
		}
	}
	return false
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

func (world *simulatedWorld) recordLaunchEffect(request adapter.LaunchRequest, command EffectCommand, response EffectResponse, consequence any, faultID string) {
	if receipt, ok := consequence.(adapter.LaunchReceipt); ok {
		execution := world.executions[receipt.LaunchKey]
		consequence = map[string]any{
			"external_id": receipt.ExternalID,
			"launch_key":  receipt.LaunchKey,
			"phase":       receipt.Phase,
			"accepted_at": receipt.AcceptedAt,
			"duplicate":   receipt.Duplicate,
			// The two actuals a prediction is calibrated against: how long the
			// container waited for its image, and how long it then ran.
			"start_latency_seconds":  execution.StartedAt.Sub(execution.AcceptedAt).Seconds(),
			"actual_runtime_seconds": execution.CompletesAt.Sub(execution.StartedAt).Seconds(),
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

func labOffer(id string, kind domain.OfferKind, lane domain.ExecutionLane, ratePerHourUSD float64, resources *scenario.ResourcesSpec) domain.OfferSnapshot {
	offer := domain.OfferSnapshot{
		ID:           id,
		ConnectionID: labConnection,
		AdapterType:  "lab",
		NativeRef:    id,
		Kind:         kind,
		Lane:         lane,
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources:    labResources(resources),
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

func labResources(resources *scenario.ResourcesSpec) domain.ResourceInventory {
	inventory := domain.ResourceInventory{
		CPUMillis:          defaultLabCPUMillis,
		MemoryBytes:        defaultLabMemoryBytes,
		EphemeralDiskBytes: defaultLabDiskBytes,
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
	return inventory
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
		verifiedAt:     state.verifiedAt,
		validUntil:     state.validUntil,
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
