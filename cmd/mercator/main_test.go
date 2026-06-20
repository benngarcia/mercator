package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bengarcia/mercator/internal/adapter"
)

func TestRunDelegatesJSONCLICommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runs" || r.URL.Query().Get("workspace_id") != "ws_1" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer cli-token" {
			t.Fatalf("missing bearer token header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[]}`))
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"mercator", "run", "list", "--workspace-id", "ws_1"}, map[string]string{
		"MERCATOR_API_URL":   server.URL,
		"MERCATOR_API_TOKEN": "cli-token",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run returned %d stderr=%s", code, stderr.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout was not json: %q: %v", stdout.String(), err)
	}
}

func TestRuntimeAdapterCanSelectDocker(t *testing.T) {
	ad := runtimeAdapter(map[string]string{"MERCATOR_ADAPTER": "docker"})
	if ad == nil {
		t.Fatal("expected docker runtime adapter")
	}
	offers, err := ad.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list docker offers: %v", err)
	}
	if len(offers) != 1 || offers[0].AdapterType != "docker" {
		t.Fatalf("unexpected docker offers: %+v", offers)
	}
}

func TestFakeOffersAreOptIn(t *testing.T) {
	if got := fakeOffers(map[string]string{}); got != nil {
		t.Fatalf("expected no fake offers by default, got %+v", got)
	}
	got := fakeOffers(map[string]string{"MERCATOR_FAKE_OFFER": "1"})
	if len(got) != 1 || got[0].AdapterType != "fake" || got[0].ConnectionID == "" {
		t.Fatalf("unexpected fake offers: %+v", got)
	}
}
