package lab

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scenario"
)

// TestExecutionWarmsARentalUnderTheRealControlPlane is the warming claim at L1.
// The placement harness can prove the scheduler prices a warm host correctly;
// only this proves the host got warm by running a workload, through the offer
// catalog, with the real orchestrator, event log, and Run projection in the
// loop.
func TestExecutionWarmsARentalUnderTheRealControlPlane(t *testing.T) {
	execution := openConformanceExecution(t, "execution-warms-a-rental")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	// The first Run lands, pulls, completes, and releases its Rental before the
	// second Run arrives at 30m.
	if _, err := execution.Drive(context.Background(), Advance(25*time.Minute)); err != nil {
		t.Fatalf("drive the first Run to completion: %v", err)
	}
	if _, err := execution.Drive(context.Background(), Advance(25*time.Minute)); err != nil {
		t.Fatalf("drive the second Run: %v", err)
	}

	decisions := bookingDecisions(t, execution)
	first := decisions["run-first"]
	second := decisions["run-second"]
	if first.SelectedOfferSnapshotID != "held-4090" {
		t.Fatalf("the first Run landed on %q, want the cheaper Rental", first.SelectedOfferSnapshotID)
	}
	if pull := candidatePullSeconds(t, first, "held-4090"); pull < 200 {
		t.Fatalf("the first Run was priced %.2fs of pull on a cold host, want the whole image", pull)
	}
	if pull := candidatePullSeconds(t, second, "held-4090"); pull != 0 {
		t.Fatalf("the Rental that ran the image is still priced %.2fs of pull", pull)
	}
	if pull := candidatePullSeconds(t, second, "spare-4090"); pull < 200 {
		t.Fatalf("the Rental that ran nothing is priced %.2fs of pull, want the whole image", pull)
	}
}

// TestABorrowedSlotIsPricedTheWholePullEveryTime is the lane's claim at L1. The
// machine exists and keeps running, so the offer is standing capacity; nothing
// Mercator has enrolled on it survives the container, so every Run there pays
// for the image again while the Rental beside it stays warm.
func TestABorrowedSlotIsPricedTheWholePullEveryTime(t *testing.T) {
	execution := openConformanceExecution(t, "borrowed-slot-holds-nothing")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	// Runs arrive on the half hour and each occupies its host for a pull plus a
	// runtime. The Lab reconciles only when it is driven, and liveness.stale_lease_expiry
	// allows an execution five minutes past its deadline, so this drives at the
	// cadence a control plane would poll at rather than in one jump.
	for range 16 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrivals: %v", err)
		}
	}

	decisions := bookingDecisions(t, execution)
	for _, name := range []string{"run-borrowed-first", "run-borrowed-second"} {
		decision := decisions[name]
		if decision.SelectedOfferSnapshotID != "local-docker" {
			t.Fatalf("%s landed on %q, want the cheap borrowed slot", name, decision.SelectedOfferSnapshotID)
		}
		if pull := candidatePullSeconds(t, decision, "local-docker"); pull < 200 {
			t.Fatalf("%s was priced %.2fs of pull on capacity Mercator keeps nothing on", name, pull)
		}
	}
	if pull := candidatePullSeconds(t, decisions["run-borrowed-second"], "held-4090"); pull != 0 {
		t.Fatalf("the Rental that ran the image is priced %.2fs of pull", pull)
	}
}

