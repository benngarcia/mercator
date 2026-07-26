package scheduler

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/prediction"
)

func TestSchedulerSelectsLowestDeterministicScore(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	workload := schedulerRevision()
	workload.Spec.Placement.Class = domain.ClassInteractive
	input := SchedulingInput{
		RunID:        "run_1",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{schedulerOffer("off_slow", now, 0.00010, 40), schedulerOffer("off_fast", now, 0.00012, 5)},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	}

	decision, err := New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "off_fast" {
		t.Fatalf("expected off_fast to win, got %+v", decision)
	}
	if len(decision.Candidates) != 2 {
		t.Fatalf("expected two audited candidates, got %+v", decision.Candidates)
	}
	if decision.Booking == nil || decision.Booking.RentalID != "off_fast" || decision.Booking.State != domain.BookingStateRunning || decision.Booking.ScheduleVersion != 1 {
		t.Fatalf("selected standing Rental must receive its first running Booking, got %+v", decision.Booking)
	}

	again, err := New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate again: %v", err)
	}
	if decision.ID != again.ID || decision.SelectedOfferSnapshotID != again.SelectedOfferSnapshotID {
		t.Fatalf("scheduler is not deterministic:\nfirst=%+v\nsecond=%+v", decision, again)
	}
}

func TestSchedulerQueuesOnWarmRentalWhenReuseBeatsFreshCapacity(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	schedule, _, err := domain.NewRentalSchedule("rental-warm").Reserve(domain.BookingRequest{
		BookingID:              "booking-active",
		RunID:                  "run-active",
		ExpectedRuntimeSeconds: 30,
		MaxRuntimeSeconds:      60,
		ReservedAt:             now,
	})
	if err != nil {
		t.Fatalf("reserve active Booking: %v", err)
	}
	warm := schedulerOffer("offer-warm", now, 0.00002, 0)
	warm.RentalID = "rental-warm"
	warm.Capacity.Available = false
	warm.Queue = nil
	fresh := schedulerOffer("offer-fresh", now, 0.001, 0)
	fresh.Kind = domain.OfferKindProvisionable
	fresh.RentalID = ""
	fresh.Provisioning = &domain.Estimate{Expected: 120, P90: 150}

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:       "run-burst-1",
		Workload:    schedulerRevision(),
		Offers:      []domain.OfferSnapshot{fresh, warm},
		Schedules:   map[string]domain.RentalSchedule{"rental-warm": schedule},
		EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if decision.SelectedOfferSnapshotID != "offer-warm" {
		t.Fatalf("selected Offer = %q, want warm Rental", decision.SelectedOfferSnapshotID)
	}
	if decision.Booking == nil || decision.Booking.State != domain.BookingStateQueued || decision.Booking.RunID != "run-burst-1" || decision.Booking.AfterBookingID != "booking-active" || decision.Booking.ScheduleVersion != 2 {
		t.Fatalf("queued Booking = %+v", decision.Booking)
	}
	if candidate := findCandidate(t, decision, "offer-warm"); candidate.Disposition != domain.CandidateDispositionQueue {
		t.Fatalf("warm candidate disposition = %q", candidate.Disposition)
	}
	if !containsString(decision.SelectionReasonCodes, "QUEUE_EXISTING_RENTAL") {
		t.Fatalf("selection reasons = %v", decision.SelectionReasonCodes)
	}
}

func TestSchedulerMintsRentalForProvisionableOffer(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	offer := schedulerOffer("off_fresh", now, 0.00012, 5)
	offer.Kind = domain.OfferKindProvisionable
	offer.RentalID = ""
	offer.Provisioning = &domain.Estimate{Expected: 5}

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID: "run_1", Workload: schedulerRevision(), Offers: []domain.OfferSnapshot{offer}, ModelVersion: "latency-v1", EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Booking == nil || !strings.HasPrefix(decision.Booking.RentalID, "rnt_") || decision.Booking.State != domain.BookingStateRunning {
		t.Fatalf("provisionable Offer must mint a Rental and running Booking, got %+v", decision.Booking)
	}
}

// TestAStartBoundIsAskedOfThePublishedProvisioningTail is the one term of the
// start prediction somebody else publishes a quantile for. A provider that says
// this machine takes a minute on average and ten in its tail has answered the
// question a p90 bound asks, and the answer Mercator used to enforce was its own
// expectation scaled by a factor of its own: the Run was promised a p90 start of
// 76 seconds and recorded as compliant while the provider's published p90 was
// ten minutes.
func TestAStartBoundIsAskedOfThePublishedProvisioningTail(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	offer := schedulerOffer("off_slow_tail", now, 0.00012, 0)
	offer.Kind = domain.OfferKindProvisionable
	offer.RentalID = ""
	offer.Provisioning = &domain.Estimate{Expected: 60, P90: 600}
	workload := schedulerRevision()
	workload.Spec.Placement.MaxP90StartSeconds = 300

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID: "run_impatient", Workload: workload, Offers: []domain.OfferSnapshot{offer},
		ModelVersion: "latency-v1", EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	candidate := schedulerCandidate(t, decision, "off_slow_tail")
	if candidate.Estimates.Stages.Boot.P90 != 600 {
		t.Fatalf("the decision recorded a provisioning p90 of %v, and the provider published 600",
			candidate.Estimates.Stages.Boot.P90)
	}
	if candidate.Feasible {
		t.Fatalf("a machine whose own publisher says it takes ten minutes in the tail met a five-minute bound: %+v", candidate.Estimates)
	}
	if !slices.ContainsFunc(candidate.Rejections, func(rejection domain.Violation) bool {
		return rejection.Code == "LATENCY_SLO_EXCEEDED"
	}) {
		t.Fatalf("the candidate was refused for %+v", candidate.Rejections)
	}
	if !slices.Contains(decision.SelectionReasonCodes, "NO_FEASIBLE_OFFERS") {
		t.Fatalf("the decision recorded %v", decision.SelectionReasonCodes)
	}
}

