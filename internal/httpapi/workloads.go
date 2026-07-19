package httpapi

import (
	"context"
	"net/http"

	"github.com/benngarcia/mercator/internal/workload"
)

func (s *Server) CreateWorkload(ctx context.Context, request CreateWorkloadRequestObject) (CreateWorkloadResponseObject, error) {
	if s.workloads == nil {
		return CreateWorkload501JSONResponse(apiError("WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")), nil
	}
	body := request.Body
	if body.WorkspaceId == "" {
		return CreateWorkload400JSONResponse(apiError("WORKSPACE_ID_REQUIRED", "workspace_id is required.")), nil
	}
	if workspaceErr := s.authorizeRequestWorkspace(ctx, body.WorkspaceId); workspaceErr != nil {
		return CreateWorkload403JSONResponse(workspaceErr.Response), nil
	}
	if err := s.workloads.CreateWorkload(ctx, workload.CreateWorkloadRequest{WorkspaceID: body.WorkspaceId, WorkloadID: body.WorkloadId, Name: body.Name}); err != nil {
		return CreateWorkload400JSONResponse(apiError(errorCode(err, "CREATE_WORKLOAD_FAILED"), errorMessage(err))), nil
	}
	return CreateWorkload202JSONResponse{WorkloadId: body.WorkloadId}, nil
}

func (s *Server) CreateWorkloadRevision(ctx context.Context, request CreateWorkloadRevisionRequestObject) (CreateWorkloadRevisionResponseObject, error) {
	if s.workloads == nil {
		return CreateWorkloadRevision501JSONResponse(apiError("WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")), nil
	}
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return CreateWorkloadRevision403JSONResponse(workspaceErr.Response), nil
		}
		return CreateWorkloadRevision400JSONResponse(workspaceErr.Response), nil
	}
	revision, err := s.workloads.CreateRevision(ctx, workload.CreateRevisionRequest{WorkspaceID: workspaceID, WorkloadID: request.WorkloadId, Revision: request.Body.Revision})
	if err != nil {
		return CreateWorkloadRevision400JSONResponse(apiError(errorCode(err, "CREATE_REVISION_FAILED"), errorMessage(err))), nil
	}
	return CreateWorkloadRevision202JSONResponse{Revision: revision}, nil
}

func (s *Server) ListWorkloadRevisions(ctx context.Context, request ListWorkloadRevisionsRequestObject) (ListWorkloadRevisionsResponseObject, error) {
	if s.workloads == nil {
		return ListWorkloadRevisions501JSONResponse(apiError("WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")), nil
	}
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return ListWorkloadRevisions403JSONResponse(workspaceErr.Response), nil
		}
		return ListWorkloadRevisions400JSONResponse(workspaceErr.Response), nil
	}
	revisions, err := s.workloads.ListRevisions(ctx, workspaceID, request.WorkloadId)
	if err != nil {
		return ListWorkloadRevisions500JSONResponse(internalAPIError(http.StatusInternalServerError, "LIST_REVISIONS_FAILED", err)), nil
	}
	return ListWorkloadRevisions200JSONResponse{Revisions: revisions}, nil
}

func (s *Server) GetWorkloadRevision(ctx context.Context, request GetWorkloadRevisionRequestObject) (GetWorkloadRevisionResponseObject, error) {
	if s.workloads == nil {
		return GetWorkloadRevision501JSONResponse(apiError("WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")), nil
	}
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return GetWorkloadRevision403JSONResponse(workspaceErr.Response), nil
		}
		return GetWorkloadRevision400JSONResponse(workspaceErr.Response), nil
	}
	revision, err := s.workloads.GetRevision(ctx, workspaceID, request.WorkloadId, request.RevisionId)
	if err != nil {
		return GetWorkloadRevision404JSONResponse(apiError("REVISION_NOT_FOUND", err.Error())), nil
	}
	return GetWorkloadRevision200JSONResponse{Revision: revision}, nil
}
