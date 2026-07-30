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
	type scoredOffer struct {
		id    string
		score float64
	}
	var feasible []scoredOffer
	for _, offer := range input.Offers {
		if !referenceFeasible(input, offer) {
			continue
		}
		feasible = append(feasible, scoredOffer{id: offer.ID, score: referenceScore(input, offer)})
	}
	sort.Slice(feasible, func(i, j int) bool {
		if feasible[i].score == feasible[j].score {
			return feasible[i].id < feasible[j].id
		}
		return feasible[i].score < feasible[j].score
	})
	decision := ReferenceDecision{FeasibleOfferIDs: make([]string, len(feasible))}
	for index, candidate := range feasible {
		decision.FeasibleOfferIDs[index] = candidate.id
	}
	sort.Strings(decision.FeasibleOfferIDs)
	if len(feasible) > 0 {
		decision.SelectedOfferID = feasible[0].id
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
		!offer.ImageCache.Known ||
		!offer.Pricing.Known && !input.Workload.Spec.Placement.AllowUnknownPricing {
		return false
	}
	required := input.Workload.Spec.Resources
	if offer.Resources.CPUMillis < required.CPU.MinMillis ||
		offer.Resources.MemoryBytes < required.Memory.MinBytes ||
		offer.Resources.EphemeralDiskBytes < required.EphemeralDisk.MinBytes {
		return false
	}
	estimates := referenceEstimates(input, offer)
	if maximum := input.Workload.Spec.Placement.MaxExpectedCostUSD; maximum != nil && estimates.CostUSD.Expected > *maximum {
		return false
	}
	if maximum := input.Workload.Spec.Placement.MaxP90StartSeconds; maximum > 0 && estimates.StartSeconds.P90 > maximum {
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

func referenceScore(input scheduler.SchedulingInput, offer domain.OfferSnapshot) float64 {
	estimates := referenceEstimates(input, offer)
	weights := input.Weights
	if weights.StartLatencyUSDPerSecond == 0 && input.Workload.Spec.Placement.Objective == domain.ObjectiveBalanced {
		weights.StartLatencyUSDPerSecond = 0.0005
	}
	score := estimates.CostUSD.Expected +
		weights.StartLatencyUSDPerSecond*estimates.StartSeconds.Expected +
		weights.CompletionLatencyUSDPerSecond*(estimates.StartSeconds.Expected+input.Workload.Spec.Placement.ExpectedRuntimeSeconds) +
		weights.StartFailurePenaltyUSD*offer.Reliability.StartFailureRate +
		weights.InterruptionPenaltyUSD*offer.Reliability.InterruptionRate +
		weights.UncertaintyPenaltyUSD*referenceUncertainty(offer)
	return math.Round(score*1_000_000) / 1_000_000
}

func referenceEstimates(input scheduler.SchedulingInput, offer domain.OfferSnapshot) domain.CandidateEstimates {
	queue := 0.0
	if schedule, exists := input.Schedules[offer.RentalID]; exists {
		queue = schedule.ExpectedWaitSeconds()
	} else if offer.Queue != nil {
		queue = offer.Queue.QueuedWorkSeconds
	}
	provision := 0.0
	if offer.Kind == domain.OfferKindProvisionable && offer.Provisioning != nil {
		provision = offer.Provisioning.Expected
	}
	pull := referenceTransferSeconds(offer.ImageCache.MissingBytes, registryBandwidth(offer))
	start := queue + provision + pull + 1
	runtime := input.Workload.Spec.Placement.ExpectedRuntimeSeconds
	if runtime <= 0 {
		runtime = float64(input.Workload.Spec.Execution.MaxRuntimeSeconds)
	}
	if runtime <= 0 {
		runtime = 1
	}
	billed := math.Max(runtime, float64(offer.Pricing.MinimumChargeSeconds))
	return domain.CandidateEstimates{
		QueueSeconds:     domain.Estimate{Expected: queue},
		ProvisionSeconds: domain.Estimate{Expected: provision},
		PullSeconds:      domain.Estimate{Expected: pull},
		StartSeconds:     domain.Estimate{Expected: start, P90: start * 1.25},
		CostUSD:          domain.Estimate{Expected: offer.Pricing.SetupFeeUSD + offer.Pricing.RatePerSecondUSD*billed},
	}
}

func registryBandwidth(offer domain.OfferSnapshot) float64 {
	for _, fact := range offer.Network.Download {
		if fact.Scope == domain.NetworkScopeRegistry && fact.Statistic == "p10" && fact.ValueMbps > 0 {
			return fact.ValueMbps
		}
	}
	return 500
}

func referenceTransferSeconds(bytes int64, bandwidthMbps float64) float64 {
	if bytes <= 0 {
		return 0
	}
	return float64(bytes*8)/1_000_000/bandwidthMbps + 0.5
}

func referenceUncertainty(offer domain.OfferSnapshot) float64 {
	penalty := 0.0
	if offer.Capacity.Confidence > 0 && offer.Capacity.Confidence < 1 {
		penalty += 1 - offer.Capacity.Confidence
	}
	if offer.Reliability.Confidence > 0 && offer.Reliability.Confidence < 1 {
		penalty += 1 - offer.Reliability.Confidence
	}
	if !offer.ImageCache.Known {
		penalty++
	}
	if !offer.Pricing.Known {
		penalty++
	}
	return penalty
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

func CheckWarmingDoesNotIncreaseMissingBytes(before, after domain.OfferSnapshot) error {
	if after.ImageCache.MissingBytes > before.ImageCache.MissingBytes {
		return fmt.Errorf("warming increased missing bytes from %d to %d", before.ImageCache.MissingBytes, after.ImageCache.MissingBytes)
	}
	return nil
}

func CheckReducedBandwidthDoesNotReduceTransferDuration(bytes int64, fasterMbps, slowerMbps float64) error {
	if bytes <= 0 || fasterMbps <= slowerMbps || slowerMbps <= 0 {
		return fmt.Errorf("bandwidth metamorphism requires positive bytes and faster > slower > 0")
	}
	if referenceTransferSeconds(bytes, slowerMbps) < referenceTransferSeconds(bytes, fasterMbps) {
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
