package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/benngarcia/mercator/internal/workspace"
)

func TestWorkspaceHTTPFlowPersistsListsAndArchives(t *testing.T) {
	handler, closeHandler, err := HandlerForSQLite(
		t.Context(),
		"file:"+filepath.Join(t.TempDir(), "mercator.db"),
		nil,
		WithBearerAuth("test-token"),
	)
	if err != nil {
		t.Fatalf("open handler: %v", err)
	}
	t.Cleanup(func() { _ = closeHandler() })

	created := requestWorkspace(t, handler, http.MethodPost, "/v1/workspaces", `{"display_name":"Production"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create workspace status = %d body=%s", created.Code, created.Body.String())
	}
	var createResponse struct {
		Workspace workspace.Workspace `json:"workspace"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createResponse); err != nil {
		t.Fatalf("decode created workspace: %v", err)
	}
	if createResponse.Workspace.ID == "" || createResponse.Workspace.DisplayName != "Production" || createResponse.Workspace.CreatedBy != "bearer" {
		t.Fatalf("created workspace = %+v", createResponse.Workspace)
	}

	listed := requestWorkspace(t, handler, http.MethodGet, "/v1/workspaces", "")
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(createResponse.Workspace.ID)) {
		t.Fatalf("list workspaces status = %d body=%s", listed.Code, listed.Body.String())
	}

	archived := requestWorkspace(t, handler, http.MethodPost, "/v1/workspaces/"+createResponse.Workspace.ID+"/archive", "")
	if archived.Code != http.StatusOK {
		t.Fatalf("archive workspace status = %d body=%s", archived.Code, archived.Body.String())
	}
	active := requestWorkspace(t, handler, http.MethodGet, "/v1/workspaces", "")
	if active.Code != http.StatusOK || bytes.Contains(active.Body.Bytes(), []byte(createResponse.Workspace.ID)) {
		t.Fatalf("active workspaces status = %d body=%s", active.Code, active.Body.String())
	}
	withArchived := requestWorkspace(t, handler, http.MethodGet, "/v1/workspaces?include_archived=true", "")
	if withArchived.Code != http.StatusOK || !bytes.Contains(withArchived.Body.Bytes(), []byte(createResponse.Workspace.ID)) {
		t.Fatalf("archived workspaces status = %d body=%s", withArchived.Code, withArchived.Body.String())
	}

	archivedCreate := requestWorkspace(t, handler, http.MethodPost, "/v1/connections", `{"workspace_id":"`+createResponse.Workspace.ID+`","connection_id":"conn_archived","adapter_type":"fake"}`)
	if archivedCreate.Code != http.StatusBadRequest || !bytes.Contains(archivedCreate.Body.Bytes(), []byte(`"code":"WORKSPACE_ARCHIVED"`)) {
		t.Fatalf("archived create status = %d body=%s", archivedCreate.Code, archivedCreate.Body.String())
	}

	unknownCreate := requestWorkspace(t, handler, http.MethodPost, "/v1/connections", `{"workspace_id":"ws_unknown","connection_id":"conn_unknown","adapter_type":"fake"}`)
	if unknownCreate.Code != http.StatusBadRequest || !bytes.Contains(unknownCreate.Body.Bytes(), []byte(`"code":"WORKSPACE_NOT_FOUND"`)) {
		t.Fatalf("unknown create status = %d body=%s", unknownCreate.Code, unknownCreate.Body.String())
	}
}

func requestWorkspace(t *testing.T, handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Idempotency-Key", "test-idempotency-key")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
