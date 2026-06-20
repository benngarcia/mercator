package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/adapter/fake"
	"github.com/bengarcia/mercator/internal/connection"
	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
	"github.com/bengarcia/mercator/internal/ociresolver"
	"github.com/bengarcia/mercator/internal/offers"
	"github.com/bengarcia/mercator/internal/orchestrator"
	"github.com/bengarcia/mercator/internal/scheduler"
	"github.com/bengarcia/mercator/internal/secrets"
	sinkspkg "github.com/bengarcia/mercator/internal/sinks"
	"github.com/bengarcia/mercator/internal/workload"
	"github.com/bengarcia/mercator/web"
)

type Server struct {
	mux       *http.ServeMux
	orch      *orchestrator.Orchestrator
	scheduler scheduler.Scheduler
	adapter   adapter.Adapter
	workloads *workload.Service
	secrets   *secrets.Vault
	sinks     *sinkspkg.Manager
	conns     *connection.Service
	offers    *offers.Service
	resolver  interface {
		Resolve(context.Context, ociresolver.ResolveRequest) (ociresolver.ResolvedImage, error)
	}
}

type createRunBody struct {
	WorkspaceID        string                  `json:"workspace_id,omitempty"`
	RunID              string                  `json:"run_id"`
	WorkloadID         string                  `json:"workload_id,omitempty"`
	WorkloadRevisionID string                  `json:"workload_revision_id,omitempty"`
	Workload           domain.WorkloadRevision `json:"workload"`
}

type createWorkloadBody struct {
	WorkspaceID string `json:"workspace_id"`
	WorkloadID  string `json:"workload_id"`
	Name        string `json:"name"`
}

type createRevisionBody struct {
	Revision domain.WorkloadRevision `json:"revision"`
}

type workloadRevisionResponse struct {
	Revision domain.WorkloadRevision `json:"revision"`
}

type workloadRevisionListResponse struct {
	Revisions []domain.WorkloadRevision `json:"revisions"`
}

type resolveImageBody struct {
	Image    string `json:"image"`
	Platform string `json:"platform"`
}

type resolveImageResponse struct {
	Image ociresolver.ResolvedImage `json:"image"`
}

type createSecretVersionBody struct {
	WorkspaceID string `json:"workspace_id"`
	SecretID    string `json:"secret_id"`
	Value       string `json:"value"`
}

type secretMetadataListResponse struct {
	Secrets []secrets.SecretMetadata `json:"secrets"`
}

type grantSecretBody struct {
	WorkspaceID string `json:"workspace_id"`
	SecretID    string `json:"secret_id"`
	Version     int    `json:"version"`
	ScopeType   string `json:"scope_type"`
	ScopeID     string `json:"scope_id"`
}

type secretGrantResponse struct {
	Grant secrets.Grant `json:"grant"`
}

type connectionListResponse struct {
	Connections []connection.Record `json:"connections"`
}

type offerListResponse struct {
	Offers []domain.OfferSnapshot `json:"offers"`
}

