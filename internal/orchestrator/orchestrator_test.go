package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/reporting"
	"github.com/benngarcia/mercator/internal/scheduler"
)

type workspaceTestLog struct {
	eventlog.EventLog
}

func (l workspaceTestLog) AppendIfWorkspaceActive(ctx context.Context, request eventlog.AppendRequest) (eventlog.AppendResult, error) {
	return l.Append(ctx, request)
}

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

func TestListRunsDoesNotReadEveryStream(t *testing.T) {
	ctx := context.Background()
	log := &streamReadCountingLog{WorkspaceEventLog: openOrchestratorLog(t)}
	orch := New(log, scheduler.New(), fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	))
	for _, runID := range []string{"run_1", "run_2"} {
		if _, err := orch.CreateRun(ctx, CreateRunRequest{WorkspaceID: "ws_1", RunID: runID, IdempotencyKey: "idem_" + runID, Workload: orchRevision()}); err != nil {
			t.Fatalf("create %s: %v", runID, err)
		}
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance run_1: %v", err)
	}
	log.streamReads = 0

	records, err := orch.ListRuns(ctx, "ws_1")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("listed %d runs, want 2", len(records))
	}
	if records[0].ID != "run_1" || !records[0].Closed || records[0].Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("run_1 projection = %+v, want closed successful run", records[0])
	}
	if log.streamReads != 0 {
		t.Fatalf("list read %d individual streams, want 0", log.streamReads)
	}
}

