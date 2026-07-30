package lab

import (
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
	if _, err := execution.Drive(context.Background(), Quiesce()); err != nil {
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
	if _, err := replayed.Drive(context.Background(), Quiesce()); err != nil {
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
