package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/benngarcia/mercator/internal/workspace"
)

func (s *Server) ListWorkspaces(ctx context.Context, request ListWorkspacesRequestObject) (ListWorkspacesResponseObject, error) {
	if _, authError := requirePrincipal(ctx); authError != nil {
		return ListWorkspaces401JSONResponse(*authError), nil
	}
	if s.workspaces == nil {
		return ListWorkspaces500JSONResponse(internalAPIError(http.StatusInternalServerError, "WORKSPACE_CATALOG_DISABLED", errors.New("workspace catalog is not configured"))), nil
	}
	items, err := s.workspaces.List(ctx, workspace.ListOptions{IncludeArchived: request.Params.IncludeArchived})
	if err != nil {
		return ListWorkspaces500JSONResponse(internalAPIError(http.StatusInternalServerError, "LIST_WORKSPACES_FAILED", err)), nil
	}
	if items == nil {
		items = []workspace.Workspace{}
	}
	return ListWorkspaces200JSONResponse{Workspaces: items}, nil
}

func (s *Server) CreateWorkspace(ctx context.Context, request CreateWorkspaceRequestObject) (CreateWorkspaceResponseObject, error) {
	if s.workspaces == nil {
		return CreateWorkspace500JSONResponse(internalAPIError(http.StatusInternalServerError, "WORKSPACE_CATALOG_DISABLED", errors.New("workspace catalog is not configured"))), nil
	}
	createdBy, authError := requirePrincipal(ctx)
	if authError != nil {
		return CreateWorkspace401JSONResponse(*authError), nil
	}
	id, err := newWorkspaceID()
	if err != nil {
		return CreateWorkspace500JSONResponse(internalAPIError(http.StatusInternalServerError, "WORKSPACE_ID_FAILED", err)), nil
	}
	item, err := s.workspaces.Create(ctx, workspace.Create{
		ID:          id,
		DisplayName: request.Body.DisplayName,
		CreatedAt:   time.Now().UTC(),
		CreatedBy:   createdBy,
	})
	if err != nil {
		if errors.Is(err, workspace.ErrAlreadyExists) {
			return CreateWorkspace409JSONResponse(apiError("WORKSPACE_ALREADY_EXISTS", err.Error())), nil
		}
		return CreateWorkspace400JSONResponse(apiError("CREATE_WORKSPACE_FAILED", err.Error())), nil
	}
	return CreateWorkspace201JSONResponse{Workspace: item}, nil
}

func (s *Server) ArchiveWorkspace(ctx context.Context, request ArchiveWorkspaceRequestObject) (ArchiveWorkspaceResponseObject, error) {
	if _, authError := requirePrincipal(ctx); authError != nil {
		return ArchiveWorkspace401JSONResponse(*authError), nil
	}
	if s.workspaces == nil {
		return ArchiveWorkspace500JSONResponse(internalAPIError(http.StatusInternalServerError, "WORKSPACE_CATALOG_DISABLED", errors.New("workspace catalog is not configured"))), nil
	}
	item, err := s.workspaces.Archive(ctx, request.WorkspaceId, time.Now().UTC())
	if err != nil {
		if errors.Is(err, workspace.ErrNotFound) {
			return ArchiveWorkspace404JSONResponse(apiError("WORKSPACE_NOT_FOUND", "Workspace not found.")), nil
		}
		return ArchiveWorkspace400JSONResponse(apiError("ARCHIVE_WORKSPACE_FAILED", err.Error())), nil
	}
	return ArchiveWorkspace200JSONResponse{Workspace: item}, nil
}

func newWorkspaceID() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "ws_" + hex.EncodeToString(value[:]), nil
}
