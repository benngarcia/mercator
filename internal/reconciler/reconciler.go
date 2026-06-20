package reconciler

import "context"

type Driver interface {
	AdvanceRun(ctx context.Context, workspaceID, runID string) error
}

type Reconciler struct {
	driver Driver
}

func New(driver Driver) *Reconciler {
	return &Reconciler{driver: driver}
}

func (r *Reconciler) AdvanceRun(ctx context.Context, workspaceID, runID string) error {
	return r.driver.AdvanceRun(ctx, workspaceID, runID)
}