type replaySinkBody struct {
	FromExclusive eventlog.GlobalPosition `json:"from_exclusive"`
	Limit         int                     `json:"limit"`
	ReplayID      string                  `json:"replay_id"`
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

func NewWithServices(orch *orchestrator.Orchestrator, sched scheduler.Scheduler, ad adapter.Adapter, workloads *workload.Service, secretVault *secrets.Vault, resolver interface {
	Resolve(context.Context, ociresolver.ResolveRequest) (ociresolver.ResolvedImage, error)
}) http.Handler {
	return NewWithAllServices(orch, sched, ad, workloads, secretVault, nil, nil, nil, resolver)
}

func NewWithAllServices(orch *orchestrator.Orchestrator, sched scheduler.Scheduler, ad adapter.Adapter, workloads *workload.Service, secretVault *secrets.Vault, sinkManager *sinkspkg.Manager, conns *connection.Service, offerService *offers.Service, resolver interface {
	Resolve(context.Context, ociresolver.ResolveRequest) (ociresolver.ResolvedImage, error)
}) http.Handler {
	s := &Server{mux: http.NewServeMux(), orch: orch, scheduler: sched, adapter: ad, workloads: workloads, secrets: secretVault, sinks: sinkManager, conns: conns, offers: offerService, resolver: resolver}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	staticFS := http.FS(web.Static())
	s.mux.HandleFunc("GET /", s.serveUI)
	s.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	s.mux.Handle("GET /ui/", http.StripPrefix("/ui/", http.FileServer(staticFS)))
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
	s.mux.HandleFunc("POST /v1/workloads", s.createWorkload)
	s.mux.HandleFunc("POST /v1/workloads/{workload_id}/revisions", s.createRevision)
	s.mux.HandleFunc("GET /v1/workloads/{workload_id}/revisions", s.listRevisions)
	s.mux.HandleFunc("GET /v1/workloads/{workload_id}/revisions/{revision_id}", s.getRevision)
	s.mux.HandleFunc("POST /v1/images:resolve", s.resolveImage)
	s.mux.HandleFunc("GET /v1/connections", s.listConnections)
	s.mux.HandleFunc("GET /v1/offers", s.listOffers)
	s.mux.HandleFunc("GET /v1/secrets", s.listSecrets)
	s.mux.HandleFunc("POST /v1/secrets/{secret_id}/versions", s.createSecretVersion)
	s.mux.HandleFunc("POST /v1/secrets/{secret_id}/grants", s.grantSecret)
	s.mux.HandleFunc("GET /v1/sinks/{sink_id}", s.sinkStatus)
	s.mux.HandleFunc("POST /v1/sinks/{sink_action}", s.sinkAction)
	s.mux.HandleFunc("POST /v1/placements:preview", s.previewPlacement)
}

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	file, err := web.Static().Open("index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UI_NOT_AVAILABLE", err.Error())
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UI_NOT_AVAILABLE", err.Error())
		return
	}
	reader, ok := file.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	})
	if !ok {
		writeError(w, http.StatusInternalServerError, "UI_NOT_AVAILABLE", "embedded UI file is not seekable")
		return
	}
	http.ServeContent(w, r, "index.html", stat.ModTime(), reader)
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
	}
	result, err := s.orch.CreateRun(r.Context(), orchestrator.CreateRunRequest{
		WorkspaceID:    workspaceID,
		RunID:          body.RunID,
		IdempotencyKey: idempotencyKey,
		Workload:       workloadRevision,
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
		s.cancelRun(w, r, strings.TrimSuffix(runAction, ":cancel"))
		return
	}
	writeError(w, http.StatusNotFound, "RUN_ACTION_NOT_FOUND", "Unknown run action.")
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request, runID string) {
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	record, err := s.orch.CancelRun(r.Context(), workspaceID, runID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "CANCEL_RUN_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runResponse{Run: record, Links: runLinks(workspaceID, record.ID)})
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

func (s *Server) createWorkload(w http.ResponseWriter, r *http.Request) {
	if s.workloads == nil {
		writeError(w, http.StatusNotImplemented, "WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")
		return
	}
	var body createWorkloadBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if err := s.workloads.CreateWorkload(r.Context(), workload.CreateWorkloadRequest{WorkspaceID: body.WorkspaceID, WorkloadID: body.WorkloadID, Name: body.Name}); err != nil {
		writeError(w, http.StatusBadRequest, errorCode(err, "CREATE_WORKLOAD_FAILED"), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"workload_id": body.WorkloadID})
}

func (s *Server) createRevision(w http.ResponseWriter, r *http.Request) {
	if s.workloads == nil {
		writeError(w, http.StatusNotImplemented, "WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")
		return
	}
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	var body createRevisionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	revision, err := s.workloads.CreateRevision(r.Context(), workload.CreateRevisionRequest{WorkspaceID: workspaceID, WorkloadID: r.PathValue("workload_id"), Revision: body.Revision})
	if err != nil {
		writeError(w, http.StatusBadRequest, errorCode(err, "CREATE_REVISION_FAILED"), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, workloadRevisionResponse{Revision: revision})
}

func (s *Server) listRevisions(w http.ResponseWriter, r *http.Request) {
	if s.workloads == nil {
		writeError(w, http.StatusNotImplemented, "WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")
		return
	}
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	revisions, err := s.workloads.ListRevisions(r.Context(), workspaceID, r.PathValue("workload_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_REVISIONS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, workloadRevisionListResponse{Revisions: revisions})
}

func (s *Server) getRevision(w http.ResponseWriter, r *http.Request) {
	if s.workloads == nil {
		writeError(w, http.StatusNotImplemented, "WORKLOAD_SERVICE_DISABLED", "Workload service is not configured.")
		return
	}
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	revision, err := s.workloads.GetRevision(r.Context(), workspaceID, r.PathValue("workload_id"), r.PathValue("revision_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "REVISION_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, workloadRevisionResponse{Revision: revision})
}

func (s *Server) resolveImage(w http.ResponseWriter, r *http.Request) {
	if s.resolver == nil {
		writeError(w, http.StatusNotImplemented, "IMAGE_RESOLVER_DISABLED", "Image resolver is not configured.")
		return
	}
	var body resolveImageBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	resolved, err := s.resolver.Resolve(r.Context(), ociresolver.ResolveRequest{Image: body.Image, Platform: body.Platform})
	if err != nil {
		writeError(w, http.StatusBadRequest, "IMAGE_RESOLUTION_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resolveImageResponse{Image: resolved})
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	if s.conns == nil {
		writeJSON(w, http.StatusOK, connectionListResponse{Connections: []connection.Record{}})
		return
	}
	records, err := s.conns.List(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_CONNECTIONS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, connectionListResponse{Connections: records})
}

func (s *Server) listOffers(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	if s.offers != nil {
		records, err := s.offers.ListCached(r.Context(), workspaceID, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "LIST_OFFERS_FAILED", err.Error())
			return
		}
		if len(records) > 0 {
			writeJSON(w, http.StatusOK, offerListResponse{Offers: records})
			return
		}
	}
	records, err := s.adapter.ListOffers(r.Context(), adapter.OfferRequest{WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusBadGateway, "LIST_OFFERS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, offerListResponse{Offers: records})
}

func (s *Server) createSecretVersion(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusNotImplemented, "SECRET_VAULT_DISABLED", "Secret vault is not configured.")
		return
	}
	var body createSecretVersionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if body.SecretID == "" {
		body.SecretID = r.PathValue("secret_id")
	}
	version, err := s.secrets.CreateVersion(r.Context(), secrets.CreateVersionRequest{WorkspaceID: body.WorkspaceID, SecretID: body.SecretID, Plaintext: []byte(body.Value)})
	if err != nil {
		writeError(w, http.StatusBadRequest, "CREATE_SECRET_VERSION_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"secret_id": version.SecretID, "version": version.Version})
}

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusNotImplemented, "SECRET_VAULT_DISABLED", "Secret vault is not configured.")
		return
	}
	workspaceID, ok := requiredWorkspace(w, r)
	if !ok {
		return
	}
	metadata, err := s.secrets.ListMetadata(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_SECRETS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, secretMetadataListResponse{Secrets: metadata})
}

func (s *Server) grantSecret(w http.ResponseWriter, r *http.Request) {
	if s.secrets == nil {
		writeError(w, http.StatusNotImplemented, "SECRET_VAULT_DISABLED", "Secret vault is not configured.")
		return
	}
	var body grantSecretBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if body.SecretID == "" {
		body.SecretID = r.PathValue("secret_id")
	}
	grant, err := s.secrets.Grant(r.Context(), secrets.GrantRequest{WorkspaceID: body.WorkspaceID, SecretID: body.SecretID, Version: body.Version, ScopeType: body.ScopeType, ScopeID: body.ScopeID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "GRANT_SECRET_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, secretGrantResponse{Grant: grant})
}

func (s *Server) sinkStatus(w http.ResponseWriter, r *http.Request) {
	if s.sinks == nil {
		writeError(w, http.StatusNotImplemented, "SINKS_DISABLED", "Sink manager is not configured.")
		return
	}
	status, err := s.sinks.Status(r.Context(), r.PathValue("sink_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "SINK_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) sinkAction(w http.ResponseWriter, r *http.Request) {
	sinkAction := r.PathValue("sink_action")
	if strings.HasSuffix(sinkAction, ":deliver") {
		s.deliverSink(w, r, strings.TrimSuffix(sinkAction, ":deliver"))
		return
	}
	if strings.HasSuffix(sinkAction, ":replay") {
		s.replaySink(w, r, strings.TrimSuffix(sinkAction, ":replay"))
		return
	}
	writeError(w, http.StatusNotFound, "SINK_ACTION_NOT_FOUND", "Unknown sink action.")
}

func (s *Server) deliverSink(w http.ResponseWriter, r *http.Request, sinkID string) {
	if s.sinks == nil {
		writeError(w, http.StatusNotImplemented, "SINKS_DISABLED", "Sink manager is not configured.")
		return
	}
	result, err := s.sinks.DeliverOnce(r.Context(), sinkID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "SINK_DELIVERY_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) replaySink(w http.ResponseWriter, r *http.Request, sinkID string) {
	if s.sinks == nil {
		writeError(w, http.StatusNotImplemented, "SINKS_DISABLED", "Sink manager is not configured.")
		return
	}
	var body replaySinkBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	result, err := s.sinks.Replay(r.Context(), sinkspkg.ReplayRequest{SinkID: sinkID, FromExclusive: body.FromExclusive, Limit: body.Limit, ReplayID: body.ReplayID})
	if err != nil {
		writeError(w, http.StatusBadGateway, "SINK_REPLAY_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, result)
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
	return NewWithAllServices(orch, sched, ad, workload.New(log), secrets.New(log, []byte("01234567890123456789012345678901")), nil, connection.New(log), offers.New(log), ociresolver.NewStaticResolver(nil)), log.Close, nil
}

var _ fs.FS = web.Static()
