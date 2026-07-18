package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/reporting"
	"github.com/benngarcia/mercator/internal/scheduler"
	sinkspkg "github.com/benngarcia/mercator/internal/sinks"
	"github.com/benngarcia/mercator/internal/workload"
	"github.com/benngarcia/mercator/web"
)

// ImageResolver resolves a tag-form image reference to a digest-pinned one.
type ImageResolver interface {
	Resolve(context.Context, ociresolver.ResolveRequest) (ociresolver.ResolvedImage, error)
}

// Deps are the services the server routes to. Orchestrator, Scheduler, and
// Adapter are required; a nil optional service disables its endpoints.
type Deps struct {
	Orchestrator *orchestrator.Orchestrator
	Scheduler    scheduler.Scheduler
	Adapter      adapter.Adapter
	Workloads    *workload.Service
	Sinks        *sinkspkg.Manager
	Connections  *connection.Service
	Resolver     ImageResolver
}

type Server struct {
	mux          *http.ServeMux
	orch         *orchestrator.Orchestrator
	scheduler    scheduler.Scheduler
	adapter      adapter.Adapter
	workloads    *workload.Service
	sinks        *sinkspkg.Manager
	conns        *connection.Service
	resolver     ImageResolver
	secretStore  credential.SecretStore
	credentials  *credential.Resolver
	verifier     connectionVerifier
	security     securityConfig
	reportSigner *reporting.Signer
	webauth      WebAuth
	manifests    func() []adapter.Manifest
}

// WebAuth is the human-login surface the server mounts at /auth/ when OIDC is
// configured: it serves the login/callback/logout/session endpoints, answers
// which signed-in human a request's session cookie belongs to, and verifies
// the bearer tokens `mercator login` mints for CLI users.
type WebAuth interface {
	http.Handler
	SessionEmail(*http.Request) (string, bool)
	VerifyCLIToken(token string) (string, bool)
}

