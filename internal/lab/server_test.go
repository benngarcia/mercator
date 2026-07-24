package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/httpapi"
	"github.com/benngarcia/mercator/internal/scenario"
)

func TestServerDrivesTheRealControlPlaneThroughLabOnlyRoutes(t *testing.T) {
	// Arrange
	server := openServerFixture(t)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	// Act
	checkpoint := postDrive(t, httpServer.URL, `{"kind":"step"}`)

	// Assert
	if checkpoint.Transitions != 1 {
		t.Fatalf("transitions = %d, want 1", checkpoint.Transitions)
	}
	response := labRequest(t, http.MethodGet, httpServer.URL+"/v1/runs?workspace_id="+WorkspaceID, nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("normal Run API = %s, want 200 OK", response.Status)
	}
	var runs httpapi.RunListResponse
	decodeResponse(t, response.Body, &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].ID != "run-producer" {
		t.Fatalf("normal Run API returned %+v", runs.Runs)
	}
}

func TestServerExportsAReplayableBundleAndWorldTruth(t *testing.T) {
	// Arrange
	server := openServerFixture(t)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	postDrive(t, httpServer.URL, `{"kind":"quiesce"}`)

	// Act
	truthResponse := labRequest(t, http.MethodGet, httpServer.URL+"/v1/lab/truth", nil)
	defer truthResponse.Body.Close()
	var truth WorldTruthSnapshot
	decodeResponse(t, truthResponse.Body, &truth)
	bundleResponse := labRequest(t, http.MethodGet, httpServer.URL+"/v1/lab/bundle", nil)
	defer bundleResponse.Body.Close()
	archive, err := io.ReadAll(bundleResponse.Body)
	if err != nil {
		t.Fatalf("read Run Bundle: %v", err)
	}

	// Assert
	if truth.At.IsZero() || len(truth.Offers) == 0 {
		t.Fatalf("world truth = %+v", truth)
	}
	replayed, err := Replay(context.Background(), archive)
	if err != nil {
		t.Fatalf("replay exported Run Bundle: %v", err)
	}
	t.Cleanup(func() { _ = replayed.Close() })
}

func TestServerRejectsUnauthenticatedLabControls(t *testing.T) {
	// Arrange
	server := openServerFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/v1/lab/status", nil)
	response := httptest.NewRecorder()

	// Act
	server.Handler().ServeHTTP(response, request)

	// Assert
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}

func TestServerRejectsRawLabTokenWithoutBearerScheme(t *testing.T) {
	// Arrange
	server := openServerFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/v1/lab/status", nil)
	request.Header.Set("Authorization", "lab-test-token")
	response := httptest.NewRecorder()

	// Act
	server.Handler().ServeHTTP(response, request)

	// Assert
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}

func TestProductionHandlerCannotMountLabControls(t *testing.T) {
	// Arrange
	handler := httpapi.New(httpapi.Deps{})
	request := httptest.NewRequest(http.MethodGet, "/v1/lab/status", nil)
	response := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(response, request)

	// Assert
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func openServerFixture(t *testing.T) *Server {
	t.Helper()
	blueprint, err := scenario.LoadBlueprint(filepath.Join(
		"..", "scenario", "scenarios", "demos", "artifact-warmth-restart.json",
	))
	if err != nil {
		t.Fatalf("load Blueprint: %v", err)
	}
	tape, samples, err := Compile(blueprint, CompileOptions{})
	if err != nil {
		t.Fatalf("compile Blueprint: %v", err)
	}
	server, err := NewServer(context.Background(), ServerConfig{
		Execution: Config{
			Blueprint:        blueprint,
			Tape:             tape,
			Samples:          samples,
			Limits:           DefaultLimits(),
			Policy:           "default",
			MercatorRevision: "server-test",
		},
		OperatorToken: "lab-test-token",
	})
	if err != nil {
		t.Fatalf("open Lab server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown Lab server: %v", err)
		}
	})
	return server
}

func postDrive(t *testing.T, baseURL, body string) Checkpoint {
	t.Helper()
	response := labRequest(
		t,
		http.MethodPost,
		baseURL+"/v1/lab/drive",
		bytes.NewBufferString(body),
	)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("drive = %s: %s", response.Status, data)
	}
	var checkpoint Checkpoint
	decodeResponse(t, response.Body, &checkpoint)
	return checkpoint
}

func labRequest(t *testing.T, method, url string, body io.Reader) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, url, body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer lab-test-token")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return response
}

func decodeResponse(t *testing.T, body io.Reader, value any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(value); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}