func TestCreateRunPublicEventRedactsEnvironmentBindings(t *testing.T) {
	ctx := context.Background()
	orch := newTestOrchestrator(t, fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})))
	literal := "literal-token-that-must-not-be-public"
	rev := orchRevision()
	rev.Spec.Containers[0].Env = map[string]domain.EnvBinding{
		"LOG_LEVEL":        {Value: ptr("info")},
		"PROVIDER_API_KEY": {Value: ptr("provider-token-that-must-not-be-public")},
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
		"provider-token-that-must-not-be-public",
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

func TestAdvanceRunPassesCompleteWorkloadAndPlacementToAdapter(t *testing.T) {
	ctx := context.Background()
	entrypoint := []string{"/bin/worker"}
	literal := "safe-literal"
	rev := orchRevision()
	rev.Spec.Containers[0].Entrypoint = &entrypoint
	rev.Spec.Containers[0].Args = []string{"--batch", "64"}
	rev.Spec.Containers[0].Env = map[string]domain.EnvBinding{
		"LOG_LEVEL": {Value: &literal},
		"API_TOKEN": {Value: ptr("runtime-managed-token")},
	}
	rev.Spec.Containers[0].Ports = []domain.PortSpec{{Name: "metrics", ContainerPort: 9090, Protocol: "tcp", Exposure: domain.PortExposurePrivate}}
	rev.Spec.Resources.Accelerators = []domain.AcceleratorRequirement{{Vendor: "nvidia", ModelAnyOf: []string{"nvidia-a10"}, Count: 1, MemoryMinBytes: 16 << 30}}
	offer := orchOffer("off_1", time.Now().UTC())
	offer.NativeRef = "native-offer-1"
	offer.Resources.Accelerators = []domain.AcceleratorInventory{{Vendor: "nvidia", Model: "a10", CanonicalModel: "nvidia-a10", Count: 1, MemoryBytes: 24 << 30}}
	offer.Capabilities.Resources.GPUVendors = []string{"nvidia"}
	ad := &captureLaunchAdapter{Adapter: fake.New(fake.WithOffers([]domain.OfferSnapshot{offer}))}
	orch := newTestOrchestrator(t, ad)
	if _, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_contract",
		CommandKey:     "cmd_create_contract",
		IdempotencyKey: "idem_create_contract",
		Workload:       rev,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	if err := orch.AdvanceRun(ctx, "ws_1", "run_contract"); err != nil {
		t.Fatalf("advance: %v", err)
	}
	req := ad.launchRequest
	if req.WorkloadRevisionID != rev.ID || req.WorkloadID != rev.WorkloadID {
		t.Fatalf("launch request missing workload identity: %+v", req)
	}
	if req.Image != rev.Spec.Containers[0].Image || req.Platform != rev.Spec.Containers[0].Platform {
		t.Fatalf("launch request missing OCI image/platform: %+v", req)
	}
	if req.Entrypoint == nil || strings.Join(*req.Entrypoint, " ") != "/bin/worker" || strings.Join(req.Args, " ") != "--batch 64" {
		t.Fatalf("launch request missing command: %+v", req)
	}
	if len(req.Ports) != 1 || req.Ports[0].ContainerPort != 9090 {
		t.Fatalf("launch request missing ports: %+v", req)
	}
	if req.Resources.CPU.MinMillis == 0 || len(req.Resources.Accelerators) != 1 {
		t.Fatalf("launch request missing resources: %+v", req)
	}
	if !reflect.DeepEqual(ad.offerRequest.Resources, rev.Spec.Resources) {
		t.Fatalf("offer request resources = %+v, want %+v", ad.offerRequest.Resources, rev.Spec.Resources)
	}
	if req.SelectedOfferSnapshotID != "off_1" || req.SelectedOfferNativeRef != "native-offer-1" {
		t.Fatalf("launch request missing selected offer context: %+v", req)
	}
	if req.CleanupLocator == "" || req.OwnershipToken == "" || req.LaunchKey == "" || req.RequestHash == "" {
		t.Fatalf("launch request missing side-effect identity fields: %+v", req)
	}
	if binding := findLaunchEnv(t, req.Environment, "API_TOKEN"); binding.Value == nil || *binding.Value != "runtime-managed-token" {
		t.Fatalf("literal env binding missing from launch request: %+v", binding)
	}
	if binding := findLaunchEnv(t, req.Environment, "LOG_LEVEL"); binding.Value == nil || *binding.Value != literal {
		t.Fatalf("literal env binding missing from launch request: %+v", binding)
	}
}

func TestAdvanceRunInjectsReportingEnvWhenConfigured(t *testing.T) {
	ctx := context.Background()
	const publicURL = "https://pub.example"
	signer := reporting.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	offer := orchOffer("off_reporting", time.Now().UTC())
	ad := &captureLaunchAdapter{Adapter: fake.New(fake.WithOffers([]domain.OfferSnapshot{offer}))}
	log := openOrchestratorLog(t)
	orch := New(log, scheduler.New(), ad, WithReporting(publicURL, signer))

	if _, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_reporting",
		CommandKey:     "cmd_reporting",
		IdempotencyKey: "idem_reporting",
		Workload:       orchRevision(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_reporting"); err != nil {
		t.Fatalf("advance run: %v", err)
	}

	req := ad.launchRequest
	// MERCATOR_RUN_ID
	runIDBinding := findLaunchEnv(t, req.Environment, "MERCATOR_RUN_ID")
	if runIDBinding.Value == nil || *runIDBinding.Value != "run_reporting" {
		t.Fatalf("MERCATOR_RUN_ID wrong: %+v", runIDBinding)
	}
	// MERCATOR_REPORT_URL
	reportURLBinding := findLaunchEnv(t, req.Environment, "MERCATOR_REPORT_URL")
	if reportURLBinding.Value == nil || *reportURLBinding.Value != publicURL {
		t.Fatalf("MERCATOR_REPORT_URL wrong: %+v", reportURLBinding)
	}
	// MERCATOR_RUN_TOKEN
	wantToken := signer.Token("ws_1", "run_reporting")
	reportTokenBinding := findLaunchEnv(t, req.Environment, "MERCATOR_RUN_TOKEN")
	if reportTokenBinding.Value == nil || *reportTokenBinding.Value != wantToken {
		t.Fatalf("MERCATOR_RUN_TOKEN wrong: got %+v, want %q", reportTokenBinding, wantToken)
	}
	// MERCATOR_WORKSPACE_ID
	workspaceIDBinding := findLaunchEnv(t, req.Environment, "MERCATOR_WORKSPACE_ID")
	if workspaceIDBinding.Value == nil || *workspaceIDBinding.Value != "ws_1" {
		t.Fatalf("MERCATOR_WORKSPACE_ID wrong: %+v", workspaceIDBinding)
	}
}

func TestAdvanceRunDoesNotInjectReportingEnvWhenNotConfigured(t *testing.T) {
	ctx := context.Background()
	offer := orchOffer("off_no_reporting", time.Now().UTC())
	ad := &captureLaunchAdapter{Adapter: fake.New(fake.WithOffers([]domain.OfferSnapshot{offer}))}
	// newTestOrchestrator does NOT pass WithReporting — baseline orchestrator.
	orch := newTestOrchestrator(t, ad)

	if _, err := orch.CreateRun(ctx, CreateRunRequest{
		WorkspaceID:    "ws_1",
		RunID:          "run_no_reporting",
		CommandKey:     "cmd_no_reporting",
		IdempotencyKey: "idem_no_reporting",
		Workload:       orchRevision(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_no_reporting"); err != nil {
		t.Fatalf("advance run: %v", err)
	}

	req := ad.launchRequest
	for _, name := range []string{"MERCATOR_RUN_ID", "MERCATOR_REPORT_URL", "MERCATOR_RUN_TOKEN", "MERCATOR_WORKSPACE_ID"} {
		for _, binding := range req.Environment {
			if binding.Name == name {
				t.Fatalf("unexpected reporting env var %q in environment when reporting is not configured", name)
			}
		}
	}
}

func TestCancelRunAfterLaunchRecordsCancelledOutcomeAndCleansUp(t *testing.T) {
	ctx := context.Background()
	offer := orchProvisionableOffer("offer_cancel", time.Now().UTC())
	ad := fake.New(fake.WithOffers([]domain.OfferSnapshot{offer}), fake.WithLaunchOutcome(adapter.ExternalPhaseRunning))
	orch := newTestOrchestrator(t, ad)
	rev := orchRevision()

	if _, err := orch.CreateRun(ctx, CreateRunRequest{WorkspaceID: "ws_1", RunID: "run_cancel", IdempotencyKey: "idem_cancel_create", Workload: rev}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_cancel"); err != nil {
		t.Fatalf("advance run: %v", err)
	}
	record, err := orch.CancelRun(ctx, "ws_1", "run_cancel", nil)
	if err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if !record.Closed || record.Outcome != domain.RunOutcomeCancelled || record.Cleanup != domain.CleanupConfirmed {
		t.Fatalf("unexpected cancelled record: %+v", record)
	}
	again, err := orch.CancelRun(ctx, "ws_1", "run_cancel", nil)
	if err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
	if again.Outcome != domain.RunOutcomeCancelled {
		t.Fatalf("cancel replay changed outcome: %+v", again)
	}
	events, err := orch.GetRunEvents(ctx, "ws_1", "run_cancel")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, eventType := range []string{EventCancelRequested, EventRunOutcomeRecorded, EventCleanupRequested, EventCleanupConfirmed, EventRunClosed} {
		if !hasEvent(events, eventType) {
			t.Fatalf("expected %s in %s", eventType, eventTypes(events))
		}
	}
	if hasEvent(events, EventCancelAccepted) {
		t.Fatalf("cancellation must use the terminal cleanup path without cancel_accepted: %s", eventTypes(events))
	}
	if ad.TerminateCount() != 1 || ad.ReleaseCount() != 0 {
		t.Fatalf("cleanup counts: terminate=%d release=%d, want one termination", ad.TerminateCount(), ad.ReleaseCount())
	}
	owned, err := ad.ListOwned(ctx, adapter.OwnershipQuery{WorkspaceID: "ws_1"})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 0 {
		t.Fatalf("cancel cleanup should release owned objects: %+v", owned)
	}
}

func TestAdvanceRunCommandKeysAreScopedPerRun(t *testing.T) {
	ctx := context.Background()
	offer := orchOffer("offer_command_scope", time.Now().UTC())
	ad := fake.New(fake.WithOffers([]domain.OfferSnapshot{offer}), fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded))
	orch := newTestOrchestrator(t, ad)
	for _, runID := range []string{"run_scope_a", "run_scope_b"} {
		if _, err := orch.CreateRun(ctx, CreateRunRequest{WorkspaceID: "ws_1", RunID: runID, IdempotencyKey: "idem_" + runID, Workload: orchRevision()}); err != nil {
			t.Fatalf("create %s: %v", runID, err)
		}
		if err := orch.AdvanceRun(ctx, "ws_1", runID); err != nil {
			t.Fatalf("advance %s: %v", runID, err)
		}
		record, err := orch.GetRun(ctx, "ws_1", runID)
		if err != nil {
			t.Fatalf("get %s: %v", runID, err)
		}
		if !record.Closed || record.Cleanup != domain.CleanupConfirmed {
			t.Fatalf("run %s did not close cleanly: %+v", runID, record)
		}
	}
}

func TestRunEventIDsAreScopedAcrossWorkspaces(t *testing.T) {
	ctx := context.Background()
	offerA := orchOffer("offer_workspace_a", time.Now().UTC())
	offerB := orchOffer("offer_workspace_b", time.Now().UTC())
	offerB.ID = "offer_workspace_b"
	offerB.ConnectionID = "conn_2"
	orch := newTestOrchestrator(t, fake.New(fake.WithOffers([]domain.OfferSnapshot{offerA, offerB}), fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded)))
	for _, workspaceID := range []string{"ws_1", "ws_2"} {
		rev := orchRevision()
		rev.WorkspaceID = workspaceID
		if _, err := orch.CreateRun(ctx, CreateRunRequest{WorkspaceID: workspaceID, RunID: "run_same", IdempotencyKey: "idem_" + workspaceID, Workload: rev}); err != nil {
			t.Fatalf("create %s: %v", workspaceID, err)
		}
		events, err := orch.GetRunEvents(ctx, workspaceID, "run_same")
		if err != nil {
			t.Fatalf("events %s: %v", workspaceID, err)
		}
		if len(events) != 1 || events[0].WorkspaceID != workspaceID {
			t.Fatalf("unexpected events for %s: %+v", workspaceID, events)
		}
		if err := orch.AdvanceRun(ctx, workspaceID, "run_same"); err != nil {
			t.Fatalf("advance %s: %v", workspaceID, err)
		}
		record, err := orch.GetRun(ctx, workspaceID, "run_same")
		if err != nil {
			t.Fatalf("get %s: %v", workspaceID, err)
		}
		if !record.Closed || record.Cleanup != domain.CleanupConfirmed {
			t.Fatalf("unexpected record for %s: %+v", workspaceID, record)
		}
	}
}

func TestCancelRunResumesAfterCancelAcceptedEvents(t *testing.T) {
	ctx := context.Background()
	ad := fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("offer_cancel_resume", time.Now().UTC())}), fake.WithLaunchOutcome(adapter.ExternalPhaseRunning))
	orch := newTestOrchestrator(t, ad)
	runID := "run_cancel_resume"
	if _, err := orch.CreateRun(ctx, CreateRunRequest{WorkspaceID: "ws_1", RunID: runID, IdempotencyKey: "idem_" + runID, Workload: orchRevision()}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", runID); err != nil {
		t.Fatalf("advance run: %v", err)
	}
	events, err := orch.GetRunEvents(ctx, "ws_1", runID)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	state, err := reduceRun(events)
	if err != nil {
		t.Fatalf("reduce run: %v", err)
	}
	if err := orch.appendEvents(ctx, "ws_1", runID, uint64(len(events)), "cancel:accepted", []eventlog.NewEvent{
		mustEvent(runID, "cancel_requested", EventCancelRequested, cancelRequestedData{LaunchKey: state.launchIntent.LaunchKey}, time.Now()),
		mustEvent(runID, "cancel_accepted", EventCancelAccepted, launchReferenceData{LaunchKey: state.launchIntent.LaunchKey}, time.Now()),
	}); err != nil {
		t.Fatalf("seed cancel events: %v", err)
	}

	record, err := orch.CancelRun(ctx, "ws_1", runID, nil)
	if err != nil {
		t.Fatalf("resume cancel: %v", err)
	}
	if !record.Closed || record.Outcome != domain.RunOutcomeCancelled || record.Cleanup != domain.CleanupConfirmed {
		t.Fatalf("unexpected resumed cancel record: %+v", record)
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

func TestAdvanceRunRetriesCleanupWithoutRelaunch(t *testing.T) {
	ctx := context.Background()
	ad := &releaseFailsOnceAdapter{
		Adapter: fake.New(
			fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
			fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
		),
	}
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err == nil {
		t.Fatal("first advance should report release failure after recording cleanup request")
	}
	if ad.launchCalls != 1 || ad.releaseCalls != 1 {
		t.Fatalf("unexpected first side effects: launches=%d releases=%d", ad.launchCalls, ad.releaseCalls)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("second advance should resume cleanup: %v", err)
	}
	if ad.launchCalls != 1 || ad.releaseCalls != 2 {
		t.Fatalf("cleanup retry should not relaunch: launches=%d releases=%d", ad.launchCalls, ad.releaseCalls)
	}
	events, err := orch.GetRunEvents(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if countEvents(events, EventRunClosed) != 1 {
		t.Fatalf("expected cleanup retry to close run, got %s", eventTypes(events))
	}
}

func TestAdvanceRunReconcilesIndeterminateLaunchBeforeRetry(t *testing.T) {
	ctx := context.Background()
	ad := &indeterminateLaunchAdapter{
		Adapter: fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})),
	}
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("expected indeterminate launch error, got %v", err)
	}
	events, err := orch.GetRunEvents(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if countEvents(events, EventLaunchFailed) != 0 {
		t.Fatalf("indeterminate launch must not be recorded as simple failure: %s", eventTypes(events))
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("second advance should reconcile existing launch before retry: %v", err)
	}
	if ad.launchCalls != 1 {
		t.Fatalf("expected no blind relaunch after indeterminate result, got %d launches", ad.launchCalls)
	}
	if ad.observeCalls == 0 && ad.listOwnedCalls == 0 {
		t.Fatalf("expected observe or list-owned reconciliation before retry")
	}
}

