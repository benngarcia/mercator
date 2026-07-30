package lab

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/scenario"
)

func TestGeneratedBlueprintCompilesCandidateSpecificActualsAndRunsRealControlPlane(t *testing.T) {
	blueprint, _, err := scenario.GenerateBlueprint(scenario.GeneratorConfig{
		Seed:             "lab-generated-integration",
		ArrivalType:      scenario.ArrivalFixed,
		RunCount:         3,
		RentalCount:      2,
		MarketplaceCount: 2,
		ImageCount:       2,
		ArtifactCount:    1,
	})
	if err != nil {
		t.Fatalf("generate Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile generated Blueprint: %v", err)
	}
	if len(samples) != 3*(1+4) {
		t.Fatalf("World Tape samples = %d, want 15 default and candidate-specific actuals", len(samples))
	}
	for _, event := range tape.Events {
		var arrival RunArrival
		if err := json.Unmarshal(event.Data, &arrival); err != nil {
			t.Fatalf("decode generated Run arrival: %v", err)
		}
		if len(arrival.ActualRuntimeByOffer) != 4 {
			t.Fatalf("candidate actual runtimes = %d, want 4", len(arrival.ActualRuntimeByOffer))
		}
	}

	execution, err := Open(context.Background(), Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           testLimits(),
		Policy:           "policy:generated",
		MercatorRevision: "revision:test",
	})
	if err != nil {
		t.Fatalf("open generated execution: %v", err)
	}
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close generated execution: %v", err)
		}
	}()
	if _, err := execution.Drive(context.Background(), Quiesce()); err != nil {
		t.Fatalf("drive generated arrivals: %v", err)
	}
	assertSelectedCandidateActual(t, tape, execution.runtime.world.effectRecords())
	for range 3 {
		if _, err := execution.Drive(context.Background(), Advance(15*time.Minute)); err != nil {
			t.Fatalf("advance generated execution: %v", err)
		}
	}
	if _, err := execution.Check(context.Background()); err != nil {
		t.Fatalf("check generated invariants: %v", err)
	}
	bundle, err := execution.Export(context.Background())
	if err != nil {
		t.Fatalf("export generated execution: %v", err)
	}
	archive, err := bundle.Bytes()
	if err != nil {
		t.Fatalf("encode generated Run Bundle: %v", err)
	}
	replayed, err := Replay(context.Background(), archive)
	if err != nil {
		t.Fatalf("open generated Run Bundle: %v", err)
	}
	if err := replayed.Close(); err != nil {
		t.Fatalf("close generated replay: %v", err)
	}
}

func assertSelectedCandidateActual(t *testing.T, tape WorldTape, effects []EffectRecord) {
	t.Helper()
	for _, effect := range effects {
		if effect.Operation != OperationProviderLaunch || effect.Command != EffectCommandAccepted {
			continue
		}
		var request struct {
			OfferID string `json:"offer_id"`
		}
		var consequence struct {
			ActualRuntimeSeconds float64 `json:"actual_runtime_seconds"`
		}
		if err := json.Unmarshal(effect.Request, &request); err != nil {
			t.Fatalf("decode generated launch request effect: %v", err)
		}
		if err := json.Unmarshal(effect.Consequence, &consequence); err != nil {
			t.Fatalf("decode generated launch consequence effect: %v", err)
		}
		arrival := findRunArrival(t, tape, effect.CorrelationID[len("run-"):])
		want := arrival.ActualRuntimeByOffer[request.OfferID].Duration().Seconds()
		if consequence.ActualRuntimeSeconds != want {
			t.Fatalf(
				"selected candidate actual = %f, want tape value %f for %q",
				consequence.ActualRuntimeSeconds,
				want,
				request.OfferID,
			)
		}
		return
	}
	t.Fatal("generated execution recorded no accepted provider launch")
}

