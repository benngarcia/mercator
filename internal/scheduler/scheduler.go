package scheduler

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/gpunorm"
)

type Scheduler interface {
	Evaluate(ctx context.Context, input SchedulingInput) (domain.BookingDecision, error)
}

type SchedulingInput struct {
	RunID                    string
	Workload                 domain.WorkloadRevision
	Offers                   []domain.OfferSnapshot
	Schedules                map[string]domain.RentalSchedule
	ExcludedOfferSnapshotIDs []string
	ModelVersion             string
	EvaluatedAt              time.Time
	Weights                  ScoreWeights
	LatencyEstimates         map[string]domain.Estimate
}

type ScoreWeights struct {
	StartLatencyUSDPerSecond      float64
	CompletionLatencyUSDPerSecond float64
	StartFailurePenaltyUSD        float64
	InterruptionPenaltyUSD        float64
	UncertaintyPenaltyUSD         float64
}

type deterministicScheduler struct{}

func New() Scheduler {
	return deterministicScheduler{}
}

func (deterministicScheduler) Evaluate(_ context.Context, input SchedulingInput) (domain.BookingDecision, error) {
	if input.EvaluatedAt.IsZero() {
		return domain.BookingDecision{}, fmt.Errorf("scheduler: evaluated_at is required")
	}
	if input.ModelVersion == "" {
		input.ModelVersion = "latency-v1"
	}
	if input.Weights.StartLatencyUSDPerSecond == 0 && input.Workload.Spec.Placement.Objective == domain.ObjectiveBalanced {
		input.Weights.StartLatencyUSDPerSecond = 0.0005
	}

	decision := domain.BookingDecision{
		RunID:                  input.RunID,
		WorkloadRevisionDigest: input.Workload.Digest,
		EvaluatedAt:            input.EvaluatedAt.UTC(),
		ModelVersion:           input.ModelVersion,
		Policy:                 input.Workload.Spec.Placement,
		CollectionReport: domain.CollectionReport{
			ConnectionsQueried: connectionIDs(input.Offers),
		},
		Candidates: make([]domain.CandidateDecision, 0, len(input.Offers)),
	}

	bestIndex := -1
	offers := sortedOffers(input.Offers)
	for _, offer := range offers {
		candidate := evaluateOffer(input, offer)
		decision.Candidates = append(decision.Candidates, candidate)
		if !candidate.Feasible {
			continue
		}
		if bestIndex == -1 || candidate.ScoreUSD < decision.Candidates[bestIndex].ScoreUSD ||
			(candidate.ScoreUSD == decision.Candidates[bestIndex].ScoreUSD && candidate.OfferSnapshotID < decision.Candidates[bestIndex].OfferSnapshotID) {
			bestIndex = len(decision.Candidates) - 1
		}
	}
	if bestIndex >= 0 {
		decision.SelectedOfferSnapshotID = decision.Candidates[bestIndex].OfferSnapshotID
		decision.SelectionReasonCodes = []string{"FEASIBLE", "LOWEST_SCORE"}
		decision.SelectionReasonCodes = append(decision.SelectionReasonCodes, selectionReason(decision.Candidates[bestIndex].Disposition))
		if input.Workload.Spec.Placement.MaxP90StartSeconds > 0 {
			decision.SelectionReasonCodes = append(decision.SelectionReasonCodes, "WITHIN_START_SLO")
		}
	} else {
		decision.SelectionReasonCodes = []string{"NO_FEASIBLE_OFFERS"}
	}
	id, err := domain.CanonicalHash(struct {
		RunID       string
		Revision    string
		EvaluatedAt time.Time
		Model       string
		Candidates  []domain.CandidateDecision
		SelectedID  string
	}{input.RunID, input.Workload.Digest, input.EvaluatedAt.UTC(), input.ModelVersion, decision.Candidates, decision.SelectedOfferSnapshotID})
	if err != nil {
		return domain.BookingDecision{}, err
	}
	decision.ID = "dec_" + id[len("sha256:"):24]
	if bestIndex >= 0 {
		booking, err := bookingForDecision(input, decision.ID, offers[bestIndex])
		if err != nil {
			return domain.BookingDecision{}, err
		}
		decision.Booking = &booking
	}
	return decision, nil
}