func TestAdvanceRunClosesMissingIndeterminateLaunchAsFailed(t *testing.T) {
	ctx := context.Background()
	ad := &missingIndeterminateLaunchAdapter{
		Adapter: fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})),
	}
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("expected indeterminate launch error, got %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("second advance should close missing launch: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if !record.Closed || record.Outcome != domain.RunOutcomeFailed || record.Cleanup != domain.CleanupConfirmed {
		t.Fatalf("missing indeterminate launch should close failed, got %+v", record)
	}
}

func TestAdvanceRunUsesListOwnedForIndeterminateLaunchRecovery(t *testing.T) {
	ctx := context.Background()
	ad := &ownedIndeterminateLaunchAdapter{
		Adapter: fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())})),
	}
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("expected indeterminate launch error, got %v", err)
	}
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("second advance should reconcile owned launch: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Closed || record.Phase != "running" {
		t.Fatalf("owned indeterminate launch should remain running, got %+v", record)
	}
	if ad.listOwnedCalls == 0 {
		t.Fatalf("expected ListOwned recovery")
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

type releaseFailsOnceAdapter struct {
	*fake.Adapter
	launchCalls  int
	releaseCalls int
}

func (r *releaseFailsOnceAdapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	r.launchCalls++
	return r.Adapter.Launch(ctx, req)
}

func (r *releaseFailsOnceAdapter) Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	r.releaseCalls++
	if r.releaseCalls == 1 {
		return adapter.ReleaseReceipt{}, adapter.ErrRetryableFailure
	}
	return r.Adapter.Release(ctx, req)
}

