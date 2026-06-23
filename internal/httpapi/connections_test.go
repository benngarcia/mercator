package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/benngarcia/mercator/internal/connection"
)

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
