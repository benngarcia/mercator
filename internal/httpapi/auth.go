package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type principalContextKey struct{}

// principalKind separates the two authorities that reach this API. The instance
// credential is the deployment itself: it is how Mercator's own operator and
// its automation act, and it is scoped to nothing narrower than the process. A
// human is a subject who signed in, and a subject is only ever authorised for
// the workspaces they are a member of.
type principalKind string

const (
	principalInstance principalKind = "instance"
	principalHuman    principalKind = "human"
)

type principal struct {
	Subject string
	Kind    principalKind
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

// requestMemberScope is the subject a listing is narrowed to. A human sees the
// workspaces they belong to; the instance credential, and a deployment running
// with no authentication at all, see the whole catalog.
func requestMemberScope(ctx context.Context) string {
	actor, ok := requestPrincipal(ctx)
	if !ok || actor.Kind != principalHuman {
		return ""
	}
	return actor.Subject
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
	s.mux.ServeHTTP(w, r)
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
// itself and is scoped to nothing narrower; the other two authenticate a human,
// who reaches only the workspaces they are a member of.
func (s *Server) authenticate(r *http.Request) (principal, bool) {
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == "" {
			return principal{}, false
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.security.Token)) == 1 {
			return principal{Subject: "bearer", Kind: principalInstance}, true
		}
		if s.webauth != nil {
			if email, ok := s.webauth.VerifyCLIToken(token); ok {
				return s.humanPrincipal(email), true
			}
		}
		return principal{}, false
	}
	if s.webauth != nil {
		if email, ok := s.webauth.SessionEmail(r); ok {
			return s.humanPrincipal(email), true
		}
	}
	return principal{}, false
}

// humanPrincipal is what a signed-in email acts as. Ordinarily a human, scoped
// to the workspaces they are a member of. On a deployment whose authenticator
// can establish exactly one identity, that person is the deployment acting as
// itself and is scoped to nothing narrower: `mercator serve --dev` and the Lab
// bind loopback, mint a session for whoever is at the keyboard, and print the
// instance bearer token to their terminal, so scoping them to their memberships
// would refuse them their own console while protecting nothing they cannot
// already reach with the token they were just handed.
//
// The authenticator is asked rather than a separate option read, because the
// authenticator is what knows.
func (s *Server) humanPrincipal(email string) principal {
	if sole := s.webauth.SoleOperator(); sole != "" && email == sole {
		return principal{Subject: email, Kind: principalInstance}
	}
	return principal{Subject: email, Kind: principalHuman}
}
