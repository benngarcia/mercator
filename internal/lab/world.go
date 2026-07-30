package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/gpunorm"
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
	StartedAt      time.Time             `json:"started_at"`
	CompletesAt    time.Time             `json:"completes_at"`
	OutputsStored  bool                  `json:"outputs_stored"`
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

type observedOffer struct {
	offer      domain.OfferSnapshot
	heldLayers map[string]int64
}

type worldOperation struct {
	hash          string
	correlationID string
	receipt       any
}

type simulatedWorld struct {
	mu sync.Mutex

	seed      string
	now       time.Time
	images    map[string][]scenario.LayerSpec
	truth     map[string]observedOffer
	observed  map[string]observedOffer
	activeRun string
	runs      map[string]RunArrival
	artifacts map[string]int64
	replicas  map[string]map[string]bool
	// seededArtifacts are the Artifacts a Rental already held when the world was
	// built. They are available to a consuming Run without any producer having
	// published them, so invariants that order launches against publication
	// treat them as present from virtual time zero.
	seededArtifacts map[string]bool
	cacheMounts     map[string]map[string]uint64

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
		images:          make(map[string][]scenario.LayerSpec, len(tape.InitialWorld.Images)),
		truth:           map[string]observedOffer{},
		observed:        map[string]observedOffer{},
		runs:            map[string]RunArrival{},
		artifacts:       map[string]int64{},
		replicas:        map[string]map[string]bool{},
		seededArtifacts: map[string]bool{},
		cacheMounts:     map[string]map[string]uint64{},
		executions:      map[string]externalExecution{},
		operations:      map[string]worldOperation{},
		launchCount:     map[string]int{},
		faults:          slices.Clone(tape.Faults),
		usedFaults:      map[string]bool{},
	}
	for reference, image := range tape.InitialWorld.Images {
		world.images[reference] = slices.Clone(image.Layers)
	}
	for _, artifact := range tape.InitialWorld.Artifacts {
		world.artifacts[artifact.ID] = int64(artifact.Size)
		world.replicas[artifact.ID] = map[string]bool{}
	}
	for _, rental := range tape.InitialWorld.Rentals {
		state := observedOffer{
			offer:      labOffer(rental.ID, domain.OfferKindStanding, rental.RatePerHourUSD, rental.Resources),
			heldLayers: map[string]int64{},
		}
		applyOfferWorldFacts(&state.offer, tape.InitialWorld, rental.ID, nil, rental.Billing)
		for _, reference := range rental.CachedImages {
			for _, layer := range tape.InitialWorld.Images[reference].Layers {
				state.heldLayers[layer.Digest] = int64(layer.Size)
			}
		}
		for _, digest := range rental.CachedLayers {
			state.heldLayers[digest] = int64(layerBytes(tape.InitialWorld, digest))
		}
		world.truth[rental.ID] = cloneObservedOffer(state)
		world.observed[rental.ID] = cloneObservedOffer(state)
		for _, artifactID := range rental.ArtifactReplicas {
			world.replicas[artifactID][rental.ID] = true
			world.seededArtifacts[artifactID] = true
		}
		world.cacheMounts[rental.ID] = map[string]uint64{}
		for _, name := range rental.CacheMounts {
			world.cacheMounts[rental.ID][name] = 1
		}
	}
	for _, marketplace := range tape.InitialWorld.Marketplace {
		state := observedOffer{
			offer: labOffer(
				marketplace.ID,
				domain.OfferKindProvisionable,
				marketplace.RatePerHourUSD,
				marketplace.Resources,
			),
			heldLayers: map[string]int64{},
		}
		applyOfferWorldFacts(&state.offer, tape.InitialWorld, marketplace.ID, marketplace.Available, marketplace.Billing)
		state.offer.Provisioning = &domain.Estimate{
			Expected: marketplace.Provisioning.Expected.Duration().Seconds(),
			Source:   "lab-world",
		}
		if marketplace.Provisioning.P90 != nil {
			state.offer.Provisioning.P90 = marketplace.Provisioning.P90.Duration().Seconds()
		}
		world.truth[marketplace.ID] = cloneObservedOffer(state)
		world.observed[marketplace.ID] = cloneObservedOffer(state)
	}
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

func (world *simulatedWorld) setNow(now time.Time) {
	world.mu.Lock()
	defer world.mu.Unlock()
	world.now = now.UTC()
}

