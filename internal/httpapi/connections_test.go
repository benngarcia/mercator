package httpapi

import (
	"bytes"
	"context"
	"database/sql"
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
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
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

// newHTTPTestServerWithConns builds a test server with a real connection.Service.
func newHTTPTestServerWithConns(t *testing.T, extraOpts ...Option) http.Handler {
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
	return New(Deps{Orchestrator: orch, Scheduler: sched, Offers: singleProviderOffers{provider: ad}, Workloads: workload.New(log), Connections: svc, Resolver: staticResolver}, extraOpts...)
}

// TestConnectionsListReflectsRegistry asserts that GET /v1/connections returns
// connections that were registered via POST /v1/connections (the registry is
// now the sole source of truth for the list — offer-derivation has been removed).
func TestConnectionsListReflectsRegistry(t *testing.T) {
	handler := newHTTPTestServerWithConns(t)

	// Create a connection via the API.
	body := mustMarshal(t, CreateConnectionRequest{
		WorkspaceId:  "ws_1",
		ConnectionId: "conn_registry",
		AdapterType:  "fake",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k-registry")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create connection expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertResponseOmitsCredential(t, rec.Body.Bytes(), "connection")

	// List and confirm the created connection appears.
	req = httptest.NewRequest(http.MethodGet, "/v1/connections?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list connections expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertListedConnectionOmitsCredential(t, rec.Body.Bytes(), "conn_registry")
	var resp ConnectionListResponse
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

func assertResponseOmitsCredential(t *testing.T, body []byte, field string) {
	t.Helper()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	assertRecordOmitsCredential(t, response[field])
}

func assertListedConnectionOmitsCredential(t *testing.T, body []byte, connectionID string) {
	t.Helper()
	var response struct {
		Connections []json.RawMessage `json:"connections"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode connection list: %v", err)
	}
	for _, record := range response.Connections {
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(record, &identity); err != nil {
			t.Fatalf("decode connection identity: %v", err)
		}
		if identity.ID == connectionID {
			assertRecordOmitsCredential(t, record)
			return
		}
	}
	t.Fatalf("connection %q absent from response", connectionID)
}

func assertRecordOmitsCredential(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode connection record: %v", err)
	}
	if credential, present := record["credential"]; present {
		t.Fatalf("credential-free connection serialized credential %s", credential)
	}
}

// TestAuthorizeConnectionMarksAuthorized asserts that a successful
// POST /v1/connections/{id}/authorize returns 200 with the record's
// authorized field set to true.
func TestAuthorizeConnectionMarksAuthorized(t *testing.T) {
	verifier := &fakeVerifier{err: nil} // verify always succeeds
	handler := newHTTPTestServerWithConns(t, WithVerifier(verifier))

	// Create an unauthorized connection.
	body := mustMarshal(t, CreateConnectionRequest{
		WorkspaceId:  "ws_1",
		ConnectionId: "conn_auth",
		AdapterType:  "fake",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k-auth-create")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create connection expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Authorize it.
	req = httptest.NewRequest(http.MethodPost, "/v1/connections/conn_auth/authorize?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp ConnectionResponse
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
	verifier := &fakeVerifier{err: errors.New("dial timeout")}
	handler := newHTTPTestServerWithConns(t, WithVerifier(verifier))

	// Create an unauthorized connection.
	body := mustMarshal(t, CreateConnectionRequest{
		WorkspaceId:  "ws_1",
		ConnectionId: "conn_noauth",
		AdapterType:  "fake",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k-noauth-create")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create connection expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Attempt to authorize — verifier will fail.
	req = httptest.NewRequest(http.MethodPost, "/v1/connections/conn_noauth/authorize?workspace_id=ws_1", nil)
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
	var resp ConnectionListResponse
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
	server := newAtomicCredentialServer(t, testKey32())
	handler := server.handler

	body := mustMarshal(t, CreateConnectionRequest{
		WorkspaceId:  "ws_1",
		ConnectionId: "conn_rp",
		AdapterType:  "runpod",
		Credential:   credential.Credential{Source: "mercator"},
		Secret:       "rp_live_key",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "rp_live_key") {
		t.Fatal("response must not echo the secret")
	}
	// Decode the response body and verify that credential.ref is set correctly.
	var resp ConnectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	got := resp.Connection
	if got.Credential.Ref != "conn_rp" {
		t.Errorf("credential ref: got %q, want %q (credential ref must be set to connection id)", got.Credential.Ref, "conn_rp")
	}
	// Secret is retrievable (encrypted) and decrypts to the original.
	blob, err := server.store.Get(context.Background(), "ws_1", "conn_rp")
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

type atomicCredentialServer struct {
	handler  http.Handler
	db       *sql.DB
	log      *eventlog.SQLiteEventLog
	service  *connection.Service
	store    *credential.SQLiteStore
	resolver *credential.Resolver
	storage  *sqlitestore.Storage
}

func newAtomicCredentialServer(t *testing.T, masterKey []byte, options ...Option) atomicCredentialServer {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	storage, err := sqlitestore.New(t.Context(), db)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	log := storage.EventLog()
	store := storage.CredentialStore()
	resolver := credential.NewResolver(nil, store, masterKey)
	connections, err := storage.Connections(resolver)
	if err != nil {
		t.Fatalf("open connection storage: %v", err)
	}
	service := connection.NewWithCredentials(connections)
	ad := fake.New()
	sched := scheduler.New()
	handler := New(Deps{
		Orchestrator: orchestrator.New(log, sched, ad),
		Scheduler:    sched,
		Offers:       singleProviderOffers{provider: ad},
		Workloads:    workload.New(log),
		Connections:  service,
		Resolver:     ociresolver.NewStaticResolver(nil),
	}, options...)
	return atomicCredentialServer{handler: handler, db: db, log: log, service: service, store: store, resolver: resolver, storage: storage}
}

func (s atomicCredentialServer) handlerWithMasterKey(t *testing.T, masterKey []byte) http.Handler {
	t.Helper()
	resolver := credential.NewResolver(nil, s.store, masterKey)
	connections, err := s.storage.Connections(resolver)
	if err != nil {
		t.Fatalf("open connection storage: %v", err)
	}
	service := connection.NewWithCredentials(connections)
	ad := fake.New()
	sched := scheduler.New()
	return New(Deps{
		Orchestrator: orchestrator.New(s.log, sched, ad),
		Scheduler:    sched,
		Offers:       singleProviderOffers{provider: ad},
		Workloads:    workload.New(s.log),
		Connections:  service,
		Resolver:     ociresolver.NewStaticResolver(nil),
	})
}

func createMercatorConnection(t *testing.T, handler http.Handler, config map[string]string, secret string) *httptest.ResponseRecorder {
	t.Helper()
	body := mustMarshal(t, CreateConnectionRequest{
		WorkspaceId:  "ws_1",
		ConnectionId: "conn_atomic",
		AdapterType:  "runpod",
		Config:       config,
		Credential:   credential.Credential{Source: credential.SourceMercator},
		Secret:       secret,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "create-conn-atomic")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func TestCreateConnectionConflictPreservesStoredCredential(t *testing.T) {
	server := newAtomicCredentialServer(t, testKey32())
	first := createMercatorConnection(t, server.handler, map[string]string{"region": "us-east"}, "original-secret")
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: status=%d body=%s", first.Code, first.Body.String())
	}

	conflict := createMercatorConnection(t, server.handler, map[string]string{"region": "us-west"}, "replacement-secret")

	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting create: status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	secret, err := server.resolver.Resolve(t.Context(), "ws_1", credential.Credential{Source: credential.SourceMercator, Ref: "conn_atomic"})
	if err != nil {
		t.Fatalf("resolve original credential: %v", err)
	}
	if secret != "original-secret" {
		t.Fatalf("stored credential = %q, want original-secret", secret)
	}
}

func TestCreateConnectionReplayDoesNotRotateCredential(t *testing.T) {
	server := newAtomicCredentialServer(t, testKey32())
	if response := createMercatorConnection(t, server.handler, nil, "original-secret"); response.Code != http.StatusCreated {
		t.Fatalf("first create: status=%d body=%s", response.Code, response.Body.String())
	}

	replay := createMercatorConnection(t, server.handler, nil, "replacement-secret")

	if replay.Code != http.StatusCreated {
		t.Fatalf("replay: status=%d body=%s", replay.Code, replay.Body.String())
	}
	secret, err := server.resolver.Resolve(t.Context(), "ws_1", credential.Credential{Source: credential.SourceMercator, Ref: "conn_atomic"})
	if err != nil {
		t.Fatalf("resolve original credential: %v", err)
	}
	if secret != "original-secret" {
		t.Fatalf("stored credential = %q, want original-secret", secret)
	}
}

func TestCreateConnectionReplayAndConflictDoNotRequireTheSealKey(t *testing.T) {
	server := newAtomicCredentialServer(t, testKey32())
	if response := createMercatorConnection(t, server.handler, nil, "original-secret"); response.Code != http.StatusCreated {
		t.Fatalf("first create: status=%d body=%s", response.Code, response.Body.String())
	}
	handlerWithoutKey := server.handlerWithMasterKey(t, nil)

	replay := createMercatorConnection(t, handlerWithoutKey, nil, "original-secret")
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay without seal key: status=%d body=%s", replay.Code, replay.Body.String())
	}

	conflict := createMercatorConnection(t, handlerWithoutKey, map[string]string{"region": "us-west"}, "replacement-secret")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict without seal key: status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestCreateConnectionEventFailureRollsBackCredential(t *testing.T) {
	server := newAtomicCredentialServer(t, testKey32())
	if _, err := server.db.ExecContext(t.Context(), `
		CREATE TRIGGER fail_connection_event
		BEFORE INSERT ON events
		WHEN NEW.event_type = 'compute.connection.created.v1'
		BEGIN SELECT RAISE(FAIL, 'event write failed'); END`); err != nil {
		t.Fatalf("create event failure trigger: %v", err)
	}

	response := createMercatorConnection(t, server.handler, nil, "must-rollback")

	if response.Code < 400 {
		t.Fatalf("create status=%d body=%s, want failure", response.Code, response.Body.String())
	}
	_, err := server.resolver.Resolve(t.Context(), "ws_1", credential.Credential{Source: credential.SourceMercator, Ref: "conn_atomic"})
	if !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("resolve error = %v, want credential.ErrNotFound", err)
	}
}

func TestCreateConnectionCredentialFailureRollsBackEvent(t *testing.T) {
	server := newAtomicCredentialServer(t, testKey32())
	if _, err := server.db.ExecContext(t.Context(), `
		CREATE TRIGGER fail_connection_secret
		BEFORE INSERT ON connection_secret
		BEGIN SELECT RAISE(FAIL, 'credential write failed'); END`); err != nil {
		t.Fatalf("create credential failure trigger: %v", err)
	}

	response := createMercatorConnection(t, server.handler, nil, "must-rollback")

	if response.Code < 400 {
		t.Fatalf("create status=%d body=%s, want failure", response.Code, response.Body.String())
	}
	_, err := server.service.Get(t.Context(), "ws_1", "conn_atomic")
	if !errors.Is(err, connection.ErrNotFound) {
		t.Fatalf("connection lookup error = %v, want connection.ErrNotFound", err)
	}
}

// TestSecretNeverLeavesTheServer is the credential-material audit: after a
// mercator-source connection is created with a secret, the plaintext must not
// appear in any API read path (connection list, offers, adapters, OpenAPI) nor
// anywhere in the event log — including private-visibility events, which sink
// exports may carry.
func TestSecretNeverLeavesTheServer(t *testing.T) {
	const secret = "rp_live_key_audit_canary"
	server := newAtomicCredentialServer(t, testKey32(), WithVerifier(&fakeVerifier{}))
	handler := server.handler

	body := mustMarshal(t, CreateConnectionRequest{
		WorkspaceId:  "ws_1",
		ConnectionId: "conn_audit",
		AdapterType:  "runpod",
		Credential:   credential.Credential{Source: "mercator"},
		Secret:       secret,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k-audit")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Authorize so the authorization event exists too.
	req = httptest.NewRequest(http.MethodPost, "/v1/connections/conn_audit/authorize?workspace_id=ws_1", nil)
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

	events, err := server.log.ReadAll(context.Background(), 0, 1000, eventlog.EventFilter{})
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
	blob, err := server.store.Get(context.Background(), "ws_1", "conn_audit")
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
	server := newAtomicCredentialServer(t, nil)
	handler := server.handler

	body := mustMarshal(t, CreateConnectionRequest{
		WorkspaceId:  "ws_1",
		ConnectionId: "conn_rp2",
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
	server := newAtomicCredentialServer(t, testKey32())
	handler := server.handler

	body := mustMarshal(t, CreateConnectionRequest{
		WorkspaceId:  "ws_1",
		ConnectionId: "conn_gone",
		AdapterType:  "runpod",
		Credential:   credential.Credential{Source: "mercator"},
		Secret:       "rp_delete_me",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/connections", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "k-del")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d body=%s", rec.Code, rec.Body.String())
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

	if _, err := server.store.Get(context.Background(), "ws_1", "conn_gone"); err == nil {
		t.Fatal("sealed blob must be removed on delete")
	}

	req = httptest.NewRequest(http.MethodDelete, "/v1/connections/conn_ghost?workspace_id=ws_1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete unknown: expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}
