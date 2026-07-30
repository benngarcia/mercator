package scenario

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestGenerateBlueprintBuildsEveryRequiredWorldDimension(t *testing.T) {
	for _, arrivalType := range []ArrivalType{ArrivalFixed, ArrivalPeriodic, ArrivalBurst} {
		t.Run(string(arrivalType), func(t *testing.T) {
			blueprint, samples, err := GenerateBlueprint(GeneratorConfig{
				Seed:             "generator-fixture-" + string(arrivalType),
				ArrivalType:      arrivalType,
				RunCount:         4,
				RentalCount:      2,
				MarketplaceCount: 2,
				ImageCount:       3,
				ArtifactCount:    2,
				IncludeFaults:    true,
			})
			if err != nil {
				t.Fatalf("generate Blueprint: %v", err)
			}
			if err := blueprint.validate(); err != nil {
				t.Fatalf("validate generated Blueprint: %v", err)
			}
			encoded, err := json.Marshal(blueprint)
			if err != nil {
				t.Fatalf("encode generated Blueprint: %v", err)
			}
			var decoded Blueprint
			if err := strictUnmarshal(encoded, &decoded); err != nil {
				t.Fatalf("strictly decode generated Blueprint: %v", err)
			}
			decoded.Name = blueprint.Name
			if err := decoded.validate(); err != nil {
				t.Fatalf("validate generated Blueprint round trip: %v", err)
			}
			runs, err := blueprint.Arrivals.ExpandedRuns()
			if err != nil {
				t.Fatalf("expand arrivals: %v", err)
			}
			if len(runs) != 4 {
				t.Fatalf("expanded Runs = %d, want 4", len(runs))
			}
			if len(blueprint.World.Images) != 3 ||
				len(blueprint.World.Artifacts) != 2 ||
				len(blueprint.World.Rentals) != 2 ||
				len(blueprint.World.Marketplace) != 2 ||
				len(blueprint.World.Paths) != 8 ||
				len(blueprint.World.RuntimeModels) != 16 ||
				len(samples) == 0 {
				t.Fatalf("generated dimensions are incomplete: world=%+v samples=%d", blueprint.World, len(samples))
			}
			if len(runs[0].Request.Phases) != 2 {
				t.Fatalf("generated workload phases = %+v", runs[0].Request.Phases)
			}
			if blueprint.World.Marketplace[0].Region == "" ||
				blueprint.World.Marketplace[0].Billing.MinimumCharge == nil ||
				blueprint.World.Marketplace[0].Available == nil {
				t.Fatalf("generated provider market facts are incomplete: %+v", blueprint.World.Marketplace[0])
			}
			if len(blueprint.Faults) != 1 {
				t.Fatalf("generated faults = %d, want 1", len(blueprint.Faults))
			}
		})
	}
}

func TestGenerateBlueprintIsDeterministicAndKeyed(t *testing.T) {
	config := GeneratorConfig{
		Seed:             "generator-determinism",
		ArrivalType:      ArrivalFixed,
		RunCount:         3,
		RentalCount:      2,
		MarketplaceCount: 2,
		ImageCount:       2,
		ArtifactCount:    1,
		IncludeFaults:    true,
	}
	first, firstSamples, err := GenerateBlueprint(config)
	if err != nil {
		t.Fatalf("generate first Blueprint: %v", err)
	}
	second, secondSamples, err := GenerateBlueprint(config)
	if err != nil {
		t.Fatalf("generate second Blueprint: %v", err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(firstSamples, secondSamples) {
		t.Fatal("same generator seed changed Blueprint or samples")
	}

	config.ImageCount++
	_, extendedSamples, err := GenerateBlueprint(config)
	if err != nil {
		t.Fatalf("generate extended Blueprint: %v", err)
	}
	extended := samplesByKey(extendedSamples)
	for key, value := range samplesByKey(firstSamples) {
		if extendedValue, exists := extended[key]; exists && extendedValue != value {
			t.Fatalf("unrelated generation changed keyed sample %q from %d to %d", key, value, extendedValue)
		}
	}
}

func samplesByKey(samples []GenerationSample) map[string]uint64 {
	indexed := make(map[string]uint64, len(samples))
	for _, sample := range samples {
		indexed[sample.Key] = sample.Value
	}
	return indexed
}
