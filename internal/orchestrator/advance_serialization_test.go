package orchestrator

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
)

func TestAdvanceRunSerializesConcurrentLaunchForSameRun(t *testing.T) {
	ctx := context.Background()
	ad := newBlockingLaunchAdapter(fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})))
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	firstErr := make(chan error, 1)
	go func() {
		firstErr <- orch.AdvanceRun(ctx, "ws_1", "run_1")
	}()

	select {
	case <-ad.firstLaunchStarted:
	case <-time.After(time.Second):
		t.Fatal("first AdvanceRun did not enter adapter Launch")
	}

	secondErr := make(chan error, 1)
	go func() {
		secondErr <- orch.AdvanceRun(ctx, "ws_1", "run_1")
	}()

	select {
	case <-ad.overlappedLaunch:
	case <-time.After(100 * time.Millisecond):
	}

	close(ad.releaseFirstLaunch)

	if err := <-firstErr; err != nil {
		t.Fatalf("first advance: %v", err)
	}
	if err := <-secondErr; err != nil {
		t.Fatalf("second advance: %v", err)
	}
	if launches := ad.launchCalls.Load(); launches != 1 {
		t.Fatalf("expected exactly one adapter Launch call for the run, got %d", launches)
	}
	if ad.overlapped.Load() {
		t.Fatal("concurrent AdvanceRun calls overlapped inside adapter Launch")
	}

	events, err := orch.GetRunEvents(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if got := countEvents(events, EventLaunchAccepted); got != 1 {
		t.Fatalf("expected one launch_accepted event, got %d in %v", got, eventTypes(events))
	}
	if got := countEvents(events, EventLaunchFailed); got != 0 {
		t.Fatalf("expected no launch_failed events, got %d in %v", got, eventTypes(events))
	}
}

func TestTerminalReportWaitsForRunAdvancement(t *testing.T) {
	ctx := context.Background()
	ad := newBlockingLaunchAdapter(fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})))
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	advanceErr := make(chan error, 1)
	go func() {
		advanceErr <- orch.AdvanceRun(ctx, "ws_1", "run_1")
	}()
	select {
	case <-ad.firstLaunchStarted:
	case <-time.After(time.Second):
		t.Fatal("AdvanceRun did not enter adapter Launch")
	}

	reportStarted := make(chan struct{})
	reportErr := make(chan error, 1)
	go func() {
		close(reportStarted)
		reportErr <- orch.RecordReport(ctx, "ws_1", "run_1", mustRunReport(t, "exit", nil, intPtr(0)))
	}()
	<-reportStarted
	select {
	case err := <-reportErr:
		t.Fatalf("terminal report returned while launch advancement owned the run: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(ad.releaseFirstLaunch)
	if err := <-advanceErr; err != nil {
		t.Fatalf("advance run: %v", err)
	}
	if err := <-reportErr; err != nil {
		t.Fatalf("record terminal report: %v", err)
	}
	if _, err := orch.AdvanceOpenRuns(ctx, "ws_1"); err != nil {
		t.Fatalf("reconcile terminal report: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if !record.Closed || record.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("run = %+v, want closed successful run", record)
	}
}

type blockingLaunchAdapter struct {
	*fake.Adapter
	inFlight           atomic.Int32
	launchCalls        atomic.Int32
	overlapped         atomic.Bool
	firstLaunchStarted chan struct{}
	releaseFirstLaunch chan struct{}
	overlappedLaunch   chan struct{}
}

func newBlockingLaunchAdapter(ad *fake.Adapter) *blockingLaunchAdapter {
	return &blockingLaunchAdapter{
		Adapter:            ad,
		firstLaunchStarted: make(chan struct{}),
		releaseFirstLaunch: make(chan struct{}),
		overlappedLaunch:   make(chan struct{}),
	}
}

func (b *blockingLaunchAdapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	inFlight := b.inFlight.Add(1)
	defer b.inFlight.Add(-1)

	if inFlight > 1 {
		if b.overlapped.CompareAndSwap(false, true) {
			close(b.overlappedLaunch)
		}
	}

	call := b.launchCalls.Add(1)
	if call == 1 {
		close(b.firstLaunchStarted)
		select {
		case <-b.releaseFirstLaunch:
		case <-ctx.Done():
			return adapter.LaunchReceipt{}, ctx.Err()
		}
	}

	return b.Adapter.Launch(ctx, req)
}