func TestMinimizedFailureFingerprintReplaysFromOneRunBundle(t *testing.T) {
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/minimized/provider-rejection-single-run.json")
	if err != nil {
		t.Fatalf("load minimized Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile minimized Blueprint: %v", err)
	}
	config := Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           testLimits(),
		Policy:           "policy:minimized",
		MercatorRevision: "revision:test",
	}
	execution, err := Open(context.Background(), config)
	if err != nil {
		t.Fatalf("open minimized execution: %v", err)
	}
	if _, err := execution.Drive(context.Background(), Quiesce()); err != nil {
		t.Fatalf("drive minimized failure: %v", err)
	}
	assertEffect(
		t,
		execution.runtime.world.effectRecords(),
		OperationProviderLaunch,
		"run-failure",
		EffectCommandRejected,
		EffectResponseDelivered,
	)
	bundle, err := execution.Export(context.Background())
	if err != nil {
		t.Fatalf("export minimized failure: %v", err)
	}
	if err := execution.Close(); err != nil {
		t.Fatalf("close minimized execution: %v", err)
	}
	archive, err := bundle.Bytes()
	if err != nil {
		t.Fatalf("encode minimized Run Bundle: %v", err)
	}

	replayed, err := Replay(context.Background(), archive)
	if err != nil {
		t.Fatalf("open minimized replay: %v", err)
	}
	defer func() {
		if err := replayed.Close(); err != nil {
			t.Fatalf("close minimized replay: %v", err)
		}
	}()
	if _, err := replayed.Drive(context.Background(), Quiesce()); err != nil {
		t.Fatalf("drive minimized replay: %v", err)
	}
	assertEffect(
		t,
		replayed.runtime.world.effectRecords(),
		OperationProviderLaunch,
		"run-failure",
		EffectCommandRejected,
		EffectResponseDelivered,
	)
	replayedBundle, err := replayed.Export(context.Background())
	if err != nil {
		t.Fatalf("export minimized replay: %v", err)
	}
	if bundle.NormalizedSHA256() != replayedBundle.NormalizedSHA256() {
		t.Fatalf(
			"minimized replay hash = %s, want %s",
			replayedBundle.NormalizedSHA256(),
			bundle.NormalizedSHA256(),
		)
	}
}

func FuzzGeneratedBlueprintCompilesAndPreservesInvariants(f *testing.F) {
	f.Add([]byte("fixed-seed"), byte(0), byte(3), byte(2), byte(2))
	f.Add([]byte("periodic-seed"), byte(1), byte(2), byte(1), byte(3))
	f.Add([]byte("burst-seed"), byte(2), byte(4), byte(3), byte(1))

	f.Fuzz(func(t *testing.T, seed []byte, arrival, runs, rentals, offers byte) {
		if len(seed) == 0 || len(seed) > 64 {
			return
		}
		arrivalTypes := []scenario.ArrivalType{
			scenario.ArrivalFixed,
			scenario.ArrivalPeriodic,
			scenario.ArrivalBurst,
		}
		blueprint, _, err := scenario.GenerateBlueprint(scenario.GeneratorConfig{
			Seed:             string(seed),
			ArrivalType:      arrivalTypes[int(arrival)%len(arrivalTypes)],
			RunCount:         1 + int(runs%4),
			RentalCount:      1 + int(rentals%3),
			MarketplaceCount: 1 + int(offers%3),
			ImageCount:       2,
			ArtifactCount:    1,
		})
		if err != nil {
			t.Fatalf("generate Blueprint: %v", err)
		}
		tape, samples, err := Compile(blueprint, CompileOptions{})
		if err != nil {
			t.Fatalf("compile generated Blueprint: %v", err)
		}
		execution, err := Open(context.Background(), Config{
			Blueprint:        blueprint,
			Tape:             tape,
			Samples:          samples,
			Limits:           testLimits(),
			Policy:           "policy:fuzz",
			MercatorRevision: "revision:test",
		})
		if err != nil {
			t.Fatalf("open generated execution: %v", err)
		}
		defer func() { _ = execution.Close() }()
		if _, err := execution.Drive(context.Background(), Quiesce()); err != nil {
			t.Fatalf("drive generated arrivals: %v", err)
		}
		if _, err := execution.Check(context.Background()); err != nil {
			t.Fatalf("generated invariant failure: %v", err)
		}
	})
}
