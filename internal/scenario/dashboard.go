package scenario

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/gpunorm"
	"github.com/benngarcia/mercator/internal/orchestrator"
	"github.com/benngarcia/mercator/internal/scheduler"
	"github.com/google/uuid"
)

const (
	ProvenanceOrchestrator   = "orchestrator"
	ProvenanceTargetContract = "target_contract"
	dashboardStepMillis      = 3500
	dashboardGib             = int64(1024 * 1024 * 1024)
)

//go:embed testdata/dashboard_offer_catalog.json
var dashboardOfferCatalogJSON []byte

type DashboardFidelity struct {
	OfferSource        string   `json:"offer_source"`
	ProvenCapabilities []string `json:"proven_capabilities"`
	TargetCapabilities []string `json:"target_capabilities"`
}

type DashboardOfferCatalog struct {
	WorkspaceID string                 `json:"workspace_id"`
	Revision    string                 `json:"revision"`
	ObservedAt  time.Time              `json:"observed_at"`
	Offers      []domain.OfferSnapshot `json:"offers"`
	Failures    []any                  `json:"failures"`
}

type DashboardMessage struct {
	Type                  string                  `json:"type"`
	Event                 *eventlog.CloudEvent    `json:"event,omitempty"`
	Catalog               *DashboardOfferCatalog  `json:"catalog,omitempty"`
	ThroughGlobalPosition eventlog.GlobalPosition `json:"through_global_position,omitempty"`
}

type DashboardStep struct {
	ID         string           `json:"id"`
	Label      string           `json:"label"`
	AtMillis   int              `json:"at_millis"`
	Provenance string           `json:"provenance"`
	Message    DashboardMessage `json:"message"`
}

type DashboardTranscript struct {
	Baseline       []DashboardMessage `json:"baseline"`
	Steps          []DashboardStep    `json:"steps"`
	DurationMillis int                `json:"duration_millis"`
	Fidelity       DashboardFidelity  `json:"fidelity"`
}

func (t DashboardTranscript) OfferCatalog() *DashboardOfferCatalog {
	for _, message := range t.Baseline {
		if message.Catalog != nil {
			return message.Catalog
		}
	}
	return nil
}

func BuildDashboardTranscript(ctx context.Context, workspaceID string) (DashboardTranscript, error) {
	if workspaceID == "" {
		return DashboardTranscript{}, fmt.Errorf("dashboard scenario requires a Workspace")
	}
	offers, observedAt, err := loadDashboardOffers()
	if err != nil {
		return DashboardTranscript{}, err
	}
	realEvents, err := runDashboardSimulation(ctx, workspaceID, observedAt, offers)
	if err != nil {
		return DashboardTranscript{}, err
	}
	baseline, position, err := dashboardBaseline(workspaceID, observedAt, offers)
	if err != nil {
		return DashboardTranscript{}, err
	}
	steps := make([]DashboardStep, 0, len(realEvents)+8)
	for _, event := range realEvents {
		position++
		event.GlobalPosition = position
		steps = append(steps, dashboardStep(event, ProvenanceOrchestrator, len(steps)))
	}
	for _, event := range dashboardQueueDrain(workspaceID, observedAt, position) {
		steps = append(steps, dashboardStep(event, ProvenanceTargetContract, len(steps)))
	}
	return DashboardTranscript{
		Baseline:       baseline,
		Steps:          steps,
		DurationMillis: (len(steps) + 1) * dashboardStepMillis,
		Fidelity: DashboardFidelity{
			OfferSource:        "sanitized_recordings",
			ProvenCapabilities: []string{"placement", "launch_replacement", "run_lifecycle", "cleanup"},
			TargetCapabilities: []string{"rental_schedule"},
		},
	}, nil
}

func dashboardStep(event eventlog.CloudEvent, provenance string, index int) DashboardStep {
	return DashboardStep{
		ID:         fmt.Sprintf("step-%02d", index+1),
		Label:      event.Type,
		AtMillis:   (index + 1) * dashboardStepMillis,
		Provenance: provenance,
		Message:    DashboardMessage{Type: "domain_event", Event: &event},
	}
}

type recordedDashboardCatalog struct {
	RecordedAt time.Time                `json:"recorded_at"`
	Offers     []recordedDashboardOffer `json:"offers"`
}

