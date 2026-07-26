package lab

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scenario"
)

// TestEveryStageOfALaunchHasAnActual is the waterfall at L1, driven through the
// real orchestrator, event log, Run projection, and Run Bundle.
//
// The placement corpus can state what each stage was predicted to cost and what
// the whole launch then took. Only this can read the record a calibration would
// be trained on: one row per stage, the prediction off the Booking Decision
// Mercator wrote, the actual off the world's own launch consequence in the Effect
// Ledger. What it holds is that both halves exist for all eight and that no
// stage's two halves came from the same place.
func TestEveryStageOfALaunchHasAnActual(t *testing.T) {
	execution := openConformanceExecution(t, "every-stage-of-a-launch-has-an-actual")
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

	rows := bundlePredictions(t, execution)
	// Ten minutes of provisioning over three stages, 500MB of image, the assembly
	// of it, and a container runtime asked for a process. Every one of them is a
	// number this world spent, and every one has a prediction beside it.
	spent := 0.0
	for _, stage := range domain.LaunchStages {
		row, present := rows[string(stage)+"_seconds"]
		if !present {
			t.Fatalf("the Bundle carries no row for the %s stage", stage)
		}
		if row.PredictionSource != "booking_decision.estimates.stages."+string(stage) {
			t.Fatalf("the %s prediction came from %q, and the only thing that predicted it is the decision Mercator recorded",
				stage, row.PredictionSource)
		}
		if row.ActualSource != "effect_ledger.launch.stage_seconds" {
			t.Fatalf("the %s actual came from %q, and the world's own ledger is the only thing that spent it",
				stage, row.ActualSource)
		}
		spent += row.ActualSeconds
	}
	// Three machine stages, an image fetch, an assembly, a container start, and
	// four minutes of an application coming up. A world in which the stages after
	// the machine cost nothing would total the provisioning alone.
	if spent < 890 || spent > 900 {
		t.Fatalf("the eight stages total %.2f seconds, and this world spends ten minutes of machine, 500MB of image, 30s of assembly, 20s of container, and four minutes of application", spent)
	}

	// The stage the machine's publisher spoke about, and the one nobody did. Both
	// happened, and only one was predicted at anything: that gap is the record
	// making an unpublished stage visible instead of folding it into a total.
	boot := rows[string(domain.StageBoot)+"_seconds"]
	if boot.PredictedSeconds != 600 || boot.ActualSeconds != 360 {
		t.Fatalf("boot was predicted %.2fs against an actual of %.2fs, and this offer publishes 600 and boots for 360",
			boot.PredictedSeconds, boot.ActualSeconds)
	}
	acquisition := rows[string(domain.StageAcquisition)+"_seconds"]
	if acquisition.PredictedSeconds != 0 || acquisition.ActualSeconds != 120 {
		t.Fatalf("acquisition was predicted %.2fs against an actual of %.2fs, and no provider in this tree publishes an acquisition claim",
			acquisition.PredictedSeconds, acquisition.ActualSeconds)
	}

	// The last stage is the workload's own, and it is the one the record could not
	// carry at all before this: the application declared two minutes, took four, and
	// nothing on the machine could have said either number.
	ready := rows[string(domain.StageApplicationReady)+"_seconds"]
	if ready.PredictedSeconds != 120 || ready.ActualSeconds != 240 {
		t.Fatalf("readiness was predicted %.2fs against an actual of %.2fs, and the workload declared 120 while this world takes 240",
			ready.PredictedSeconds, ready.ActualSeconds)
	}
	record := projectedRun(t, execution, "run-server")
	if record.StartedAt == nil || record.ReadyAt == nil {
		t.Fatalf("the Run projection carries start %v and readiness %v", record.StartedAt, record.ReadyAt)
	}
	// A running process is not a serving one, and the projection has to hold both
	// moments to be able to say so.
	if gap := record.ReadyAt.Sub(*record.StartedAt); gap != 4*time.Minute {
		t.Fatalf("the application was ready %s after its process began, and this world takes four minutes", gap)
	}
}

