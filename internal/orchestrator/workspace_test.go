package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/workspace"
)

func TestCreateRunRejectsArchivedWorkspace(t *testing.T) {
	log := openOrchestratorLog(t)
	orch := New(log, scheduler.New(), fake.New(), rejectedWorkspace{err: workspace.ErrArchived})

	_, err := orch.CreateRun(context.Background(), CreateRunRequest{
		WorkspaceID: "ws_archived",
		RunID:       "run_1",
		CommandKey:  "cmd_1",
		Workload:    orchRevision(),
	})

	if !errors.Is(err, workspace.ErrArchived) {
		t.Fatalf("create run error = %v, want %v", err, workspace.ErrArchived)
	}
}

type rejectedWorkspace struct {
	err error
}

var activeTestWorkspace = rejectedWorkspace{}

func (w rejectedWorkspace) RequireActive(context.Context, string) error {
	return w.err
}
