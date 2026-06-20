package janitor

import (
	"context"
	"testing"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/adapter/fake"
)

func TestJanitorReleasesOwnedResources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ad := fake.New()
	_, err := ad.Launch(ctx, adapter.LaunchRequest{
		OperationKey:       "launch_orphan",
		RequestHash:        "sha256:orphan",
		WorkspaceID:        "ws_1",
		RunID:              "run_orphan",
		AttemptID:          "att_orphan",
		OwnershipToken:     "own_orphan",
		LaunchKey:          "launch_orphan",
		CleanupLocator:     "cleanup_orphan",
		WorkloadID:         "wl_1",
		WorkloadRevisionID: "wrev_1",
	})
	if err != nil {
		t.Fatalf("seed orphan: %v", err)
	}

	result, err := New(ad).Sweep(ctx, "ws_1")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.Found != 1 || result.Released != 1 {
		t.Fatalf("unexpected sweep result: %+v", result)
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("expected owned resources released, got %+v", owned)
	}
}
