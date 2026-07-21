package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/benngarcia/mercator/internal/domain"
)

// IntakeRequest is the deep create-run interface: HTTP/CLI-shaped input that
// owns shorthand synthesis, env overrides, run-id minting, append, and the
// first advance. Workload revision lookup (stored revisions) stays with the
// caller — pass the resolved Workload here.
type IntakeRequest struct {
	WorkspaceID    string
	RunID          string
	IdempotencyKey string
	Actor          json.RawMessage
	Workload       domain.WorkloadRevision
	WorkloadID     string
	Image          string
	Args           []string
	Env            map[string]domain.EnvBinding
	ResolveImage   func(ctx context.Context, image, platform string) (string, error)
}

// IntakeResult is the run after intake has recorded and advanced it.
type IntakeResult struct {
	Run       domain.RunRecord
	Duplicate bool
}

// Intake accepts a create-run request and returns the current record once the
// run_requested event is durable. Eager advancement is best-effort after that
// acceptance point: provider failures are represented by the Run's recorded
// state and do not replace the accepted result with an error.
func (o *Orchestrator) Intake(ctx context.Context, req IntakeRequest) (IntakeResult, error) {
	if req.WorkspaceID == "" {
		return IntakeResult{}, fmt.Errorf("orchestrator: workspace_id is required")
	}
	workload := req.Workload
	if !hasWorkloadSpec(workload) && req.Image != "" {
		// Top-level image shorthand: synthesize the single container from the
		// top-level fields. Defaulting is applied during CreateRun's normalize
		// pass. An explicit full workload spec always takes precedence.
		workload = domain.WorkloadRevision{
			WorkspaceID: req.WorkspaceID,
			WorkloadID:  req.WorkloadID,
			Spec: domain.WorkloadSpec{
				Containers: []domain.ContainerSpec{{
					Image: req.Image,
					Args:  req.Args,
					Env:   req.Env,
				}},
			},
		}
	}
	workload = applyRunEnvOverrides(workload, req.Env)

	runID := req.RunID
	generated := false
	if runID == "" {
		generated = true
		runID = newRunID()
	}

	result, err := o.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    req.WorkspaceID,
		RunID:          runID,
		GeneratedRunID: generated,
		IdempotencyKey: req.IdempotencyKey,
		Actor:          req.Actor,
		Workload:       workload,
		ResolveImage:   req.ResolveImage,
	})
	if err != nil {
		return IntakeResult{}, err
	}
	_ = o.AdvanceRun(ctx, req.WorkspaceID, result.RunID)
	record, err := o.GetRun(ctx, req.WorkspaceID, result.RunID)
	if err != nil {
		return IntakeResult{}, fmt.Errorf("%w: %w", ErrAcceptedRunUnavailable, err)
	}
	return IntakeResult{Run: record, Duplicate: result.Duplicate}, nil
}

func hasWorkloadSpec(revision domain.WorkloadRevision) bool {
	return len(revision.Spec.Containers) > 0
}

func applyRunEnvOverrides(revision domain.WorkloadRevision, runEnv map[string]domain.EnvBinding) domain.WorkloadRevision {
	if len(runEnv) == 0 || len(revision.Spec.Containers) == 0 {
		return revision
	}
	container := &revision.Spec.Containers[0]
	merged := make(map[string]domain.EnvBinding, len(container.Env)+len(runEnv))
	for key, binding := range container.Env {
		merged[key] = binding
	}
	for key, binding := range runEnv {
		merged[key] = binding
	}
	container.Env = merged
	return revision
}

// newRunID mints a server-generated run identifier from a uuidv7 (time-ordered,
// collision-resistant). Used when a caller omits run_id on create.
func newRunID() string {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	return "run_" + id.String()
}
