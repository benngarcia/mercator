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

// The acting principal recorded on the create and cancel command facts must
// surface on the run record as created_by / cancelled_by.
func TestRunRecordSurfacesCreateAndCancelPrincipals(t *testing.T) {
	ctx := context.Background()
	offer := orchOffer("offer_actor", time.Now().UTC())
	ad := fake.New(fake.WithOffers([]domain.OfferSnapshot{offer}), fake.WithLaunchOutcome(adapter.ExternalPhaseRunning))
	orch := newTestOrchestrator(t, ad)

	creator := json.RawMessage(`{"subject":"bearer"}`)
	canceller := json.RawMessage(`{"subject":"operator@example.com"}`)

	if _, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_actor",
		IdempotencyKey: "idem_actor",
		Actor:          creator,
		Workload:       orchRevision(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_actor"); err != nil {
		t.Fatalf("advance run: %v", err)
	}

	record, err := orch.GetRun(ctx, "ws_1", "run_actor")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.CreatedBy != "bearer" {
		t.Fatalf("expected created_by=bearer, got %q", record.CreatedBy)
	}
	if record.CancelledBy != "" {
		t.Fatalf("cancelled_by must be empty before a cancel, got %q", record.CancelledBy)
	}

	cancelled, err := orch.CancelRun(ctx, "ws_1", "run_actor", canceller)
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if cancelled.CreatedBy != "bearer" {
		t.Fatalf("created_by should survive cancel, got %q", cancelled.CreatedBy)
	}
	if cancelled.CancelledBy != "operator@example.com" {
		t.Fatalf("expected cancelled_by=operator@example.com, got %q", cancelled.CancelledBy)
	}
}

// Runs recorded without an actor (auth disabled, or logs from before auditing
// existed) must reduce cleanly with empty audit fields.
func TestRunRecordToleratesActorlessEvents(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_pre", time.Now().UTC())})))
	if _, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_pre_audit",
		IdempotencyKey: "idem_pre_audit",
		Workload:       orchRevision(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_pre_audit")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.CreatedBy != "" || record.CancelledBy != "" {
		t.Fatalf("actorless events should leave audit fields empty, got %+v", record)
	}
}
