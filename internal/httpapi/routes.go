package httpapi

import (
	"net/http"

	"github.com/benngarcia/mercator/web"
)

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
	HandlerWithOptions(s, StdHTTPServerOptions{
		BaseRouter: s.mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		},
	})
}

func (s *Server) HealthLive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) HealthReady(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) GetOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(openAPIJSON)
}

var _ ServerInterface = (*Server)(nil)
