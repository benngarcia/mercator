package main

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/janitor"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
)

// TestReconcileWorkspacesClosesExitedRunWithoutClients drives the serve path's
// reconcile tick body directly (no ticker, no sleeps) against the fake adapter:
// a run whose container exited stays open with no client polling, one tick
// closes it, and the workspace is left with zero owned external objects.
func TestReconcileWorkspacesClosesExitedRunWithoutClients(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenSQLite(ctx, "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	// The container exits successfully, but only AFTER the create-time advance
	// has observed it running once — the tick, not a client, must see the exit.
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{reconcileTestOffer(time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
		fake.WithOpenObservations(1),
	)
	orch := orchestrator.New(log, scheduler.New(), ad)
	if _, err := orch.CreateRun(ctx, orchestrator.CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_1",
		CommandKey:     "cmd_create",
		IdempotencyKey: "idem_create",
		Workload:       reconcileTestRevision(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	// The create-time advance the HTTP layer runs once on POST /v1/runs.
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("create-time advance: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Closed {
		t.Fatalf("run closed before any reconcile tick; precondition did not hold: %+v", record)
	}

	reconcileWorkspaces(ctx, orch, janitor.New(ad, janitor.WithEventLog(log)))

	record, err = orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run after tick: %v", err)
	}
	if !record.Closed {
		t.Fatalf("run still open after one reconcile tick: %+v", record)
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("reconcile tick left external objects behind: %+v", owned)
	}
}

func reconcileTestRevision() domain.WorkloadRevision {
	return domain.WorkloadRevision{
		ID:          "wrev_1",
		WorkspaceID: "ws_1",
		WorkloadID:  "wrk_1",
		Digest:      "sha256:revision",
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "ghcr.io/acme/inference@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
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

func reconcileTestOffer(now time.Time) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:           "off_1",
		ConnectionID: "conn_1",
		AdapterType:  "fake",
		Kind:         domain.OfferKindStanding,
		ObservedAt:   now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Minute),
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          2000,
			MemoryBytes:        2 << 30,
			EphemeralDiskBytes: 2 << 30,
		},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: 32768},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone, Protocols: []string{"tcp"}},
			Pricing:   domain.PricingCapabilities{Known: true},
			Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "deterministic_name", ListOwned: true},
		},
		Pricing:    domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.0001, Known: true},
		Queue:      &domain.QueueSnapshot{},
		Capacity:   domain.CapacityEvidence{Available: true, Confidence: 1},
		ImageCache: domain.ImageCacheEvidence{ManifestCached: true, MissingBytes: 0, Known: true},
	}
}
