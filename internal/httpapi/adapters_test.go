package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/credential"
)

// TestListAdaptersServesManifests asserts GET /v1/adapters returns the wired
// manifests verbatim.
func TestListAdaptersServesManifests(t *testing.T) {
	manifests := []adapter.Manifest{{
		Type:        "stub",
		DisplayName: "Stub",
		Logo:        "stub",
		Description: "A stub provider.",
		Credential:  adapter.CredentialSpec{Required: true, Label: "API key"},
		ConfigFields: []adapter.ConfigField{
			{Name: "region", Label: "Region", Type: "string", Help: "Provider region."},
		},
		SetupSteps: []adapter.SetupStep{
			{Text: "Create an account.", URL: "https://example.test"},
			{Text: "Copy the API key."},
		},
	}}
	handler := newHTTPTestServerWithConns(t, credential.NewMemoryStore(), nil,
		WithAdapterManifests(func() []adapter.Manifest { return manifests }))

	req := httptest.NewRequest(http.MethodGet, "/v1/adapters", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp adapterListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Adapters) != 1 {
		t.Fatalf("expected 1 adapter, got %d", len(resp.Adapters))
	}
	got := resp.Adapters[0]
	if got.Type != "stub" || got.DisplayName != "Stub" {
		t.Fatalf("unexpected manifest identity: %+v", got)
	}
	if len(got.SetupSteps) != 2 || got.SetupSteps[0].URL != "https://example.test" {
		t.Fatalf("setup steps did not round-trip: %+v", got.SetupSteps)
	}
	if len(got.ConfigFields) != 1 || got.ConfigFields[0].Name != "region" {
		t.Fatalf("config fields did not round-trip: %+v", got.ConfigFields)
	}
}

// TestListAdaptersEmptyWithoutWiring asserts the endpoint degrades to an empty
// list when no manifests are wired (never a 500 or nil).
func TestListAdaptersEmptyWithoutWiring(t *testing.T) {
	handler := newHTTPTestServerWithConns(t, credential.NewMemoryStore(), nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/adapters", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "{\"adapters\":[]}\n" {
		t.Fatalf("expected empty adapters envelope, got %s", body)
	}
}

// TestListAdaptersBehindAuthGate asserts the new route sits behind the same
// /v1 bearer gate as every other API route.
func TestListAdaptersBehindAuthGate(t *testing.T) {
	handler := newHTTPTestServerWithConns(t, credential.NewMemoryStore(), nil,
		WithBearerAuth("test-token", []string{"ws_1"}),
		WithAdapterManifests(func() []adapter.Manifest { return nil }))

	req := httptest.NewRequest(http.MethodGet, "/v1/adapters", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/adapters", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with bearer, got %d body=%s", rec.Code, rec.Body.String())
	}
}
