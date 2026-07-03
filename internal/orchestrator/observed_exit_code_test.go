package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

// TestObservedRunningExitCodeDoesNotFinalizeRun pins the fix for the
// background sweep killing live runs. Docker inspect reports
// .State.ExitCode == 0 while a container is still running; v0.2.0 recorded
// that on the running observation and treated any recorded exit code as an
// authoritative exit on the next advance — closing the run and reclaiming
// the live container seconds after launch. A running observation carrying an
// exit code (from an old adapter, or already sitting in an existing event
// log) must not finalize the run; only an exited phase makes the observed
// code authoritative.
func TestObservedRunningExitCodeDoesNotFinalizeRun(t *testing.T) {
	ctx := context.Background()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
		fake.WithOpenObservations(2),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("create-time advance: %v", err)
	}

	// Mimic the v0.2.0 docker adapter's wire data (and the events real logs
	// already contain): a RUNNING observation carrying exit_code 0.
	events, err := orch.log.ReadStream(ctx, runStream("ws_1", "run_1"), 0, 1000)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	err = orch.appendEvents(ctx, "ws_1", "run_1", uint64(len(events)), "test:poisoned-observation", []eventlog.NewEvent{
		mustEvent("run_1", "poisoned_running_observation", EventExternalStateObserved, map[string]any{
			"phase":       "running",
			"exit_code":   0,
			"observed_at": time.Now().UTC(),
		}, time.Now().UTC()),
	})
	if err != nil {
		t.Fatalf("append poisoned observation: %v", err)
	}

	// A sweep over the poisoned log must leave the run open and the
	// container untouched (pre-fix: outcome succeeded + release here).
	result, err := orch.AdvanceOpenRuns(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Closed != 0 {
		t.Fatalf("sweep finalized a running container: %+v", result)
	}
	if ad.ReleaseCount() != 0 {
		t.Fatalf("sweep reclaimed a live container, release count=%d", ad.ReleaseCount())
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Closed {
		t.Fatalf("run closed off a running observation: %+v", record)
	}

	// Once the container actually exits, the next sweep closes normally.
	if _, err := orch.AdvanceOpenRuns(ctx, "ws_1"); err != nil {
		t.Fatalf("terminal sweep: %v", err)
	}
	record, err = orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run after terminal sweep: %v", err)
	}
	if !record.Closed || record.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("run did not close on the real exit: %+v", record)
	}
	if ad.ReleaseCount() != 1 {
		t.Fatalf("exited container not released, release count=%d", ad.ReleaseCount())
	}
}
