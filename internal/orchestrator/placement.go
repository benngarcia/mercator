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

// ErrNoFeasibleOffers marks a complete placement evaluation that could not
// select an Offer. Replacement placement treats this as retry exhaustion once
// at least one stale Offer has already completed an attempt.
var ErrNoFeasibleOffers = errors.New("orchestrator: no feasible offers")

// PreviewPlacement evaluates placement for a workload without recording a run.
// It uses the same offer query and scheduler path as live placement (decide).
func (o *Orchestrator) PreviewPlacement(ctx context.Context, workspaceID, runID string, workload domain.WorkloadRevision) (domain.BookingDecision, error) {
	if workspaceID == "" {
		return domain.BookingDecision{}, fmt.Errorf("orchestrator: workspace_id is required")
	}
	if workload.WorkspaceID == "" {
		workload.WorkspaceID = workspaceID
	} else if workload.WorkspaceID != workspaceID {
		return domain.BookingDecision{}, fmt.Errorf("WORKSPACE_MISMATCH: request workspace_id must match workload workspace_id")
	}
	workload = domain.NormalizeWorkloadRevision(workload)
	if violations := domain.ValidateWorkloadRevision(workload); len(violations) > 0 {
		return domain.BookingDecision{}, &ValidationError{Violations: violations}
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

func (o *Orchestrator) decide(ctx context.Context, workspaceID string, requested runRequestedData, runID string, attemptNumber int, excludedOfferSnapshotIDs []string) (domain.BookingDecision, attemptData, domain.OfferSnapshot, error) {
	decision, offers, err := o.evaluatePlacement(ctx, runID, requested.Workload, excludedOfferSnapshotIDs)
	if err != nil {
		return domain.BookingDecision{}, attemptData{}, domain.OfferSnapshot{}, err
	}
	if decision.SelectedOfferSnapshotID == "" {
		return decision, attemptData{}, domain.OfferSnapshot{}, nil
	}
	selectedOffer, ok := selectedOfferByID(offers, decision.SelectedOfferSnapshotID)
	if !ok {
		return domain.BookingDecision{}, attemptData{}, domain.OfferSnapshot{}, fmt.Errorf("orchestrator: selected offer %s not found", decision.SelectedOfferSnapshotID)
	}
	return decision, newAttempt(workspaceID, runID, attemptNumber), selectedOffer, nil
}

// evaluatePlacement is the shared placement path for preview and live decide:
// fail-closed offer list, then scheduler.Evaluate.
func (o *Orchestrator) evaluatePlacement(ctx context.Context, runID string, workload domain.WorkloadRevision, excludedOfferSnapshotIDs []string) (domain.BookingDecision, []domain.OfferSnapshot, error) {
	offers, err := o.adapter.ListOffers(ctx, adapter.OfferRequest{
		WorkspaceID: workload.WorkspaceID,
		Resources:   workload.Spec.Resources,
	})
	if err != nil {
		return domain.BookingDecision{}, nil, fmt.Errorf("%w: %v", ErrOfferQuery, err)
	}
	decision, err := o.scheduler.Evaluate(ctx, scheduler.SchedulingInput{
		RunID:                    runID,
		Workload:                 workload,
		Offers:                   offers,
		ExcludedOfferSnapshotIDs: excludedOfferSnapshotIDs,
		ModelVersion:             "latency-v1",
		EvaluatedAt:              o.now().UTC(),
	})
	if err != nil {
		return domain.BookingDecision{}, nil, err
	}
	return decision, offers, nil
}
