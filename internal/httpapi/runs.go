package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

func (s *Server) CreateRun(w http.ResponseWriter, r *http.Request, _ CreateRunParams) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Mutation requests require Idempotency-Key.")
		return
	}
	var body createRunBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	workspaceID := body.WorkspaceID
	if workspaceID == "" {
		workspaceID = body.Workload.WorkspaceID
	}
	if workspaceID == "" {
		workspaceID = r.URL.Query().Get("workspace_id")
	}
	if workspaceID == "" {
		workspaceID = s.defaultWorkspace()
	}
	if !s.authorizeRequestWorkspace(w, r, workspaceID) {
		return
	}
	workloadRevision := body.Workload
	if body.WorkloadRevisionID != "" {
		if s.workloads == nil {
			writeError(w, http.StatusBadRequest, "WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")
			return
		}
		revision, err := s.workloads.GetRevision(r.Context(), workspaceID, body.WorkloadID, body.WorkloadRevisionID)
		if err != nil {
			writeError(w, http.StatusNotFound, "WORKLOAD_REVISION_NOT_FOUND", err.Error())
			return
		}
		workloadRevision = revision
	} else if !body.hasWorkloadSpec() && body.Image != "" {
		// Top-level image shorthand: synthesize the single container from the
		// top-level fields. Defaulting (container name, platform, resources, etc.)
		// is applied server-side during CreateRun's normalize pass. An explicit
		// full workload spec always takes precedence over shorthand.
		workloadRevision = domain.WorkloadRevision{
			WorkspaceID: workspaceID,
			WorkloadID:  body.WorkloadID,
			Spec: domain.WorkloadSpec{
				Containers: []domain.ContainerSpec{{
					Image: body.Image,
					Args:  body.Args,
					Env:   body.Env,
				}},
			},
		}
	}

	// run_id is optional: generate a uuidv7 when omitted and use it as the
	// stream/run identifier returned to the caller.
	runID := body.RunID
	generated := false
	if runID == "" {
		generated = true
		runID = newRunID()
	}

	workloadRevision = applyRunEnvOverrides(workloadRevision, body.Env)
	result, err := s.orch.CreateRun(r.Context(), orchestrator.CreateRunRequest{
		WorkspaceID:    workspaceID,
		RunID:          runID,
		GeneratedRunID: generated,
		IdempotencyKey: idempotencyKey,
		Actor:          requestActor(r),
		Workload:       workloadRevision,
		ResolveImage:   s.resolveImageFn(),
	})
	if err != nil {
		if errors.Is(err, eventlog.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency key was reused with a different request hash.")
			return
		}
		writeError(w, http.StatusBadRequest, errorCode(err, "CREATE_RUN_FAILED"), errorMessage(err))
		return
	}
	// result.RunID is the canonical run identifier (the ORIGINAL run_id on a
	// replay, even if this request generated a fresh one).
	if err := s.orch.AdvanceRun(r.Context(), workspaceID, result.RunID); err != nil {
		writeError(w, http.StatusBadGateway, "ADVANCE_RUN_FAILED", "Run advancement failed; inspect public run events for the stable failure code.")
		return
	}
	record, err := s.orch.GetRun(r.Context(), workspaceID, result.RunID)
	if err != nil {
		writeInternalError(w, http.StatusInternalServerError, "RUN_NOT_FOUND", err)
		return
	}
	writeJSON(w, http.StatusAccepted, newRunResponse(workspaceID, record, result.Duplicate))
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

func (s *Server) ListRunEvents(w http.ResponseWriter, r *http.Request, runID string, _ ListRunEventsParams) {
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	events, err := s.orch.GetRunEvents(r.Context(), workspaceID, runID)
	if err != nil {
		writeInternalError(w, http.StatusInternalServerError, "READ_EVENTS_FAILED", err)
		return
	}
	public := make([]eventlog.CloudEvent, 0, len(events))
	for _, event := range events {
		if event.Visibility == eventlog.VisibilityPrivate {
			continue
		}
		public = append(public, event.CloudEvent())
	}
	writeJSON(w, http.StatusOK, eventListResponse{Events: public})
}

func (s *Server) GetRun(w http.ResponseWriter, r *http.Request, runID string, _ GetRunParams) {
	s.writeRun(w, r, runID)
}

// waitDeadline bounds how long waitRun will long-poll for a terminal state.
// Overridable in tests.
var waitDeadline = 30 * time.Second