// TestWhatABorrowedMachineHoldsIsNotSomethingMercatorKnows is the other half of
// the lane's locality claim, and the one the retention half cannot make. This
// machine holds every byte of the image before the Run arrives. Nothing of
// Mercator's runs on it, so nothing enumerates it: the offer carries no
// inventory and the Run is priced the whole fetch, which is what every provider
// adapter in the tree produces for the machines it sells. The world still knows
// what the machine holds, and says so where World Truth is stated, because a
// world that erased it at the source would leave the laws about what capacity
// accumulates reading an inventory that is empty whatever happened.
func TestWhatABorrowedMachineHoldsIsNotSomethingMercatorKnows(t *testing.T) {
	execution := openConformanceExecution(t, "borrowed-warmth-is-invisible")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 6 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}

	truth := offerByID(t, execution.runtime.world.truthSnapshot().Offers, "local-docker")
	if !truth.Images.Holds(domain.ReferenceDigest(borrowedWarmthImage)) {
		t.Fatalf("World Truth says the borrowed machine holds %+v, and the Blueprint seeded it the whole image", truth.Images)
	}
	decision := bookingDecisions(t, execution)["run-borrowed"]
	if decision.SelectedOfferSnapshotID != "local-docker" {
		t.Fatalf("the Run landed on %q, want the cheap borrowed slot", decision.SelectedOfferSnapshotID)
	}
	if source := candidatePullSource(t, decision, "local-docker"); source != "inventory_unknown" {
		t.Fatalf("the decision recorded pull source %q for a machine nothing of Mercator's runs on, want its silence named", source)
	}
	if pull := candidatePullSeconds(t, decision, "local-docker"); pull < 200 {
		t.Fatalf("the Run was priced %.2fs of pull from content no offer could carry", pull)
	}
	// The Artifact half of the same silence. This machine is sitting on a
	// checked copy of the dataset in World Truth and no offer can say so, so the
	// copy buys the Run nothing, and the decision records whose silence it was
	// rather than a machine that answered and holds none of it.
	truthCopy, held := truth.Artifacts.Replica(borrowedWarmthArtifact)
	if !held || !truthCopy.State.Usable() {
		t.Fatalf("World Truth says the borrowed machine holds %+v of the dataset, and the Blueprint seeded it a checked copy", truth.Artifacts)
	}
	borrowed := candidateFor(t, decision, "local-docker")
	if found := borrowed.ArtifactEvidence; len(found) != 1 || found[0].Locality != domain.LocalityUnknown {
		t.Fatalf("the decision recorded %+v for a copy nothing of Mercator's can be asked about", found)
	}
	if fetch := borrowed.Estimates.Stages.ArtifactFetch.Expected; fetch < 100 {
		t.Fatalf("the Run was priced %.2fs of Artifact read, and 7GB crosses a 500 Mbps link in 112s", fetch)
	}
}

const (
	borrowedWarmthImage    = "trainer@sha256:5d7e0dc3bcc75e4b3639ed8b3badf9b610b97221c7f8013edc0beebcf34fbc58"
	borrowedWarmthArtifact = "artifact:reference-set:v1"
)

// TestAStartBoundStrikesOutStatedLatenessAndNotSilence is the start-bound rule
// at L1, and it is the execution safety.locality_is_never_infeasibility is a law
// about: no Blueprint in the corpus combined a start bound with an Artifact the
// Run reads, so every clause of that rule was checked against decisions no
// world had produced.
//
// Both halves are one rule read from either end. Ten minutes of provisioning is
// what the offer says about itself, so the Run's three-minute bound removes that
// candidate whatever its disk holds. The borrowed host's twenty minutes is what
// its content would cost from nowhere, so the same bound leaves it alone.
func TestAStartBoundStrikesOutStatedLatenessAndNotSilence(t *testing.T) {
	execution := openConformanceExecution(t, "a-late-start-must-be-a-fact")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 8 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}

	decision := bookingDecisions(t, execution)["run-impatient"]
	if decision.SelectedOfferSnapshotID != "warm-rental" {
		t.Fatalf("the Run landed on %q, want the only machine whose promptness anybody established", decision.SelectedOfferSnapshotID)
	}
	provisionable := candidateFor(t, decision, "slow-provision")
	if provisionable.Feasible || !refusedForLatency(provisionable) {
		t.Fatalf("ten minutes of stated provisioning did not bust a three-minute bound: %+v", provisionable.Rejections)
	}
	// The tail the provider published, rather than one Mercator scaled off its
	// own expectation. This offer says ten minutes on average and eighteen in its
	// p90, and a p90 bound is a question about the eighteen.
	if provisionable.Estimates.Stages.Boot.P90 != 18*time.Minute.Seconds() {
		t.Fatalf("the decision recorded a provisioning p90 of %.2fs, and this offer published 1080",
			provisionable.Estimates.Stages.Boot.P90)
	}
	if provisionable.Estimates.EstablishedStartSeconds.P90 < 18*time.Minute.Seconds() {
		t.Fatalf("the bound was enforced against %.2fs while the provider published an eighteen-minute p90",
			provisionable.Estimates.EstablishedStartSeconds.P90)
	}
	silent := candidateFor(t, decision, "silent-host")
	if !silent.Feasible {
		t.Fatalf("a machine nothing could enumerate was refused: %+v", silent.Rejections)
	}
	if silent.Estimates.StartSeconds.P90 <= 180 {
		t.Fatalf("the silent host is predicted ready in %.2fs, and the case needs a prediction past the bound", silent.Estimates.StartSeconds.P90)
	}
	if silent.Estimates.EstablishedStartSeconds.P90 > 180 {
		t.Fatalf("the silent host established %.2fs of lateness, and nothing there answered any question", silent.Estimates.EstablishedStartSeconds.P90)
	}
	if !slices.Contains(decision.SelectionReasonCodes, "WITHIN_START_SLO") {
		t.Fatalf("the decision recorded %v for a candidate predicted inside its own bound", decision.SelectionReasonCodes)
	}
}

