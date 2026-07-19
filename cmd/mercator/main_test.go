package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/domain"
)

func TestBuildServerDepsValidatesSecretKey(t *testing.T) {
	validBase64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	tests := []struct {
		name    string
		key     string
		wantErr string
	}{
		{name: "absent"},
		{name: "malformed", key: "definitely-not-a-key", wantErr: "MERCATOR_SECRET_KEY must be hex or base64"},
		{name: "too short", key: "abcd", wantErr: "MERCATOR_SECRET_KEY must decode to at least 32 bytes"},
		{name: "hex", key: "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"},
		{name: "base64", key: validBase64},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps, err := buildServerDeps(map[string]string{
				"MERCATOR_SQLITE_DSN": "file:" + t.Name() + "?mode=memory&cache=shared",
				"MERCATOR_SECRET_KEY": test.key,
			})
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("buildServerDeps() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildServerDeps(): %v", err)
			}
			t.Cleanup(func() {
				if err := deps.close(); err != nil {
					t.Fatalf("close dependencies: %v", err)
				}
			})
		})
	}
}

func mustBuildServerDeps(t *testing.T, values map[string]string) serverDeps {
	t.Helper()
	deps, err := buildServerDeps(values)
	if err != nil {
		t.Fatalf("buildServerDeps(): %v", err)
	}
	return deps
}