// TestAQueueThatIsNearlyDoneIsAShortWait is the queue half of the same bound. The
// only Rental in the fleet is a minute from finishing an hour-long Booking, and
// the wait Mercator projects has to be the minute that is left rather than the
// hour its caller declared. Summing declared runtimes reported the same wait for
// the whole hour, so this Run was refused capacity it could have had in a minute
// and the decision said there was none.
func TestAQueueThatIsNearlyDoneIsAShortWait(t *testing.T) {
	reserved := time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC)
	now := reserved.Add(59 * time.Minute)
	schedule, _, err := domain.NewRentalSchedule("off_busy").Reserve(domain.BookingRequest{
		BookingID:              "booking-long",
		RunID:                  "run-long",
		ExpectedRuntimeSeconds: 3600,
		MaxRuntimeSeconds:      7200,
		ReservedAt:             reserved,
	})
	if err != nil {
		t.Fatalf("reserve the running Booking: %v", err)
	}
	// Its workload started when it was placed. Both runtimes a Booking declares
	// bound a container, so nothing has elapsed against them until the machine
	// says there is one.
	schedule, err = schedule.Started("booking-long", reserved)
	if err != nil {
		t.Fatalf("record the workload running: %v", err)
	}
	offer := schedulerOffer("off_busy", now, 0.0001, 0)
	workload := schedulerRevision()
	workload.Spec.Placement.MaxP90StartSeconds = 180

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_impatient",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{offer},
		Schedules:    map[string]domain.RentalSchedule{"off_busy": schedule},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	candidate := schedulerCandidate(t, decision, "off_busy")
	if candidate.Estimates.QueueSeconds.Expected != 60 {
		t.Fatalf("the decision projected %v seconds of waiting for a Booking a minute from its own expected finish",
			candidate.Estimates.QueueSeconds.Expected)
	}
	if !candidate.Feasible {
		t.Fatalf("the Run was refused a machine a minute from free: %+v", candidate.Rejections)
	}
	if candidate.Disposition != domain.CandidateDispositionQueue {
		t.Fatalf("the candidate was recorded as %q, and there is a Booking to wait behind", candidate.Disposition)
	}
}

func TestSchedulerRejectsStandingOfferWithoutRentalIdentity(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	offer := schedulerOffer("off_orphaned", now, 0.00012, 5)
	offer.RentalID = ""

	_, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID: "run_1", Workload: schedulerRevision(), Offers: []domain.OfferSnapshot{offer}, ModelVersion: "latency-v1", EvaluatedAt: now,
	})
	if err == nil || !strings.Contains(err.Error(), "requires rental_id") {
		t.Fatalf("evaluate error = %v, want missing Rental identity", err)
	}
}

func TestSchedulerReportsHardRejections(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	stale := schedulerOffer("off_stale", now, 0.01, 1)
	stale.ExpiresAt = now.Add(-time.Second)
	wrongPlatform := schedulerOffer("off_platform", now, 0.01, 1)
	wrongPlatform.Platform = domain.Platform{OS: "linux", Architecture: "arm64"}
	noInbound := schedulerOffer("off_no_inbound", now, 0.01, 1)
	noInbound.Capabilities.Network.Inbound = domain.InboundNetworkNone
	unknownNetwork := schedulerOffer("off_unknown_network", now, 0.01, 1)
	unknownNetwork.Network.Download = nil

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     schedulerRevision(),
		Offers:       []domain.OfferSnapshot{stale, wrongPlatform, noInbound, unknownNetwork},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "" {
		t.Fatalf("expected no selected offer, got %+v", decision)
	}
	assertCandidateRejected(t, decision, "off_stale", "OFFER_EXPIRED", "expires_at")
	assertCandidateRejected(t, decision, "off_platform", "CAPABILITY_MISMATCH", "platform")
	assertCandidateRejected(t, decision, "off_no_inbound", "CAPABILITY_MISMATCH", "network.inbound")
	assertCandidateRejected(t, decision, "off_unknown_network", "UNKNOWN_FACT", "network.download")
}

func TestSchedulerRejectsEntrypointOverrideOnIncapableOffer(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	offer := schedulerOffer("off_no_entrypoint", now, 0.01, 1)
	offer.Capabilities.Container.SupportsEntrypointOverride = false
	workload := schedulerRevision()
	entrypoint := []string{"/bin/worker"}
	workload.Spec.Containers[0].Entrypoint = &entrypoint

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{offer},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "" {
		t.Fatalf("an entrypoint-overriding workload must not land on an offer that cannot override entrypoints: %+v", decision)
	}
	assertCandidateRejected(t, decision, "off_no_entrypoint", "CAPABILITY_MISMATCH", "container.supports_entrypoint_override")
}

func TestSchedulerRejectsConservativeFactAndResourceGaps(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	rev := schedulerRevision()
	rev.Spec.Resources.Accelerators = []domain.AcceleratorRequirement{{
		Vendor: "nvidia", ModelAnyOf: []string{"nvidia-a10"}, Count: 1, MemoryMinBytes: 16 << 30,
	}}
	maxCost := 0.05
	rev.Spec.Placement.MaxExpectedCostUSD = &maxCost

	noGPU := schedulerOffer("off_no_gpu", now, 0.00001, 1)
	zeroMaxContainers := schedulerOffer("off_zero_containers", now, 0.00001, 1)
	zeroMaxContainers.Capabilities.Container.MaxContainers = 0
	unavailable := schedulerOffer("off_unavailable", now, 0.00001, 1)
	unavailable.Capacity.Available = false
	tooExpensive := schedulerOffer("off_too_expensive", now, 0.001, 1)
	tooExpensive.Resources.Accelerators = []domain.AcceleratorInventory{{Vendor: "nvidia", Model: "a10", CanonicalModel: "nvidia-a10", Count: 1, MemoryBytes: 24 << 30}}
	tooExpensive.Capabilities.Resources.GPUVendors = []string{"nvidia"}
	tooExpensive.Images.Known = true

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     rev,
		Offers:       []domain.OfferSnapshot{noGPU, zeroMaxContainers, unavailable, tooExpensive},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "" {
		t.Fatalf("expected no selected offer, got %+v", decision)
	}
	assertCandidateRejected(t, decision, "off_no_gpu", "RESOURCE_INSUFFICIENT", "resources.accelerators")
	assertCandidateRejected(t, decision, "off_zero_containers", "UNKNOWN_FACT", "container.max_containers")
	assertCandidateRejected(t, decision, "off_unavailable", "CAPACITY_UNAVAILABLE", "capacity.available")
	assertCandidateRejected(t, decision, "off_too_expensive", "COST_LIMIT_EXCEEDED", "placement.max_expected_cost_usd")
}

