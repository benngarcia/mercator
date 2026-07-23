package scenario

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

func TestDashboardScenariosExerciseRealPlacementPaths(t *testing.T) {
	tests := []struct {
		name             string
		queued           int
		dispatched       int
		launchFailures   int
		requiredReject   string
		requiredSelected domain.CandidateDisposition
	}{
		{name: DashboardScenarioWarmPoolBurst, queued: 3, dispatched: 3, requiredSelected: domain.CandidateDispositionQueue},
		{name: DashboardScenarioDeadlineCost, queued: 1, dispatched: 1, requiredReject: "LATENCY_SLO_EXCEEDED", requiredSelected: domain.CandidateDispositionProvision},
		{name: DashboardScenarioFailureRebalance, queued: 1, dispatched: 1, launchFailures: 1, requiredReject: "PREVIOUS_ATTEMPT_CAPACITY_UNAVAILABLE", requiredSelected: domain.CandidateDispositionProvision},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transcript, err := BuildDashboardScenarioTranscript(t.Context(), "ws_scenario", test.name)
			if err != nil {
				t.Fatalf("build dashboard transcript: %v", err)
			}
			catalog := transcript.OfferCatalog()
			if catalog == nil || len(catalog.Offers) < 8 {
				t.Fatalf("Offer catalog = %+v", catalog)
			}
			for _, adapterType := range []string{"docker", "runpod", "shadeform", "vast"} {
				if !catalogHasAdapter(catalog, adapterType) {
					t.Errorf("Offer catalog has no %s Offer", adapterType)
				}
			}
			decisions := dashboardDecisions(t, transcript)
			if len(decisions) < 3 {
				t.Fatalf("Booking Decisions = %d, want at least 3", len(decisions))
			}
			if countQueued(decisions) != test.queued {
				t.Errorf("queued decisions = %d, want %d", countQueued(decisions), test.queued)
			}
			if countEventType(transcript, orchestrator.EventBookingDispatched) != test.dispatched {
				t.Errorf("dispatch events = %d, want %d", countEventType(transcript, orchestrator.EventBookingDispatched), test.dispatched)
			}
			if countEventType(transcript, orchestrator.EventLaunchFailed) < test.launchFailures {
				t.Errorf("launch failures = %d, want at least %d", countEventType(transcript, orchestrator.EventLaunchFailed), test.launchFailures)
			}
			if !decisionsContainDisposition(decisions, test.requiredSelected) {
				t.Errorf("no selected candidate disposition %q", test.requiredSelected)
			}
			if test.requiredReject != "" && !decisionsContainRejection(decisions, test.requiredReject) {
				t.Errorf("no candidate rejection %q", test.requiredReject)
			}
			for _, step := range transcript.Steps {
				if step.Provenance != ProvenanceOrchestrator {
					t.Errorf("step %q provenance = %q", step.ID, step.Provenance)
				}
			}
			if !contains(transcript.Fidelity.ProvenCapabilities, "rental_schedule") || len(transcript.Fidelity.TargetCapabilities) != 0 {
				t.Errorf("Fidelity = %+v", transcript.Fidelity)
			}
		})
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

func dashboardDecisions(t *testing.T, transcript DashboardTranscript) []domain.BookingDecision {
	t.Helper()
	decisions := []domain.BookingDecision{}
	for _, step := range transcript.Steps {
		if step.Message.Event == nil || step.Message.Event.Type != orchestrator.EventBookingDecided {
			continue
		}
		var data struct {
			Decision domain.BookingDecision `json:"decision"`
		}
		if err := json.Unmarshal(step.Message.Event.Data, &data); err != nil {
			t.Fatalf("decode Booking Decision: %v", err)
		}
		decisions = append(decisions, data.Decision)
	}
	return decisions
}

func countQueued(decisions []domain.BookingDecision) int {
	count := 0
	for _, decision := range decisions {
		if decision.Booking != nil && decision.Booking.State == domain.BookingStateQueued {
			count++
		}
	}
	return count
}

func decisionsContainDisposition(decisions []domain.BookingDecision, disposition domain.CandidateDisposition) bool {
	for _, decision := range decisions {
		for _, candidate := range decision.Candidates {
			if candidate.OfferSnapshotID == decision.SelectedOfferSnapshotID && candidate.Disposition == disposition {
				return true
			}
		}
	}
	return false
}

func decisionsContainRejection(decisions []domain.BookingDecision, code string) bool {
	for _, decision := range decisions {
		for _, candidate := range decision.Candidates {
			for _, rejection := range candidate.Rejections {
				if rejection.Code == code {
					return true
				}
			}
		}
	}
	return false
}

func countEventType(transcript DashboardTranscript, eventType string) int {
	count := 0
	for _, step := range transcript.Steps {
		if step.Message.Event != nil && step.Message.Event.Type == eventType {
			count++
		}
	}
	return count
}

func catalogHasAdapter(catalog *DashboardOfferCatalog, adapterType string) bool {
	for _, offer := range catalog.Offers {
		if offer.AdapterType == adapterType {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
