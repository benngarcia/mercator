package lab

import (
	"context"
	"testing"
	"time"

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

// TestVerticalProofHoldsInTheOrderTheConsoleDrivesIt drives the demo the way the
// browser flow drives it rather than the way the test above does, and asks the
// same fifteen checkpoints of the result.
//
// The two orders are not the same world. The test above steps once, restarts,
// and then drives to completion, so every placement happens after the restart.
// The console steps twice, advances half an hour, restarts, and then advances
// until the consumer closes, so the consumer is placed before the restart and
// against a fleet the other order never presents. A checkpoint can hold in one
// and fail in the other, which is exactly what happened: turning on the class
// weights changed which machine the consumer chose, and nothing outside a
// Playwright job noticed, because nothing outside it drove this order.
//
// It carries no browser and asserts nothing about a rendered page. What it holds
// is the claim the demo exists to make, at the point the console's own sequence
// produces it.
func TestVerticalProofHoldsInTheOrderTheConsoleDrivesIt(t *testing.T) {
	bundle := exportConsoleOrderedBundle(t)
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
	for _, checkpoint := range report.Checkpoints {
		if !checkpoint.Passed {
			t.Errorf("checkpoint %d (%s) failed: %s", checkpoint.Step, checkpoint.Evidence, checkpoint.Detail)
		}
	}
}

// exportConsoleOrderedBundle is web/app/test/browser/lab-console.mjs as a
// sequence of drive commands: the two steps and the advance that put the
// consumer's Booking on the record, the control-plane restart, and then up to
// four hours of virtual time waiting for the consumer to close.
func exportConsoleOrderedBundle(t *testing.T) RunBundle {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
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
		Limits:           testLimits(),
		Policy:           "policy:test",
		MercatorRevision: "revision:test",
	})
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	defer execution.Close()

	ctx := context.Background()
	for _, command := range []DriveCommand{Step(), Step()} {
		if _, err := execution.Drive(ctx, command); err != nil {
			t.Fatalf("drive to the restart boundary: %v", err)
		}
	}
	// The console advances until the consumer's Booking is on the record rather
	// than assuming how long that takes, so this does too. How long it takes is
	// the world's answer: the consumer waits for its producer to publish and then
	// queues behind it, both of which the class of work decides.
	for range 8 {
		if decided(ctx, t, execution) {
			break
		}
		if _, err := execution.Drive(ctx, Advance(30*time.Minute)); err != nil {
			t.Fatalf("advance to the consumer's Booking: %v", err)
		}
	}
	if err := execution.Restart(ctx); err != nil {
		t.Fatalf("restart control plane: %v", err)
	}
	for range 8 {
		if _, err := execution.Drive(ctx, Advance(30*time.Minute)); err != nil {
			t.Fatalf("advance past the restart: %v", err)
		}
	}
	bundle, err := execution.Export(ctx)
	if err != nil {
		t.Fatalf("export Run Bundle: %v", err)
	}
	return bundle
}

// decided reports whether Mercator has recorded a Booking Decision for the
// consumer, which is the event the console waits to see before it restarts.
func decided(ctx context.Context, t *testing.T, execution *Execution) bool {
	t.Helper()
	bundle, err := execution.Export(ctx)
	if err != nil {
		t.Fatalf("export to read the consumer's Booking: %v", err)
	}
	facts, err := readProofFacts(bundle)
	if err != nil {
		t.Fatalf("read the decisions so far: %v", err)
	}
	_, exists := facts.decisions["run-consumer"]
	return exists
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
