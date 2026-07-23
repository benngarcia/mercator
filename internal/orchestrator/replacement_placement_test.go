package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/rentalschedule"
	"github.com/benngarcia/mercator/internal/scheduler"
)

func TestAdvanceRunReplacesOnlyTheRejectedOffer(t *testing.T) {
	ctx := t.Context()
	offers := replacementOffers(t, "same_native_ref")
	stale, alternate := offers[0], offers[1]
	provider := newReplacementProvider([]domain.OfferSnapshot{stale, alternate}, map[string]error{
		stale.ID: capacityUnavailable(),
	})
	var orch *Orchestrator
	provider.beforeLaunch = func(req adapter.LaunchRequest) {
		events, err := orch.GetRunEvents(ctx, req.WorkspaceID, req.RunID)
		if err != nil {
			t.Fatalf("read launch intent: %v", err)
		}
		for _, event := range events {
			if event.Type != EventLaunchIntentRecorded {
				continue
			}
			var intent adapter.LaunchRequest
			if err := json.Unmarshal(event.Data, &intent); err != nil {
				t.Fatalf("decode launch intent: %v", err)
			}
			if intent.LaunchKey == req.LaunchKey {
				return
			}
		}
		t.Fatalf("provider launch %q happened before its durable intent", req.LaunchKey)
	}
	log := openOrchestratorLog(t)
	schedules := rentalschedule.NewMemory(log)
	orch = New(log, scheduler.New(), provider, WithRentalSchedules(schedules))
	createReplacementRun(t, orch, 2)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_replacement"); err != nil {
		t.Fatalf("advance replacement: %v", err)
	}

	record, err := orch.GetRun(ctx, "ws_1", "run_replacement")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Closed || record.Phase != "running" {
		t.Fatalf("replacement should keep the run alive on alternate capacity: %+v", record)
	}
	decision, err := orch.GetBookingDecision(ctx, "ws_1", "run_replacement")
	if err != nil {
		t.Fatalf("get latest placement: %v", err)
	}
	if decision.SelectedOfferSnapshotID != alternate.ID {
		t.Fatalf("latest placement selected %q, want exact alternate %q", decision.SelectedOfferSnapshotID, alternate.ID)
	}
	assertOfferRejected(t, decision, stale.ID, "PREVIOUS_ATTEMPT_CAPACITY_UNAVAILABLE")
	if len(provider.launches) != 2 {
		t.Fatalf("launches = %d, want stale attempt plus alternate", len(provider.launches))
	}
	if provider.launches[0].SelectedOfferSnapshotID != stale.ID || provider.launches[1].SelectedOfferSnapshotID != alternate.ID {
		t.Fatalf("offer sequence = %q then %q", provider.launches[0].SelectedOfferSnapshotID, provider.launches[1].SelectedOfferSnapshotID)
	}
	if provider.launches[0].SelectedOfferNativeRef != provider.launches[1].SelectedOfferNativeRef {
		t.Fatalf("fixture must prove identity exclusion rather than native-ref exclusion: %+v", provider.launches)
	}
	assertCompleteAttemptHistory(t, orch, "run_replacement", 2, 1, 1)
	stored, err := schedules.List(ctx, "ws_1")
	if err != nil {
		t.Fatalf("list Rental Schedules: %v", err)
	}
	decisions := bookingDecisionsFromRun(t, orch, "run_replacement")
	if len(stored[decisions[0].Booking.RentalID].Bookings) != 0 {
		t.Fatalf("rejected Booking still occupies its Rental Schedule: %+v", stored[decisions[0].Booking.RentalID])
	}
	if len(stored[decisions[1].Booking.RentalID].Bookings) != 1 {
		t.Fatalf("replacement Booking missing from its Rental Schedule: %+v", stored[decisions[1].Booking.RentalID])
	}
}

