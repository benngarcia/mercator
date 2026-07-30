package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/scheduler"
)

const checkpointArtifact = "artifact:checkpoint:v1"

// stubCatalog is an object store that says what it holds, and nothing about
// what any machine holds.
type stubCatalog struct {
	versions map[string]domain.ArtifactVersion
	asked    []string
}

func (catalog *stubCatalog) ArtifactVersion(_ context.Context, workspaceID, artifactID string) (domain.ArtifactVersion, error) {
	catalog.asked = append(catalog.asked, workspaceID+"/"+artifactID)
	return catalog.versions[artifactID], nil
}

// TestARunIsNotPlacedUntilItsInputIsDurable is the admission rule in Mercator.
// The consumer is accepted the moment it is submitted, and stays unplaced while
// the object store does not hold what it reads: durability is a fact about the
// store, so the Run waits for a publication and never for a machine.
func TestARunIsNotPlacedUntilItsInputIsDurable(t *testing.T) {
	ctx := context.Background()
	catalog := &stubCatalog{versions: map[string]domain.ArtifactVersion{
		checkpointArtifact: {ID: checkpointArtifact, WorkspaceID: "ws_1", Location: "mercator://ws_1/artifacts/checkpoint"},
	}}
	orch := New(
		openOrchestratorLog(t),
		scheduler.New(),
		fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})),
		WithArtifactCatalog(catalog),

		withTestCapacity(),
	)
	createConsumingRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance the consumer: %v", err)
	}

	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("read the consumer: %v", err)
	}
	if record.Phase != "requested" {
		t.Fatalf("the consumer is %q, and the version it reads has never been published", record.Phase)
	}
	if _, err := orch.GetBookingDecisions(ctx, "ws_1", "run_1"); err == nil {
		t.Fatal("Mercator placed a Run that reads content the object store does not hold")
	}
	if len(catalog.asked) == 0 || catalog.asked[0] != "ws_1/"+checkpointArtifact {
		t.Fatalf("the object store was asked %v, and admission is a question about this workspace's version", catalog.asked)
	}

	// The publication lands, and nothing else about the world changes.
	catalog.versions[checkpointArtifact] = domain.ArtifactVersion{
		ID:          checkpointArtifact,
		WorkspaceID: "ws_1",
		Location:    "mercator://ws_1/artifacts/checkpoint",
		PublishedAt: time.Now().UTC(),
	}

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance the consumer after publication: %v", err)
	}

	decision, err := standingDecision(t, orch, ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("the consumer was still not placed once its input was durable: %v", err)
	}
	if decision.SelectedOfferSnapshotID == "" {
		t.Fatalf("the consumer's decision selected nothing: %+v", decision)
	}
}

// TestARunReadingAnArtifactNeedsAnObjectStoreToAskAbout is the loud half, and
// it is loud at the door. A Mercator with no artifact catalog cannot establish
// that an Artifact exists, and there is no later moment at which it could, so
// the Run is refused where it is submitted. Recording it instead would leave an
// arrival nothing in the system can ever move, with the caller told Mercator
// had taken the work.
func TestARunReadingAnArtifactNeedsAnObjectStoreToAskAbout(t *testing.T) {
	ctx := context.Background()
	orch := New(
		openOrchestratorLog(t),
		scheduler.New(),
		fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})),

		withTestCapacity(),
	)
	revision := orchRevision()
	revision.Spec.Artifacts = domain.ArtifactRequirements{Consumes: []string{checkpointArtifact}}

	_, err := orch.Intake(ctx, IntakeRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_1",
		IdempotencyKey: "idem_create",
		Workload:       revision,
	})

	if err == nil || !strings.Contains(err.Error(), "no artifact catalog") {
		t.Fatalf("submitting a Run that reads an Artifact with no object store configured returned %v", err)
	}
	if _, err := orch.GetRun(ctx, "ws_1", "run_1"); err == nil {
		t.Fatal("the refused Run is in Mercator's records, and a Run nothing can ever place must not be one Mercator holds")
	}
}

func createConsumingRun(t *testing.T, ctx context.Context, orch *Orchestrator) {
	t.Helper()
	revision := orchRevision()
	revision.Spec.Artifacts = domain.ArtifactRequirements{Consumes: []string{checkpointArtifact}}
	if _, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_1",
		CommandKey:     "cmd_create",
		IdempotencyKey: "idem_create",
		Workload:       revision,
	}); err != nil {
		t.Fatalf("create the consumer: %v", err)
	}
}