// TestAQueueDrainsAsItRunsAndTheBoundFollowsIt is the queue half of the same
// bound at L1. The only Rental in this world is twenty-nine minutes into a
// Booking whose Run declared half an hour, so what an arriving Run waits for is
// the minute that is left. A schedule that summed declared runtimes answered
// half an hour for the whole half hour, which struck this Run out against its
// own three-minute bound and left the decision saying there was no capacity at
// all.
func TestAQueueDrainsAsItRunsAndTheBoundFollowsIt(t *testing.T) {
	execution := openConformanceExecution(t, "a-queue-drains-as-it-runs")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 30 {
		if _, err := execution.Drive(context.Background(), Advance(time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}

	decision := bookingDecisions(t, execution)["run-impatient"]
	candidate := candidateFor(t, decision, "rental-solo")
	if candidate.Estimates.QueueSeconds.Expected > 90 {
		t.Fatalf("the decision projected %.2fs of waiting for a Booking a minute from its own expected finish",
			candidate.Estimates.QueueSeconds.Expected)
	}
	if !candidate.Feasible {
		t.Fatalf("the Run was refused the only machine in the fleet, a minute from free: %+v", candidate.Rejections)
	}
	if candidate.Disposition != domain.CandidateDispositionQueue {
		t.Fatalf("the candidate was recorded as %q, and there is a Booking to wait behind", candidate.Disposition)
	}
	if decision.SelectedOfferSnapshotID != "rental-solo" {
		t.Fatalf("the Run landed on %q", decision.SelectedOfferSnapshotID)
	}
	if decision.Booking == nil || decision.Booking.State != domain.BookingStateQueued {
		t.Fatalf("the Run took %+v, want a queued Booking behind the Run that is still going", decision.Booking)
	}
	assertQueueEvidenceIsWhatIsLeft(t, decision, candidate)
}

// assertQueueEvidenceIsWhatIsLeft reads the schedule the decision recorded
// beside the seconds it charged. This is the one execution in the tree where a
// Booking is deep into a runtime its Run declared, so it is the only place the
// difference between what a Booking has left and what its caller asked for
// leaves a visible trace in the record.
func assertQueueEvidenceIsWhatIsLeft(t *testing.T, decision domain.BookingDecision, candidate domain.CandidateDecision) {
	t.Helper()
	evidence := candidate.RentalSchedule
	if evidence == nil {
		t.Fatalf("the decision queued this Run and recorded no schedule to retrace the wait from")
	}
	if evidence.Running == nil || evidence.Running.RunID != "run-long" {
		t.Fatalf("the evidence names %+v as what this Run waits behind, want the Run that is still going", evidence.Running)
	}
	if evidence.Running.RemainingExpectedRuntimeSeconds > 90 {
		t.Fatalf("the record says the Booking ahead has %.2fs left, and it is a minute from its own expected finish",
			evidence.Running.RemainingExpectedRuntimeSeconds)
	}
	if len(evidence.Preceding) != 0 {
		t.Fatalf("the record says %d Bookings are already waiting, and this Run is the first", len(evidence.Preceding))
	}
	if decision.Booking.ScheduleVersion != evidence.Version+1 {
		t.Fatalf("the Booking follows schedule version %d and the evidence was read from %d",
			decision.Booking.ScheduleVersion, evidence.Version)
	}
}

func refusedForLatency(candidate domain.CandidateDecision) bool {
	return slices.ContainsFunc(candidate.Rejections, func(rejection domain.Violation) bool {
		return rejection.Code == "LATENCY_SLO_EXCEEDED"
	})
}

func openConformanceExecution(t *testing.T, name string) *Execution {
	t.Helper()
	return openBlueprintExecution(t, "../scenario/scenarios/conformance/"+name+".json", testLimits())
}

func openBlueprintExecution(t *testing.T, path string, limits Limits) *Execution {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint(path)
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	execution, err := Open(context.Background(), Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           limits,
		Policy:           "policy:test",
		MercatorRevision: "revision:test",
	})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	return execution
}

func bookingDecisions(t *testing.T, execution *Execution) map[string]domain.BookingDecision {
	t.Helper()
	stored, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	decisions := map[string]domain.BookingDecision{}
	for _, event := range stored {
		cloud := event.CloudEvent()
		if cloud.Type != "compute.run.booking_decided.v1" {
			continue
		}
		var payload struct {
			Decision domain.BookingDecision `json:"decision"`
		}
		if err := json.Unmarshal(cloud.Data, &payload); err != nil {
			t.Fatalf("decode Booking Decision: %v", err)
		}
		decisions[payload.Decision.RunID] = payload.Decision
	}
	if len(decisions) == 0 {
		t.Fatalf("no Booking Decision was recorded: %d events", len(stored))
	}
	return decisions
}

func candidatePullSource(t *testing.T, decision domain.BookingDecision, offerID string) string {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate.Estimates.Stages.ImageFetch.Source
		}
	}
	t.Fatalf("Run %q has no candidate for offer %q", decision.RunID, offerID)
	return ""
}

