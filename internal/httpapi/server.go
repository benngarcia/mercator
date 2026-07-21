package httpapi

import (
	"context"
	"net/http"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/reporting"
	sinkspkg "github.com/benngarcia/mercator/internal/sinks"
	"github.com/benngarcia/mercator/internal/workload"
)

// ImageResolver resolves a tag-form image reference to a digest-pinned one.
type ImageResolver interface {
	Resolve(context.Context, ociresolver.ResolveRequest) (ociresolver.ResolvedImage, error)
}

type OfferAggregator interface {
	AggregateOffers(context.Context, adapter.OfferRequest) (broker.OfferAggregation, error)
}

// singleProviderOffers adapts the fake-mode provider used by HandlerForSQLite
// to the aggregate contract. A single provider cannot produce connection-level
// partial failures.
type singleProviderOffers struct {
	provider adapter.Provider
}

func (s singleProviderOffers) AggregateOffers(ctx context.Context, request adapter.OfferRequest) (broker.OfferAggregation, error) {
	offers, err := s.provider.ListOffers(ctx, request)
	if offers == nil {
		offers = []domain.OfferSnapshot{}
	}
	return broker.OfferAggregation{Offers: offers, Failures: broker.ConnectionErrors{}}, err
}

// Deps are the services the server routes to. Orchestrator and Offers are
// required; a nil optional service disables its endpoints. Placement preview
// and create-run intake both go through Orchestrator.
type Deps struct {
	Orchestrator *orchestrator.Orchestrator
	Offers       OfferAggregator
	Workloads    *workload.Service
	Sinks        *sinkspkg.Manager
	Connections  *connection.Service
	Resolver     ImageResolver
}

type Server struct {
	mux          *http.ServeMux
	orch         *orchestrator.Orchestrator
	offers       OfferAggregator
	workloads    *workload.Service
	sinks        *sinkspkg.Manager
	conns        *connection.Service
	resolver     ImageResolver
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
	Token string
}

type Option func(*Server)

func WithBearerAuth(token string) Option {
	return func(s *Server) {
		s.security = securityConfig{Token: token}
	}
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
		offers:    deps.Offers,
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
