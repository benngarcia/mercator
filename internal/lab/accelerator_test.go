package lab

import (
	"context"
	"testing"
)

// TestAMachineNobodyCountedIsRefusedAsASilence drives the corpus Blueprint
// through the Lab, which is where the refusal is graded against the standing
// rules rather than against a fixture's expectation.
//
// The placement corpus states what the two Runs there are told. What this adds is that
// the fleet's own answer has to survive being recounted off the decision beside
// it: safety.a_silence_is_not_an_answer_about_capacity reads every recorded wait
// against the candidates the decision recorded, and a machine struck out for an
// inventory nobody took counts as one that said too little rather than as one
// that can never run the work. An accelerator floor read as a measured zero puts
// the machine in the third column and the whole deployment loses its ordering to
// one nvidia-smi that would not run.
func TestAMachineNobodyCountedIsRefusedAsASilence(t *testing.T) {
	execution := openBlueprintExecution(t,
		"testdata/blueprints/a-machine-nobody-counted-is-not-a-machine-with-no-cards.json", DefaultLimits())
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()

	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive the Blueprint: %v", err)
	}

	candidate := recordedCandidate(t, execution, "run-pinned-to-eight-cards", "rental-nobody-counted")
	if candidate.Feasible {
		t.Fatalf("a Run pinned to eight cards was called feasible on a machine nobody counted: %+v", candidate)
	}
	if len(candidate.Rejections) != 1 {
		t.Fatalf("a machine nobody counted was refused %+v", candidate.Rejections)
	}
	refusal := candidate.Rejections[0]
	if refusal.Code != "UNKNOWN_FACT" || refusal.Path != "resources.accelerators" || !refusal.Unstated {
		t.Fatalf("a machine nobody counted was refused %+v, and it published no inventory to be short of", refusal)
	}
}
