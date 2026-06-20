package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/httpapi"
)

func TestRunCommandsEmitParseableJSON(t *testing.T) {
	handler := newCLITestServer(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	workload := mustJSON(t, cliRevision())
	commands := [][]string{
		{"run", "create", "--workspace-id", "ws_1", "--run-id", "run_cli", "--idempotency-key", "idem_cli", "--workload-json", workload},
		{"run", "get", "--workspace-id", "ws_1", "--run-id", "run_cli"},
		{"run", "list", "--workspace-id", "ws_1"},
		{"run", "wait", "--workspace-id", "ws_1", "--run-id", "run_cli"},
		{"run", "events", "--workspace-id", "ws_1", "--run-id", "run_cli"},
		{"run", "decision", "--workspace-id", "ws_1", "--run-id", "run_cli"},
		{"run", "refresh", "--workspace-id", "ws_1", "--run-id", "run_cli"},
		{"run", "cancel", "--workspace-id", "ws_1", "--run-id", "run_cli"},
	}
	for _, args := range commands {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), Config{
			BaseURL: server.URL,
			Args:    args,
			Stdout:  &stdout,
			Stderr:  &stderr,
		})
		if code != 0 {
			t.Fatalf("%v failed with code %d stderr=%s stdout=%s", args, code, stderr.String(), stdout.String())
		}
		var decoded map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
			t.Fatalf("%v emitted non-json output %q: %v", args, stdout.String(), err)
		}
	}
}

func TestIdempotencyConflictIsReportedAsJSON(t *testing.T) {
	handler := newCLITestServer(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	first := mustJSON(t, cliRevision())
	other := cliRevision()
	other.ID = "wrev_cli_other"
	second := mustJSON(t, other)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Config{
		BaseURL: server.URL,
		Args:    []string{"run", "create", "--workspace-id", "ws_1", "--run-id", "run_cli_conflict", "--idempotency-key", "idem_conflict", "--workload-json", first},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if code != 0 {
		t.Fatalf("first create failed: code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), Config{
		BaseURL: server.URL,
		Args:    []string{"run", "create", "--workspace-id", "ws_1", "--run-id", "run_cli_conflict", "--idempotency-key", "idem_conflict", "--workload-json", second},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if code != 1 {
		t.Fatalf("expected conflict exit code 1, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var decoded struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &decoded); err != nil {
		t.Fatalf("conflict stderr was not json: %q: %v", stderr.String(), err)
	}
	if decoded.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("unexpected conflict json: %s", stderr.String())
	}
}

func newCLITestServer(t *testing.T) http.Handler {
	t.Helper()
	handler, closeFn, err := httpapi.HandlerForSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared", []domain.OfferSnapshot{cliOffer()})
	if err != nil {
		t.Fatalf("build http handler: %v", err)
	}
	t.Cleanup(func() {
		if err := closeFn(); err != nil {
			t.Fatalf("close handler: %v", err)
		}
	})
	return handler
}

func cliRevision() domain.WorkloadRevision {
	value := "debug"
	return domain.WorkloadRevision{
		ID:          "wrev_cli",
		WorkspaceID: "ws_1",
		WorkloadID:  "wl_cli",
		Digest:      "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "example.com/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
				Env: map[string]domain.EnvBinding{
					"LOG_LEVEL": {Value: &value},
				},
			}},
			Resources: domain.ResourceRequirements{
				CPU:           domain.CPURequirement{MinMillis: 100},
				Memory:        domain.MemoryRequirement{MinBytes: 128 * 1024 * 1024},
				EphemeralDisk: domain.DiskRequirement{MinBytes: 512 * 1024 * 1024},
			},
			Network: domain.NetworkRequirements{Inbound: domain.InboundNetworkNone},
			Placement: domain.PlacementPolicy{
				Objective:              domain.ObjectiveCheapest,
				ExpectedRuntimeSeconds: 60,
			},
			Execution: domain.ExecutionPolicy{MaxRuntimeSeconds: 60, MaxPreStartAttempts: 1},
		},
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(data)
}

func cliOffer() domain.OfferSnapshot {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	return domain.OfferSnapshot{
		ID:           "offer_cli",
		ConnectionID: "conn_cli",
		AdapterType:  "fake",
		Kind:         domain.OfferKindStanding,
		NativeRef:    "fake://cli",
		ObservedAt:   now,
		ExpiresAt:    now.Add(time.Hour),
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          1000,
			MemoryBytes:        1024 * 1024 * 1024,
			EphemeralDiskBytes: 2 * 1024 * 1024 * 1024,
		},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{
				MaxContainers:       1,
				SupportsDigestRefs:  true,
				MaxEnvironmentBytes: 4096,
			},
			Lifecycle: domain.LifecycleCapabilities{
				IdempotentLaunch: "launch_key",
				ListOwned:        true,
				CancelQueued:     true,
			},
			Resources: domain.ResourceCapabilities{},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone},
			Pricing:   domain.PricingCapabilities{Known: true},
		},
		Network: domain.NetworkFacts{Download: []domain.NetworkFact{{
			Scope:      domain.NetworkScopeRegistry,
			Statistic:  "p10",
			ValueMbps:  100,
			Source:     "test",
			ObservedAt: now,
			ValidUntil: now.Add(time.Hour),
			Confidence: 1,
		}}},
		Pricing: domain.PriceModel{
			Currency:             "USD",
			RatePerSecondUSD:     0.001,
			MinimumChargeSeconds: 1,
			GranularitySeconds:   1,
			Known:                true,
		},
		ImageCache: domain.ImageCacheEvidence{Known: true, ManifestCached: true},
		Capacity:   domain.CapacityEvidence{Available: true, Confidence: 1},
	}
}
