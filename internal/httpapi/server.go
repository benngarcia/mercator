package httpapi

import (
	"context"
	"net/http"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/reporting"
	"github.com/benngarcia/mercator/internal/scheduler"
	sinkspkg "github.com/benngarcia/mercator/internal/sinks"
	"github.com/benngarcia/mercator/internal/workload"
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
// POST /v1/runs/{id}/report ingest endpoint.
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
