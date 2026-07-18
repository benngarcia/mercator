package httpapi

import (
	"net/http"

	"github.com/benngarcia/mercator/internal/workload"
)

func (s *Server) CreateWorkload(w http.ResponseWriter, r *http.Request, _ CreateWorkloadParams) {
	if s.workloads == nil {
		writeError(w, http.StatusNotImplemented, "WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")
		return
	}
	if r.Header.Get("Idempotency-Key") == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Mutation requests require Idempotency-Key.")
		return
	}
	var body createWorkloadBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if !s.authorizeRequestWorkspace(w, r, body.WorkspaceID) {
		return
	}
	if err := s.workloads.CreateWorkload(r.Context(), workload.CreateWorkloadRequest{WorkspaceID: body.WorkspaceID, WorkloadID: body.WorkloadID, Name: body.Name}); err != nil {
		writeError(w, http.StatusBadRequest, errorCode(err, "CREATE_WORKLOAD_FAILED"), errorMessage(err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"workload_id": body.WorkloadID})
}

func (s *Server) CreateWorkloadRevision(w http.ResponseWriter, r *http.Request, workloadID string, _ CreateWorkloadRevisionParams) {
	if s.workloads == nil {
		writeError(w, http.StatusNotImplemented, "WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")
		return
	}
	if r.Header.Get("Idempotency-Key") == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Mutation requests require Idempotency-Key.")
		return
	}
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	var body createRevisionBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	revision, err := s.workloads.CreateRevision(r.Context(), workload.CreateRevisionRequest{WorkspaceID: workspaceID, WorkloadID: workloadID, Revision: body.Revision})
	if err != nil {
		writeError(w, http.StatusBadRequest, errorCode(err, "CREATE_REVISION_FAILED"), errorMessage(err))
		return
	}
	writeJSON(w, http.StatusAccepted, workloadRevisionResponse{Revision: revision})
}

func (s *Server) ListWorkloadRevisions(w http.ResponseWriter, r *http.Request, workloadID string, _ ListWorkloadRevisionsParams) {
	if s.workloads == nil {
		writeError(w, http.StatusNotImplemented, "WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")
		return
	}
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	revisions, err := s.workloads.ListRevisions(r.Context(), workspaceID, workloadID)
	if err != nil {
		writeInternalError(w, http.StatusInternalServerError, "LIST_REVISIONS_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, workloadRevisionListResponse{Revisions: revisions})
}

func (s *Server) GetWorkloadRevision(w http.ResponseWriter, r *http.Request, workloadID, revisionID string, _ GetWorkloadRevisionParams) {
	if s.workloads == nil {
		writeError(w, http.StatusNotImplemented, "WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")
		return
	}
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	revision, err := s.workloads.GetRevision(r.Context(), workspaceID, workloadID, revisionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "REVISION_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, workloadRevisionResponse{Revision: revision})
}
