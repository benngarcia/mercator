package conformance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/httpapi"
)

func TestTrialClientAcceptsStructuredReadinessMetadata(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			_, _ = w.Write([]byte(`{"status":`))
			return
		}
		response := httpapi.HealthReady200JSONResponse{Status: "starting"}
		if attempt > 2 {
			response = httpapi.HealthReady200JSONResponse{
				Status:                "ready",
				StorageEpoch:          "single-scope-v1",
				ApiEpoch:              "single-scope-v2",
				SupportedClientEpochs: []string{"workspace-client-v1", "single-scope-client-v2"},
				CompatibilityFeatures: []string{"legacy_workspace_selectors"},
			}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	client := trialClient{baseURL: server.URL, client: server.Client()}
	if err := client.ready(ctx); err != nil {
		t.Fatalf("ready() error = %v", err)
	}
	if requests.Load() != 3 {
		t.Fatalf("readiness requests = %d, want malformed, non-ready, then ready", requests.Load())
	}
}
