package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
)

// TestARunRecordsTheStartMomentItsHolderPublished is the moment every start
// latency in the record is measured to. A provider that publishes one on a clock
// Mercator shares is believed, and the Run carries it.
func TestARunRecordsTheStartMomentItsHolderPublished(t *testing.T) {
	ctx := context.Background()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithPublishedStart(0),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}

	record, err := orch.GetRun(ctx, "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.StartedAt == nil {
		t.Fatalf("the provider published a start moment and the Run records none: %+v", record)
	}
}

// TestAStartAheadOfTheReadThatCarriedItIsNotThisRunsStart is the host whose clock
// runs ahead. The moment it publishes is one Mercator cannot have observed, because
// Mercator read it before it happened, and adopting it would file a start latency an
// hour too large as a measurement and blame Mercator in the Lab's own start rule for
// a moment it only passed through. The provider is still recorded saying it: the
// observation carries the claim, and only the Run's own start is refused.
func TestAStartAheadOfTheReadThatCarriedItIsNotThisRunsStart(t *testing.T) {
	ctx := context.Background()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithPublishedStart(time.Hour),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}

	record, err := orch.GetRun(ctx, "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.StartedAt != nil {
		t.Fatalf("the Run records a start of %s, which its holder published an hour ahead of the read that carried it",
			record.StartedAt.Format(time.RFC3339Nano))
	}
	events, err := orch.log.ReadStream(ctx, runStream("run_1"), 0, 1000)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if countEvents(events, EventExecutionStarted) != 0 {
		t.Fatalf("the run stream records a start nothing observed: %v", eventTypes(events))
	}
	if countEvents(events, EventExternalStateObserved) == 0 {
		t.Fatalf("the observation that carried the claim was not recorded: %v", eventTypes(events))
	}
}

// TestAQueuedObservationDoesNotStartAWorkload is the provider that says running
// from the moment it accepts. RunPod publishes lastStartedAt while the image is
// still landing, and an address is what makes a pod running here; a moment carried
// by a phase saying the work has not begun is that same claim in another field, so
// a Run whose holder has not started it has no start.
func TestAQueuedObservationDoesNotStartAWorkload(t *testing.T) {
	ctx := context.Background()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseQueued),
		fake.WithPublishedStart(0),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}

	record, err := orch.GetRun(ctx, "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.StartedAt != nil {
		t.Fatalf("a Run its holder reports queued records a start of %s", record.StartedAt.Format(time.RFC3339Nano))
	}
}
