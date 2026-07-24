package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
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

func TestWatchingTheConsoleDoesNotChangeTheExportedBundle(t *testing.T) {
	// Arrange
	server := openServerFixture(t)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	postDrive(t, httpServer.URL, `{"kind":"quiesce"}`)
	unwatched := exportedEffects(t, httpServer.URL)

	// Act: an operator leaves the Offers page open, which polls the catalog.
	for range 3 {
		response := labRequest(t, http.MethodGet, httpServer.URL+"/v1/offers?workspace_id="+WorkspaceID, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("observe Offers = %s", response.Status)
		}
		_ = response.Body.Close()
	}
	watched := exportedEffects(t, httpServer.URL)

	// Assert
	if len(watched) != len(unwatched) {
		t.Fatalf("effect ledger grew from %d to %d while a browser watched", len(unwatched), len(watched))
	}
	for index, effect := range watched {
		if effect.ID != unwatched[index].ID {
			t.Fatalf("effect %d changed identity from %s to %s", index, unwatched[index].ID, effect.ID)
		}
	}
}

func TestObservingOffersBeforeTheFirstDriveSucceeds(t *testing.T) {
	// Arrange
	server := openServerFixture(t)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	// Act
	response := labRequest(t, http.MethodGet, httpServer.URL+"/v1/offers?workspace_id="+WorkspaceID, nil)
	defer response.Body.Close()

	// Assert
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("observe Offers before driving = %s: %s", response.Status, body)
	}
}

func exportedEffects(t *testing.T, baseURL string) []EffectRecord {
	t.Helper()
	response := labRequest(t, http.MethodGet, baseURL+"/v1/lab/bundle", nil)
	defer response.Body.Close()
	archive, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read Run Bundle: %v", err)
	}
	entries, err := readBundleEntries(archive)
	if err != nil {
		t.Fatalf("read Run Bundle entries: %v", err)
	}
	var effects []EffectRecord
	for _, line := range bytes.Split(entries["effects.jsonl"], []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var effect EffectRecord
		if err := json.Unmarshal(line, &effect); err != nil {
			t.Fatalf("decode effect: %v", err)
		}
		effects = append(effects, effect)
	}
	return effects
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

func TestServerAddsBrowserEvidenceToDownloadedBundle(t *testing.T) {
	server := openServerFixture(t)
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	trace, err := writer.CreateFormFile("trace", "trace.zip")
	if err != nil {
		t.Fatalf("create trace part: %v", err)
	}
	if _, err := trace.Write([]byte("trace bytes")); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	screenshot, err := writer.CreateFormFile("screenshots", "terminal.png")
	if err != nil {
		t.Fatalf("create screenshot part: %v", err)
	}
	if _, err := screenshot.Write([]byte("png bytes")); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart body: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/lab/evidence", body)
	request.Header.Set("Authorization", "Bearer lab-test-token")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	bundleRequest := httptest.NewRequest(http.MethodGet, "/v1/lab/bundle", nil)
	bundleRequest.Header.Set("Authorization", "Bearer lab-test-token")
	bundleResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(bundleResponse, bundleRequest)
	entries, err := readBundleEntries(bundleResponse.Body.Bytes())
	if err != nil {
		t.Fatalf("read downloaded bundle: %v", err)
	}
	if string(entries["ui/trace.zip"]) != "trace bytes" {
		t.Fatal("downloaded bundle did not carry the trace")
	}
	if string(entries["ui/screenshots/terminal.png"]) != "png bytes" {
		t.Fatal("downloaded bundle did not carry the screenshot")
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
