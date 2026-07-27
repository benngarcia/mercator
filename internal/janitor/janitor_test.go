package janitor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

// TestJanitorTerminatesCapacityMercatorCannotAccountFor is the terminate half of
// the policy. The provider is holding an execution whose Run this control plane
// has no record of at all, so nothing can ever be bound to it and nothing will
// ever collect it. Releasing only its slot, which is what a sweep with no stated
// policy did, leaves a machine billing that nothing in the fleet can use.
func TestJanitorTerminatesCapacityMercatorCannotAccountFor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_orphan",
		RequestHash:        "sha256:orphan",
		WorkspaceID:        "ws_1",
		RunID:              "run_orphan",
		AttemptID:          "att_orphan",
		OwnershipToken:     "own_orphan",
		LaunchKey:          "launch_orphan",
		CleanupLocator:     "cleanup_orphan",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed orphan: %v", err)
	}

	log := openJanitorTestLog(t)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Found != 1 || result.Terminated != 1 || result.Adopted != 0 {
		t.Fatalf("sweep result = %+v, want the unaccounted-for execution terminated", result)
	}
	if ad.TerminateCount() != 1 || ad.ReleaseCount() != 0 {
		t.Fatalf(
			"capacity nothing can be bound to was reclaimed with release=%d terminate=%d, want it destroyed",
			ad.ReleaseCount(), ad.TerminateCount(),
		)
	}
	convergence := onlyConvergence(t, log, "ws_1")
	if convergence.Policy != OrphanPolicy || convergence.Outcome != OrphanTerminated {
		t.Fatalf("the record says %+v, want the stated policy naming a termination", convergence)
	}
	if convergence.Reason != reasonNoRecordedRun {
		t.Fatalf("the record gives reason %q, want the Run nobody recorded", convergence.Reason)
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("expected owned resources released, got %+v", owned)
	}
}

func TestJanitorSkipsActiveRunResources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_active",
		RequestHash:        "sha256:active",
		WorkspaceID:        "ws_1",
		RunID:              "run_active",
		AttemptID:          "att_active",
		OwnershipToken:     "own_active",
		LaunchKey:          "launch_active",
		CleanupLocator:     "cleanup_active",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed active object: %v", err)
	}
	log := openJanitorTestLog(t)
	appendRunEvent(t, log, "ws_1", "run_active", "compute.run.requested.v1")

	result, err := New(ad, WithEventLog(log)).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Found != 1 || result.Converged() != 0 {
		t.Fatalf("live work should be found and left alone: %+v", result)
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 1 {
		t.Fatalf("expected active resource to remain, got %+v", owned)
	}
}

// TestJanitorAdoptsCapacityItsOwnRecordSaysSurvives is the adopt half. Mercator
// holds the launch this execution came from, and that record says the capacity is
// handed back by releasing the slot: the machine outlives the workload. So the
// slot goes back and the machine stays in the fleet, and the record says which
// policy kept it.
func TestJanitorAdoptsCapacityItsOwnRecordSaysSurvives(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_adopt",
		RequestHash:        "sha256:adopt",
		WorkspaceID:        "ws_1",
		RunID:              "run_adopt",
		AttemptID:          "att_adopt",
		OwnershipToken:     "own_adopt",
		LaunchKey:          "launch_adopt",
		CleanupLocator:     "cleanup_adopt",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
		Disposition:        domain.DispositionRelease,
	})
	if err != nil {
		t.Fatalf("seed adoptable execution: %v", err)
	}
	log := openJanitorTestLog(t)
	appendLaunchIntent(t, log, "ws_1", "run_adopt", domain.DispositionRelease)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if result.Adopted != 1 || result.Terminated != 0 {
		t.Fatalf("sweep result = %+v, want the machine adopted rather than destroyed", result)
	}
	if ad.ReleaseCount() != 1 || ad.TerminateCount() != 0 {
		t.Fatalf(
			"adopted capacity was reclaimed with release=%d terminate=%d, want its slot released and the machine kept",
			ad.ReleaseCount(), ad.TerminateCount(),
		)
	}
	convergence := onlyConvergence(t, log, "ws_1")
	if convergence.Policy != OrphanPolicy || convergence.Outcome != OrphanAdopted {
		t.Fatalf("the record says %+v, want the stated policy naming an adoption", convergence)
	}
	if convergence.Reason != reasonRecordedRelease || convergence.LaunchKey != "launch_adopt" {
		t.Fatalf("the record gives %q for %q, want the recorded disposition for this capacity", convergence.Reason, convergence.LaunchKey)
	}
}

