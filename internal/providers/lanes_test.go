package providers_test

import (
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/providers"
)

// TestEveryProductionBackendDeclaresTheLaneItCanActuallyServe is the standing
// guard against the conflation this package split apart. A backend lands in the
// reusable lane only by allocating machine capacity that outlives the workload
// run on it, so this test fails the moment a one-shot product claims reuse.
func TestEveryProductionBackendDeclaresTheLaneItCanActuallyServe(t *testing.T) {
	declarations, err := providers.Factory().Declarations()
	if err != nil {
		t.Fatalf("declare the production catalog: %v", err)
	}

	if len(declarations) == 0 {
		t.Fatal("the production catalog declared no backends")
	}
	for _, declaration := range declarations {
		if !declaration.Lane.Valid() {
			t.Errorf("%s declared an unknown lane %q", declaration.Type, declaration.Lane)
		}
		if declaration.Lane.Reusable() && declaration.Capacity == nil {
			t.Errorf("%s claims the reusable lane while allocating no capacity to reuse", declaration.Type)
		}
		if declaration.Ephemeral != nil && declaration.Ephemeral.ReusableBetweenRuns {
			t.Errorf("%s is an EphemeralExecutor claiming reuse between Runs", declaration.Type)
		}
	}
}

// TestTodaysBackendsAreAllOneShot records where the migration actually stands.
// Every current backend creates capacity for one workload and destroys it
// afterwards. Shadeform and Vast join the reusable lane when they implement
// CapacityProvider, and their machines become candidates when an agent enrolls on
// one. Updating this list is the deliberate act of promoting a backend.
func TestTodaysBackendsAreAllOneShot(t *testing.T) {
	declarations, err := providers.Factory().Declarations()
	if err != nil {
		t.Fatalf("declare the production catalog: %v", err)
	}

	lanes := map[string]domain.ExecutionLane{}
	for _, declaration := range declarations {
		lanes[declaration.Type] = declaration.Lane
	}
	want := map[string]domain.ExecutionLane{
		"docker":    domain.LaneEphemeral,
		"runpod":    domain.LaneEphemeral,
		"shadeform": domain.LaneEphemeral,
		"vast":      domain.LaneEphemeral,
	}
	for adapterType, wantLane := range want {
		if lanes[adapterType] != wantLane {
			t.Errorf("%s lane = %q, want %q", adapterType, lanes[adapterType], wantLane)
		}
	}
	if len(lanes) != len(want) {
		t.Errorf("catalog has %d backends, this test states %d", len(lanes), len(want))
	}
}

// TestEphemeralBackendsReportTheirWeakerGuaranteesExplicitly holds the lane to
// its own contract: an ephemeral executor states what it cannot do rather than
// leaving a caller to infer it from silence.
func TestEphemeralBackendsReportTheirWeakerGuaranteesExplicitly(t *testing.T) {
	declarations, err := providers.Factory().Declarations()
	if err != nil {
		t.Fatalf("declare the production catalog: %v", err)
	}

	for _, declaration := range declarations {
		if declaration.Ephemeral == nil {
			continue
		}
		if declaration.Ephemeral.IdempotentLaunch == "" {
			t.Errorf("%s does not say how it deduplicates a repeated launch", declaration.Type)
		}
	}
}
