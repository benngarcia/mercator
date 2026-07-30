package lab

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/scenario"
)

func TestCompileProducesStablePolicyNeutralWorldTape(t *testing.T) {
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}

	firstTape, firstSamples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile first tape: %v", err)
	}
	secondTape, secondSamples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile second tape: %v", err)
	}

	if !reflect.DeepEqual(firstTape, secondTape) || !reflect.DeepEqual(firstSamples, secondSamples) {
		t.Fatalf("repeated compilation changed tape or samples")
	}
	if err := firstTape.Validate(); err != nil {
		t.Fatalf("validate tape: %v", err)
	}
	if len(firstTape.Events) != len(blueprint.Arrivals.Runs) {
		t.Fatalf("tape events = %d, want %d arrivals", len(firstTape.Events), len(blueprint.Arrivals.Runs))
	}
	for _, event := range firstTape.Events {
		if event.Kind != EventRunArrived {
			t.Fatalf("event kind = %q, want %q", event.Kind, EventRunArrived)
		}
		var arrival RunArrival
		if err := json.Unmarshal(event.Data, &arrival); err != nil {
			t.Fatalf("decode arrival: %v", err)
		}
		if arrival.Policy != "" {
			t.Fatalf("World Tape arrival leaked policy %q", arrival.Policy)
		}
	}
}

func TestCompileRejectsAnUnsupportedBlueprintSchema(t *testing.T) {
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	blueprint.Schema = "mercator.lab/blueprint.v999"

	if _, _, err := Compile(blueprint, CompileOptions{}); err == nil {
		t.Fatal("compiled an unsupported Blueprint schema")
	}
}

func TestCompileSamplesActualRuntimeIndependentlyFromMercatorPrediction(t *testing.T) {
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	firstTape, firstSamples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile first tape: %v", err)
	}

	changed := scenario.Duration(30 * time.Second)
	blueprint.Arrivals.Runs[0].Request.ExpectedRuntime = &changed
	secondTape, secondSamples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile second tape: %v", err)
	}

	firstArrival := findRunArrival(t, firstTape, "producer")
	secondArrival := findRunArrival(t, secondTape, "producer")
	if firstArrival.ActualRuntime != secondArrival.ActualRuntime {
		t.Fatalf(
			"actual runtime changed with prediction: %s vs %s",
			firstArrival.ActualRuntime.Duration(),
			secondArrival.ActualRuntime.Duration(),
		)
	}
	if len(firstSamples) != len(blueprint.Arrivals.Runs) ||
		len(secondSamples) != len(blueprint.Arrivals.Runs) {
		t.Fatalf("runtime samples = %d and %d, want %d", len(firstSamples), len(secondSamples), len(blueprint.Arrivals.Runs))
	}
	if firstSamples[0].Key != "world.actual-runtime/run/producer" {
		t.Fatalf("sample key = %q", firstSamples[0].Key)
	}
}

func TestCompilePreservesAuthoredOrderForSimultaneousArrivals(t *testing.T) {
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}

	tape, _, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}

	first := decodeRunArrival(t, tape.Events[0])
	second := decodeRunArrival(t, tape.Events[1])
	if first.Name != "producer" || second.Name != "consumer" {
		t.Fatalf("simultaneous arrivals = %q then %q, want authored producer then consumer", first.Name, second.Name)
	}
	if tape.Events[0].Sequence != 1 || tape.Events[1].Sequence != 2 {
		t.Fatalf("event sequences = %d and %d, want 1 and 2", tape.Events[0].Sequence, tape.Events[1].Sequence)
	}
}

func decodeRunArrival(t *testing.T, event WorldEvent) RunArrival {
	t.Helper()
	var arrival RunArrival
	if err := json.Unmarshal(event.Data, &arrival); err != nil {
		t.Fatalf("decode Run arrival: %v", err)
	}
	return arrival
}

func findRunArrival(t *testing.T, tape WorldTape, name string) RunArrival {
	t.Helper()
	for _, event := range tape.Events {
		arrival := decodeRunArrival(t, event)
		if arrival.Name == name {
			return arrival
		}
	}
	t.Fatalf("World Tape has no Run arrival %q", name)
	return RunArrival{}
}

func TestWorldTapeRejectsUnstableOrdering(t *testing.T) {
	t.Run("virtual time moves backward", func(t *testing.T) {
		tape := tapeWithEvents(
			worldEvent("event-b", 2, 2, "second"),
			worldEvent("event-a", 1, 1, "first"),
		)

		if err := tape.Validate(); err == nil {
			t.Fatal("out-of-order World Tape validated")
		}
	})

	t.Run("global sequence moves backward", func(t *testing.T) {
		tape := tapeWithEvents(
			worldEvent("event-a", time.Second, 2, "first"),
			worldEvent("event-b", 2*time.Second, 1, "second"),
		)

		if err := tape.Validate(); err == nil {
			t.Fatal("World Tape with decreasing global sequence validated")
		}
	})
}

func TestWorldTapeRequiresIdentityAndJSONEventData(t *testing.T) {
	t.Run("Blueprint name", func(t *testing.T) {
		tape := tapeWithEvents(worldEvent("event-a", time.Second, 1, "first"))
		tape.BlueprintName = ""

		if err := tape.Validate(); err == nil {
			t.Fatal("World Tape without a Blueprint name validated")
		}
	})

	t.Run("event JSON", func(t *testing.T) {
		tape := tapeWithEvents(worldEvent("event-a", time.Second, 1, "first"))
		tape.Events[0].Data = []byte(`{`)

		if err := tape.Validate(); err == nil {
			t.Fatal("World Tape with invalid event JSON validated")
		}
	})
}