func candidatePullSeconds(t *testing.T, decision domain.BookingDecision, offerID string) float64 {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate.Estimates.Stages.ImageFetch.Expected
		}
	}
	t.Fatalf("Run %q has no candidate for offer %q", decision.RunID, offerID)
	return 0
}

// TestAClassMercatorDoesNotKnowIsRefusedAtL1 is the loud refusal through the real
// control plane's own create path. The world would place this Run: one idle Rental
// holding the whole image. What it says about itself is that it is urgent, and
// there is no urgent class, so it has stated no exchange rate at all. Mercator
// refuses it where it enters rather than accepting it and ranking every candidate
// on price alone, which is how a caller would otherwise learn their word was
// ignored: from the bill.
func TestAClassMercatorDoesNotKnowIsRefusedAtL1(t *testing.T) {
	execution := openConformanceExecution(t, "a-class-mercator-does-not-know-is-refused")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	_, err := execution.Drive(context.Background(), Advance(time.Minute))

	if err == nil {
		t.Fatal("a Run stating a class Mercator cannot price was accepted")
	}
	if !strings.Contains(err.Error(), "SERVICE_CLASS_UNKNOWN") {
		t.Fatalf("the refusal was %v, and it has to name the field the caller got wrong", err)
	}
	stored, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("a Run refused at the door left %d events behind, and a refusal is not a Run", len(stored))
	}
}

// TestADisownedLinkFactBuysWhatSilenceBuysAtL1 is the two silences under the real
// control plane, and the execution the Lab's own world statement about path
// confidence is falsifiable through.
//
// A Run states a floor on how fast a candidate reaches the registry and says it
// would rather run on a machine nobody has measured than not run. Four Rentals at
// one price: one measured 750 Mbps and stands behind it, one measured 100 and
// stands behind it, one published 5 Gbps and disowned it, one published nothing.
// The measurement decides the placement, the machine measured too slow is refused
// with the speed it published, and the disowned publisher sits in every column
// beside the machine that said nothing.
func TestADisownedLinkFactBuysWhatSilenceBuysAtL1(t *testing.T) {
	execution := openConformanceExecution(t, "a-link-nobody-measured-is-not-a-slow-link")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 8 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}

	decision := bookingDecisions(t, execution)["run-picky"]
	if decision.SelectedOfferSnapshotID != "rental-measured" {
		t.Fatalf("the Run landed on %q, and the only machine that answered its floor measured 750 Mbps", decision.SelectedOfferSnapshotID)
	}
	slow := candidateFor(t, decision, "rental-slow")
	if slow.Feasible {
		t.Fatalf("a machine that published 100 Mbps cleared a 500 Mbps floor")
	}
	rejection := slow.Rejections[0]
	if rejection.Code != "NETWORK_FACT_UNSATISFIED" || rejection.Offered != 100.0 {
		t.Fatalf("the record says %+v, and this machine measured too slow rather than measuring nothing", rejection)
	}
	disowned := candidateFor(t, decision, "rental-disowned")
	silent := candidateFor(t, decision, "rental-silent")
	for _, candidate := range []domain.CandidateDecision{disowned, silent} {
		if !candidate.Feasible {
			t.Fatalf("%s was refused as %+v, and this Run allows a link nobody measured", candidate.OfferSnapshotID, candidate.Rejections)
		}
	}
	if disowned.Estimates.Stages.ImageFetch != silent.Estimates.Stages.ImageFetch {
		t.Fatalf("the disowned publisher was priced %+v and the silent machine %+v, and a number nobody stands behind is the silence it is",
			disowned.Estimates.Stages.ImageFetch, silent.Estimates.Stages.ImageFetch)
	}
	if disowned.Estimates.Stages.ImageFetch.Expected <= candidateFor(t, decision, "rental-measured").Estimates.Stages.ImageFetch.Expected {
		t.Fatalf("the machine that disowned 5 Gbps was priced %.2fs of pull, no worse than the machine that measured 750 Mbps",
			disowned.Estimates.Stages.ImageFetch.Expected)
	}
}