func TestSchedulerMatchesAcceleratorByCanonicalModelAndNormalizedVendor(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)

	// Offer advertises the GPU with the provider's RAW vendor casing and the
	// canonical model id. Matching must normalize the vendor and compare the
	// canonical model — not the raw Model string.
	gpu := schedulerOffer("off_gpu", now, 0.00001, 1)
	gpu.Resources.Accelerators = []domain.AcceleratorInventory{{
		Vendor: "NVIDIA", Model: "RTX A2000", CanonicalModel: "nvidia-a2000", Count: 1, MemoryBytes: 6 << 30,
	}}
	gpu.Capabilities.Resources.GPUVendors = []string{"nvidia"}

	rev := schedulerRevision()
	rev.Spec.Resources.Accelerators = []domain.AcceleratorRequirement{{
		Vendor: "nvidia", ModelAnyOf: []string{"nvidia-a2000"}, Count: 1,
	}}
	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID: "run_1", Workload: rev, Offers: []domain.OfferSnapshot{gpu}, ModelVersion: "latency-v1", EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "off_gpu" {
		t.Fatalf("canonical GPU match should select off_gpu, got %q", decision.SelectedOfferSnapshotID)
	}

	// A requirement for a DIFFERENT canonical model must not match this offer.
	revH := schedulerRevision()
	revH.Spec.Resources.Accelerators = []domain.AcceleratorRequirement{{
		Vendor: "nvidia", ModelAnyOf: []string{"nvidia-h100"}, Count: 1,
	}}
	dec2, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID: "run_2", Workload: revH, Offers: []domain.OfferSnapshot{gpu}, ModelVersion: "latency-v1", EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if dec2.SelectedOfferSnapshotID != "" {
		t.Fatalf("a different canonical model must not match, got %q", dec2.SelectedOfferSnapshotID)
	}
}

func TestSchedulerNormalizesRequirementModelSpellings(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)

	gpu := schedulerOffer("off_gpu", now, 0.00001, 1)
	gpu.Resources.Accelerators = []domain.AcceleratorInventory{{
		Vendor: "NVIDIA", Model: "NVIDIA GeForce RTX 5090", CanonicalModel: "nvidia-rtx-5090", Count: 1, MemoryBytes: 32 << 30,
	}}
	gpu.Capabilities.Resources.GPUVendors = []string{"nvidia"}

	// A requirement spelled any way gpunorm can resolve — marketing name,
	// separator-free id, or canonical id — must match the same inventory.
	for _, spelling := range []string{"nvidia-rtx-5090", "nvidia-rtx5090", "RTX 5090"} {
		rev := schedulerRevision()
		rev.Spec.Resources.Accelerators = []domain.AcceleratorRequirement{{
			Vendor: "nvidia", ModelAnyOf: []string{spelling}, Count: 1,
		}}
		decision, err := New().Evaluate(context.Background(), SchedulingInput{
			RunID: "run_1", Workload: rev, Offers: []domain.OfferSnapshot{gpu}, ModelVersion: "latency-v1", EvaluatedAt: now,
		})
		if err != nil {
			t.Fatalf("evaluate(%q): %v", spelling, err)
		}
		if decision.SelectedOfferSnapshotID != "off_gpu" {
			t.Fatalf("requirement spelling %q must match canonical inventory, got %q", spelling, decision.SelectedOfferSnapshotID)
		}
	}

	// Normalization must not loosen matching: a nearby but different model
	// still rejects.
	rev := schedulerRevision()
	rev.Spec.Resources.Accelerators = []domain.AcceleratorRequirement{{
		Vendor: "nvidia", ModelAnyOf: []string{"RTX 5080"}, Count: 1,
	}}
	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID: "run_2", Workload: rev, Offers: []domain.OfferSnapshot{gpu}, ModelVersion: "latency-v1", EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "" {
		t.Fatalf("a different model spelling must not match, got %q", decision.SelectedOfferSnapshotID)
	}
}

func TestSchedulerAllowsUnknownNetworkWhenPolicyAllowsIt(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	rev := schedulerRevision()
	rev.Spec.Network.Download.AllowUnknown = true
	offer := schedulerOffer("off_unknown_network", now, 0.01, 1)
	offer.Network.Download = nil

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     rev,
		Offers:       []domain.OfferSnapshot{offer},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "off_unknown_network" {
		t.Fatalf("expected unknown network offer to be selected, got %+v", decision)
	}
}

