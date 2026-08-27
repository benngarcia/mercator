package conformance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTrialClientAcceptsStructuredReadinessMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready","supported_client_epochs":["workspace-client-v1","single-scope-client-v2"],"compatibility_features":["legacy_workspace_selectors"]}`))
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	client := trialClient{baseURL: server.URL, client: server.Client()}
	if err := client.ready(ctx); err != nil {
		t.Fatalf("ready() error = %v", err)
	}
}
