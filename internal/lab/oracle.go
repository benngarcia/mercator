package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scheduler"
)

type ReferenceDecision struct {
	FeasibleOfferIDs []string `json:"feasible_offer_ids"`
	SelectedOfferID  string   `json:"selected_offer_id,omitempty"`
}

// SolveSmallWorld exhaustively evaluates the deliberately bounded scheduler
// subset used by generated oracle cases. Unsupported policy dimensions fail
// loudly instead of silently borrowing production scheduler behavior.
func SolveSmallWorld(input scheduler.SchedulingInput) (ReferenceDecision, error) {
	if err := validateSmallWorld(input); err != nil {
		return ReferenceDecision{}, err
	}
	var feasible []domain.CandidateDecision
	for _, offer := range input.Offers {
		if !referenceFeasible(input, offer) {
			continue
		}
		feasible = append(feasible, referenceCandidate(input, offer))
	}
	// The Run's objective orders the candidates here exactly as it does in
	// production, because which quantity a Run asked for the least of is a
	// statement about the placement and not about either model's arithmetic.
	policy := input.Workload.Spec.Placement
	sort.Slice(feasible, func(i, j int) bool { return policy.Prefers(feasible[i], feasible[j]) })
	decision := ReferenceDecision{FeasibleOfferIDs: make([]string, len(feasible))}
	for index, candidate := range feasible {
		decision.FeasibleOfferIDs[index] = candidate.OfferSnapshotID
	}
	sort.Strings(decision.FeasibleOfferIDs)
	if len(feasible) > 0 {
		decision.SelectedOfferID = feasible[0].OfferSnapshotID
	}
	return decision, nil
}

func validateSmallWorld(input scheduler.SchedulingInput) error {
	if input.EvaluatedAt.IsZero() || len(input.Workload.Spec.Containers) != 1 {
		return fmt.Errorf("small-world oracle requires evaluated_at and exactly one container")
	}
	container := input.Workload.Spec.Containers[0]
	if len(container.Ports) > 0 ||
		len(input.Workload.Spec.Resources.Accelerators) > 0 ||
		input.Workload.Spec.Network.Download != nil ||
		len(input.LatencyEstimates) > 0 {
		return fmt.Errorf("small-world oracle does not support ports, accelerators, network requirements, or measured latency overrides")
	}
	return nil
}

func referenceFeasible(input scheduler.SchedulingInput, offer domain.OfferSnapshot) bool {
	container := input.Workload.Spec.Containers[0]
	if slices.Contains(input.ExcludedOfferSnapshotIDs, offer.ID) ||
		!offer.ExpiresAt.IsZero() && !offer.ExpiresAt.After(input.EvaluatedAt) ||
		offer.Platform != container.Platform ||
		offer.Capabilities.Container.MaxContainers < 1 ||
		!offer.Capabilities.Container.SupportsDigestRefs ||
		container.Entrypoint != nil && !offer.Capabilities.Container.SupportsEntrypointOverride ||
		!referenceCapacityAvailable(input, offer) ||
		referenceQueueFull(input, offer) ||
		!offer.Pricing.Known && !input.Workload.Spec.Placement.AllowUnknownPricing {
		return false
	}
	required := input.Workload.Spec.Resources
	if offer.Resources.CPUMillis < required.CPU.MinMillis ||
		offer.Resources.MemoryBytes < required.Memory.MinBytes {
		return false
	}
	// The room a Run reserved and the room its content takes are one question
	// about one resource. The reference model asks it because production does: a
	// model blind to the disk would call a machine with nowhere to put the
	// dataset the warmest candidate in the world and disagree about the winner
	// for a reason belonging to neither model.
	if !referenceDisk(input, offer).Fits() {
		return false
	}
	estimates := referenceEstimates(input, offer)
	if maximum := input.Workload.Spec.Placement.MaxExpectedCostUSD; maximum != nil && estimates.CostUSD.Expected > *maximum {
		return false
	}
	// Only a candidate KNOWN to start late fails the latency SLO, so the bound
	// is asked of the established part of the prediction. Seconds priced out of
	// a silence are a guess, and the goal is explicit that silence is
	// uncertainty to price and never a hard constraint; seconds of queue and
	// provisioning are facts the offer stated, and those still bind.
	if maximum := input.Workload.Spec.Placement.MaxP90StartSeconds; maximum > 0 &&
		estimates.EstablishedStartSeconds.P90 > maximum {
		return false
	}
	return true
}

