package orchestrator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
)

// A standing offer must record disposition=release on the launch intent and the
// cleanup path must invoke Release (not Terminate), then close the run.
func TestStandingOfferRecordsReleaseDispositionAndInvokesRelease(t *testing.T) {
	ctx := context.Background()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_standing", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}

	assertRecordedDisposition(t, ctx, orch, "ws_1", "run_1", domain.DispositionRelease)
	if ad.ReleaseCount() != 1 {
		t.Fatalf("expected release path invoked once, got %d", ad.ReleaseCount())
	}
	if ad.TerminateCount() != 0 {
		t.Fatalf("expected terminate path never invoked, got %d", ad.TerminateCount())
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if !record.Closed || record.Cleanup != domain.CleanupConfirmed {
		t.Fatalf("expected closed+confirmed, got closed=%v cleanup=%q", record.Closed, record.Cleanup)
	}
	if record.Disposition != domain.DispositionRelease {
		t.Fatalf("expected record disposition release, got %q", record.Disposition)
	}
}

// A provisionable offer must record disposition=terminate on the launch intent
// and the cleanup path must invoke Terminate (not Release), then close the run.
func TestProvisionableOfferRecordsTerminateDispositionAndInvokesTerminate(t *testing.T) {
	ctx := context.Background()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchProvisionableOffer("off_prov", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}

	assertRecordedDisposition(t, ctx, orch, "ws_1", "run_1", domain.DispositionTerminate)
	if ad.TerminateCount() != 1 {
		t.Fatalf("expected terminate path invoked once, got %d", ad.TerminateCount())
	}
	if ad.ReleaseCount() != 0 {
		t.Fatalf("expected release path never invoked, got %d", ad.ReleaseCount())
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if !record.Closed || record.Cleanup != domain.CleanupConfirmed {
		t.Fatalf("expected closed+confirmed, got closed=%v cleanup=%q", record.Closed, record.Cleanup)
	}
	if record.Disposition != domain.DispositionTerminate {
		t.Fatalf("expected record disposition terminate, got %q", record.Disposition)
	}
}

// The load-bearing invariant: cleanup dispatches on the RECORDED disposition,
// never re-inferred from live offers. Here the launch records terminate from a
// provisionable offer and stays running; then ALL offers disappear before
// cleanup is triggered (via cancel, whose cleanup path never consults offers).
// Cleanup must still invoke Terminate because that is what was recorded.
func TestCleanupDispatchesOnRecordedDispositionNotLiveOffers(t *testing.T) {
	ctx := context.Background()
	base := fake.New(fake.WithLaunchOutcome(adapter.ExternalPhaseRunning))
	ad := &offerDisappearingAdapter{
		Adapter: base,
		offers:  []domain.OfferSnapshot{orchProvisionableOffer("off_prov", time.Now().UTC())},
	}
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	// First advance: decide on the provisionable offer (records terminate) and
	// launch (stays running).
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("first advance: %v", err)
	}
	assertRecordedDisposition(t, ctx, orch, "ws_1", "run_1", domain.DispositionTerminate)

	// Offers vanish entirely. Cancel drives the run terminal and through cleanup
	// without ever re-listing offers.
	ad.offers = nil
	if _, err := orch.CancelRun(ctx, "ws_1", "run_1", nil); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	if base.TerminateCount() != 1 {
		t.Fatalf("cleanup must dispatch terminate from RECORDED disposition even with no live offers, terminate count=%d", base.TerminateCount())
	}
	if base.ReleaseCount() != 0 {
		t.Fatalf("cleanup must not fall back to release, release count=%d", base.ReleaseCount())
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if !record.Closed {
		t.Fatalf("expected run closed after recorded-disposition cleanup")
	}
}

func TestMissingRecordedDispositionFailsBeforeProviderCleanup(t *testing.T) {
	ctx := context.Background()
	ad := fake.New()
	orch := newTestOrchestrator(t, ad)

	intent := &adapter.LaunchRequest{
		AttemptID:      "att_legacy",
		LaunchKey:      "launch_att_legacy",
		OwnershipToken: "own_att_legacy",
		RunID:          "run_legacy",
		WorkspaceID:    "ws_1",
	}

	if err := orch.releaseAndClose(ctx, "ws_1", "run_missing_disposition", 0, intent); err == nil {
		t.Fatal("releaseAndClose accepted a missing recorded disposition")
	}
	if ad.ReleaseCount() != 0 || ad.TerminateCount() != 0 {
		t.Fatalf("missing disposition reached provider cleanup: release=%d terminate=%d", ad.ReleaseCount(), ad.TerminateCount())
	}
}

func assertRecordedDisposition(t *testing.T, ctx context.Context, orch *Orchestrator, workspaceID, runID string, want domain.Disposition) {
	t.Helper()
	events, err := orch.GetRunEvents(ctx, workspaceID, runID)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	for _, event := range events {
		if event.Type != EventLaunchIntentRecorded {
			continue
		}
		payload := event.PrivateData
		if len(payload) == 0 {
			payload = event.Data
		}
		var intent adapter.LaunchRequest
		if err := json.Unmarshal(payload, &intent); err != nil {
			t.Fatalf("decode launch intent: %v", err)
		}
		if intent.Disposition != want {
			t.Fatalf("recorded disposition = %q, want %q", intent.Disposition, want)
		}
		return
	}
	t.Fatalf("no launch_intent_recorded event found in %s", eventTypes(events))
}

// offerDisappearingAdapter lets a test make offers vanish between advances while
// keeping launch/cleanup tracking from the embedded fake.
type offerDisappearingAdapter struct {
	*fake.Adapter
	offers []domain.OfferSnapshot
}

func (o *offerDisappearingAdapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return append([]domain.OfferSnapshot(nil), o.offers...), nil
}