// waitPollInterval is the cadence at which waitRun re-drives an open run toward
// a terminal state. Overridable in tests.
var waitPollInterval = 100 * time.Millisecond

func (s *Server) WaitRun(w http.ResponseWriter, r *http.Request, runID string, _ WaitRunParams) {
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	// Confirm the run exists (and the caller is authorized) before looping.
	record, err := s.orch.GetRun(r.Context(), workspaceID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "RUN_NOT_FOUND", err.Error())
		return
	}
	deadline := time.Now().Add(waitDeadline)
	for {
		if record.Closed {
			writeJSON(w, http.StatusOK, newRunResponse(workspaceID, record, false))
			return
		}
		if !time.Now().Before(deadline) {
			// Bounded-deadline timeout: return the latest open run with a clean
			// 202 signal so the caller can decide to re-issue the wait.
			writeJSON(w, http.StatusAccepted, newRunResponse(workspaceID, record, false))
			return
		}
		select {
		case <-r.Context().Done():
			writeError(w, http.StatusRequestTimeout, "WAIT_CANCELLED", "Wait request was cancelled.")
			return
		case <-time.After(waitPollInterval):
		}
		// Actively drive the run toward terminal rather than passively re-reading
		// stale state. RefreshRun advances the run then returns the latest record.
		next, err := s.orch.RefreshRun(r.Context(), workspaceID, runID)
		if err != nil {
			// Advancement may legitimately error mid-flight (e.g. indeterminate
			// launch); fall back to the last readable state and keep waiting.
			next, err = s.orch.GetRun(r.Context(), workspaceID, runID)
			if err != nil {
				writeError(w, http.StatusNotFound, "RUN_NOT_FOUND", err.Error())
				return
			}
		}
		record = next
	}
}

func (s *Server) writeRun(w http.ResponseWriter, r *http.Request, runID string) {
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	record, err := s.orch.GetRun(r.Context(), workspaceID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "RUN_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, newRunResponse(workspaceID, record, false))
}

func (s *Server) ListRuns(w http.ResponseWriter, r *http.Request, _ ListRunsParams) {
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	records, err := s.orch.ListRuns(r.Context(), workspaceID)
	if err != nil {
		writeInternalError(w, http.StatusInternalServerError, "LIST_RUNS_FAILED", err)
		return
	}
	writeJSON(w, http.StatusOK, runListResponse{Runs: records})
}

func (s *Server) CancelRun(w http.ResponseWriter, r *http.Request, runID string, _ CancelRunParams) {
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	record, err := s.orch.CancelRun(r.Context(), workspaceID, runID, requestActor(r))
	if err != nil {
		writeError(w, http.StatusBadGateway, "CANCEL_RUN_FAILED", "Run cancellation failed.")
		return
	}
	writeJSON(w, http.StatusOK, newRunResponse(workspaceID, record, false))
}

func (s *Server) RefreshRun(w http.ResponseWriter, r *http.Request, runID string, _ RefreshRunParams) {
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	record, err := s.orch.RefreshRun(r.Context(), workspaceID, runID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "REFRESH_RUN_FAILED", "Run refresh failed.")
		return
	}
	writeJSON(w, http.StatusOK, newRunResponse(workspaceID, record, false))
}

type reportBody struct {
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
}

func (s *Server) ReportRun(w http.ResponseWriter, r *http.Request, runID string, _ ReportRunParams) {
	if s.reportSigner == nil || !s.reportSigner.Enabled() {
		writeError(w, http.StatusNotImplemented, "REPORTING_DISABLED", "Reporting is not configured.")
		return
	}
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "WORKSPACE_REQUIRED", "workspace_id query parameter is required.")
		return
	}
	authHeader := r.Header.Get("Authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if !s.reportSigner.Verify(workspaceID, runID, token) {
		writeError(w, http.StatusUnauthorized, "INVALID_RUN_TOKEN", "Invalid or missing run token.")
		return
	}
	var body reportBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if err := s.orch.RecordReport(r.Context(), workspaceID, runID, body.Type, body.Data, body.ExitCode); err != nil {
		if errors.Is(err, orchestrator.ErrRunNotFound) {
			writeError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run not found.")
			return
		}
		writeInternalError(w, http.StatusBadGateway, "REPORT_FAILED", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"recorded": true})
}

func (s *Server) GetRunDecision(w http.ResponseWriter, r *http.Request, runID string, _ GetRunDecisionParams) {
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	decision, err := s.orch.GetPlacementDecision(r.Context(), workspaceID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "DECISION_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, placementDecisionResponse{Decision: decision})
}
