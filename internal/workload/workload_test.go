package workload

import (
	"context"
	"testing"

	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
)

func TestServiceCreatesImmutableWorkloadRevisionsFromEvents(t *testing.T) {
	ctx := context.Background()
	log := openWorkloadTestLog(t)
	svc := New(log)

	if err := svc.CreateWorkload(ctx, CreateWorkloadRequest{WorkspaceID: "ws_1", WorkloadID: "wrk_1", Name: "trainer"}); err != nil {
		t.Fatalf("create workload: %v", err)
	}
	rev := validRevision()
	created, err := svc.CreateRevision(ctx, CreateRevisionRequest{WorkspaceID: "ws_1", WorkloadID: "wrk_1", Revision: rev})
	if err != nil {
		t.Fatalf("create revision: %v", err)
	}
	if created.ID != "wrev_1" || created.Spec.Containers[0].Image != rev.Spec.Containers[0].Image {
		t.Fatalf("unexpected created revision: %+v", created)
	}

	got, err := svc.GetRevision(ctx, "ws_1", "wrk_1", "wrev_1")
	if err != nil {
		t.Fatalf("get revision: %v", err)
	}
	if got.ID != "wrev_1" || got.WorkloadID != "wrk_1" {
		t.Fatalf("unexpected revision: %+v", got)
	}
	revisions, err := svc.ListRevisions(ctx, "ws_1", "wrk_1")
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revisions) != 1 || revisions[0].ID != "wrev_1" {
		t.Fatalf("unexpected revisions: %+v", revisions)
	}
}

func TestServiceRejectsMutableTagsAndInvalidRevisions(t *testing.T) {
	ctx := context.Background()
	svc := New(openWorkloadTestLog(t))
	if err := svc.CreateWorkload(ctx, CreateWorkloadRequest{WorkspaceID: "ws_1", WorkloadID: "wrk_1", Name: "trainer"}); err != nil {
		t.Fatalf("create workload: %v", err)
	}
	rev := validRevision()
	rev.Spec.Containers[0].Image = "ghcr.io/acme/trainer:latest"

	if _, err := svc.CreateRevision(ctx, CreateRevisionRequest{WorkspaceID: "ws_1", WorkloadID: "wrk_1", Revision: rev}); err == nil {
		t.Fatal("expected mutable tag revision to be rejected")
	}
}

func openWorkloadTestLog(t *testing.T) *eventlog.SQLiteEventLog {
	t.Helper()
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() {
		if err := log.Close(); err != nil {
			t.Fatalf("close event log: %v", err)
		}
	})
	return log
}

func validRevision() domain.WorkloadRevision {
	return domain.WorkloadRevision{
		ID:          "wrev_1",
		WorkspaceID: "ws_1",
		WorkloadID:  "wrk_1",
		Digest:      "sha256:revision",
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "ghcr.io/acme/trainer@sha256:0000000000000000000000000000000000000000000000000000000000000000",
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
