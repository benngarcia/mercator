package lab

import (
	"context"
	"encoding/json"
	"slices"
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

// TestTheReferenceModelPricesAssemblyTheSameWayProductionDoes keeps the two
// models from drifting where they now have a second kind of work to account
// for. A host that fetched an image and never assembled it owes local work and
// no transfer, and a reference model that folded the two together would disagree
// with the scheduler about every half-built host for a reason that has nothing
// to do with either model, which is exactly what an independent oracle exists
// to rule out.
func TestTheReferenceModelPricesAssemblyTheSameWayProductionDoes(t *testing.T) {
	input := smallSchedulingInput(t)
	for index := range input.Offers {
		if input.Offers[index].ID != "rental-warm" {
			continue
		}
		// Every byte is here and none of it is ready to mount.
		input.Offers[index].Images = domain.ImageInventory{
			Known:              true,
			ObservedAt:         input.EvaluatedAt,
			PulledImageDigests: []string{input.Image.Digest},
		}
	}

	production, err := scheduler.New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate production scheduler: %v", err)
	}
	warm := candidateFor(t, production, "rental-warm")
	reference := referenceEstimates(input, offerFor(t, input, "rental-warm"))

	if warm.ImageLocality != domain.LocalityPartial {
		t.Fatalf("image locality = %q, want partial: the bytes are here and the chain is not", warm.ImageLocality)
	}
	if reference.PullSeconds.Expected != warm.Estimates.PullSeconds.Expected {
		t.Fatalf("reference priced %v seconds of image work, production priced %v",
			reference.PullSeconds.Expected, warm.Estimates.PullSeconds.Expected)
	}
	if warm.Estimates.PullSeconds.Expected == 0 {
		t.Fatal("assembling 18GB was priced at nothing by both models, so neither is accounting for it")
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
		before := heldEverySpace(input.Offers[0])
		after := before
		// Warming is a host acquiring content, so the inventory grows, in
		// whichever space the host names that content in.
		after.Images.LayerDigests = append(slices.Clone(before.Images.LayerDigests), topLayerDigest)
		after.Images.LayerDiffIDs = append(slices.Clone(before.Images.LayerDiffIDs), topLayerDiffID)
		if err := CheckWarmingDoesNotShrinkInventory(before, after); err != nil {
			t.Fatal(err)
		}
	})

	// A law nothing can break is not a law. Each clause is shown failing on the
	// one transform it exists to catch. The diff-ID clause matters most: a
	// Docker daemon names its layers that way and no other, so a host losing a
	// diff ID has lost content exactly as a host losing a blob digest has.
	t.Run("warming that loses content", func(t *testing.T) {
		before := heldEverySpace(input.Offers[0])
		for name, shrunk := range map[string]domain.ImageInventory{
			// Each transform loses exactly one thing, so exactly one clause can
			// catch it. A host that stopped enumerating still lists what it
			// listed before: an empty inventory would be caught by the layer
			// clauses first and leave the Known clause driven by nothing, which
			// is how a law acquires a term no test can break.
			"stopped enumerating its content": {
				Known:        false,
				LayerDigests: before.Images.LayerDigests,
				LayerDiffIDs: before.Images.LayerDiffIDs,
				ImageDigests: before.Images.ImageDigests,
			},
			"lost a blob digest it held":  {Known: true, LayerDiffIDs: before.Images.LayerDiffIDs, ImageDigests: before.Images.ImageDigests},
			"lost a diff ID it held":      {Known: true, LayerDigests: before.Images.LayerDigests, ImageDigests: before.Images.ImageDigests},
			"lost an image it held whole": {Known: true, LayerDigests: before.Images.LayerDigests, LayerDiffIDs: before.Images.LayerDiffIDs},
		} {
			after := before
			after.Images = shrunk
			if err := CheckWarmingDoesNotShrinkInventory(before, after); err == nil {
				t.Errorf("a host that %s was reported as lawfully warmed", name)
			}
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
	warm := labOffer("rental-warm", domain.OfferKindStanding, domain.LaneReusable, 2.5, request.Resources)
	warm.ObservedAt = now
	warm.ExpiresAt = now.Add(time.Minute)
	// The warm host holds the 18GB base layer and not the 80MB top layer.
	warm.Images = domain.ImageInventory{Known: true, ObservedAt: now, LayerDigests: []string{baseLayerDigest}}
	fresh := labOffer("fresh-4090", domain.OfferKindProvisionable, domain.LaneReusable, 4, request.Resources)
	fresh.ObservedAt = now
	fresh.ExpiresAt = now.Add(time.Minute)
	// A machine that does not exist yet holds nothing, and says so.
	fresh.Images = domain.ImageInventory{Known: true, ObservedAt: now}
	fresh.Provisioning = &domain.Estimate{Expected: 240}
	return scheduler.SchedulingInput{
		RunID:    "run-reference",
		Workload: workload,
		Image: domain.ImageManifest{
			Known:  true,
			Digest: request.Image,
			Layers: []domain.ImageLayer{
				{Digest: baseLayerDigest, CompressedBytes: 18_000_000_000},
				{Digest: topLayerDigest, CompressedBytes: 80_000_000},
			},
		},
		Offers:       []domain.OfferSnapshot{warm, fresh},
		Schedules:    map[string]domain.RentalSchedule{},
		ModelVersion: "latency-v1",
		EvaluatedAt:  now,
	}
}

func candidateFor(t *testing.T, decision domain.BookingDecision, offerID string) domain.CandidateDecision {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID == offerID {
			return candidate
		}
	}
	t.Fatalf("the decision records no candidate for %q", offerID)
	return domain.CandidateDecision{}
}

func offerFor(t *testing.T, input scheduler.SchedulingInput, offerID string) domain.OfferSnapshot {
	t.Helper()
	for _, offer := range input.Offers {
		if offer.ID == offerID {
			return offer
		}
	}
	t.Fatalf("the input carries no offer %q", offerID)
	return domain.OfferSnapshot{}
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

// The reference world's two layers: a large shared base and a small top layer,
// which is what makes a warm host cheap and a cold one expensive. Each is named
// in both spaces, because a host reports whichever one its runtime can say.
const (
	baseLayerDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	topLayerDigest  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	baseLayerDiffID = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	topLayerDiffID  = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	warmImageDigest = "sha256:5555555555555555555555555555555555555555555555555555555555555555"
)

// heldEverySpace is a host that has something to lose in each of the three
// vocabularies an inventory speaks, which is what a law about losing content
// has to be checked against.
func heldEverySpace(offer domain.OfferSnapshot) domain.OfferSnapshot {
	offer.Images.LayerDigests = []string{baseLayerDigest}
	offer.Images.LayerDiffIDs = []string{baseLayerDiffID}
	offer.Images.ImageDigests = []string{warmImageDigest}
	return offer
}
