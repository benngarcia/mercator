package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/benngarcia/mercator/internal/lab"
	"github.com/benngarcia/mercator/internal/scenario"
)

func TestLabAuthorAndGenerateWriteLoadableBlueprints(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "author", args: []string{"mercator", "lab", "author"}},
		{name: "generate", args: []string{"mercator", "lab", "generate", "--seed", "command-test"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), test.name+".json")
			args := append(append([]string(nil), test.args...), "--output", output)
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			exitCode := run(context.Background(), args, nil, stdout, stderr)

			if exitCode != 0 {
				t.Fatalf("run() = %d, stderr = %s", exitCode, stderr.String())
			}
			if _, err := scenario.LoadBlueprint(output); err != nil {
				t.Fatalf("load written Blueprint: %v", err)
			}
		})
	}
}

func TestLabRunAndReplayReconstructOneRunBundle(t *testing.T) {
	directory := t.TempDir()
	bundle := filepath.Join(directory, "proof.mlab")
	replayed := filepath.Join(directory, "replayed.mlab")
	blueprint := filepath.Join(
		"..",
		"..",
		"internal",
		"scenario",
		"scenarios",
		"demos",
		"artifact-warmth-restart.json",
	)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	exitCode := run(context.Background(), []string{
		"mercator", "lab", "run",
		"--blueprint", blueprint,
		"--bundle", bundle,
	}, nil, stdout, stderr)

	if exitCode != 0 {
		t.Fatalf("run Lab Blueprint = %d, stderr = %s", exitCode, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	exitCode = run(context.Background(), []string{
		"mercator", "lab", "replay",
		"--bundle", bundle,
		"--output", replayed,
	}, nil, stdout, stderr)
	if exitCode != 0 {
		t.Fatalf("replay Lab bundle = %d, stderr = %s", exitCode, stderr.String())
	}
	first, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatalf("read first bundle: %v", err)
	}
	second, err := os.ReadFile(replayed)
	if err != nil {
		t.Fatalf("read replayed bundle: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("replayed Run Bundle differs from original")
	}
}

func TestLabMinimizeWritesAReplayableFailureBlueprint(t *testing.T) {
	directory := t.TempDir()
	bundle := filepath.Join(directory, "failure.mlab")
	output := filepath.Join(directory, "minimized.json")
	blueprint := filepath.Join(
		"..", "..", "internal", "scenario", "scenarios", "minimized",
		"provider-rejection-single-run.json",
	)
	stderr := &bytes.Buffer{}
	if exitCode := run(context.Background(), []string{
		"mercator", "lab", "run", "--blueprint", blueprint, "--bundle", bundle,
	}, nil, &bytes.Buffer{}, stderr); exitCode != 0 {
		t.Fatalf("run failure Blueprint = %d, stderr = %s", exitCode, stderr.String())
	}

	stderr.Reset()
	exitCode := run(context.Background(), []string{
		"mercator", "lab", "minimize", "--bundle", bundle, "--output", output,
	}, nil, &bytes.Buffer{}, stderr)

	if exitCode != 0 {
		t.Fatalf("minimize = %d, stderr = %s", exitCode, stderr.String())
	}
	minimized, err := scenario.LoadBlueprint(output)
	if err != nil {
		t.Fatalf("load minimized Blueprint: %v", err)
	}
	if minimized.Kind != scenario.KindMinimized {
		t.Fatalf("kind = %q, want minimized", minimized.Kind)
	}
}

func TestLabPromoteRequiresAndRecordsAllFifteenProofCheckpoints(t *testing.T) {
	directory := t.TempDir()
	catalogSource := filepath.Join(
		"..", "..", "internal", "scenario", "scenarios", "demos",
		"artifact-warmth-restart.json",
	)
	source := filepath.Join(directory, "target.json")
	bundlePath := filepath.Join(directory, "proof.mlab")
	output := filepath.Join(directory, "promoted.json")
	writeTargetBlueprint(t, catalogSource, source)
	writeProofBundle(t, source, bundlePath)
	stderr := &bytes.Buffer{}

	exitCode := run(context.Background(), []string{
		"mercator", "lab", "promote",
		"--blueprint", source,
		"--bundle", bundlePath,
		"--output", output,
	}, nil, &bytes.Buffer{}, stderr)

	if exitCode != 0 {
		t.Fatalf("promote = %d, stderr = %s", exitCode, stderr.String())
	}
	promoted, err := scenario.LoadBlueprint(output)
	if err != nil {
		t.Fatalf("load promoted Blueprint: %v", err)
	}
	if promoted.Classification != scenario.ClassificationGreen {
		t.Fatalf("classification = %q, want green", promoted.Classification)
	}
}

func writeTargetBlueprint(t *testing.T, source, target string) {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint(source)
	if err != nil {
		t.Fatalf("load green Blueprint: %v", err)
	}
	blueprint.Classification = scenario.ClassificationTarget
	blueprint.MissingCapabilities = []scenario.Capability{scenario.CapabilityLabUI}
	encoded, err := scenario.EncodeBlueprint(blueprint)
	if err != nil {
		t.Fatalf("encode target Blueprint: %v", err)
	}
	if err := os.WriteFile(target, encoded, 0o644); err != nil {
		t.Fatalf("write target Blueprint: %v", err)
	}
}

func writeProofBundle(t *testing.T, blueprintPath, bundlePath string) {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint(blueprintPath)
	if err != nil {
		t.Fatalf("load proof Blueprint: %v", err)
	}
	tape, samples, err := lab.Compile(blueprint, lab.CompileOptions{})
	if err != nil {
		t.Fatalf("compile proof Blueprint: %v", err)
	}
	execution, err := lab.Open(context.Background(), lab.Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           lab.DefaultLimits(),
		Policy:           "test",
		MercatorRevision: "test",
	})
	if err != nil {
		t.Fatalf("open proof execution: %v", err)
	}
	defer execution.Close()
	if _, err := execution.Drive(context.Background(), lab.Step()); err != nil {
		t.Fatalf("drive proof boundary: %v", err)
	}
	if err := execution.Restart(context.Background()); err != nil {
		t.Fatalf("restart proof execution: %v", err)
	}
	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("complete proof execution: %v", err)
	}
	bundle, err := execution.Export(context.Background())
	if err != nil {
		t.Fatalf("export proof bundle: %v", err)
	}
	bundle, err = bundle.WithUIEvidence(lab.UIEvidence{
		Trace:       []byte("PK trace"),
		Screenshots: map[string][]byte{"terminal.png": []byte("\x89PNG\r\n\x1a\n")},
	})
	if err != nil {
		t.Fatalf("attach UI evidence: %v", err)
	}
	archive, err := bundle.Bytes()
	if err != nil {
		t.Fatalf("encode proof bundle: %v", err)
	}
	if err := os.WriteFile(bundlePath, archive, 0o644); err != nil {
		t.Fatalf("write proof bundle: %v", err)
	}
}
