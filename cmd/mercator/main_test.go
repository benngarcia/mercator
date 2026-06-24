package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
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

func TestBrokerServesRegisteredDockerConnection(t *testing.T) {
	deps, ok := buildServerDeps(map[string]string{
		"MERCATOR_ADAPTER":     "docker",
		"MERCATOR_DOCKER_ARCH": "amd64",
		"MERCATOR_SQLITE_DSN":  "file:" + t.Name() + "?mode=memory&cache=shared",
	})
	if !ok {
		t.Fatal("expected docker server deps")
	}
	defer func() {
		if err := deps.close(); err != nil {
			t.Fatalf("close deps: %v", err)
		}
	}()

	offers, err := deps.broker.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 1 || offers[0].AdapterType != "docker" || offers[0].ConnectionID == "" {
		t.Fatalf("expected one docker offer from the registered connection, got %+v", offers)
	}

	conns, err := deps.conns.List(context.Background(), "ws_1")
	if err != nil {
		t.Fatalf("list conns: %v", err)
	}
	if len(conns) != 1 || !conns[0].Authorized {
		t.Fatalf("expected one authorized registered connection, got %+v", conns)
	}
}
