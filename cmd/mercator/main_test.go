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

// TestBuildServerDepsReportingSigner verifies that buildServerDeps populates the
// signer and publicURL fields correctly.
func TestBuildServerDepsReportingSigner(t *testing.T) {
	// A 64-char hex string decodes cleanly to 32 bytes.
	hexKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

	t.Run("with_key_and_public_url_signer_enabled", func(t *testing.T) {
		deps := buildServerDeps(map[string]string{
			"MERCATOR_ADAPTER":     "docker",
			"MERCATOR_DOCKER_ARCH": "amd64",
			"MERCATOR_SQLITE_DSN":  "file:" + t.Name() + "?mode=memory&cache=shared",
			"MERCATOR_SECRET_KEY":  hexKey,
			"MERCATOR_PUBLIC_URL":  "http://127.0.0.1:8080",
		})
		defer deps.close()
		if deps.signer == nil {
			t.Fatal("expected non-nil signer when MERCATOR_SECRET_KEY is set")
		}
		if !deps.signer.Enabled() {
			t.Fatal("signer should be enabled with a key")
		}
		if deps.publicURL != "http://127.0.0.1:8080" {
			t.Fatalf("unexpected publicURL: %q", deps.publicURL)
		}
	})

	t.Run("without_public_url_signer_still_built_but_reporting_off", func(t *testing.T) {
		deps := buildServerDeps(map[string]string{
			"MERCATOR_ADAPTER":     "docker",
			"MERCATOR_DOCKER_ARCH": "amd64",
			"MERCATOR_SQLITE_DSN":  "file:" + t.Name() + "?mode=memory&cache=shared",
			"MERCATOR_SECRET_KEY":  hexKey,
			// No MERCATOR_PUBLIC_URL
		})
		defer deps.close()
		if deps.signer == nil {
			t.Fatal("expected non-nil signer when MERCATOR_SECRET_KEY is set")
		}
		if !deps.signer.Enabled() {
			t.Fatal("signer key should still be derived even without publicURL")
		}
		// Reporting is only enabled when BOTH signer.Enabled() AND publicURL != "".
		if deps.publicURL != "" {
			t.Fatalf("expected empty publicURL, got %q", deps.publicURL)
		}
	})

	t.Run("without_secret_key_signer_disabled", func(t *testing.T) {
		deps := buildServerDeps(map[string]string{
			"MERCATOR_ADAPTER":     "docker",
			"MERCATOR_DOCKER_ARCH": "amd64",
			"MERCATOR_SQLITE_DSN":  "file:" + t.Name() + "?mode=memory&cache=shared",
			// No MERCATOR_SECRET_KEY, no MERCATOR_PUBLIC_URL
		})
		defer deps.close()
		if deps.signer != nil && deps.signer.Enabled() {
			t.Fatal("signer should be disabled when no MERCATOR_SECRET_KEY is set")
		}
	})
}

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
	deps := buildServerDeps(map[string]string{
		"MERCATOR_ADAPTER":     "docker",
		"MERCATOR_DOCKER_ARCH": "amd64",
		"MERCATOR_SQLITE_DSN":  "file:" + t.Name() + "?mode=memory&cache=shared",
	})
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

	// Verify the offer is backed by the registry record: the offer's ConnectionID
	// must match the registered connection's ID.
	if offers[0].ConnectionID != conns[0].ID {
		t.Fatalf("offer is not backed by registry: offer.ConnectionID=%s, conn.ID=%s", offers[0].ConnectionID, conns[0].ID)
	}
}
