package connection

import (
	"context"
	"errors"
	"testing"

	"github.com/benngarcia/mercator/internal/workspace"
)

func TestCreateRejectsArchivedWorkspace(t *testing.T) {
	service := New(openConnectionTestLog(t), rejectedWorkspace{err: workspace.ErrArchived})

	_, err := service.Create(context.Background(), CreateRequest{
		WorkspaceID:  "ws_archived",
		ConnectionID: "conn_1",
		AdapterType:  "fake",
	})

	if !errors.Is(err, workspace.ErrArchived) {
		t.Fatalf("create connection error = %v, want %v", err, workspace.ErrArchived)
	}
}

type rejectedWorkspace struct {
	err error
}

var activeTestWorkspace = rejectedWorkspace{}

func (w rejectedWorkspace) RequireActive(context.Context, string) error {
	return w.err
}
