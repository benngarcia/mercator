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
	// Candidates are ordered here exactly as they are in production: least
	// dollars, then earliest ready, then the offer ID. What differs per Run is
	// what its class said a second of waiting is worth, and that is already in
	// each candidate's own score.
	sort.Slice(feasible, func(i, j int) bool { return feasible[i].Preferred(feasible[j]) })
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
	// A world this fleet has already measured is refused rather than modelled.
	// The generated cases this oracle grades are single decisions in worlds with
	// no launch behind them, so a history here would mean the generator started
	// producing a world the reference model has no independent account of, and
	// the two models would agree because one of them stopped having an opinion.
	if len(container.Ports) > 0 ||
		len(input.Workload.Spec.Resources.Accelerators) > 0 ||
		input.Workload.Spec.Network.Download != nil ||
		!input.History.Empty() {
		return fmt.Errorf("small-world oracle does not support ports, accelerators, network requirements, or a measured launch history")
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
	// A budget is not cleared by a candidate with no dollars to compare against it.
	if maximum := input.Workload.Spec.Placement.MaxExpectedCostUSD; maximum != nil {
		if estimates.CostUSD.Source == domain.CostUnpriced || estimates.CostUSD.Expected > *maximum {
			return false
		}
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

// referenceCapacityAvailable is this model's own answer to whether a machine may
// be used: capacity that says it is there, or a Rental with an open position in a
// schedule that can still say when it comes free. A schedule whose Booking is
// past the runtime Mercator enforces projects no wait at all, so a model that
// queued behind it would call an occupied machine immediately available and
// disagree with production about a machine neither of them can project.
func referenceCapacityAvailable(input scheduler.SchedulingInput, offer domain.OfferSnapshot) bool {
	if offer.Capacity.Available {
		return true
	}
	schedule, exists := input.Schedules[offer.RentalID]
	return offer.Kind == domain.OfferKindStanding &&
		exists &&
		len(schedule.Bookings) > 0 &&
		len(schedule.Bookings) <= domain.RentalScheduleQueueCapacity &&
		!schedule.Exhausted(input.EvaluatedAt)
}

// referenceCandidate is the reference model's own candidate record: enough of
// one to be ranked, and nothing else. It is stated against the same fields the
// production decision carries, so the two models compare the same quantities or
// disagree visibly.
//
// The confidences are the reference model's own, derived from its own estimates,
// which is what keeps the uncertainty term independent. Both models now count the
// same thing, the shortfall of the answers this candidate was scored on, and the
// definitions that drifted apart counted different things and agreed only because
// each was multiplied by zero.
func referenceCandidate(input scheduler.SchedulingInput, offer domain.OfferSnapshot) domain.CandidateDecision {
	estimates := referenceEstimates(input, offer)
	candidate := domain.CandidateDecision{
		OfferSnapshotID: offer.ID,
		Feasible:        true,
		Estimates:       estimates,
		// The rate every transfer was priced at, recorded by this model for the
		// reason the risk history below is: two models that price the same seconds
		// off different rates agree about the answer and disagree about why, and a
		// record only one of them keeps cannot catch it.
		TransferRates: referenceTransferRates(input, offer),
		Confidences:   referenceConfidences(offer, estimates),
		// The risk history this model was given, carried through unpriced and
		// undoubted. It is here so the two records hold the same answers: a term
		// added to one model and not the other is the drift an independent model
		// exists to catch.
		Reliability: offer.Reliability,
	}
	weights := input.Workload.Spec.Placement.Class.Weights()
	candidate.ScoreUSD = weights.ScoreUSD(candidate, input.Workload.Spec.Placement.ExpectedRuntimeSeconds)
	return candidate
}

// referenceConfidences is what this model says each of its own answers is worth.
// A published capacity confidence is worth what its publisher said; a transfer
// duration is worth what the reference content estimate concluded. The published
// risk history is worth nothing here, because this model prices no refusal and a
// doubt about an answer the score never reads is a charge for having answered.
func referenceConfidences(offer domain.OfferSnapshot, estimates domain.CandidateEstimates) []domain.Confidence {
	var stated []domain.Confidence
	for _, answer := range []domain.Confidence{
		{Answer: "capacity", Value: offer.Capacity.Confidence},
		{Answer: domain.StageImageFetch.ConfidenceAnswer(), Value: estimates.Stages.ImageFetch.Confidence},
		{Answer: domain.StageUnpack.ConfidenceAnswer(), Value: estimates.Stages.Unpack.Confidence},
		{Answer: domain.StageArtifactFetch.ConfidenceAnswer(), Value: estimates.Stages.ArtifactFetch.Confidence},
	} {
		if answer.Value > 0 {
			stated = append(stated, answer)
		}
	}
	return stated
}

// referenceTransferRates is this model's own account of what each stage that had
// bytes to move was priced at. It reads the paths the same way it prices them,
// and states nothing for a stage with nothing to move.
func referenceTransferRates(input scheduler.SchedulingInput, offer domain.OfferSnapshot) []domain.TransferRate {
	work, _ := input.Image.StartWork(offer.Images)
	fetchBytes, _ := domain.ArtifactFetchWork(input.Artifacts, offer.Artifacts)
	stated := []domain.TransferRate{
		domain.TransferRateFor(domain.StageImageFetch, domain.NetworkScopeRegistry, work.TransferBytes, offer.DownloadRate(domain.NetworkScopeRegistry, input.EvaluatedAt)),
		domain.TransferRateFor(domain.StageUnpack, "", work.UnpackBytes, domain.UnpackRate()),
		domain.TransferRateFor(domain.StageArtifactFetch, domain.NetworkScopeObjectStore, fetchBytes, offer.DownloadRate(domain.NetworkScopeObjectStore, input.EvaluatedAt)),
	}
	return slices.DeleteFunc(stated, func(rate domain.TransferRate) bool { return rate.Bytes == 0 })
}

// referenceEstimates is the reference model's own account of a candidate,
// including how much of its start prediction anybody established. It derives
// that the same way the scheduler does and from the same two published facts,
// which is what makes disagreeing about it a disagreement about the models
// rather than about which silences each one happened to notice.
func referenceEstimates(input scheduler.SchedulingInput, offer domain.OfferSnapshot) domain.CandidateEstimates {
	queue := referenceQueue(input, offer)
	work, locality := input.Image.StartWork(offer.Images)
	fetchBytes, evidence := domain.ArtifactFetchWork(input.Artifacts, offer.Artifacts)
	// The rate each transfer crosses is asked of the offer rather than assumed,
	// and asked per path: a machine beside the object store reads a dataset faster
	// than one across the country from it, and a model that priced both at one
	// constant would disagree with production about every machine that published a
	// measurement of its own.
	registry := offer.DownloadRate(domain.NetworkScopeRegistry, input.EvaluatedAt)
	store := offer.DownloadRate(domain.NetworkScopeObjectStore, input.EvaluatedAt)
	storage := domain.UnpackRate()
	imageFetch := referenceContent(
		referenceTransferSeconds(work.TransferBytes, registry.Mbps),
		referenceImageStageConfidence(work.TransferBytes, locality, registry.Confidence),
	)
	unpack := referenceContent(
		referenceUnpackSeconds(work.UnpackBytes, storage.Mbps),
		referenceImageStageConfidence(work.UnpackBytes, locality, storage.Confidence),
	)
	fetch := referenceContent(
		referenceObjectStoreSeconds(fetchBytes, store.Mbps),
		referenceArtifactConfidence(evidence, fetchBytes, store.Confidence),
	)
	establishedBytes := int64(0)
	for _, found := range evidence {
		if found.Locality != domain.LocalityUnknown {
			establishedBytes += found.FetchBytes
		}
	}
	establishedFetch := referenceContent(
		referenceObjectStoreSeconds(establishedBytes, store.Mbps),
		referenceArtifactConfidence(evidence, establishedBytes, store.Confidence),
	)
	stages := domain.LaunchStageEstimates{
		Boot:             referenceProvision(offer),
		ImageFetch:       imageFetch,
		Unpack:           unpack,
		ArtifactFetch:    fetch,
		ContainerStart:   referenceContainerStart(),
		ApplicationReady: referenceApplicationReady(input),
	}
	established := stages
	established.ArtifactFetch = establishedFetch
	// Content nobody could describe is priced and never established, which is
	// what stops a start bound striking a candidate out for a silence.
	if locality == domain.LocalityUnknown {
		established.ImageFetch = domain.Estimate{}
		established.Unpack = domain.Estimate{}
	}
	// So are seconds nobody measured the path of. A byte count an inventory
	// answered about exactly is still divided by the same prior every silent
	// machine is given, and a bound refusing capacity on that quotient refuses it
	// for a number nothing on the machine ever published.
	established.ImageFetch = referenceEstablished(established.ImageFetch, registry)
	established.Unpack = referenceEstablished(established.Unpack, storage)
	established.ArtifactFetch = referenceEstablished(established.ArtifactFetch, store)
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
		Stages:                  stages,
		StartSeconds:            referenceStart(queue, stages),
		EstablishedStartSeconds: referenceStart(queue, established),
		CostUSD:                 referenceCost(offer, billed),
	}
}

// referenceEstablished is this model's own reading of which half of a transfer
// prediction rests on somebody's measurement. Nothing to move is nothing to wait
// for whatever the path, and every other duration is only as established as the
// rate that produced it.
func referenceEstablished(estimate domain.Estimate, rate domain.LinkSpeed) domain.Estimate {
	if estimate.Expected > 0 && !rate.Measured() {
		return domain.Estimate{}
	}
	return estimate
}

// referenceCost is what this model says running here is billed at. It states the
// absence of a price the same way production does, because that absence is what
// the ranking reads: a model predicting zero dollars for a machine nobody quoted
// would call it the cheapest candidate in the world and agree with nothing.
func referenceCost(offer domain.OfferSnapshot, billedSeconds float64) domain.Estimate {
	if !offer.Pricing.Known {
		return domain.Estimate{Source: domain.CostUnpriced}
	}
	return domain.Estimate{Expected: offer.Pricing.SetupFeeUSD + offer.Pricing.RatePerSecondUSD*billedSeconds}
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

// referenceContent is what content that has to move costs, tail included, and
// what this model says that answer is worth. Half again as long is its own
// pessimism about a transfer, stated here rather than applied to the finished sum
// because a start's tail is made of each part's tail.
func referenceContent(seconds, confidence float64) domain.Estimate {
	return domain.Estimate{Expected: seconds, P50: seconds, P90: seconds * 1.5, Confidence: confidence}
}

// referenceImageStageConfidence is what this model thinks one of its own image
// answers is worth. Certainty belongs to a stage with nothing to do on a host
// that said what it holds. Bytes that have to move cross a rate somebody either
// measured or assumed, and bytes charged because a host said nothing are worth no
// more than an assumption whatever the rate is worth.
//
// A stage with nothing to do on a host nobody could describe gets no confidence
// at all, because the model stated no opinion rather than a doubtful one: the
// same nothing is charged to every candidate, so there is nothing to be uncertain
// between.
func referenceImageStageConfidence(bytes int64, locality domain.LocalityState, rateConfidence float64) float64 {
	if bytes == 0 {
		if locality == domain.LocalityUnknown {
			return 0
		}
		return 1
	}
	if locality == domain.LocalityUnknown {
		return min(rateConfidence, domain.AssumedLinkConfidence)
	}
	return rateConfidence
}

// referenceContainerStart is what this model says asking a container runtime for
// a process costs on a machine holding everything it needs.
func referenceContainerStart() domain.Estimate {
	return domain.Estimate{
		Expected: domain.AssumedContainerStartSeconds,
		P50:      domain.AssumedContainerStartSeconds,
		P90:      domain.AssumedContainerStartSeconds * 1.25,
	}
}

// referenceApplicationReady is what this model says the workload's own readiness
// costs, which is what the workload declared and nothing else. A model with a
// prior of its own here would disagree with production about every Run that
// declared nothing, for a reason that is not about either model.
func referenceApplicationReady(input scheduler.SchedulingInput) domain.Estimate {
	seconds := input.Workload.Spec.Placement.ExpectedReadySeconds
	return domain.Estimate{Expected: seconds, P50: seconds, P90: seconds}
}

// referenceArtifactConfidence is what this model thinks its own Artifact answer
// is worth. A Run that reads nothing is not a Run with a doubtful read, so it
// carries no confidence at all; a host that owes nothing is certain; and a read
// that has to happen is worth what the path it crosses is worth, which is a
// measurement on a host that published one and an assumption on a host that did
// not.
func referenceArtifactConfidence(evidence []domain.ArtifactEvidence, bytes int64, rateConfidence float64) float64 {
	switch {
	case len(evidence) == 0:
		return 0
	case bytes == 0:
		return 1
	default:
		return rateConfidence
	}
}

// referenceStart assembles a start out of the wait in front of a candidate and
// every stage before its process is running. Readiness is left out for the reason
// production leaves it out: the actual a start is calibrated against is the
// container's own start moment, and readiness happens after it.
func referenceStart(queue domain.Estimate, stages domain.LaunchStageEstimates) domain.Estimate {
	start := queue
	start.Confidence, start.Source, start.SampleCount = 0, "", 0
	for _, stage := range domain.LaunchStages {
		if stage == domain.StageApplicationReady {
			continue
		}
		part := stages.Stage(stage)
		start.Expected += part.Expected
		start.P50 += part.P50
		start.P90 += part.P90
	}
	return start
}

// referenceObjectStoreSeconds is the reference model's own account of reading a
// Run's declared inputs out of the object store, over the path this host reaches
// it on. It carries no fixed overhead because nothing has measured one: the only
// honest terms are the bytes and the rate they cross.
func referenceObjectStoreSeconds(bytes int64, mbps float64) float64 {
	return float64(bytes*8) / 1_000_000 / mbps
}

// referenceUnpackSeconds is the reference model's own account of turning bytes
// already on the disk into a layer chain. It is stated apart from the transfer
// above it because it is different work over a different resource, priced from a
// rate of its own.
func referenceUnpackSeconds(bytes int64, mbps float64) float64 {
	return float64(bytes*8) / 1_000_000 / mbps
}

// referenceTransferSeconds is the reference model's own account of bytes crossing
// a link onto this host, including the half second a transfer costs before any of
// them move. A host with nothing to fetch pays neither.
func referenceTransferSeconds(bytes int64, bandwidthMbps float64) float64 {
	if bytes == 0 {
		return 0
	}
	return float64(bytes*8)/1_000_000/bandwidthMbps + 0.5
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
