package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/httpapi"
	sqlitestore "github.com/benngarcia/mercator/internal/storage/sqlite"
	"github.com/benngarcia/mercator/internal/workspace"
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

func TestHelpDoesNotRequireBaseURL(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		contains string
	}{
		{
			name:     "root",
			args:     []string{"--help"},
			contains: "Usage: mercator",
		},
		{
			name:     "run",
			args:     []string{"run", "--help"},
			contains: "Usage: mercator run",
		},
		{
			name:     "run create",
			args:     []string{"run", "create", "--help"},
			contains: "mercator run create busybox -- echo hi",
		},
		{
			name:     "sink",
			args:     []string{"sink", "--help"},
			contains: "Usage: mercator sink",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), Config{
				Args:   tc.args,
				Stdout: &stdout,
				Stderr: &stderr,
			})
			if code != 0 {
				t.Fatalf("help returned code %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("help should not write stderr, got %q", stderr.String())
			}
			if !bytes.Contains(stdout.Bytes(), []byte(tc.contains)) {
				t.Fatalf("help output did not contain %q:\n%s", tc.contains, stdout.String())
			}
		})
	}
}

func TestWorkspaceIDDefaultsFromConfig(t *testing.T) {
	handler := newCLITestServer(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	workload := mustJSON(t, cliRevision())

	// With Config.WorkspaceID set (sourced from MERCATOR_WORKSPACE_ID), commands
	// may omit --workspace-id entirely.
	create := []string{"run", "create", "--run-id", "run_ws_default", "--idempotency-key", "idem_ws_default", "--workload-json", workload}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Config{
		BaseURL:     server.URL,
		WorkspaceID: "ws_1",
		Args:        create,
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	if code != 0 {
		t.Fatalf("create with default workspace failed code=%d stderr=%s", code, stderr.String())
	}
	var created struct {
		Run struct {
			ID          string `json:"id"`
			WorkspaceID string `json:"workspace_id"`
		} `json:"run"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil {
		t.Fatalf("decode create response %q: %v", stdout.String(), err)
	}
	if created.Run.ID != "run_ws_default" || created.Run.WorkspaceID != "ws_1" {
		t.Fatalf("unexpected create run: %+v", created.Run)
	}

	// A read also works without --workspace-id, and an explicit flag still
	// overrides the default.
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), Config{
		BaseURL:     server.URL,
		WorkspaceID: "ws_1",
		Args:        []string{"run", "get", "--run-id", "run_ws_default"},
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	if code != 0 {
		t.Fatalf("get with default workspace failed code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunCreatePositionalImageShorthand(t *testing.T) {
	handler := newCLITestServer(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	// The simplest possible invocation: positional image, no --run-id (the
	// server generates and returns one), no --idempotency-key (the CLI mints a
	// stable one), workspace from MERCATOR_WORKSPACE_ID. Args after `--` become
	// the container args.
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Config{
		BaseURL:     server.URL,
		WorkspaceID: "ws_1",
		Args:        []string{"run", "create", "busybox", "--", "echo", "hi"},
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	if code != 0 {
		t.Fatalf("positional shorthand create failed code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var created struct {
		Run struct {
			ID          string `json:"id"`
			WorkspaceID string `json:"workspace_id"`
		} `json:"run"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil {
		t.Fatalf("decode shorthand create response %q: %v", stdout.String(), err)
	}
	if created.Run.WorkspaceID != "ws_1" {
		t.Fatalf("expected workspace ws_1, got %+v", created.Run)
	}
	if created.Run.ID == "" {
		t.Fatalf("expected a server-generated run id, got empty: %s", stdout.String())
	}
}

func TestRunCreateRequiresImageOrWorkload(t *testing.T) {
	handler := newCLITestServer(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Config{
		BaseURL:     server.URL,
		WorkspaceID: "ws_1",
		Args:        []string{"run", "create"},
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	if code != 2 {
		t.Fatalf("expected arg validation exit 2, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestIdempotencyConflictIsReportedAsJSON(t *testing.T) {
	handler := newCLITestServer(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	first := mustJSON(t, cliRevision())
	// A regenerated cosmetic revision ID is a safe replay, not a conflict; use a
	// substantive change (different image) to provoke a real idempotency conflict.
	other := cliRevision()
	other.Spec.Containers[0].Image = "ghcr.io/acme/other@sha256:3333333333333333333333333333333333333333333333333333333333333333"
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

func TestRunAcceptsGlobalAPIURLFlag(t *testing.T) {
	handler := newCLITestServer(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Config{
		Args:   []string{"--api-url", server.URL, "run", "list", "--workspace-id", "ws_1"},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if code != 0 {
		t.Fatalf("global api url flag failed with code %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout was not json: %q: %v", stdout.String(), err)
	}
}

func newCLITestServer(t *testing.T) http.Handler {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	seedCLIWorkspace(t, dsn)
	return handlerForDSN(t, dsn)
}

func handlerForDSN(t *testing.T, dsn string, options ...httpapi.Option) http.Handler {
	t.Helper()
	handler, closeFn, err := httpapi.HandlerForSQLite(context.Background(), dsn, []domain.OfferSnapshot{cliOffer()}, options...)
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

func seedCLIWorkspace(t *testing.T, dsn string) {
	t.Helper()
	storage, err := sqlitestore.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open workspace storage: %v", err)
	}
	if _, err := storage.Workspaces().Create(t.Context(), workspace.Create{
		ID:          "ws_1",
		DisplayName: "CLI test",
		CreatedAt:   time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		CreatedBy:   "test:cli",
	}); err != nil {
		t.Fatalf("create CLI workspace: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
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
	now := time.Now().UTC()
	return domain.OfferSnapshot{
		ID:           "offer_cli",
		RentalID:     "offer_cli",
		ConnectionID: "conn_cli",
		AdapterType:  "fake",
		Kind:         domain.OfferKindStanding,
		NativeRef:    "fake://cli",
		ObservedAt:   now.Add(-time.Minute),
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