func TestAdvanceRunClosesWithRetryExhaustedAfterBoundedAttempts(t *testing.T) {
	offers := replacementOffers(t, "bounded_exhaustion")
	first, second := offers[0], offers[1]
	provider := newReplacementProvider([]domain.OfferSnapshot{first, second}, map[string]error{
		first.ID:  capacityUnavailable(),
		second.ID: capacityUnavailable(),
	})
	orch := newReplacementOrchestrator(t, provider)
	createReplacementRun(t, orch, 2)

	if err := orch.AdvanceRun(t.Context(), "ws_1", "run_replacement"); err != nil {
		t.Fatalf("bounded capacity rejection is a managed terminal outcome: %v", err)
	}

	record, err := orch.GetRun(t.Context(), "ws_1", "run_replacement")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if !record.Closed || record.Outcome != domain.RunOutcomeFailed || record.Cleanup != domain.CleanupNotRequired {
		t.Fatalf("exhausted run = %+v", record)
	}
	if len(provider.launches) != 2 {
		t.Fatalf("launches = %d, want max_pre_start_attempts=2", len(provider.launches))
	}
	assertCompleteAttemptHistory(t, orch, "run_replacement", 2, 2, 0)
	assertClosedReason(t, orch, "run_replacement", "RETRY_EXHAUSTED")
}

func TestAdvanceRunRecordsTheDecisionThatExhaustsEligibleOffers(t *testing.T) {
	stale := replacementOffers(t, "single_stale")[0]
	provider := newReplacementProvider([]domain.OfferSnapshot{stale}, map[string]error{
		stale.ID: capacityUnavailable(),
	})
	orch := newReplacementOrchestrator(t, provider)
	createReplacementRun(t, orch, 3)

	if err := orch.AdvanceRun(t.Context(), "ws_1", "run_replacement"); err != nil {
		t.Fatalf("exhaust eligible offers: %v", err)
	}

	decision, err := orch.GetBookingDecision(t.Context(), "ws_1", "run_replacement")
	if err != nil {
		t.Fatalf("get exhausted placement: %v", err)
	}
	if decision.SelectedOfferSnapshotID != "" {
		t.Fatalf("exhausted placement selected %q", decision.SelectedOfferSnapshotID)
	}
	assertOfferRejected(t, decision, stale.ID, "PREVIOUS_ATTEMPT_CAPACITY_UNAVAILABLE")
	assertClosedReason(t, orch, "run_replacement", "RETRY_EXHAUSTED")
}

func TestAdvanceRunResumesReplacementFromDurableAttemptHistory(t *testing.T) {
	offers := replacementOffers(t, "alternate_capacity")
	stale, alternate := offers[0], offers[1]
	log := openOrchestratorLog(t)
	beforeRestart := newReplacementProvider([]domain.OfferSnapshot{stale, alternate}, map[string]error{stale.ID: capacityUnavailable()})
	beforeRestart.listOffersErrAfter = 1
	beforeRestart.listOffersErr = errors.New("catalog temporarily unavailable")
	orch := New(log, scheduler.New(), beforeRestart)
	createReplacementRun(t, orch, 2)

	if err := orch.AdvanceRun(t.Context(), "ws_1", "run_replacement"); !errors.Is(err, ErrOfferQuery) {
		t.Fatalf("first process = %v, want replacement offer query interruption", err)
	}
	if len(beforeRestart.launches) != 1 || beforeRestart.launches[0].SelectedOfferSnapshotID != stale.ID {
		t.Fatalf("pre-restart launch history = %+v", beforeRestart.launches)
	}

	afterRestart := newReplacementProvider([]domain.OfferSnapshot{stale, alternate}, nil)
	orch = New(log, scheduler.New(), afterRestart)
	if err := orch.AdvanceRun(t.Context(), "ws_1", "run_replacement"); err != nil {
		t.Fatalf("resume replacement: %v", err)
	}
	if len(afterRestart.launches) != 1 || afterRestart.launches[0].SelectedOfferSnapshotID != alternate.ID {
		t.Fatalf("restart did not preserve exact Offer exclusion: %+v", afterRestart.launches)
	}
	assertCompleteAttemptHistory(t, orch, "run_replacement", 2, 1, 1)
}