func referenceQueueFull(input scheduler.SchedulingInput, offer domain.OfferSnapshot) bool {
	schedule, exists := input.Schedules[offer.RentalID]
	return exists && len(schedule.Bookings) >= domain.RentalScheduleQueueCapacity+1
}

func referenceCapacityAvailable(input scheduler.SchedulingInput, offer domain.OfferSnapshot) bool {
	if offer.Capacity.Available {
		return true
	}
	schedule, exists := input.Schedules[offer.RentalID]
	return offer.Kind == domain.OfferKindStanding &&
		exists &&
		len(schedule.Bookings) > 0 &&
		len(schedule.Bookings) < domain.RentalScheduleQueueCapacity+1
}

// referenceCandidate is the reference model's own candidate record: enough of
// one for the Run's objective to rank it, and nothing else. Ranking is stated
// against the same fields the production decision carries, so the two models
// compare the same quantities or disagree visibly.
func referenceCandidate(input scheduler.SchedulingInput, offer domain.OfferSnapshot) domain.CandidateDecision {
	return domain.CandidateDecision{
		OfferSnapshotID: offer.ID,
		Estimates:       referenceEstimates(input, offer),
		ScoreUSD:        referenceScore(input, offer),
	}
}

func referenceScore(input scheduler.SchedulingInput, offer domain.OfferSnapshot) float64 {
	estimates := referenceEstimates(input, offer)
	weights := input.Weights
	if weights.StartLatencyUSDPerSecond == 0 && input.Workload.Spec.Placement.Objective == domain.ObjectiveBalanced {
		weights.StartLatencyUSDPerSecond = domain.BalancedWaitingUSDPerSecond
	}
	score := estimates.CostUSD.Expected +
		weights.StartLatencyUSDPerSecond*estimates.StartSeconds.Expected +
		weights.CompletionLatencyUSDPerSecond*(estimates.StartSeconds.Expected+input.Workload.Spec.Placement.ExpectedRuntimeSeconds) +
		weights.StartFailurePenaltyUSD*offer.Reliability.StartFailureRate +
		weights.InterruptionPenaltyUSD*offer.Reliability.InterruptionRate +
		weights.UncertaintyPenaltyUSD*offer.UncertaintyPenalty()
	return math.Round(score*1_000_000) / 1_000_000
}

// referenceEstimates is the reference model's own account of a candidate,
// including how much of its start prediction anybody established. It derives
// that the same way the scheduler does and from the same two published facts,
// which is what makes disagreeing about it a disagreement about the models
// rather than about which silences each one happened to notice.
func referenceEstimates(input scheduler.SchedulingInput, offer domain.OfferSnapshot) domain.CandidateEstimates {
	queue := referenceQueue(input, offer)
	provision := referenceProvision(offer)
	work, locality := input.Image.StartWork(offer.Images)
	fetchBytes, evidence := domain.ArtifactFetchWork(input.Artifacts, offer.Artifacts)
	pull := referenceContent(referenceStartWorkSeconds(work, offer.RegistryDownloadMbps()))
	fetch := referenceContent(referenceObjectStoreSeconds(fetchBytes))
	establishedPull := domain.Estimate{}
	if locality != domain.LocalityUnknown {
		establishedPull = pull
	}
	establishedBytes := int64(0)
	for _, found := range evidence {
		if found.Locality != domain.LocalityUnknown {
			establishedBytes += found.FetchBytes
		}
	}
	establishedFetch := referenceContent(referenceObjectStoreSeconds(establishedBytes))
	runtime := input.Workload.Spec.Placement.ExpectedRuntimeSeconds
	if runtime <= 0 {
		runtime = float64(input.Workload.Spec.Execution.MaxRuntimeSeconds)
	}
	if runtime <= 0 {
		runtime = 1
	}
	billed := math.Max(runtime, float64(offer.Pricing.MinimumChargeSeconds))
	return domain.CandidateEstimates{
		QueueSeconds:            queue,
		ProvisionSeconds:        provision,
		PullSeconds:             pull,
		ArtifactSeconds:         fetch,
		StartSeconds:            referenceStart(queue, provision, pull, fetch),
		EstablishedStartSeconds: referenceStart(queue, provision, establishedPull, establishedFetch),
		CostUSD:                 domain.Estimate{Expected: offer.Pricing.SetupFeeUSD + offer.Pricing.RatePerSecondUSD*billed},
	}
}