// TestTheClassPricesTheDoubtInTheAnswersItWasGiven is the uncertainty term
// firing. Two machines five seconds from ready, the cheaper of them publishing a
// capacity claim it is only forty percent sure of, and an interactive Run that
// would rather pay than act on that. The shortfall is priced from the class's own
// exchange rate, so the machine that costs 0.02 USD less over this Run is not
// worth the doubt.
//
// The reliability rates on both offers are deliberately unpriced. Probability
// times the cost of starting again is a derivation over a prediction, and any
// flat dollar penalty invented for it now would be exactly the unmeasured
// constant this model keeps deleting.
func TestTheClassPricesTheDoubtInTheAnswersItWasGiven(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	stable := schedulerOffer("off_stable", now, 0.00012, 5)
	risky := schedulerOffer("off_risky", now, 0.00010, 5)
	risky.Capacity.Confidence = 0.4
	workload := schedulerRevision()
	workload.Spec.Placement.Class = domain.ClassInteractive

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{risky, stable},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "off_stable" {
		t.Fatalf("the cheaper machine won on a capacity claim it is 40%% sure of, got %+v", decision)
	}
	doubted := findCandidate(t, decision, "off_risky")
	if doubted.Uncertainty() != 0.6 {
		t.Fatalf("the doubted candidate carries %v points of uncertainty, and its capacity claim was worth 0.4: %+v",
			doubted.Uncertainty(), doubted.Confidences)
	}
	if doubted.ScoreUSD <= findCandidate(t, decision, "off_stable").ScoreUSD {
		t.Fatalf("doubt was priced at nothing: %+v", decision.Candidates)
	}
	if decision.Weights != domain.ClassInteractive.Weights() {
		t.Fatalf("the decision recorded weights %+v, and it scored at %+v", decision.Weights, domain.ClassInteractive.Weights())
	}
}

// TestAMeasuredStageIsAnsweredFromTheMachineAndNotFromTheListing is the whole
// point of keying a history on what recurs, asked of the scheduler directly. The
// machine was measured under one listing ID and is offered under another, which
// is what a marketplace search does, and the answer has to arrive anyway.
func TestAMeasuredStageIsAnsweredFromTheMachineAndNotFromTheListing(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	measured := schedulerOffer("off_ask_11111", now, 0.00010, 0)
	measured.MachineID = "machine-77"
	republished := schedulerOffer("off_ask_99999", now, 0.00010, 0)
	republished.MachineID = "machine-77"
	image := schedulerRevision().Spec.Containers[0].Image

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     schedulerRevision(),
		Offers:       []domain.OfferSnapshot{republished},
		Image:        domain.ImageManifest{Digest: image},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
		History: prediction.NewHistory([]prediction.Observation{{
			Candidate: domain.CandidateIdentityOf(measured, image),
			Stage:     domain.StageApplicationReady,
			Seconds:   42,
		}}),
	})

	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	ready := findCandidate(t, decision, "off_ask_99999").Estimates.Stages.ApplicationReady
	if ready.Level != domain.LevelExactCandidate || ready.SampleCount != 1 || ready.Expected != 42 {
		t.Fatalf("the machine was measured at 42s and its next listing was answered %+v", ready)
	}
	if strings.Contains(ready.Key, "off_ask_") {
		t.Fatalf("the answer names the listing %q, and a listing does not recur", ready.Key)
	}
}

// TestAStageNobodyMeasuredNamesThePrior holds the other half: an answer with no
// history behind it says so, rather than leaving a reader to tell a measurement
// from an assumption by the seconds.
func TestAStageNobodyMeasuredNamesThePrior(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     schedulerRevision(),
		Offers:       []domain.OfferSnapshot{schedulerOffer("off_unmeasured", now, 0.00010, 0)},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})

	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	candidate := findCandidate(t, decision, "off_unmeasured")
	for _, stage := range domain.LaunchStages {
		answered := candidate.Estimates.Stages.Stage(stage)
		if answered.Level != domain.LevelPrior || answered.SampleCount != 0 || answered.Key != "" {
			t.Fatalf("nothing has measured this machine and its %s stage answered %+v", stage, answered)
		}
	}
}

func TestSchedulerDecisionStableAcrossOfferOrder(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	offers := []domain.OfferSnapshot{
		schedulerOffer("off_slow", now, 0.00010, 40),
		schedulerOffer("off_fast", now, 0.00012, 5),
	}
	workload := schedulerRevision()
	workload.Spec.Placement.Class = domain.ClassInteractive
	forward, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     workload,
		Offers:       offers,
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate forward: %v", err)
	}
	reversed, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{offers[1], offers[0]},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate reversed: %v", err)
	}
	if forward.ID != reversed.ID || forward.SelectedOfferSnapshotID != reversed.SelectedOfferSnapshotID {
		t.Fatalf("same offer set should produce same decision identity and selection:\nforward=%+v\nreversed=%+v", forward, reversed)
	}
	if got, want := candidateIDs(reversed), candidateIDs(forward); got != want {
		t.Fatalf("same offer set should produce stable candidate order: forward=%s reversed=%s", want, got)
	}
}

func TestSchedulerPopulatesDeterministicCollectionAndCandidateAuditData(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	offB := schedulerOffer("off_b", now, 0.00010, 10)
	offB.ConnectionID = "conn_b"
	offB.NativeRef = "native_b"
	offA := schedulerOffer("off_a", now, 0.00010, 10)
	offA.ConnectionID = "conn_a"
	offA.NativeRef = "native_a"

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     schedulerRevision(),
		Offers:       []domain.OfferSnapshot{offB, offA},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := strings.Join(decision.CollectionReport.ConnectionsQueried, ","); got != "conn_a,conn_b" {
		t.Fatalf("expected deterministic collection report, got %+v", decision.CollectionReport)
	}
	first := decision.Candidates[0]
	if first.OfferSnapshotID != "off_a" || first.ConnectionID != "conn_a" || first.AdapterType != "fake" || first.NativeRef != "native_a" {
		t.Fatalf("candidate audit data missing or unstable: %+v", decision.Candidates)
	}
}

func schedulerRevision() domain.WorkloadRevision {
	return domain.WorkloadRevision{
		ID:          "wrev_1",
		WorkspaceID: "ws_1",
		WorkloadID:  "wrk_1",
		Digest:      "sha256:revision",
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "ghcr.io/acme/inference@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
				Ports: []domain.PortSpec{{
					Name: "http", ContainerPort: 8080, Protocol: "tcp", Exposure: domain.PortExposurePublic,
				}},
			}},
			Resources: domain.ResourceRequirements{
				CPU:           domain.CPURequirement{MinMillis: 4000},
				Memory:        domain.MemoryRequirement{MinBytes: 8 << 30},
				EphemeralDisk: domain.DiskRequirement{MinBytes: 40 << 30},
			},
			Network: domain.NetworkRequirements{
				Inbound: domain.InboundNetworkPublicPort,
				Download: &domain.NetworkDownloadRequirement{
					Scope:                    domain.NetworkScopeRegistry,
					MinP10Mbps:               500,
					MaxMeasurementAgeSeconds: 86400,
					AllowUnknown:             false,
				},
			},
			Placement: domain.PlacementPolicy{Class: domain.ClassStandard, MaxP90StartSeconds: 180, ExpectedRuntimeSeconds: 900},
		},
	}
}

