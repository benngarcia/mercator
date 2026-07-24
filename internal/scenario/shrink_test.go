package scenario

import (
	"context"
	"reflect"
	"testing"
)

func TestShrinkBlueprintRemovesEveryGeneratedSemanticDimension(t *testing.T) {
	original, _, err := GenerateBlueprint(GeneratorConfig{
		Seed:             "shrinker-fixture",
		ArrivalType:      ArrivalFixed,
		RunCount:         4,
		RentalCount:      2,
		MarketplaceCount: 2,
		ImageCount:       2,
		ArtifactCount:    2,
		IncludeFaults:    true,
	})
	if err != nil {
		t.Fatalf("generate Blueprint: %v", err)
	}
	original.Faults[0].Action = FaultRejectCommand
	original.Faults = append(original.Faults, FaultSpec{
		ID: "unrelated-duplicate-response",
		Trigger: FaultTriggerSpec{
			Operation: "provider.launch",
			Run:       original.Arrivals.Runs[1].Name,
			Attempt:   1,
		},
		Action: FaultDuplicateResponse,
	})
	preservesRejectedLaunch := func(_ context.Context, blueprint Blueprint) (bool, error) {
		for _, fault := range blueprint.Faults {
			if fault.ID == "generated-provider-fault" && fault.Action == FaultRejectCommand {
				return true, nil
			}
		}
		return false, nil
	}

	first, err := ShrinkBlueprint(context.Background(), original, preservesRejectedLaunch)
	if err != nil {
		t.Fatalf("shrink first Blueprint: %v", err)
	}
	second, err := ShrinkBlueprint(context.Background(), original, preservesRejectedLaunch)
	if err != nil {
		t.Fatalf("shrink second Blueprint: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatal("semantic shrinking is not deterministic")
	}
	if err := first.Blueprint.validate(); err != nil {
		t.Fatalf("minimized Blueprint is invalid: %v", err)
	}
	for _, dimension := range []string{
		"runs",
		"rentals",
		"offers",
		"image_layers",
		"artifacts",
		"faults",
		"optional_fields",
	} {
		if !hasShrinkDimension(first.Steps, dimension) {
			t.Fatalf("shrink steps have no %q reduction: %+v", dimension, first.Steps)
		}
	}
}

func TestPersistedMinimizedFailureIsIrreducible(t *testing.T) {
	blueprint, err := LoadBlueprint("scenarios/minimized/provider-rejection-single-run.json")
	if err != nil {
		t.Fatalf("load persisted minimized failure: %v", err)
	}
	preservesFingerprint := func(_ context.Context, candidate Blueprint) (bool, error) {
		for _, fault := range candidate.Faults {
			if fault.ID == "rejected-provider-launch" && fault.Action == FaultRejectCommand {
				return true, nil
			}
		}
		return false, nil
	}

	result, err := ShrinkBlueprint(context.Background(), blueprint, preservesFingerprint)
	if err != nil {
		t.Fatalf("shrink persisted failure: %v", err)
	}
	if len(result.Steps) != 0 {
		t.Fatalf("persisted minimized failure still shrinks: %+v", result.Steps)
	}
}

func TestShrinkBlueprintRemovesTimelineOperations(t *testing.T) {
	blueprint, err := LoadBlueprint("scenarios/multiple-runs-schedule-in-order.json")
	if err != nil {
		t.Fatalf("load timeline Blueprint: %v", err)
	}

	result, err := ShrinkBlueprint(
		context.Background(),
		blueprint,
		func(context.Context, Blueprint) (bool, error) { return true, nil },
	)
	if err != nil {
		t.Fatalf("shrink timeline Blueprint: %v", err)
	}

	if !hasShrinkDimension(result.Steps, "timeline") {
		t.Fatalf("shrink steps have no timeline reduction: %+v", result.Steps)
	}
	if err := result.Blueprint.validate(); err != nil {
		t.Fatalf("minimized timeline Blueprint is invalid: %v", err)
	}
}

func hasShrinkDimension(steps []ShrinkStep, dimension string) bool {
	for _, step := range steps {
		if step.Dimension == dimension {
			return true
		}
	}
	return false
}
