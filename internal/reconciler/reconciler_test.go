package reconciler

import (
	"context"
	"errors"
	"testing"
)

func TestReconcilerAdvanceRunDelegatesLifecycleProgress(t *testing.T) {
	ctx := context.Background()
	driver := &fakeDriver{}
	rec := New(driver)

	if err := rec.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance run: %v", err)
	}
	if driver.workspaceID != "ws_1" || driver.runID != "run_1" || driver.calls != 1 {
		t.Fatalf("unexpected driver call: %+v", driver)
	}
}

func TestReconcilerAdvanceRunPropagatesDriverErrors(t *testing.T) {
	expected := errors.New("boom")
	rec := New(&fakeDriver{err: expected})

	if err := rec.AdvanceRun(context.Background(), "ws_1", "run_1"); !errors.Is(err, expected) {
		t.Fatalf("expected %v, got %v", expected, err)
	}
}

type fakeDriver struct {
	workspaceID string
	runID       string
	calls       int
	err         error
}

func (f *fakeDriver) AdvanceRun(_ context.Context, workspaceID, runID string) error {
	f.calls++
	f.workspaceID = workspaceID
	f.runID = runID
	return f.err
}