func schedulerOffer(id string, now time.Time, ratePerSecondUSD float64, startSeconds float64) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:           id,
		RentalID:     id,
		ConnectionID: "conn_1",
		AdapterType:  "fake",
		Kind:         domain.OfferKindStanding,
		Lane:         domain.LaneReusable,
		ObservedAt:   now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Minute),
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          8000,
			MemoryBytes:        16 << 30,
			EphemeralDiskBytes: 80 << 30,
		},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: 32768},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkPublicPort, Protocols: []string{"tcp"}, PublicIPv4: true},
			Pricing:   domain.PricingCapabilities{Known: true},
			Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "deterministic_name", ListOwned: true, CancelQueued: true},
		},
		Network: domain.NetworkFacts{Download: []domain.NetworkFact{{
			Scope:      domain.NetworkScopeRegistry,
			Statistic:  "p10",
			ValueMbps:  750,
			ObservedAt: now.Add(-time.Hour),
			ValidUntil: now.Add(time.Hour),
			Confidence: 0.9,
		}}},
		Pricing:  domain.PriceModel{Currency: "USD", RatePerSecondUSD: ratePerSecondUSD, Known: true, GranularitySeconds: 1},
		Queue:    &domain.QueueSnapshot{QueuedWorkSeconds: startSeconds},
		Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
		Images:   domain.ImageInventory{Known: true},
		Reliability: domain.ReliabilityEvidence{
			StartFailures: domain.StatedRate{Rate: 0.01, Confidence: 1},
			Interruptions: domain.StatedRate{Rate: 0.01, Confidence: 1},
		},
	}
}

func candidateIDs(decision domain.BookingDecision) string {
	ids := ""
	for _, candidate := range decision.Candidates {
		if ids != "" {
			ids += ","
		}
		ids += candidate.OfferSnapshotID
	}
	return ids
}

func assertCandidateRejected(t *testing.T, decision domain.BookingDecision, offerID, code, path string) {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID != offerID {
			continue
		}
		for _, rejection := range candidate.Rejections {
			if rejection.Code == code && rejection.Path == path {
				return
			}
		}
		t.Fatalf("candidate %s missing rejection code=%s path=%s: %+v", offerID, code, path, candidate)
	}
	t.Fatalf("candidate %s not found in %+v", offerID, decision.Candidates)
}

func findCandidate(t *testing.T, decision domain.BookingDecision, offerID string) domain.CandidateDecision {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate
		}
	}
	t.Fatalf("candidate %s not found in %+v", offerID, decision.Candidates)
	return domain.CandidateDecision{}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// TestArtifactLocalityDecidesBetweenOtherwiseIdenticalHosts is the whole point
// of Artifact evidence at placement time. Two machines are identical down to the
// image they hold and the price they charge, and one of them is already holding
// a checked copy of the 40GB dataset this Run reads. Nothing else can separate
// them, so the decision has to reach the right answer through the transfer it
// prices rather than through a tie-break on offer ID.
func TestArtifactLocalityDecidesBetweenOtherwiseIdenticalHosts(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	dataset := schedulerArtifact()
	// "off_cold" sorts before "off_holder", so an untouched tie-break picks the
	// machine holding nothing and this case would pass for the wrong reason.
	cold := schedulerOffer("off_cold", now, 0.0001, 0)
	cold.Artifacts = domain.ArtifactInventory{Known: true, ObservedAt: now}
	holder := schedulerOffer("off_holder", now, 0.0001, 0)
	holder.Artifacts = schedulerHolds(dataset, domain.ArtifactReplicaVerified, now)

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_dataset",
		Workload:     schedulerConsumingRevision(dataset.ID),
		Artifacts:    []domain.ArtifactVersion{dataset},
		Offers:       []domain.OfferSnapshot{cold, holder},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if decision.SelectedOfferSnapshotID != "off_holder" {
		t.Fatalf("expected the machine holding the dataset to win, got %q", decision.SelectedOfferSnapshotID)
	}
	warm := schedulerCandidate(t, decision, "off_holder")
	if len(warm.ArtifactEvidence) != 1 || warm.ArtifactEvidence[0].Locality != domain.LocalityHot {
		t.Fatalf("the holder recorded %+v", warm.ArtifactEvidence)
	}
	if warm.Estimates.Stages.ArtifactFetch.Expected != 0 || warm.Estimates.Stages.ArtifactFetch.Confidence != 1 {
		t.Fatalf("a host holding a checked copy was priced %+v, and it owes nothing", warm.Estimates.Stages.ArtifactFetch)
	}
	// 40GB at the assumed 500 Mbps is 640 seconds, over a link nothing measured.
	empty := schedulerCandidate(t, decision, "off_cold")
	if empty.Estimates.Stages.ArtifactFetch.Expected != 640 || empty.Estimates.Stages.ArtifactFetch.Confidence != domain.AssumedLinkConfidence {
		t.Fatalf("a host holding no copy was priced %+v", empty.Estimates.Stages.ArtifactFetch)
	}
	if empty.ArtifactEvidence[0].FetchBytes != dataset.SizeBytes {
		t.Fatalf("a host holding no copy owes %d bytes, and the version is %d", empty.ArtifactEvidence[0].FetchBytes, dataset.SizeBytes)
	}
}