// TestAnUnquotedMachineIsTheLastResortAtL1 is the unpriced Rental under the real
// control plane, and the execution the Lab's own world statement about a machine
// nobody quoted is falsifiable through. Both machines hold the whole image and are
// a second from ready; the one nobody quoted has no dollars, so it is ranked
// behind the one somebody did and the decision says so.
func TestAnUnquotedMachineIsTheLastResortAtL1(t *testing.T) {
	execution := openConformanceExecution(t, "an-unquoted-machine-is-the-last-resort")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 8 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}

	decision := bookingDecisions(t, execution)["run-thrifty"]
	if decision.SelectedOfferSnapshotID != "rental-quoted" {
		t.Fatalf("the Run landed on %q, and the alternative is a machine nobody has quoted", decision.SelectedOfferSnapshotID)
	}
	unquoted := candidateFor(t, decision, "rental-unquoted")
	if !unquoted.Feasible {
		t.Fatalf("the unquoted machine was refused as %+v, and this Run said it would rather run there than not run", unquoted.Rejections)
	}
	if unquoted.Priced() || unquoted.Estimates.CostUSD.Source != domain.CostUnpriced {
		t.Fatalf("the unquoted machine records cost %+v, and this world says nobody has priced it", unquoted.Estimates.CostUSD)
	}
	quoted := candidateFor(t, decision, "rental-quoted")
	if quoted.ScoreUSD <= unquoted.ScoreUSD {
		t.Fatalf("the selected machine scored %.6f against the unquoted machine's %.6f, and the case needs the winner to be the one with the higher score",
			quoted.ScoreUSD, unquoted.ScoreUSD)
	}
	if !slices.Contains(decision.SelectionReasonCodes, "PRICED_BEFORE_UNPRICED") {
		t.Fatalf("the decision recorded %v, and it took the costlier machine because the cheaper one had no price", decision.SelectionReasonCodes)
	}
}

