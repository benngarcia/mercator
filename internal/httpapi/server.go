package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/authz"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/offers"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
	sinkspkg "github.com/benngarcia/mercator/internal/sinks"
	"github.com/benngarcia/mercator/internal/workload"
	"github.com/benngarcia/mercator/web"
)

type Server struct {
	mux       *http.ServeMux
	orch      *orchestrator.Orchestrator
	scheduler scheduler.Scheduler
	adapter   adapter.Adapter
	workloads *workload.Service
	sinks     *sinkspkg.Manager
	conns     *connection.Service
	offers    *offers.Service
	resolver  interface {
		Resolve(context.Context, ociresolver.ResolveRequest) (ociresolver.ResolvedImage, error)
	}
	security securityConfig
}

type securityConfig struct {
	Token      string
	Workspaces []string
}

type Option func(*Server)

func WithBearerAuth(token string, workspaces []string) Option {
	return func(s *Server) {
		s.security = securityConfig{Token: token, Workspaces: append([]string(nil), workspaces...)}
	}
}

type principalContextKey struct{}

type createRunBody struct {
	WorkspaceID        string                  `json:"workspace_id,omitempty"`
	RunID              string                  `json:"run_id,omitempty"`
	WorkloadID         string                  `json:"workload_id,omitempty"`
	WorkloadRevisionID string                  `json:"workload_revision_id,omitempty"`
	Workload           domain.WorkloadRevision `json:"workload"`
	// Top-level image shorthand. When no full workload (or revision id) is
	// supplied, the server synthesizes workload.spec.containers[0] from these.
	Image string                       `json:"image,omitempty"`
	Args  []string                     `json:"args,omitempty"`
	Env   map[string]domain.EnvBinding `json:"env,omitempty"`
}

// hasWorkloadSpec reports whether the body carries an explicit full workload
// spec (at least one container). The shorthand image form is only expanded when
// no explicit spec is present.
func (b createRunBody) hasWorkloadSpec() bool {
	return len(b.Workload.Spec.Containers) > 0
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
	// RunID is the convenience top-level run identifier, returned alongside the
	// full run{} record on every run response for envelope consistency. Metadata
	// is reserved for a future per-response metadata object.
	RunID     string            `json:"run_id"`
	Run       domain.RunRecord  `json:"run"`
	Metadata  map[string]any    `json:"metadata,omitempty"`
	Links     map[string]string `json:"links,omitempty"`
	Duplicate bool              `json:"duplicate,omitempty"`
}

