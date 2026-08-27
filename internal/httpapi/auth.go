package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type principalContextKey struct{}

type principal struct {
	Subject string
}

// requestActor marshals the request principal into the event-envelope actor
// recorded on human-command facts: {"subject": <email or "bearer">}. Nil when
// auth is disabled entirely (no principal to record).
func requestActor(ctx context.Context) json.RawMessage {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(map[string]string{"subject": actor.Subject})
	if err != nil {
		return nil
	}
	return encoded
}

func requestPrincipal(ctx context.Context) (principal, bool) {
	actor, ok := ctx.Value(principalContextKey{}).(principal)
	return actor, ok && actor.Subject != ""
}

func requirePrincipal(ctx context.Context) (string, *ErrorResponse) {
	actor, ok := requestPrincipal(ctx)
	if !ok {
		response := apiError("UNAUTHORIZED", "An authenticated principal is required.")
		return "", &response
	}
	return actor.Subject, nil
}

// maxRequestBodyBytes bounds request bodies server-wide. The largest legitimate
// payloads are well under 1 MiB.
const maxRequestBodyBytes = 1 << 20

// isRunReportPath reports whether the request is the run-report endpoint, which
// is exempted from the operator-token gate because it authenticates with a
// per-run token (handled by the report handler itself, added in a later task).
// The check is intentionally narrow: POST method, path under /v1/runs/, suffix
// exactly /report — so it cannot accidentally exempt actions like /cancel.
func isRunReportPath(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		strings.HasPrefix(r.URL.Path, "/v1/runs/") &&
		strings.HasSuffix(r.URL.Path, "/report")
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/v1/console/events" {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		w.Header().Set("Cache-Control", "no-cache, no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		w = flushingResponseWriter{ResponseWriter: w}
	}
	// An operation this listener does not serve answers what a path this
	// deployment does not have answers, and answers it before authentication,
	// so a probe on the public address cannot tell an administrative route from
	// a misspelled one.
	if s.admin != nil && s.admin.unroutedHere(r) {
		http.NotFound(w, r)
		return
	}
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
	if strings.HasPrefix(r.URL.Path, "/v1/") && s.bridgeWorkspaceSelector(w, r) {
		return
	}
	s.mux.ServeHTTP(w, r)
}

var legacyWorkspaceBridgeRequests atomic.Uint64

