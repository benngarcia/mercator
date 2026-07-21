package httpapi

import "context"

type activeTestWorkspaceCatalog struct{}

func (activeTestWorkspaceCatalog) RequireActive(context.Context, string) error {
	return nil
}

var activeTestWorkspaces = activeTestWorkspaceCatalog{}
