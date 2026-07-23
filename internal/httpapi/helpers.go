package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
	sinkspkg "github.com/benngarcia/mercator/internal/sinks"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workload"
	"github.com/benngarcia/mercator/internal/workspace"
	"github.com/benngarcia/mercator/web"
)

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
	writeJSON(w, status, apiError(code, message))
}

func apiError(code, message string) ErrorResponse {
	return ErrorResponse{Code: code, Message: strings.TrimSpace(message)}
}

func apiErrorWithDetails(code, message string, details []domain.Violation) ErrorResponse {
	return ErrorResponse{Code: code, Message: strings.TrimSpace(message), Details: details}
}

// writeInternalError logs the underlying error server-side and returns a
// generic message to the client, so internal state (file paths, SQL fragments,
// adapter internals) never leaks through 5xx response bodies.
func writeInternalError(w http.ResponseWriter, status int, code string, err error) {
	writeJSON(w, status, internalAPIError(status, code, err))
}

func internalAPIError(status int, code string, err error) ErrorResponse {
	log.Printf("httpapi: %d %s: %v", status, code, err)
	return apiError(code, "Internal error; see server logs for detail.")
}

type workspaceError struct {
	Forbidden bool
	Response  ErrorResponse
}

// resolveWorkspace resolves the explicit workspace ID from the request body or
// query. Workspace IDs partition durable event history; server configuration
// never supplies or authorizes one on the caller's behalf.
func (s *Server) resolveWorkspace(_ context.Context, bodyWorkspaceID, queryWorkspaceID string) (string, *workspaceError) {
	workspaceID := bodyWorkspaceID
	if workspaceID == "" {
		workspaceID = queryWorkspaceID
	}
	if workspaceID == "" {
		return "", &workspaceError{Response: apiError("WORKSPACE_ID_REQUIRED", "workspace_id is required.")}
	}
	return workspaceID, nil
}

func (s *Server) requiredWorkspace(ctx context.Context, queryWorkspaceID string) (string, *workspaceError) {
	return s.resolveWorkspace(ctx, "", queryWorkspaceID)
}

// resolveImageFn adapts the server's OCI resolver into the orchestrator's
// ResolveImage hook. It returns nil when no resolver is configured, in which
// case images are stored/launched as submitted.
func (s *Server) resolveImageFn() orchestrator.ResolveImageFunc {
	if s.resolver == nil {
		return nil
	}
	return func(ctx context.Context, image, platform string) (string, string, error) {
		resolved, err := s.resolver.Resolve(ctx, ociresolver.ResolveRequest{Image: image, Platform: platform})
		if err != nil {
			return "", "", err
		}
		if resolved.Image == "" {
			resolved.Image = image
		}
		if resolved.Platform == "" {
			resolved.Platform = platform
		}
		return resolved.Image, resolved.Platform, nil
	}
}

func runLinks(workspaceID, runID string) map[string]string {
	query := "?workspace_id=" + workspaceID
	base := "/v1/runs/" + runID
	return map[string]string{
		"self":     base + query,
		"events":   base + "/events" + query,
		"decision": base + "/decision" + query,
		"refresh":  base + "/refresh" + query,
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

func workspaceAPIError(err error) (ErrorResponse, bool) {
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		return apiError("WORKSPACE_NOT_FOUND", "Workspace not found."), true
	case errors.Is(err, workspace.ErrArchived):
		return apiError("WORKSPACE_ARCHIVED", "Workspace is archived."), true
	default:
		return ErrorResponse{}, false
	}
}

// HandlerForSQLite builds a fully-wired handler over a SQLite event log with
// the fake adapter serving the given offers. Used for evaluation and tests.
func HandlerForSQLite(ctx context.Context, dsn string, offer []domain.OfferSnapshot, options ...Option) (http.Handler, func() error, error) {
	storage, err := sqlitestore.Open(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	log := storage.EventLog()
	workspaces := storage.Workspaces()
	ad := fake.New(fake.WithOffers(offer), fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded))
	sched := scheduler.New()
	// Synthetic-digest resolution lets the minimal create path
	// (POST /v1/runs {"image":"busybox"}) resolve an arbitrary tag to a
	// deterministic digest with no network, keeping fake mode end-to-end
	// exercisable without a pre-pinned image. Fictional images cannot be read
	// for their platform, so the sandbox assumes the platform its own offers
	// advertise.
	resolver := ociresolver.NewStaticResolver(nil,
		ociresolver.WithSyntheticDigests(),
		ociresolver.WithAssumedPlatform(offeredPlatform(offer)),
	)
	handler := New(Deps{
		Orchestrator: orchestrator.New(log, sched, ad),
		Offers:       singleProviderOffers{provider: ad},
		Workloads:    workload.New(log),
		Sinks:        sinkspkg.NewManager(log, map[string]sinkspkg.Sink{"audit": sinkspkg.DiscardSink{}}),
		Connections:  connection.New(log),
		Resolver:     resolver,
		Workspaces:   workspaces,
		Events:       log,
	}, options...)
	return handler, storage.Close, nil
}

// offeredPlatform reports the platform the fake sandbox's offers advertise, so
// a synthetic image resolves to something those offers can actually accept.
func offeredPlatform(offers []domain.OfferSnapshot) string {
	for _, offer := range offers {
		if platform := offer.Platform.String(); platform != "" {
			return platform
		}
	}
	return ""
}

var _ fs.FS = web.Static()