// connectionVerifier is the narrow capability the server needs from the Broker
// to verify a connection during the authorize flow.
type connectionVerifier interface {
	VerifyConnection(ctx context.Context, workspaceID, connectionID string) error
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

// WithSecretStore wires the SecretStore the server uses to persist sealed
// connection credentials.
func WithSecretStore(store credential.SecretStore) Option {
	return func(s *Server) { s.secretStore = store }
}

// WithCredentialResolver wires the credential.Resolver the server uses to turn
// a {source, ref} credential into a plaintext secret.
func WithCredentialResolver(resolver *credential.Resolver) Option {
	return func(s *Server) { s.credentials = resolver }
}

// WithVerifier wires the connection verifier (the Broker) the server uses to
// validate a connection during the authorize flow.
func WithVerifier(verifier connectionVerifier) Option {
	return func(s *Server) { s.verifier = verifier }
}

// WithReportSigner wires the per-run token signer used to authenticate the
// POST /v1/runs/{id}:report ingest endpoint.
func WithReportSigner(signer *reporting.Signer) Option {
	return func(s *Server) { s.reportSigner = signer }
}

// WithWebAuth mounts the OIDC human-login surface. Session-cookie requests are
// then accepted by the API gate with the signed-in email as the principal
// subject, and unauthenticated console page loads are routed into /auth/login.
func WithWebAuth(auth WebAuth) Option {
	return func(s *Server) { s.webauth = auth }
}

// WithAdapterManifests wires the registered adapters' onboarding manifests
// served by GET /v1/adapters (in practice broker.Factory.Manifests).
func WithAdapterManifests(manifests func() []adapter.Manifest) Option {
	return func(s *Server) { s.manifests = manifests }
}

type principalContextKey struct{}

type principal struct {
	Subject      string
	WorkspaceIDs []string
}

func (p principal) allows(workspaceID string) bool {
	return p.Subject != "" &&
		(slices.Contains(p.WorkspaceIDs, "*") || slices.Contains(p.WorkspaceIDs, workspaceID))
}

// requestActor marshals the request's principal into the event-envelope actor
// recorded on human-command facts: {"subject": <email or "bearer">}. Nil when
// auth is disabled entirely (no principal to record).
func requestActor(r *http.Request) json.RawMessage {
	actor, ok := r.Context().Value(principalContextKey{}).(principal)
	if !ok || actor.Subject == "" {
		return nil
	}
	encoded, err := json.Marshal(map[string]string{"subject": actor.Subject})
	if err != nil {
		return nil
	}
	return encoded
}

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

type createConnectionBody struct {
	WorkspaceID  string                `json:"workspace_id"`
	ConnectionID string                `json:"connection_id"`
	AdapterType  string                `json:"adapter_type"`
	Config       map[string]string     `json:"config,omitempty"`
	Credential   credential.Credential `json:"credential"`
	// Secret is write-only: accepted on create, never echoed in any response.
	Secret string `json:"secret,omitempty"`
}

type connectionResponse struct {
	Connection connection.Record `json:"connection"`
}

type connectionListResponse struct {
	Connections []connection.Record `json:"connections"`
}

type adapterListResponse struct {
	Adapters []adapter.Manifest `json:"adapters"`
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

func New(deps Deps, options ...Option) http.Handler {
	s := &Server{
		mux:       http.NewServeMux(),
		orch:      deps.Orchestrator,
		scheduler: deps.Scheduler,
		adapter:   deps.Adapter,
		workloads: deps.Workloads,
		sinks:     deps.Sinks,
		conns:     deps.Connections,
		resolver:  deps.Resolver,
	}
	for _, option := range options {
		option(s)
	}
	s.routes()
	return s
}

// isRunReportPath reports whether the request is the run-report endpoint, which
// is exempted from the operator-token gate because it authenticates with a
// per-run token (handled by the report handler itself, added in a later task).
// The check is intentionally narrow: POST method, path under /v1/runs/, suffix
// exactly :report — so it cannot accidentally exempt actions like :cancel.
func isRunReportPath(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		strings.HasPrefix(r.URL.Path, "/v1/runs/") &&
		strings.HasSuffix(r.URL.Path, ":report")
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	operatorAuthRequired := s.security.Token != "" &&
		strings.HasPrefix(r.URL.Path, "/v1/") &&
		!isRunReportPath(r)
	if operatorAuthRequired {
		actor, ok := s.authenticate(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Bearer token or signed-in session is required.")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), principalContextKey{}, actor))
	}
	// Bound every request body so no caller (operator or run-token holder) can
	// stream an unbounded payload into a JSON decoder or the event store.
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	}
	s.mux.ServeHTTP(w, r)
}

// authenticate resolves the request's principal: the machine bearer token, a
// CLI token minted by `mercator login`, or (when webauth is mounted) a
// signed-in human session. A presented bearer credential must verify as one of
// the two token kinds — a wrong token fails outright rather than silently
// downgrading to cookie auth. Every principal kind carries the same configured
// workspace grants; they differ only in their audited subject.
func (s *Server) authenticate(r *http.Request) (principal, bool) {
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == "" {
			return principal{}, false
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.security.Token)) == 1 {
			return principal{Subject: "bearer", WorkspaceIDs: slices.Clone(s.security.Workspaces)}, true
		}
		if s.webauth != nil {
			if email, ok := s.webauth.VerifyCLIToken(token); ok {
				return principal{Subject: email, WorkspaceIDs: slices.Clone(s.security.Workspaces)}, true
			}
		}
		return principal{}, false
	}
	if s.webauth != nil {
		if email, ok := s.webauth.SessionEmail(r); ok {
			return principal{Subject: email, WorkspaceIDs: slices.Clone(s.security.Workspaces)}, true
		}
	}
	return principal{}, false
}

// maxRequestBodyBytes bounds request bodies server-wide. The largest legitimate
// payloads (full workload revisions, run reports) are well under 1 MiB; the
// container env budget alone is capped at 32 KiB by capability validation.
const maxRequestBodyBytes = 1 << 20

