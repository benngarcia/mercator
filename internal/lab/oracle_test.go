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

// TestTheReferenceModelPricesArtifactLocalityTheSameWayProductionDoes keeps the
// two models together on the second kind of content a candidate can be warm for.
// Reading a Run's inputs out of the object store is transfer this candidate owes
// before it can work, exactly as an image pull is, so a reference model blind to
// it would call every host equal on a 40GB dataset and disagree with the
// scheduler about which machine to use for a reason belonging to neither model.
func TestTheReferenceModelPricesArtifactLocalityTheSameWayProductionDoes(t *testing.T) {
	input := smallSchedulingInput(t)
	input.Artifacts = []domain.ArtifactVersion{labArtifactVersion(input.EvaluatedAt)}
	for index := range input.Offers {
		if input.Offers[index].ID != "rental-warm" {
			continue
		}
		// This host enumerated its copies and holds none of the dataset.
		input.Offers[index].Artifacts = domain.ArtifactInventory{Known: true, ObservedAt: input.EvaluatedAt}
	}

	production, err := scheduler.New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate production scheduler: %v", err)
	}
	warm := candidateFor(t, production, "rental-warm")
	reference := referenceEstimates(input, offerFor(t, input, "rental-warm"))

	if len(warm.ArtifactEvidence) != 1 || warm.ArtifactEvidence[0].Locality != domain.LocalityCold {
		t.Fatalf("the decision recorded %+v, and this host holds no copy of the dataset", warm.ArtifactEvidence)
	}
	if reference.ArtifactSeconds.Expected != warm.Estimates.ArtifactSeconds.Expected {
		t.Fatalf("reference priced %v seconds of Artifact fetch, production priced %v",
			reference.ArtifactSeconds.Expected, warm.Estimates.ArtifactSeconds.Expected)
	}
	if warm.Estimates.ArtifactSeconds.Expected == 0 {
		t.Fatal("reading 40GB out of the object store was priced at nothing by both models, so neither is accounting for it")
	}
}

// TestBothModelsRefuseAHostTheContentDoesNotFitOn keeps the two models together
// on the disk. A machine holding 18GB of the image with ten gigabytes free has
// nowhere to put a 40GB dataset, and nothing it could delete helps: every byte
// it gave up is a byte this Run needs back. So it is refused rather than priced,
// and a reference model blind to that would call it the warmest candidate in the
// world and disagree with production about the winner for a reason belonging to
// neither model.
func TestBothModelsRefuseAHostTheContentDoesNotFitOn(t *testing.T) {
	input := smallSchedulingInput(t)
	input.Artifacts = []domain.ArtifactVersion{labArtifactVersion(input.EvaluatedAt)}
	for index := range input.Offers {
		if input.Offers[index].ID != "rental-warm" {
			continue
		}
		input.Offers[index].Artifacts = domain.ArtifactInventory{Known: true, ObservedAt: input.EvaluatedAt}
		input.Offers[index].Resources.EphemeralDiskBytes = 10_000_000_000
	}

	production, err := scheduler.New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate production scheduler: %v", err)
	}
	warm := candidateFor(t, production, "rental-warm")

	if warm.Feasible {
		t.Fatalf("a machine with 10GB free took a Run reading a 40GB dataset: %+v", warm.Disk)
	}
	if refusal := warm.Rejections[0]; refusal.Code != "RESOURCE_INSUFFICIENT" || refusal.Path != "resources.ephemeral_disk" {
		t.Fatalf("the machine was refused with %+v, and what it has no room for is content on its disk", refusal)
	}
	if referenceFeasible(input, offerFor(t, input, "rental-warm")) {
		t.Fatal("the reference model would have run 40GB of content on a machine with ten gigabytes free")
	}
	// The machine with room takes it, because nothing about a full neighbour
	// changes what a candidate with space is worth.
	if roomy := candidateFor(t, production, "fresh-4090"); !roomy.Feasible {
		t.Fatalf("a machine with room was refused beside the one without: %+v", roomy.Rejections)
	}
}

// TestNeitherModelRefusesAMachineForContentNobodyCouldDescribe is the other half
// of the same rule, and the architectural one. A host that could not enumerate
// itself is charged the whole content in seconds and never turned away for it:
// the bytes nobody could describe may already be on its disk, and refusing a
// machine for a silence is exactly what unknown locality must never become.
func TestNeitherModelRefusesAMachineForContentNobodyCouldDescribe(t *testing.T) {
	input := smallSchedulingInput(t)
	input.Artifacts = []domain.ArtifactVersion{labArtifactVersion(input.EvaluatedAt)}
	for index := range input.Offers {
		if input.Offers[index].ID != "rental-warm" {
			continue
		}
		input.Offers[index].Images = domain.ImageInventory{}
		input.Offers[index].Artifacts = domain.ArtifactInventory{}
		input.Offers[index].Resources.EphemeralDiskBytes = 10_000_000_000
	}

	production, err := scheduler.New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate production scheduler: %v", err)
	}
	silent := candidateFor(t, production, "rental-warm")

	if !silent.Feasible {
		t.Fatalf("a machine nobody could enumerate was refused for room: %+v", silent.Rejections)
	}
	if !referenceFeasible(input, offerFor(t, input, "rental-warm")) {
		t.Fatal("the reference model refused a machine for content nobody established it was missing")
	}
	if silent.Disk.LandBytes <= silent.Disk.EstablishedLandBytes {
		t.Fatalf("the decision records %+v, and none of that content was established", silent.Disk)
	}
}