// TestAHostThatCannotEnumerateItsCopiesRecordsUnknownAndNotZero is where silence
// costs what absence costs. A machine nothing of Mercator's runs on holds
// whatever it holds and can report none of it, and the one answer that would be
// wrong is zero: that scores a machine nobody can describe exactly like one
// provably holding the content.
func TestAHostThatCannotEnumerateItsCopiesRecordsUnknownAndNotZero(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	dataset := schedulerArtifact()
	silent := schedulerOffer("off_silent", now, 0.0001, 0)
	silent.Artifacts = domain.ArtifactInventory{}

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_dataset",
		Workload:     schedulerConsumingRevision(dataset.ID),
		Artifacts:    []domain.ArtifactVersion{dataset},
		Offers:       []domain.OfferSnapshot{silent},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	candidate := schedulerCandidate(t, decision, "off_silent")
	if len(candidate.ArtifactEvidence) != 1 || candidate.ArtifactEvidence[0].Locality != domain.LocalityUnknown {
		t.Fatalf("a machine that cannot enumerate its copies recorded %+v", candidate.ArtifactEvidence)
	}
	if candidate.Estimates.Stages.ArtifactFetch.Expected != 640 {
		t.Fatalf("silence was priced %v seconds, and absence costs 640", candidate.Estimates.Stages.ArtifactFetch.Expected)
	}
	if candidate.Estimates.Stages.ArtifactFetch.Source != "inventory_unknown" {
		t.Fatalf("the estimate names its source %q, and this one rests on a machine nobody could ask", candidate.Estimates.Stages.ArtifactFetch.Source)
	}
	if !candidate.Feasible {
		t.Fatalf("a machine that cannot say what it holds was refused: %+v", candidate.Rejections)
	}
}

func schedulerArtifact() domain.ArtifactVersion {
	const id = "artifact:imagenet:v2.41"
	return domain.ArtifactVersion{
		ID:            id,
		WorkspaceID:   "ws_scheduler",
		ContentDigest: "sha256:1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a",
		SizeBytes:     40_000_000_000,
		Location:      domain.ArtifactLocation("ws_scheduler", id),
		PublishedAt:   time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC),
	}
}

func schedulerHolds(version domain.ArtifactVersion, state domain.ArtifactReplicaState, now time.Time) domain.ArtifactInventory {
	return domain.ArtifactInventory{
		Known:      true,
		ObservedAt: now,
		Replicas: []domain.ArtifactReplica{{
			ArtifactID:    version.ID,
			ContentDigest: version.ContentDigest,
			SizeBytes:     version.SizeBytes,
			State:         state,
			VerifiedAt:    now,
		}},
	}
}

func schedulerConsumingRevision(artifactID string) domain.WorkloadRevision {
	revision := schedulerRevision()
	revision.Spec.Artifacts = domain.ArtifactRequirements{Consumes: []string{artifactID}}
	return revision
}

func schedulerCandidate(t *testing.T, decision domain.BookingDecision, offerID string) domain.CandidateDecision {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate
		}
	}
	t.Fatalf("the decision records no candidate for %q", offerID)
	return domain.CandidateDecision{}
}

// TestTheServiceClassDecidesWhichCandidateWins is what a Run's stated class does
// to a placement. Every row is the same two offers: one a fifth of a cent cheaper
// an hour and forty seconds from ready, the other pricier and five seconds from
// ready. What differs is the exchange rate each class declares, and the winner
// changes with it, which is what a class is for.
//
// Before the class carried the rate, every one of these rows returned the same
// machine: the score's only time term was a weight nothing populated, so
// "fastest_start" was a word the public API accepted and nothing read.
func TestTheServiceClassDecidesWhichCandidateWins(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	for _, choice := range []struct {
		class  domain.ServiceClass
		winner string
	}{
		// Somebody is watching, so thirty-five seconds is worth 0.35 USD and the
		// cheaper machine is not.
		{domain.ClassInteractive, "off_fast"},
		// Waiting costs what the machine costs, which is less than the price gap.
		{domain.ClassStandard, "off_slow"},
		{domain.ClassBatch, "off_slow"},
		// The answer is the point and the next iteration is behind it.
		{domain.ClassExperimental, "off_fast"},
		// Waiting is free, so this is the price and nothing else.
		{domain.ClassOpportunistic, "off_slow"},
	} {
		t.Run(string(choice.class), func(t *testing.T) {
			workload := schedulerRevision()
			workload.Spec.Placement.Class = choice.class

			decision, err := New().Evaluate(context.Background(), SchedulingInput{
				RunID:        "run_1",
				Workload:     workload,
				Offers:       []domain.OfferSnapshot{schedulerOffer("off_slow", now, 0.00010, 40), schedulerOffer("off_fast", now, 0.00012, 5)},
				ModelVersion: "latency-v1",
				EvaluatedAt:  now,
			})
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if decision.SelectedOfferSnapshotID != choice.winner {
				t.Fatalf("a %q Run landed on %q", choice.class, decision.SelectedOfferSnapshotID)
			}
			if !slices.Contains(decision.SelectionReasonCodes, choice.class.SelectionReason()) {
				t.Fatalf("the decision recorded %v, and it was scored at the %q class's rates",
					decision.SelectionReasonCodes, choice.class)
			}
			if decision.Weights != choice.class.Weights() {
				t.Fatalf("the decision recorded weights %+v, and this class declares %+v", decision.Weights, choice.class.Weights())
			}
		})
	}
}

// TestAClassMercatorCannotPriceIsRefusedRatherThanRanked is the other half of the
// same rule. Scoring a Run whose class declares nothing ranks every candidate on
// price alone and records a reason naming a class nothing declared, which is the
// silent fallback this replaced: a caller would learn their word was ignored from
// the bill. CreateRun refuses such a Run at the door, so reaching Placement means
// a revision was stored by something that did not ask.
func TestAClassMercatorCannotPriceIsRefusedRatherThanRanked(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	workload := schedulerRevision()
	workload.Spec.Placement.Class = "urgent"

	_, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{schedulerOffer("off_slow", now, 0.00010, 40)},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})

	if err == nil || !strings.Contains(err.Error(), `service class "urgent"`) {
		t.Fatalf("evaluating a Run of an unknown class returned %v", err)
	}
}

