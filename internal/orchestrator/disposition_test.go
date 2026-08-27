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

	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}

	assertRecordedDisposition(t, ctx, orch, "run_1", domain.DispositionRelease)
	if ad.ReleaseCount() != 1 {
		t.Fatalf("expected release path invoked once, got %d", ad.ReleaseCount())
	}
	if ad.TerminateCount() != 0 {
		t.Fatalf("expected terminate path never invoked, got %d", ad.TerminateCount())
	}
	record, err := orch.GetRun(ctx, "run_1")
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

// A one-shot execution Mercator allocated must record disposition=terminate on
// the launch intent and the cleanup path must invoke Terminate (not Release),
// then close the run. It is the only capacity whose cleanup destroys anything:
// the workload was the whole product, so there is no host left over to hand back.
func TestOneShotOfferRecordsTerminateDispositionAndInvokesTerminate(t *testing.T) {
	ctx := context.Background()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOneShotOffer("off_oneshot", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}

	assertRecordedDisposition(t, ctx, orch, "run_1", domain.DispositionTerminate)
	if ad.TerminateCount() != 1 {
		t.Fatalf("expected terminate path invoked once, got %d", ad.TerminateCount())
	}
	if ad.ReleaseCount() != 0 {
		t.Fatalf("expected release path never invoked, got %d", ad.ReleaseCount())
	}
	record, err := orch.GetRun(ctx, "run_1")
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

// A machine Mercator provisions to hold a Rental records disposition=release,
// because the Run ending is not the lease ending. The machine did not exist
// before this placement asked for it, and it is still not this Run's to destroy:
// cleanup takes the workload off it and the lease decides when the host goes.
//
// This is the half that reads the lane rather than the kind. Both offers here
// are provisionable, and the one above is destroyed while this one is not.
func TestAProvisionedRentalRecordsReleaseAndLeavesItsHostStanding(t *testing.T) {
	ctx := context.Background()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchProvisionableOffer("off_prov", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}

	assertRecordedDisposition(t, ctx, orch, "run_1", domain.DispositionRelease)
	if ad.TerminateCount() != 0 {
		t.Fatalf("the Run ended and its host was destroyed %d times, and the lease is what decides that", ad.TerminateCount())
	}
	if ad.ReleaseCount() != 1 {
		t.Fatalf("expected the workload released once, got %d", ad.ReleaseCount())
	}
	record, err := orch.GetRun(ctx, "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Disposition != domain.DispositionRelease {
		t.Fatalf("expected record disposition release, got %q", record.Disposition)
	}
}

// The load-bearing invariant: cleanup dispatches on the RECORDED disposition,
// never re-inferred from live offers. Here the launch records terminate from a
// one-shot offer and stays running; then ALL offers disappear before
// cleanup is triggered (via cancel, whose cleanup path never consults offers).
// Cleanup must still invoke Terminate because that is what was recorded.
func TestCleanupDispatchesOnRecordedDispositionNotLiveOffers(t *testing.T) {
	ctx := context.Background()
	base := fake.New(fake.WithLaunchOutcome(adapter.ExternalPhaseRunning))
	ad := &offerDisappearingAdapter{
		Adapter: base,
		offers:  []domain.OfferSnapshot{orchOneShotOffer("off_oneshot", time.Now().UTC())},
	}
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	// First advance: decide on the one-shot offer (records terminate) and
	// launch (stays running).
	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("first advance: %v", err)
	}
	assertRecordedDisposition(t, ctx, orch, "run_1", domain.DispositionTerminate)

	// Offers vanish entirely. Cancel drives the run terminal and through cleanup
	// without ever re-listing offers.
	ad.offers = nil
	if _, err := orch.CancelRun(ctx, "run_1", nil); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	if base.TerminateCount() != 1 {
		t.Fatalf("cleanup must dispatch terminate from RECORDED disposition even with no live offers, terminate count=%d", base.TerminateCount())
	}
	if base.ReleaseCount() != 0 {
		t.Fatalf("cleanup must not fall back to release, release count=%d", base.ReleaseCount())
	}
	record, err := orch.GetRun(ctx, "run_1")
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
	}

	if err := orch.releaseAndClose(ctx, "run_missing_disposition", 0, intent); err == nil {
		t.Fatal("releaseAndClose accepted a missing recorded disposition")
	}
	if ad.ReleaseCount() != 0 || ad.TerminateCount() != 0 {
		t.Fatalf("missing disposition reached provider cleanup: release=%d terminate=%d", ad.ReleaseCount(), ad.TerminateCount())
	}
}

func assertRecordedDisposition(t *testing.T, ctx context.Context, orch *Orchestrator, runID string, want domain.Disposition) {
	t.Helper()
	events, err := orch.GetRunEvents(ctx, runID)
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

// CollectOffers is this double answering as the whole fleet. Every double that
// states its own offers has to state its own census too: Go resolves an embedded
// method against the embedded value, so a census inherited from the fake adapter
// would answer about offers this double does not publish.
func (o *offerDisappearingAdapter) CollectOffers(ctx context.Context, req adapter.OfferRequest) (adapter.OfferCollection, error) {
	offers, err := o.ListOffers(ctx, req)
	if err != nil {
		return adapter.OfferCollection{}, err
	}
	return adapter.OfferCollection{Offers: offers, Queried: []string{fake.ConnectionID}}, nil
}
