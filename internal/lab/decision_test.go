package lab

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// TestAReplacementNamesTheDecisionItReplaces is the launch-failure half of the
// supersession rule, driven through the real control plane.
//
// It reads the world where a machine refuses to start the work, because that is
// the re-decision the placement corpus cannot state: a refused launch is a fault,
// and a placement fixture has no faults, so the corpus states its half of the same
// rule through a fleet that changed under a queued Run. Here the fleet does not
// change at all. The machine the first decision chose takes the launch and refuses
// it, and the answer that replaces it is about the same two listings minus the one
// that turned the Run away.
//
// It asserts what a reader of the record gets, which is more than the chain being
// well formed. Both answers survive, in order, with distinct identities. The newest
// names the record before it and gives the reason a reader can check against the
// Run's own launch failure. And the answer that no longer stands is still the one
// that says the Run was sent to the machine with the clean history first, which is
// the only place that fact exists: reading the last decision alone showed a Run
// that had always been going to the machine whose provider publishes the worse
// record.
func TestAReplacementNamesTheDecisionItReplaces(t *testing.T) {
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

	chain := decisionsFor(t, execution, "run-unlucky")
	if len(chain) != 2 {
		t.Fatalf("the record holds %d decisions, and a refused start is answered by deciding again", len(chain))
	}
	first, second := chain[0], chain[1]
	if first.Supersedes != "" || first.SupersedesReason != "" {
		t.Fatalf("the first decision replaces %q for %q, and nothing came before it", first.Supersedes, first.SupersedesReason)
	}
	if second.Supersedes != first.ID {
		t.Fatalf("the second decision replaces %q, and the decision recorded before it was %q", second.Supersedes, first.ID)
	}
	if second.SupersedesReason != domain.SupersededLaunchFailed {
		t.Fatalf("the second decision gives %q as its reason, and this machine refused the launch", second.SupersedesReason)
	}
	if first.ID == second.ID {
		t.Fatalf("both answers were recorded under %q, and an audited decision is added rather than edited", first.ID)
	}
	if first.SelectedOfferSnapshotID != "ask-a-clean-record" {
		t.Fatalf("the answer that no longer stands says the Run went to %q, and the record of where it was sent first is nowhere else", first.SelectedOfferSnapshotID)
	}
	if second.SelectedOfferSnapshotID != "ask-b-bad-record" {
		t.Fatalf("the answer that stands says the Run went to %q, and the only machine left is the one whose provider published the worse record", second.SelectedOfferSnapshotID)
	}
	// The identity answers for the content on both, which is what makes the naming
	// above worth anything: a chain of records nobody can re-derive is a chain that
	// can be assembled after the fact.
	for _, decision := range chain {
		identity, err := decision.Identity()
		if err != nil {
			t.Fatalf("re-derive decision %q: %v", decision.ID, err)
		}
		if identity != decision.ID {
			t.Fatalf("decision %q re-derives to %q from its own recorded content", decision.ID, identity)
		}
	}
}
