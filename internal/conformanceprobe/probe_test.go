package conformanceprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestSuccessReportsReadyThenZeroExit(t *testing.T) {
	var requests []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, recordRequest(t, r))
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	exitCode := Run(context.Background(), []string{"success"}, reportEnvironment(server.URL), io.Discard, io.Discard)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	want := reportsFixture(t, "testdata/success_reports.json")
	if got := requestReports(requests); !reflect.DeepEqual(got, want) {
		t.Fatalf("reports = %#v, want %#v", got, want)
	}
	for _, request := range requests {
		if request.Path != "/v1/runs/run_probe/report?workspace_id=ws_probe" {
			t.Errorf("request path = %q", request.Path)
		}
		if request.Authorization != "Bearer run-token" {
			t.Errorf("authorization = %q", request.Authorization)
		}
		if request.UserAgent != UserAgent {
			t.Errorf("user agent = %q, want %q", request.UserAgent, UserAgent)
		}
	}
}

func TestWaitForCancelReportsReadyThenBlocks(t *testing.T) {
	ready := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = recordRequest(t, r)
		w.WriteHeader(http.StatusAccepted)
		ready <- struct{}{}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"wait-for-cancel"}, reportEnvironment(server.URL), io.Discard, io.Discard)
	}()

	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("probe did not report ready")
	}
	select {
	case code := <-done:
		t.Fatalf("probe returned before cancellation with %d", code)
	default:
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("exit code after cancellation = %d, want zero", code)
	}
}

func TestProbeRetriesTransientReportFailures(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	exitCode := Run(context.Background(), []string{"success"}, reportEnvironment(server.URL), io.Discard, io.Discard)

	if exitCode != 0 || attempts != 4 {
		t.Fatalf("exit code = %d, attempts = %d, want success after ready retries plus exit", exitCode, attempts)
	}
}

func TestSuccessRejectsMissingReportingEnvironment(t *testing.T) {
	for _, missing := range []string{
		"MERCATOR_REPORT_URL",
		"MERCATOR_RUN_ID",
		"MERCATOR_WORKSPACE_ID",
		"MERCATOR_RUN_TOKEN",
	} {
		t.Run(missing, func(t *testing.T) {
			environment := reportEnvironment("https://reports.example.com")
			delete(environment, missing)
			var stderr bytes.Buffer

			exitCode := Run(context.Background(), []string{"success"}, environment, io.Discard, &stderr)

			if exitCode != 2 {
				t.Fatalf("exit code = %d, want 2", exitCode)
			}
			if !bytes.Contains(stderr.Bytes(), []byte(missing)) {
				t.Fatalf("stderr = %q, want %s", stderr.String(), missing)
			}
		})
	}
}

type recordedRequest struct {
	Path          string
	Authorization string
	UserAgent     string
	Report        map[string]any
}

func recordRequest(t *testing.T, request *http.Request) recordedRequest {
	t.Helper()
	defer request.Body.Close()
	var report map[string]any
	if err := json.NewDecoder(request.Body).Decode(&report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	return recordedRequest{
		Path:          request.URL.RequestURI(),
		Authorization: request.Header.Get("Authorization"),
		UserAgent:     request.Header.Get("User-Agent"),
		Report:        report,
	}
}

func reportEnvironment(reportURL string) map[string]string {
	return map[string]string{
		"MERCATOR_REPORT_URL":   reportURL,
		"MERCATOR_RUN_ID":       "run_probe",
		"MERCATOR_WORKSPACE_ID": "ws_probe",
		"MERCATOR_RUN_TOKEN":    "run-token",
	}
}

func reportsFixture(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var reports []map[string]any
	if err := json.Unmarshal(raw, &reports); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return reports
}

func requestReports(requests []recordedRequest) []map[string]any {
	reports := make([]map[string]any, 0, len(requests))
	for _, request := range requests {
		reports = append(reports, request.Report)
	}
	return reports
}