// TestBuildServerDepsReportingSigner verifies that buildServerDeps populates the
// signer and publicURL fields correctly.
func TestBuildServerDepsReportingSigner(t *testing.T) {
	// A 64-char hex string decodes cleanly to 32 bytes.
	hexKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

	t.Run("with_key_and_public_url_signer_enabled", func(t *testing.T) {
		deps := mustBuildServerDeps(t, map[string]string{
			"MERCATOR_SQLITE_DSN": "file:" + t.Name() + "?mode=memory&cache=shared",
			"MERCATOR_SECRET_KEY": hexKey,
			"MERCATOR_PUBLIC_URL": "http://127.0.0.1:8080",
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
		deps := mustBuildServerDeps(t, map[string]string{
			"MERCATOR_SQLITE_DSN": "file:" + t.Name() + "?mode=memory&cache=shared",
			"MERCATOR_SECRET_KEY": hexKey,
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
		deps := mustBuildServerDeps(t, map[string]string{
			"MERCATOR_SQLITE_DSN": "file:" + t.Name() + "?mode=memory&cache=shared",
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

func TestRunClosesServerDependenciesWhenOIDCDiscoveryFails(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "discovery unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(issuer.Close)

	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	code := run(context.Background(), []string{"mercator", "serve"}, map[string]string{
		"MERCATOR_API_TOKEN":           "operator-token",
		"MERCATOR_SQLITE_DSN":          dsn,
		"MERCATOR_OIDC_ISSUER":         issuer.URL,
		"MERCATOR_OIDC_CLIENT_ID":      "client-id",
		"MERCATOR_OIDC_CLIENT_SECRET":  "client-secret",
		"MERCATOR_OIDC_ALLOWED_DOMAIN": "example.com",
		"MERCATOR_SESSION_KEY":         "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
		"MERCATOR_PUBLIC_URL":          "https://mercator.example.com",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("run() = %d, want 1", code)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open database after run: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		t.Fatalf("inspect database after run: %v", err)
	}
	if tables != 0 {
		t.Fatalf("database retained %d tables after run returned; server dependencies are still open", tables)
	}
}

func TestBrokerStartsWithoutInventingAConnection(t *testing.T) {
	deps := mustBuildServerDeps(t, map[string]string{
		"MERCATOR_SQLITE_DSN": "file:" + t.Name() + "?mode=memory&cache=shared",
	})
	defer func() {
		if err := deps.close(); err != nil {
			t.Fatalf("close deps: %v", err)
		}
	}()

	offers, err := deps.broker.ListOffers(context.Background(), adapter.OfferRequest{WorkspaceID: "staging"})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("startup offers = %+v, want none until a connection is created and authorized", offers)
	}

	conns, err := deps.conns.List(context.Background(), "staging")
	if err != nil {
		t.Fatalf("list conns: %v", err)
	}
	if len(conns) != 0 {
		t.Fatalf("startup connections = %+v, want none", conns)
	}
}

func TestBrokerServesConnectionsCreatedThroughTheRegistry(t *testing.T) {
	deps := mustBuildServerDeps(t, map[string]string{
		"MERCATOR_SQLITE_DSN": "file:" + t.Name() + "?mode=memory&cache=shared",
	})
	defer func() {
		if err := deps.close(); err != nil {
			t.Fatalf("close deps: %v", err)
		}
	}()

	ctx := context.Background()
	if _, err := deps.conns.Create(ctx, connection.CreateRequest{
		WorkspaceID:  "staging",
		ConnectionID: "conn_docker_loopback",
		AdapterType:  "docker",
	}); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := deps.conns.UpdateAuthorization(ctx, connection.UpdateAuthorizationRequest{
		WorkspaceID:  "staging",
		ConnectionID: "conn_docker_loopback",
		Authorized:   true,
	}); err != nil {
		t.Fatalf("authorize connection: %v", err)
	}

	offers, err := deps.broker.ListOffers(ctx, adapter.OfferRequest{WorkspaceID: "staging"})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 1 || offers[0].ConnectionID != "conn_docker_loopback" {
		t.Fatalf("offers = %+v, want the authorized registry connection", offers)
	}
}

// TestBrokerRoutesEachDockerConnectionToItsOwnEndpoint guards against the
// factory memoizing a single docker adapter: a second docker connection must
// advertise its own offer identity (and thus route launches to its own
// endpoint), not relabel the first connection's.
func TestBrokerRoutesEachDockerConnectionToItsOwnEndpoint(t *testing.T) {
	deps := mustBuildServerDeps(t, map[string]string{
		"MERCATOR_SQLITE_DSN": "file:" + t.Name() + "?mode=memory&cache=shared",
	})
	defer func() {
		if err := deps.close(); err != nil {
			t.Fatalf("close deps: %v", err)
		}
	}()
	ctx := context.Background()
	if _, err := deps.conns.Create(ctx, connection.CreateRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_docker_loopback",
		AdapterType:  "docker",
		Config:       map[string]string{"arch": "arm64"},
	}); err != nil {
		t.Fatalf("create local docker connection: %v", err)
	}
	if err := deps.conns.UpdateAuthorization(ctx, connection.UpdateAuthorizationRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_docker_loopback",
		Authorized:   true,
	}); err != nil {
		t.Fatalf("authorize local docker connection: %v", err)
	}

	if _, err := deps.conns.Create(ctx, connection.CreateRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_docker_remote",
		AdapterType:  "docker",
		Config:       map[string]string{"host": "tcp://gpu-2:2375", "arch": "amd64"},
	}); err != nil {
		t.Fatalf("create second docker connection: %v", err)
	}
	if err := deps.conns.UpdateAuthorization(ctx, connection.UpdateAuthorizationRequest{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_docker_remote",
		Authorized:   true,
	}); err != nil {
		t.Fatalf("authorize second docker connection: %v", err)
	}

	offers, err := deps.broker.ListOffers(ctx, adapter.OfferRequest{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list offers: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("expected one offer per docker connection, got %+v", offers)
	}
	byConn := map[string]domain.OfferSnapshot{}
	for _, offer := range offers {
		byConn[offer.ConnectionID] = offer
	}
	if byConn["conn_docker_loopback"].ID == "" || byConn["conn_docker_remote"].ID == "" {
		t.Fatalf("expected offers for both connections, got %+v", byConn)
	}
	if byConn["conn_docker_loopback"].ID == byConn["conn_docker_remote"].ID {
		t.Fatalf("both connections advertise the same offer id %q: adapter is shared", byConn["conn_docker_loopback"].ID)
	}
	if byConn["conn_docker_remote"].ID != "offer_docker_gpu-2" {
		t.Fatalf("remote offer id = %q, want offer_docker_gpu-2 derived from its own endpoint", byConn["conn_docker_remote"].ID)
	}
	if byConn["conn_docker_loopback"].Platform.Architecture != "arm64" || byConn["conn_docker_remote"].Platform.Architecture != "amd64" {
		t.Fatalf("connection architectures = loopback:%s remote:%s, want arm64 and amd64", byConn["conn_docker_loopback"].Platform.Architecture, byConn["conn_docker_remote"].Platform.Architecture)
	}
}