// referenceDisk is the reference model's own account of what this Run asks of
// this candidate's disk. It derives every part from the same published facts the
// scheduler reads, and asks the domain the same question, because a machine that
// cannot hold the work is not a machine that costs more: nothing this Run could
// give up frees a byte it does not need straight back.
func referenceDisk(input scheduler.SchedulingInput, offer domain.OfferSnapshot) domain.DiskDemand {
	work, locality := input.Image.StartWork(offer.Images)
	fetchBytes, evidence := domain.ArtifactFetchWork(input.Artifacts, offer.Artifacts)
	caches := domain.CacheLandBytes(input.Workload.WorkspaceID, input.Workload.Spec.Caches, offer.Caches)
	established := int64(0)
	for _, found := range evidence {
		if found.Locality != domain.LocalityUnknown {
			established += found.FetchBytes
		}
	}
	if locality != domain.LocalityUnknown {
		established += work.TransferBytes
	}
	if offer.Caches.Known {
		established += caches
	}
	return domain.DiskDemand{
		FreeBytes:            offer.Resources.EphemeralDiskBytes,
		ReservedBytes:        input.Workload.Spec.Resources.EphemeralDisk.MinBytes,
		LandBytes:            work.TransferBytes + fetchBytes + caches,
		EstablishedLandBytes: established,
	}
}

// referenceQueue is how long this reference model says work arriving now waits.
// A Rental Schedule is asked as of the evaluation moment, because the wait is a
// projection from where its Bookings are and not a restatement of what their
// callers declared.
func referenceQueue(input scheduler.SchedulingInput, offer domain.OfferSnapshot) domain.Estimate {
	seconds := 0.0
	if schedule, exists := input.Schedules[offer.RentalID]; exists {
		seconds = schedule.ExpectedWaitSeconds(input.EvaluatedAt)
	} else if offer.Queue != nil {
		seconds = offer.Queue.QueuedWorkSeconds
	}
	return domain.Estimate{Expected: seconds, P50: seconds, P90: seconds}
}

// referenceProvision is what the provider published about bringing this machine
// up, including its own tail. A quantile the provider left unstated is its
// expectation restated rather than a spread of this model's invention.
func referenceProvision(offer domain.OfferSnapshot) domain.Estimate {
	if offer.Kind != domain.OfferKindProvisionable || offer.Provisioning == nil {
		return domain.Estimate{}
	}
	published := *offer.Provisioning
	estimate := domain.Estimate{Expected: published.Expected, P50: published.Expected, P90: published.Expected}
	if published.P50 > 0 {
		estimate.P50 = published.P50
	}
	if published.P90 > 0 {
		estimate.P90 = published.P90
	}
	return estimate
}

// referenceContent is what content that has to move costs, tail included. Half
// again as long is this model's own pessimism about a transfer, and it is stated
// here rather than applied to the finished sum because a start's tail is made of
// each part's tail.
func referenceContent(seconds float64) domain.Estimate {
	return domain.Estimate{Expected: seconds, P50: seconds, P90: seconds * 1.5}
}

