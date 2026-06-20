package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/adapter/fake"
	"github.com/bengarcia/mercator/internal/domain"
	"github.com/bengarcia/mercator/internal/eventlog"
	"github.com/bengarcia/mercator/internal/scheduler"
)

func TestCreateRunIsIdempotent(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})))
	req := CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_1",
		CommandKey:     "cmd_create",
		IdempotencyKey: "idem_create",
		Workload:       orchRevision(),
	}

	first, err := orch.CreateRun(ctx, req)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := orch.CreateRun(ctx, req)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first.RunID != second.RunID || !second.Duplicate {
		t.Fatalf("expected duplicate create result, first=%+v second=%+v", first, second)
	}
	events, err := orch.GetRunEvents(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if countEvents(events, EventRunRequested) != 1 {
		t.Fatalf("expected one RunRequested event, got %+v", events)
	}
}

func TestCreateRunPublicEventRedactsEnvironmentBindings(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})))
	literal := "literal-token-that-must-not-be-public"
	rev := orchRevision()
	rev.Spec.Containers[0].Env = map[string]domain.EnvBinding{
		"LOG_LEVEL": {Value: ptr("info")},
		"API_TOKEN": {SecretRef: &domain.SecretReference{
			Name:    "provider-secret-name-that-must-not-be-public",
			Version: 7,
		}},
		"SERVICE_PASSWORD": {Value: &literal},
	}

	if _, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_redaction",
		CommandKey:     "cmd_create_redaction",
		IdempotencyKey: "idem_create_redaction",
		Workload:       rev,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	events, err := orch.GetRunEvents(ctx, "ws_1", "run_redaction")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	publicData := string(events[0].CloudEvent().Data)
	for _, forbidden := range []string{
		"literal-token-that-must-not-be-public",
		"provider-secret-name-that-must-not-be-public",
		`"value":"info"`,
	} {
		if strings.Contains(publicData, forbidden) {
			t.Fatalf("public RunRequested event exposed %q in %s", forbidden, publicData)
		}
	}
}

func TestCreateRunRejectsWorkspaceMismatch(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})))
	_, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_other",
		RunID:          "run_workspace_mismatch",
		CommandKey:     "cmd_create_workspace_mismatch",
		IdempotencyKey: "idem_create_workspace_mismatch",
		Workload:       orchRevision(),
	})
	if err == nil || !strings.Contains(err.Error(), "WORKSPACE_MISMATCH") {
		t.Fatalf("expected WORKSPACE_MISMATCH, got %v", err)
	}
}

func TestAdvanceRunPersistsLaunchIntentBeforeCallingAdapter(t *testing.T) {
	ctx := context.Background()
	log := openOrchestratorLog(t)
	spy := &spyAdapter{Adapter: fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})), log: log}
	orch := New(log, scheduler.New(), spy)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !spy.sawLaunchIntentBeforeLaunch {
		t.Fatal("adapter launch happened before LaunchIntentRecorded was visible in the event log")
	}
}

func TestAdvanceRunDoesNotRelaunchAfterNonterminalObservation(t *testing.T) {
	ctx := context.Background()
	ad := &countingAdapter{
		Adapter: fake.New(
			fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
			fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
		),
	}
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("first advance: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("second advance should observe recorded launch intent, not relaunch: %v", err)
	}
	if ad.launchCalls != 1 {
		t.Fatalf("expected one adapter launch call across replay, got %d", ad.launchCalls)
	}
}

func TestAdvanceRunRecoversRecordedLaunchIntentWhenOffersChange(t *testing.T) {
	ctx := context.Background()
	ad := &mutableOfferAdapter{
		Adapter: fake.New(fake.WithLaunchOutcome(adapter.ExternalPhaseRunning)),
		offers:  []domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())},
	}
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("first advance: %v", err)
	}
	ad.offers = nil
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance should recover from recorded launch intent after offers disappear: %v", err)
	}
	if ad.launchCalls != 1 {
		t.Fatalf("expected recovery to avoid a second launch, got %d launch calls", ad.launchCalls)
	}
}

func TestAdvanceRunRecordsLaunchConflict(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, conflictAdapter{Adapter: fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}))})
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err == nil {
		t.Fatal("expected advance to report launch conflict")
	}
	events, err := orch.GetRunEvents(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if countEvents(events, EventLaunchFailed) != 1 {
		t.Fatalf("expected LaunchFailed event, got %+v", eventTypes(events))
	}
}

