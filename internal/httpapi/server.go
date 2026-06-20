package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/adapter/fake"
	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
	"github.com/bengarcia/mercator/internal/orchestrator"
	"github.com/bengarcia/mercator/internal/scheduler"
)

type Server struct {
	mux       *http.ServeMux
	orch      *orchestrator.Orchestrator
	scheduler scheduler.Scheduler
	adapter   adapter.Adapter
}

type createRunBody struct {
	WorkspaceID string                  `json:"workspace_id,omitempty"`
	RunID       string                  `json:"run_id"`
	Workload    domain.WorkloadRevision `json:"workload"`
}

type createRunResponse struct {
	RunID     string `json:"run_id"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

type eventListResponse struct {
	Events []eventlog.CloudEvent `json:"events"`
}

type placementPreviewBody struct {
	RunID       string                  `json:"run_id,omitempty"`
	WorkspaceID string                  `json:"workspace_id,omitempty"`
	Workload    domain.WorkloadRevision `json:"workload"`
}

type placementPreviewResponse struct {
	Decision domain.PlacementDecision `json:"decision"`
}

type runResponse struct {
	Run   domain.RunRecord  `json:"run"`
	Links map[string]string `json:"links,omitempty"`
}

type runListResponse struct {
	Runs []domain.RunRecord `json:"runs"`
}

type placementDecisionResponse struct {
	Decision domain.PlacementDecision `json:"decision"`
}

type errorResponse struct {
	Code    string             `json:"code"`
	Message string             `json:"message"`
	Details []domain.Violation `json:"details,omitempty"`
}

func New(orch *orchestrator.Orchestrator, sched scheduler.Scheduler, ad adapter.Adapter) http.Handler {
	s := &Server{mux: http.NewServeMux(), orch: orch, scheduler: sched, adapter: ad}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	s.mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(OpenAPIJSON))
	})
	s.mux.HandleFunc("POST /v1/runs", s.createRun)
	s.mux.HandleFunc("GET /v1/runs", s.listRuns)
	s.mux.HandleFunc("GET /v1/runs/{run_id}/events", s.runEvents)
	s.mux.HandleFunc("GET /v1/runs/{run_id}/decision", s.runDecision)
	s.mux.HandleFunc("GET /v1/runs/{run_ref}", s.getRunOrWait)
	s.mux.HandleFunc("POST /v1/runs/{run_action}", s.runAction)
	s.mux.HandleFunc("POST /v1/placements:preview", s.previewPlacement)
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Mutation requests require Idempotency-Key.")
		return
	}
	var body createRunBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	workspaceID := body.WorkspaceID
	if workspaceID == "" {
		workspaceID = body.Workload.WorkspaceID
	}
	result, err := s.orch.CreateRun(r.Context(), orchestrator.CreateRunRequest{
		WorkspaceID:    workspaceID,
		RunID:          body.RunID,
		IdempotencyKey: idempotencyKey,
		Workload:       body.Workload,
	})
	if err != nil {
		if errors.Is(err, eventlog.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency key was reused with a different request hash.")
			return
		}
		writeError(w, http.StatusBadRequest, errorCode(err, "CREATE_RUN_FAILED"), err.Error())
		return
	}
	if err := s.orch.AdvanceRun(r.Context(), workspaceID, body.RunID); err != nil {
		writeError(w, http.StatusBadGateway, "ADVANCE_RUN_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, createRunResponse{RunID: result.RunID, Duplicate: result.Duplicate})
}

func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	events, err := s.orch.GetRunEvents(r.Context(), workspaceID, runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "READ_EVENTS_FAILED", err.Error())
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

func (s *Server) getRunOrWait(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSuffix(r.PathValue("run_ref"), ":wait")
	s.writeRun(w, r, runID)
}

func (s *Server) writeRun(w http.ResponseWriter, r *http.Request, runID string) {
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	record, err := s.orch.GetRun(r.Context(), workspaceID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "RUN_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runResponse{Run: record, Links: runLinks(workspaceID, record.ID)})
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	records, err := s.orch.ListRuns(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_RUNS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runListResponse{Runs: records})
}

func (s *Server) runAction(w http.ResponseWriter, r *http.Request) {
	runAction := r.PathValue("run_action")
	if strings.HasSuffix(runAction, ":refresh") {
		s.refreshRun(w, r, strings.TrimSuffix(runAction, ":refresh"))
		return
	}
	if strings.HasSuffix(runAction, ":cancel") {
		s.writeRun(w, r, strings.TrimSuffix(runAction, ":cancel"))
		return
	}
	writeError(w, http.StatusNotFound, "RUN_ACTION_NOT_FOUND", "Unknown run action.")
}

func (s *Server) refreshRun(w http.ResponseWriter, r *http.Request, runID string) {
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	record, err := s.orch.RefreshRun(r.Context(), workspaceID, runID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "REFRESH_RUN_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runResponse{Run: record, Links: runLinks(workspaceID, record.ID)})
}

func (s *Server) runDecision(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	decision, err := s.orch.GetPlacementDecision(r.Context(), workspaceID, r.PathValue("run_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "DECISION_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, placementDecisionResponse{Decision: decision})
}

func (s *Server) previewPlacement(w http.ResponseWriter, r *http.Request) {
	var body placementPreviewBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	workspaceID := body.WorkspaceID
	if workspaceID == "" {
		workspaceID = body.Workload.WorkspaceID
	}
	if violations := domain.ValidateWorkloadRevision(body.Workload); len(violations) > 0 {
		writeError(w, http.StatusBadRequest, violations[0].Code, violations[0].Message)
		return
	}
	offers, err := s.adapter.ListOffers(r.Context(), adapter.OfferRequest{WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusBadGateway, "OFFER_QUERY_FAILED", err.Error())
		return
	}
	decision, err := s.scheduler.Evaluate(r.Context(), scheduler.SchedulingInput{
		RunID:        body.RunID,
		Workload:     body.Workload,
		Offers:       offers,
		ModelVersion: "latency-v1",
		EvaluatedAt:  time.Now().UTC(),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "PLACEMENT_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, placementPreviewResponse{Decision: decision})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Code: code, Message: strings.TrimSpace(message)})
}

func requiredWorkspace(w http.ResponseWriter, r *http.Request) (string, bool) {
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "WORKSPACE_ID_REQUIRED", "workspace_id query parameter is required.")
		return "", false
	}
	return workspaceID, true
}

func runLinks(workspaceID, runID string) map[string]string {
	query := "?workspace_id=" + workspaceID
	base := "/v1/runs/" + runID
	return map[string]string{
		"self":     base + query,
		"events":   base + "/events" + query,
		"decision": base + "/decision" + query,
		"refresh":  base + ":refresh" + query,
	}
}

var codedErrorPattern = regexp.MustCompile(`^([A-Z0-9_]+):\s*(.*)$`)

func errorCode(err error, fallback string) string {
	match := codedErrorPattern.FindStringSubmatch(err.Error())
	if len(match) == 3 {
		return match[1]
	}
	return fallback
}

func HandlerForSQLite(ctx context.Context, dsn string, offer []domain.OfferSnapshot) (http.Handler, func() error, error) {
	log, err := eventlog.OpenSQLite(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	ad := fake.New(fake.WithOffers(offer), fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded))
	sched := scheduler.New()
	orch := orchestrator.New(log, sched, ad)
	return New(orch, sched, ad), log.Close, nil
}