func TestCancelRunClosesLocallyAfterSideEffectFreeLaunchFailure(t *testing.T) {
	offers := replacementOffers(t, "alternate_capacity")
	stale, alternate := offers[0], offers[1]
	provider := newReplacementProvider([]domain.OfferSnapshot{stale, alternate}, map[string]error{
		stale.ID: capacityUnavailable(),
	})
	provider.listOffersErrAfter = 1
	provider.listOffersErr = errors.New("catalog unavailable")
	orch := newReplacementOrchestrator(t, provider)
	createReplacementRun(t, orch, 2)

	if err := orch.AdvanceRun(t.Context(), "ws_1", "run_replacement"); !errors.Is(err, ErrOfferQuery) {
		t.Fatalf("interrupted replacement = %v, want offer query error", err)
	}
	record, err := orch.CancelRun(t.Context(), "ws_1", "run_replacement", nil)
	if err != nil {
		t.Fatalf("cancel side-effect-free attempt: %v", err)
	}

	if !record.Closed || record.Outcome != domain.RunOutcomeCancelled || record.Cleanup != domain.CleanupNotRequired {
		t.Fatalf("cancelled replacement = %+v", record)
	}
	if provider.TerminateCount() != 0 {
		t.Fatalf("provider terminate calls = %d, want 0 for known-absent object", provider.TerminateCount())
	}
}

func TestAdvanceRunLeavesNonCapacityFailuresTerminal(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "invalid request", err: &adapter.ProviderFailure{Kind: adapter.ProviderFailureInvalidRequest, SideEffect: adapter.SideEffectNone}},
		{name: "authentication", err: &adapter.ProviderFailure{Kind: adapter.ProviderFailureAuthentication, SideEffect: adapter.SideEffectNone}},
		{name: "rate limited", err: &adapter.ProviderFailure{Kind: adapter.ProviderFailureRateLimited, Retryable: true, SideEffect: adapter.SideEffectNone}},
		{name: "ownership conflict", err: adapter.ErrIdempotencyConflict},
		{name: "generic retryable", err: adapter.ErrRetryableFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			offer := replacementOffers(t, "single_terminal")[0]
			provider := newReplacementProvider([]domain.OfferSnapshot{offer}, map[string]error{offer.ID: test.err})
			orch := newReplacementOrchestrator(t, provider)
			createReplacementRun(t, orch, 3)

			if err := orch.AdvanceRun(t.Context(), "ws_1", "run_replacement"); err == nil {
				t.Fatal("terminal provider failure should be returned")
			}

			record, err := orch.GetRun(t.Context(), "ws_1", "run_replacement")
			if err != nil {
				t.Fatalf("get run: %v", err)
			}
			if !record.Closed || record.Outcome != domain.RunOutcomeFailed {
				t.Fatalf("non-capacity failure must close terminally: %+v", record)
			}
			if len(provider.launches) != 1 {
				t.Fatalf("non-capacity failure launched %d attempts, want 1", len(provider.launches))
			}
			assertCompleteAttemptHistory(t, orch, "run_replacement", 1, 1, 0)
		})
	}
}

func TestAdvanceRunReconcilesIndeterminateCreateWithoutReplacement(t *testing.T) {
	offer := replacementOffers(t, "single_indeterminate")[0]
	provider := &indeterminateCreateProvider{
		Adapter: fake.New(
			fake.WithOffers([]domain.OfferSnapshot{offer}),
			fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
		),
	}
	orch := newReplacementOrchestrator(t, provider)
	createReplacementRun(t, orch, 3)

	if err := orch.AdvanceRun(t.Context(), "ws_1", "run_replacement"); !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("first advance = %v, want indeterminate Create", err)
	}
	if err := orch.AdvanceRun(t.Context(), "ws_1", "run_replacement"); err != nil {
		t.Fatalf("reconcile original launch: %v", err)
	}
	if err := orch.AdvanceRun(t.Context(), "ws_1", "run_replacement"); err != nil {
		t.Fatalf("repeat reconciliation: %v", err)
	}

	owned, err := provider.ListOwned(t.Context(), adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if provider.launchCalls != 1 || len(owned) != 1 {
		t.Fatalf("indeterminate reconciliation created duplicates: launches=%d owned=%d", provider.launchCalls, len(owned))
	}
	if owned[0].LaunchKey != provider.launchRequest.LaunchKey {
		t.Fatalf("reconciliation changed launch identity: object=%q intent=%q", owned[0].LaunchKey, provider.launchRequest.LaunchKey)
	}
	assertCompleteAttemptHistory(t, orch, "run_replacement", 1, 0, 0)
	events := replacementEvents(t, orch, "run_replacement")
	if countEvents(events, EventLaunchIndeterminate) != 1 {
		t.Fatalf("indeterminate history = %v", eventTypes(events))
	}
	var outcome map[string]any
	for _, event := range events {
		if event.Type == EventLaunchIndeterminate {
			if err := json.Unmarshal(event.Data, &outcome); err != nil {
				t.Fatalf("decode indeterminate event: %v", err)
			}
		}
	}
	if outcome["side_effect"] != string(adapter.SideEffectIndeterminate) {
		t.Fatalf("durable indeterminate outcome = %#v", outcome)
	}
}

