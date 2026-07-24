package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

type FailurePredicate func(context.Context, Blueprint) (bool, error)

type ShrinkStep struct {
	Dimension string `json:"dimension"`
	Removed   string `json:"removed"`
}

type ShrinkResult struct {
	Blueprint Blueprint    `json:"blueprint"`
	Steps     []ShrinkStep `json:"steps"`
}

func ShrinkBlueprint(ctx context.Context, original Blueprint, fails FailurePredicate) (ShrinkResult, error) {
	if fails == nil {
		return ShrinkResult{}, fmt.Errorf("Blueprint shrinker needs a failure predicate")
	}
	current, err := cloneBlueprintForShrink(original)
	if err != nil {
		return ShrinkResult{}, err
	}
	if err := current.validate(); err != nil {
		return ShrinkResult{}, fmt.Errorf("cannot shrink invalid Blueprint: %w", err)
	}
	failing, err := fails(ctx, current)
	if err != nil {
		return ShrinkResult{}, err
	}
	if !failing {
		return ShrinkResult{}, fmt.Errorf("cannot shrink a Blueprint that does not reproduce the failure")
	}

	var steps []ShrinkStep
	for {
		changed := false
		for _, candidate := range shrinkCandidates(current) {
			if err := ctx.Err(); err != nil {
				return ShrinkResult{}, err
			}
			if err := candidate.blueprint.validate(); err != nil {
				continue
			}
			failing, err := fails(ctx, candidate.blueprint)
			if err != nil {
				return ShrinkResult{}, err
			}
			if !failing {
				continue
			}
			current = candidate.blueprint
			steps = append(steps, candidate.step)
			changed = true
			break
		}
		if !changed {
			return ShrinkResult{Blueprint: current, Steps: steps}, nil
		}
	}
}

type shrinkCandidate struct {
	blueprint Blueprint
	step      ShrinkStep
}

func shrinkCandidates(blueprint Blueprint) []shrinkCandidate {
	var candidates []shrinkCandidate
	candidates = append(candidates, shrinkTimeline(blueprint)...)
	candidates = append(candidates, shrinkFaults(blueprint)...)
	candidates = append(candidates, shrinkRuns(blueprint)...)
	candidates = append(candidates, shrinkRentals(blueprint)...)
	candidates = append(candidates, shrinkMarketplace(blueprint)...)
	candidates = append(candidates, shrinkImageLayers(blueprint)...)
	candidates = append(candidates, shrinkArtifacts(blueprint)...)
	candidates = append(candidates, shrinkOptionalFields(blueprint)...)
	return candidates
}

func shrinkTimeline(blueprint Blueprint) []shrinkCandidate {
	var candidates []shrinkCandidate
	for index := range blueprint.Timeline {
		candidate := mustCloneBlueprintForShrink(blueprint)
		removed := candidate.Timeline[index]
		candidate.Timeline = slices.Delete(candidate.Timeline, index, index+1)
		candidates = append(candidates, shrinkCandidate{
			blueprint: candidate,
			step: ShrinkStep{
				Dimension: "timeline",
				Removed:   fmt.Sprintf("step-%d:%s%s", index+1, removed.Submit, removed.Reconcile),
			},
		})
	}
	return candidates
}

func shrinkRuns(blueprint Blueprint) []shrinkCandidate {
	if blueprint.Arrivals == nil {
		return nil
	}
	var candidates []shrinkCandidate
	switch blueprint.Arrivals.Type {
	case ArrivalFixed:
		if len(blueprint.Arrivals.Runs) <= 1 {
			return nil
		}
		for index, run := range blueprint.Arrivals.Runs {
			candidate := mustCloneBlueprintForShrink(blueprint)
			candidate.Arrivals.Runs = slices.Delete(candidate.Arrivals.Runs, index, index+1)
			removeRunReferences(&candidate, run.Name)
			candidates = append(candidates, shrinkCandidate{
				blueprint: candidate,
				step:      ShrinkStep{Dimension: "runs", Removed: run.Name},
			})
		}
	case ArrivalPeriodic:
		if blueprint.Arrivals.Periodic.Count > 1 {
			candidate := mustCloneBlueprintForShrink(blueprint)
			candidate.Arrivals.Periodic.Count--
			removed := fmt.Sprintf("%s-%03d", candidate.Arrivals.Periodic.NamePrefix, candidate.Arrivals.Periodic.Count+1)
			removeRunReferences(&candidate, removed)
			candidates = append(candidates, shrinkCandidate{
				blueprint: candidate,
				step:      ShrinkStep{Dimension: "runs", Removed: removed},
			})
		}
	case ArrivalBurst:
		if blueprint.Arrivals.Burst.Count > 1 {
			candidate := mustCloneBlueprintForShrink(blueprint)
			candidate.Arrivals.Burst.Count--
			removed := fmt.Sprintf("%s-%03d", candidate.Arrivals.Burst.NamePrefix, candidate.Arrivals.Burst.Count+1)
			removeRunReferences(&candidate, removed)
			candidates = append(candidates, shrinkCandidate{
				blueprint: candidate,
				step:      ShrinkStep{Dimension: "runs", Removed: removed},
			})
		}
	}
	return candidates
}

