package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scenario"
	"github.com/benngarcia/mercator/internal/scheduler"
)

// TestSmallWorldReferenceSolverAgreesWithProductionOnEveryCandidate is the
// oracle law, and it is stated over every candidate rather than over the winner.
//
// Comparing feasible sets and winners only catches a disagreement large enough
// to move a placement, which in a world of two machines means a disagreement
// worth more than the gap between them. Every drift this corpus has found so far
// was smaller than that when it landed and larger later: two definitions of
// uncertainty agreed on every winner for a phase because both were multiplied by
// zero, and an Artifact read nobody priced changed no winner until a fixture put
// forty gigabytes on one side. A model that agrees candidate by candidate about
// each stage of the prediction, the dollars, the doubt, and what it recorded has
// nowhere left to hide one.
func TestSmallWorldReferenceSolverAgreesWithProductionOnEveryCandidate(t *testing.T) {
	input := smallSchedulingInput(t)
	// One machine publishes a risk history, so the agreement covers the answers a
	// decision records without scoring as well as the ones it scores. A term read
	// off an offer by one model and recorded by neither is how the two definitions
	// of uncertainty came apart.
	for index := range input.Offers {
		if input.Offers[index].ID == "fresh-4090" {
			input.Offers[index].Reliability = domain.ReliabilityEvidence{
				StartFailures: domain.StatedRate{Rate: 0.4, Confidence: 0.9},
				Interruptions: domain.StatedRate{Rate: 0.25, Confidence: 0.9},
			}
		}
	}
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
	for _, candidate := range production.Candidates {
		offer := offerFor(t, input, candidate.OfferSnapshotID)
		assertModelsAgreeAboutCandidate(t, candidate, referenceCandidate(input, offer))
	}
}

// assertModelsAgreeAboutCandidate holds the two models to the same account of one
// candidate: every stage of the prediction it was scored on, the dollars it
// costs, the doubt it carries, and the risk history the record states beside
// them. Each quantity is named, so a failure says which answer the models
// disagree about rather than that they disagree.
//
// The score is compared only where both models have something to rank. An
// infeasible candidate is not for sale, so production scores it nothing while
// this reference model prices only what it would use.
func assertModelsAgreeAboutCandidate(t *testing.T, production, reference domain.CandidateDecision) {
	t.Helper()
	for _, stage := range []struct {
		answer                string
		production, reference domain.Estimate
	}{
		{"queue_seconds", production.Estimates.QueueSeconds, reference.Estimates.QueueSeconds},
		{"start_seconds", production.Estimates.StartSeconds, reference.Estimates.StartSeconds},
		{"established_start_seconds", production.Estimates.EstablishedStartSeconds, reference.Estimates.EstablishedStartSeconds},
		{"cost_usd", production.Estimates.CostUSD, reference.Estimates.CostUSD},
	} {
		if !sameEstimate(stage.production, stage.reference) {
			t.Errorf("candidate %q: %s: production predicted %s, the reference model %s",
				production.OfferSnapshotID, stage.answer, describeEstimate(stage.production), describeEstimate(stage.reference))
		}
	}
	// Every stage of the launch, read through the one list that names them, so a
	// stage added to the record cannot be added without both models being held to
	// it.
	for _, stage := range domain.LaunchStages {
		predicted, referenced := production.Estimates.Stages.Stage(stage), reference.Estimates.Stages.Stage(stage)
		if !sameEstimate(predicted, referenced) {
			t.Errorf("candidate %q: %s: production predicted %s, the reference model %s",
				production.OfferSnapshotID, stage, describeEstimate(predicted), describeEstimate(referenced))
		}
	}
	if production.Priced() != reference.Priced() {
		t.Errorf("candidate %q: production says priced=%v and the reference model %v, over cost %+v and %+v",
			production.OfferSnapshotID, production.Priced(), reference.Priced(), production.Estimates.CostUSD, reference.Estimates.CostUSD)
	}
	if production.Reliability != reference.Reliability {
		t.Errorf("candidate %q: production recorded risk %+v, the reference model %+v",
			production.OfferSnapshotID, production.Reliability, reference.Reliability)
	}
	if production.Uncertainty() != reference.Uncertainty() {
		t.Errorf("candidate %q: production counted %v points of doubt over %+v, the reference model %v over %+v",
			production.OfferSnapshotID, production.Uncertainty(), production.Confidences,
			reference.Uncertainty(), reference.Confidences)
	}
	if production.Feasible && math.Abs(production.ScoreUSD-reference.ScoreUSD) > 1e-6 {
		t.Errorf("candidate %q: production scored %.6f USD, the reference model %.6f",
			production.OfferSnapshotID, production.ScoreUSD, reference.ScoreUSD)
	}
}

