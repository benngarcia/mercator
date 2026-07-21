package workload

import (
	"context"
	"errors"
	"testing"

	"github.com/benngarcia/mercator/internal/workspace"
)

func TestCreationRejectsArchivedWorkspace(t *testing.T) {
	service := New(openWorkloadTestLog(t), rejectedWorkspace{err: workspace.ErrArchived})

	if err := service.CreateWorkload(context.Background(), CreateWorkloadRequest{
		WorkspaceID: "ws_archived",
		WorkloadID:  "wrk_1",
	}); !errors.Is(err, workspace.ErrArchived) {
		t.Fatalf("create workload error = %v, want %v", err, workspace.ErrArchived)
	}

	if _, err := service.CreateRevision(context.Background(), CreateRevisionRequest{
		WorkspaceID: "ws_archived",
		WorkloadID:  "wrk_1",
		Revision:    validRevision(),
	}); !errors.Is(err, workspace.ErrArchived) {
		t.Fatalf("create revision error = %v, want %v", err, workspace.ErrArchived)
	}
}

type rejectedWorkspace struct {
	err error
}

var activeTestWorkspace = rejectedWorkspace{}

func (w rejectedWorkspace) RequireActive(context.Context, string) error {
	return w.err
}
