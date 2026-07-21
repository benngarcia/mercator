package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/shadeform"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
)

func TestShadeformOutOfStockFailureIsPrivateAndPublicSafe(t *testing.T) {
	const (
		apiKey         = "shadeform-api-secret"
		registrySecret = "registry-secret"
		workloadSecret = "workload-secret"
	)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/instances":
			_, _ = io.WriteString(w, `{"instances":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/instances/types":
			_, _ = w.Write(readFixture(t, "testdata/shadeform_a6000_catalog.json"))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/instances/create":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write(readFixture(t, "testdata/shadeform_out_of_stock.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)

	var privateLog bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&privateLog, nil))
	factory := NewFactory()
	factory.Register(shadeform.Manifest(), func(config map[string]string, secret string) (adapter.Provider, error) {
		return shadeform.New(secret, config)
	})
	connections := fakeConns{recs: []connection.Record{{
		ID:          "conn_shadeform",
		WorkspaceID: "ws_1",
		AdapterType: "shadeform",
		Authorized:  true,
		Config: map[string]string{
			"base_url":          provider.URL + "/v1",
			"registry_username": "registry-user",
			"registry_password": registrySecret,
		},
		Credential: credential.Credential{Source: credential.SourceEnv, Ref: "SHADEFORM_API_KEY"},
	}}}
	broker := NewBroker(connections, factory, resolverFunc(func(context.Context, string, credential.Credential) (string, error) {
		return apiKey, nil
	}), WithLogger(logger))
	log, err := eventlog.OpenSQLite(t.Context(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	orch := orchestrator.New(log, scheduler.New(), broker)
	value := workloadSecret
	workload := providerFailureWorkload(&value)

	_, err = orch.CreateRun(t.Context(), orchestrator.CreateRunRequest{
		WorkspaceID: "ws_1",
		RunID:       "run_out_of_stock",
		CommandKey:  "create_run_out_of_stock",
		Workload:    workload,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := orch.AdvanceRun(t.Context(), "ws_1", "run_out_of_stock"); err != nil {
		t.Fatalf("capacity rejection should be handled by replacement policy: %v", err)
	}

	lines := bytes.Split(bytes.TrimSpace(privateLog.Bytes()), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("private diagnostic lines = %d, want exactly one: %s", len(lines), privateLog.String())
	}
	var diagnostic map[string]any
	if err := json.Unmarshal(lines[0], &diagnostic); err != nil {
		t.Fatalf("decode private diagnostic: %v", err)
	}
	for key, want := range map[string]any{
		"workspace_id":       "ws_1",
		"run_id":             "run_out_of_stock",
		"connection_id":      "conn_shadeform",
		"adapter_type":       "shadeform",
		"operation":          "launch",
		"http_status":        float64(http.StatusConflict),
		"provider_code":      "OUT_OF_STOCK",
		"retryable":          true,
		"side_effect":        "none",
		"retry_count":        float64(0),
		"response_truncated": false,
	} {
		if got := diagnostic[key]; got != want {
			t.Errorf("diagnostic[%q] = %#v, want %#v", key, got, want)
		}
	}
	for _, key := range []string{"attempt_id", "offer_snapshot_id", "offer_native_ref", "response_body"} {
		if diagnostic[key] == nil || diagnostic[key] == "" {
			t.Errorf("diagnostic[%q] must be populated: %#v", key, diagnostic)
		}
	}
	privateText := privateLog.String()
	for _, secret := range []string{apiKey, registrySecret, workloadSecret, "Bearer " + apiKey} {
		if strings.Contains(privateText, secret) {
			t.Fatalf("private diagnostic leaked %q: %s", secret, privateText)
		}
	}

	events, err := orch.GetRunEvents(t.Context(), "ws_1", "run_out_of_stock")
	if err != nil {
		t.Fatalf("get run events: %v", err)
	}
	var publicFailure map[string]any
	var publicClosed map[string]any
	publicEvents := make([]eventlog.CloudEvent, 0, len(events))
	for _, event := range events {
		publicEvents = append(publicEvents, event.CloudEvent())
		switch event.Type {
		case orchestrator.EventLaunchFailed:
			if err := json.Unmarshal(event.CloudEvent().Data, &publicFailure); err != nil {
				t.Fatalf("decode public failure: %v", err)
			}
		case orchestrator.EventRunClosed:
			if err := json.Unmarshal(event.CloudEvent().Data, &publicClosed); err != nil {
				t.Fatalf("decode public close: %v", err)
			}
		}
	}
	if publicFailure["code"] != "PROVIDER_CAPACITY_UNAVAILABLE" || publicFailure["retryable"] != true || publicFailure["side_effect"] != "none" {
		t.Fatalf("public failure = %#v", publicFailure)
	}
	if _, exposed := publicFailure["provider_kind"]; exposed {
		t.Fatalf("public failure exposed canonical private classification: %#v", publicFailure)
	}
	record, err := orch.GetRun(t.Context(), "ws_1", "run_out_of_stock")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if !record.Closed || record.Outcome != domain.RunOutcomeFailed || record.Cleanup != domain.CleanupNotRequired {
		t.Fatalf("single stale Offer should exhaust without cleanup: %+v", record)
	}
	if publicClosed["reason"] != "RETRY_EXHAUSTED" {
		t.Fatalf("public close = %#v", publicClosed)
	}
	publicJSON, _ := json.Marshal(publicEvents)
	for _, private := range []string{"OUT_OF_STOCK", "response_body", apiKey, registrySecret, workloadSecret} {
		if strings.Contains(string(publicJSON), private) {
			t.Fatalf("public events leaked %q: %s", private, publicJSON)
		}
	}
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	fixture, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return fixture
}

type resolverFunc func(context.Context, string, credential.Credential) (string, error)

func (f resolverFunc) Resolve(ctx context.Context, workspaceID string, ref credential.Credential) (string, error) {
	return f(ctx, workspaceID, ref)
}

func providerFailureWorkload(secret *string) domain.WorkloadRevision {
	return domain.WorkloadRevision{
		ID:          "wrev_provider_failure",
		WorkspaceID: "ws_1",
		WorkloadID:  "wrk_provider_failure",
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "ghcr.io/acme/inference@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
				Env:      map[string]domain.EnvBinding{"TOKEN": {Value: secret}},
			}},
			Resources: domain.ResourceRequirements{
				CPU:           domain.CPURequirement{MinMillis: 1000},
				Memory:        domain.MemoryRequirement{MinBytes: 1 << 30},
				EphemeralDisk: domain.DiskRequirement{MinBytes: 1 << 30},
			},
			Network:   domain.NetworkRequirements{Inbound: domain.InboundNetworkNone},
			Placement: domain.PlacementPolicy{Objective: domain.ObjectiveBalanced, ExpectedRuntimeSeconds: 60},
			Execution: domain.ExecutionPolicy{MaxRuntimeSeconds: 120, MaxPreStartAttempts: 3},
		},
	}
}