func bookingForDecision(input SchedulingInput, decisionID string, offer domain.OfferSnapshot) (domain.Booking, error) {
	bookingHash, err := domain.CanonicalHash(struct {
		DecisionID string
		OfferID    string
	}{decisionID, offer.ID})
	if err != nil {
		return domain.Booking{}, err
	}
	bookingID := "bkg_" + bookingHash[len("sha256:"):24]
	rentalID := offer.RentalID
	schedule := domain.RentalSchedule{}
	switch offer.Kind {
	case domain.OfferKindStanding:
		if rentalID == "" {
			return domain.Booking{}, fmt.Errorf("scheduler: standing offer %q requires rental_id", offer.ID)
		}
		schedule = input.Schedules[rentalID]
		if schedule.RentalID == "" {
			schedule = domain.NewRentalSchedule(rentalID)
		}
	case domain.OfferKindProvisionable:
		rentalHash, hashErr := domain.CanonicalHash(struct {
			BookingID string
			OfferID   string
		}{bookingID, offer.ID})
		if hashErr != nil {
			return domain.Booking{}, hashErr
		}
		rentalID = "rnt_" + rentalHash[len("sha256:"):24]
		schedule = domain.NewRentalSchedule(rentalID)
	default:
		return domain.Booking{}, fmt.Errorf("scheduler: offer %q has unknown kind %q", offer.ID, offer.Kind)
	}
	expectedRuntime, maxRuntime := runtimeBounds(input.Workload)
	_, booking, err := schedule.Reserve(domain.BookingRequest{
		BookingID:              bookingID,
		RunID:                  input.RunID,
		ExpectedRuntimeSeconds: expectedRuntime,
		MaxRuntimeSeconds:      maxRuntime,
		ReservedAt:             input.EvaluatedAt,
	})
	return booking, err
}

func runtimeBounds(workload domain.WorkloadRevision) (float64, float64) {
	maxRuntime := float64(workload.Spec.Execution.MaxRuntimeSeconds)
	if maxRuntime <= 0 {
		maxRuntime = float64(domain.DefaultMaxRuntimeSeconds)
	}
	expectedRuntime := workload.Spec.Placement.ExpectedRuntimeSeconds
	if expectedRuntime <= 0 {
		expectedRuntime = maxRuntime
	}
	return expectedRuntime, maxRuntime
}

func evaluateOffer(input SchedulingInput, offer domain.OfferSnapshot) domain.CandidateDecision {
	rejections := feasibilityViolations(input, offer)
	estimates := estimateCandidate(input, offer)
	score := estimates.CostUSD.Expected +
		input.Weights.StartLatencyUSDPerSecond*estimates.StartSeconds.Expected +
		input.Weights.CompletionLatencyUSDPerSecond*(estimates.StartSeconds.Expected+input.Workload.Spec.Placement.ExpectedRuntimeSeconds) +
		input.Weights.StartFailurePenaltyUSD*offer.Reliability.StartFailureRate +
		input.Weights.InterruptionPenaltyUSD*offer.Reliability.InterruptionRate +
		input.Weights.UncertaintyPenaltyUSD*uncertaintyPenalty(offer)
	if len(rejections) > 0 {
		score = 0
	}
	return domain.CandidateDecision{
		OfferSnapshotID: offer.ID,
		ConnectionID:    offer.ConnectionID,
		AdapterType:     offer.AdapterType,
		NativeRef:       offer.NativeRef,
		Disposition:     candidateDisposition(input, offer),
		Feasible:        len(rejections) == 0,
		Rejections:      rejections,
		Estimates:       estimates,
		ScoreUSD:        round(score, 6),
	}
}