func TestAdvanceRunClosesSuccessfulFakeRun(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	))
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance: %v", err)
	}
	events, err := orch.GetRunEvents(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	for _, eventType := range []string{
		EventPlacementDecided,
		EventAttemptCreated,
		EventLaunchIntentRecorded,
		EventLaunchAccepted,
		EventExternalStateObserved,
		EventRunOutcomeRecorded,
		EventCleanupRequested,
		EventCleanupConfirmed,
		EventRunClosed,
	} {
		if countEvents(events, eventType) != 1 {
			t.Fatalf("expected one %s event, got %s", eventType, eventTypes(events))
		}
	}
}

type spyAdapter struct {
	*fake.Adapter
	log                         eventlog.EventLog
	sawLaunchIntentBeforeLaunch bool
}

func (s *spyAdapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	events, err := s.log.ReadStream(ctx, eventlog.StreamKey{WorkspaceID: req.WorkspaceID, Type: "run", ID: req.RunID}, 0, 100)
	if err == nil && countEvents(events, EventLaunchIntentRecorded) == 1 {
		s.sawLaunchIntentBeforeLaunch = true
	}
	return s.Adapter.Launch(ctx, req)
}

type conflictAdapter struct {
	*fake.Adapter
}

func (c conflictAdapter) Launch(context.Context, adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	return adapter.LaunchReceipt{}, adapter.ErrIdempotencyConflict
}

type countingAdapter struct {
	*fake.Adapter
	launchCalls int
}

func (c *countingAdapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	c.launchCalls++
	return c.Adapter.Launch(ctx, req)
}

type mutableOfferAdapter struct {
	*fake.Adapter
	offers      []domain.OfferSnapshot
	launchCalls int
}

func (m *mutableOfferAdapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return append([]domain.OfferSnapshot(nil), m.offers...), nil
}

func (m *mutableOfferAdapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	m.launchCalls++
	return m.Adapter.Launch(ctx, req)
}

func newTestOrchestrator(t *testing.T, ad adapter.Adapter) *Orchestrator {
	t.Helper()
	return New(openOrchestratorLog(t), scheduler.New(), ad)
}

func openOrchestratorLog(t *testing.T) *eventlog.SQLiteEventLog {
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
	return log
}

func createRun(t *testing.T, ctx context.Context, orch *Orchestrator) {
	t.Helper()
	_, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_1",
		CommandKey:     "cmd_create",
		IdempotencyKey: "idem_create",
		Workload:       orchRevision(),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
}

func countEvents(events []eventlog.StoredEvent, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func eventTypes(events []eventlog.StoredEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func orchRevision() domain.WorkloadRevision {
	return domain.WorkloadRevision{
		ID:          "wrev_1",
		WorkspaceID: "ws_1",
		WorkloadID:  "wrk_1",
		Digest:      "sha256:revision",
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "ghcr.io/acme/inference@sha256:0000000000000000000000000000000000000000000000000000000000000000",
				Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
			}},
			Resources: domain.ResourceRequirements{
				CPU:           domain.CPURequirement{MinMillis: 1000},
				Memory:        domain.MemoryRequirement{MinBytes: 1 << 30},
				EphemeralDisk: domain.DiskRequirement{MinBytes: 1 << 30},
			},
			Network:   domain.NetworkRequirements{Inbound: domain.InboundNetworkNone},
			Placement: domain.PlacementPolicy{Objective: domain.ObjectiveBalanced, ExpectedRuntimeSeconds: 60},
			Execution: domain.ExecutionPolicy{MaxRuntimeSeconds: 120, MaxPreStartAttempts: 3},
		},
	}
}

func orchOffer(id string, now time.Time) domain.OfferSnapshot {
	return domain.OfferSnapshot{
		ID:           id,
		ConnectionID: "conn_1",
		AdapterType:  "fake",
		Kind:         domain.OfferKindStanding,
		ObservedAt:   now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Minute),
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          2000,
			MemoryBytes:        2 << 30,
			EphemeralDiskBytes: 2 << 30,
		},
		Capabilities: domain.CapabilityProfile{
			Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, MaxEnvironmentBytes: 32768},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone, Protocols: []string{"tcp"}},
			Pricing:   domain.PricingCapabilities{Known: true},
			Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "deterministic_name", ListOwned: true},
			Secrets:   domain.SecretDeliveryCapabilities{Delivery: "direct_env", CleanupSupported: true},
		},
		Pricing:  domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.0001, Known: true},
		Queue:    &domain.QueueSnapshot{},
		Capacity: domain.CapacityEvidence{Available: true, Confidence: 1},
		ImageCache: domain.ImageCacheEvidence{
			ManifestCached: true,
			MissingBytes:   0,
			Known:          true,
		},
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func expectErrorIs(t *testing.T, err error, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("expected %v, got %v", target, err)
	}
}

func ptr(value string) *string {
	return &value
}
