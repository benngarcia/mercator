package scenario

import (
	"context"
	"fmt"
	"testing"
)

func TestBuildDashboardTranscriptExercisesProviderReplacementAndQueueDrain(t *testing.T) {
	transcript, err := BuildDashboardTranscript(context.Background(), "ws_scenario")
	if err != nil {
		t.Fatalf("build dashboard transcript: %v", err)
	}

	if len(transcript.Steps) < 30 {
		t.Fatalf("scenario steps = %d, want at least 30", len(transcript.Steps))
	}
	catalog := transcript.OfferCatalog()
	if catalog == nil {
		t.Fatal("baseline has no Offer catalog")
	}
	if len(catalog.Offers) < 8 {
		t.Fatalf("Offer count = %d, want at least 8", len(catalog.Offers))
	}
	adapterTypes := map[string]bool{}
	for _, offer := range catalog.Offers {
		adapterTypes[offer.AdapterType] = true
	}
	for _, adapterType := range []string{"docker", "runpod", "shadeform", "vast"} {
		if !adapterTypes[adapterType] {
			t.Errorf("Offer catalog has no %s Offer", adapterType)
		}
	}

	realEventTypes := map[string]int{}
	targetEventTypes := map[string]int{}
	for _, step := range transcript.Steps {
		if step.Message.Event == nil {
			continue
		}
		switch step.Provenance {
		case ProvenanceOrchestrator:
			realEventTypes[step.Message.Event.Type]++
		case ProvenanceTargetContract:
			targetEventTypes[step.Message.Event.Type]++
		default:
			t.Errorf("step %q has unknown provenance %q", step.ID, step.Provenance)
		}
	}
	for _, eventType := range []string{
		"compute.run.requested.v1",
		"compute.run.booking_decided.v1",
		"compute.run.launch_failed.v1",
		"compute.run.launch_accepted.v1",
		"compute.run.external_state_observed.v1",
		"compute.run.cleanup_confirmed.v1",
		"compute.run.closed.v1",
	} {
		if realEventTypes[eventType] == 0 {
			t.Errorf("real orchestrator transcript has no %s", eventType)
		}
	}
	if realEventTypes["compute.run.booking_decided.v1"] < 2 {
		t.Errorf("booking decisions = %d, want initial plus replacement", realEventTypes["compute.run.booking_decided.v1"])
	}
	for _, eventType := range []string{
		"compute.rental.booking_queued.v1",
		"compute.rental.booking_dispatched.v1",
	} {
		if targetEventTypes[eventType] == 0 {
			t.Errorf("target Rental Schedule transcript has no %s", eventType)
		}
	}

	if transcript.Fidelity.OfferSource != "sanitized_recordings" {
		t.Errorf("Offer source = %q", transcript.Fidelity.OfferSource)
	}
	if !contains(transcript.Fidelity.TargetCapabilities, "rental_schedule") {
		t.Errorf("target capabilities = %v, want rental_schedule", transcript.Fidelity.TargetCapabilities)
	}
}

func TestBuildDashboardTranscriptIsolatesConcurrentWorkspaces(t *testing.T) {
	const workspaceCount = 8
	start := make(chan struct{})
	results := make(chan error, workspaceCount)
	for index := range workspaceCount {
		go func() {
			<-start
			_, err := BuildDashboardTranscript(t.Context(), fmt.Sprintf("ws_concurrent_%d", index))
			results <- err
		}()
	}
	close(start)
	for range workspaceCount {
		if err := <-results; err != nil {
			t.Errorf("build concurrent dashboard transcript: %v", err)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