func feasibilityViolations(input SchedulingInput, offer domain.OfferSnapshot) []domain.Violation {
	var violations []domain.Violation
	workload := input.Workload
	container := workload.Spec.Containers[0]
	if slices.Contains(input.ExcludedOfferSnapshotIDs, offer.ID) {
		violations = append(violations, domain.Violation{
			Code:     "PREVIOUS_ATTEMPT_CAPACITY_UNAVAILABLE",
			Path:     "offer_snapshot_id",
			Required: "offer not rejected by an earlier attempt",
			Offered:  offer.ID,
			Message:  "Offer was rejected as unavailable by an earlier launch attempt.",
		})
	}
	if !offer.ExpiresAt.IsZero() && !offer.ExpiresAt.After(input.EvaluatedAt) {
		violations = append(violations, domain.Violation{Code: "OFFER_EXPIRED", Path: "expires_at", Required: "future", Offered: offer.ExpiresAt, Message: "Offer is expired and cannot be selected."})
	}
	if offer.Platform != container.Platform {
		violations = append(violations, domain.Violation{Code: "CAPABILITY_MISMATCH", Path: "platform", Required: container.Platform.String(), Offered: offer.Platform.String(), Message: "Offer platform does not match the workload platform."})
	}
	if offer.Capabilities.Container.MaxContainers == 0 {
		violations = append(violations, domain.Violation{Code: "UNKNOWN_FACT", Path: "container.max_containers", Required: len(workload.Spec.Containers), Offered: 0, Message: "Offer lacks a trustworthy container capacity limit."})
	}
	if offer.Capabilities.Container.MaxContainers > 0 && offer.Capabilities.Container.MaxContainers < len(workload.Spec.Containers) {
		violations = append(violations, domain.Violation{Code: "CAPABILITY_MISMATCH", Path: "container.max_containers", Required: len(workload.Spec.Containers), Offered: offer.Capabilities.Container.MaxContainers, Message: "Offer cannot run the required number of containers."})
	}
	if !offer.Capacity.Available && !queueable(input, offer) {
		violations = append(violations, domain.Violation{Code: "CAPACITY_UNAVAILABLE", Path: "capacity.available", Required: true, Offered: false, Message: "Offer capacity evidence says the capacity is not currently available."})
	}
	if schedule, ok := input.Schedules[offer.RentalID]; ok && len(schedule.Bookings) >= domain.RentalScheduleQueueCapacity+1 {
		violations = append(violations, domain.Violation{Code: "QUEUE_CAPACITY_EXCEEDED", Path: "rental_schedule.bookings", Required: domain.RentalScheduleQueueCapacity + 1, Offered: len(schedule.Bookings), Message: "Rental Schedule has no open Booking position."})
	}
	if !offer.Capabilities.Container.SupportsDigestRefs {
		violations = append(violations, domain.Violation{Code: "CAPABILITY_MISMATCH", Path: "container.supports_digest_refs", Required: true, Offered: false, Message: "Offer must support digest-pinned images."})
	}
	if container.Entrypoint != nil && !offer.Capabilities.Container.SupportsEntrypointOverride {
		violations = append(violations, domain.Violation{Code: "CAPABILITY_MISMATCH", Path: "container.supports_entrypoint_override", Required: true, Offered: false, Message: "Offer cannot override the image entrypoint."})
	}
	if offer.Resources.CPUMillis < workload.Spec.Resources.CPU.MinMillis {
		violations = append(violations, domain.Violation{Code: "RESOURCE_INSUFFICIENT", Path: "resources.cpu", Required: workload.Spec.Resources.CPU.MinMillis, Offered: offer.Resources.CPUMillis, Message: "Offer has insufficient CPU."})
	}
	if offer.Resources.MemoryBytes < workload.Spec.Resources.Memory.MinBytes {
		violations = append(violations, domain.Violation{Code: "RESOURCE_INSUFFICIENT", Path: "resources.memory", Required: workload.Spec.Resources.Memory.MinBytes, Offered: offer.Resources.MemoryBytes, Message: "Offer has insufficient memory."})
	}
	if offer.Resources.EphemeralDiskBytes < workload.Spec.Resources.EphemeralDisk.MinBytes {
		violations = append(violations, domain.Violation{Code: "RESOURCE_INSUFFICIENT", Path: "resources.ephemeral_disk", Required: workload.Spec.Resources.EphemeralDisk.MinBytes, Offered: offer.Resources.EphemeralDiskBytes, Message: "Offer has insufficient ephemeral disk."})
	}
	if !acceleratorRequirementsSatisfied(workload.Spec.Resources.Accelerators, offer) {
		violations = append(violations, domain.Violation{Code: "RESOURCE_INSUFFICIENT", Path: "resources.accelerators", Required: workload.Spec.Resources.Accelerators, Offered: offer.Resources.Accelerators, Message: "Offer has insufficient accelerator inventory."})
	}
	if requiresPublicInbound(container) && offer.Capabilities.Network.Inbound != domain.InboundNetworkPublicPort {
		violations = append(violations, domain.Violation{Code: "CAPABILITY_MISMATCH", Path: "network.inbound", Required: domain.InboundNetworkPublicPort, Offered: offer.Capabilities.Network.Inbound, Message: "Offer cannot expose inbound public ports."})
	}
	if !offer.ImageCache.Known {
		violations = append(violations, domain.Violation{Code: "UNKNOWN_FACT", Path: "image_cache", Required: "known", Offered: "unknown", Message: "Policy does not allow unknown image cache facts."})
	}
	if req := workload.Spec.Network.Download; req != nil {
		if !downloadRequirementSatisfied(input.EvaluatedAt, *req, offer.Network.Download) {
			code := "NETWORK_FACT_UNSATISFIED"
			if len(offer.Network.Download) == 0 && !req.AllowUnknown {
				code = "UNKNOWN_FACT"
			}
			violations = append(violations, domain.Violation{Code: code, Path: "network.download", Required: req.MinP10Mbps, Offered: "unknown_or_insufficient", Message: "Offer lacks a compatible registry download p10 fact."})
		}
	}
	if !offer.Pricing.Known && !workload.Spec.Placement.AllowUnknownPricing {
		violations = append(violations, domain.Violation{Code: "UNKNOWN_FACT", Path: "pricing", Required: "known", Offered: "unknown", Message: "Policy does not allow unknown pricing."})
	}
	estimates := estimateCandidate(input, offer)
	if workload.Spec.Placement.MaxP90StartSeconds > 0 && estimates.StartSeconds.P90 > workload.Spec.Placement.MaxP90StartSeconds {
		violations = append(violations, domain.Violation{Code: "LATENCY_SLO_EXCEEDED", Path: "placement.max_p90_start_seconds", Required: workload.Spec.Placement.MaxP90StartSeconds, Offered: estimates.StartSeconds.P90, Message: "Offer exceeds the requested p90 start latency."})
	}
	if workload.Spec.Placement.MaxExpectedCostUSD != nil && estimates.CostUSD.Expected > *workload.Spec.Placement.MaxExpectedCostUSD {
		violations = append(violations, domain.Violation{Code: "COST_LIMIT_EXCEEDED", Path: "placement.max_expected_cost_usd", Required: *workload.Spec.Placement.MaxExpectedCostUSD, Offered: estimates.CostUSD.Expected, Message: "Offer exceeds the requested maximum expected cost."})
	}
	return violations
}

