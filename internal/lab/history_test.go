package lab

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/prediction"
)

// TestASecondRunIsPredictedFromTheFirstRunsMeasuredLaunch is the estimator at L1,
// driven through the real orchestrator, event log, Run projection, and Effect
// Ledger.
//
// The placement corpus can state that a stage was answered at a level with a
// count behind it. Only this can hold where the two halves of that answer came
// from: the identity is the one Mercator's own Booking Decision recorded, and the
// seconds are the difference between two moments its own Run stream adopted, one
// stated by the machine holding the container and one stated by the application
// inside it. Nothing here hands the control plane an answer to read back.
func TestASecondRunIsPredictedFromTheFirstRunsMeasuredLaunch(t *testing.T) {
	execution := openConformanceExecution(t, "history-answers-through-the-control-plane")
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	for range 12 {
		if _, err := execution.Drive(context.Background(), Advance(5*time.Minute)); err != nil {
			t.Fatalf("drive the arrivals: %v", err)
		}
	}

	decisions := bookingDecisions(t, execution)
	first, taken := decisions["run-measures"]
	if !taken {
		t.Fatalf("the first Run took no placement: %v", decisions)
	}
	// Nothing had been measured when the first Run arrived, so its readiness is
	// the two minutes it declared about itself, named as the prior it is.
	measuring := candidateByOffer(t, first, "rental-measured").Estimates.Stages.ApplicationReady
	if measuring.Level != domain.LevelPrior || measuring.Expected != 120 {
		t.Fatalf("the first Run was answered %+v, and nothing in this world had been measured yet", measuring)
	}

	second, taken := decisions["run-learns"]
	if !taken {
		t.Fatalf("the second Run took no placement: %v", decisions)
	}
	// The machine that ran the first Run, answered out of that Run. This world
	// spends 45 seconds bringing the application up and the workload declared two
	// minutes, so the two answers cannot be confused for each other.
	learned := candidateByOffer(t, second, "rental-measured").Estimates.Stages.ApplicationReady
	if learned.Level != domain.LevelExactCandidate || learned.SampleCount != 1 || learned.Expected != 45 {
		t.Fatalf("the measured machine was answered %+v, and the launch it performed took 45s", learned)
	}
	if learned.Source != prediction.Source {
		t.Fatalf("the answer names %q as its evidence", learned.Source)
	}
	if !strings.Contains(learned.Key, "machine=") || strings.Contains(learned.Key, "rental-measured") {
		t.Fatalf("the answer was read under %q, and rental-measured is this fixture's name for a lease", learned.Key)
	}
	// The machine nobody has used falls past the level it has no key for. These
	// Rentals publish no region, so there is nothing between this exact candidate
	// and everything the provider has done.
	spare := candidateByOffer(t, second, "rental-spare").Estimates.Stages.ApplicationReady
	if spare.Level != domain.LevelProvider || spare.SampleCount != 1 || spare.Expected != 45 {
		t.Fatalf("the machine nobody has measured was answered %+v", spare)
	}
	if result := invariantResultByID(t, latestInvariantResults(execution.invariants), "safety.prediction_states_its_provenance"); result.Status != InvariantPassed {
		t.Fatalf("the provenance rule failed on a world that measured one of its two machines: %s", result.Violation)
	}
}

func candidateByOffer(t *testing.T, decision domain.BookingDecision, offerID string) domain.CandidateDecision {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate
		}
	}
	t.Fatalf("Run %q weighed no candidate %q", decision.RunID, offerID)
	return domain.CandidateDecision{}
}