// TestAPublishedRiskHistoryReachesTheRecordAtL1 is the risk history under the
// real control plane, and the execution the Lab world's own publication of it is
// falsifiable through.
//
// It asserts the record and deliberately not the placement. Three listings whose
// only difference is what their providers published about them score to the same
// dollar, so the winner is whichever offer ID sorts first, and that is the honest
// state of this model: what a refusal costs is a probability times a predicted
// start, nothing here predicts either, and a flat penalty invented for it would
// be the unmeasured constant this program keeps deleting. The gap is written down
// where a fixture will fail when somebody closes it.
//
// The third listing, the one nobody measured, is what holds the doubt to the same
// rule. While the published confidence was charged through the uncertainty term
// and the rate beside it was priced nowhere, the only thing a measured history did
// to a score was penalise the provider that published it, and this Run went to the
// machine that had said nothing.
//
// A published history hangs off a marketplace listing here because that is where
// production reads one: Vast states its uptime score on an unrented ask, and every
// standing offer in this tree publishes no history at all.
func TestAPublishedRiskHistoryReachesTheRecordAtL1(t *testing.T) {
	execution := openConformanceExecution(t, "a-published-risk-history-reaches-the-record")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 8 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}

	decision := bookingDecisions(t, execution)["run-unbothered"]
	flaky := candidateFor(t, decision, "ask-flaky")
	steady := candidateFor(t, decision, "ask-steady")
	unmeasured := candidateFor(t, decision, "ask-unmeasured")
	for _, candidate := range []domain.CandidateDecision{flaky, steady, unmeasured} {
		if !candidate.Feasible {
			t.Fatalf("%s was refused as %+v, and an unreliable machine is not an unusable one", candidate.OfferSnapshotID, candidate.Rejections)
		}
	}
	if flaky.Reliability != (domain.ReliabilityEvidence{
		StartFailures: domain.StatedRate{Rate: 0.4, Confidence: 0.9},
		Interruptions: domain.StatedRate{Rate: 0.25, Confidence: 0.9},
	}) {
		t.Fatalf("the decision recorded %+v for the listing whose provider says it refuses two starts in five", flaky.Reliability)
	}
	if steady.Reliability != (domain.ReliabilityEvidence{
		StartFailures: domain.StatedRate{Rate: 0, Confidence: 0.9},
		Interruptions: domain.StatedRate{Rate: 0, Confidence: 0.9},
	}) {
		t.Fatalf("the decision recorded %+v for the machine measured and never seen to fail, and a clean record is not silence", steady.Reliability)
	}
	if unmeasured.Reliability != (domain.ReliabilityEvidence{}) {
		t.Fatalf("the decision recorded %+v for the machine nobody has measured, and silence is not a clean record", unmeasured.Reliability)
	}
	if flaky.Uncertainty() != steady.Uncertainty() || flaky.Uncertainty() != unmeasured.Uncertainty() {
		t.Fatalf("the three machines carry %v, %v and %v points of doubt over %+v, and the only answer they differ about is one nothing prices",
			flaky.Uncertainty(), steady.Uncertainty(), unmeasured.Uncertainty(), flaky.Confidences)
	}
	if flaky.ScoreUSD != steady.ScoreUSD || flaky.ScoreUSD != unmeasured.ScoreUSD {
		t.Fatalf("the three machines scored %.6f, %.6f and %.6f, and this model neither prices a refusal nor charges a provider for having measured one",
			flaky.ScoreUSD, steady.ScoreUSD, unmeasured.ScoreUSD)
	}
	if decision.SelectedOfferSnapshotID != "ask-flaky" {
		t.Fatalf("the Run landed on %q, and with the three machines priced identically the record has nothing left to rank them by but the offer ID",
			decision.SelectedOfferSnapshotID)
	}
}

// TestAPublishedRateIsNotWhatAMachineDoes is the published risk history beside a
// world that contradicts it, which is the half the corpus could not state.
//
// Two reviewers made the same objection to the record above: a rate is copied
// from a fixture onto an offer onto a decision, and no simulated world can
// produce the event it is a rate of, so a Blueprint may say a machine refuses
// every start and be graded green while that machine starts everything. The term
// that will price a refusal is a probability times the start of the redo a
// refusal causes, and a corpus that cannot produce the redo cannot falsify it.
//
// This is that world. The Run takes the machine whose provider measured it and
// never saw it refuse a start, because the two histories are recorded and priced
// nowhere and the placement falls through to the offer ID, and that machine
// refuses the launch. Mercator strikes it out and places the Run on the listing
// whose provider published the worse record, where it runs and succeeds.
//
// What the record says about the two providers does not move. A rate is what a
// provider measured and published, and Mercator has begun measuring nothing about
// machines on its providers' behalf, so a refusal that really happened leaves both
// histories exactly as they were stated. That separation is the thing to keep when
// the redo is priced: the prediction reads the published rate, and what the world
// then does is what the prediction is scored against.
func TestAPublishedRateIsNotWhatAMachineDoes(t *testing.T) {
	execution := openConformanceExecution(t, "a-published-rate-is-not-what-a-machine-does")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 12 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}

	decisions := decisionsFor(t, execution, "run-unlucky")
	if len(decisions) != 2 {
		t.Fatalf("the Run was placed %d times, and a refused start is answered by placing it again", len(decisions))
	}
	refused, redone := decisions[0], decisions[1]
	if refused.SelectedOfferSnapshotID != "ask-a-clean-record" {
		t.Fatalf("the Run was first placed on %q, and the two histories rank nothing so the offer ID decides", refused.SelectedOfferSnapshotID)
	}
	if !slices.Contains(runEventTypes(t, execution, "run-unlucky"), orchestrator.EventLaunchFailed) {
		t.Fatalf("the machine took the launch, and this world is the one where the machine its provider stands behind refuses it")
	}
	struckOut := candidateFor(t, redone, "ask-a-clean-record")
	if struckOut.Feasible || struckOut.Rejections[0].Code != "PREVIOUS_ATTEMPT_CAPACITY_UNAVAILABLE" {
		t.Fatalf("the machine that refused the start was weighed again as %+v", struckOut.Rejections)
	}
	if redone.SelectedOfferSnapshotID != "ask-b-bad-record" {
		t.Fatalf("the redo landed on %q, and the only machine left is the one whose provider published the worse record", redone.SelectedOfferSnapshotID)
	}
	clean := domain.ReliabilityEvidence{
		StartFailures: domain.StatedRate{Rate: 0, Confidence: 0.9},
		Interruptions: domain.StatedRate{Rate: 0, Confidence: 0.9},
	}
	bad := domain.ReliabilityEvidence{
		StartFailures: domain.StatedRate{Rate: 0.4, Confidence: 0.9},
		Interruptions: domain.StatedRate{Rate: 0.25, Confidence: 0.9},
	}
	for _, decision := range decisions {
		if got := candidateFor(t, decision, "ask-a-clean-record").Reliability; got != clean {
			t.Fatalf("the decision records %+v for the machine that refused the start, and a rate is what its provider measured", got)
		}
		if got := candidateFor(t, decision, "ask-b-bad-record").Reliability; got != bad {
			t.Fatalf("the decision records %+v for the machine that took the work, and a rate is what its provider measured", got)
		}
	}
	if outcome := projectedRun(t, execution, "run-unlucky").Outcome; outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("the Run closed %q, and the machine its provider says refuses two starts in five started it", outcome)
	}
}

