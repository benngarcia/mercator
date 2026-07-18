package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/ociresolver"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/benngarcia/mercator/internal/workload"
)

func testKey32() []byte { return []byte("0123456789abcdef0123456789abcdef") }

// fakeVerifier is a test double for connectionVerifier whose VerifyConnection
// outcome is controlled by the test.
type fakeVerifier struct {
	err error
}

func (f *fakeVerifier) VerifyConnection(_ context.Context, _, _ string) error {
	return f.err
}

// newHTTPTestServerWithConns builds a test server with a real connection.Service,
// secret store, and optional credential resolver and verifier wired in.
func newHTTPTestServerWithConns(t *testing.T, store credential.SecretStore, resolver *credential.Resolver, extraOpts ...Option) http.Handler {
	t.Helper()
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	now := time.Now().UTC()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_1", now)}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	sched := scheduler.New()
	orch := orchestrator.New(log, sched, ad)
	staticResolver := ociresolver.NewStaticResolver(nil)
	svc := connection.New(log)
	options := []Option{WithSecretStore(store)}
	if resolver != nil {
		options = append(options, WithCredentialResolver(resolver))
	}
	options = append(options, extraOpts...)
	return New(Deps{Orchestrator: orch, Scheduler: sched, Adapter: ad, Workloads: workload.New(log), Connections: svc, Resolver: staticResolver}, options...)
}

