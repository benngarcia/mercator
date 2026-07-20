package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/scheduler"
)

func TestIntakeImageShorthandCreatesAndAdvances(t *testing.T) {
	orch := newIntakeOrch(t, adapter.ExternalPhaseSucceeded)

	result, err := orch.Intake(context.Background(), IntakeRequest{
		WorkspaceID:    "ws_1",
		IdempotencyKey: "idem_intake_shorthand",
		Image:          "busybox:latest",
		Args:           []string{"echo", "hi"},
		ResolveImage: func(_ context.Context, image, _ string) (string, error) {
			return image + "@sha256:deadbeef", nil
		},
	})
	if err != nil {
		t.Fatalf("Intake: %v", err)
	}
	if !strings.HasPrefix(result.Run.ID, "run_") {
		t.Fatalf("expected generated run id, got %q", result.Run.ID)
	}
	if result.Run.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("outcome = %q, want succeeded", result.Run.Outcome)
	}
	events, err := orch.GetRunEvents(context.Background(), "ws_1", result.Run.ID)
	if err != nil {
		t.Fatalf("GetRunEvents: %v", err)
	}
	joined := ""
	for _, event := range events {
		joined += string(event.Data) + string(event.PrivateData)
	}
	if !strings.Contains(joined, "@sha256:deadbeef") {
		t.Fatalf("expected pinned image in events, got %s", joined)
	}
}

func TestIntakeReplayReturnsOriginalRun(t *testing.T) {
	orch := newIntakeOrch(t, adapter.ExternalPhaseSucceeded)
	req := IntakeRequest{
		WorkspaceID:    "ws_1",
		IdempotencyKey: "idem_intake_replay",
		Image:          "busybox:latest",
		ResolveImage: func(_ context.Context, image, _ string) (string, error) {
			return image + "@sha256:deadbeef", nil
		},
	}
	first, err := orch.Intake(context.Background(), req)
	if err != nil {
		t.Fatalf("first Intake: %v", err)
	}
	second, err := orch.Intake(context.Background(), req)
	if err != nil {
		t.Fatalf("second Intake: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("expected duplicate=true on replay")
	}
	if second.Run.ID != first.Run.ID {
		t.Fatalf("replay run id = %q, want %q", second.Run.ID, first.Run.ID)
	}
}

func TestIntakeFullWorkloadTakesPrecedenceOverShorthand(t *testing.T) {
	orch := newIntakeOrch(t, adapter.ExternalPhaseSucceeded)
	rev := orchRevision()
	result, err := orch.Intake(context.Background(), IntakeRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_intake_precedence",
		IdempotencyKey: "idem_intake_precedence",
		Workload:       rev,
		Image:          "ignored-shorthand",
	})
	if err != nil {
		t.Fatalf("Intake: %v", err)
	}
	events, err := orch.GetRunEvents(context.Background(), "ws_1", result.Run.ID)
	if err != nil {
		t.Fatalf("GetRunEvents: %v", err)
	}
	joined := ""
	for _, event := range events {
		joined += string(event.Data) + string(event.PrivateData)
	}
	if strings.Contains(joined, "ignored-shorthand") {
		t.Fatalf("shorthand leaked into events: %s", joined)
	}
	if !strings.Contains(joined, rev.Spec.Containers[0].Image) {
		t.Fatalf("expected workload image in events, got %s", joined)
	}
}

func TestPreviewPlacementSharesOfferQueryPathWithDecide(t *testing.T) {
	log, err := eventlog.OpenSQLite(context.Background(), "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	ad := fake.New(fake.WithListOffersError(errors.New("provider unavailable")))
	orch := New(log, scheduler.New(), ad)

	_, err = orch.PreviewPlacement(context.Background(), "ws_1", "run_preview", orchRevision())
	if !errors.Is(err, ErrOfferQuery) {
		t.Fatalf("PreviewPlacement error = %v, want ErrOfferQuery", err)
	}

	// Live decide uses the same evaluatePlacement path.
	_, err = orch.Intake(context.Background(), IntakeRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_decide_offers",
		IdempotencyKey: "idem_decide_offers",
		Workload:       orchRevision(),
	})
	if !errors.Is(err, ErrAdvanceFailed) {
		t.Fatalf("Intake advance error = %v, want ErrAdvanceFailed wrapping offer failure", err)
	}
}

func newIntakeOrch(t *testing.T, outcome adapter.ExternalPhase) *Orchestrator {
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
	now := time.Now().UTC()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", now)}),
		fake.WithLaunchOutcome(outcome),
	)
	return New(log, scheduler.New(), ad)
}