type replacementProvider struct {
	*fake.Adapter
	offers             []domain.OfferSnapshot
	failures           map[string]error
	launches           []adapter.LaunchRequest
	beforeLaunch       func(adapter.LaunchRequest)
	listOffersCalls    int
	listOffersErrAfter int
	listOffersErr      error
}

func newReplacementProvider(offers []domain.OfferSnapshot, failures map[string]error) *replacementProvider {
	return &replacementProvider{
		Adapter:  fake.New(fake.WithLaunchOutcome(adapter.ExternalPhaseRunning)),
		offers:   offers,
		failures: failures,
	}
}

func (p *replacementProvider) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	p.listOffersCalls++
	if p.listOffersErr != nil && p.listOffersCalls > p.listOffersErrAfter {
		return nil, p.listOffersErr
	}
	return append([]domain.OfferSnapshot(nil), p.offers...), nil
}

func (p *replacementProvider) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	if p.beforeLaunch != nil {
		p.beforeLaunch(req)
	}
	p.launches = append(p.launches, req)
	if err := p.failures[req.SelectedOfferSnapshotID]; err != nil {
		return adapter.LaunchReceipt{}, err
	}
	return p.Adapter.Launch(ctx, req)
}

type indeterminateCreateProvider struct {
	*fake.Adapter
	launchCalls   int
	launchRequest adapter.LaunchRequest
}

func (p *indeterminateCreateProvider) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	p.launchCalls++
	p.launchRequest = req
	if p.launchCalls > 1 {
		type unexpectedDuplicate struct{ error }
		return adapter.LaunchReceipt{}, unexpectedDuplicate{errors.New("duplicate Create")}
	}
	if _, err := p.Adapter.Launch(ctx, req); err != nil {
		return adapter.LaunchReceipt{}, err
	}
	return adapter.LaunchReceipt{}, &adapter.ProviderFailure{
		Kind:       adapter.ProviderFailureInternal,
		Retryable:  true,
		SideEffect: adapter.SideEffectIndeterminate,
	}
}

func replacementOffer(id, connectionID, nativeRef string, rate float64, now time.Time) domain.OfferSnapshot {
	offer := orchProvisionableOffer(id, now)
	offer.ConnectionID = connectionID
	offer.AdapterType = "shadeform"
	offer.NativeRef = nativeRef
	offer.Pricing.RatePerSecondUSD = rate
	return offer
}

type replacementOfferSpec struct {
	ID           string  `json:"id"`
	ConnectionID string  `json:"connection_id"`
	NativeRef    string  `json:"native_ref"`
	Rate         float64 `json:"rate"`
}

func replacementOffers(t *testing.T, scenario string) []domain.OfferSnapshot {
	t.Helper()
	data, err := os.ReadFile("testdata/replacement_offers.json")
	if err != nil {
		t.Fatalf("read replacement Offer fixtures: %v", err)
	}
	var fixtures map[string][]replacementOfferSpec
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode replacement Offer fixtures: %v", err)
	}
	specs, ok := fixtures[scenario]
	if !ok {
		t.Fatalf("replacement Offer fixture %q not found", scenario)
	}
	now := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	offers := make([]domain.OfferSnapshot, 0, len(specs))
	for _, spec := range specs {
		offers = append(offers, replacementOffer(spec.ID, spec.ConnectionID, spec.NativeRef, spec.Rate, now))
	}
	return offers
}

