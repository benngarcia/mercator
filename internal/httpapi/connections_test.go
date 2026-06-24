package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
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

// newHTTPTestServerWithConns builds a test server with a real connection.Service,
// secret store, and optional credential resolver wired in.
func newHTTPTestServerWithConns(t *testing.T, store credential.SecretStore, resolver *credential.Resolver) http.Handler {
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
	return NewWithAllServices(orch, sched, ad, workload.New(log), nil, svc, nil, staticResolver, options...)
}

// TestConnectionsListReflectsOfferSources asserts that the connections list
// surfaces the connection an offer came from. Every OfferSnapshot is stamped
// with the connection_id (and adapter_type) it was discovered through, so a
// configured adapter's connection appears on the connections surface even
// before connection management exists — the offer is the source of truth.
func TestConnectionsListReflectsOfferSources(t *testing.T) {
	handler := newHTTPTestServer(t) // fake adapter, offers carry conn_1 / fake

	req := httptest.NewRequest(http.MethodGet, "/v1/connections?workspace_id=ws_1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Connections []connection.Record `json:"connections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var got *connection.Record
	for i := range resp.Connections {
		if resp.Connections[i].ID == "conn_1" {
			got = &resp.Connections[i]
		}
	}
	if got == nil {
		t.Fatalf("connections should include conn_1 (the offer's connection); got %+v", resp.Connections)
	}
	if got.AdapterType != "fake" {
		t.Errorf("adapter_type = %q, want fake", got.AdapterType)
	}
	if !got.Authorized {
		t.Error("a connection actively serving offers should read as authorized")
	}
	if got.WorkspaceID != "ws_1" {
		t.Errorf("workspace_id = %q, want ws_1", got.WorkspaceID)
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
		WorkspaceID: "ws_1",
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
	// Secret is retrievable (encrypted) and decrypts to the original.
	blob, err := store.Get(context.Background(), "ws_1", "conn_rp")
	if err != nil {
		t.Fatalf("secret not stored: %v", err)
	}
	plain, err := credential.Open(testKey32(), blob)
	if err != nil {
		t.Fatalf("decrypt stored secret: %v", err)
	}
	if string(plain) != "rp_live_key" {
		t.Fatalf("stored secret wrong: %q", string(plain))
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