// TestJanitorTerminatesCapacityLeftBehindByAClosedRun is the case a sweep keyed
// on the cleanup request alone could only skip. The Run is over and Mercator
// never asked for its capacity back, which is what a control plane that died
// between closing a Run and reclaiming it leaves behind, and nothing else in the
// tree would ever have come for it.
func TestJanitorTerminatesCapacityLeftBehindByAClosedRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_closed",
		RequestHash:        "sha256:closed",
		WorkspaceID:        "ws_1",
		RunID:              "run_closed",
		AttemptID:          "att_closed",
		OwnershipToken:     "own_closed",
		LaunchKey:          "launch_closed",
		CleanupLocator:     "cleanup_closed",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed stranded execution: %v", err)
	}
	log := openJanitorTestLog(t)
	appendRunEvent(t, log, "ws_1", "run_closed", "compute.run.closed.v1")

	result, err := New(ad, WithEventLog(log)).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if result.Terminated != 1 {
		t.Fatalf("sweep result = %+v, want the capacity of a finished Run destroyed", result)
	}
	convergence := onlyConvergence(t, log, "ws_1")
	if convergence.Reason != reasonClosedWithoutAsking {
		t.Fatalf("the record gives reason %q, want the Run that closed with nothing asked for", convergence.Reason)
	}
}

// onlyConvergence is the one orphan decision this workspace's record holds. It
// reads the public log rather than the sweep's return value, because the record is
// what an operator and every rule about the policy actually see.
func onlyConvergence(t *testing.T, log eventlog.EventLog, workspaceID string) OrphanConvergence {
	t.Helper()
	head, err := log.LatestPosition(context.Background(), eventlog.EventFilter{WorkspaceID: workspaceID})
	if err != nil {
		t.Fatalf("read log head: %v", err)
	}
	var found []OrphanConvergence
	for event, err := range eventlog.ScanAll(context.Background(), log, head, eventlog.EventFilter{WorkspaceID: workspaceID}) {
		if err != nil {
			t.Fatalf("scan log: %v", err)
		}
		if event.Type != EventOrphanConverged {
			continue
		}
		var convergence OrphanConvergence
		if err := json.Unmarshal(event.Data, &convergence); err != nil {
			t.Fatalf("decode orphan convergence: %v", err)
		}
		found = append(found, convergence)
	}
	if len(found) != 1 {
		t.Fatalf("the record holds %d orphan decisions, want exactly one: %+v", len(found), found)
	}
	return found[0]
}

func TestJanitorRequiresEventLog(t *testing.T) {
	t.Parallel()
	_, err := New(fake.New()).Sweep(context.Background(), "ws_1")
	if err == nil {
		t.Fatalf("expected missing event log error")
	}
}

func openJanitorTestLog(t *testing.T) *eventlog.SQLiteEventLog {
	t.Helper()
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func appendRunEvent(t *testing.T, log eventlog.EventLog, workspaceID, runID, eventType string) {
	t.Helper()
	_, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: workspaceID, Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            "seed:" + eventType,
		RequestHash:           "sha256:seed",
		CorrelationID:         runID,
		CausationID:           "seed",
		Events: []eventlog.NewEvent{{
			ID:            "evt_" + workspaceID + "_" + runID + "_seed",
			Type:          eventType,
			SchemaVersion: 1,
			OccurredAt:    time.Now().UTC(),
			Visibility:    eventlog.VisibilityPublic,
			Data:          []byte(`{}`),
		}},
	})
	if err != nil {
		t.Fatalf("append run event: %v", err)
	}
}

