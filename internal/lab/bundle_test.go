package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/benngarcia/mercator/internal/scenario"
)

func TestRunBundleIsDeterministicAndReplayable(t *testing.T) {
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile: %v", err)
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
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()
	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive: %v", err)
	}

	first, err := execution.Export(context.Background())
	if err != nil {
		t.Fatalf("export first bundle: %v", err)
	}
	firstBytes, err := first.Bytes()
	if err != nil {
		t.Fatalf("encode first bundle: %v", err)
	}
	replayed, err := Replay(context.Background(), firstBytes)
	if err != nil {
		t.Fatalf("open replay: %v", err)
	}
	defer func() {
		if err := replayed.Close(); err != nil {
			t.Fatalf("close replay: %v", err)
		}
	}()
	if _, err := replayed.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive replay: %v", err)
	}
	second, err := replayed.Export(context.Background())
	if err != nil {
		t.Fatalf("export replayed bundle: %v", err)
	}
	secondBytes, err := second.Bytes()
	if err != nil {
		t.Fatalf("encode replayed bundle: %v", err)
	}

	if string(secondBytes) != string(firstBytes) {
		t.Fatal("replayed bundle bytes differ")
	}
	if second.NormalizedSHA256() != first.NormalizedSHA256() {
		t.Fatalf("normalized hashes differ: %s vs %s", second.NormalizedSHA256(), first.NormalizedSHA256())
	}
	wantEntries := []string{
		"manifest.json",
		"configuration.json",
		"blueprint.json",
		"world-tape.json",
		"drives.jsonl",
		"samples.jsonl",
		"events/mercator.jsonl",
		"events/world.jsonl",
		"effects.jsonl",
		"predictions.jsonl",
		"invariants.json",
		"metrics.json",
	}
	if !reflect.DeepEqual(first.EntryNames(), wantEntries) {
		t.Fatalf("bundle entries = %v", first.EntryNames())
	}
	for _, name := range []string{"events/mercator.jsonl", "events/world.jsonl", "effects.jsonl", "predictions.jsonl"} {
		if len(bundleEntryData(t, first, name)) == 0 {
			t.Fatalf("bundle entry %q is empty", name)
		}
	}
	var invariantResults []InvariantResult
	if err := json.Unmarshal(bundleEntryData(t, first, "invariants.json"), &invariantResults); err != nil {
		t.Fatalf("decode invariant results: %v", err)
	}
	if len(invariantResults) == 0 {
		t.Fatal("Run Bundle has no invariant results")
	}
	for _, result := range invariantResults {
		if result.Status != InvariantPassed {
			t.Fatalf("bundled invariant did not pass: %+v", result)
		}
	}
	for _, forbidden := range [][]byte{[]byte("PrivateData"), []byte("private_data")} {
		if bytes.Contains(firstBytes, forbidden) {
			t.Fatalf("Run Bundle contains private field %q", forbidden)
		}
	}
}

func TestRunBundleCarriesCanonicalUIEvidenceWithoutChangingSemanticHash(t *testing.T) {
	base := exportFixtureBundle(t)

	withEvidence, err := base.WithUIEvidence(UIEvidence{
		Trace: []byte("playwright trace"),
		Screenshots: map[string][]byte{
			"terminal.png": []byte("terminal screenshot"),
			"warmth.png":   []byte("warmth screenshot"),
		},
	})
	if err != nil {
		t.Fatalf("attach UI evidence: %v", err)
	}

	wantEntries := append(
		append([]string(nil), requiredBundleEntries...),
		"ui/trace.zip",
		"ui/screenshots/terminal.png",
		"ui/screenshots/warmth.png",
	)
	if !reflect.DeepEqual(withEvidence.EntryNames(), wantEntries) {
		t.Fatalf("bundle entries = %v", withEvidence.EntryNames())
	}
	if withEvidence.NormalizedSHA256() != base.NormalizedSHA256() {
		t.Fatalf(
			"UI evidence changed semantic hash: %s vs %s",
			withEvidence.NormalizedSHA256(),
			base.NormalizedSHA256(),
		)
	}
	archive, err := withEvidence.Bytes()
	if err != nil {
		t.Fatalf("encode bundle: %v", err)
	}
	replayed, err := Replay(context.Background(), archive)
	if err != nil {
		t.Fatalf("replay bundle with UI evidence: %v", err)
	}
	if err := replayed.Close(); err != nil {
		t.Fatalf("close replay: %v", err)
	}
}

func TestRunBundleRejectsUnsafeUIEvidenceNames(t *testing.T) {
	bundle := exportFixtureBundle(t)

	if _, err := bundle.WithUIEvidence(UIEvidence{
		Screenshots: map[string][]byte{"../secret.png": []byte("no")},
	}); err == nil {
		t.Fatal("bundle accepted a path-traversing screenshot name")
	}
}