func estimateCandidate(input SchedulingInput, offer domain.OfferSnapshot) domain.CandidateEstimates {
	queue := 0.0
	if schedule, ok := input.Schedules[offer.RentalID]; ok {
		queue = schedule.ExpectedWaitSeconds()
	} else if offer.Queue != nil {
		queue = offer.Queue.QueuedWorkSeconds
	}
	provision := 0.0
	if offer.Kind == domain.OfferKindProvisionable && offer.Provisioning != nil {
		provision = offer.Provisioning.Expected
	}
	pull := estimatePullSeconds(offer)
	expected := queue + provision + pull + 1
	start := domain.Estimate{Expected: expected, P50: expected, P90: expected * 1.25, Source: "scheduler", ModelVersion: input.ModelVersion}
	// A measured latency estimate for this offer overrides the derived one.
	if estimate, ok := input.LatencyEstimates[offer.ID]; ok && estimate.SampleCount > 0 {
		start = estimate
		if start.ModelVersion == "" {
			start.ModelVersion = input.ModelVersion
		}
	}
	costSeconds := input.Workload.Spec.Placement.ExpectedRuntimeSeconds
	if costSeconds <= 0 {
		costSeconds = float64(input.Workload.Spec.Execution.MaxRuntimeSeconds)
	}
	if costSeconds <= 0 {
		costSeconds = 1
	}
	minSeconds := float64(offer.Pricing.MinimumChargeSeconds)
	billedSeconds := math.Max(costSeconds, minSeconds)
	cost := offer.Pricing.SetupFeeUSD + offer.Pricing.RatePerSecondUSD*billedSeconds
	return domain.CandidateEstimates{
		QueueSeconds:     domain.Estimate{Expected: queue, P50: queue, P90: queue, Source: "offer", ModelVersion: input.ModelVersion},
		ProvisionSeconds: domain.Estimate{Expected: provision, P50: provision, P90: provision, Source: "offer", ModelVersion: input.ModelVersion},
		PullSeconds:      domain.Estimate{Expected: pull, P50: pull, P90: pull * 1.5, Source: "image_cache", ModelVersion: input.ModelVersion},
		StartSeconds:     start,
		CostUSD:          domain.Estimate{Expected: cost, Source: "price_model", ModelVersion: input.ModelVersion},
	}
}

func queueable(input SchedulingInput, offer domain.OfferSnapshot) bool {
	schedule, ok := input.Schedules[offer.RentalID]
	return offer.Kind == domain.OfferKindStanding && ok && len(schedule.Bookings) > 0 && len(schedule.Bookings) < domain.RentalScheduleQueueCapacity+1
}

func candidateDisposition(input SchedulingInput, offer domain.OfferSnapshot) domain.CandidateDisposition {
	if offer.Kind == domain.OfferKindProvisionable {
		return domain.CandidateDispositionProvision
	}
	if schedule, ok := input.Schedules[offer.RentalID]; ok && len(schedule.Bookings) > 0 {
		return domain.CandidateDispositionQueue
	}
	return domain.CandidateDispositionRunNow
}

