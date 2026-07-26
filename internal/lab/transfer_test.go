package lab

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// TestAWorldCrossesThePathItDeclaredAndNotTheOneMercatorRead is the claim that
// separates prediction from actual in this harness, held by the one case that can
// tell them apart.
//
// A Blueprint states a path once and it is read twice: this world moves bytes at
// the rate the fixture declared, and the machine publishes a fact about the same
// path for Mercator to price from. On every other Blueprint here the two are the
// same figure, so a world that quietly priced its own transfers off the offer
// Mercator reads would agree with itself and the corpus could not say a word about
// it. That is the tautology this fixture exists to remove: the machine states no
// confidence in its own 200 Mbps reading, Mercator may not act on a number its
// publisher disowned and charges its fleet-wide assumption instead, and the world
// still spends what the path really costs. The predicted seconds and the spent
// seconds are different numbers, and only one of them can have come from the
// world's own declaration.
func TestAWorldCrossesThePathItDeclaredAndNotTheOneMercatorRead(t *testing.T) {
	execution := openConformanceExecution(t, "a-path-a-host-disowned-is-still-the-path")
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
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("a launch priced from an assumption over a declared path broke a standing rule: %v", err)
	}

	// What Mercator predicted, off the only thing it was allowed to read: nothing
	// answered for this path, so the read is priced from the standing assumption and
	// the record says so.
	decision := bookingDecisions(t, execution)["run-reader"]
	candidate := candidateFor(t, decision, "rental-that-disowned-its-reading")
	assertReadPricedFromTheAssumption(t, candidate, 640)

	// What the world then spent, off its own declaration. Sixteen hundred seconds
	// is forty gigabytes over 200 Mbps, and there is nowhere else that number can
	// have come from: Mercator never read it, and this world's constant for a path
	// no fixture declared is 500 Mbps, which is the 640 above.
	rows := bundlePredictions(t, execution)
	read := rows[string(domain.StageArtifactFetch)+"_seconds"]
	if read.ActualSource != "effect_ledger.launch.stage_seconds" {
		t.Fatalf("the read's actual came from %q, and the world's own ledger is the only thing that spent it", read.ActualSource)
	}
	if read.ActualSeconds < 1599 || read.ActualSeconds > 1601 {
		t.Fatalf("this world spent %.2fs reading forty gigabytes over the 200 Mbps path it declared, and it costs sixteen hundred",
			read.ActualSeconds)
	}
}

// assertReadPricedFromTheAssumption holds a candidate to seconds it was charged
// for an Artifact read nothing measured, and to the record saying which of the two
// claims it made: Mercator's own constant, and no measurement at all.
func assertReadPricedFromTheAssumption(t *testing.T, candidate domain.CandidateDecision, seconds float64) {
	t.Helper()
	read := candidate.Estimates.Stages.ArtifactFetch
	if read.Expected < seconds-1 || read.Expected > seconds+1 {
		t.Fatalf("candidate %q was charged %.2fs of reading, and the assumption over forty gigabytes is %.2f",
			candidate.OfferSnapshotID, read.Expected, seconds)
	}
	for _, rate := range candidate.TransferRates {
		if rate.Stage != domain.StageArtifactFetch {
			continue
		}
		if rate.Measurement != "" {
			t.Fatalf("the read was priced at %.2f Mbps measured by %q, on a machine that stands behind nothing about this path",
				rate.Mbps, rate.Measurement)
		}
		if rate.Assumption != domain.AssumptionObjectStoreRate || rate.Mbps != domain.DefaultObjectStoreDownloadMbps {
			t.Fatalf("the read was priced at %+v, and the only thing Mercator may read here is its own assumption", rate)
		}
		return
	}
	t.Fatalf("candidate %q records no rate for the read it was charged for", candidate.OfferSnapshotID)
}