func TestNormalizedRunBundleIgnoresAnEquivalentControlPlaneRestart(t *testing.T) {
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	config := Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           testLimits(),
		Policy:           "policy:test",
		MercatorRevision: "revision:test",
	}
	baseline := exportTerminalBundle(t, config, false)
	restarted := exportTerminalBundle(t, config, true)

	if restarted.NormalizedSHA256() != baseline.NormalizedSHA256() {
		t.Fatalf(
			"equivalent restart hash = %s, want %s",
			restarted.NormalizedSHA256(),
			baseline.NormalizedSHA256(),
		)
	}
}

func exportTerminalBundle(t *testing.T, config Config, extraRestart bool) RunBundle {
	t.Helper()
	execution, err := Open(context.Background(), config)
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	defer execution.Close()
	if extraRestart {
		if _, err := execution.Drive(context.Background(), Step()); err != nil {
			t.Fatalf("drive to restart boundary: %v", err)
		}
		if err := execution.Restart(context.Background()); err != nil {
			t.Fatalf("restart control plane: %v", err)
		}
	}
	if _, err := execution.DriveToCompletion(context.Background()); err != nil {
		t.Fatalf("drive terminal execution: %v", err)
	}
	bundle, err := execution.Export(context.Background())
	if err != nil {
		t.Fatalf("export terminal Run Bundle: %v", err)
	}
	return bundle
}

func TestReplayRejectsAnUnsupportedBundledBlueprint(t *testing.T) {
	bundle := exportFixtureBundle(t)
	replaceBundleJSON(t, &bundle, "blueprint.json", func(document map[string]any) {
		document["schema"] = "mercator.lab/blueprint.v999"
	})
	archive, err := bundle.Bytes()
	if err != nil {
		t.Fatalf("encode bundle: %v", err)
	}

	if _, err := Replay(context.Background(), archive); err == nil {
		t.Fatal("replay accepted an unsupported bundled Blueprint")
	}
}

func TestReplayRejectsUnknownFieldsAndNoncanonicalEntryOrder(t *testing.T) {
	t.Run("unknown field", func(t *testing.T) {
		bundle := exportFixtureBundle(t)
		replaceBundleJSON(t, &bundle, "configuration.json", func(document map[string]any) {
			document["fallback_policy"] = "forbidden"
		})
		archive, err := bundle.Bytes()
		if err != nil {
			t.Fatalf("encode bundle: %v", err)
		}

		if _, err := Replay(context.Background(), archive); err == nil {
			t.Fatal("replay accepted an unknown configuration field")
		}
	})

	t.Run("entry order", func(t *testing.T) {
		bundle := exportFixtureBundle(t)
		bundle.entries[0], bundle.entries[1] = bundle.entries[1], bundle.entries[0]
		archive, err := bundle.Bytes()
		if err != nil {
			t.Fatalf("encode bundle: %v", err)
		}

		if _, err := Replay(context.Background(), archive); err == nil {
			t.Fatal("replay accepted noncanonical entry order")
		}
	})
}

func TestReconstructRejectsAnUnreachableRecordedPredicate(t *testing.T) {
	bundle := exportFixtureBundle(t)
	for index := range bundle.entries {
		if bundle.entries[index].name == "drives.jsonl" {
			bundle.entries[index].data = []byte(`{"kind":"until_predicate","target_transitions":999}` + "\n")
		}
	}

	if _, err := Reconstruct(context.Background(), bundle); err == nil {
		t.Fatal("reconstruction accepted an unreachable predicate target")
	}
}

func exportFixtureBundle(t *testing.T) RunBundle {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile: %v", err)
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
		t.Fatalf("open: %v", err)
	}
	bundle, err := execution.Export(context.Background())
	if err != nil {
		t.Fatalf("export bundle: %v", err)
	}
	if err := execution.Close(); err != nil {
		t.Fatalf("close execution: %v", err)
	}
	return bundle
}

func replaceBundleJSON(t *testing.T, bundle *RunBundle, name string, replace func(map[string]any)) {
	t.Helper()
	for index := range bundle.entries {
		if bundle.entries[index].name != name {
			continue
		}
		var document map[string]any
		if err := json.Unmarshal(bundle.entries[index].data, &document); err != nil {
			t.Fatalf("decode Blueprint entry: %v", err)
		}
		replace(document)
		encoded, err := canonicalJSON(document)
		if err != nil {
			t.Fatalf("encode Blueprint entry: %v", err)
		}
		bundle.entries[index].data = encoded
		return
	}
	t.Fatalf("bundle has no %s entry", name)
}

func bundleEntryData(t *testing.T, bundle RunBundle, name string) []byte {
	t.Helper()
	for _, entry := range bundle.entries {
		if entry.name == name {
			return entry.data
		}
	}
	t.Fatalf("bundle has no %s entry", name)
	return nil
}