// TestEqualPricesAreDecidedByWhatEachCandidateHolds is the case Artifact locality
// was added for and the one a pure cost ranking cannot answer. Two machines at one
// price, one of them forty seconds from ready, and a batch Run that values a
// second of waiting at a fifth of the machine's rent. That fifth is what turns
// locality into dollars; without any rate at all the winner is whichever offer ID
// sorts first and every locality answer in the decision is arithmetic nobody read.
func TestEqualPricesAreDecidedByWhatEachCandidateHolds(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 31, 22, 0, time.UTC)
	workload := schedulerRevision()
	workload.Spec.Placement.Class = domain.ClassBatch

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_1",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{schedulerOffer("off_a_slow", now, 0.0001, 40), schedulerOffer("off_b_ready", now, 0.0001, 1)},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "off_b_ready" {
		t.Fatalf("two machines at one price were decided by %q rather than by how ready each is", decision.SelectedOfferSnapshotID)
	}
	if findCandidate(t, decision, "off_b_ready").ScoreUSD >= findCandidate(t, decision, "off_a_slow").ScoreUSD {
		t.Fatalf("the readier machine did not score lower, so the offer ID decided it: %+v", decision.Candidates)
	}
}

// TestADownloadFloorRefusesOnlyWhatWasMeasuredTooSlow is the difference between the
// two ways a candidate can miss a floor, which the record has to keep apart. The
// machine that published 100 Mbps was measured too slow and is refused with the
// number it published. The machines nobody answered for measured nothing, and this
// Run allows an unmeasured link, so each is admitted exactly as the machine that
// published nothing at all is: a number its own publisher disowned and an expired
// one are the silence AllowUnknown was asked about, and a disowned fact that struck
// a candidate out where an identical silence was admitted made publishing a number
// you disown strictly worse than saying nothing.
func TestADownloadFloorRefusesOnlyWhatWasMeasuredTooSlow(t *testing.T) {
	now := time.Now().UTC()
	workload := schedulerRevision()
	workload.Spec.Network.Download.AllowUnknown = true
	slow := schedulerOffer("off_slow", now, 0.0002, 0)
	slow.Network.Download[0].ValueMbps = 100
	disowned := schedulerOffer("off_disowned", now, 0.0002, 0)
	disowned.Network.Download[0].ValueMbps, disowned.Network.Download[0].Confidence = 5000, 0
	expired := schedulerOffer("off_expired", now, 0.0002, 0)
	expired.Network.Download[0].ValidUntil = now.Add(-time.Minute)
	silent := schedulerOffer("off_silent", now, 0.0002, 0)
	silent.Network.Download = nil

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_floor_allowed",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{slow, disowned, expired, silent},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	for _, id := range []string{"off_disowned", "off_expired", "off_silent"} {
		candidate := findCandidate(t, decision, id)
		if !candidate.Feasible {
			t.Errorf("%s was refused as %+v, and nobody measured its link: this Run allows that, and the machine that published nothing is feasible", id, candidate.Rejections)
		}
	}
	refused := findCandidate(t, decision, "off_slow")
	if refused.Feasible {
		t.Fatalf("the machine that published 100 Mbps cleared a 500 Mbps floor")
	}
	rejection := refused.Rejections[0]
	if rejection.Code != "NETWORK_FACT_UNSATISFIED" || rejection.Offered != 100.0 {
		t.Fatalf("the record says %+v, and an operator reading it has to see the speed the machine published", rejection)
	}
}

// TestARunsDownloadFloorIsNotClearedByADisownedFact is the hard half of the same
// rule the score follows. A Run that states a floor on how fast a candidate
// reaches content has said it would rather not run than run below it, and a
// publisher that puts no confidence in its own number has measured nothing. The
// two offers here publish the same 5 Gbps: one stands behind it and clears the
// floor, one disowns it and is refused as the silence it is, which is what a Run
// asked for when it set AllowUnknown false.
func TestARunsDownloadFloorIsNotClearedByADisownedFact(t *testing.T) {
	now := time.Now().UTC()
	workload := schedulerRevision()
	stood := schedulerOffer("off_stood", now, 0.0002, 0)
	stood.Network.Download[0].ValueMbps, stood.Network.Download[0].Confidence = 5000, 0.9
	disowned := schedulerOffer("off_disowned", now, 0.0001, 0)
	disowned.Network.Download[0].ValueMbps, disowned.Network.Download[0].Confidence = 5000, 0

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_floor",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{stood, disowned},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if decision.SelectedOfferSnapshotID != "off_stood" {
		t.Errorf("the placement chose %q, and the cheaper machine cleared the floor with a number nobody stands behind", decision.SelectedOfferSnapshotID)
	}
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID != "off_disowned" {
			continue
		}
		if candidate.Feasible {
			t.Errorf("the disowned publisher is feasible, and its own publisher stated no confidence in the measurement that admitted it")
		}
		if len(candidate.Rejections) == 0 || candidate.Rejections[0].Code != "UNKNOWN_FACT" || candidate.Rejections[0].Offered != "unknown" {
			t.Errorf("the disowned publisher was refused as %+v, and its machine measured nothing rather than measuring too slow", candidate.Rejections)
		}
	}
}

