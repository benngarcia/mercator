package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scheduler"
)

// ErrOfferQuery is returned when placement cannot load a complete offer set
// (provider failure, or a fail-closed partial aggregation).
var ErrOfferQuery = errors.New("orchestrator: offer query failed")
var ErrNoFeasibleOffers = errors.New("orchestrator: no feasible offers")

// PreviewPlacement evaluates placement for a workload without recording a run.
// It uses the same offer query and scheduler path as live placement (decide).
func (o *Orchestrator) PreviewPlacement(ctx context.Context, workspaceID, runID string, workload domain.WorkloadRevision) (domain.PlacementDecision, error) {
	if workspaceID == "" {
		return domain.PlacementDecision{}, fmt.Errorf("orchestrator: workspace_id is required")
	}
	if workload.WorkspaceID == "" {
		workload.WorkspaceID = workspaceID
	} else if workload.WorkspaceID != workspaceID {
		return domain.PlacementDecision{}, fmt.Errorf("WORKSPACE_MISMATCH: request workspace_id must match workload workspace_id")
	}
	workload = domain.NormalizeWorkloadRevision(workload)
	if violations := domain.ValidateWorkloadRevision(workload); len(violations) > 0 {
		return domain.PlacementDecision{}, &ValidationError{Violations: violations}
	}
	decision, _, err := o.evaluatePlacement(ctx, runID, workload, nil)
	return decision, err
}

// ValidationError carries domain violations from preview (and similar) validation.
type ValidationError struct {
	Violations []domain.Violation
}

func (e *ValidationError) Error() string {
	if e == nil || len(e.Violations) == 0 {
		return "orchestrator: validation failed"
	}
	return fmt.Sprintf("%s: %s", e.Violations[0].Code, e.Violations[0].Message)
}

type placementPlan struct {
	Decision             domain.PlacementDecision
	Attempt              attemptData
	SelectedOffer        domain.OfferSnapshot
	FinalPreStartAttempt bool
}

func (o *Orchestrator) decide(ctx context.Context, workspaceID string, requested runRequestedData, runID string, excludedOffers map[string]struct{}, attemptNumber int) (placementPlan, error) {
	decision, offers, err := o.evaluatePlacement(ctx, runID, requested.Workload, excludedOffers)
	if err != nil {
		return placementPlan{}, err
	}
	if decision.SelectedOfferSnapshotID == "" {
		return placementPlan{}, ErrNoFeasibleOffers
	}
	selectedOffer, ok := selectedOfferByID(offers, decision.SelectedOfferSnapshotID)
	if !ok {
		return placementPlan{}, fmt.Errorf("orchestrator: selected offer %s not found", decision.SelectedOfferSnapshotID)
	}
	return placementPlan{
		Decision:             decision,
		Attempt:              newAttempt(workspaceID, runID, attemptNumber),
		SelectedOffer:        selectedOffer,
		FinalPreStartAttempt: finalPreStartAttempt(decision, attemptNumber, requested.Workload.Spec.Execution.MaxPreStartAttempts),
	}, nil
}

// evaluatePlacement is the shared placement path for preview and live decide:
// fail-closed offer list, then scheduler.Evaluate.
func (o *Orchestrator) evaluatePlacement(ctx context.Context, runID string, workload domain.WorkloadRevision, excludedOffers map[string]struct{}) (domain.PlacementDecision, []domain.OfferSnapshot, error) {
	offers, err := o.adapter.ListOffers(ctx, adapter.OfferRequest{
		WorkspaceID: workload.WorkspaceID,
		DiagnosticContext: adapter.ProviderOperationContext{
			RunID: runID,
		},
		Resources: workload.Spec.Resources,
	})
	if err != nil {
		return domain.PlacementDecision{}, nil, fmt.Errorf("%w: %v", ErrOfferQuery, err)
	}
	offers = remainingOffers(offers, excludedOffers)
	decision, err := o.scheduler.Evaluate(ctx, scheduler.SchedulingInput{
		RunID:        runID,
		Workload:     workload,
		Offers:       offers,
		ModelVersion: "latency-v1",
		EvaluatedAt:  o.now().UTC(),
	})
	if err != nil {
		return domain.PlacementDecision{}, nil, err
	}
	return decision, offers, nil
}

func remainingOffers(offers []domain.OfferSnapshot, excluded map[string]struct{}) []domain.OfferSnapshot {
	if len(excluded) == 0 {
		return offers
	}
	remaining := make([]domain.OfferSnapshot, 0, len(offers))
	for _, offer := range offers {
		if _, rejected := excluded[offer.ID]; !rejected {
			remaining = append(remaining, offer)
		}
	}
	return remaining
}

func finalPreStartAttempt(decision domain.PlacementDecision, attemptNumber, maximumAttempts int) bool {
	if attemptNumber >= maximumAttempts {
		return true
	}
	feasibleOffers := 0
	for _, candidate := range decision.Candidates {
		if candidate.Feasible {
			feasibleOffers++
		}
	}
	return feasibleOffers <= 1
}