// decisionsFor is every placement this Run was given, in the order they were
// taken. The map beside it keeps one decision per Run, which is the wrong shape
// for a Run placed twice: the redo is the whole subject here.
func decisionsFor(t *testing.T, execution *Execution, runID string) []domain.BookingDecision {
	t.Helper()
	stored, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	var decisions []domain.BookingDecision
	for _, event := range stored {
		cloud := event.CloudEvent()
		if cloud.Type != orchestrator.EventBookingDecided {
			continue
		}
		var payload struct {
			Decision domain.BookingDecision `json:"decision"`
		}
		if err := json.Unmarshal(cloud.Data, &payload); err != nil {
			t.Fatalf("decode Booking Decision: %v", err)
		}
		if payload.Decision.RunID == runID {
			decisions = append(decisions, payload.Decision)
		}
	}
	return decisions
}

// runEventTypes is everything this Run's own stream says happened to it.
func runEventTypes(t *testing.T, execution *Execution, runID string) []string {
	t.Helper()
	stored, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	var types []string
	for _, event := range stored {
		cloud := event.CloudEvent()
		if cloud.Subject == "runs/"+runID {
			types = append(types, cloud.Type)
		}
	}
	return types
}

// TestTheRunStreamRecordsAStartNobodyInferred is the observed-start claim at L1.
// The placement corpus can prove that a start latency read off the run stream is
// the world's own moment; only this proves it through the real orchestrator, event
// log, and Run projection, and only this can read the Run Bundle the calibration
// this phase is building would be trained on.
//
// The Run takes the machine that does not exist yet. The world spends thirty
// seconds acquiring it, four minutes booting it, thirty seconds enrolling the
// runtime, and then 288.64 seconds moving 18.04GB of image onto it, so the
// container begins 588.64 seconds after the launch was accepted. Mercator's own
// prediction is a different number over the same content, and the Bundle holds
// both: a calibration set whose two columns came from one piece of code would
// teach nothing.
func TestTheRunStreamRecordsAStartNobodyInferred(t *testing.T) {
	execution := openConformanceExecution(t, "a-node-reports-when-the-container-really-started")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 6 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}

	decision := bookingDecisions(t, execution)["run-patient"]
	if decision.SelectedOfferSnapshotID != "fresh-cheap" {
		t.Fatalf("the Run landed on %q, and opportunistic work values a second of waiting at nothing", decision.SelectedOfferSnapshotID)
	}

	record := projectedRun(t, execution, "run-patient")
	accepted := launchAcceptedMoment(t, execution, "run-patient")
	if record.StartedAt == nil {
		t.Fatalf("the Run projection carries no start moment, and its container has been running for minutes")
	}
	if !record.StartedAt.After(accepted) {
		t.Fatalf("the Run started at %s and its launch was accepted at %s, and this machine still had eighteen gigabytes to pull when the launch was taken",
			record.StartedAt.Format(time.RFC3339Nano), accepted.Format(time.RFC3339Nano))
	}
	// The five minutes of provisioning are not in this subtraction, and that is
	// the point of the lease being its own contract: the machine was allocated,
	// booted, and enrolled before anything took this launch, and those three
	// stages have three actuals of their own. What is left between the launch
	// being accepted and the container starting is the content it still owed.
	if latency := record.StartedAt.Sub(accepted).Seconds(); latency < 288 || latency > 289 {
		t.Fatalf("the recorded start is %.2fs after the launch was accepted, want 288.64s of image on a machine that already existed", latency)
	}

	rows := bundlePredictions(t, execution)
	// Two aggregates and one row per launch stage. The stage rows are what make the
	// waterfall calibratable at all: a bundle carrying only the sum could say a
	// start was ninety seconds late and never which stage was late.
	if len(rows) != 2+len(domain.LaunchStages) {
		t.Fatalf("the Bundle holds %d predicted-versus-actual records for one Run: %+v", len(rows), rows)
	}
	for _, stage := range domain.LaunchStages {
		row, present := rows[string(stage)+"_seconds"]
		if !present {
			t.Fatalf("the Bundle carries no row for the %s stage", stage)
		}
		if row.ActualSource != "effect_ledger.launch.stage_seconds" {
			t.Fatalf("the %s actual came from %q, and the world's own ledger is the only thing that spent it", stage, row.ActualSource)
		}
	}
	// The stage the fixture is about: the provider published five minutes and the
	// world spent four of them booting, so the prediction and the actual come from
	// two places and disagree.
	boot := rows[string(domain.StageBoot)+"_seconds"]
	if boot.PredictedSeconds != 300 || boot.ActualSeconds != 240 {
		t.Fatalf("boot was predicted %.2fs against an actual of %.2fs, and the fixture publishes 300 and spends 240",
			boot.PredictedSeconds, boot.ActualSeconds)
	}
	start := rows["start_latency_seconds"]
	if start.ActualSource != "run_stream.execution_started" {
		t.Fatalf("the start actual came from %q, and the only admissible source is a moment somebody observed", start.ActualSource)
	}
	if start.ActualSeconds < 288 || start.ActualSeconds > 289 {
		t.Fatalf("the Bundle records a start actual of %.2fs", start.ActualSeconds)
	}
	predicted := candidateFor(t, decision, "fresh-cheap").Estimates.StartSeconds.Expected
	if start.PredictedSeconds != predicted {
		t.Fatalf("the Bundle predicts %.2fs and the decision it was taken on predicted %.2fs", start.PredictedSeconds, predicted)
	}
	if start.PredictedSeconds == start.ActualSeconds {
		t.Fatalf("the prediction and the actual are both %.2fs, and two numbers from one piece of code calibrate nothing", predicted)
	}
}

