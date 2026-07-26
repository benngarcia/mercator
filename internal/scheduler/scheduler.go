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
	// Image is the content every candidate is being asked to run. It travels
	// with the request because it is a property of the image: an offer that
	// restated it could disagree with the others about the same image.
	Image domain.ImageManifest
	// Artifacts is what the catalog says each version this Run reads is. It
	// travels with the request for the same reason the manifest does: size and
	// content digest are properties of the content, and a host that does not
	// hold something cannot be asked how big it is.
	Artifacts        []domain.ArtifactVersion
	LatencyEstimates map[string]domain.Estimate
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
	// A class Mercator cannot price is refused rather than ranked. CreateRun
	// already turns such a Run away, so reaching here means a revision was stored
	// by something that did not ask: scoring it would rank every candidate on
	// price alone and record a reason naming a class nothing declared.
	class := input.Workload.Spec.Placement.Class
	if !class.Known() {
		return domain.BookingDecision{}, fmt.Errorf("scheduler: workload states service class %q, which Mercator cannot price", class)
	}
	weights := class.Weights()

	decision := domain.BookingDecision{
		RunID:                  input.RunID,
		WorkloadRevisionDigest: input.Workload.Digest,
		EvaluatedAt:            input.EvaluatedAt.UTC(),
		ModelVersion:           input.ModelVersion,
		Policy:                 input.Workload.Spec.Placement,
		Weights:                weights,
		CollectionReport: domain.CollectionReport{
			ConnectionsQueried: connectionIDs(input.Offers),
		},
		Candidates: make([]domain.CandidateDecision, 0, len(input.Offers)),
	}

	bestIndex := -1
	offers := sortedOffers(input.Offers)
	for _, offer := range offers {
		candidate := evaluateOffer(input, weights, offer)
		decision.Candidates = append(decision.Candidates, candidate)
		if !candidate.Feasible {
			continue
		}
		if bestIndex == -1 || candidate.Preferred(decision.Candidates[bestIndex]) {
			bestIndex = len(decision.Candidates) - 1
		}
	}
	if bestIndex >= 0 {
		decision.SelectedOfferSnapshotID = decision.Candidates[bestIndex].OfferSnapshotID
		decision.SelectionReasonCodes = []string{"FEASIBLE", class.SelectionReason()}
		decision.SelectionReasonCodes = append(decision.SelectionReasonCodes, selectionReason(decision.Candidates[bestIndex].Disposition))
		if code := startSLOReason(input.Workload.Spec.Placement.MaxP90StartSeconds, decision.Candidates[bestIndex]); code != "" {
			decision.SelectionReasonCodes = append(decision.SelectionReasonCodes, code)
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
	kind := offer.Kind
	if !offer.Lane.Reusable() {
		// A one-shot execution holds nothing afterwards, so it gets its own
		// single-use binding instead of joining capacity another Run could
		// later queue behind.
		kind = domain.OfferKindProvisionable
	}
	switch kind {
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

// evaluateOffer is one candidate: what it was found holding, what that leaves it
// to do, whether it may be used at all, and what it is worth to a Run of this
// class. The score is computed last and from the record, never from the offer:
// every quantity it multiplies is a field of the candidate below, which is what
// lets a reader re-derive it and what lets the Lab police a term whose input
// nobody wrote down.
func evaluateOffer(input SchedulingInput, weights domain.ScoreWeights, offer domain.OfferSnapshot) domain.CandidateDecision {
	work := estimateCandidate(input, offer)
	rejections := feasibilityViolations(input, offer, work)
	candidate := domain.CandidateDecision{
		OfferSnapshotID:  offer.ID,
		ConnectionID:     offer.ConnectionID,
		AdapterType:      offer.AdapterType,
		NativeRef:        offer.NativeRef,
		Disposition:      candidateDisposition(input, offer),
		Feasible:         len(rejections) == 0,
		Rejections:       rejections,
		ImageLocality:    work.image,
		ArtifactEvidence: work.artifacts,
		// What each candidate holds of the Run's mutable caches is recorded and
		// never scored. A warm cache saves work inside the application, which
		// nothing here has measured, so pricing it would be an exchange rate this
		// model invented. The workspace comparison is why it is worth recording:
		// a cache of the same name in another workspace is a different cache, and
		// it must never read as warmth on this Run's record.
		CacheEvidence:  domain.CacheWarmth(input.Workload.WorkspaceID, input.Workload.Spec.Caches, offer.Caches),
		Disk:           work.disk,
		RentalSchedule: scheduleEvidence(input, offer),
		Estimates:      work.estimates,
		Confidences:    confidences(offer, work.estimates),
	}
	candidate.ScoreUSD = weights.ScoreUSD(candidate, input.Workload.Spec.Placement.ExpectedRuntimeSeconds)
	return candidate
}

// confidences is what each answer this candidate was scored on is worth, in the
// order the placement asked the questions. Only an answer whose source stated a
// confidence is recorded: zero means nobody said, and recording a silence as
// worthlessness would charge every candidate for questions this Run never asked.
func confidences(offer domain.OfferSnapshot, estimates domain.CandidateEstimates) []domain.Confidence {
	var stated []domain.Confidence
	for _, answer := range []domain.Confidence{
		{Answer: "capacity", Value: offer.Capacity.Confidence},
		{Answer: "reliability", Value: offer.Reliability.Confidence},
		{Answer: "pull_seconds", Value: estimates.PullSeconds.Confidence},
		{Answer: "artifact_seconds", Value: estimates.ArtifactSeconds.Confidence},
	} {
		if answer.Value > 0 {
			stated = append(stated, answer)
		}
	}
	return stated
}

// scheduleEvidence is the Broker state this candidate was weighed against. A
// Rental with no Bookings on it records none: the queue estimate already says
// there is nothing to wait for, and an empty schedule offered as evidence reads
// as a queue that was measured rather than one that does not exist.
func scheduleEvidence(input SchedulingInput, offer domain.OfferSnapshot) *domain.ScheduleEvidence {
	schedule, scheduled := input.Schedules[offer.RentalID]
	if !scheduled || len(schedule.Bookings) == 0 {
		return nil
	}
	evidence := schedule.Evidence(input.EvaluatedAt)
	return &evidence
}

func feasibilityViolations(input SchedulingInput, offer domain.OfferSnapshot, work candidateWork) []domain.Violation {
	estimates := work.estimates
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
	if !offer.Lane.Valid() {
		violations = append(violations, domain.Violation{
			Code:     "UNKNOWN_FACT",
			Path:     "lane",
			Required: "reusable or ephemeral",
			Offered:  string(offer.Lane),
			Message:  "Offer does not state whether Mercator can run a second workload on it.",
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
	// The disk is one question. A Run's own reservation and the room its content
	// takes are spent against the same bytes, so asking them separately admits a
	// Run onto a machine whose whole floor turns out to be its own dataset, and
	// asking about the floor alone lets a machine with nowhere to put forty
	// gigabytes be selected and then refuse the launch with nothing in the record
	// naming disk.
	if !work.disk.Fits() {
		violations = append(violations, domain.Violation{
			Code:     "RESOURCE_INSUFFICIENT",
			Path:     "resources.ephemeral_disk",
			Required: work.disk.RequiredBytes(),
			Offered:  work.disk.FreeBytes,
			Message:  "Offer has less room left than the Run reserved plus the content it would have to land here.",
		})
	}
	if !acceleratorRequirementsSatisfied(workload.Spec.Resources.Accelerators, offer) {
		violations = append(violations, domain.Violation{Code: "RESOURCE_INSUFFICIENT", Path: "resources.accelerators", Required: workload.Spec.Resources.Accelerators, Offered: offer.Resources.Accelerators, Message: "Offer has insufficient accelerator inventory."})
	}
	if requiresPublicInbound(container) && offer.Capabilities.Network.Inbound != domain.InboundNetworkPublicPort {
		violations = append(violations, domain.Violation{Code: "CAPABILITY_MISMATCH", Path: "network.inbound", Required: domain.InboundNetworkPublicPort, Offered: offer.Capabilities.Network.Inbound, Message: "Offer cannot expose inbound public ports."})
	}
	// A host that cannot say what it holds is not infeasible. Unknown locality
	// is uncertainty to price, and the goal is explicit that it must not be
	// mistaken for a hard constraint. The transfer estimate records the silence
	// with no confidence, so the decision says which it was.
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
	if exceedsStartSLO(workload.Spec.Placement.MaxP90StartSeconds, estimates.EstablishedStartSeconds) {
		violations = append(violations, domain.Violation{
			Code:     "LATENCY_SLO_EXCEEDED",
			Path:     "placement.max_p90_start_seconds",
			Required: workload.Spec.Placement.MaxP90StartSeconds,
			Offered:  estimates.EstablishedStartSeconds.P90,
			Message:  "Offer is known to start later than the requested p90 start latency, before any content nobody could describe is priced.",
		})
	}
	if workload.Spec.Placement.MaxExpectedCostUSD != nil && estimates.CostUSD.Expected > *workload.Spec.Placement.MaxExpectedCostUSD {
		violations = append(violations, domain.Violation{Code: "COST_LIMIT_EXCEEDED", Path: "placement.max_expected_cost_usd", Required: *workload.Spec.Placement.MaxExpectedCostUSD, Offered: estimates.CostUSD.Expected, Message: "Offer exceeds the requested maximum expected cost."})
	}
	return violations
}

// startSLOReason is what the decision says about the bound the Run set on its
// own start. A candidate predicted inside it is inside it. A candidate admitted
// because nobody could say what it holds is not: its prediction is over the
// bound and rests on an image locality nothing established, and recording that
// as compliance would state a promise out of a silence.
func startSLOReason(limit float64, selected domain.CandidateDecision) string {
	switch {
	case limit <= 0:
		return ""
	case selected.Estimates.StartSeconds.P90 <= limit:
		return "WITHIN_START_SLO"
	default:
		return "START_SLO_UNVERIFIED"
	}
}

// exceedsStartSLO reports whether this candidate is KNOWN to start later than
// the Run asked for. It is asked of the established part of the prediction and
// never of the whole, because a second priced out of a silence is a guess and
// refusing a candidate on a guess is what turns silence into infeasibility. The
// silence is still priced, so a host nobody can describe never outranks one
// provably ready; priced is as far as it may go.
//
// What the split buys over waiving the bound whenever anything was unreadable
// is the other half of the same rule. Provisioning is what the provider
// published, and the queue is what Mercator projects from Bookings it holds and
// re-projects as they run, so a machine fifteen minutes deep in its own queue is
// late whatever it could say about its disk, and a Run that refuses to wait
// three minutes gets to strike it out.
//
// A measured start latency for this offer is a measurement whatever the
// locality was, so it binds: startEstimate returns the sample for both halves.
func exceedsStartSLO(limit float64, established domain.Estimate) bool {
	return limit > 0 && established.P90 > limit
}

// candidateWork is what one candidate was found holding, image and Artifact,
// and what that leaves it to do. The two localities travel with the estimates
// because they are the same answer read two ways: the seconds are what it costs,
// the states are what it was.
type candidateWork struct {
	estimates domain.CandidateEstimates
	image     domain.LocalityState
	artifacts []domain.ArtifactEvidence
	disk      domain.DiskDemand
}

// contentWork is what one kind of content costs this candidate and how much of
// that price somebody established. The two differ only where a host could not
// enumerate: bytes charged because nothing said they were already here are a
// price, and the established estimate is what is left when they are taken out.
type contentWork struct {
	predicted   domain.Estimate
	established domain.Estimate
}

func estimateCandidate(input SchedulingInput, offer domain.OfferSnapshot) candidateWork {
	queue := queueEstimate(input, offer)
	provision := provisionEstimate(input, offer)
	content := contentFor(input, offer)
	image := pullEstimate(input.Image, offer, content, input.ModelVersion)
	inputs := artifactEstimate(offer.Artifacts, content, input.ModelVersion)
	return candidateWork{
		estimates: domain.CandidateEstimates{
			QueueSeconds:            queue,
			ProvisionSeconds:        provision,
			PullSeconds:             image.predicted,
			ArtifactSeconds:         inputs.predicted,
			StartSeconds:            startEstimate(input, offer, queue, provision, image.predicted, inputs.predicted),
			EstablishedStartSeconds: startEstimate(input, offer, queue, provision, image.established, inputs.established),
			CostUSD:                 costEstimate(input, offer),
		},
		image:     content.locality,
		artifacts: content.evidence,
		disk:      content.disk,
	}
}

// candidateContent is everything this Run's content amounts to on one
// candidate: what each kind of it the host was found holding, what it still
// owes, and whether the room the machine has left can take it. The answers are
// made together because the disk they compete for is one resource, and made
// before either estimate prices anything, because whether the work fits here is
// not a property of the image or of the Artifact alone.
type candidateContent struct {
	image    domain.ImageWork
	locality domain.LocalityState
	fetch    int64
	evidence []domain.ArtifactEvidence
	disk     domain.DiskDemand
}

func contentFor(input SchedulingInput, offer domain.OfferSnapshot) candidateContent {
	work, locality := input.Image.StartWork(offer.Images)
	fetch, evidence := domain.ArtifactFetchWork(input.Artifacts, offer.Artifacts)
	caches := domain.CacheLandBytes(input.Workload.WorkspaceID, input.Workload.Spec.Caches, offer.Caches)
	return candidateContent{
		image:    work,
		locality: locality,
		fetch:    fetch,
		evidence: evidence,
		disk: domain.DiskDemand{
			FreeBytes:     offer.Resources.EphemeralDiskBytes,
			ReservedBytes: input.Workload.Spec.Resources.EphemeralDisk.MinBytes,
			LandBytes:     work.TransferBytes + fetch + caches,
			EstablishedLandBytes: enumerated(work.TransferBytes, locality != domain.LocalityUnknown) +
				establishedFetchBytes(evidence) +
				enumerated(caches, offer.Caches.Known),
		},
	}
}

// enumerated is bytes counted only where something looked. Content a host could
// not describe is priced in seconds and never asked to fit, because nothing said
// those bytes have to arrive and nothing said they are not already here.
func enumerated(bytes int64, known bool) int64 {
	if !known {
		return 0
	}
	return bytes
}

// queueEstimate is how long work arriving now waits behind what is already
// assigned here. A Rental Schedule projects it from where its Bookings actually
// are, so a machine a minute from finishing an hour-long Booking is a minute of
// waiting; an offer with no schedule states its own queued work. Either way it
// is one number rather than a distribution, because neither authority publishes
// a spread on it, and inventing one would be this model's arithmetic wearing a
// provider's clothes.
func queueEstimate(input SchedulingInput, offer domain.OfferSnapshot) domain.Estimate {
	seconds := 0.0
	switch schedule, scheduled := input.Schedules[offer.RentalID]; {
	case scheduled:
		seconds = schedule.ExpectedWaitSeconds(input.EvaluatedAt)
	case offer.Queue != nil:
		seconds = offer.Queue.QueuedWorkSeconds
	}
	return domain.Estimate{Expected: seconds, P50: seconds, P90: seconds, Source: "offer", ModelVersion: input.ModelVersion}
}

// provisionEstimate is what the provider published about bringing this machine
// up, carried through as published. A quantile the provider did not state is its
// expectation restated: an unstated p90 is not a promise of a short tail, and
// replacing a published one with a spread of this model's own would enforce the
// Run's start bound against a quantile Mercator made up while the provider's own
// answer sat unread on the offer.
func provisionEstimate(input SchedulingInput, offer domain.OfferSnapshot) domain.Estimate {
	estimate := domain.Estimate{Source: "offer", ModelVersion: input.ModelVersion}
	if offer.Kind != domain.OfferKindProvisionable || offer.Provisioning == nil {
		return estimate
	}
	published := *offer.Provisioning
	estimate.Expected = published.Expected
	estimate.P50 = orExpected(published.P50, published.Expected)
	estimate.P90 = orExpected(published.P90, published.Expected)
	return estimate
}

func orExpected(quantile, expected float64) float64 {
	if quantile <= 0 {
		return expected
	}
	return quantile
}

// startEstimate assembles when this candidate is ready out of the parts it is
// waiting on, plus the second every launch costs whatever it holds. Quantiles
// add rather than being scaled off the expectation, because each part's tail
// belongs to whoever published it: a provider that states an eighteen-minute p90
// provisioning has said what its own tail is. Summing them is deliberately
// pessimistic about the joint distribution, which nothing here models, and
// pessimism about a bound the caller set is the safe direction.
//
// A measured latency estimate for this offer replaces the derived answer
// outright, and replaces both halves of it: a sample is a measurement about this
// machine whatever anyone could enumerate, so there is no unestablished part of
// it to discount.
func startEstimate(input SchedulingInput, offer domain.OfferSnapshot, parts ...domain.Estimate) domain.Estimate {
	if measured, ok := input.LatencyEstimates[offer.ID]; ok && measured.SampleCount > 0 {
		if measured.ModelVersion == "" {
			measured.ModelVersion = input.ModelVersion
		}
		return measured
	}
	start := domain.Estimate{
		Expected:     domain.LaunchSeconds,
		P50:          domain.LaunchSeconds,
		P90:          domain.LaunchSeconds * 1.25,
		Source:       "scheduler",
		ModelVersion: input.ModelVersion,
	}
	for _, part := range parts {
		start.Expected += part.Expected
		start.P50 += part.P50
		start.P90 += part.P90
	}
	return start
}

func costEstimate(input SchedulingInput, offer domain.OfferSnapshot) domain.Estimate {
	seconds := input.Workload.Spec.Placement.ExpectedRuntimeSeconds
	if seconds <= 0 {
		seconds = float64(input.Workload.Spec.Execution.MaxRuntimeSeconds)
	}
	if seconds <= 0 {
		seconds = 1
	}
	billed := math.Max(seconds, float64(offer.Pricing.MinimumChargeSeconds))
	return domain.Estimate{
		Expected:     offer.Pricing.SetupFeeUSD + offer.Pricing.RatePerSecondUSD*billed,
		Source:       "price_model",
		ModelVersion: input.ModelVersion,
	}
}

// queueable reports whether a Run may wait behind work already assigned here.
// Only reusable capacity qualifies: waiting for a one-shot execution to finish
// buys nothing, because the machine does not survive it.
//
// A schedule that can no longer say when its Rental comes free is not something
// to wait behind either, and it is the one case where the machine's own capacity
// evidence is the better answer: the Booking on it is past the runtime Mercator
// enforces, so the wait projects to nothing while the offer says the capacity is
// occupied. Queueing there would price a busy machine at zero seconds of waiting
// and hand the arriving Run a latest start already at its deadline.
func queueable(input SchedulingInput, offer domain.OfferSnapshot) bool {
	if !offer.Lane.Reusable() || offer.Kind != domain.OfferKindStanding {
		return false
	}
	schedule, ok := input.Schedules[offer.RentalID]
	if !ok || len(schedule.Bookings) == 0 || len(schedule.Bookings) > domain.RentalScheduleQueueCapacity {
		return false
	}
	return !schedule.Exhausted(input.EvaluatedAt)
}

func candidateDisposition(input SchedulingInput, offer domain.OfferSnapshot) domain.CandidateDisposition {
	if !offer.Lane.Reusable() {
		return domain.CandidateDispositionEphemeral
	}
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
	case domain.CandidateDispositionEphemeral:
		return "LAUNCH_EPHEMERAL"
	default:
		return "UNKNOWN_DISPOSITION"
	}
}

// pullEstimate prices what this candidate still owes before the image can
// start, and states how much that answer is worth. Zero seconds is reserved for
// a host an inventory says holds the image, or for an image nothing could
// resolve, where the same nothing is charged to every candidate and the
// comparison is unaffected. A host that will not say what it holds is charged
// the whole image, because the bytes have to come from somewhere and nothing
// here says they are already there.
//
// A host that fetched the image and has not assembled it owes local work rather
// than a transfer, over a different resource at a different rate, so the two
// are added as what they are. Charging that host a pull would bill the network
// twice for bytes that are already on the machine.
//
// Confidence is about the duration, not the bytes. Bytes counted from a
// manifest and an inventory that both spoke are certain, so a host that holds
// everything is certainly zero seconds away from starting. Bytes assumed
// because a host said nothing are not, and neither is a duration over a rate
// nothing measured, so either one caps the answer at AssumedLinkConfidence.
//
// Room is not priced here, or anywhere. A machine short of it is refused rather
// than charged, because the only content it could give up to make room is
// content this Run needs back, and deleting that frees exactly what fetching it
// again consumes.
func pullEstimate(manifest domain.ImageManifest, offer domain.OfferSnapshot, content candidateContent, modelVersion string) contentWork {
	work, locality := content.image, content.locality
	transfer := work.TransferBytes
	estimate := domain.Estimate{
		Source:       pullSource(locality, manifest, offer.Images),
		ModelVersion: modelVersion,
	}
	if transfer == 0 && work.UnpackBytes == 0 {
		if locality != domain.LocalityUnknown {
			estimate.Confidence = 1
		}
		return establishedIfDescribed(estimate, locality)
	}
	link := offer.RegistryDownload()
	seconds := float64(transfer*8)/1_000_000/link.Mbps +
		float64(work.UnpackBytes)/1_000_000/domain.AssumedUnpackMBps + 0.5
	estimate.Expected, estimate.P50, estimate.P90 = seconds, seconds, seconds*1.5
	estimate.Confidence = link.Confidence
	if work.UnpackBytes > 0 || locality == domain.LocalityUnknown {
		estimate.Confidence = min(estimate.Confidence, domain.AssumedLinkConfidence)
	}
	return establishedIfDescribed(estimate, locality)
}

// establishedIfDescribed splits one image prediction into the whole price and
// the part of it somebody established. A host that could not say what it holds
// is charged this whole image and establishes none of it: nothing said the bytes
// are here, and nothing said they are not. Every other answer counts bytes a
// manifest and an inventory both spoke about.
func establishedIfDescribed(estimate domain.Estimate, locality domain.LocalityState) contentWork {
	if locality == domain.LocalityUnknown {
		return contentWork{predicted: estimate}
	}
	return contentWork{predicted: estimate, established: estimate}
}

// artifactEstimate prices what this candidate would still have to read out of
// the object store before the Run can touch its declared inputs, and records
// what was found of each one. It is the placement half of Artifact locality: the
// durable copy is what makes the content consumable at all, and a host-local
// copy only ever changes how long getting to it takes.
//
// The answer reaches the score through this estimate and the start estimate it
// feeds, which the Run's class prices by the second. It is deliberately not a
// weighted locality term of its own: what a candidate holds is worth the seconds
// it saves, and a second is worth what the class says it is worth.
//
// Confidence follows the same rule transfers already follow: a host that owes
// nothing is certainly zero seconds away, and every other answer crosses a link
// nothing has measured.
func artifactEstimate(inventory domain.ArtifactInventory, content candidateContent, modelVersion string) contentWork {
	source := artifactSource(inventory, content.evidence)
	if len(content.evidence) == 0 {
		return contentWork{predicted: domain.Estimate{Source: source, ModelVersion: modelVersion}}
	}
	return contentWork{
		predicted:   objectStoreRead(content.fetch, source, modelVersion),
		established: objectStoreRead(establishedFetchBytes(content.evidence), source, modelVersion),
	}
}

// objectStoreRead is what reading these bytes out of the object store costs.
// Bytes that do not have to move cost nothing and there is no doubt about it;
// bytes that do cross a link nothing has measured, which is what caps the
// answer's confidence however exactly the arithmetic on it reads.
func objectStoreRead(bytes int64, source, modelVersion string) domain.Estimate {
	seconds := objectStoreSeconds(bytes)
	estimate := domain.Estimate{
		Expected:     seconds,
		P50:          seconds,
		P90:          seconds * 1.5,
		Confidence:   domain.AssumedLinkConfidence,
		Source:       source,
		ModelVersion: modelVersion,
	}
	if bytes == 0 {
		estimate.Confidence = 1
	}
	return estimate
}

// establishedFetchBytes is the content this candidate owes that some inventory
// actually answered about. A version filed unknown is charged its whole size
// and establishes none of it: nothing said the bytes are here, and nothing said
// they are not.
func establishedFetchBytes(evidence []domain.ArtifactEvidence) int64 {
	bytes := int64(0)
	for _, found := range evidence {
		if found.Locality != domain.LocalityUnknown {
			bytes += found.FetchBytes
		}
	}
	return bytes
}

func objectStoreSeconds(bytes int64) float64 {
	return float64(bytes*8) / 1_000_000 / domain.DefaultObjectStoreDownloadMbps
}

// artifactSource names whose evidence this answer rests on, and when it rests on
// none, whose silence it was. A host that cannot enumerate its copies and a host
// that enumerated and holds nothing are priced the same seconds and are
// different problems for an operator.
func artifactSource(inventory domain.ArtifactInventory, evidence []domain.ArtifactEvidence) string {
	switch {
	case len(evidence) == 0:
		return ""
	case inventory.Known:
		return "artifact_inventory"
	default:
		return "inventory_unknown"
	}
}

// localitySource names where this answer came from, and when there is no
// answer, which silence it was. "Unknown" alone made a registry Mercator could
// not read indistinguishable from a host that cannot enumerate itself, and
// those are fixed by different people.
func pullSource(locality domain.LocalityState, manifest domain.ImageManifest, inventory domain.ImageInventory) string {
	switch {
	case locality != domain.LocalityUnknown:
		return "image_inventory"
	case !manifest.Known && manifest.Unreadable != "":
		return manifest.Unreadable
	case !manifest.Known:
		return "manifest_unresolved"
	case !inventory.Known:
		return "inventory_unknown"
	case inventory.Undescribed(manifest.Digest):
		// The host enumerated itself and failed on this one image. An operator
		// reading "inventory_unknown" would go looking for a machine that
		// cannot be asked, and this one answered about everything else.
		return "image_undescribed"
	default:
		return "manifest_without_layers"
	}
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