func (world *simulatedWorld) nowTime() time.Time {
	world.mu.Lock()
	defer world.mu.Unlock()
	return world.now
}

func (world *simulatedWorld) setTruthOfferAvailable(id string, available bool) {
	world.mu.Lock()
	defer world.mu.Unlock()
	state := world.truth[id]
	state.offer.Capacity = domain.CapacityEvidence{Available: available, Confidence: 1}
	world.truth[id] = state
}

func (world *simulatedWorld) deliverOfferObservation(id string) {
	world.mu.Lock()
	defer world.mu.Unlock()
	world.observed[id] = cloneObservedOffer(world.truth[id])
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
		Offers:           world.offerSnapshots(world.truth),
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

func (world *simulatedWorld) invariantFacts() (map[string]RunArrival, map[string]bool, map[string]bool) {
	world.mu.Lock()
	defer world.mu.Unlock()
	runs := make(map[string]RunArrival, len(world.runs))
	for runID, arrival := range world.runs {
		runs[runID] = arrival
	}
	artifacts := make(map[string]bool, len(world.artifacts))
	for artifactID := range world.artifacts {
		artifacts[artifactID] = true
	}
	seeded := make(map[string]bool, len(world.seededArtifacts))
	for artifactID := range world.seededArtifacts {
		seeded[artifactID] = true
	}
	return runs, artifacts, seeded
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
	return world.offerSnapshots(world.observed)
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
	offers := world.offerSnapshots(world.observed)
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
		StartedAt:      world.now,
		CompletesAt:    world.now.Add(actualRuntimeForOffer(arrival, request.SelectedOfferSnapshotID)),
	}
	world.fetchRunArtifacts(execution, arrival)
	world.executions[request.LaunchKey] = execution
	if offer.offer.Kind == domain.OfferKindStanding {
		offer.offer.Capacity = domain.CapacityEvidence{Available: false, Confidence: 1}
		world.truth[request.SelectedOfferSnapshotID] = offer
	}
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
		if offer := world.truth[execution.OfferID]; offer.offer.Kind == domain.OfferKindStanding {
			offer.offer.Capacity = domain.CapacityEvidence{Available: true, Confidence: 1}
			world.truth[execution.OfferID] = offer
		}
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

func (world *simulatedWorld) offerSnapshots(source map[string]observedOffer) []domain.OfferSnapshot {
	arrival := world.runs[world.activeRun]
	layers := world.images[arrival.Request.Image]
	offers := make([]domain.OfferSnapshot, 0, len(source))
	for _, state := range source {
		offer := state.offer
		offer.ObservedAt = world.now
		offer.ExpiresAt = world.now.Add(5 * time.Minute)
		missing := int64(0)
		for _, layer := range layers {
			if _, held := state.heldLayers[layer.Digest]; !held {
				missing += int64(layer.Size)
			}
		}
		offer.ImageCache = domain.ImageCacheEvidence{
			ManifestCached: missing == 0 && len(layers) > 0,
			MissingBytes:   missing,
			Known:          true,
		}
		offers = append(offers, offer)
	}
	sort.Slice(offers, func(i, j int) bool { return offers[i].ID < offers[j].ID })
	return offers
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
			"external_id":            receipt.ExternalID,
			"launch_key":             receipt.LaunchKey,
			"phase":                  receipt.Phase,
			"accepted_at":            receipt.AcceptedAt,
			"duplicate":              receipt.Duplicate,
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

func labOffer(id string, kind domain.OfferKind, ratePerHourUSD float64, resources *scenario.ResourcesSpec) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:           id,
		ConnectionID: labConnection,
		AdapterType:  "lab",
		NativeRef:    id,
		Kind:         kind,
		RentalID:     standingRentalID(id, kind),
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

func layerBytes(world scenario.WorldSpec, digest string) scenario.ByteSize {
	for _, image := range world.Images {
		for _, layer := range image.Layers {
			if layer.Digest == digest {
				return layer.Size
			}
		}
	}
	return 0
}

func standingRentalID(id string, kind domain.OfferKind) string {
	if kind == domain.OfferKindStanding {
		return id
	}
	return ""
}

func cloneObservedOffer(state observedOffer) observedOffer {
	return observedOffer{offer: state.offer, heldLayers: cloneMap(state.heldLayers)}
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