type recordedDashboardOffer struct {
	ID                       string  `json:"id"`
	ConnectionID             string  `json:"connection_id"`
	AdapterType              string  `json:"adapter_type"`
	Kind                     string  `json:"kind"`
	NativeRef                string  `json:"native_ref"`
	RentalID                 string  `json:"rental_id"`
	HourlyUSD                float64 `json:"hourly_usd"`
	CPUMillis                int64   `json:"cpu_millis"`
	MemoryGiB                int64   `json:"memory_gib"`
	DiskGiB                  int64   `json:"disk_gib"`
	GPUVendor                string  `json:"gpu_vendor"`
	GPUModel                 string  `json:"gpu_model"`
	GPUCount                 int     `json:"gpu_count"`
	GPUMemoryGiB             int64   `json:"gpu_memory_gib"`
	ManifestCached           bool    `json:"manifest_cached"`
	InterruptionRate         float64 `json:"interruption_rate"`
	ProvisionExpectedSeconds float64 `json:"provision_expected_seconds"`
	ProvisionP90Seconds      float64 `json:"provision_p90_seconds"`
	RecordedFrom             string  `json:"recorded_from"`
}

func loadDashboardOffers() ([]domain.OfferSnapshot, time.Time, error) {
	var catalog recordedDashboardCatalog
	if err := json.Unmarshal(dashboardOfferCatalogJSON, &catalog); err != nil {
		return nil, time.Time{}, fmt.Errorf("decode dashboard Offer recordings: %w", err)
	}
	if catalog.RecordedAt.IsZero() || len(catalog.Offers) == 0 {
		return nil, time.Time{}, fmt.Errorf("dashboard Offer recordings require recorded_at and offers")
	}
	offers := make([]domain.OfferSnapshot, 0, len(catalog.Offers))
	for _, recorded := range catalog.Offers {
		offer, err := recorded.dashboardOffer(catalog.RecordedAt)
		if err != nil {
			return nil, time.Time{}, err
		}
		offers = append(offers, offer)
	}
	return offers, catalog.RecordedAt, nil
}

func (r recordedDashboardOffer) dashboardOffer(observedAt time.Time) (domain.OfferSnapshot, error) {
	if r.ID == "" || r.ConnectionID == "" || r.AdapterType == "" || r.NativeRef == "" || r.RecordedFrom == "" {
		return domain.OfferSnapshot{}, fmt.Errorf("dashboard Offer recording is missing identity or provenance: %q", r.ID)
	}
	if r.HourlyUSD <= 0 || r.CPUMillis <= 0 || r.MemoryGiB <= 0 || r.DiskGiB <= 0 {
		return domain.OfferSnapshot{}, fmt.Errorf("dashboard Offer recording %q has incomplete resources or pricing", r.ID)
	}
	kind := domain.OfferKind(r.Kind)
	if kind != domain.OfferKindStanding && kind != domain.OfferKindProvisionable {
		return domain.OfferSnapshot{}, fmt.Errorf("dashboard Offer recording %q has invalid kind %q", r.ID, r.Kind)
	}
	offer := domain.OfferSnapshot{
		ID:           r.ID,
		RentalID:     r.RentalID,
		ConnectionID: r.ConnectionID,
		AdapterType:  r.AdapterType,
		Kind:         kind,
		NativeRef:    r.NativeRef,
		ObservedAt:   observedAt,
		ExpiresAt:    observedAt.Add(5 * time.Minute),
		Platform:     domain.Platform{OS: "linux", Architecture: "amd64"},
		Resources: domain.ResourceInventory{
			CPUMillis:          r.CPUMillis,
			MemoryBytes:        r.MemoryGiB * dashboardGib,
			EphemeralDiskBytes: r.DiskGiB * dashboardGib,
		},
		Capabilities: dashboardCapabilities(r.AdapterType, kind, r.GPUVendor),
		Pricing: domain.PriceModel{
			Currency:           "USD",
			RatePerSecondUSD:   r.HourlyUSD / 3600,
			GranularitySeconds: 1,
			Known:              true,
		},
		ImageCache: domain.ImageCacheEvidence{ManifestCached: r.ManifestCached, Known: true},
		Capacity:   domain.CapacityEvidence{Available: true, Confidence: 1},
		Reliability: domain.ReliabilityEvidence{
			InterruptionRate: r.InterruptionRate,
			Confidence:       1,
		},
	}
	if r.GPUCount > 0 {
		offer.Resources.Accelerators = []domain.AcceleratorInventory{{
			Vendor:         r.GPUVendor,
			Model:          r.GPUModel,
			CanonicalModel: gpunorm.Canonical(r.GPUVendor, r.GPUModel),
			Count:          r.GPUCount,
			MemoryBytes:    r.GPUMemoryGiB * dashboardGib,
		}}
	}
	if r.ProvisionExpectedSeconds > 0 {
		offer.Provisioning = &domain.Estimate{
			Expected: r.ProvisionExpectedSeconds,
			P50:      r.ProvisionExpectedSeconds,
			P90:      r.ProvisionP90Seconds,
			Source:   r.AdapterType + ":recorded",
		}
	}
	return offer, nil
}

