package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
)

// Item 1: exit_code + outcome surfaced on the Run read model.
func TestGetRunSurfacesExitCode(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	))
	req := CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_exit",
		CommandKey:     "cmd_exit",
		IdempotencyKey: "idem_exit",
		Workload:       orchRevision(),
	}
	if _, err := orch.CreateRun(ctx, req); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_exit"); err != nil {
		t.Fatalf("advance: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_exit")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("expected succeeded outcome, got %q", record.Outcome)
	}
	if record.ExitCode == nil {
		t.Fatalf("expected exit_code to be surfaced on the run record, got nil")
	}
	if *record.ExitCode != 0 {
		t.Fatalf("expected exit_code 0, got %d", *record.ExitCode)
	}
}

// Item 4: a logical retry that regenerates the cosmetic revision id must replay,
// not 409, when the same command key is reused.
func TestCreateRunReplayIgnoresCosmeticRevisionID(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})))

	rev := orchRevision()
	rev.ID = "wrev_minted_aaaa"
	first, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_replay",
		IdempotencyKey: "idem_replay",
		Workload:       rev,
	})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Logical retry of the SAME run: identical run_id + idempotency key,
	// but the client regenerated the cosmetic revision id.
	rev2 := orchRevision()
	rev2.ID = "wrev_minted_bbbb"
	second, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_replay",
		IdempotencyKey: "idem_replay",
		Workload:       rev2,
	})
	if err != nil {
		t.Fatalf("logical retry should replay, not error: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("expected duplicate replay, got %+v", second)
	}
	if first.RunID != second.RunID {
		t.Fatalf("replay returned different run id: %q vs %q", first.RunID, second.RunID)
	}
}

// C: the submitted tag-form image is pinned to a resolved digest in the stored
// revision, while the request hash is computed over the SUBMITTED spec so a
// moving tag stays replay-stable.
func TestCreateRunPinsResolvedDigestAfterHashing(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	))

	rev := orchRevision()
	rev.Spec.Containers[0].Image = "busybox" // tag form, no digest

	pinned := "docker.io/library/busybox@sha256:" + strings.Repeat("a", 64)
	resolveCalls := 0
	_, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_pin",
		IdempotencyKey: "idem_pin",
		Workload:       rev,
		ResolveImage: func(_ context.Context, image, platform string) (string, string, error) {
			resolveCalls++
			if image != "busybox" {
				t.Fatalf("resolver should see the submitted tag, got %q", image)
			}
			if platform != "linux/amd64" {
				t.Fatalf("resolver should see the stated platform, got %q", platform)
			}
			return pinned, platform, nil
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resolveCalls != 1 {
		t.Fatalf("expected resolver called once, got %d", resolveCalls)
	}

	// The stored revision must carry the pinned digest reference.
	events, err := orch.GetRunEvents(ctx, "ws_1", "run_pin")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	stored, err := decodeRunRequested(events)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := stored.Workload.Spec.Containers[0].Image; got != pinned {
		t.Fatalf("stored image not pinned: got %q want %q", got, pinned)
	}
}

// D: a logical retry that regenerates the run_id (server-generated) replays to
// the ORIGINAL run id, not a freshly minted one, when the Idempotency-Key matches.
func TestCreateRunReplayReturnsOriginalGeneratedRunID(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})))

	first, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_gen_original",
		GeneratedRunID: true,
		IdempotencyKey: "idem_gen",
		Workload:       orchRevision(),
	})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Retry with the SAME idempotency key but a DIFFERENT generated run_id.
	second, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_gen_DIFFERENT",
		GeneratedRunID: true,
		IdempotencyKey: "idem_gen",
		Workload:       orchRevision(),
	})
	if err != nil {
		t.Fatalf("replay should not error: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("expected duplicate replay, got %+v", second)
	}
	if second.RunID != first.RunID {
		t.Fatalf("replay returned a new run id %q; want original %q", second.RunID, first.RunID)
	}
}

// C (failure path): a resolver error surfaces a coded IMAGE_RESOLUTION_FAILED error.
func TestCreateRunSurfacesResolutionFailure(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})))
	rev := orchRevision()
	rev.Spec.Containers[0].Image = "busybox"
	_, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_resolve_fail",
		IdempotencyKey: "idem_resolve_fail",
		Workload:       rev,
		ResolveImage: func(context.Context, string, string) (string, string, error) {
			return "", "", fmt.Errorf("registry unreachable")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "IMAGE_RESOLUTION_FAILED") {
		t.Fatalf("expected IMAGE_RESOLUTION_FAILED, got %v", err)
	}
}