// decodeJSONBody decodes a request body into v, writing the appropriate error
// response (413 for an over-limit body, 400 otherwise) and returning false on
// failure.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "Request body exceeds the 1 MiB limit.")
			return false
		}
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return false
	}
	return true
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
	// Human login surface. When OIDC is not configured, /auth/session still
	// answers (enabled: false) so the console can pick the token fallback
	// without probing errors; the other /auth endpoints do not exist.
	if s.webauth != nil {
		// Per-method registrations: a method-less "/auth/" subtree would
		// conflict with the SPA fallback's "GET /" under ServeMux precedence.
		s.mux.Handle("GET /auth/", s.webauth)
		s.mux.Handle("POST /auth/", s.webauth)
	} else {
		s.mux.HandleFunc("GET /auth/session", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		})
	}
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
	s.mux.HandleFunc("POST /v1/connections", s.createConnection)
	s.mux.HandleFunc("POST /v1/connections/{conn_action}", s.connectionAction)
	s.mux.HandleFunc("GET /v1/connections", s.listConnections)
	s.mux.HandleFunc("GET /v1/adapters", s.listAdapters)
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
	// With OIDC configured, the console is for signed-in humans: route
	// unauthenticated page loads into the login flow, preserving the deep link.
	if s.webauth != nil {
		if _, ok := s.webauth.SessionEmail(r); !ok {
			http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
	}
	file, err := web.Static().Open("index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UI_NOT_AVAILABLE", "UI assets are not built; run the `ui` task (bun run build) before building the binary.")
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		writeInternalError(w, http.StatusInternalServerError, "UI_NOT_AVAILABLE", err)
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

func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
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
		writeInternalError(w, http.StatusInternalServerError, "LIST_RUNS_FAILED", err)
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
	if strings.HasSuffix(runAction, ":report") {
		s.reportRun(w, r, strings.TrimSuffix(runAction, ":report"))
		return
	}
	writeError(w, http.StatusNotFound, "RUN_ACTION_NOT_FOUND", "Unknown run action.")
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request, runID string) {
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

type reportBody struct {
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
}

func (s *Server) reportRun(w http.ResponseWriter, r *http.Request, runID string) {
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
	if !decodeJSONBody(w, r, &body) {
		return
	}
	workspaceID := body.WorkspaceID
	if workspaceID == "" {
		workspaceID = body.Workload.WorkspaceID
	}
	if !s.authorizeRequestWorkspace(w, r, workspaceID) {
		return
	}
	if violations := domain.ValidateWorkloadRevision(body.Workload); len(violations) > 0 {
		writeError(w, http.StatusBadRequest, violations[0].Code, violations[0].Message)
		return
	}
	offers, err := s.adapter.ListOffers(r.Context(), adapter.OfferRequest{
		WorkspaceID: workspaceID,
		Resources:   body.Workload.Spec.Resources,
	})
	if err != nil {
		writeInternalError(w, http.StatusBadGateway, "OFFER_QUERY_FAILED", err)
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
	if !decodeJSONBody(w, r, &body) {
		return
	}
	revision, err := s.workloads.CreateRevision(r.Context(), workload.CreateRevisionRequest{WorkspaceID: workspaceID, WorkloadID: r.PathValue("workload_id"), Revision: body.Revision})
	if err != nil {
		writeError(w, http.StatusBadRequest, errorCode(err, "CREATE_REVISION_FAILED"), errorMessage(err))
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
		writeInternalError(w, http.StatusInternalServerError, "LIST_REVISIONS_FAILED", err)
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
	if !decodeJSONBody(w, r, &body) {
		return
	}
	resolved, err := s.resolver.Resolve(r.Context(), ociresolver.ResolveRequest{Image: body.Image, Platform: body.Platform})
	if err != nil {
		writeError(w, http.StatusBadRequest, "IMAGE_RESOLUTION_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resolveImageResponse{Image: resolved})
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	if s.conns == nil {
		writeError(w, http.StatusNotImplemented, "CONNECTION_SERVICE_DISABLED", "Connection service is not configured.")
		return
	}
	if r.Header.Get("Idempotency-Key") == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Mutation requests require Idempotency-Key.")
		return
	}
	var body createConnectionBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	workspaceID := body.WorkspaceID
	if workspaceID == "" {
		workspaceID = r.URL.Query().Get("workspace_id")
	}
	if workspaceID == "" {
		workspaceID = s.defaultWorkspace()
	}
	if !s.authorizeRequestWorkspace(w, r, workspaceID) {
		return
	}

	cred := body.Credential
	// For mercator-source connections with a provided secret, seal the secret
	// out-of-band and store the ciphertext in the secret store. The plaintext
	// never enters the event log or the response.
	if cred.Source == credential.SourceMercator && body.Secret != "" {
		if s.credentials == nil || s.secretStore == nil {
			writeError(w, http.StatusBadRequest, "SECRET_STORE_DISABLED", "Secret store is not configured; cannot accept mercator credentials.")
			return
		}
		blob, ok := s.credentials.Seal([]byte(body.Secret))
		if !ok {
			writeError(w, http.StatusBadRequest, "SECRET_STORE_DISABLED", "Master key is not set; cannot seal mercator credentials.")
			return
		}
		if err := s.secretStore.Put(r.Context(), workspaceID, body.ConnectionID, blob); err != nil {
			writeInternalError(w, http.StatusInternalServerError, "SECRET_STORE_FAILED", err)
			return
		}
		cred.Ref = body.ConnectionID
	}

	record, err := s.conns.Create(r.Context(), connection.CreateRequest{
		WorkspaceID:  workspaceID,
		ConnectionID: body.ConnectionID,
		AdapterType:  body.AdapterType,
		Config:       body.Config,
		Credential:   cred,
		Actor:        requestActor(r),
	})
	if err != nil {
		if errors.Is(err, eventlog.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency key was reused with a different request hash.")
			return
		}
		writeError(w, http.StatusBadRequest, errorCode(err, "CREATE_CONNECTION_FAILED"), errorMessage(err))
		return
	}
	writeJSON(w, http.StatusAccepted, connectionResponse{Connection: record})
}

func (s *Server) connectionAction(w http.ResponseWriter, r *http.Request) {
	connAction := r.PathValue("conn_action")
	if strings.HasSuffix(connAction, ":authorize") {
		s.authorizeConnection(w, r, strings.TrimSuffix(connAction, ":authorize"))
		return
	}
	writeError(w, http.StatusNotFound, "CONNECTION_ACTION_NOT_FOUND", "Unknown connection action.")
}

func (s *Server) authorizeConnection(w http.ResponseWriter, r *http.Request, id string) {
	if s.conns == nil {
		writeError(w, http.StatusNotImplemented, "CONNECTION_SERVICE_DISABLED", "Connection service is not configured.")
		return
	}
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	if s.verifier == nil {
		writeError(w, http.StatusNotImplemented, "CONNECTION_VERIFY_DISABLED", "Connection verification is not configured.")
		return
	}
	if err := s.verifier.VerifyConnection(r.Context(), workspaceID, id); err != nil {
		writeInternalError(w, http.StatusBadGateway, "CONNECTION_VERIFY_FAILED", err)
		return
	}
	if err := s.conns.UpdateAuthorization(r.Context(), connection.UpdateAuthorizationRequest{
		WorkspaceID:  workspaceID,
		ConnectionID: id,
		Authorized:   true,
		Actor:        requestActor(r),
	}); err != nil {
		writeInternalError(w, http.StatusInternalServerError, "CONNECTION_AUTHORIZE_FAILED", err)
		return
	}
	record, err := s.conns.Get(r.Context(), workspaceID, id)
	if err != nil {
		writeInternalError(w, http.StatusInternalServerError, "CONNECTION_NOT_FOUND", err)
		return
	}
	writeJSON(w, http.StatusOK, connectionResponse{Connection: record})
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	if s.conns == nil {
		writeJSON(w, http.StatusOK, connectionListResponse{Connections: []connection.Record{}})
		return
	}
	records, err := s.conns.List(r.Context(), workspaceID)
	if err != nil {
		writeInternalError(w, http.StatusInternalServerError, "LIST_CONNECTIONS_FAILED", err)
		return
	}
	if records == nil {
		records = []connection.Record{}
	}
	writeJSON(w, http.StatusOK, connectionListResponse{Connections: records})
}

// listAdapters serves the registered adapters' onboarding manifests. The list
// is static per process (registration happens once at boot) and carries no
// workspace state, so no workspace scoping applies — but it sits behind the
// same /v1 auth gate as everything else.
func (s *Server) listAdapters(w http.ResponseWriter, _ *http.Request) {
	if s.manifests == nil {
		writeJSON(w, http.StatusOK, adapterListResponse{Adapters: []adapter.Manifest{}})
		return
	}
	manifests := s.manifests()
	if manifests == nil {
		manifests = []adapter.Manifest{}
	}
	writeJSON(w, http.StatusOK, adapterListResponse{Adapters: manifests})
}

func (s *Server) listOffers(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := s.requiredWorkspace(w, r)
	if !ok {
		return
	}
	records, err := s.adapter.ListOffers(r.Context(), adapter.OfferRequest{WorkspaceID: workspaceID})
	if err != nil {
		writeInternalError(w, http.StatusBadGateway, "LIST_OFFERS_FAILED", err)
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
		writeInternalError(w, http.StatusBadGateway, "SINK_DELIVERY_FAILED", err)
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
	if !decodeJSONBody(w, r, &body) {
		return
	}
	result, err := s.sinks.Replay(r.Context(), sinkspkg.ReplayRequest{SinkID: sinkID, FromExclusive: body.FromExclusive, Limit: body.Limit, ReplayID: body.ReplayID})
	if err != nil {
		writeInternalError(w, http.StatusBadGateway, "SINK_REPLAY_FAILED", err)
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

// writeInternalError logs the underlying error server-side and returns a
// generic message to the client, so internal state (file paths, SQL fragments,
// adapter internals) never leaks through 5xx response bodies.
func writeInternalError(w http.ResponseWriter, status int, code string, err error) {
	log.Printf("httpapi: %d %s: %v", status, code, err)
	writeError(w, status, code, "Internal error; see server logs for detail.")
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
	if !s.authorizeRequestWorkspace(w, r, workspaceID) {
		return "", false
	}
	return workspaceID, true
}

func (s *Server) authorizeRequestWorkspace(w http.ResponseWriter, r *http.Request, workspaceID string) bool {
	actor, ok := r.Context().Value(principalContextKey{}).(principal)
	if !ok {
		// No principal is only legitimate when bearer auth is disabled entirely
		// (an explicit dev/embedding mode). With auth enabled, a missing
		// principal means the request somehow bypassed the token gate — deny,
		// never fail open.
		if s.security.Token == "" {
			return true
		}
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Principal is not authorized for this workspace.")
		return false
	}
	if !actor.allows(workspaceID) {
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

// errorMessage strips a coded error's "CODE: " prefix so the code isn't
// duplicated inside the response message field.
func errorMessage(err error) string {
	match := codedErrorPattern.FindStringSubmatch(err.Error())
	if len(match) == 3 {
		return match[2]
	}
	return err.Error()
}

// HandlerForSQLite builds a fully-wired handler over a SQLite event log with
// the fake adapter serving the given offers. Used for evaluation and tests.
func HandlerForSQLite(ctx context.Context, dsn string, offer []domain.OfferSnapshot, options ...Option) (http.Handler, func() error, error) {
	log, err := eventlog.OpenSQLite(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	ad := fake.New(fake.WithOffers(offer), fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded))
	sched := scheduler.New()
	// Synthetic-digest resolution lets the minimal create path
	// (POST /v1/runs {"image":"busybox"}) resolve an arbitrary tag to a
	// deterministic digest with no network, keeping fake mode end-to-end
	// exercisable without a pre-pinned image.
	handler := New(Deps{
		Orchestrator: orchestrator.New(log, sched, ad),
		Scheduler:    sched,
		Adapter:      ad,
		Workloads:    workload.New(log),
		Sinks:        sinkspkg.NewManager(log, map[string]sinkspkg.Sink{"audit": sinkspkg.DiscardSink{}}),
		Connections:  connection.New(log),
		Resolver:     ociresolver.NewStaticResolver(nil, ociresolver.WithSyntheticDigests()),
	}, options...)
	return handler, log.Close, nil
}

var _ fs.FS = web.Static()
