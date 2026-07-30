package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestListRunsPaginatesByOpaqueCursor(t *testing.T) {
	handler := newHTTPTestServer(t)
	for _, runID := range []string{"run_c", "run_a", "run_b"} {
		body := mustMarshal(t, CreateRunRequest{RunId: runID, Workload: httpRevision()})
		request := httptest.NewRequest(http.MethodPost, "/v1/runs?workspace_id=ws_1", bytes.NewReader(body))
		request.Header.Set("Idempotency-Key", "create:"+runID)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code < 200 || response.Code >= 300 {
			t.Fatalf("create %s: status %d body=%s", runID, response.Code, response.Body.String())
		}
	}

	first := listRunPage(t, handler, url.Values{"workspace_id": {"ws_1"}, "limit": {"2"}})
	if len(first.Runs) != 2 || first.Runs[0].ID != "run_a" || first.Runs[1].ID != "run_b" {
		t.Fatalf("first page = %+v, want run_a and run_b", first.Runs)
	}
	if first.NextCursor != "run_b" {
		t.Fatalf("first next_cursor = %q, want run_b", first.NextCursor)
	}

	second := listRunPage(t, handler, url.Values{
		"workspace_id": {"ws_1"},
		"limit":        {"2"},
		"cursor":       {first.NextCursor},
	})
	if len(second.Runs) != 1 || second.Runs[0].ID != "run_c" || second.NextCursor != "" {
		t.Fatalf("second page = %+v cursor %q, want terminal run_c page", second.Runs, second.NextCursor)
	}
}

func TestListRunsRejectsPageAboveMaximum(t *testing.T) {
	handler := newHTTPTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/v1/runs?workspace_id=ws_1&limit=101", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", response.Code, response.Body.String())
	}
}

func listRunPage(t *testing.T, handler http.Handler, query url.Values) RunListResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/runs?"+query.Encode(), nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list Run page: status %d body=%s", response.Code, response.Body.String())
	}
	var page RunListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode Run page: %v", err)
	}
	return page
}