// TestNeitherModelPricesAnUncheckedCopyAsWarmth is what verification buys at
// placement time. A host sitting on bytes nobody hashed is not a host that saves
// this Run a read: the copy may be anything, so the Run still owes the whole
// fetch and both models have to say so. Scoring presence instead is the
// distributed-filesystem answer arriving through the back door of an estimate.
func TestNeitherModelPricesAnUncheckedCopyAsWarmth(t *testing.T) {
	for _, copyOnDisk := range []struct {
		name  string
		state domain.ArtifactReplicaState
		owes  bool
	}{
		{"a copy checked against the catalog", domain.ArtifactReplicaVerified, false},
		{"a copy nobody checked", domain.ArtifactReplicaUnverified, true},
	} {
		t.Run(copyOnDisk.name, func(t *testing.T) {
			input := smallSchedulingInput(t)
			version := labArtifactVersion(input.EvaluatedAt)
			input.Artifacts = []domain.ArtifactVersion{version}
			holder := offerFor(t, input, "rental-warm")
			holder.Artifacts = domain.ArtifactInventory{
				Known:      true,
				ObservedAt: input.EvaluatedAt,
				Replicas: []domain.ArtifactReplica{{
					ArtifactID:    version.ID,
					ContentDigest: version.ContentDigest,
					SizeBytes:     version.SizeBytes,
					State:         copyOnDisk.state,
				}},
			}
			input.Offers = []domain.OfferSnapshot{holder}

			production, err := scheduler.New().Evaluate(context.Background(), input)
			if err != nil {
				t.Fatalf("evaluate production scheduler: %v", err)
			}
			candidate := candidateFor(t, production, "rental-warm")
			reference := referenceEstimates(input, holder)

			if owes := candidate.Estimates.ArtifactSeconds.Expected > 0; owes != copyOnDisk.owes {
				t.Errorf("production priced %s at %v seconds, and owing a fetch should be %v",
					copyOnDisk.name, candidate.Estimates.ArtifactSeconds.Expected, copyOnDisk.owes)
			}
			if reference.ArtifactSeconds.Expected != candidate.Estimates.ArtifactSeconds.Expected {
				t.Errorf("reference priced %v seconds, production priced %v",
					reference.ArtifactSeconds.Expected, candidate.Estimates.ArtifactSeconds.Expected)
			}
		})
	}
}

// TestBothModelsPriceUncertaintyFromTheSameFacts closes a divergence the dead
// weight was hiding. The reference model counted an offer nobody could enumerate
// and an offer with no price as uncertainty and the scheduler did not, and the
// two agreed on the score only because ScoreWeights.UncertaintyPenaltyUSD
// multiplies the term by zero in every deployment. Phase 4 populates those
// weights, and a disagreement waiting for that is a disagreement about which
// machine to use.
func TestBothModelsPriceUncertaintyFromTheSameFacts(t *testing.T) {
	input := smallSchedulingInput(t)
	input.Weights = scheduler.ScoreWeights{UncertaintyPenaltyUSD: 1}
	// Every kind of not-knowing at once: a capacity claim published at partial
	// confidence, reliability likewise, no inventory at all, and no price.
	silent := offerFor(t, input, "rental-warm")
	silent.Capacity = domain.CapacityEvidence{Available: true, Confidence: 0.4}
	silent.Reliability = domain.ReliabilityEvidence{Confidence: 0.5}
	silent.Images = domain.ImageInventory{}
	silent.Pricing.Known = false
	input.Workload.Spec.Placement.AllowUnknownPricing = true
	input.Offers = []domain.OfferSnapshot{silent}

	production, err := scheduler.New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate production scheduler: %v", err)
	}
	candidate := candidateFor(t, production, silent.ID)

	if penalty := silent.UncertaintyPenalty(); penalty != 3.1 {
		t.Fatalf("this offer is worth %v of uncertainty, and the case needs every term to fire", penalty)
	}
	if got := referenceScore(input, silent); got != candidate.ScoreUSD {
		t.Fatalf("reference scored %v and production scored %v, so the two models disagree about what nobody knows", got, candidate.ScoreUSD)
	}
}