func projectedRun(t *testing.T, execution *Execution, runID string) domain.RunRecord {
	t.Helper()
	records, err := execution.runtime.allRuns(context.Background())
	if err != nil {
		t.Fatalf("read Run projection: %v", err)
	}
	for _, record := range records {
		if record.ID == runID {
			return record
		}
	}
	t.Fatalf("no Run %q in the projection", runID)
	return domain.RunRecord{}
}

func launchAcceptedMoment(t *testing.T, execution *Execution, runID string) time.Time {
	t.Helper()
	stored, err := execution.runtime.mercatorEvents(context.Background())
	if err != nil {
		t.Fatalf("read Mercator events: %v", err)
	}
	for _, event := range stored {
		cloud := event.CloudEvent()
		if cloud.Type != orchestrator.EventLaunchAccepted || cloud.CorrelationID != runID {
			continue
		}
		var payload struct {
			AcceptedAt time.Time `json:"accepted_at"`
		}
		if err := json.Unmarshal(cloud.Data, &payload); err != nil {
			t.Fatalf("decode launch receipt: %v", err)
		}
		return payload.AcceptedAt
	}
	t.Fatalf("Run %q has no accepted launch", runID)
	return time.Time{}
}

func bundlePredictions(t *testing.T, execution *Execution) map[string]predictionActualRecord {
	t.Helper()
	bundle, err := execution.Export(context.Background())
	if err != nil {
		t.Fatalf("export Run Bundle: %v", err)
	}
	rows := map[string]predictionActualRecord{}
	for _, line := range strings.Split(strings.TrimSpace(string(bundleEntryData(t, bundle, "predictions.jsonl"))), "\n") {
		var record predictionActualRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode prediction record %q: %v", line, err)
		}
		rows[record.Metric] = record
	}
	return rows
}