// TestConnectionsListReflectsRegistry asserts that GET /v1/connections returns
// connections that were registered via POST /v1/connections (the registry is
// now the sole source of truth for the list — offer-derivation has been removed).
func TestConnectionsListReflectsRegistry(t *testing.T) {
	store := credential.NewMemoryStore()
	handler := newHTTPTestServerWithConns(t, store, nil)

	// Create a connection via the API.
	body := mustMarshal(t, createConnectionBody{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_registry",
		AdapterType:  "fake",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k-registry")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusCreated {
		t.Fatalf("create connection expected 201/202, got %d body=%s", rec.Code, rec.Body.String())
	}

	// List and confirm the created connection appears.
	req = httptest.NewRequest(http.MethodGet, "/v1/connections?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list connections expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp connectionListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var got *connection.Record
	for i := range resp.Connections {
		if resp.Connections[i].ID == "conn_registry" {
			got = &resp.Connections[i]
		}
	}
	if got == nil {
		t.Fatalf("listConnections should include conn_registry; got %+v", resp.Connections)
	}
	if got.AdapterType != "fake" {
		t.Errorf("adapter_type = %q, want fake", got.AdapterType)
	}
}

// TestAuthorizeConnectionMarksAuthorized asserts that a successful
// POST /v1/connections/{id}:authorize returns 200 with the record's
// authorized field set to true.
func TestAuthorizeConnectionMarksAuthorized(t *testing.T) {
	store := credential.NewMemoryStore()
	verifier := &fakeVerifier{err: nil} // verify always succeeds
	handler := newHTTPTestServerWithConns(t, store, nil, WithVerifier(verifier))

	// Create an unauthorized connection.
	body := mustMarshal(t, createConnectionBody{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_auth",
		AdapterType:  "fake",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k-auth-create")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusCreated {
		t.Fatalf("create connection expected 201/202, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Authorize it.
	req = httptest.NewRequest(http.MethodPost, "/v1/connections/conn_auth:authorize?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp connectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode authorize response: %v", err)
	}
	if !resp.Connection.Authorized {
		t.Errorf("authorize response: Authorized = false, want true")
	}
}

// TestAuthorizeConnectionVerifyFailureStaysUnauthorized asserts that when the
// verifier returns an error the authorize endpoint responds non-2xx (502)
// carrying the adapter's real error text, and a subsequent GET shows the
// connection is still unauthorized.
func TestAuthorizeConnectionVerifyFailureStaysUnauthorized(t *testing.T) {
	store := credential.NewMemoryStore()
	verifier := &fakeVerifier{err: errors.New("dial timeout")}
	handler := newHTTPTestServerWithConns(t, store, nil, WithVerifier(verifier))

	// Create an unauthorized connection.
	body := mustMarshal(t, createConnectionBody{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_noauth",
		AdapterType:  "fake",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k-noauth-create")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusCreated {
		t.Fatalf("create connection expected 201/202, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Attempt to authorize — verifier will fail.
	req = httptest.NewRequest(http.MethodPost, "/v1/connections/conn_noauth:authorize?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Fatalf("failed verify should return non-2xx, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "dial timeout") {
		t.Errorf("verify failure must carry the adapter's real error text, got %s", rec.Body.String())
	}

	// Follow-up GET confirms the connection is still unauthorized.
	req = httptest.NewRequest(http.MethodGet, "/v1/connections?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp connectionListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	for _, c := range resp.Connections {
		if c.ID == "conn_noauth" && c.Authorized {
			t.Error("conn_noauth should remain unauthorized after failed verify")
		}
	}
}

// TestCreateConnectionStoresSecretOutOfBand asserts that posting a mercator-source
// connection with a secret stores the secret encrypted out-of-band and never
// echoes it in the response body.
func TestCreateConnectionStoresSecretOutOfBand(t *testing.T) {
	store := credential.NewMemoryStore()
	resolver := credential.NewResolver(nil, store, testKey32())
	handler := newHTTPTestServerWithConns(t, store, resolver)

	body := mustMarshal(t, createConnectionBody{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_rp",
		AdapterType:  "runpod",
		Credential:   credential.Credential{Source: "mercator"},
		Secret:       "rp_live_key",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted && rec.Code != http.StatusCreated {
		t.Fatalf("expected 201/202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "rp_live_key") {
		t.Fatal("response must not echo the secret")
	}
	// Decode the response body and verify that credential.ref is set correctly.
	var resp connectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	got := resp.Connection
	if got.Credential.Ref != "conn_rp" {
		t.Errorf("credential ref: got %q, want %q (credential ref must be set to connection id)", got.Credential.Ref, "conn_rp")
	}
	// Secret is retrievable (encrypted) and decrypts to the original.
	blob, err := store.Get(context.Background(), "ws_1", "conn_rp")
	if err != nil {
		t.Fatalf("secret not stored: %v", err)
	}
	plain, err := credential.Open(credential.DeriveSealKey(testKey32()), blob)
	if err != nil {
		t.Fatalf("decrypt stored secret: %v", err)
	}
	if string(plain) != "rp_live_key" {
		t.Fatalf("stored secret wrong: %q", string(plain))
	}
}

// TestSecretNeverLeavesTheServer is the credential-material audit: after a
// mercator-source connection is created with a secret, the plaintext must not
// appear in any API read path (connection list, offers, adapters, OpenAPI) nor
// anywhere in the event log — including private-visibility events, which sink
// exports may carry.
func TestSecretNeverLeavesTheServer(t *testing.T) {
	const secret = "rp_live_key_audit_canary"
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	now := time.Now().UTC()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{httpOffer("off_1", now)}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	sched := scheduler.New()
	orch := orchestrator.New(log, sched, ad)
	svc := connection.New(log)
	store := credential.NewMemoryStore()
	resolver := credential.NewResolver(nil, store, testKey32())
	handler := New(Deps{
		Orchestrator: orch, Scheduler: sched, Adapter: ad,
		Workloads: workload.New(log), Connections: svc,
		Resolver: ociresolver.NewStaticResolver(nil),
	}, WithSecretStore(store), WithCredentialResolver(resolver), WithVerifier(&fakeVerifier{}))

	body := mustMarshal(t, createConnectionBody{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_audit",
		AdapterType:  "runpod",
		Credential:   credential.Credential{Source: "mercator"},
		Secret:       secret,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k-audit")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Authorize so the authorization event exists too.
	req = httptest.NewRequest(http.MethodPost, "/v1/connections/conn_audit:authorize?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	for _, path := range []string{
		"/v1/connections?workspace_id=ws_1",
		"/v1/adapters",
		"/v1/offers?workspace_id=ws_1",
		"/openapi.json",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("GET %s leaks the credential plaintext", path)
		}
	}

	events, err := log.ReadAll(context.Background(), 0, 1000, eventlog.EventFilter{})
	if err != nil {
		t.Fatalf("read all events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected connection events in the log")
	}
	for _, ev := range events {
		if strings.Contains(string(ev.Data), secret) {
			t.Errorf("event %s (%s) contains the credential plaintext", ev.ID, ev.Type)
		}
	}

	// The stored blob itself is ciphertext, not the plaintext.
	blob, err := store.Get(context.Background(), "ws_1", "conn_audit")
	if err != nil {
		t.Fatalf("stored blob: %v", err)
	}
	if strings.Contains(string(blob), secret) {
		t.Error("stored blob contains the plaintext")
	}
}

// TestCreateConnectionNoMasterKeyReturnsError asserts that posting a
// mercator-source connection on a server without a master key returns 400
// SECRET_STORE_DISABLED.
func TestCreateConnectionNoMasterKeyReturnsError(t *testing.T) {
	store := credential.NewMemoryStore()
	// Resolver built with nil key — no master key.
	resolver := credential.NewResolver(nil, store, nil)
	handler := newHTTPTestServerWithConns(t, store, resolver)

	body := mustMarshal(t, createConnectionBody{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_rp2",
		AdapterType:  "runpod",
		Credential:   credential.Credential{Source: "mercator"},
		Secret:       "rp_live_key",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k2")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "SECRET_STORE_DISABLED") {
		t.Fatalf("expected SECRET_STORE_DISABLED code, got %s", rec.Body.String())
	}
}

// TestDeleteConnectionRemovesRecordAndSecret: DELETE hides the connection
// from the list and removes the sealed blob; deleting an unknown id is 404.
func TestDeleteConnectionRemovesRecordAndSecret(t *testing.T) {
	store := credential.NewMemoryStore()
	resolver := credential.NewResolver(nil, store, testKey32())
	handler := newHTTPTestServerWithConns(t, store, resolver)

	body := mustMarshal(t, createConnectionBody{
		WorkspaceID:  "ws_1",
		ConnectionID: "conn_gone",
		AdapterType:  "runpod",
		Credential:   credential.Credential{Source: "mercator"},
		Secret:       "rp_delete_me",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k-del")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/v1/connections/conn_gone?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/connections?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "conn_gone") {
		t.Fatalf("deleted connection still listed: %s", rec.Body.String())
	}

	if _, err := store.Get(context.Background(), "ws_1", "conn_gone"); err == nil {
		t.Fatal("sealed blob must be removed on delete")
	}

	req = httptest.NewRequest(http.MethodDelete, "/v1/connections/conn_ghost?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete unknown: expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}
