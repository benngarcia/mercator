package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

func (s *Server) CreateRun(ctx context.Context, request CreateRunRequestObject) (CreateRunResponseObject, error) {
	body := request.Body
	bodyWS := body.WorkspaceId
	if bodyWS == "" {
		bodyWS = body.Workload.WorkspaceID
	}
	workspaceID, workspaceErr := s.resolveWorkspace(ctx, bodyWS, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return CreateRun403JSONResponse(workspaceErr.Response), nil
		}
		return CreateRun400JSONResponse(workspaceErr.Response), nil
	}
	workloadRevision := body.Workload
	if body.WorkloadRevisionId != "" {
		if s.workloads == nil {
			return CreateRun400JSONResponse(apiError("WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")), nil
		}
		revision, err := s.workloads.GetRevision(ctx, workspaceID, body.WorkloadId, body.WorkloadRevisionId)
		if err != nil {
			return CreateRun404JSONResponse(apiError("WORKLOAD_REVISION_NOT_FOUND", err.Error())), nil
		}
		workloadRevision = revision
	}

	result, err := s.orch.Intake(ctx, orchestrator.IntakeRequest{
		WorkspaceID:    workspaceID,
		RunID:          body.RunId,
		IdempotencyKey: request.Params.IdempotencyKey,
		Actor:          requestActor(ctx),
		Workload:       workloadRevision,
		WorkloadID:     body.WorkloadId,
		Image:          body.Image,
		Args:           body.Args,
		Env:            body.Env,
		ResolveImage:   s.resolveImageFn(),
	})
	if err != nil {
		if response, ok := workspaceAPIError(err); ok {
			return CreateRun400JSONResponse(response), nil
		}
		if errors.Is(err, eventlog.ErrIdempotencyConflict) {
			return CreateRun409JSONResponse(apiError("IDEMPOTENCY_CONFLICT", "Idempotency key was reused with a different request hash.")), nil
		}
		if errors.Is(err, orchestrator.ErrRunRequestPersistence) || errors.Is(err, orchestrator.ErrAcceptedRunUnavailable) {
			return CreateRun500JSONResponse(internalAPIError(http.StatusInternalServerError, "CREATE_RUN_FAILED", err)), nil
		}
		return CreateRun400JSONResponse(apiError(errorCode(err, "CREATE_RUN_FAILED"), errorMessage(err))), nil
	}
	return CreateRun202JSONResponse(newRunResponse(workspaceID, result.Run, result.Duplicate)), nil
}

func newRunResponse(workspaceID string, record domain.RunRecord, duplicate bool) RunResponse {
	return RunResponse{
		RunId:     record.ID,
		Run:       record,
		Links:     runLinks(workspaceID, record.ID),
		Duplicate: duplicate,
	}
}

func (s *Server) ListRunEvents(ctx context.Context, request ListRunEventsRequestObject) (ListRunEventsResponseObject, error) {
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return ListRunEvents403JSONResponse(workspaceErr.Response), nil
		}
		return ListRunEvents400JSONResponse(workspaceErr.Response), nil
	}
	events, err := s.orch.GetRunEvents(ctx, workspaceID, request.RunId)
	if err != nil {
		return ListRunEvents500JSONResponse(internalAPIError(http.StatusInternalServerError, "READ_EVENTS_FAILED", err)), nil
	}
	public := make([]eventlog.CloudEvent, 0, len(events))
	for _, event := range events {
		if event.Visibility == eventlog.VisibilityPrivate {
			continue
		}
		public = append(public, event.CloudEvent())
	}
	return ListRunEvents200JSONResponse{Events: public}, nil
}

func (s *Server) GetRun(ctx context.Context, request GetRunRequestObject) (GetRunResponseObject, error) {
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return GetRun403JSONResponse(workspaceErr.Response), nil
		}
		return GetRun400JSONResponse(workspaceErr.Response), nil
	}
	record, err := s.orch.GetRun(ctx, workspaceID, request.RunId)
	if err != nil {
		return GetRun404JSONResponse(apiError("RUN_NOT_FOUND", err.Error())), nil
	}
	return GetRun200JSONResponse(newRunResponse(workspaceID, record, false)), nil
}

// waitDeadline bounds how long waitRun will long-poll for a terminal state.
// Overridable in tests.
var waitDeadline = 30 * time.Second

// waitPollInterval is the cadence at which waitRun re-drives an open run toward
// a terminal state. Overridable in tests.
var waitPollInterval = 100 * time.Millisecond