// sameEstimate compares what two models predicted, quantiles and confidence
// included. It reads no source or model version: those name who answered, and
// two independent models are meant to name themselves.
func sameEstimate(left, right domain.Estimate) bool {
	return math.Abs(left.Expected-right.Expected) < 1e-6 &&
		math.Abs(left.P50-right.P50) < 1e-6 &&
		math.Abs(left.P90-right.P90) < 1e-6 &&
		math.Abs(left.Confidence-right.Confidence) < 1e-6
}

func describeEstimate(estimate domain.Estimate) string {
	return fmt.Sprintf("expected %.4f, p50 %.4f, p90 %.4f, confidence %.4f",
		estimate.Expected, estimate.P50, estimate.P90, estimate.Confidence)
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
	if reference.Stages.Unpack.Expected != warm.Estimates.Stages.Unpack.Expected {
		t.Fatalf("reference priced %v seconds of assembly, production priced %v",
			reference.Stages.Unpack.Expected, warm.Estimates.Stages.Unpack.Expected)
	}
	if warm.Estimates.Stages.Unpack.Expected == 0 {
		t.Fatal("assembling 18GB was priced at nothing by both models, so neither is accounting for it")
	}
	// The other half of the same claim: this host owes assembly and no transfer, so
	// a model folding the two together would price the network for bytes that are
	// already on the disk.
	if warm.Estimates.Stages.ImageFetch.Expected != 0 {
		t.Fatalf("a host holding every byte was priced %v seconds of transfer",
			warm.Estimates.Stages.ImageFetch.Expected)
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
	if reference.Stages.ArtifactFetch.Expected != warm.Estimates.Stages.ArtifactFetch.Expected {
		t.Fatalf("reference priced %v seconds of Artifact fetch, production priced %v",
			reference.Stages.ArtifactFetch.Expected, warm.Estimates.Stages.ArtifactFetch.Expected)
	}
	if warm.Estimates.Stages.ArtifactFetch.Expected == 0 {
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

// TestBothModelsPriceDoubtTheSameWay is the two uncertainty definitions collapsed
// into one, held by the only thing that can hold it: an independent model scoring
// the same candidates and getting the same dollars.
//
// They disagreed for a phase and nothing could say so. The scheduler counted the
// capacity and reliability confidences a candidate was given; the reference model
// counted those plus a full point for an unenumerated image inventory and another
// for unknown pricing. Both were multiplied by zero in every Run either model ever
// scored, so the disagreement was invisible until a class declared a rate, at
// which point it would have moved the winner on every machine Mercator borrows a
// slot on.
//
// Every candidate here is a machine with something to be unsure about: one that
// cannot be asked what it holds, one whose publisher is 70 percent sure of its
// capacity, and one that answered and owes a transfer over a link nothing has
// measured. The Run is interactive, so a point of doubt is 0.60 USD and a term
// deleted from either model shows up in the dollars.
func TestBothModelsPriceDoubtTheSameWay(t *testing.T) {
	input := smallSchedulingInput(t)
	input.Workload.Spec.Placement.Class = domain.ClassInteractive
	borrowed := offerFor(t, input, "rental-warm")
	borrowed.ID, borrowed.RentalID, borrowed.NativeRef = "borrowed-host", "", "borrowed-host"
	borrowed.Kind, borrowed.Lane = domain.OfferKindStanding, domain.LaneEphemeral
	// Nothing of Mercator's runs there, so nothing enumerates it.
	borrowed.Images = domain.ImageInventory{}
	doubted := offerFor(t, input, "rental-warm")
	doubted.ID, doubted.RentalID, doubted.NativeRef = "doubted-rental", "doubted-rental", "doubted-rental"
	doubted.Capacity.Confidence = 0.7
	input.Offers = append(input.Offers, borrowed, doubted)

	production, err := scheduler.New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate production scheduler: %v", err)
	}

	doubtful := 0
	for _, candidate := range production.Candidates {
		reference := referenceCandidate(input, offerFor(t, input, candidate.OfferSnapshotID))
		if candidate.Uncertainty() > 0 {
			doubtful++
		}
		if candidate.Uncertainty() != reference.Uncertainty() {
			t.Errorf("candidate %q: production counted %v points of doubt over %+v, the reference model %v over %+v",
				candidate.OfferSnapshotID, candidate.Uncertainty(), candidate.Confidences,
				reference.Uncertainty(), reference.Confidences)
		}
		if math.Abs(candidate.ScoreUSD-reference.ScoreUSD) > 1e-6 {
			t.Errorf("candidate %q: production scored %.6f USD, the reference model %.6f",
				candidate.OfferSnapshotID, candidate.ScoreUSD, reference.ScoreUSD)
		}
	}
	if doubtful != len(production.Candidates) {
		t.Fatalf("only %d of %d candidates carry any doubt, so agreeing about it proves less than it should",
			doubtful, len(production.Candidates))
	}
	borrowedCandidate := candidateFor(t, production, "borrowed-host")
	if borrowedCandidate.Uncertainty() != domain.AssumedLinkConfidence {
		t.Fatalf("the machine nobody can ask carries %v points of doubt, and the only unsure answer it has is a transfer over an unmeasured link: %+v",
			borrowedCandidate.Uncertainty(), borrowedCandidate.Confidences)
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

			if owes := candidate.Estimates.Stages.ArtifactFetch.Expected > 0; owes != copyOnDisk.owes {
				t.Errorf("production priced %s at %v seconds, and owing a fetch should be %v",
					copyOnDisk.name, candidate.Estimates.Stages.ArtifactFetch.Expected, copyOnDisk.owes)
			}
			if reference.Stages.ArtifactFetch.Expected != candidate.Estimates.Stages.ArtifactFetch.Expected {
				t.Errorf("reference priced %v seconds, production priced %v",
					reference.Stages.ArtifactFetch.Expected, candidate.Estimates.Stages.ArtifactFetch.Expected)
			}
		})
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
	if candidate.Estimates.Stages.ArtifactFetch.Expected == 0 {
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
	// A Run always states a class by the time Placement sees one: intake fills the
	// omission and refuses a word it cannot price. The small world states standard,
	// which prices a second of waiting at what the machine doing the waiting costs.
	workload := scenario.WorkloadForRun(labWorkspace, "run-reference", request)
	workload.Spec.Placement.Class = domain.ClassStandard
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
	// The provider states its own quantiles, tail included. A model that scaled a
	// spread off the expectation instead would enforce a Run's start bound against
	// a number Mercator made up while the provider's own answer sat unread on the
	// offer, and with the expectation alone stated here neither model could be
	// caught doing it.
	fresh.Provisioning = &domain.Estimate{Expected: 240, P50: 210, P90: 480}
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

// TestBothModelsRefuseToPriceAMachineNobodyQuoted is the one term of the score
// that had no answer at all. A machine whose price nobody published was scored as
// costing zero dollars, and both models did it, so they agreed about a candidate
// they were both wrong about: a Run allowing unknown pricing took the unquoted
// machine over one somebody quoted, every time, because nothing is cheaper than
// nothing.
//
// The absence is now stated in the record as the source of the cost estimate, and
// both models state it, which is what lets the ranking read it. The reference
// model used to charge a full point of doubt for unknown pricing instead. That
// point was deleted with the inventory point beside it, and only the inventory one
// was double counted; the answer here is not more doubt about a number, it is that
// there is no number.
func TestBothModelsRefuseToPriceAMachineNobodyQuoted(t *testing.T) {
	input := smallSchedulingInput(t)
	input.Workload.Spec.Placement.AllowUnknownPricing = true
	unquoted := offerFor(t, input, "rental-warm")
	unquoted.ID, unquoted.RentalID, unquoted.NativeRef = "rental-unquoted", "rental-unquoted", "rental-unquoted"
	unquoted.Pricing = domain.PriceModel{Currency: "USD"}
	unquoted.Capabilities.Pricing = domain.PricingCapabilities{}
	input.Offers = append(input.Offers, unquoted)

	production, err := scheduler.New().Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("evaluate production scheduler: %v", err)
	}
	reference, err := SolveSmallWorld(input)
	if err != nil {
		t.Fatalf("solve the small world: %v", err)
	}

	if production.SelectedOfferSnapshotID != reference.SelectedOfferID {
		t.Fatalf("production placed on %q and the reference model on %q", production.SelectedOfferSnapshotID, reference.SelectedOfferID)
	}
	if production.SelectedOfferSnapshotID == "rental-unquoted" {
		t.Fatalf("both models chose the machine nobody priced over one somebody did")
	}
	candidate := candidateFor(t, production, "rental-unquoted")
	if candidate.Priced() {
		t.Errorf("the unquoted candidate records cost %+v, which reads as a price somebody stated", candidate.Estimates.CostUSD)
	}
	if referenceCandidate(input, unquoted).Priced() {
		t.Errorf("the reference model prices the unquoted machine at %+v", referenceCandidate(input, unquoted).Estimates.CostUSD)
	}
}