func dashboardCapabilities(adapterType string, kind domain.OfferKind, gpuVendor string) domain.CapabilityProfile {
	providerTTL := adapterType == "shadeform"
	entrypoint := adapterType == "docker"
	inbound := domain.InboundNetworkPublicPort
	if adapterType == "docker" || adapterType == "shadeform" {
		inbound = domain.InboundNetworkNone
	}
	return domain.CapabilityProfile{
		OfferKinds: []domain.OfferKind{kind},
		Container: domain.ContainerCapabilities{
			MaxContainers:              1,
			SupportsDigestRefs:         true,
			SupportsEntrypointOverride: entrypoint,
			MaxEnvironmentBytes:        32768,
		},
		Lifecycle: domain.LifecycleCapabilities{
			IdempotentLaunch: "launch_key",
			ListOwned:        true,
			ProviderTTL:      providerTTL,
			CancelQueued:     providerTTL,
		},
		Resources:     domain.ResourceCapabilities{GPUVendors: []string{gpuVendor}},
		Network:       domain.NetworkCapabilities{Inbound: inbound, PublicIPv4: adapterType != "docker"},
		Pricing:       domain.PricingCapabilities{Known: true},
		Observability: domain.ObservabilityCapabilities{Logs: "native", Metrics: "none", Shell: "native"},
	}
}

type dashboardProvider struct {
	*fake.Adapter
	offers       []domain.OfferSnapshot
	launchErrors map[string]error
}

func (p *dashboardProvider) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return append([]domain.OfferSnapshot(nil), p.offers...), nil
}

func (p *dashboardProvider) Launch(ctx context.Context, request adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	if err := p.launchErrors[request.SelectedOfferSnapshotID]; err != nil {
		return adapter.LaunchReceipt{}, err
	}
	return p.Adapter.Launch(ctx, request)
}

func runDashboardSimulation(ctx context.Context, workspaceID string, now time.Time, offers []domain.OfferSnapshot) ([]eventlog.CloudEvent, error) {
	log, err := eventlog.OpenSQLite(ctx, "file:dashboard-scenario-"+uuid.NewString()+"?mode=memory&cache=shared")
	if err != nil {
		return nil, fmt.Errorf("open dashboard scenario event log: %w", err)
	}
	defer log.Close()
	clock := fake.NewClock(now)
	provider := &dashboardProvider{
		Adapter: fake.New(
			fake.WithOffers(offers),
			fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
			fake.WithOpenObservations(1),
			fake.WithNow(clock.Now),
		),
		offers: offers,
		launchErrors: map[string]error{
			"off_vast_9001": &adapter.ProviderFailure{
				Kind:       adapter.ProviderFailureCapacityUnavailable,
				Retryable:  true,
				SideEffect: adapter.SideEffectNone,
			},
		},
	}
	orch := orchestrator.New(alwaysActiveWorkspaceLog{log}, scheduler.New(), provider, orchestrator.WithClock(clock.Now))
	for _, run := range []struct {
		id    string
		model string
		mem   int64
	}{
		{id: "run-provider-replacement", model: "RTX 4090", mem: 16},
		{id: "run-render-a6000", model: "RTX A6000", mem: 40},
		{id: "run-training-h100", model: "H100", mem: 64},
	} {
		if _, err := orch.CreateRun(ctx, orchestrator.CreateRunRequest{
			WorkspaceID:    workspaceID,
			RunID:          run.id,
			IdempotencyKey: "scenario:" + run.id,
			Workload:       dashboardWorkload(workspaceID, run.id, run.model, run.mem),
		}); err != nil {
			return nil, fmt.Errorf("create dashboard Run %s: %w", run.id, err)
		}
		if err := orch.AdvanceRun(ctx, workspaceID, run.id); err != nil {
			return nil, fmt.Errorf("start dashboard Run %s: %w", run.id, err)
		}
		clock.Advance(2 * time.Second)
		if err := orch.AdvanceRun(ctx, workspaceID, run.id); err != nil {
			return nil, fmt.Errorf("finish dashboard Run %s: %w", run.id, err)
		}
		clock.Advance(time.Second)
	}
	stored, err := log.ReadAll(ctx, 0, 1000, eventlog.EventFilter{WorkspaceID: workspaceID, Visibility: eventlog.VisibilityPublic})
	if err != nil {
		return nil, fmt.Errorf("read dashboard scenario events: %w", err)
	}
	events := make([]eventlog.CloudEvent, 0, len(stored))
	for _, event := range stored {
		events = append(events, event.CloudEvent())
	}
	return events, nil
}

