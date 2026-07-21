package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunRejectsInvalidServerSecretKeyBeforeOpeningStorage(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr string
	}{
		{name: "malformed", key: "definitely-not-a-key", wantErr: "must be hex or base64"},
		{name: "too short", key: "abcd", wantErr: "must decode to at least 32 bytes"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			original := stdlog.Writer()
			stdlog.SetOutput(&logs)
			t.Cleanup(func() { stdlog.SetOutput(original) })

			code := run(context.Background(), []string{"mercator", "serve"}, map[string]string{
				"MERCATOR_API_TOKEN":  "operator-token",
				"MERCATOR_SECRET_KEY": test.key,
			}, &bytes.Buffer{}, &bytes.Buffer{})

			if code != 1 {
				t.Fatalf("run() = %d, want 1", code)
			}
			if !strings.Contains(logs.String(), test.wantErr) {
				t.Fatalf("logs = %q, want %q", logs.String(), test.wantErr)
			}
		})
	}
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

func TestRunDelegatesVerifyConnectionToTheManagedHarness(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"mercator", "verify", "connection", "--adapter", "docker", "--json"}, map[string]string{}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("run returned %d, want invalid-argument exit 2; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "image") {
		t.Fatalf("stderr = %q, want missing image diagnostic", stderr.String())
	}
}

func TestRunClosesRuntimeWhenOIDCDiscoveryFails(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "discovery unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(issuer.Close)

	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	code := run(context.Background(), []string{"mercator", "serve"}, map[string]string{
		"MERCATOR_ADDR":                "127.0.0.1:0",
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
		t.Fatalf("database retained %d tables after run returned; runtime storage is still open", tables)
	}
}
