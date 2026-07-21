package scenario

import (
	"strings"
	"testing"
)

// TestPlacementScenarios executes the corpus against simulated capacity.
//
// Green scenarios assert current behavior: any failure is a regression and
// fails CI. Target scenarios encode the contract of unbuilt semantics: their
// failures are reported as pending (skipped, with the diff visible), and a
// target scenario that starts passing fails CI until it is promoted to green.
func TestPlacementScenarios(t *testing.T) {
	scenarios, err := LoadCorpus("scenarios")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	if len(scenarios) == 0 {
		t.Fatalf("the corpus is empty")
	}
	for _, sc := range scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			result, err := Run(SimBackend{}, sc)
			if err != nil {
				t.Fatalf("run scenario: %v", err)
			}
			for _, note := range result.Notes {
				t.Logf("note: %s", note)
			}
			switch {
			case sc.Status == StatusGreen && len(result.Failures) > 0:
				t.Errorf("green scenario regressed:\n  %s", strings.Join(result.Failures, "\n  "))
			case sc.Status == StatusTarget && len(result.Failures) == 0:
				t.Errorf("target scenario now passes; promote its status to green")
			case sc.Status == StatusTarget:
				t.Skipf("PENDING (target contract not built yet):\n  %s", strings.Join(result.Failures, "\n  "))
			}
		})
	}
}

// TestCorpusCoversBothStatuses keeps the corpus honest about its own shape:
// it must always contain green cases (regression protection) and, until the
// program completes, the target cases the milestones are built to satisfy.
func TestCorpusCoversBothStatuses(t *testing.T) {
	scenarios, err := LoadCorpus("scenarios")
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	counts := map[Status]int{}
	for _, sc := range scenarios {
		counts[sc.Status]++
	}
	if counts[StatusGreen] == 0 {
		t.Errorf("corpus has no green scenarios; current behavior is unprotected")
	}
	t.Logf("corpus: %d green, %d target", counts[StatusGreen], counts[StatusTarget])
}