func dashboardWorkload(workspaceID, runID, model string, gpuMemoryGiB int64) domain.WorkloadRevision {
	return domain.WorkloadRevision{
		ID:          "wrev_" + runID,
		WorkspaceID: workspaceID,
		WorkloadID:  "wrk_" + runID,
		Digest:      "sha256:" + runID,
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:     "main",
				Image:    "ghcr.io/bucketrobotics/scenario-worker@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
			}},
			Resources: domain.ResourceRequirements{
				CPU:    domain.CPURequirement{MinMillis: 4000},
				Memory: domain.MemoryRequirement{MinBytes: 16 * dashboardGib},
				Accelerators: []domain.AcceleratorRequirement{{
					Vendor:         "NVIDIA",
					ModelAnyOf:     []string{model},
					Count:          1,
					MemoryMinBytes: gpuMemoryGiB * dashboardGib,
				}},
			},
			Network:   domain.NetworkRequirements{Inbound: domain.InboundNetworkNone},
			Placement: domain.PlacementPolicy{Objective: domain.ObjectiveCheapest, ExpectedRuntimeSeconds: 20 * 60},
			Execution: domain.ExecutionPolicy{MaxRuntimeSeconds: 60 * 60, MaxPreStartAttempts: 2},
		},
	}
}

func dashboardBaseline(workspaceID string, now time.Time, offers []domain.OfferSnapshot) ([]DashboardMessage, eventlog.GlobalPosition, error) {
	activeWorkload := dashboardWorkload(workspaceID, "run-active", "RTX A6000", 40)
	queuedWorkload := dashboardWorkload(workspaceID, "run-q1", "RTX A6000", 40)
	activeDecision := dashboardBookingDecision("run-active", "booking-active", domain.BookingStateRunning, "", 5, now)
	queuedDecision := dashboardBookingDecision("run-q1", "booking-q1", domain.BookingStateQueued, "booking-active", 6, now)
	events := []eventlog.CloudEvent{
		dashboardContractEvent(workspaceID, "baseline-active-requested", orchestrator.EventRunRequested, "runs/run-active", 1, now, map[string]any{"run_id": "run-active", "workload_revision": activeWorkload}),
		dashboardContractEvent(workspaceID, "baseline-active-decided", orchestrator.EventBookingDecided, "runs/run-active", 2, now, map[string]any{"decision": activeDecision}),
		dashboardContractEvent(workspaceID, "baseline-q1-requested", orchestrator.EventRunRequested, "runs/run-q1", 3, now, map[string]any{"run_id": "run-q1", "workload_revision": queuedWorkload}),
		dashboardContractEvent(workspaceID, "baseline-q1-decided", orchestrator.EventBookingDecided, "runs/run-q1", 4, now, map[string]any{"decision": queuedDecision}),
	}
	baseline := []DashboardMessage{{
		Type: "offers_replaced",
		Catalog: &DashboardOfferCatalog{
			WorkspaceID: workspaceID,
			Revision:    "scenario-recorded-offers-v1",
			ObservedAt:  now,
			Offers:      offers,
			Failures:    []any{},
		},
	}}
	for i := range events {
		baseline = append(baseline, DashboardMessage{Type: "domain_event", Event: &events[i]})
	}
	baseline = append(baseline, DashboardMessage{Type: "ready", ThroughGlobalPosition: 4})
	return baseline, 4, nil
}

