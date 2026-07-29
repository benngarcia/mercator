package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/connection"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
)

// TestAnOutOfStockLaunchIsPrivateAndPublicSafe holds the split this package owns:
// what a provider said goes to the operator's own log, and what Mercator
// concluded goes to the Run's public record.
//
// It runs against a one-shot executor written for the case rather than against a
// production adapter, because the seam under test is the Broker's. Which HTTP
// status and provider code one marketplace calls out of stock is that adapter's
// own classification, held by its own tests; this one is about what happens to a
// classified failure once it crosses into the control plane.
func TestAnOutOfStockLaunchIsPrivateAndPublicSafe(t *testing.T) {
	const (
		apiKey         = "provider-api-secret"
		workloadSecret = "workload-secret"
	)
	var privateLog bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&privateLog, nil))
	factory := NewFactory()
	factory.Register(adapter.Manifest{Type: "stub"}, func(map[string]string, string) (capability.Backend, error) {
		return outOfStockProvider{}, nil
	})
	connections := fakeConns{recs: []connection.Record{{
		ID:          "conn_stub",
		WorkspaceID: "ws_1",
		AdapterType: "stub",
		Authorized:  true,
		Credential:  credential.Credential{Source: credential.SourceEnv, Ref: "PROVIDER_API_KEY"},
	}}}
	broker := NewBroker(connections, factory, resolverFunc(func(context.Context, string, credential.Credential) (string, error) {
		return apiKey, nil
	}), WithLogger(logger))
	log, err := eventlog.OpenSQLite(t.Context(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	orch := orchestrator.New(activeWorkspaceLog{EventLog: log}, scheduler.New(), broker)
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
		"connection_id":      "conn_stub",
		"adapter_type":       "stub",
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
	for _, secret := range []string{apiKey, workloadSecret, "Bearer " + apiKey} {
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
	for _, private := range []string{"OUT_OF_STOCK", "response_body", apiKey, workloadSecret} {
		if strings.Contains(string(publicJSON), private) {
			t.Fatalf("public events leaked %q: %s", private, publicJSON)
		}
	}
}

type activeWorkspaceLog struct {
	eventlog.EventLog
}

func (l activeWorkspaceLog) AppendIfWorkspaceActive(ctx context.Context, request eventlog.AppendRequest) (eventlog.AppendResult, error) {
	return l.Append(ctx, request)
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
			Placement: domain.PlacementPolicy{Class: domain.ClassStandard, ExpectedRuntimeSeconds: 60},
			Execution: domain.ExecutionPolicy{MaxRuntimeSeconds: 120, MaxPreStartAttempts: 3},
		},
	}
}

// outOfStockProvider is a one-shot executor whose create is refused for capacity
// that was listed and is gone. Its failure arrives already sanitized, which is
// the adapter contract: a provider response body is the adapter's to redact
// before it becomes a ProviderFailure, and what this test watches is where the
// classified failure goes afterwards.
type outOfStockProvider struct{ oneShotLane }

func (outOfStockProvider) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	now := time.Now().UTC()
	return []domain.OfferSnapshot{{
		ID:           "off_stub_1",
		Kind:         domain.OfferKindProvisionable,
		Region:       "stub/us-east-1",
		InstanceType: "A6000",
		NativeRef:    "stub/us-east-1/A6000",
		ObservedAt:   now,
		ExpiresAt:    now.Add(5 * time.Minute),
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          12000,
			MemoryBytes:        48 << 30,
			EphemeralDiskBytes: 256 << 30,
			EphemeralDiskKnown: true,
			AcceleratorsKnown:  true,
		},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: 32768},
			Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "launch_key", ListOwned: true, CancelQueued: true},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone, PublicIPv4: true},
			Pricing:   domain.PricingCapabilities{Known: true},
		},
		Pricing:  domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.0005, GranularitySeconds: 1, Known: true},
		Capacity: domain.CapacityEvidence{Available: true},
		Images:   domain.ImageInventory{Known: false},
	}}, nil
}

func (outOfStockProvider) Launch(context.Context, adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	return adapter.LaunchReceipt{}, &adapter.ProviderFailure{
		Kind:         adapter.ProviderFailureCapacityUnavailable,
		Status:       http.StatusConflict,
		ProviderCode: "OUT_OF_STOCK",
		Retryable:    true,
		SideEffect:   adapter.SideEffectNone,
		ResponseBody: `{"code":"OUT_OF_STOCK","message":"[REDACTED]"}`,
	}
}