// newRunResponse builds a run response envelope with the top-level run_id
// derived from the record, keeping run_id and run.id consistent.
func newRunResponse(workspaceID string, record domain.RunRecord, duplicate bool) runResponse {
	return runResponse{
		RunID:     record.ID,
		Run:       record,
		Links:     runLinks(workspaceID, record.ID),
		Duplicate: duplicate,
	}
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

func New(orch *orchestrator.Orchestrator, sched scheduler.Scheduler, ad adapter.Adapter, options ...Option) http.Handler {
	s := &Server{mux: http.NewServeMux(), orch: orch, scheduler: sched, adapter: ad}
	for _, option := range options {
		option(s)
	}
	s.routes()
	return s
}

func NewWithServices(orch *orchestrator.Orchestrator, sched scheduler.Scheduler, ad adapter.Adapter, workloads *workload.Service, resolver interface {
	Resolve(context.Context, ociresolver.ResolveRequest) (ociresolver.ResolvedImage, error)
}, options ...Option) http.Handler {
	return NewWithAllServices(orch, sched, ad, workloads, nil, nil, nil, resolver, options...)
}

func NewWithAllServices(orch *orchestrator.Orchestrator, sched scheduler.Scheduler, ad adapter.Adapter, workloads *workload.Service, sinkManager *sinkspkg.Manager, conns *connection.Service, offerService *offers.Service, resolver interface {
	Resolve(context.Context, ociresolver.ResolveRequest) (ociresolver.ResolvedImage, error)
}, options ...Option) http.Handler {
	s := &Server{mux: http.NewServeMux(), orch: orch, scheduler: sched, adapter: ad, workloads: workloads, sinks: sinkManager, conns: conns, offers: offerService, resolver: resolver}
	for _, option := range options {
		option(s)
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.security.Token != "" && strings.HasPrefix(r.URL.Path, "/v1/") {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Bearer token is required.")
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == "" || token != s.security.Token {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Bearer token is required.")
			return
		}
		principal := authz.Principal{Subject: "bearer", WorkspaceIDs: append([]string(nil), s.security.Workspaces...)}
		r = r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal))
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	// SPA fallback: any unmatched non-API GET serves index.html. The more
	// specific patterns below (/v1/, /health/, /openapi.json, /assets/) win
	// under the Go 1.22+ ServeMux precedence rules, so this only catches
	// client-side routes like /runs, /runs/{id}, /offers, etc.
	s.mux.HandleFunc("GET /", s.serveUI)
	s.mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	// Content-hashed, immutable build artifacts. Hashing in the filename makes
	// them safe to cache forever.
	assetServer := http.StripPrefix("/assets/", http.FileServer(http.FS(web.AssetsFS())))
	s.mux.Handle("GET /assets/", immutableCache(assetServer))
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
	s.mux.HandleFunc("GET /v1/sinks/{sink_id}", s.sinkStatus)
	s.mux.HandleFunc("POST /v1/sinks/{sink_action}", s.sinkAction)
	s.mux.HandleFunc("POST /v1/placements:preview", s.previewPlacement)
}

// serveUI is the single-page-app fallback. It serves the embedded index.html
// (with a no-cache header so clients always re-validate the entry document and
// pick up new hashed asset references after a deploy) for the root and for any
// unmatched non-API GET path, letting the client-side router own routes like
// /runs, /runs/{id}, /preview, /offers, etc. The /v1, /health, /openapi.json
// and /assets/ patterns are registered more specifically and take precedence.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	// Only non-API paths fall back to the SPA. An unmatched /v1, /health,
	// /openapi.json or /assets request is a genuine 404, not a client route.
	if isAPIPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	file, err := web.Static().Open("index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UI_NOT_AVAILABLE", "UI assets are not built; run the `ui` task (bun run build) before building the binary.")
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
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", stat.ModTime(), reader)
}

// immutableCache wraps the embedded asset file server with a long-lived
// immutable Cache-Control header. The build emits content-hashed filenames, so
// any change produces a new URL and stale caching is impossible. The header is
// injected via a ResponseWriter wrapper at WriteHeader time so it survives even
// when the wrapped http.FileServer takes its 404 path (which rewrites parts of
// the header map).
func immutableCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&immutableCacheWriter{ResponseWriter: w}, r)
	})
}

type immutableCacheWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *immutableCacheWriter) WriteHeader(status int) {
	if !w.wrote {
		w.wrote = true
		if status == http.StatusOK {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *immutableCacheWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
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
	if workspaceID == "" {
		workspaceID = r.URL.Query().Get("workspace_id")
	}
	if workspaceID == "" {
		workspaceID = s.defaultWorkspace()
	}
	if !authorizeRequestWorkspace(w, r, workspaceID) {
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
		Workload:       workloadRevision,
		ResolveImage:   s.resolveImageFn(),
	})
	if err != nil {
		if errors.Is(err, eventlog.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency key was reused with a different request hash.")
			return
		}
		writeError(w, http.StatusBadRequest, errorCode(err, "CREATE_RUN_FAILED"), err.Error())
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
		writeError(w, http.StatusInternalServerError, "RUN_NOT_FOUND", err.Error())
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

func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	workspaceID, ok := s.requiredWorkspace(w, r)
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
	if strings.HasSuffix(r.PathValue("run_ref"), ":wait") {
		s.waitRun(w, r, runID)
		return
	}
	s.writeRun(w, r, runID)
}

// waitDeadline bounds how long waitRun will long-poll for a terminal state.
// Overridable in tests.
var waitDeadline = 30 * time.Second

// waitPollInterval is the cadence at which waitRun re-drives an open run toward
// a terminal state. Overridable in tests.
var waitPollInterval = 100 * time.Millisecond

func (s *Server) waitRun(w http.ResponseWriter, r *http.Request, runID string) {
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

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := s.requiredWorkspace(w, r)
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
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	record, err := s.orch.CancelRun(r.Context(), workspaceID, runID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "CANCEL_RUN_FAILED", "Run cancellation failed.")
		return
	}
	writeJSON(w, http.StatusOK, newRunResponse(workspaceID, record, false))
}

func (s *Server) refreshRun(w http.ResponseWriter, r *http.Request, runID string) {
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

func (s *Server) runDecision(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := s.requiredWorkspace(w, r)
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
	if !authorizeRequestWorkspace(w, r, workspaceID) {
		return
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
	if r.Header.Get("Idempotency-Key") == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Mutation requests require Idempotency-Key.")
		return
	}
	var body createWorkloadBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if !authorizeRequestWorkspace(w, r, body.WorkspaceID) {
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
	if r.Header.Get("Idempotency-Key") == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Mutation requests require Idempotency-Key.")
		return
	}
	workspaceID, ok := s.requiredWorkspace(w, r)
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
	workspaceID, ok := s.requiredWorkspace(w, r)
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
	workspaceID, ok := s.requiredWorkspace(w, r)
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
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}

	// Start from the connection registry (connections created/authorized via the
	// connection service). Registry records carry real authorization state and
	// win on conflict.
	byID := map[string]connection.Record{}
	if s.conns != nil {
		records, err := s.conns.List(r.Context(), workspaceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "LIST_CONNECTIONS_FAILED", err.Error())
			return
		}
		for _, record := range records {
			byID[record.ID] = record
		}
	}

	// Derive connections from the offers visible to this workspace. Every offer
	// names the connection (and adapter) it was discovered through, so a
	// configured adapter's connection appears on this surface even before
	// connection management exists — the offer is the source of truth. A
	// connection actively serving offers reads as authorized.
	if offerList, err := s.visibleOffers(r.Context(), workspaceID); err == nil {
		for _, offer := range offerList {
			if offer.ConnectionID == "" {
				continue
			}
			if _, registered := byID[offer.ConnectionID]; registered {
				continue
			}
			byID[offer.ConnectionID] = connection.Record{
				ID:          offer.ConnectionID,
				WorkspaceID: workspaceID,
				AdapterType: offer.AdapterType,
				Authorized:  true,
			}
		}
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	records := make([]connection.Record, 0, len(ids))
	for _, id := range ids {
		records = append(records, byID[id])
	}
	writeJSON(w, http.StatusOK, connectionListResponse{Connections: records})
}

// visibleOffers returns the offers a workspace can see: the offer cache when
// populated, otherwise a live adapter query. Shared by the offers and
// connections surfaces.
func (s *Server) visibleOffers(ctx context.Context, workspaceID string) ([]domain.OfferSnapshot, error) {
	if s.offers != nil {
		records, err := s.offers.ListCached(ctx, workspaceID, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if len(records) > 0 {
			return records, nil
		}
	}
	return s.adapter.ListOffers(ctx, adapter.OfferRequest{WorkspaceID: workspaceID})
}

func (s *Server) listOffers(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := s.requiredWorkspace(w, r)
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

// isAPIPath reports whether a path belongs to the server's API/operational
// surface rather than the client-side SPA. Used to keep the SPA fallback from
// masking genuine 404s on unmatched API routes.
func isAPIPath(path string) bool {
	return strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/health") ||
		strings.HasPrefix(path, "/assets/") ||
		path == "/openapi.json"
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Code: code, Message: strings.TrimSpace(message)})
}

// defaultWorkspace returns the single concrete workspace this server is scoped
// to, if exactly one is configured (and it is not the "*" wildcard). It lets
// callers omit workspace_id on a single-tenant deployment. Empty means there is
// no unambiguous default and workspace_id must be supplied explicitly.
func (s *Server) defaultWorkspace() string {
	concrete := ""
	for _, ws := range s.security.Workspaces {
		if ws == "" || ws == "*" {
			return ""
		}
		if concrete != "" && concrete != ws {
			return ""
		}
		concrete = ws
	}
	return concrete
}

func (s *Server) requiredWorkspace(w http.ResponseWriter, r *http.Request) (string, bool) {
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		workspaceID = s.defaultWorkspace()
	}
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "WORKSPACE_ID_REQUIRED", "workspace_id query parameter is required.")
		return "", false
	}
	if !authorizeRequestWorkspace(w, r, workspaceID) {
		return "", false
	}
	return workspaceID, true
}