// TestNeitherModelTurnsArtifactSilenceIntoInfeasibility is the Artifact half of
// the rule below, at the one place a locality answer can strike a candidate out.
// A machine that cannot enumerate its copies is charged the whole read, which is
// what stops it outranking a host provably holding one, and charged is as far as
// it may go: it may well be sitting on every byte. Both models have to agree,
// because a reference model that struck the silent candidate out would make the
// production scheduler's refusal to do so look like a bug.
func TestNeitherModelTurnsArtifactSilenceIntoInfeasibility(t *testing.T) {
	input := smallSchedulingInput(t)
	input.Workload.Spec.Placement.MaxP90StartSeconds = 180
	input.Artifacts = []domain.ArtifactVersion{labArtifactVersion(input.EvaluatedAt)}
	// This host says exactly what image content it holds and nothing at all
	// about its copies, so the only silence under test is the Artifact's.
	silent := offerFor(t, input, "rental-warm")
	silent.Images = domain.ImageInventory{Known: true, ObservedAt: input.EvaluatedAt, ImageDigests: []string{input.Image.Digest}}
	silent.Artifacts = domain.ArtifactInventory{}
	input.Offers = []domain.OfferSnapshot{silent}

	production, err := scheduler.New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate production scheduler: %v", err)
	}
	reference, err := SolveSmallWorld(input)
	if err != nil {
		t.Fatalf("solve reference world: %v", err)
	}

	candidate := candidateFor(t, production, "rental-warm")
	if candidate.Estimates.StartSeconds.P90 <= input.Workload.Spec.Placement.MaxP90StartSeconds {
		t.Fatalf("this machine was predicted to start in %.2fs, which is inside the bound, so the case proves nothing",
			candidate.Estimates.StartSeconds.P90)
	}
	if candidate.Estimates.ArtifactSeconds.Expected == 0 {
		t.Fatal("a machine that cannot say which copies it holds was priced nothing to read them")
	}
	if !candidate.Feasible {
		t.Errorf("production refused a machine that cannot enumerate its copies: %+v", candidate.Rejections)
	}
	if !slices.Contains(reference.FeasibleOfferIDs, "rental-warm") {
		t.Error("the reference model refused a machine that cannot enumerate its copies")
	}
}

// labArtifactVersion is one 40GB input, durable before the Run is placed. The
// size is what makes these cases about locality rather than about arithmetic: it
// dwarfs every image in the small world, so a model that ignores it picks a
// different machine.
func labArtifactVersion(publishedAt time.Time) domain.ArtifactVersion {
	const id = "artifact:dataset:v1"
	return domain.ArtifactVersion{
		ID:            id,
		WorkspaceID:   labWorkspace,
		ContentDigest: "sha256:da7a5e7da7a5e7da7a5e7da7a5e7da7a5e7da7a5e7da7a5e7da7a5e7da7a5e700",
		SizeBytes:     40_000_000_000,
		Location:      domain.ArtifactLocation(labWorkspace, id),
		PublishedAt:   publishedAt,
	}
}

// TestNeitherModelTurnsSilenceIntoInfeasibility is the rule at the one place an
// image locality answer can strike a candidate out. A Run that refuses to wait
// gets to refuse a machine that was found to be slow; a machine nobody could
// ask has not been found to be anything. Pricing its silence as the whole image
// is what stops it outranking a host that is provably ready, and pricing is as
// far as it may go: the goal is explicit that unknown locality is uncertainty
// and never a hard constraint. Both models have to say so, because a reference
// model that struck out the silent candidate would make the production
// scheduler's refusal to look like a bug.
func TestNeitherModelTurnsSilenceIntoInfeasibility(t *testing.T) {
	for _, machine := range []struct {
		name      string
		inventory domain.ImageInventory
		feasible  bool
	}{
		{"a Rental that enumerated itself and holds none of the image", domain.ImageInventory{Known: true}, false},
		{"a machine nothing of Mercator's runs on", domain.ImageInventory{}, true},
	} {
		t.Run(machine.name, func(t *testing.T) {
			input := smallSchedulingInput(t)
			input.Workload.Spec.Placement.MaxP90StartSeconds = 180
			silent := offerFor(t, input, "rental-warm")
			silent.Images = machine.inventory
			silent.Images.ObservedAt = input.EvaluatedAt
			input.Offers = []domain.OfferSnapshot{silent}

			production, err := scheduler.New().Evaluate(context.Background(), input)
			if err != nil {
				t.Fatalf("evaluate production scheduler: %v", err)
			}
			reference, err := SolveSmallWorld(input)
			if err != nil {
				t.Fatalf("solve reference world: %v", err)
			}

			candidate := candidateFor(t, production, "rental-warm")
			if candidate.Estimates.StartSeconds.P90 <= input.Workload.Spec.Placement.MaxP90StartSeconds {
				t.Fatalf("this machine was predicted to start in %.2fs, which is inside the bound, so the case proves nothing",
					candidate.Estimates.StartSeconds.P90)
			}
			if candidate.Feasible != machine.feasible {
				t.Errorf("production called %s feasible=%v, want %v", machine.name, candidate.Feasible, machine.feasible)
			}
			if got := slices.Contains(reference.FeasibleOfferIDs, "rental-warm"); got != machine.feasible {
				t.Errorf("the reference model called %s feasible=%v, want %v", machine.name, got, machine.feasible)
			}
		})
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
	// A machine that does not exist yet has nothing on it to enumerate, so it
	// says nothing rather than claiming it looked and found nothing.
	fresh.Images = domain.ImageInventory{}
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