func (s *Server) WaitRun(ctx context.Context, request WaitRunRequestObject) (WaitRunResponseObject, error) {
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return WaitRun403JSONResponse(workspaceErr.Response), nil
		}
		return WaitRun400JSONResponse(workspaceErr.Response), nil
	}
	// Confirm the run exists (and the caller is authorized) before looping.
	record, err := s.orch.GetRun(ctx, workspaceID, request.RunId)
	if err != nil {
		return WaitRun404JSONResponse(apiError("RUN_NOT_FOUND", err.Error())), nil
	}
	deadline := time.Now().Add(waitDeadline)
	for {
		if record.Closed {
			return WaitRun200JSONResponse(newRunResponse(workspaceID, record, false)), nil
		}
		if !time.Now().Before(deadline) {
			// Bounded-deadline timeout: return the latest open run with a clean
			// 202 signal so the caller can decide to re-issue the wait.
			return WaitRun202JSONResponse(newRunResponse(workspaceID, record, false)), nil
		}
		select {
		case <-ctx.Done():
			return WaitRun408JSONResponse(apiError("WAIT_CANCELLED", "Wait request was cancelled.")), nil
		case <-time.After(waitPollInterval):
		}
		// Actively drive the run toward terminal rather than passively re-reading
		// stale state. RefreshRun advances the run then returns the latest record.
		next, err := s.orch.RefreshRun(ctx, workspaceID, request.RunId)
		if err != nil {
			// Advancement may legitimately error mid-flight (e.g. indeterminate
			// launch); fall back to the last readable state and keep waiting.
			next, err = s.orch.GetRun(ctx, workspaceID, request.RunId)
			if err != nil {
				return WaitRun404JSONResponse(apiError("RUN_NOT_FOUND", err.Error())), nil
			}
		}
		record = next
	}
}

func (s *Server) ListRuns(ctx context.Context, request ListRunsRequestObject) (ListRunsResponseObject, error) {
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return ListRuns403JSONResponse(workspaceErr.Response), nil
		}
		return ListRuns400JSONResponse(workspaceErr.Response), nil
	}
	records, err := s.orch.ListRuns(ctx, workspaceID)
	if err != nil {
		return ListRuns500JSONResponse(internalAPIError(http.StatusInternalServerError, "LIST_RUNS_FAILED", err)), nil
	}
	return ListRuns200JSONResponse{Runs: records}, nil
}

func (s *Server) CancelRun(ctx context.Context, request CancelRunRequestObject) (CancelRunResponseObject, error) {
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return CancelRun403JSONResponse(workspaceErr.Response), nil
		}
		return CancelRun400JSONResponse(workspaceErr.Response), nil
	}
	record, err := s.orch.CancelRun(ctx, workspaceID, request.RunId, requestActor(ctx))
	if err != nil {
		return CancelRun502JSONResponse(apiError("CANCEL_RUN_FAILED", "Run cancellation failed.")), nil
	}
	return CancelRun200JSONResponse(newRunResponse(workspaceID, record, false)), nil
}

func (s *Server) RefreshRun(ctx context.Context, request RefreshRunRequestObject) (RefreshRunResponseObject, error) {
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return RefreshRun403JSONResponse(workspaceErr.Response), nil
		}
		return RefreshRun400JSONResponse(workspaceErr.Response), nil
	}
	record, err := s.orch.RefreshRun(ctx, workspaceID, request.RunId)
	if err != nil {
		return RefreshRun502JSONResponse(apiError("REFRESH_RUN_FAILED", "Run refresh failed.")), nil
	}
	return RefreshRun200JSONResponse(newRunResponse(workspaceID, record, false)), nil
}

func (s *Server) ReportRun(ctx context.Context, request ReportRunRequestObject) (ReportRunResponseObject, error) {
	if s.reportSigner == nil || !s.reportSigner.Enabled() {
		return ReportRun501JSONResponse(apiError("REPORTING_DISABLED", "Reporting is not configured.")), nil
	}
	workspaceID := request.Params.WorkspaceId
	if workspaceID == "" {
		return ReportRun400JSONResponse(apiError("WORKSPACE_REQUIRED", "workspace_id query parameter is required.")), nil
	}
	token := strings.TrimPrefix(request.Params.Authorization, "Bearer ")
	if !s.reportSigner.Verify(workspaceID, request.RunId, token) {
		return ReportRun401JSONResponse(apiError("INVALID_RUN_TOKEN", "Invalid or missing run token.")), nil
	}
	body := request.Body
	report, err := orchestrator.NewRunReport(body.Type, body.Data, body.ExitCode)
	if err != nil {
		return ReportRun400JSONResponse(apiError("INVALID_REPORT", err.Error())), nil
	}
	if err := s.orch.RecordReport(ctx, workspaceID, request.RunId, report); err != nil {
		switch {
		case errors.Is(err, orchestrator.ErrRunNotFound):
			return ReportRun404JSONResponse(apiError("RUN_NOT_FOUND", "Run not found.")), nil
		case errors.Is(err, orchestrator.ErrTerminalReportConflict):
			return ReportRun409JSONResponse(apiError("TERMINAL_REPORT_CONFLICT", "A different terminal report is already recorded for this run.")), nil
		}
		return ReportRun502JSONResponse(internalAPIError(http.StatusBadGateway, "REPORT_FAILED", err)), nil
	}
	return ReportRun202JSONResponse{Recorded: true}, nil
}

func (s *Server) GetRunDecision(ctx context.Context, request GetRunDecisionRequestObject) (GetRunDecisionResponseObject, error) {
	workspaceID, workspaceErr := s.requiredWorkspace(ctx, request.Params.WorkspaceId)
	if workspaceErr != nil {
		if workspaceErr.Forbidden {
			return GetRunDecision403JSONResponse(workspaceErr.Response), nil
		}
		return GetRunDecision400JSONResponse(workspaceErr.Response), nil
	}
	decision, err := s.orch.GetBookingDecision(ctx, workspaceID, request.RunId)
	if err != nil {
		return GetRunDecision404JSONResponse(apiError("DECISION_NOT_FOUND", err.Error())), nil
	}
	return GetRunDecision200JSONResponse{Decision: decision}, nil
}
