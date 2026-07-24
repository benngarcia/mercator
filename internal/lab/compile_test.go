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