// referenceStart assembles a start out of the parts a candidate waits on, plus
// the second a launch costs whatever it holds.
func referenceStart(parts ...domain.Estimate) domain.Estimate {
	start := domain.Estimate{
		Expected: domain.LaunchSeconds,
		P50:      domain.LaunchSeconds,
		P90:      domain.LaunchSeconds * 1.25,
	}
	for _, part := range parts {
		start.Expected += part.Expected
		start.P50 += part.P50
		start.P90 += part.P90
	}
	return start
}

// referenceObjectStoreSeconds is the reference model's own account of reading a
// Run's declared inputs out of the object store. It carries no fixed overhead
// because nothing has measured one: the only honest terms are the bytes and the
// assumed rate they cross.
func referenceObjectStoreSeconds(bytes int64) float64 {
	return float64(bytes*8) / 1_000_000 / domain.DefaultObjectStoreDownloadMbps
}

// referenceStartWorkSeconds is the reference model's own account of how long a
// candidate is from starting: bytes over the wire, plus bytes already here that
// still have to be unpacked. Fetching and unpacking are separate work over
// separate resources, so an independent model that folded them together would
// disagree with the scheduler about every half-assembled host for a reason that
// has nothing to do with either model.
func referenceStartWorkSeconds(work domain.ImageWork, bandwidthMbps float64) float64 {
	if work.None() {
		return 0
	}
	return float64(work.TransferBytes*8)/1_000_000/bandwidthMbps +
		float64(work.UnpackBytes)/1_000_000/domain.AssumedUnpackMBps + 0.5
}

func CheckOfferOrderIndependence(ctx context.Context, production scheduler.Scheduler, input scheduler.SchedulingInput) error {
	first, err := production.Evaluate(ctx, input)
	if err != nil {
		return err
	}
	reversed := input
	reversed.Offers = slices.Clone(input.Offers)
	slices.Reverse(reversed.Offers)
	second, err := production.Evaluate(ctx, reversed)
	if err != nil {
		return err
	}
	if first.SelectedOfferSnapshotID != second.SelectedOfferSnapshotID {
		return fmt.Errorf("offer order changed winner from %q to %q", first.SelectedOfferSnapshotID, second.SelectedOfferSnapshotID)
	}
	return nil
}

func CheckDominatedOfferDoesNotChangeWinner(ctx context.Context, production scheduler.Scheduler, input scheduler.SchedulingInput, dominated domain.OfferSnapshot) error {
	before, err := production.Evaluate(ctx, input)
	if err != nil {
		return err
	}
	withDominated := input
	withDominated.Offers = append(slices.Clone(input.Offers), dominated)
	after, err := production.Evaluate(ctx, withDominated)
	if err != nil {
		return err
	}
	if before.SelectedOfferSnapshotID != after.SelectedOfferSnapshotID {
		return fmt.Errorf("dominated offer changed winner from %q to %q", before.SelectedOfferSnapshotID, after.SelectedOfferSnapshotID)
	}
	return nil
}

// CheckWarmingDoesNotShrinkInventory is the metamorphic law that warming is
// monotone: a host that pulled content still holds what it held before. It
// reads the inventory rather than a missing-byte count, because what is missing
// depends on which image is being asked about and what is held does not.
func CheckWarmingDoesNotShrinkInventory(before, after domain.OfferSnapshot) error {
	if before.Images.Known && !after.Images.Known {
		return fmt.Errorf("warming made a host that could enumerate its content stop being able to")
	}
	for _, layer := range before.Images.LayerDigests {
		if !after.Images.HoldsLayer(domain.ImageLayer{Digest: layer}) {
			return fmt.Errorf("warming lost layer %s the host already held", layer)
		}
	}
	for _, diffID := range before.Images.LayerDiffIDs {
		if !after.Images.HoldsLayer(domain.ImageLayer{DiffID: diffID}) {
			return fmt.Errorf("warming lost layer %s the host already held", diffID)
		}
	}
	for _, image := range before.Images.ImageDigests {
		if !after.Images.Holds(image) {
			return fmt.Errorf("warming lost image %s the host already held", image)
		}
	}
	return nil
}

