package lab

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scenario"
	"github.com/benngarcia/mercator/internal/scheduler"
)

func TestSmallWorldReferenceSolverAgreesWithProductionFeasibilityAndWinner(t *testing.T) {
	input := smallSchedulingInput(t)
	production, err := scheduler.New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate production scheduler: %v", err)
	}
	reference, err := SolveSmallWorld(input)
	if err != nil {
		t.Fatalf("solve reference world: %v", err)
	}

	if reference.SelectedOfferID != production.SelectedOfferSnapshotID {
		t.Fatalf("reference winner = %q, production winner = %q", reference.SelectedOfferID, production.SelectedOfferSnapshotID)
	}
	var productionFeasible []string
	for _, candidate := range production.Candidates {
		if candidate.Feasible {
			productionFeasible = append(productionFeasible, candidate.OfferSnapshotID)
		}
	}
	if !equalStrings(reference.FeasibleOfferIDs, productionFeasible) {
		t.Fatalf("reference feasible = %v, production feasible = %v", reference.FeasibleOfferIDs, productionFeasible)
	}
}

func TestSchedulingMetamorphisms(t *testing.T) {
	input := smallSchedulingInput(t)
	production := scheduler.New()

	t.Run("offer order", func(t *testing.T) {
		if err := CheckOfferOrderIndependence(context.Background(), production, input); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("dominated candidate", func(t *testing.T) {
		dominated := input.Offers[1]
		dominated.ID = "fresh-dominated"
		dominated.NativeRef = dominated.ID
		dominated.Pricing.RatePerSecondUSD *= 10
		dominated.Provisioning = &domain.Estimate{Expected: time.Hour.Seconds()}
		if err := CheckDominatedOfferDoesNotChangeWinner(context.Background(), production, input, dominated); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("warming", func(t *testing.T) {
		before := input.Offers[0]
		after := before
		after.ImageCache.MissingBytes /= 2
		if err := CheckWarmingDoesNotIncreaseMissingBytes(before, after); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("bandwidth", func(t *testing.T) {
		if err := CheckReducedBandwidthDoesNotReduceTransferDuration(1_000_000_000, 500, 100); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("duplicate delivery", func(t *testing.T) {
		effect := EffectRecord{
			Operation:   OperationProviderLaunch,
			OperationID: "launch-1",
			Command:     EffectCommandAccepted,
			Consequence: json.RawMessage(`{"external_id":"external-1"}`),
		}
		duplicate := effect
		duplicate.Command = EffectCommandDuplicate
		if err := CheckDuplicateMessagesDoNotDuplicateEffects(
			[]EffectRecord{effect},
			[]EffectRecord{effect, duplicate},
		); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRestartAndProjectionRebuildMetamorphisms(t *testing.T) {
	blueprint, tape, samples := demoInputs(t)
	config := Config{
		Blueprint:        blueprint,
		Tape:             tape,
		Samples:          samples,
		Limits:           testLimits(),
		Policy:           "policy:test",
		MercatorRevision: "revision:test",
	}

	if err := CheckRestartPreservesTerminalBehavior(context.Background(), config, 1); err != nil {
		t.Fatalf("restart metamorphism: %v", err)
	}

	execution, err := Open(context.Background(), config)
	if err != nil {
		t.Fatalf("open execution: %v", err)
	}
	defer func() {
		if err := execution.Close(); err != nil {
			t.Fatalf("close execution: %v", err)
		}
	}()
	if _, err := execution.Drive(context.Background(), Quiesce()); err != nil {
		t.Fatalf("drive arrivals: %v", err)
	}
	if err := CheckProjectionRebuildEquivalence(context.Background(), execution); err != nil {
		t.Fatalf("projection rebuild metamorphism: %v", err)
	}
}

func smallSchedulingInput(t *testing.T) scheduler.SchedulingInput {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint("../scenario/scenarios/demos/artifact-warmth-restart.json")
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	request := blueprint.Arrivals.Runs[0].Request
	workload := scenario.WorkloadForRun(labWorkspace, "run-reference", request)
	now := blueprint.World.Start()
	warm := labOffer("rental-warm", domain.OfferKindStanding, 2.5, request.Resources)
	warm.ObservedAt = now
	warm.ExpiresAt = now.Add(time.Minute)
	warm.ImageCache = domain.ImageCacheEvidence{Known: true, MissingBytes: 80_000_000}
	fresh := labOffer("fresh-4090", domain.OfferKindProvisionable, 4, request.Resources)
	fresh.ObservedAt = now
	fresh.ExpiresAt = now.Add(time.Minute)
	fresh.ImageCache = domain.ImageCacheEvidence{Known: true, MissingBytes: 18_080_000_000}
	fresh.Provisioning = &domain.Estimate{Expected: 240}
	return scheduler.SchedulingInput{
		RunID:        "run-reference",
		Workload:     workload,
		Offers:       []domain.OfferSnapshot{warm, fresh},
		Schedules:    map[string]domain.RentalSchedule{},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
