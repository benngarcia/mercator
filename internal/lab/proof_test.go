package lab

import (
	"context"
	"testing"

	"github.com/benngarcia/mercator/internal/scenario"
)

func TestVerifyVerticalProofPassesEveryDeclaredCheckpoint(t *testing.T) {
	bundle := exportVerticalProofBundle(t)
	bundle, err := bundle.WithUIEvidence(UIEvidence{
		Trace: []byte("PK trace"),
		Screenshots: map[string][]byte{
			"terminal-lifecycle-visible.png": []byte("\x89PNG\r\n\x1a\n"),
		},
	})
	if err != nil {
		t.Fatalf("attach UI evidence: %v", err)
	}

	report, err := VerifyVerticalProof(context.Background(), bundle)
	if err != nil {
		t.Fatalf("verify vertical proof: %v", err)
	}

	if len(report.Checkpoints) != 15 {
		t.Fatalf("proof checkpoints = %d, want 15", len(report.Checkpoints))
	}
	for _, checkpoint := range report.Checkpoints {
		if !checkpoint.Passed {
			t.Fatalf("checkpoint %d failed: %+v", checkpoint.Step, checkpoint)
		}
	}
}

func TestVerifyVerticalProofRequiresBrowserEvidence(t *testing.T) {
	bundle := exportVerticalProofBundle(t)

	report, err := VerifyVerticalProof(context.Background(), bundle)

	if err == nil {
		t.Fatal("proof passed without browser evidence")
	}
	if report.Checkpoints[12].Passed {
		t.Fatal("ui_rendered checkpoint passed without browser evidence")
	}
}

func exportVerticalProofBundle(t *testing.T) RunBundle {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	return exportTerminalBundle(t, Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           testLimits(),
		Policy:           "policy:test",
		MercatorRevision: "revision:test",
	}, true)
}
