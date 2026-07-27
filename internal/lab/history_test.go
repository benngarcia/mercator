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

	for range 14 {
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
	// The unmeasured machine beside it falls one rung, to what this provider has
	// done in this place. That rung exists only because the offer states a region,
	// and what this holds is the region surviving the steps this harness actually
	// runs: the decision records it inside the identity, and the estimator files
	// the launch under it. The two steps outside this path are held where they
	// happen. That a backend states a place at all is held in the adapters, in the
	// ephemeral lane where they do; the machines production enrols in this lane
	// state none, and internal/node holds that. That aggregation carries it is held
	// in internal/broker, because this harness hands the simulated world to the
	// orchestrator as its offer source and no Broker rewrites an offer here.
	spare := candidateByOffer(t, second, "rental-spare").Estimates.Stages.ApplicationReady
	if spare.Level != domain.LevelProviderAndRegion || spare.SampleCount != 1 || spare.Expected != 45 {
		t.Fatalf("the unmeasured machine in the measured machine's own region was answered %+v", spare)
	}
	// The key names the place and the content both. A rung generalizes over
	// machines, which is what it is for, and it may not generalize over what those
	// machines were asked to run: this rung's whole claim is that a launch of this
	// image on a machine of this product in this place is evidence about the same
	// image on the machine beside it.
	if spare.Key != "lane=reusable;provider=lab;region=US-CA;image="+measuredImage {
		t.Fatalf("the region rung answered under %q", spare.Key)
	}
	// The unmeasured machine somewhere else falls past that rung, because nothing
	// in its region has been measured, to everything the provider has done
	// anywhere. Same samples, coarser level, and less confidence for the breadth.
	elsewhere := candidateByOffer(t, second, "rental-elsewhere").Estimates.Stages.ApplicationReady
	if elsewhere.Level != domain.LevelProvider || elsewhere.SampleCount != 1 || elsewhere.Expected != 45 {
		t.Fatalf("the unmeasured machine in an unmeasured region was answered %+v", elsewhere)
	}
	// All three answers are the same forty-five seconds from the same single
	// launch, because there is only one launch in this world. What separates them
	// is what each rung is worth, and a record that stated the seconds alone would
	// read identically for a machine measured and a machine two rungs away from
	// anything measured.
	if !(elsewhere.Confidence < spare.Confidence && spare.Confidence < learned.Confidence) {
		t.Fatalf(
			"the three rungs are worth %v, %v and %v, and a coarser rung answers about other machines",
			elsewhere.Confidence, spare.Confidence, learned.Confidence,
		)
	}
	// The third Run asks the same three machines about a second image, and every
	// rung is silent about it. Readiness is what the workload's own process spends
	// coming up, so the launch this fleet measured is evidence about that image and
	// about no other: the machine that performed it, its neighbour in the same
	// place, and its provider elsewhere all fall to this Run's own declaration.
	other, taken := decisions["run-asks-about-other-content"]
	if !taken {
		t.Fatalf("the Run of a second image took no placement: %v", decisions)
	}
	for _, offerID := range []string{"rental-measured", "rental-spare", "rental-elsewhere"} {
		unlearned := candidateByOffer(t, other, offerID).Estimates.Stages.ApplicationReady
		if unlearned.Level != domain.LevelPrior || unlearned.SampleCount != 0 || unlearned.Expected != 120 {
			t.Fatalf(
				"%s was answered %+v about an image nothing in this world has launched, and this Run declared two minutes",
				offerID, unlearned,
			)
		}
	}
	if result := invariantResultByID(t, latestInvariantResults(execution.invariants), "safety.prediction_states_its_provenance"); result.Status != InvariantPassed {
		t.Fatalf("the provenance rule failed on a world that measured one of its three machines: %s", result.Violation)
	}
}

// measuredImage is the image the first two Runs of this fixture ask for, which is
// the content the keys of every rung that answers them have to name.
const measuredImage = "sha256:9e2f5d7b3a1c4e6089bd2f7a5c3e1d0b8a6f4c2e0d9b7a5f3c1e9d7b5a3f1c9e"

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
