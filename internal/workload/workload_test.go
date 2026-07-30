package workload

import (
	"context"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

type workspaceTestLog struct {
	eventlog.EventLog
}

func (l workspaceTestLog) AppendIfWorkspaceActive(ctx context.Context, request eventlog.AppendRequest) (eventlog.AppendResult, error) {
	return l.Append(ctx, request)
}

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

// Mutable (tag-form) images are now accepted at revision-store time: digests
// are no longer mandatory and are resolved server-side at run-create. Genuinely
// invalid revisions (e.g. an empty image) must still be rejected.
func TestServiceAcceptsMutableTagsRejectsInvalidRevisions(t *testing.T) {
	ctx := context.Background()
	svc := New(openWorkloadTestLog(t))
	if err := svc.CreateWorkload(ctx, CreateWorkloadRequest{WorkspaceID: "ws_1", WorkloadID: "wrk_1", Name: "trainer"}); err != nil {
		t.Fatalf("create workload: %v", err)
	}

	tagRev := validRevision()
	tagRev.Spec.Containers[0].Image = "ghcr.io/acme/trainer:latest"
	if _, err := svc.CreateRevision(ctx, CreateRevisionRequest{WorkspaceID: "ws_1", WorkloadID: "wrk_1", Revision: tagRev}); err != nil {
		t.Fatalf("mutable tag revision should now be accepted (resolution is deferred to run-create): %v", err)
	}

	badRev := validRevision()
	badRev.Spec.Containers[0].Image = ""
	if _, err := svc.CreateRevision(ctx, CreateRevisionRequest{WorkspaceID: "ws_1", WorkloadID: "wrk_1", Revision: badRev}); err == nil {
		t.Fatal("expected an empty-image revision to be rejected")
	}
}

func openWorkloadTestLog(t *testing.T) eventlog.WorkspaceEventLog {
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
	return workspaceTestLog{EventLog: log}
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
			Placement: domain.PlacementPolicy{Class: domain.ClassStandard, ExpectedRuntimeSeconds: 60},
			Execution: domain.ExecutionPolicy{MaxRuntimeSeconds: 120, MaxPreStartAttempts: 3},
		},
	}
}

// TestAStoredRevisionKeepsItsSecretsOutOfThePublicEvent is the door that stores a
// revision held to the rule the run door has always held. An environment value is
// where a caller puts a token, this event is public, and the console streams every
// public event of a workspace to every reader of it, so writing the whole revision
// into the public payload published the token. The private payload is the copy a
// Run is created from, so the value has to survive there and nowhere else.
func TestAStoredRevisionKeepsItsSecretsOutOfThePublicEvent(t *testing.T) {
	ctx := context.Background()
	log := openWorkloadTestLog(t)
	svc := New(log)
	if err := svc.CreateWorkload(ctx, CreateWorkloadRequest{WorkspaceID: "ws_1", WorkloadID: "wrk_1", Name: "trainer"}); err != nil {
		t.Fatalf("create workload: %v", err)
	}
	secret := "hf_live_SECRETVALUE"
	revision := validRevision()
	revision.Spec.Containers[0].Env = map[string]domain.EnvBinding{"HF_TOKEN": {Value: &secret}}

	if _, err := svc.CreateRevision(ctx, CreateRevisionRequest{WorkspaceID: "ws_1", WorkloadID: "wrk_1", Revision: revision}); err != nil {
		t.Fatalf("create revision: %v", err)
	}

	history, err := eventlog.ReadFullStream(ctx, log, workloadStream("ws_1", "wrk_1"))
	if err != nil {
		t.Fatalf("read the workload stream: %v", err)
	}
	for _, event := range history.Events {
		if event.Type != EventWorkloadRevisionCreated {
			continue
		}
		if strings.Contains(string(event.Data), secret) {
			t.Fatalf("the public payload of %s carries the token verbatim: %s", event.ID, event.Data)
		}
		if !strings.Contains(string(event.Data), `"kind":"literal"`) {
			t.Fatalf("the public payload of %s says nothing about the variable at all: %s", event.ID, event.Data)
		}
	}
	stored, err := svc.GetRevision(ctx, "ws_1", "wrk_1", "wrev_1")
	if err != nil {
		t.Fatalf("get revision: %v", err)
	}
	if value := stored.Spec.Containers[0].Env["HF_TOKEN"].Value; value == nil || *value != secret {
		t.Fatalf("the stored revision reads back with %v for a value the caller set, and a Run created from it would run with that", value)
	}
}