func CheckReducedBandwidthDoesNotReduceTransferDuration(bytes int64, fasterMbps, slowerMbps float64) error {
	if bytes <= 0 || fasterMbps <= slowerMbps || slowerMbps <= 0 {
		return fmt.Errorf("bandwidth metamorphism requires positive bytes and faster > slower > 0")
	}
	transfer := domain.ImageWork{TransferBytes: bytes}
	if referenceStartWorkSeconds(transfer, slowerMbps) < referenceStartWorkSeconds(transfer, fasterMbps) {
		return fmt.Errorf("reducing bandwidth reduced transfer duration")
	}
	return nil
}

func CheckDuplicateMessagesDoNotDuplicateEffects(original, duplicated []EffectRecord) error {
	if !equalAcceptedConsequences(original, duplicated) {
		return fmt.Errorf("duplicate delivery changed accepted external consequences")
	}
	return nil
}

func equalAcceptedConsequences(left, right []EffectRecord) bool {
	project := func(effects []EffectRecord) []string {
		var consequences []string
		for _, effect := range effects {
			if effect.Command == EffectCommandAccepted && effectMutatesWorld(effect.Operation) {
				consequences = append(consequences, effect.Operation+"/"+effect.OperationID+"/"+string(effect.Consequence))
			}
		}
		sort.Strings(consequences)
		return consequences
	}
	return slices.Equal(project(left), project(right))
}

type terminalSemantics struct {
	Runs             []domain.RunRecord
	ArtifactReplicas []ArtifactReplica
	CacheMounts      []CacheMountState
}

func CheckRestartPreservesTerminalBehavior(ctx context.Context, config Config, boundary int) error {
	if boundary < 0 || boundary > len(config.Tape.Events) {
		return fmt.Errorf("restart boundary %d is outside 0..%d", boundary, len(config.Tape.Events))
	}
	baseline, err := runToTerminal(ctx, config, -1)
	if err != nil {
		return err
	}
	restarted, err := runToTerminal(ctx, config, boundary)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(baseline.Runs, restarted.Runs) ||
		!slices.Equal(baseline.ArtifactReplicas, restarted.ArtifactReplicas) ||
		!slices.Equal(baseline.CacheMounts, restarted.CacheMounts) {
		return fmt.Errorf("restart at event boundary %d changed terminal behavior", boundary)
	}
	return nil
}

func runToTerminal(ctx context.Context, config Config, restartBoundary int) (terminalSemantics, error) {
	execution, err := Open(ctx, config)
	if err != nil {
		return terminalSemantics{}, err
	}
	defer func() { _ = execution.Close() }()
	for index := 0; index < restartBoundary; index++ {
		if _, err := execution.Drive(ctx, Step()); err != nil {
			return terminalSemantics{}, err
		}
	}
	if restartBoundary >= 0 {
		if err := execution.Restart(ctx); err != nil {
			return terminalSemantics{}, err
		}
	}
	if _, err := execution.Drive(ctx, Quiesce()); err != nil {
		return terminalSemantics{}, err
	}
	for _, event := range config.Tape.Events {
		var arrival RunArrival
		if err := json.Unmarshal(event.Data, &arrival); err != nil {
			return terminalSemantics{}, err
		}
		if _, err := execution.Drive(ctx, Advance(arrival.ActualRuntime.Duration()+time.Nanosecond)); err != nil {
			return terminalSemantics{}, err
		}
	}
	runs, err := execution.runtime.allRuns(ctx)
	if err != nil {
		return terminalSemantics{}, err
	}
	truth := execution.runtime.world.truthSnapshot()
	return terminalSemantics{
		Runs:             runs,
		ArtifactReplicas: truth.ArtifactReplicas,
		CacheMounts:      truth.CacheMounts,
	}, nil
}

func CheckProjectionRebuildEquivalence(ctx context.Context, execution *Execution) error {
	if execution == nil || execution.runtime == nil {
		return fmt.Errorf("projection rebuild metamorphism requires a real Lab control plane")
	}
	observation, err := execution.runtime.invariantObservation(ctx, execution.config.Tape, execution.transitions)
	if err != nil {
		return err
	}
	return projectionRebuildEquivalence(observation)
}
