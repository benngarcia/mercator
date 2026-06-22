package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/web"
)

// uiBuilt reports whether `bun run build` has populated the embedded UI (i.e.
// static/index.html exists). When false (a Go-only checkout that has not run
// the `ui` task) the UI-serving assertions that need real content are skipped,
// while the routing/precedence assertions still run.
func uiBuilt() bool {
	file, err := web.Static().Open("index.html")
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}

// TestSPAFallbackServesIndex asserts the SPA fallback: any unmatched non-API
// GET path is handled by serveUI (serving index.html with no-cache), not a mux
// 404. Client-side routes like /runs/{id} must reach the SPA.
func TestSPAFallbackServesIndex(t *testing.T) {
	handler := newHTTPTestServer(t)

	for _, path := range []string{"/", "/runs", "/runs/run_anything", "/preview", "/offers"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if !uiBuilt() {
			// Unbuilt: serveUI degrades to a 500 with a build hint rather than a
			// generic 404, proving the path still routed to the SPA fallback.
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("%s unbuilt expected 500 build hint, got %d body=%s", path, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "UI_NOT_AVAILABLE") {
				t.Fatalf("%s unbuilt expected UI_NOT_AVAILABLE, got %s", path, rec.Body.String())
			}
			continue
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d body=%s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `id="root"`) {
			t.Fatalf("%s expected SPA shell with #root, got %s", path, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s expected Cache-Control no-cache, got %q", path, cc)
		}
	}
}

// TestAssetsRouteIsRegistered asserts /assets/* routes to the embedded asset
// file server (not the SPA fallback). With no build present the artifact is
// missing, so a 404 from the file server is the expected outcome — crucially a
// plain-text file-server 404, not the JSON UI_NOT_AVAILABLE error the SPA
// fallback would emit.
func TestAssetsRouteIsRegistered(t *testing.T) {
	handler := newHTTPTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/assets/main-deadbeef.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
		t.Fatalf("assets expected 200 or 404 from file server, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "UI_NOT_AVAILABLE") {
		t.Fatalf("assets must not fall back to the SPA handler: %s", rec.Body.String())
	}
}

// TestImmutableCacheHeaderOnServe asserts the cache policy: a successfully
// served (200) asset carries the immutable Cache-Control header, while a 404
// does not get cached. Exercised via the middleware directly since the embedded
// asset tree is empty until `bun run build` runs.
func TestImmutableCacheHeaderOnServe(t *testing.T) {
	ok := immutableCache(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("asset"))
	}))
	rec := httptest.NewRecorder()
	ok.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/main-deadbeef.js", nil))
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("served asset expected immutable cache header, got %q", cc)
	}

	missing := immutableCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	rec = httptest.NewRecorder()
	missing.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if cc := rec.Header().Get("Cache-Control"); cc != "" {
		t.Fatalf("missing asset should not be cached, got %q", cc)
	}
}

// TestAPIRoutesUnaffectedBySPAFallback asserts the more-specific API patterns
// still win over the catch-all SPA fallback under Go 1.22+ mux precedence.
func TestAPIRoutesUnaffectedBySPAFallback(t *testing.T) {
	handler := newHTTPTestServer(t)
	body := mustMarshal(t, createRunBody{RunID: "run_ui", Workload: httpRevision()})
	req := httptest.NewRequest(http.MethodPost, "/v1/runs", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "idem_ui")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create run expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	for _, target := range []string{
		"/v1/runs?workspace_id=ws_1",
		"/v1/runs/run_ui/events?workspace_id=ws_1",
		"/v1/runs/run_ui/decision?workspace_id=ws_1",
		"/v1/connections?workspace_id=ws_1",
		"/v1/offers?workspace_id=ws_1",
		"/openapi.json",
		"/health/live",
		"/health/ready",
	} {
		req = httptest.NewRequest(http.MethodGet, target, nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s expected 200, got %d body=%s", target, rec.Code, rec.Body.String())
		}
	}
}