func removeRunReferences(blueprint *Blueprint, runName string) {
	blueprint.Faults = slices.DeleteFunc(blueprint.Faults, func(fault FaultSpec) bool {
		return fault.Trigger.Run == runName
	})
	blueprint.World.RuntimeModels = slices.DeleteFunc(blueprint.World.RuntimeModels, func(model RuntimeModelSpec) bool {
		return model.Run == runName
	})
}

func shrinkRentals(blueprint Blueprint) []shrinkCandidate {
	if len(blueprint.World.Rentals)+len(blueprint.World.Marketplace) <= 1 {
		return nil
	}
	var candidates []shrinkCandidate
	for index, rental := range blueprint.World.Rentals {
		candidate := mustCloneBlueprintForShrink(blueprint)
		candidate.World.Rentals = slices.Delete(candidate.World.Rentals, index, index+1)
		candidate.World.RentalSchedules = slices.DeleteFunc(candidate.World.RentalSchedules, func(schedule RentalScheduleSpec) bool {
			return schedule.RentalID == rental.ID
		})
		removeCandidateReferences(&candidate, rental.ID)
		candidates = append(candidates, shrinkCandidate{
			blueprint: candidate,
			step:      ShrinkStep{Dimension: "rentals", Removed: rental.ID},
		})
	}
	return candidates
}

func shrinkMarketplace(blueprint Blueprint) []shrinkCandidate {
	if len(blueprint.World.Rentals)+len(blueprint.World.Marketplace) <= 1 {
		return nil
	}
	var candidates []shrinkCandidate
	for index, offer := range blueprint.World.Marketplace {
		candidate := mustCloneBlueprintForShrink(blueprint)
		candidate.World.Marketplace = slices.Delete(candidate.World.Marketplace, index, index+1)
		removeCandidateReferences(&candidate, offer.ID)
		candidates = append(candidates, shrinkCandidate{
			blueprint: candidate,
			step:      ShrinkStep{Dimension: "offers", Removed: offer.ID},
		})
	}
	return candidates
}

func removeCandidateReferences(blueprint *Blueprint, candidateID string) {
	blueprint.World.Paths = slices.DeleteFunc(blueprint.World.Paths, func(path PathSpec) bool {
		return path.From == candidateID
	})
	blueprint.World.RuntimeModels = slices.DeleteFunc(blueprint.World.RuntimeModels, func(model RuntimeModelSpec) bool {
		return model.Candidate == candidateID
	})
}

func shrinkImageLayers(blueprint Blueprint) []shrinkCandidate {
	var candidates []shrinkCandidate
	refs := make([]string, 0, len(blueprint.World.Images))
	for ref := range blueprint.World.Images {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		image := blueprint.World.Images[ref]
		if len(image.Layers) <= 1 {
			continue
		}
		for index, layer := range image.Layers {
			candidate := mustCloneBlueprintForShrink(blueprint)
			candidateImage := candidate.World.Images[ref]
			candidateImage.Layers = slices.Delete(candidateImage.Layers, index, index+1)
			candidate.World.Images[ref] = candidateImage
			for rentalIndex := range candidate.World.Rentals {
				candidate.World.Rentals[rentalIndex].CachedLayers = slices.DeleteFunc(
					candidate.World.Rentals[rentalIndex].CachedLayers,
					func(digest string) bool { return digest == layer.Digest },
				)
			}
			candidates = append(candidates, shrinkCandidate{
				blueprint: candidate,
				step:      ShrinkStep{Dimension: "image_layers", Removed: ref + "/" + layer.Digest},
			})
		}
	}
	return candidates
}