// TestAnUnpricedCandidateIsTakenOnlyWhenNothingPricedWillDo is what allowing
// unknown pricing buys a Run. It admits a machine nobody has quoted, which is
// what an enrolled node with no configured shadow price publishes, and it never
// makes one preferable: the score is in dollars and that candidate has none, so
// reading the absence as zero made the machine Mercator pays for the cheapest in
// the fleet. It is the last resort it was asked to be, and it is taken when the
// alternative is not running.
func TestAnUnpricedCandidateIsTakenOnlyWhenNothingPricedWillDo(t *testing.T) {
	now := time.Now().UTC()
	workload := schedulerRevision()
	workload.Spec.Placement.AllowUnknownPricing = true
	priced := schedulerOffer("off_priced", now, 0.0002, 0)
	unpriced := schedulerOffer("off_unpriced", now, 0, 0)
	unpriced.Pricing = domain.PriceModel{Currency: "USD"}
	unpriced.Capabilities.Pricing = domain.PricingCapabilities{}

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_pricing",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{priced, unpriced},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if decision.SelectedOfferSnapshotID != "off_priced" {
		t.Errorf("the placement chose %q over a machine somebody quoted", decision.SelectedOfferSnapshotID)
	}
	// The winner has the higher score, because the score is in dollars and the
	// loser has none of them. Nothing else in the record says which rule ranked
	// them, so a reader comparing scores sees the selected machine beaten by the
	// one it beat.
	if !slices.Contains(decision.SelectionReasonCodes, "PRICED_BEFORE_UNPRICED") {
		t.Errorf("the decision recorded %v, and it took the costlier machine because the cheaper one had no price", decision.SelectionReasonCodes)
	}
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID != "off_unpriced" {
			continue
		}
		if !candidate.Feasible {
			t.Errorf("the unquoted machine is infeasible, and this Run said it would rather run there than not run: %+v", candidate.Rejections)
		}
		if candidate.Priced() || candidate.Estimates.CostUSD.Source != domain.CostUnpriced {
			t.Errorf("the unquoted machine records cost %+v, and a reader has to be able to tell an absent price from a free machine", candidate.Estimates.CostUSD)
		}
	}

	unreachable := schedulerOffer("off_priced", now, 0.0002, 0)
	unreachable.Resources.MemoryBytes = 1 << 20
	fallback, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_pricing_fallback",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{unreachable, unpriced},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate the fallback: %v", err)
	}
	if fallback.SelectedOfferSnapshotID != "off_unpriced" {
		t.Errorf("with nothing priced left to take, the placement chose %q, and a last resort that is never taken is a refusal", fallback.SelectedOfferSnapshotID)
	}
	if !slices.Contains(fallback.SelectionReasonCodes, "UNPRICED_LAST_RESORT") {
		t.Errorf("the decision recorded %v for a Run placed on a machine nobody has quoted", fallback.SelectionReasonCodes)
	}
}

// TestABudgetIsNotClearedByACandidateWithNoPrice is the same absence read as a
// bound rather than as a ranking. A Run that states a maximum expected cost has
// said what it will spend, and a candidate whose price nobody quoted cannot be
// shown to spend less: it reported zero dollars, so it passed every budget any
// Run could state.
func TestABudgetIsNotClearedByACandidateWithNoPrice(t *testing.T) {
	now := time.Now().UTC()
	budget := 1.0
	workload := schedulerRevision()
	workload.Spec.Placement.AllowUnknownPricing = true
	workload.Spec.Placement.MaxExpectedCostUSD = &budget
	unpriced := schedulerOffer("off_unpriced", now, 0, 0)
	unpriced.Pricing = domain.PriceModel{Currency: "USD"}
	unpriced.Capabilities.Pricing = domain.PricingCapabilities{}

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run_budget",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{unpriced},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	candidate := decision.Candidates[0]
	if candidate.Feasible {
		t.Fatalf("a Run that budgeted %.2f USD was offered a machine nobody priced and took it", budget)
	}
	if candidate.Rejections[0].Code != "COST_LIMIT_EXCEEDED" {
		t.Errorf("the refusal is %+v, and the caller has to see which bound the candidate missed", candidate.Rejections)
	}
}

// TestAFactThatLapsedBeforeTheDecisionIsSilenceToBothItsReaders holds the rate
// and the floor to one moment. A published fact is read twice on the way to one
// placement: once to price the transfer and once to answer the Run's floor over
// the same link. Asking the first at the offer's observation moment and the second
// at the decision's made a lapsed fact both things at once, and the record then
// said this candidate was refused because nobody had published a download p10 and
// priced its image pull at 750 Mbps measured by that same publisher.
//
// It is not a record an operator can act on, and it is the record the Lab's
// attribution rule reports as a fabricated measurement, in the words it exists to
// say about a prediction that invented a number.
func TestAFactThatLapsedBeforeTheDecisionIsSilenceToBothItsReaders(t *testing.T) {
	collected := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	decided := collected.Add(15 * time.Second)
	offer := schedulerOffer("offer-lapsed", decided, 0.0001, 0)
	offer.ObservedAt = collected
	offer.Network.Download[0].Source = "node_artifact_copy"
	offer.Network.Download[0].ValidUntil = decided.Add(-time.Second)

	decision, err := New().Evaluate(context.Background(), SchedulingInput{
		RunID:        "run-lapsed",
		Workload:     schedulerRevision(),
		Offers:       []domain.OfferSnapshot{offer},
		ModelVersion: "latency-v1",
		EvaluatedAt:  decided,
		Image: domain.ImageManifest{
			Known:  true,
			Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Layers: []domain.ImageLayer{{Digest: "sha256:aa", CompressedBytes: 2 << 30}},
		},
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	assertCandidateRejected(t, decision, "offer-lapsed", "UNKNOWN_FACT", "network.download")
	rate := findTransferRate(t, findCandidate(t, decision, "offer-lapsed"), domain.StageImageFetch)
	if rate.Measurement != "" {
		t.Fatalf("the decision priced the image pull at %+v as a measurement, and it refused the same candidate because nobody had published one", rate)
	}
	if rate.Assumption != domain.AssumptionRegistryRate {
		t.Fatalf("the decision priced the image pull at %+v, and a link nothing standing describes is priced from the stated assumption", rate)
	}
}

func findTransferRate(t *testing.T, candidate domain.CandidateDecision, stage domain.LaunchStage) domain.TransferRate {
	t.Helper()
	for _, rate := range candidate.TransferRates {
		if rate.Stage == stage {
			return rate
		}
	}
	t.Fatalf("candidate %s recorded no rate for its %s stage: %+v", candidate.OfferSnapshotID, stage, candidate.TransferRates)
	return domain.TransferRate{}
}
