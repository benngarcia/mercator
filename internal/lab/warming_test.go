package lab

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
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
	if fetch := borrowed.Estimates.ArtifactSeconds.Expected; fetch < 100 {
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
	if provisionable.Estimates.ProvisionSeconds.P90 != 18*time.Minute.Seconds() {
		t.Fatalf("the decision recorded a provisioning p90 of %.2fs, and this offer published 1080",
			provisionable.Estimates.ProvisionSeconds.P90)
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
			return candidate.Estimates.PullSeconds.Source
		}
	}
	t.Fatalf("Run %q has no candidate for offer %q", decision.RunID, offerID)
	return ""
}

func candidatePullSeconds(t *testing.T, decision domain.BookingDecision, offerID string) float64 {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate.Estimates.PullSeconds.Expected
		}
	}
	t.Fatalf("Run %q has no candidate for offer %q", decision.RunID, offerID)
	return 0
}
