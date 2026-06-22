package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/benngarcia/mercator/internal/adapter"
)

func TestFakeAdapterLaunchIsIdempotentAndDetectsConflicts(t *testing.T) {
	ctx := context.Background()
	ad := New()
	req := adapter.LaunchRequest{
		OperationKey:   "launch_run_1_att_1",
		RequestHash:    "sha256:launch",
		WorkspaceID:    "ws_1",
		RunID:          "run_1",
		AttemptID:      "att_1",
		OwnershipToken: "own_1",
		LaunchKey:      "launch_key_1",
		CleanupLocator: "cleanup_key_1",
		Image:          "ghcr.io/acme/inference@sha256:0000000000000000000000000000000000000000000000000000000000000000",
	}

	first, err := ad.Launch(ctx, req)
	if err != nil {
		t.Fatalf("first launch: %v", err)
	}
	replay, err := ad.Launch(ctx, req)
	if err != nil {
		t.Fatalf("idempotent launch: %v", err)
	}
	if first.ExternalID != replay.ExternalID || !replay.Duplicate {
		t.Fatalf("expected duplicate receipt for same launch, first=%+v replay=%+v", first, replay)
	}
	if first.CleanupLocator != "cleanup_key_1" || first.OwnershipToken != "own_1" {
		t.Fatalf("launch receipt missing cleanup locator or ownership token: %+v", first)
	}

	conflict := req
	conflict.RequestHash = "sha256:different"
	_, err = ad.Launch(ctx, conflict)
	if !errors.Is(err, adapter.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}

	sameLaunchDifferentOperation := req
	sameLaunchDifferentOperation.OperationKey = "launch_run_1_retry"
	sameLaunchDifferentOperation.RequestHash = "sha256:different-operation"
	_, err = ad.Launch(ctx, sameLaunchDifferentOperation)
	if !errors.Is(err, adapter.ErrIdempotencyConflict) {
		t.Fatalf("expected launch-key conflict for different operation key, got %v", err)
	}
}

func TestFakeAdapterObserveReleaseAndListOwned(t *testing.T) {
	ctx := context.Background()
	ad := New(WithLaunchOutcome(adapter.ExternalPhaseSucceeded))
	req := adapter.LaunchRequest{
		OperationKey:   "launch_run_1_att_1",
		RequestHash:    "sha256:launch",
		WorkspaceID:    "ws_1",
		RunID:          "run_1",
		AttemptID:      "att_1",
		OwnershipToken: "own_1",
		LaunchKey:      "launch_key_1",
		CleanupLocator: "cleanup_key_1",
		Image:          "ghcr.io/acme/inference@sha256:0000000000000000000000000000000000000000000000000000000000000000",
	}
	receipt, err := ad.Launch(ctx, req)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 1 || owned[0].OwnershipToken != "own_1" {
		t.Fatalf("unexpected owned objects: %+v", owned)
	}

	observation, err := ad.Observe(ctx, adapter.ObserveRequest{LaunchKey: req.LaunchKey})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observation.Phase != adapter.ExternalPhaseSucceeded || observation.ExternalID != receipt.ExternalID {
		t.Fatalf("unexpected observation: %+v", observation)
	}

	release, err := ad.Release(ctx, adapter.ReleaseRequest{OperationKey: "release_1", RequestHash: "sha256:release", LaunchKey: req.LaunchKey})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !release.Released {
		t.Fatalf("expected release receipt, got %+v", release)
	}
	releaseAgain, err := ad.Release(ctx, adapter.ReleaseRequest{OperationKey: "release_1", RequestHash: "sha256:release", LaunchKey: req.LaunchKey})
	if err != nil {
		t.Fatalf("release again: %v", err)
	}
	if !releaseAgain.Duplicate {
		t.Fatalf("expected duplicate release receipt, got %+v", releaseAgain)
	}
	owned, err = ad.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned after release: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("expected no owned objects after release, got %+v", owned)
	}

	absent, err := ad.Release(ctx, adapter.ReleaseRequest{OperationKey: "release_absent", RequestHash: "sha256:release-absent", LaunchKey: "missing"})
	if err != nil {
		t.Fatalf("release absent: %v", err)
	}
	if !absent.Released {
		t.Fatalf("release of absent resource should succeed, got %+v", absent)
	}
}