func selectionReason(disposition domain.CandidateDisposition) string {
	switch disposition {
	case domain.CandidateDispositionRunNow:
		return "REUSE_EXISTING_RENTAL"
	case domain.CandidateDispositionQueue:
		return "QUEUE_EXISTING_RENTAL"
	case domain.CandidateDispositionProvision:
		return "PROVISION_FRESH_RENTAL"
	default:
		return "UNKNOWN_DISPOSITION"
	}
}

func estimatePullSeconds(offer domain.OfferSnapshot) float64 {
	if offer.ImageCache.Known && offer.ImageCache.MissingBytes == 0 {
		return 0
	}
	if offer.ImageCache.MissingBytes <= 0 {
		return 0
	}
	mbits := float64(offer.ImageCache.MissingBytes*8) / 1_000_000
	speed := 500.0
	for _, fact := range offer.Network.Download {
		if fact.Scope == domain.NetworkScopeRegistry && fact.Statistic == "p10" && fact.ValueMbps > 0 {
			speed = fact.ValueMbps
			break
		}
	}
	return mbits/speed + 0.5
}

func downloadRequirementSatisfied(now time.Time, req domain.NetworkDownloadRequirement, facts []domain.NetworkFact) bool {
	if len(facts) == 0 {
		return req.AllowUnknown
	}
	for _, fact := range facts {
		if fact.Scope != req.Scope || fact.Statistic != "p10" {
			continue
		}
		if !fact.ValidUntil.IsZero() && !fact.ValidUntil.After(now) {
			continue
		}
		if req.MaxMeasurementAgeSeconds > 0 && now.Sub(fact.ObservedAt) > time.Duration(req.MaxMeasurementAgeSeconds)*time.Second {
			continue
		}
		if fact.ValueMbps >= req.MinP10Mbps {
			return true
		}
	}
	return false
}

func requiresPublicInbound(container domain.ContainerSpec) bool {
	return slices.ContainsFunc(container.Ports, func(port domain.PortSpec) bool {
		return port.Exposure == domain.PortExposurePublic
	})
}

func acceleratorRequirementsSatisfied(requirements []domain.AcceleratorRequirement, offer domain.OfferSnapshot) bool {
	for _, req := range requirements {
		if req.Count <= 0 {
			continue
		}
		matched := 0
		for _, inventory := range offer.Resources.Accelerators {
			// Both sides are normalized through gpunorm so provider spellings
			// and requirement spellings align: the inventory carries a
			// canonical id, and each ModelAnyOf entry is canonicalized before
			// comparison so a requirement written as "RTX 5090" or
			// "nvidia-rtx5090" names the same card as "nvidia-rtx-5090".
			if req.Vendor != "" && gpunorm.NormalizeVendor(inventory.Vendor) != gpunorm.NormalizeVendor(req.Vendor) {
				continue
			}
			if len(req.ModelAnyOf) > 0 && !slices.ContainsFunc(req.ModelAnyOf, func(model string) bool {
				return gpunorm.Canonical(inventory.Vendor, model) == inventory.CanonicalModel
			}) {
				continue
			}
			if req.MemoryMinBytes > 0 && inventory.MemoryBytes < req.MemoryMinBytes {
				continue
			}
			matched += inventory.Count
		}
		if matched < req.Count {
			return false
		}
	}
	return true
}

func sortedOffers(offers []domain.OfferSnapshot) []domain.OfferSnapshot {
	out := append([]domain.OfferSnapshot(nil), offers...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ConnectionID != out[j].ConnectionID {
			return out[i].ConnectionID < out[j].ConnectionID
		}
		if out[i].AdapterType != out[j].AdapterType {
			return out[i].AdapterType < out[j].AdapterType
		}
		if out[i].NativeRef != out[j].NativeRef {
			return out[i].NativeRef < out[j].NativeRef
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func connectionIDs(offers []domain.OfferSnapshot) []string {
	seen := map[string]struct{}{}
	for _, offer := range offers {
		if offer.ConnectionID == "" {
			continue
		}
		seen[offer.ConnectionID] = struct{}{}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func uncertaintyPenalty(offer domain.OfferSnapshot) float64 {
	penalty := 0.0
	if offer.Capacity.Confidence > 0 && offer.Capacity.Confidence < 1 {
		penalty += 1 - offer.Capacity.Confidence
	}
	if offer.Reliability.Confidence > 0 && offer.Reliability.Confidence < 1 {
		penalty += 1 - offer.Reliability.Confidence
	}
	return penalty
}

func round(v float64, places int) float64 {
	factor := math.Pow10(places)
	return math.Round(v*factor) / factor
}
