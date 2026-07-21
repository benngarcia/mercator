package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

type principalContextKey struct{}

type principal struct {
	Subject string
}

// requestActor marshals the request principal into the event-envelope actor
// recorded on human-command facts: {"subject": <email or "bearer">}. Nil when
// auth is disabled entirely (no principal to record).
func requestActor(ctx context.Context) json.RawMessage {
	subject, ok := requestPrincipal(ctx)
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(map[string]string{"subject": subject})
	if err != nil {
		return nil
	}
	return encoded
}

func requestPrincipal(ctx context.Context) (string, bool) {
	actor, ok := ctx.Value(principalContextKey{}).(principal)
	return actor.Subject, ok && actor.Subject != ""
}

func requirePrincipal(ctx context.Context) (string, *ErrorResponse) {
	subject, ok := requestPrincipal(ctx)
	if !ok {
		response := apiError("UNAUTHORIZED", "An authenticated principal is required.")
		return "", &response
	}
	return subject, nil
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
// downgrading to cookie auth. Every principal kind carries the same
// instance-wide authority; they differ only in their audited subject.
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