type indeterminateLaunchAdapter struct {
	*fake.Adapter
	launchCalls    int
	observeCalls   int
	listOwnedCalls int
	launchKey      string
}

func (i *indeterminateLaunchAdapter) Launch(_ context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	i.launchCalls++
	i.launchKey = req.LaunchKey
	return adapter.LaunchReceipt{}, adapter.ErrLaunchIndeterminate
}

func (i *indeterminateLaunchAdapter) Observe(_ context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	i.observeCalls++
	return adapter.ExternalObservation{ExternalID: "ambiguous", LaunchKey: req.LaunchKey, Phase: adapter.ExternalPhaseRunning, ObservedAt: time.Now().UTC()}, nil
}

func (i *indeterminateLaunchAdapter) ListOwned(context.Context, adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	i.listOwnedCalls++
	return []adapter.OwnedExternalObject{{ExternalID: "ambiguous", WorkspaceID: "ws_1", RunID: "run_1", AttemptID: "att_1", OwnershipToken: "own_1", LaunchKey: i.launchKey, Phase: adapter.ExternalPhaseRunning}}, nil
}

type missingIndeterminateLaunchAdapter struct {
	*fake.Adapter
	launchCalls int
}

func (m *missingIndeterminateLaunchAdapter) Launch(context.Context, adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	m.launchCalls++
	return adapter.LaunchReceipt{}, adapter.ErrLaunchIndeterminate
}