func capacityUnavailable() error {
	return &adapter.ProviderFailure{
		Kind:       adapter.ProviderFailureCapacityUnavailable,
		Retryable:  true,
		SideEffect: adapter.SideEffectNone,
	}
}

func newReplacementOrchestrator(t *testing.T, provider Adapter) *Orchestrator {
	t.Helper()
	return New(openOrchestratorLog(t), scheduler.New(), provider)
}

func createReplacementRun(t *testing.T, orch *Orchestrator, maxAttempts int) {
	t.Helper()
	workload := orchRevision()
	workload.Spec.Execution.MaxPreStartAttempts = maxAttempts
	if _, err := orch.CreateRun(t.Context(), CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_replacement",
		IdempotencyKey: "idem_replacement",
		Workload:       workload,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
}

func assertCompleteAttemptHistory(t *testing.T, orch *Orchestrator, runID string, attempts, failures, accepted int) {
	t.Helper()
	events := replacementEvents(t, orch, runID)
	for eventType, want := range map[string]int{
		EventBookingDecided:       attempts,
		EventAttemptCreated:       attempts,
		EventLaunchIntentRecorded: attempts,
		EventLaunchFailed:         failures,
		EventLaunchAccepted:       accepted,
	} {
		if got := countEvents(events, eventType); got != want {
			t.Errorf("%s count = %d, want %d; history=%v", eventType, got, want, eventTypes(events))
		}
	}
	seenAttempts := map[string]bool{}
	seenLaunches := map[string]bool{}
	for _, event := range events {
		if event.Type != EventLaunchIntentRecorded {
			continue
		}
		var intent adapter.LaunchRequest
		if err := json.Unmarshal(event.Data, &intent); err != nil {
			t.Fatalf("decode intent: %v", err)
		}
		if seenAttempts[intent.AttemptID] || seenLaunches[intent.LaunchKey] {
			t.Errorf("attempt history reused ownership identity: %+v", intent)
		}
		seenAttempts[intent.AttemptID] = true
		seenLaunches[intent.LaunchKey] = true
	}
}

func bookingDecisionsFromRun(t *testing.T, orch *Orchestrator, runID string) []domain.BookingDecision {
	t.Helper()
	events, err := orch.GetRunEvents(t.Context(), "ws_1", runID)
	if err != nil {
		t.Fatalf("get Booking Decisions: %v", err)
	}
	decisions := []domain.BookingDecision{}
	for _, event := range events {
		if event.Type != EventBookingDecided {
			continue
		}
		var data bookingDecisionData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode Booking Decision: %v", err)
		}
		decisions = append(decisions, data.Decision)
	}
	return decisions
}

func assertClosedReason(t *testing.T, orch *Orchestrator, runID, want string) {
	t.Helper()
	for _, event := range replacementEvents(t, orch, runID) {
		if event.Type != EventRunClosed {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("decode run closed: %v", err)
		}
		if data["reason"] != want {
			t.Fatalf("closed reason = %#v, want %q", data["reason"], want)
		}
		return
	}
	t.Fatal("run closed event not found")
}

func assertOfferRejected(t *testing.T, decision domain.BookingDecision, offerID, reason string) {
	t.Helper()
	for _, candidate := range decision.Candidates {
		if candidate.OfferSnapshotID != offerID {
			continue
		}
		for _, rejection := range candidate.Rejections {
			if rejection.Code == reason {
				return
			}
		}
		t.Fatalf("offer %q rejections = %+v, want %q", offerID, candidate.Rejections, reason)
	}
	t.Fatalf("offer %q absent from replacement decision: %+v", offerID, decision.Candidates)
}

func replacementEvents(t *testing.T, orch *Orchestrator, runID string) []eventlog.StoredEvent {
	t.Helper()
	events, err := orch.GetRunEvents(t.Context(), "ws_1", runID)
	if err != nil {
		t.Fatalf("get run events: %v", err)
	}
	return events
}
