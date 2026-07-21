package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/benngarcia/mercator/internal/sentryreporter"
	"github.com/getsentry/sentry-go"
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
	transport := &sentry.MockTransport{}
	reporter, err := sentryreporter.New(map[string]string{
		"SENTRY_DSN":         "https://public@example.com/1",
		"SENTRY_ENVIRONMENT": "test",
		"SENTRY_RELEASE":     "mercator@test",
	}, sentryreporter.WithTransport(transport))
	if err != nil {
		t.Fatalf("configure Sentry reporter: %v", err)
	}
	t.Cleanup(reporter.Close)
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
	}), WithLogger(logger), WithFailureReporter(reporter))
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
	err = orch.AdvanceRun(t.Context(), "ws_1", "run_out_of_stock")
	var failure *adapter.ProviderFailure
	if !errors.As(err, &failure) {
		t.Fatalf("advance error = %v, want typed provider failure", err)
	}
	if failure.Kind != adapter.ProviderFailureCapacityUnavailable || failure.Status != http.StatusConflict || failure.ProviderCode != "OUT_OF_STOCK" {
		t.Fatalf("failure classification = %+v", failure)
	}
	if !failure.Retryable || failure.SideEffect != adapter.SideEffectNone {
		t.Fatalf("out-of-stock retryability and side effect = %+v", failure)
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
	recorded := transport.Events()
	if len(recorded) != 1 {
		t.Fatalf("Sentry events = %d, want exactly one", len(recorded))
	}
	if got := recorded[0].Contexts["provider_failure"]["run_id"]; got != "run_out_of_stock" {
		t.Errorf("Sentry run_id = %#v, want run_out_of_stock", got)
	}
	if got := recorded[0].Contexts["provider_failure"]["provider_code"]; got != "OUT_OF_STOCK" {
		t.Errorf("Sentry provider_code = %#v, want OUT_OF_STOCK", got)
	}
	recordedJSON, _ := json.Marshal(recorded[0])
	for _, secret := range []string{apiKey, registrySecret, workloadSecret, "response_body", "authorization"} {
		if strings.Contains(string(recordedJSON), secret) {
			t.Fatalf("Sentry event leaked %q: %s", secret, recordedJSON)
		}
	}

	events, err := orch.GetRunEvents(t.Context(), "ws_1", "run_out_of_stock")
	if err != nil {
		t.Fatalf("get run events: %v", err)
	}
	var publicFailure map[string]any
	publicEvents := make([]eventlog.CloudEvent, 0, len(events))
	for _, event := range events {
		publicEvents = append(publicEvents, event.CloudEvent())
		if event.Type == orchestrator.EventLaunchFailed {
			if err := json.Unmarshal(event.CloudEvent().Data, &publicFailure); err != nil {
				t.Fatalf("decode public failure: %v", err)
			}
		}
	}
	if publicFailure["code"] != "PROVIDER_CAPACITY_UNAVAILABLE" || publicFailure["retryable"] != true {
		t.Fatalf("public failure = %#v", publicFailure)
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
