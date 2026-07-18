package httpapi

import (
	"net/http"
	"net/url"

	"github.com/benngarcia/mercator/web"
)

// serveUI is the single-page-app fallback. It serves the embedded index.html
// with a no-cache header so clients always re-validate the entry document and
// pick up new hashed asset references after a deploy. It handles any
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
