package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/ociresolver"
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
	decision, _, _, err := o.evaluatePlacement(ctx, runID, workload, nil)
	return decision, err
}

// manifestBudget is how long Placement waits for a registry. Resolution is an
// external read on the run-create path, and a registry that blackholes packets
// would otherwise hold that request open long past the point its client is
// gone. An unresolved manifest costs a placement its ability to tell a warm
// host from a cold one; a hung one costs the caller the Run.
const manifestBudget = 10 * time.Second

// imageManifest resolves what this Run's image contains. A resolver that is
// absent, too slow, or refused leaves the manifest unknown, and the reason is
// carried onto every candidate's transfer estimate rather than the failure
// being read as nothing to fetch.
func (o *Orchestrator) imageManifest(ctx context.Context, workload domain.WorkloadRevision) domain.ImageManifest {
	if o.manifests == nil || len(workload.Spec.Containers) == 0 {
		return domain.ImageManifest{}
	}
	ctx, cancel := context.WithTimeout(ctx, manifestBudget)
	defer cancel()
	container := workload.Spec.Containers[0]
	manifest, err := o.manifests.ResolveManifest(ctx, container.Image, container.Platform)
	if err != nil {
		return domain.ImageManifest{Unreadable: ociresolver.Unreadable(err)}
	}
	return manifest
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

func (o *Orchestrator) decide(ctx context.Context, workspaceID string, requested runRequestedData, runID string, attemptNumber int, excludedOfferSnapshotIDs []string) (domain.BookingDecision, attemptData, domain.OfferSnapshot, domain.RentalSchedule, error) {
	decision, offers, schedules, err := o.evaluatePlacement(ctx, runID, requested.Workload, excludedOfferSnapshotIDs)
	if err != nil {
		return domain.BookingDecision{}, attemptData{}, domain.OfferSnapshot{}, domain.RentalSchedule{}, err
	}
	if decision.SelectedOfferSnapshotID == "" {
		return decision, attemptData{}, domain.OfferSnapshot{}, domain.RentalSchedule{}, nil
	}
	selectedOffer, ok := selectedOfferByID(offers, decision.SelectedOfferSnapshotID)
	if !ok {
		return domain.BookingDecision{}, attemptData{}, domain.OfferSnapshot{}, domain.RentalSchedule{}, fmt.Errorf("orchestrator: selected offer %s not found", decision.SelectedOfferSnapshotID)
	}
	schedule := schedules[decision.Booking.RentalID]
	if schedule.RentalID == "" {
		schedule = domain.NewRentalSchedule(decision.Booking.RentalID)
	}
	return decision, newAttempt(workspaceID, runID, attemptNumber), selectedOffer, schedule, nil
}

// evaluatePlacement is the shared placement path for preview and live decide:
// fail-closed offer list, then scheduler.Evaluate.
func (o *Orchestrator) evaluatePlacement(ctx context.Context, runID string, workload domain.WorkloadRevision, excludedOfferSnapshotIDs []string) (domain.BookingDecision, []domain.OfferSnapshot, map[string]domain.RentalSchedule, error) {
	schedules, err := o.schedules.List(ctx, workload.WorkspaceID)
	if err != nil {
		return domain.BookingDecision{}, nil, nil, fmt.Errorf("orchestrator: list Rental Schedules: %w", err)
	}
	offers, err := o.adapter.ListOffers(ctx, adapter.OfferRequest{
		WorkspaceID: workload.WorkspaceID,
		Resources:   workload.Spec.Resources,
	})
	if err != nil {
		return domain.BookingDecision{}, nil, nil, fmt.Errorf("%w: %v", ErrOfferQuery, err)
	}
	artifacts, err := o.consumedArtifacts(ctx, workload.WorkspaceID, workload)
	if err != nil {
		return domain.BookingDecision{}, nil, nil, err
	}
	decision, err := o.scheduler.Evaluate(ctx, scheduler.SchedulingInput{
		RunID:                    runID,
		Workload:                 workload,
		Image:                    o.imageManifest(ctx, workload),
		Artifacts:                artifacts,
		Offers:                   offers,
		Schedules:                schedules,
		ExcludedOfferSnapshotIDs: excludedOfferSnapshotIDs,
		ModelVersion:             "latency-v1",
		EvaluatedAt:              o.now().UTC(),
	})
	if err != nil {
		return domain.BookingDecision{}, nil, nil, err
	}
	return decision, offers, schedules, nil
}
