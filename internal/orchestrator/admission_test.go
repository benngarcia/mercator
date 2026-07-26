package orchestrator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

// TestARunNothingCanTakeWaitsInsteadOfSpinning is the loop the queue left behind.
// Admission turned a Run no candidate would take from an error into a deferral,
// and a deferral the record already carries appends nothing, so a placement that
// reported progress made AdvanceRun re-derive the same state and defer again
// without end. One submission to a control plane with no capacity spun a core
// inside its own request, holding that Run's lock, and never answered.
//
// The bound is what states the claim: the fixed loop stops in milliseconds, and
// the broken one only stops when the deadline kills the query it is in the middle
// of, which is what makes this a failure rather than a hang.
func TestARunNothingCanTakeWaitsInsteadOfSpinning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	orch := newTestOrchestrator(t, fake.New())

	if _, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_unplaceable",
		IdempotencyKey: "idem_unplaceable",
		Workload:       orchRevision(),
	}); err != nil {
		t.Fatalf("submit a Run to a control plane with no capacity: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_unplaceable"); err != nil {
		t.Fatalf("advance a Run nothing can take: %v", err)
	}

	events, err := orch.GetRunEvents(ctx, "ws_1", "run_unplaceable")
	if err != nil {
		t.Fatalf("read the Run's stream: %v", err)
	}
	// One wait, stated once. A Run told to wait for the same reason on every pass
	// is a Run whose own stream an operator cannot read.
	if deferrals := countEvents(events, EventAdmissionDeferred); deferrals != 1 {
		t.Fatalf("the Run recorded %d deferrals over two advances, and it has waited once for one reason: %v",
			deferrals, eventTypes(events))
	}
	if reason := deferralReason(t, events); reason != domain.DeferredNoFeasibleOffer {
		t.Fatalf("the Run waits for %q, and nothing in this control plane could take it", reason)
	}
	if closed := countEvents(events, EventRunClosed); closed != 0 {
		t.Fatalf("a Run waiting for capacity was closed: %v", eventTypes(events))
	}
}

// deferralReason is what the last thing admission said about a Run says it is
// waiting for, read off the Run's own stream.
func deferralReason(t *testing.T, events []eventlog.StoredEvent) string {
	t.Helper()
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type != EventAdmissionDeferred {
			continue
		}
		var data admissionDeferredData
		if err := json.Unmarshal(events[index].Data, &data); err != nil {
			t.Fatalf("read the deferral: %v", err)
		}
		return data.Deferral.Reason
	}
	t.Fatalf("admission recorded nothing about this Run: %v", eventTypes(events))
	return ""
}