func dashboardBookingDecision(runID, bookingID string, state domain.BookingState, after string, version uint64, now time.Time) domain.BookingDecision {
	booking := &domain.Booking{
		ID:              bookingID,
		RentalID:        "rental-warm",
		State:           state,
		AfterBookingID:  after,
		ScheduleVersion: version,
	}
	if state == domain.BookingStateQueued {
		projected := now.Add(4 * time.Minute)
		booking.ProjectedStartAt = &projected
	}
	return domain.BookingDecision{
		ID:                     "dec-" + runID,
		RunID:                  runID,
		WorkloadRevisionDigest: "sha256:" + runID,
		EvaluatedAt:            now,
		ModelVersion:           "rental-schedule-target-v1",
		Policy:                 domain.PlacementPolicy{Objective: domain.ObjectiveBalanced},
		Candidates: []domain.CandidateDecision{{
			OfferSnapshotID: "rental-warm",
			ConnectionID:    "conn-docker-warm",
			AdapterType:     "docker",
			NativeRef:       "rental-warm",
			Feasible:        true,
		}},
		SelectedOfferSnapshotID: "rental-warm",
		Booking:                 booking,
		SelectionReasonCodes:    []string{"FEASIBLE", "LOWEST_SCORE"},
	}
}

func dashboardQueueDrain(workspaceID string, now time.Time, after eventlog.GlobalPosition) []eventlog.CloudEvent {
	position := after
	next := func(id, eventType, subject, runID string, data any) eventlog.CloudEvent {
		position++
		event := dashboardContractEvent(workspaceID, id, eventType, subject, position, now.Add(time.Duration(position)*time.Second), data)
		event.CorrelationID = runID
		return event
	}
	return []eventlog.CloudEvent{
		next("target-q1-queued", "compute.rental.booking_queued.v1", "rentals/rental-warm", "run-q1", map[string]any{
			"run_id":  "run-q1",
			"booking": map[string]any{"id": "booking-q1", "rental_id": "rental-warm", "after_booking_id": "booking-active", "schedule_version": 6},
		}),
		next("target-active-outcome", orchestrator.EventRunOutcomeRecorded, "runs/run-active", "run-active", map[string]any{"outcome": "succeeded"}),
		next("target-active-cleanup", orchestrator.EventCleanupRequested, "runs/run-active", "run-active", map[string]any{"launch_key": "launch-active"}),
		next("target-active-closed", orchestrator.EventRunClosed, "runs/run-active", "run-active", map[string]any{"closed": true}),
		next("target-q1-dispatched", "compute.rental.booking_dispatched.v1", "rentals/rental-warm", "run-q1", map[string]any{
			"run_id":  "run-q1",
			"booking": map[string]any{"id": "booking-q1", "rental_id": "rental-warm", "schedule_version": 7},
		}),
		next("target-q1-running", orchestrator.EventExternalStateObserved, "runs/run-q1", "run-q1", map[string]any{"phase": "running"}),
		next("target-q1-outcome", orchestrator.EventRunOutcomeRecorded, "runs/run-q1", "run-q1", map[string]any{"outcome": "succeeded"}),
		next("target-q1-cleanup", orchestrator.EventCleanupRequested, "runs/run-q1", "run-q1", map[string]any{"launch_key": "launch-q1"}),
		next("target-q1-closed", orchestrator.EventRunClosed, "runs/run-q1", "run-q1", map[string]any{"closed": true}),
	}
}

func dashboardContractEvent(workspaceID, id, eventType, subject string, position eventlog.GlobalPosition, occurredAt time.Time, data any) eventlog.CloudEvent {
	encoded, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return eventlog.CloudEvent{
		SpecVersion:    "1.0",
		ID:             id,
		Source:         "compute-control-plane/scenario-contracts/rental-schedule",
		Type:           eventType,
		Subject:        subject,
		Time:           occurredAt.UTC().Format(time.RFC3339Nano),
		WorkspaceID:    workspaceID,
		StreamVersion:  uint64(position),
		GlobalPosition: position,
		Data:           encoded,
	}
}
