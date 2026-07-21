package orchestrator

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/scheduler"
)

// TestAdvanceOpenRunsClosesExitedRunWithoutClientRefresh documents the broker
// gap the background sweep closes, red half then green half in one test. A run
// whose container has exited stays OPEN forever unless a client calls /refresh
// or /wait — the first assertion pins that stuck-open state (the run's final
// state before this change). One AdvanceOpenRuns sweep, with zero client
// involvement, must then observe the exit, record the outcome, confirm
// cleanup, and close the run; a follow-up sweep must find nothing open.
func TestAdvanceOpenRunsClosesExitedRunWithoutClientRefresh(t *testing.T) {
	ctx := context.Background()
	// The container exits successfully, but only AFTER the create-time advance
	// has already observed it running once (WithOpenObservations(1)).
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
		fake.WithOpenObservations(1),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)
	// The create-time advance (what the HTTP layer runs once on POST /v1/runs)
	// launches the container and observes it still running.
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("create-time advance: %v", err)
	}

	// RED half: the container has now exited (the fake's next observation is
	// terminal), but no client ever calls /refresh or /wait, so nothing
	// advances the run — it is stuck open.
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Closed {
		t.Fatalf("run closed before any sweep; the stuck-open precondition did not hold: %+v", record)
	}

	// GREEN half: one background sweep converges the run to closed.
	result, err := orch.AdvanceOpenRuns(ctx, "ws_1")
	if err != nil {
		t.Fatalf("advance open runs: %v", err)
	}
	if result.Open != 1 || result.Closed != 1 {
		t.Fatalf("expected the one open run to close, got %+v", result)
	}
	record, err = orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run after sweep: %v", err)
	}
	if !record.Closed {
		t.Fatalf("run still open after sweep: %+v", record)
	}
	if record.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("unexpected outcome after sweep: %+v", record)
	}
	if ad.ReleaseCount() != 1 {
		t.Fatalf("sweep must release the exited container, release count=%d", ad.ReleaseCount())
	}

	// A closed run must cost the next sweep nothing: no open runs to advance.
	result, err = orch.AdvanceOpenRuns(ctx, "ws_1")
	if err != nil {
		t.Fatalf("idle sweep: %v", err)
	}
	if result.Open != 0 || result.Closed != 0 {
		t.Fatalf("idle sweep should find nothing open, got %+v", result)
	}
}

// TestAdvanceOpenRunsContinuesPastFailingRun proves one wedged run cannot
// starve the rest of the workspace: a run that always fails to advance is
// reported in the joined error while the healthy exited run behind it still
// closes on the same sweep.
func TestAdvanceOpenRunsContinuesPastFailingRun(t *testing.T) {
	ctx := context.Background()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	orch := newTestOrchestrator(t, ad)
	// Seed a wedged run FIRST so the sweep hits it before the healthy run:
	// cleanup requested without a recorded launch intent makes AdvanceRun fail
	// on every attempt.
	seedWedgedRun(t, orch.log, "ws_1", "run_wedged")
	createRun(t, ctx, orch)

	result, err := orch.AdvanceOpenRuns(ctx, "ws_1")
	if err == nil || !strings.Contains(err.Error(), "run_wedged") {
		t.Fatalf("expected joined error naming run_wedged, got %v", err)
	}
	if result.Open != 2 || result.Closed != 1 {
		t.Fatalf("expected the healthy run to close despite the wedged one, got %+v", result)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get healthy run: %v", err)
	}
	if !record.Closed {
		t.Fatalf("healthy run must close even when an earlier run errors: %+v", record)
	}
}

func TestListRunWorkspacesDiscoversPersistedPartitions(t *testing.T) {
	ctx := context.Background()
	log, err := eventlog.OpenSQLite(ctx, "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	orch := New(workspaceTestLog{EventLog: log}, scheduler.New(), fake.New())

	seedWedgedRun(t, log, "staging-experiments", "run_exp")
	seedWedgedRun(t, log, "staging", "run_released")
	seedWedgedRun(t, log, "staging", "run_second")

	workspaces, err := orch.ListRunWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list run workspaces: %v", err)
	}
	if !slices.Equal(workspaces, []string{"staging", "staging-experiments"}) {
		t.Fatalf("workspaces = %v, want persisted run partitions", workspaces)
	}
}

// seedWedgedRun appends a run stream whose cleanup was requested but whose
// launch intent was never recorded, so AdvanceRun fails deterministically.
func seedWedgedRun(t *testing.T, log eventlog.EventLog, workspaceID, runID string) {
	t.Helper()
	_, err := log.Append(context.Background(), eventlog.AppendRequest{
		Stream:                eventlog.StreamKey{WorkspaceID: workspaceID, Type: "run", ID: runID},
		ExpectedStreamVersion: 0,
		CommandKey:            "seed:wedged:" + runID,
		RequestHash:           "sha256:seed_wedged",
		CorrelationID:         runID,
		CausationID:           "seed",
		Events: []eventlog.NewEvent{
			{
				ID:            "evt_" + workspaceID + "_" + runID + "_requested",
				Type:          EventRunRequested,
				SchemaVersion: 1,
				OccurredAt:    time.Now().UTC(),
				Visibility:    eventlog.VisibilityPublic,
				Data:          []byte(`{}`),
			},
			{
				ID:            "evt_" + workspaceID + "_" + runID + "_cleanup",
				Type:          EventCleanupRequested,
				SchemaVersion: 1,
				OccurredAt:    time.Now().UTC(),
				Visibility:    eventlog.VisibilityPublic,
				Data:          []byte(`{}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("seed wedged run: %v", err)
	}
}