func (m *missingIndeterminateLaunchAdapter) Observe(_ context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	return adapter.ExternalObservation{LaunchKey: req.LaunchKey, Phase: adapter.ExternalPhaseReleased, ObservedAt: time.Now().UTC()}, nil
}

type ownedIndeterminateLaunchAdapter struct {
	*fake.Adapter
	launchReq      adapter.LaunchRequest
	listOwnedCalls int
}

func (o *ownedIndeterminateLaunchAdapter) Launch(_ context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	o.launchReq = req
	return adapter.LaunchReceipt{}, adapter.ErrLaunchIndeterminate
}

func (o *ownedIndeterminateLaunchAdapter) Observe(_ context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	return adapter.ExternalObservation{LaunchKey: req.LaunchKey, Phase: adapter.ExternalPhaseReleased, ObservedAt: time.Now().UTC()}, nil
}

func (o *ownedIndeterminateLaunchAdapter) ListOwned(context.Context, adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	o.listOwnedCalls++
	return []adapter.OwnedExternalObject{{
		ExternalID:     "owned-" + o.launchReq.AttemptID,
		WorkspaceID:    o.launchReq.WorkspaceID,
		RunID:          o.launchReq.RunID,
		AttemptID:      o.launchReq.AttemptID,
		OwnershipToken: o.launchReq.OwnershipToken,
		LaunchKey:      o.launchReq.LaunchKey,
		CleanupLocator: o.launchReq.CleanupLocator,
		RequestHash:    o.launchReq.RequestHash,
		Phase:          adapter.ExternalPhaseRunning,
	}}, nil
}

