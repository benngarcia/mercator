package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunDelegatesVerifyToTheConformanceRunner(t *testing.T) {
	var stdout, stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"mercator", "verify"}, map[string]string{}, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("run() = %d, want 2", exitCode)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("--spec")) {
		t.Fatalf("stderr = %q, want verify spec diagnostic", stderr.String())
	}
}

func TestRunPrintsVerifyHelpWithoutAnAPIBaseURL(t *testing.T) {
	var stdout, stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"mercator", "help", "verify"}, map[string]string{}, &stdout, &stderr)

	if exitCode != 0 || !bytes.Contains(stdout.Bytes(), []byte("mercator verify --spec FILE")) {
		t.Fatalf("run() = %d, stdout = %s, stderr = %s", exitCode, stdout.String(), stderr.String())
	}
}

func TestRunDelegatesJSONCLICommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/runs" || request.URL.Query().Get("workspace_id") != "ws_1" {
			t.Fatalf("request = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer cli-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runs":[]}`))
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"mercator", "run", "list", "--workspace-id", "ws_1"}, map[string]string{
		"MERCATOR_API_URL":   server.URL,
		"MERCATOR_API_TOKEN": "cli-token",
	}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("run() = %d, stderr = %s", exitCode, stderr.String())
	}
	var response map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("stdout = %q: %v", stdout.String(), err)
	}
}

func TestServeRejectsInvalidMasterKeyBeforeOpeningStorage(t *testing.T) {
	exitCode := run(context.Background(), []string{"mercator", "serve"}, map[string]string{
		"MERCATOR_ADDR":       "127.0.0.1:0",
		"MERCATOR_API_TOKEN":  "operator-token",
		"MERCATOR_SECRET_KEY": "invalid",
	}, &bytes.Buffer{}, &bytes.Buffer{})

	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
}

func TestServeClosesStorageWhenOIDCDiscoveryFails(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "discovery unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(issuer.Close)
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"

	exitCode := run(context.Background(), []string{"mercator", "serve"}, map[string]string{
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

	if exitCode != 1 {
		t.Fatalf("run() = %d, want 1", exitCode)
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open database after run: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var tables int
	if err := database.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		t.Fatalf("inspect database after run: %v", err)
	}
	if tables != 0 {
		t.Fatalf("database retained %d tables after run returned", tables)
	}
}