func authorizeRequestWorkspace(w http.ResponseWriter, r *http.Request, workspaceID string) bool {
	principal, ok := r.Context().Value(principalContextKey{}).(authz.Principal)
	if !ok {
		return true
	}
	for _, allowed := range principal.WorkspaceIDs {
		if allowed == "*" {
			return true
		}
	}
	if err := authz.New().Authorize(principal, authz.ActionRead, authz.ResourceRun, workspaceID); err != nil {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Principal is not authorized for this workspace.")
		return false
	}
	return true
}

// newRunID mints a server-generated run identifier from a uuidv7 (time-ordered,
// collision-resistant). Used when a caller omits run_id on create.
func newRunID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// NewV7 only errors if the system RNG fails; fall back to a v4.
		id = uuid.New()
	}
	return "run_" + id.String()
}

// resolveImageFn adapts the server's OCI resolver into the orchestrator's
// ResolveImage hook. It returns nil when no resolver is configured, in which
// case images are stored/launched as submitted (backward-compatible).
func (s *Server) resolveImageFn() func(context.Context, string, string) (string, error) {
	if s.resolver == nil {
		return nil
	}
	return func(ctx context.Context, image, platform string) (string, error) {
		resolved, err := s.resolver.Resolve(ctx, ociresolver.ResolveRequest{Image: image, Platform: platform})
		if err != nil {
			return "", err
		}
		if resolved.Image != "" {
			return resolved.Image, nil
		}
		return image, nil
	}
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
	return HandlerForSQLiteWithOptions(ctx, dsn, offer)
}

func HandlerForSQLiteWithOptions(ctx context.Context, dsn string, offer []domain.OfferSnapshot, options ...Option) (http.Handler, func() error, error) {
	ad := fake.New(fake.WithOffers(offer), fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded))
	return HandlerForSQLiteWithAdapter(ctx, dsn, ad, options...)
}

func HandlerForSQLiteWithAdapter(ctx context.Context, dsn string, ad adapter.Adapter, options ...Option) (http.Handler, func() error, error) {
	log, err := eventlog.OpenSQLite(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	sched := scheduler.New()
	orch := orchestrator.New(log, sched, ad)
	// Synthetic-digest resolution lets the minimal create path
	// (POST /v1/runs {"image":"busybox"}) resolve an arbitrary tag to a
	// deterministic digest with no network, keeping fake mode end-to-end
	// exercisable without a pre-pinned image.
	resolver := ociresolver.NewStaticResolver(nil, ociresolver.WithSyntheticDigests())
	return NewWithAllServices(orch, sched, ad, workload.New(log), sinkspkg.NewManager(log, map[string]sinkspkg.Sink{"audit": sinkspkg.DiscardSink{}}), connection.New(log), offers.New(log), resolver, options...), log.Close, nil
}

var _ fs.FS = web.Static()