// bridgeWorkspaceSelector is the time-bounded v0.6 transport bridge. It
// removes only positions the previous HTTP contract defined, leaving
// application metadata and environment opaque. The single-scope domain and
// storage never see the retired selector.
func (s *Server) bridgeWorkspaceSelector(w http.ResponseWriter, r *http.Request) bool {
	used := false
	query := r.URL.Query()
	for key := range r.URL.Query() {
		if !isWorkspaceSelector(key) {
			continue
		}
		if key != "workspace_id" || !legacyWorkspaceQueryPath(r.Method, r.URL.Path) {
			writeError(w, http.StatusBadRequest, "REMOVED_WORKSPACE_SELECTOR", "Mercator no longer accepts a workspace selector; address the deployment directly.")
			return true
		}
		used = true
		if isRunReportPath(r) {
			s.bridgeLegacyReportToken(r, query.Get(key))
		}
		query.Del(key)
	}
	r.URL.RawQuery = query.Encode()
	if r.Body == nil {
		s.recordLegacyWorkspaceBridge(w, r, used)
		return false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "Request body exceeds the 1 MiB limit.")
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Request body could not be read.")
		}
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(bytes.TrimSpace(body)) == 0 {
		s.recordLegacyWorkspaceBridge(w, r, used)
		return false
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(body, &value) == nil {
		bridged, allowed := stripLegacyWorkspaceSelectors(r.Method, r.URL.Path, value)
		if !allowed {
			writeError(w, http.StatusBadRequest, "REMOVED_WORKSPACE_SELECTOR", "Mercator no longer accepts a workspace selector; address the deployment directly.")
			return true
		}
		if bridged {
			used = true
			body, _ = json.Marshal(value)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	s.recordLegacyWorkspaceBridge(w, r, used)
	return false
}

func stripLegacyWorkspaceSelectors(method, path string, value map[string]json.RawMessage) (bool, bool) {
	used := false
	for key := range value {
		if !isWorkspaceSelector(key) {
			continue
		}
		if key != "workspace_id" || !legacyWorkspaceBodyPath(method, path, "top") {
			return false, false
		}
		delete(value, key)
		used = true
	}
	for key, encoded := range value {
		if key != "workload" && key != "revision" {
			continue
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(encoded, &nested) != nil {
			continue
		}
		changed := false
		for nestedKey := range nested {
			if !isWorkspaceSelector(nestedKey) {
				continue
			}
			if nestedKey != "workspace_id" || !legacyWorkspaceBodyPath(method, path, key) {
				return false, false
			}
			delete(nested, nestedKey)
			changed = true
			used = true
		}
		if changed {
			value[key], _ = json.Marshal(nested)
		}
	}
	return used, true
}

func legacyWorkspaceQueryPath(method, path string) bool {
	if method == http.MethodGet && (path == "/v1/console/events" || path == "/v1/offers" || path == "/v1/connections" || path == "/v1/runs") {
		return true
	}
	if method == http.MethodPost && (path == "/v1/runs" || path == "/v1/connections" || path == "/v1/placements:preview") {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/"), "/")
	if len(parts) == 2 && parts[0] == "runs" {
		return method == http.MethodGet
	}
	if len(parts) == 3 && parts[0] == "runs" {
		return (method == http.MethodGet && (parts[2] == "wait" || parts[2] == "events" || parts[2] == "decision")) ||
			(method == http.MethodPost && (parts[2] == "refresh" || parts[2] == "cancel" || parts[2] == "report"))
	}
	if len(parts) == 2 && parts[0] == "connections" {
		return method == http.MethodDelete
	}
	if len(parts) == 3 && parts[0] == "connections" && parts[2] == "authorize" {
		return method == http.MethodPost
	}
	if len(parts) == 3 && parts[0] == "workloads" && parts[2] == "revisions" {
		return method == http.MethodGet || method == http.MethodPost
	}
	return len(parts) == 4 && parts[0] == "workloads" && parts[2] == "revisions" && method == http.MethodGet
}

func legacyWorkspaceBodyPath(method, path, position string) bool {
	if method != http.MethodPost {
		return false
	}
	switch position {
	case "top":
		return path == "/v1/runs" || path == "/v1/connections" || path == "/v1/workloads" || path == "/v1/placements:preview"
	case "workload":
		return path == "/v1/runs" || path == "/v1/placements:preview"
	case "revision":
		return strings.HasPrefix(path, "/v1/workloads/") && strings.HasSuffix(path, "/revisions")
	}
	return false
}

func (s *Server) bridgeLegacyReportToken(r *http.Request, workspaceID string) {
	if s.reportSigner == nil || workspaceID == "" {
		return
	}
	runID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/runs/"), "/report")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.reportSigner.VerifyLegacy(workspaceID, runID, token) {
		r.Header.Set("Authorization", "Bearer "+s.reportSigner.Token(runID))
	}
}

func (s *Server) recordLegacyWorkspaceBridge(w http.ResponseWriter, r *http.Request, used bool) {
	if !used {
		return
	}
	w.Header().Set("Deprecation", "true")
	count := legacyWorkspaceBridgeRequests.Add(1)
	if count&(count-1) == 0 {
		log.Printf("httpapi: legacy workspace selector bridge used count=%d method=%s path=%s", count, r.Method, r.URL.Path)
	}
}

func isWorkspaceSelector(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	return key == "workspace" || key == "workspace_id" || key == "workspaceid"
}

type flushingResponseWriter struct {
	http.ResponseWriter
}

func (w flushingResponseWriter) Write(data []byte) (int, error) {
	written, err := w.ResponseWriter.Write(data)
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	return written, err
}

func (w flushingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// authenticate resolves the request's principal: the machine bearer token, a
// CLI token minted by `mercator login`, or (when webauth is mounted) a
// signed-in human session. A presented bearer credential must verify as one of
// the two token kinds — a wrong token fails outright rather than silently
// downgrading to cookie auth. The machine token authenticates the deployment
// itself and is scoped to nothing narrower; the other two authenticate a human
// who reaches this deployment.
func (s *Server) authenticate(r *http.Request) (principal, bool) {
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == "" {
			return principal{}, false
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.security.Token)) == 1 {
			return principal{Subject: "bearer"}, true
		}
		if s.webauth != nil {
			if email, ok := s.webauth.VerifyCLIToken(token); ok {
				return principal{Subject: email}, true
			}
		}
		return principal{}, false
	}
	if s.webauth != nil {
		if email, ok := s.webauth.SessionEmail(r); ok {
			return principal{Subject: email}, true
		}
	}
	return principal{}, false
}
