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
	"github.com/benngarcia/mercator/internal/prediction"
)

type Scheduler interface {
	Evaluate(ctx context.Context, input SchedulingInput) (domain.BookingDecision, error)
}

type SchedulingInput struct {
	RunID    string
	Workload domain.WorkloadRevision
	Offers   []domain.OfferSnapshot
	// Collection is who was asked for those offers. It is given rather than
	// derived, because it cannot be derived: a connection that answered with
	// nothing publishes no offer to be counted, and neither does a connection
	// nobody contacted. Deriving the census from the offers stated the two
	// identically, so an operator reading a Run that nothing matched could not
	// tell a marketplace selling no machine of that shape from a workspace whose
	// providers were never asked.
	Collection domain.CollectionReport
	Schedules  map[string]domain.RentalSchedule
	// Excluded is what earlier attempts on this Run proved about offers this
	// evaluation can still see, each carrying what it proved. A bare list of IDs
	// could only ever say "an earlier attempt refused this", which reads a machine
	// somebody else was using and a machine Mercator allocated and destroyed as
	// one fact.
	Excluded     []domain.OfferExclusion
	ModelVersion string
	EvaluatedAt  time.Time
	// Image is the content every candidate is being asked to run. It travels
	// with the request because it is a property of the image: an offer that
	// restated it could disagree with the others about the same image.
	Image domain.ImageManifest
	// Artifacts is what the catalog says each version this Run reads is. It
	// travels with the request for the same reason the manifest does: size and
	// content digest are properties of the content, and a host that does not
	// hold something cannot be asked how big it is.
	Artifacts []domain.ArtifactVersion
	// Supersedes is the decision this evaluation is being asked to stand in for,
	// and SupersedesReason is why. They are inputs rather than something stamped on
	// afterwards because the decision's identity is derived from them: two answers
	// about one unchanged fleet at one instant are different decisions exactly
	// because the second one replaces the first, and an identity that ignored that
	// would give them the same ID.
	Supersedes       string
	SupersedesReason string
	// History is what earlier launches of these candidates really spent, indexed
	// by what recurs about them. It replaces a map of measured estimates keyed by
	// offer snapshot ID: nothing in production ever wrote that map, and nothing
	// could have written it honestly, because half the backends in the tree mint a
	// fresh snapshot ID for every search of one machine and a store keyed on one
	// would report a key that cannot grow as candidate-specific evidence.
	History prediction.History
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
		Supersedes:             input.Supersedes,
		SupersedesReason:       input.SupersedesReason,
		CollectionReport:       input.Collection,
		Candidates:             make([]domain.CandidateDecision, 0, len(input.Offers)),
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
		if code := pricedRankingReason(decision.Candidates, bestIndex); code != "" {
			decision.SelectionReasonCodes = append(decision.SelectionReasonCodes, code)
		}
		if code := startSLOReason(input.Workload.Spec.Placement.MaxP90StartSeconds, decision.Candidates[bestIndex]); code != "" {
			decision.SelectionReasonCodes = append(decision.SelectionReasonCodes, code)
		}
	} else {
		decision.SelectionReasonCodes = []string{"NO_FEASIBLE_OFFERS"}
	}
	// The identity is derived from the decision itself and not from the search that
	// produced it, so that any reader holding the record can re-derive it and get
	// the same answer. See domain.BookingDecision.Identity.
	id, err := decision.Identity()
	if err != nil {
		return domain.BookingDecision{}, err
	}
	decision.ID = id
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
		OfferSnapshotID: offer.ID,
		ConnectionID:    offer.ConnectionID,
		AdapterType:     offer.AdapterType,
		NativeRef:       offer.NativeRef,
		// What this candidate is, as opposed to what this listing was called. Every
		// stage below is about to be predicted from what earlier launches of the same
		// thing spent, and this is the account of what Mercator took the same thing to
		// be: derived from the facts the backend published rather than from the ID it
		// numbered the listing with, which recurs for one backend and never for
		// another.
		Candidate:        domain.CandidateIdentityOf(offer, input.Image.Digest),
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
		CacheEvidence: domain.CacheWarmth(input.Workload.WorkspaceID, input.Workload.Spec.Caches, offer.Caches),
		// What this machine's publisher says it does to work: refuse to start it,
		// or drop it once it is running. Recorded and not scored, because pricing a
		// refusal needs a probability times a predicted start and nothing here
		// predicts either. It is recorded for the same reason the cache warmth above
		// is: this is the account of what was known when the placement was taken,
		// and a fact the record omits is one the slice that prices it cannot be
		// held to.
		Reliability:    offer.Reliability,
		Disk:           work.disk,
		RentalSchedule: scheduleEvidence(input, offer),
		Estimates:      work.estimates,
		TransferRates:  work.rates,
		Confidences:    confidences(offer, work.estimates),
	}
	candidate.ScoreUSD = weights.ScoreUSD(candidate, input.Workload.Spec.Placement.ExpectedRuntimeSeconds)
	return candidate
}