func shrinkArtifacts(blueprint Blueprint) []shrinkCandidate {
	var candidates []shrinkCandidate
	for index, artifact := range blueprint.World.Artifacts {
		candidate := mustCloneBlueprintForShrink(blueprint)
		candidate.World.Artifacts = slices.Delete(candidate.World.Artifacts, index, index+1)
		for rentalIndex := range candidate.World.Rentals {
			candidate.World.Rentals[rentalIndex].ArtifactReplicas = slices.DeleteFunc(
				candidate.World.Rentals[rentalIndex].ArtifactReplicas,
				func(id string) bool { return id == artifact.ID },
			)
		}
		rewriteRequests(&candidate, func(request *RequestSpec) {
			request.ConsumesArtifacts = slices.DeleteFunc(request.ConsumesArtifacts, func(id string) bool { return id == artifact.ID })
			request.ProducesArtifacts = slices.DeleteFunc(request.ProducesArtifacts, func(id string) bool { return id == artifact.ID })
		})
		candidates = append(candidates, shrinkCandidate{
			blueprint: candidate,
			step:      ShrinkStep{Dimension: "artifacts", Removed: artifact.ID},
		})
	}
	return candidates
}

func shrinkFaults(blueprint Blueprint) []shrinkCandidate {
	var candidates []shrinkCandidate
	for index, fault := range blueprint.Faults {
		candidate := mustCloneBlueprintForShrink(blueprint)
		candidate.Faults = slices.Delete(candidate.Faults, index, index+1)
		candidates = append(candidates, shrinkCandidate{
			blueprint: candidate,
			step:      ShrinkStep{Dimension: "faults", Removed: fault.ID},
		})
	}
	return candidates
}

func shrinkOptionalFields(blueprint Blueprint) []shrinkCandidate {
	var candidates []shrinkCandidate
	if len(blueprint.World.Paths) > 0 {
		candidate := mustCloneBlueprintForShrink(blueprint)
		candidate.World.Paths = nil
		candidates = append(candidates, shrinkCandidate{
			blueprint: candidate,
			step:      ShrinkStep{Dimension: "optional_fields", Removed: "world.paths"},
		})
	}
	if len(blueprint.World.RuntimeModels) > 0 {
		candidate := mustCloneBlueprintForShrink(blueprint)
		candidate.World.RuntimeModels = nil
		candidates = append(candidates, shrinkCandidate{
			blueprint: candidate,
			step:      ShrinkStep{Dimension: "optional_fields", Removed: "world.runtime_models"},
		})
	}
	candidate := mustCloneBlueprintForShrink(blueprint)
	changed := false
	rewriteRequests(&candidate, func(request *RequestSpec) {
		if len(request.Phases) > 0 || len(request.CacheMounts) > 0 || request.Resources != nil {
			request.Phases = nil
			request.CacheMounts = nil
			request.Resources = nil
			changed = true
		}
	})
	if changed {
		candidates = append(candidates, shrinkCandidate{
			blueprint: candidate,
			step:      ShrinkStep{Dimension: "optional_fields", Removed: "request phases, Cache Mounts, and resources"},
		})
	}
	return candidates
}

func rewriteRequests(blueprint *Blueprint, rewrite func(*RequestSpec)) {
	if blueprint.Request != nil {
		rewrite(blueprint.Request)
	}
	for index := range blueprint.Timeline {
		if blueprint.Timeline[index].Request != nil {
			rewrite(blueprint.Timeline[index].Request)
		}
	}
	if blueprint.Arrivals == nil {
		return
	}
	for index := range blueprint.Arrivals.Runs {
		rewrite(&blueprint.Arrivals.Runs[index].Request)
	}
	if blueprint.Arrivals.Periodic != nil {
		rewrite(&blueprint.Arrivals.Periodic.Request)
	}
	if blueprint.Arrivals.Burst != nil {
		rewrite(&blueprint.Arrivals.Burst.Request)
	}
}

func cloneBlueprintForShrink(blueprint Blueprint) (Blueprint, error) {
	encoded, err := json.Marshal(blueprint)
	if err != nil {
		return Blueprint{}, err
	}
	var cloned Blueprint
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return Blueprint{}, err
	}
	cloned.Name = blueprint.Name
	return cloned, nil
}

func mustCloneBlueprintForShrink(blueprint Blueprint) Blueprint {
	cloned, err := cloneBlueprintForShrink(blueprint)
	if err != nil {
		panic(err)
	}
	return cloned
}