type captureLaunchAdapter struct {
	*fake.Adapter
	offerRequest  adapter.OfferRequest
	launchRequest adapter.LaunchRequest
}

func (c *captureLaunchAdapter) ListOffers(ctx context.Context, req adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	c.offerRequest = req
	return c.Adapter.ListOffers(ctx, req)
}

func (c *captureLaunchAdapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	c.launchRequest = req
	return c.Adapter.Launch(ctx, req)
}

func newTestOrchestrator(t *testing.T, ad Adapter) *Orchestrator {
	t.Helper()
	return New(openOrchestratorLog(t), scheduler.New(), ad)
}

type streamReadCountingLog struct {
	eventlog.WorkspaceEventLog
	streamReads int
}

func (l *streamReadCountingLog) ReadStream(ctx context.Context, stream eventlog.StreamKey, afterVersion uint64, limit int) ([]eventlog.StoredEvent, error) {
	l.streamReads++
	return l.WorkspaceEventLog.ReadStream(ctx, stream, afterVersion, limit)
}

func openOrchestratorLog(t *testing.T) eventlog.WorkspaceEventLog {
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
	return workspaceTestLog{EventLog: log}
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

func orchProvisionableOffer(id string, now time.Time) domain.OfferSnapshot {
	offer := orchOffer(id, now)
	offer.Kind = domain.OfferKindProvisionable
	return offer
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
			Container: domain.ContainerCapabilities{MaxContainers: 1, SupportsDigestRefs: true, SupportsEntrypointOverride: true, MaxEnvironmentBytes: 32768},
			Network:   domain.NetworkCapabilities{Inbound: domain.InboundNetworkNone, Protocols: []string{"tcp"}},
			Pricing:   domain.PricingCapabilities{Known: true},
			Lifecycle: domain.LifecycleCapabilities{IdempotentLaunch: "deterministic_name", ListOwned: true},
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

func mustHash(t *testing.T, v any) string {
	t.Helper()
	hash, err := domain.CanonicalHash(v)
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	return hash
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

func findLaunchEnv(t *testing.T, bindings []adapter.EnvironmentBinding, name string) adapter.EnvironmentBinding {
	t.Helper()
	for _, binding := range bindings {
		if binding.Name == name {
			return binding
		}
	}
	t.Fatalf("environment binding %s not found in %+v", name, bindings)
	return adapter.EnvironmentBinding{}
}

func TestAdvanceRunDoesNotAppendUnchangedObservations(t *testing.T) {
	// A run polled repeatedly while still running must not grow its event stream
	// on every poll: waitRun refreshes every 100ms, so per-poll appends brick the
	// stream once it outgrows a single read page. 1500 polls comfortably exceeds
	// that page size if each one were to append.
	ctx := context.Background()
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
		fake.WithOpenObservations(1500),
	)
	orch := newTestOrchestrator(t, ad)
	createRun(t, ctx, orch)

	for i := 0; i < 1500; i++ {
		if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
	}
	events, err := orch.GetRunEvents(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if n := countEvents(events, EventExternalStateObserved); n != 1 {
		t.Fatalf("expected exactly one recorded observation across identical running polls, got %d", n)
	}

	// The next poll observes the terminal phase; the run must still close.
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("terminal advance: %v", err)
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Phase != "closed" || record.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("expected closed/succeeded run, got phase=%q outcome=%q", record.Phase, record.Outcome)
	}
	events, err = orch.GetRunEvents(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get events after close: %v", err)
	}
	if n := countEvents(events, EventExternalStateObserved); n != 2 {
		t.Fatalf("expected running + terminal observations only, got %d", n)
	}
}

func TestAdvanceRunSurvivesStreamsLongerThanOneReadPage(t *testing.T) {
	// Streams longer than one 1000-event read page must still reduce and append
	// at the true stream version instead of wedging on a concurrency conflict.
	ctx := context.Background()
	log := openOrchestratorLog(t)
	ad := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	orch := New(log, scheduler.New(), ad)
	createRun(t, ctx, orch)

	fillerData, err := json.Marshal(domain.ProviderError{
		Code:      "TEST_PAGINATION_FILLER",
		Message:   "Pagination filler.",
		Retryable: false,
		LaunchKey: "launch_pagination_filler",
	})
	if err != nil {
		t.Fatalf("marshal pagination filler: %v", err)
	}
	version := uint64(1) // run_requested
	for batch := 0; batch < 11; batch++ {
		var filler []eventlog.NewEvent
		for i := 0; i < 100; i++ {
			filler = append(filler, eventlog.NewEvent{
				ID:            fmt.Sprintf("evt_run_1_filler_%d_%d", batch, i),
				Type:          EventLaunchFailed,
				SchemaVersion: 1,
				OccurredAt:    time.Now().UTC(),
				Visibility:    eventlog.VisibilityPublic,
				Data:          fillerData,
			})
		}
		if _, err := log.Append(ctx, eventlog.AppendRequest{
			Stream:                runStream("ws_1", "run_1"),
			ExpectedStreamVersion: version,
			CommandKey:            fmt.Sprintf("run_1:filler:%d", batch),
			RequestHash:           mustHash(t, filler),
			CorrelationID:         "run_1",
			CausationID:           "filler",
			Events:                filler,
		}); err != nil {
			t.Fatalf("append filler batch %d: %v", batch, err)
		}
		version += 100
	}

	events, err := orch.GetRunEvents(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(events) != 1101 {
		t.Fatalf("expected full paginated read of 1101 events, got %d", len(events))
	}

	for i := 0; i < 3; i++ {
		if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
			t.Fatalf("advance %d past read page: %v", i, err)
		}
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Phase != "closed" || record.Outcome != domain.RunOutcomeSucceeded {
		t.Fatalf("expected closed/succeeded run, got phase=%q outcome=%q", record.Phase, record.Outcome)
	}
}

func TestAdvanceRunClosesRunOnDefinitiveLaunchFailure(t *testing.T) {
	// A definitive launch rejection must terminate the run (failed outcome,
	// closed) rather than retrying the same doomed launch on every poll and
	// leaving the run wedged in "launching" forever.
	ctx := context.Background()
	orch := newTestOrchestrator(t, conflictAdapter{Adapter: fake.New(fake.WithOffers([]domain.OfferSnapshot{orchOffer("off_1", time.Now().UTC())}))})
	createRun(t, ctx, orch)

	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err == nil {
		t.Fatal("expected advance to report the launch failure")
	}
	record, err := orch.GetRun(ctx, "ws_1", "run_1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if record.Phase != "closed" || record.Outcome != domain.RunOutcomeFailed {
		t.Fatalf("expected closed/failed run after definitive launch failure, got phase=%q outcome=%q", record.Phase, record.Outcome)
	}
	if record.Cleanup != domain.CleanupNotRequired {
		t.Fatalf("nothing launched, so cleanup must not be required; got %q", record.Cleanup)
	}
	// Subsequent advances are no-ops on the closed run, not relaunch attempts.
	if err := orch.AdvanceRun(ctx, "ws_1", "run_1"); err != nil {
		t.Fatalf("advance on closed run must be a no-op, got %v", err)
	}
}

func hasEvent(events []eventlog.StoredEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func decodeRunRequested(events []eventlog.StoredEvent) (runRequestedData, error) {
	for _, event := range events {
		if event.Type == EventRunRequested {
			var data runRequestedData
			payload := event.PrivateData
			if len(payload) == 0 {
				payload = event.Data
			}
			if err := json.Unmarshal(payload, &data); err != nil {
				return runRequestedData{}, err
			}
			return data, nil
		}
	}
	return runRequestedData{}, fmt.Errorf("orchestrator: run requested event not found")
}