// confidences is what each answer this candidate was scored on is worth, in the
// order the placement asked the questions. Only an answer whose source stated a
// confidence is recorded: zero means nobody said, and recording a silence as
// worthlessness would charge every candidate for questions this Run never asked.
//
// Every answer here is one the score reads: the capacity claim decides whether
// this machine is for sale at all, and the two transfer durations are terms of
// the start it is priced on. The reliability history is read by nothing, and the
// doubt about it was listed here anyway. Because a stated confidence is charged
// and a silence is not, the only thing a published history could do to a score
// was penalise its publisher for having published one: a machine measured and
// never seen to fail lost the Run to an identical machine nobody had measured,
// and to a machine whose provider was certain it refuses every start. Doubt about
// an answer the score does not use prices the absence of a price, and what a
// refusal is worth belongs to the term that predicts a redo.
//
// Which answers those are is declared by domain.ScoredAnswers rather than by this
// list, and safety.doubt_only_the_answers_the_score_reads holds every recorded
// decision to the declaration. The rule stated in a comment beside the one
// producer is the shape the reliability entry survived a phase in.
func confidences(offer domain.OfferSnapshot, estimates domain.CandidateEstimates) []domain.Confidence {
	var stated []domain.Confidence
	for _, answer := range []domain.Confidence{
		{Answer: domain.AnswerCapacity, Value: offer.Capacity.Confidence},
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
	if exclusion, struck := domain.ExcludedOffer(input.Excluded, offer.ID); struck {
		violations = append(violations, exclusion.Reason.Violation(offer.ID))
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
		violations = append(violations, domain.Violation{Code: "CAPACITY_UNAVAILABLE", Path: "capacity.available", Required: true, Offered: false, Message: "Offer capacity evidence says the capacity is not currently available.", EndedByWaiting: true})
	}
	// A listing of a machine this fleet already holds is capacity already
	// acquired, and buying it again would buy one host twice. It does not end by
	// waiting: nothing hands the machine back, and whether this Run can ever run
	// there is the machine's own answer beside it. See domain.HolderOfMachine.
	if holder, held := domain.HolderOfMachine(input.Offers, offer); held {
		violations = append(violations, domain.Violation{Code: "CAPACITY_ALREADY_HELD", Path: "machine_id", Required: "a machine this fleet does not already hold", Offered: offer.MachineID, Message: "Offer sells a machine this fleet already holds under Rental " + holder + "."})
	}
	if schedule, ok := input.Schedules[offer.RentalID]; ok && len(schedule.Bookings) >= domain.RentalScheduleQueueCapacity+1 {
		violations = append(violations, domain.Violation{Code: "QUEUE_CAPACITY_EXCEEDED", Path: "rental_schedule.bookings", Required: domain.RentalScheduleQueueCapacity + 1, Offered: len(schedule.Bookings), Message: "Rental Schedule has no open Booking position.", EndedByWaiting: true})
	}
	// A Rental whose Booking is past the runtime Mercator enforces can promise no
	// start behind it, and domain.RentalSchedule refuses the reservation outright.
	// It is asked here as well as of the offer's own availability, because the two
	// answers come from different authorities and can disagree: a machine that says
	// it is free while Mercator still holds an open Booking on it was selected and
	// then failed to reserve, which ended the whole placement rather than striking
	// out one candidate.
	//
	// It does not end by waiting, and the message says why: this schedule cannot
	// promise a start at all, which is not the claim that the capacity comes back
	// when the work spending it finishes. Every projection off an exhausted
	// schedule reads zero, so a refusal counted as a wait would put this Run behind
	// a Booking that already overran, name its Run as work ahead, and defer with a
	// projected wait of nothing. That is the head-of-line block domain.Violation
	// names as the reason the flag is false by default.
	if schedule, ok := input.Schedules[offer.RentalID]; ok && offer.KeepsWhatItRuns() && schedule.Exhausted(input.EvaluatedAt) {
		violations = append(violations, domain.Violation{Code: "RENTAL_SCHEDULE_EXHAUSTED", Path: "rental_schedule.bookings", Required: 0, Offered: schedule.Bookings[0].OverrunSeconds(input.EvaluatedAt), Message: "Rental Schedule cannot promise a start behind a Booking past the runtime Mercator enforces."})
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
	//
	// A machine that never measured its room is refused too, and not for the same
	// thing. Landing content on a disk nobody looked at is a launch nobody can
	// promise, so the refusal stands, but it names a fact nobody published rather
	// than a shortfall somebody measured. Read as a shortfall it said the fleet
	// can never hold this work on the strength of one failed statfs.
	switch {
	case !work.disk.FreeBytesKnown:
		violations = append(violations, domain.Violation{
			Code:     "UNKNOWN_FACT",
			Path:     "resources.ephemeral_disk",
			Required: work.disk.RequiredBytes(),
			Offered:  "unknown",
			Message:  "Offer does not say how much room this machine has left.",
			Unstated: true,
		})
	case !work.disk.Fits():
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
		violations = append(violations, downloadFloorViolations(input.EvaluatedAt, *req, offer.Network)...)
	}
	if !offer.Pricing.Known && !workload.Spec.Placement.AllowUnknownPricing {
		violations = append(violations, domain.Violation{Code: "UNKNOWN_FACT", Path: "pricing", Required: "known", Offered: "unknown", Message: "Policy does not allow unknown pricing."})
	}
	// Capacity its provider may take back is refused to work whose class does not
	// permit interruption. It is decided here, before the work starts, because that
	// is the only moment there is a decision to make: nothing Mercator holds
	// survives a machine being reclaimed, so by the time the provider says so the
	// choice has been made for it.
	//
	// It refuses the machine for what it is rather than for what it is doing, so no
	// amount of waiting ends it and the fleet answer counts this candidate among the
	// machines that can never hold this Run.
	if offer.Reclaimable && !workload.Spec.Placement.Class.Admission().PermitsInterruption {
		violations = append(violations, domain.Violation{
			Code:     "INTERRUPTION_NOT_PERMITTED",
			Path:     "reclaimable",
			Required: false,
			Offered:  true,
			Message:  "Offer capacity can be taken back by its provider, and this Run's class does not permit interruption.",
		})
	}
	// Capacity somebody holds for a particular kind of work is refused to every
	// other kind rather than priced for it. Reserved capacity is a statement about
	// what the machine is for, so no amount of waiting makes a batch sweep eligible
	// for a machine kept for work somebody is watching, and pricing it there would
	// rank a machine the sweep can never have.
	if !offer.Terms.Admits(workload.Spec.Placement.Class) {
		violations = append(violations, domain.Violation{
			Code:     "CLASS_NOT_ELIGIBLE",
			Path:     "capacity_terms.eligible_classes",
			Required: offer.Terms.EligibleClasses,
			Offered:  workload.Spec.Placement.Class,
			Message:  "This capacity is reserved for other service classes, and this Run's class is not one of them.",
		})
	}
	// Capacity that stops being Mercator's at a known moment is refused work that
	// could still be holding it then. The bound is the runtime Mercator enforces
	// rather than the one the Run expects, because the expectation is a guess and
	// the bound is what Mercator would actually allow: admitting on the guess puts
	// work on a machine that goes away underneath it whenever the guess is short.
	//
	// It is a refusal rather than a price because there is nothing to trade off. A
	// window that closes mid-Run does not make the work more expensive, it makes it
	// not happen, and a Run that waits longer for this machine is worse off rather
	// than better.
	if offer.Terms.OutlivesWindow(work.occupancy) {
		violations = append(violations, domain.Violation{
			Code:     "AVAILABILITY_WINDOW_CLOSES",
			Path:     "capacity_terms.available_until",
			Required: offer.Terms.AvailableUntil,
			Offered:  work.occupancy.LatestEnd(),
			Message:  "This capacity stops being available before the Run would have to be off it.",
		})
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
	// A bound on dollars is not cleared by a candidate that has no dollars. An
	// unpriced machine reported a cost of zero, so it passed every budget a Run
	// could state, which is the same fabrication as pricing it at nothing: the Run
	// asked to spend no more than a number, and nobody can say what this spends.
	if maximum := workload.Spec.Placement.MaxExpectedCostUSD; maximum != nil {
		offered := any(estimates.CostUSD.Expected)
		exceeded := estimates.CostUSD.Expected > *maximum
		if estimates.CostUSD.Source == domain.CostUnpriced {
			offered, exceeded = domain.CostUnpriced, true
		}
		if exceeded {
			violations = append(violations, domain.Violation{Code: "COST_LIMIT_EXCEEDED", Path: "placement.max_expected_cost_usd", Required: *maximum, Offered: offered, Message: "Offer exceeds the requested maximum expected cost."})
		}
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
	// rates is the throughput every stage that had bytes to move was priced at.
	// It travels with the estimates because it is where they came from: one
	// LinkSpeed produced the seconds and this account of them.
	rates []domain.TransferRate
	disk  domain.DiskDemand
	// occupancy is when this Run would hold the machine and for how long, which
	// is what the price above was computed over and what the terms of the sale
	// are checked against. It travels with the estimates because it is derived
	// from one of them: the start.
	occupancy domain.Occupancy
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
	machine := provisionEstimate(input, offer)
	content := contentFor(input, offer)
	registry, store, storage := offer.DownloadRate(domain.NetworkScopeRegistry, input.EvaluatedAt), offer.DownloadRate(domain.NetworkScopeObjectStore, input.EvaluatedAt), domain.UnpackRate()
	fetch, unpack := imageEstimates(input.Image, offer, content, registry, storage, input.ModelVersion)
	inputs := artifactEstimate(offer.Artifacts, content, store, input.ModelVersion)
	container := containerStartEstimate(input.ModelVersion)
	ready := applicationReadyEstimate(input)
	stages := domain.LaunchStageEstimates{
		Acquisition:      machine.acquisition,
		Boot:             machine.boot,
		AgentReady:       machine.agentReady,
		ImageFetch:       fetch.predicted,
		Unpack:           unpack.predicted,
		ArtifactFetch:    inputs.predicted,
		ContainerStart:   container,
		ApplicationReady: ready,
	}
	established := stages
	established.ImageFetch = fetch.established
	established.Unpack = unpack.established
	established.ArtifactFetch = inputs.established
	// What this fleet has watched this candidate do, wherever it has watched
	// anything, replaces what it assumed. Both halves go through the same
	// predictor: a measurement is established by definition, so a stage answered
	// from history is as good in the bound as it is in the score. No stage priced
	// from bytes is answerable that way, so the discount the established estimate
	// carries above is never one a measurement of some other launch swallowed.
	answer := stagePredictor(input, offer)
	stages, established = stages.Answered(answer), established.Answered(answer)
	start := startEstimate(input, queue, stages)
	// The price is asked last, of the start this launch was just predicted to
	// have. What a machine costs depends on when the Run gets it: the seconds a
	// Run spends of an interval Mercator already owes begin where the wait in
	// front of it ends, and a model that priced from the decision's own moment
	// would sell one committed hour to everything queued on the machine.
	held := occupancy(input, start)
	cost, terms, committed := costEstimate(input, offer, held)
	return candidateWork{
		estimates: domain.CandidateEstimates{
			QueueSeconds:            queue,
			Stages:                  stages,
			StartSeconds:            start,
			EstablishedStartSeconds: startEstimate(input, queue, established),
			CostUSD:                 cost,
			CostTerms:               terms,
			Committed:               committed,
		},
		image:     content.locality,
		artifacts: content.evidence,
		rates:     transferRates(content, registry, storage, store),
		disk:      content.disk,
		occupancy: held,
	}
}

// stagePredictor is this candidate's launch put to the fleet's own history:
// every stage is answered by the narrowest level holding measured launches of
// this candidate, of this provider in this place, or of this provider, and a
// stage nothing has ever measured keeps the answer the rest of this file derived
// and says that is what it is.
//
// The prior is named on the record rather than left blank because it is a real
// claim about the evidence: a published boot window, a rate over a byte count,
// and a workload's own declaration are all somebody's prediction, and none of
// them is a launch anyone watched. A reader cannot tell the two apart from the
// seconds, and a calibration reading the record has to know which answers it is
// allowed to grade.
func stagePredictor(input SchedulingInput, offer domain.OfferSnapshot) func(domain.LaunchStage, domain.Estimate) domain.Estimate {
	identity := domain.CandidateIdentityOf(offer, input.Image.Digest)
	return func(stage domain.LaunchStage, prior domain.Estimate) domain.Estimate {
		if answer := input.History.Predict(identity, stage); answer.Answered() {
			return answer.Estimate(input.ModelVersion)
		}
		prior.Level = domain.LevelPrior
		return prior
	}
}

// transferRates is what every stage of this launch that had bytes to move was
// priced at, in the order a launch moves them. A stage with nothing to move
// records nothing: there was no transfer, so there is no rate it was priced from,
// and an entry would name a number the decision never divided by.
//
// Every stage that did have bytes to move records one, with no exception, which is
// what lets a rule about who says so read every transfer this fleet ever priced.
// The exception this function carried for a while was a stage the fleet's own
// history had answered, and suppressing the rate was the wrong half of that
// collision to give way: seconds measured over one launch's byte count are not a
// prediction of another launch's, so the estimator no longer answers a stage
// priced from bytes at all and this account is complete again.
func transferRates(content candidateContent, registry, storage, store domain.LinkSpeed) []domain.TransferRate {
	priced := []domain.TransferRate{
		domain.TransferRateFor(domain.StageImageFetch, domain.NetworkScopeRegistry, content.image.TransferBytes, registry),
		// Assembly crosses no link, so it names no scope: the rate is a storage
		// rate, and a reader checking it against a measurement of a path would be
		// checking it against a measurement of something else.
		domain.TransferRateFor(domain.StageUnpack, "", content.image.UnpackBytes, storage),
		domain.TransferRateFor(domain.StageArtifactFetch, domain.NetworkScopeObjectStore, content.fetch, store),
	}
	return slices.DeleteFunc(priced, func(rate domain.TransferRate) bool { return rate.Bytes == 0 })
}

// containerStartEstimate is what asking this machine's container runtime for a
// process costs. It is Mercator's own stated assumption rather than anything a
// provider published, because no offer in any catalog says how long its runtime
// takes to create a container.
func containerStartEstimate(modelVersion string) domain.Estimate {
	return domain.Estimate{
		Expected:     domain.AssumedContainerStartSeconds,
		P50:          domain.AssumedContainerStartSeconds,
		P90:          domain.AssumedContainerStartSeconds * 1.25,
		Source:       "scheduler",
		ModelVersion: modelVersion,
	}
}

// applicationReadyEstimate is how long the workload said it takes to become
// ready once its process is running. Only the application knows: readiness is
// its own semantics, and no machine fact and no provider claim predicts it.
//
// A Run that declared nothing is predicted nothing, and the record says which
// silence that was. A prior of Mercator's here would be a number invented for
// every workload in the fleet, and unlike a link speed there is no shared
// physical thing it could be an assumption about.
func applicationReadyEstimate(input SchedulingInput) domain.Estimate {
	seconds := input.Workload.Spec.Placement.ExpectedReadySeconds
	estimate := domain.Estimate{
		Expected:     seconds,
		P50:          seconds,
		P90:          seconds,
		Source:       "workload.expected_ready",
		ModelVersion: input.ModelVersion,
	}
	if seconds <= 0 {
		estimate.Source = "workload_states_none"
	}
	return estimate
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
			FreeBytes:      offer.Resources.EphemeralDiskBytes,
			FreeBytesKnown: offer.Resources.EphemeralDiskKnown,
			ReservedBytes:  input.Workload.Spec.Resources.EphemeralDisk.MinBytes,
			LandBytes:      work.TransferBytes + fetch + caches,
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

// machineEstimates is what this candidate is predicted to spend becoming a
// machine Mercator can run a container on: the provider allocating it, the
// machine booting, and Mercator's node runtime enrolling.
type machineEstimates struct {
	acquisition domain.Estimate
	boot        domain.Estimate
	agentReady  domain.Estimate
}

// provisionEstimate is what somebody published about each stage of bringing this
// machine up. A quantile the provider did not state is its expectation restated:
// an unstated p90 is not a promise of a short tail, and replacing a published one
// with a spread of this model's own would enforce the Run's start bound against a
// quantile Mercator made up while the provider's own answer sat unread on the
// offer.
//
// The published claim is read as a claim about boot, because that is what its
// only publisher in this tree calls it: Shadeform states a min and max
// boot_in_sec for an instance type and nothing else about getting one. Acquiring
// the machine and enrolling Mercator's runtime on it are published by nobody, so
// they are predicted as nothing and the record says whose silence that was. A
// prior of Mercator's for either would be a number invented for every listing in
// every catalog, and a share of the published claim would attribute a provider's
// boot window to stages the provider never mentioned.
//
// The consequence is a machine prediction that is short of the truth by whatever
// acquisition and enrollment really take, which is a gap the record now makes
// visible rather than hiding inside one number: each stage has its own actual,
// and the calibration slice reads them.
func provisionEstimate(input SchedulingInput, offer domain.OfferSnapshot) machineEstimates {
	// A machine that already exists and already runs Mercator's runtime owes none
	// of these three stages, which is a different answer from nobody having
	// published them and reads differently on the record.
	source := "unpublished"
	if offer.Kind != domain.OfferKindProvisionable {
		source = "machine_exists"
	}
	machine := machineEstimates{
		acquisition: domain.Estimate{Source: source, ModelVersion: input.ModelVersion},
		boot:        domain.Estimate{Source: source, ModelVersion: input.ModelVersion},
		agentReady:  domain.Estimate{Source: source, ModelVersion: input.ModelVersion},
	}
	if offer.Kind != domain.OfferKindProvisionable || offer.Provisioning == nil {
		return machine
	}
	published := *offer.Provisioning
	machine.boot = domain.Estimate{
		Expected:     published.Expected,
		P50:          orExpected(published.P50, published.Expected),
		P90:          orExpected(published.P90, published.Expected),
		Source:       "offer",
		ModelVersion: input.ModelVersion,
	}
	return machine
}

func orExpected(quantile, expected float64) float64 {
	if quantile <= 0 {
		return expected
	}
	return quantile
}

// startEstimate assembles when this candidate's process begins out of the wait
// in front of it and the stages between the launch being taken and the container
// holding a process. Quantiles add rather than being scaled off the expectation,
// because each part's tail belongs to whoever published it: a provider that
// states an eighteen-minute p90 provisioning has said what its own tail is.
// Summing them is deliberately pessimistic about the joint distribution, which
// nothing here models, and pessimism about a bound the caller set is the safe
// direction.
//
// Application readiness is predicted and is not in this sum, because a start is
// a moment somebody observed and readiness is a later one. The actual this
// prediction is calibrated against is the container's own start, so folding a
// stage that happens after it into the same number would compare a prediction
// with an actual of something else.
//
// The sum is the only way this number is ever reached. What replaced it was a
// measured start for this offer snapshot ID, which nothing ever wrote and which
// would have been wrong if anything had: a start latency is the sum of seven
// stages whose costs depend on what the machine happens to hold now, so the
// measurement of a machine that pulled forty gigabytes last week would be served
// back as the prediction for the same machine now holding the image. History
// answers each stage on its own terms instead, and this adds them up.
func startEstimate(input SchedulingInput, queue domain.Estimate, stages domain.LaunchStageEstimates) domain.Estimate {
	start := domain.Estimate{Source: "scheduler", ModelVersion: input.ModelVersion}
	for _, part := range append([]domain.Estimate{queue}, startingStages(stages)...) {
		start.Expected += part.Expected
		start.P50 += part.P50
		start.P90 += part.P90
	}
	return start
}

// startingStages are the seven stages a launch goes through before its process
// is running, which is the moment a start latency is measured to.
func startingStages(stages domain.LaunchStageEstimates) []domain.Estimate {
	parts := make([]domain.Estimate, 0, len(domain.LaunchStages)-1)
	for _, stage := range domain.LaunchStages {
		if stage == domain.StageApplicationReady {
			continue
		}
		parts = append(parts, stages.Stage(stage))
	}
	return parts
}

// occupancy is when this Run would hold this candidate and for how long. The
// start it is measured from is the prediction the rest of this file just made,
// which is what makes the price of a committed second belong to the Run that
// really spends it: a Run queued behind an hour of work occupies the second hour
// of a commitment, not the first.
func occupancy(input SchedulingInput, start domain.Estimate) domain.Occupancy {
	expected, maximum := runtimeBounds(input.Workload)
	return domain.Occupancy{
		At:                input.EvaluatedAt,
		StartSeconds:      start.Expected,
		RuntimeSeconds:    expected,
		MaxRuntimeSeconds: maximum,
	}
}

// costEstimate is what Mercator's spend changes by if this Run occupies this
// machine, and the account of what that number is made of. A machine nobody
// quoted has no such number, and the estimate says so rather than predicting
// nothing: a rate of zero is a machine somebody says is free, and a machine
// Mercator would actually pay for is not that.
//
// Four terms, because a rate times a runtime is only one of them:
//
// Rent for seconds Mercator has already committed to is charged to whoever
// spends them. The invoice arrives either way, so the money is not what this
// decision changes; the seconds are, because nothing else can have them
// afterwards. That is what an owned machine's shadow price states, and it is why
// an idle owned machine is not free: its seconds are the scarce thing.
//
// Rent for seconds beyond that commitment is what this placement is what commits
// Mercator to, and it is bought in whatever increment the publisher sells. The
// part of that increment nothing will use is the idle tail, charged here rather
// than to nobody: an hourly machine asked for twenty minutes costs the hour, and
// a model that billed the twenty minutes reported two thirds of the bill to
// nobody.
//
// The setup fee is charged to capacity Mercator has to acquire and never to
// capacity it already holds, because a machine that is already running was
// already paid for. Charging it to every candidate priced an existing machine as
// though it were being bought again.
func costEstimate(input SchedulingInput, offer domain.OfferSnapshot, held domain.Occupancy) (domain.Estimate, []domain.CostTerm, domain.CommittedInterval) {
	if !offer.Pricing.Known {
		return domain.Estimate{Source: domain.CostUnpriced, ModelVersion: input.ModelVersion}, nil, domain.CommittedInterval{}
	}
	committed := committedInterval(offer.Terms, held)
	price, rate := offer.Pricing, offer.Pricing.RatePerSecondUSD
	keepAlive := held.RuntimeSeconds - committed.Seconds
	// The minimum charge is the smallest allocation this publisher sells, so it
	// binds only where Mercator is allocating something. A machine it already
	// holds has already paid whatever minimum its allocation carried.
	billed := price.BilledSeconds(math.Max(keepAlive, float64(minimumChargeSeconds(offer))))
	terms := []domain.CostTerm{
		{Name: domain.CostTermSetupFee, USD: acquisitionFee(offer)},
		{Name: domain.CostTermCommittedRent, USD: rate * committed.Seconds},
		{Name: domain.CostTermKeepAlive, USD: rate * keepAlive},
		{Name: domain.CostTermIdleTail, USD: rate * (billed - keepAlive)},
	}
	total := 0.0
	for _, term := range terms {
		total += term.USD
	}
	return domain.Estimate{
		Expected:     total,
		Source:       "price_model",
		ModelVersion: input.ModelVersion,
	}, terms, committed
}

// committedInterval is the already-owed rent this candidate met, and nothing at
// all for capacity nothing is owed on. The absence is stated as an absence
// because a commitment recorded with no moment in it would read as an interval
// that has already lapsed, and those are opposite answers: a machine whose
// interval ended pays for its next second, and a machine nobody has allocated
// pays for its first.
func committedInterval(terms domain.CapacityTerms, held domain.Occupancy) domain.CommittedInterval {
	if terms.CommittedUntil.IsZero() {
		return domain.CommittedInterval{}
	}
	return domain.CommittedInterval{
		Until:       terms.CommittedUntil,
		FromSeconds: held.StartSeconds,
		Seconds:     terms.CommittedSeconds(held),
	}
}

// acquisitionFee is what this publisher charges to hand over a machine, and
// nothing for a machine Mercator is already holding.
func acquisitionFee(offer domain.OfferSnapshot) float64 {
	if offer.Kind != domain.OfferKindProvisionable {
		return 0
	}
	return offer.Pricing.SetupFeeUSD
}

// minimumChargeSeconds is the shortest allocation this publisher sells, asked
// only of capacity Mercator would be allocating.
func minimumChargeSeconds(offer domain.OfferSnapshot) int64 {
	if offer.Kind != domain.OfferKindProvisionable {
		return 0
	}
	return offer.Pricing.MinimumChargeSeconds
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

// pricedRankingReason is the decision saying that a price, or the absence of one,
// decided this placement.
//
// The ranking asks whether a candidate has dollars before it compares any, so a
// machine nobody quoted ranks behind every machine somebody did. That rule is
// invisible in the score: the score is in dollars and an unpriced candidate has
// none of the only term that would separate it, so it reads as the cheapest thing
// in the fleet, and a reader ranking candidates on the number the ranking is
// stated in sees the winner beaten by the machine that lost. This is the record
// saying which rule it lost to, exactly as the class states why the costliest
// machine can win.
func pricedRankingReason(candidates []domain.CandidateDecision, bestIndex int) string {
	if !candidates[bestIndex].Priced() {
		return "UNPRICED_LAST_RESORT"
	}
	for _, candidate := range candidates {
		if candidate.Feasible && !candidate.Priced() {
			return "PRICED_BEFORE_UNPRICED"
		}
	}
	return ""
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

// imageEstimates prices the two stages this candidate still owes before a
// container can start on the image, and states how much each answer is worth.
// They are two stages rather than one because they are different work over
// different resources: fetching crosses a link from a registry, and unpacking
// turns bytes already on the disk into a layer chain. A host that fetched the
// image and never assembled it owes the second and none of the first, and
// charging it a pull would bill the network twice for bytes that are here.
//
// Zero seconds is reserved for a host an inventory says holds the image, or for
// an image nothing could resolve, where the same nothing is charged to every
// candidate and the comparison is unaffected. A host that will not say what it
// holds is charged the whole image, because the bytes have to come from
// somewhere and nothing here says they are already there.
//
// Confidence is about the duration, not the bytes. Bytes counted from a
// manifest and an inventory that both spoke are certain, so a host with nothing
// to do at a stage is certainly zero seconds away from finishing it. Bytes
// assumed because a host said nothing are not, and neither is a duration over a
// rate nothing measured, so either one caps that stage's answer at
// AssumedLinkConfidence.
//
// Room is not priced here, or anywhere. A machine short of it is refused rather
// than charged, because the only content it could give up to make room is
// content this Run needs back, and deleting that frees exactly what fetching it
// again consumes.
func imageEstimates(
	manifest domain.ImageManifest,
	offer domain.OfferSnapshot,
	content candidateContent,
	registry, storage domain.LinkSpeed,
	modelVersion string,
) (fetch, unpack contentWork) {
	work, locality := content.image, content.locality
	source := pullSource(locality, manifest, offer.Images)
	return imageStage(
			transferSeconds(work.TransferBytes, registry),
			work.TransferBytes,
			registry,
			source,
			locality,
			modelVersion,
		),
		imageStage(
			storage.TransferSeconds(work.UnpackBytes),
			work.UnpackBytes,
			storage,
			source,
			locality,
			modelVersion,
		)
}

// transferSeconds is how long bytes this host does not hold take to arrive over
// this link, plus the half second a transfer costs before any of them move. A
// host with nothing to fetch pays neither.
func transferSeconds(bytes int64, link domain.LinkSpeed) float64 {
	if bytes == 0 {
		return 0
	}
	return link.TransferSeconds(bytes) + 0.5
}

// imageStage is one stage of getting an image ready, priced from the bytes it
// has to move and the rate they move at.
func imageStage(seconds float64, bytes int64, rate domain.LinkSpeed, source string, locality domain.LocalityState, modelVersion string) contentWork {
	estimate := domain.Estimate{Source: source, ModelVersion: modelVersion}
	if bytes == 0 {
		if locality != domain.LocalityUnknown {
			estimate.Confidence = 1
		}
		return establishedIfDescribed(estimate, locality, rate)
	}
	estimate.Expected, estimate.P50, estimate.P90 = seconds, seconds, seconds*1.5
	estimate.Confidence = rate.Confidence
	if locality == domain.LocalityUnknown {
		estimate.Confidence = min(estimate.Confidence, domain.AssumedLinkConfidence)
	}
	return establishedIfDescribed(estimate, locality, rate)
}

// establishedIfDescribed splits one image prediction into the whole price and
// the part of it somebody established. A host that could not say what it holds
// is charged this whole image and establishes none of it: nothing said the bytes
// are here, and nothing said they are not. Every other answer counts bytes a
// manifest and an inventory both spoke about, for as long as the seconds they
// buy are established too.
func establishedIfDescribed(estimate domain.Estimate, locality domain.LocalityState, rate domain.LinkSpeed) contentWork {
	if locality == domain.LocalityUnknown {
		return contentWork{predicted: estimate}
	}
	return contentWork{predicted: estimate, established: establishedOverAMeasuredPath(estimate, rate)}
}

// establishedOverAMeasuredPath is the second half of the same question, asked of
// the rate rather than the bytes. Seconds are the product of the two and either
// one can be a silence, so a byte count a manifest and an inventory both spoke
// about still buys a duration nobody established when what divides it is
// Mercator's own fleet-wide prior.
//
// It exists because the established half is what a Run's hard start bound is
// allowed to refuse capacity on, and a machine nothing has measured the path of
// would otherwise be struck out for a number nothing on it answered for. That is
// silence about a path becoming infeasibility by arithmetic, which the goal
// forbids in the same words it forbids it for locality. Priced is as far as
// either silence may go, and the prediction still charges every one of those
// seconds, so the unmeasured machine never outranks one that measured a fast
// path.
//
// Nothing to move is nothing to wait for at any rate at all, so a stage with no
// bytes establishes its zero over a path nobody has ever described.
func establishedOverAMeasuredPath(estimate domain.Estimate, rate domain.LinkSpeed) domain.Estimate {
	if estimate.Expected > 0 && !rate.Measured() {
		return domain.Estimate{}
	}
	return estimate
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
func artifactEstimate(inventory domain.ArtifactInventory, content candidateContent, store domain.LinkSpeed, modelVersion string) contentWork {
	source := artifactSource(inventory, content.evidence)
	if len(content.evidence) == 0 {
		return contentWork{predicted: domain.Estimate{Source: source, ModelVersion: modelVersion}}
	}
	answered := objectStoreRead(establishedFetchBytes(content.evidence), store, source, modelVersion)
	return contentWork{
		predicted:   objectStoreRead(content.fetch, store, source, modelVersion),
		established: establishedOverAMeasuredPath(answered, store),
	}
}

// objectStoreRead is what reading these bytes out of the object store costs over
// the path this host reaches it on. Bytes that do not have to move cost nothing
// and there is no doubt about it; bytes that do are worth exactly what the rate
// they cross is worth, which is a measurement on a host that published one and
// Mercator's own assumption on a host that did not.
func objectStoreRead(bytes int64, store domain.LinkSpeed, source, modelVersion string) domain.Estimate {
	seconds := store.TransferSeconds(bytes)
	estimate := domain.Estimate{
		Expected:     seconds,
		P50:          seconds,
		P90:          seconds * 1.5,
		Confidence:   store.Confidence,
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

// artifactSource names whose evidence this answer rests on, and when it rests on
// none, whose silence it was. A host that cannot enumerate its copies and a host
// that enumerated and holds nothing are priced the same seconds and are
// different problems for an operator.
func artifactSource(inventory domain.ArtifactInventory, evidence []domain.ArtifactEvidence) string {
	switch {
	case len(evidence) == 0:
		// A Run that reads nothing is not a Run whose read nobody could answer for.
		// The stage costs no seconds either way, and the record has to say which of
		// the two it was, because a stage with no source is a stage nothing predicted.
		return "workload_reads_nothing"
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

// downloadFloorViolations is a Run's hard floor on how fast a candidate can reach
// content, and there are two ways to miss it that a decision must never state as
// one.
//
// A candidate whose published fact answers and falls below the floor was measured
// too slow, and the record says the number it published. A candidate nobody
// answered for measured nothing, and what that buys is the Run's own to decide:
// an unmeasured link is uncertainty, so AllowUnknown admits it, and the record
// says nobody answered rather than naming a speed nothing published. A number its
// own publisher disowned and an expired one are silence for that purpose, which is
// exactly what publishing nothing is, and the two must buy their publisher the
// same thing.
func downloadFloorViolations(now time.Time, req domain.NetworkDownloadRequirement, facts domain.NetworkFacts) []domain.Violation {
	fact, answered := req.Answer(facts, now)
	switch {
	case !answered && !req.AllowUnknown:
		return []domain.Violation{{
			Code:     "UNKNOWN_FACT",
			Path:     "network.download",
			Required: req.MinP10Mbps,
			Offered:  "unknown",
			Message:  "Nobody has published a download p10 for this offer's link that its own publisher stands behind, and this Run does not allow an unmeasured link.",
		}}
	case answered && fact.ValueMbps < req.MinP10Mbps:
		return []domain.Violation{{
			Code:     "NETWORK_FACT_UNSATISFIED",
			Path:     "network.download",
			Required: req.MinP10Mbps,
			Offered:  fact.ValueMbps,
			Message:  "Offer published a download p10 below the floor this Run states.",
		}}
	}
	return nil
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