func TestJanitorReclaimsViaRecordedTerminateDisposition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_term",
		RequestHash:        "sha256:term",
		WorkspaceID:        "ws_1",
		RunID:              "run_term",
		AttemptID:          "att_term",
		OwnershipToken:     "own_term",
		LaunchKey:          "launch_term",
		CleanupLocator:     "cleanup_term",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
		Disposition:        domain.DispositionTerminate,
	})
	if err != nil {
		t.Fatalf("seed terminate orphan: %v", err)
	}
	log := openJanitorTestLog(t)
	appendLaunchIntent(t, log, "ws_1", "run_term", domain.DispositionTerminate)

	result, err := New(ad, WithEventLog(log)).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Terminated != 1 {
		t.Fatalf("expected one reclaim, got %+v", result)
	}
	if ad.TerminateCount() != 1 {
		t.Fatalf("janitor must reclaim a provisioned run via Terminate, terminate count=%d", ad.TerminateCount())
	}
	if ad.ReleaseCount() != 0 {
		t.Fatalf("janitor must not Release a provisioned run, release count=%d", ad.ReleaseCount())
	}
}

func TestJanitorRejectsCleanupWithoutRecordedDisposition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_missing_disposition",
		RequestHash:        "sha256:missing_disposition",
		WorkspaceID:        "ws_1",
		RunID:              "run_missing_disposition",
		AttemptID:          "att_missing_disposition",
		OwnershipToken:     "own_missing_disposition",
		LaunchKey:          "launch_missing_disposition",
		CleanupLocator:     "cleanup_missing_disposition",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed owned resource: %v", err)
	}
	log := openJanitorTestLog(t)
	appendLaunchIntent(t, log, "ws_1", "run_missing_disposition", "")

	if _, err := New(ad, WithEventLog(log)).Sweep(ctx, "ws_1"); err == nil {
		t.Fatal("janitor accepted cleanup without a recorded disposition")
	}
	if ad.ReleaseCount() != 0 || ad.TerminateCount() != 0 {
		t.Fatalf("invalid disposition reached provider cleanup: release=%d terminate=%d", ad.ReleaseCount(), ad.TerminateCount())
	}
}

func appendLaunchIntent(t *testing.T, log eventlog.EventLog, workspaceID, runID string, disposition domain.Disposition) {
	t.Helper()
	intent := adapter.LaunchRequest{
		AttemptID:   "att_" + runID,
		LaunchKey:   "launch_" + runID,
		Disposition: disposition,
	}
	private, err := json.Marshal(intent)
	if err != nil {
		t.Fatalf("marshal intent: %v", err)
	}
	_, err = log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: workspaceID, Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            "seed:intent:" + runID,
		RequestHash:           "sha256:seed_intent",
		CorrelationID:         runID,
		CausationID:           "seed",
		Events: []eventlog.NewEvent{
			{
				ID:            "evt_" + workspaceID + "_" + runID + "_intent",
				Type:          "compute.run.launch_intent_recorded.v1",
				SchemaVersion: 1,
				OccurredAt:    time.Now().UTC(),
				Visibility:    eventlog.VisibilityPublic,
				Data:          []byte(`{}`),
				PrivateData:   private,
			},
			{
				ID:            "evt_" + workspaceID + "_" + runID + "_cleanup",
				Type:          "compute.run.cleanup_requested.v1",
				SchemaVersion: 1,
				OccurredAt:    time.Now().UTC(),
				Visibility:    eventlog.VisibilityPublic,
				Data:          []byte(`{}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("append launch intent: %v", err)
	}
}