// TestAWorkloadThatNeverBecomesReadyMeasuresNoReadiness is the failure mode the
// readiness stage exists to expose, driven at L1. A container starts, the
// application behind it never reports it can do work, and the record has to say
// three things without contradicting itself: the seven stages this launch reached
// each have an actual, the eighth has none, and the reason it has none is that the
// world says this workload never came up rather than that nothing timed it.
//
// The three readings of a missing readiness are wrong in different directions, which
// is why the ledger states which one this is. A measured zero says the application
// was serving the instant its process existed. An untimed stage says a launch path
// lost its accounting. Neither is this world.
func TestAWorkloadThatNeverBecomesReadyMeasuresNoReadiness(t *testing.T) {
	execution := openConformanceExecution(t, "a-workload-that-never-becomes-ready")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 4 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("a workload that never became ready broke a standing rule: %v", err)
	}

	rows := bundlePredictions(t, execution)
	ready := rows[string(domain.StageApplicationReady)+"_seconds"]
	if ready.ActualSource != "launch_never_reached_stage" || ready.ActualSeconds != 0 {
		t.Fatalf("the readiness row is sourced %q at %.2fs, and this world's application never reported at all",
			ready.ActualSource, ready.ActualSeconds)
	}
	if ready.PredictedSeconds != 120 {
		t.Fatalf("readiness was predicted %.2fs, and this workload declared two minutes of it",
			ready.PredictedSeconds)
	}
	// The seven stages before it are measured, because the launch reached every one
	// of them. A rule that let an unreached stage through would have to keep holding
	// the rest, and this is where that is checked.
	for _, stage := range domain.LaunchStages {
		if stage == domain.StageApplicationReady {
			continue
		}
		row := rows[string(stage)+"_seconds"]
		if row.ActualSource != "effect_ledger.launch.stage_seconds" {
			t.Fatalf("the %s actual came from %q, and this launch reached that stage", stage, row.ActualSource)
		}
	}
	for _, run := range labRunRecords(t, execution) {
		if run.ReadyAt != nil {
			t.Fatalf("Run %q records an application readiness of %s in a world whose applications never report one",
				run.ID, run.ReadyAt.Format(time.RFC3339Nano))
		}
	}
}

// TestAMeasuredPathPricesTheReadAndThenSpendsIt is the transfer model at L1, and
// it is one claim about two halves of one declaration. The Blueprint states each
// machine's path to the object store once. Mercator reads the fact the machine
// published and prices the read; this world reads the same declaration and really
// moves the bytes. A tree where either half fell back to a constant per scope
// would still produce a prediction and an actual, and they would agree by
// construction on every machine in the fleet.
//
// The two machines are identical in every other column, including the image, so
// nothing but the path can decide the placement. The far one publishes the faster
// registry link on purpose: it holds the image already, so there is nothing to
// fetch over that link, and a fast path of the wrong kind buys a candidate
// nothing.
func TestAMeasuredPathPricesTheReadAndThenSpendsIt(t *testing.T) {
	execution := openConformanceExecution(t, "a-path-somebody-measured-prices-the-read")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 4 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrival: %v", err)
		}
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("a launch priced from a measured path broke a standing rule: %v", err)
	}

	decision := bookingDecisions(t, execution)["run-reader"]
	if decision.SelectedOfferSnapshotID != "rental-near-the-data" {
		t.Fatalf("the decision placed on %q, and the machine beside the data reads the dataset twenty times faster",
			decision.SelectedOfferSnapshotID)
	}
	// Eighty seconds against sixteen hundred, off the same forty gigabytes. Under
	// one constant per scope both were 640 and the placement fell to whichever
	// offer ID sorted first.
	near := candidateFor(t, decision, "rental-near-the-data")
	far := candidateFor(t, decision, "rental-far-from-the-data")
	assertPricedRead(t, near, 80, 4000)
	assertPricedRead(t, far, 1600, 200)

	// The world's own account of what it then spent, which is the half a flat
	// constant made unfalsifiable: this number comes from the Effect Ledger and
	// the one above comes from the Booking Decision.
	rows := bundlePredictions(t, execution)
	read := rows[string(domain.StageArtifactFetch)+"_seconds"]
	if read.ActualSource != "effect_ledger.launch.stage_seconds" {
		t.Fatalf("the artifact read's actual came from %q, and the world's own ledger is the only thing that spent it",
			read.ActualSource)
	}
	if read.ActualSeconds < 79 || read.ActualSeconds > 81 {
		t.Fatalf("this world spent %.2fs reading forty gigabytes over a 4 Gbps path, and the path says eighty",
			read.ActualSeconds)
	}
}

// assertPricedRead holds one candidate to the seconds it was charged for its
// Artifact read and to the rate those seconds came from. The rate is asserted
// beside the seconds because the seconds alone cannot say which of the two halves
// of the arithmetic a fixture pinned.
func assertPricedRead(t *testing.T, candidate domain.CandidateDecision, seconds, mbps float64) {
	t.Helper()
	read := candidate.Estimates.Stages.ArtifactFetch
	if read.Expected < seconds-1 || read.Expected > seconds+1 {
		t.Fatalf("candidate %q was charged %.2fs of reading, and its path says %.2f", candidate.OfferSnapshotID, read.Expected, seconds)
	}
	for _, rate := range candidate.TransferRates {
		if rate.Stage != domain.StageArtifactFetch {
			continue
		}
		if rate.Mbps != mbps || rate.Measurement != scenario.PathFactSource {
			t.Fatalf("candidate %q priced its read at %.2f Mbps measured by %q, want %.2f measured by the path it declared",
				candidate.OfferSnapshotID, rate.Mbps, rate.Measurement, mbps)
		}
		return
	}
	t.Fatalf("candidate %q records no rate for the read it was charged %.2fs for", candidate.OfferSnapshotID, read.Expected)
}
