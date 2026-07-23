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
	ThroughGlobalPosition eventlog.GlobalPosition `json:"through_global_position"`
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
	return BuildDashboardScenarioTranscript(ctx, workspaceID, DashboardScenarioName)
}

func BuildDashboardScenarioTranscript(ctx context.Context, workspaceID, scenarioName string) (DashboardTranscript, error) {
	if workspaceID == "" {
		return DashboardTranscript{}, fmt.Errorf("dashboard scenario requires a Workspace")
	}
	offers, observedAt, err := loadDashboardOffers()
	if err != nil {
		return DashboardTranscript{}, err
	}
	offers, err = dashboardScenarioOffers(scenarioName, offers)
	if err != nil {
		return DashboardTranscript{}, err
	}
	realEvents, err := runDashboardSimulation(ctx, workspaceID, scenarioName, observedAt, offers)
	if err != nil {
		return DashboardTranscript{}, err
	}
	baseline := dashboardBaseline(workspaceID, observedAt, offers)
	steps := make([]DashboardStep, 0, len(realEvents))
	for _, event := range realEvents {
		steps = append(steps, dashboardStep(event, ProvenanceOrchestrator, len(steps)))
	}
	return DashboardTranscript{
		Baseline:       baseline,
		Steps:          steps,
		DurationMillis: (len(steps) + 1) * dashboardStepMillis,
		Fidelity: DashboardFidelity{
			OfferSource:        "sanitized_recordings",
			ProvenCapabilities: []string{"placement", "rental_schedule", "queued_dispatch", "launch_replacement", "run_lifecycle", "cleanup"},
			TargetCapabilities: []string{},
		},
	}, nil
}

func dashboardScenarioOffers(scenarioName string, offers []domain.OfferSnapshot) ([]domain.OfferSnapshot, error) {
	if !validDashboardScenario(scenarioName) {
		return nil, fmt.Errorf("%w %q", ErrUnknownDashboardScenario, scenarioName)
	}
	adjusted := append([]domain.OfferSnapshot(nil), offers...)
	for index := range adjusted {
		if adjusted[index].RentalID == "rental-warm" {
			adjusted[index].Pricing.RatePerSecondUSD = 0.10 / 3600
		}
	}
	return adjusted, nil
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

func runDashboardSimulation(ctx context.Context, workspaceID, scenarioName string, now time.Time, offers []domain.OfferSnapshot) ([]eventlog.CloudEvent, error) {
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
	if err := executeDashboardScenario(ctx, workspaceID, scenarioName, orch, clock); err != nil {
		return nil, err
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

func executeDashboardScenario(ctx context.Context, workspaceID, scenarioName string, orch *orchestrator.Orchestrator, clock *fake.Clock) error {
	switch scenarioName {
	case DashboardScenarioWarmPoolBurst:
		return executeWarmPoolBurst(ctx, workspaceID, orch, clock)
	case DashboardScenarioDeadlineCost:
		return executeDeadlineCost(ctx, workspaceID, orch, clock)
	case DashboardScenarioFailureRebalance:
		return executeFailureRebalance(ctx, workspaceID, orch, clock)
	default:
		return fmt.Errorf("%w %q", ErrUnknownDashboardScenario, scenarioName)
	}
}

func executeWarmPoolBurst(ctx context.Context, workspaceID string, orch *orchestrator.Orchestrator, clock *fake.Clock) error {
	runIDs := []string{"run-burst-01", "run-burst-02", "run-burst-03", "run-burst-04"}
	for _, runID := range runIDs {
		workload := dashboardWorkload(workspaceID, runID, "RTX A6000", 40)
		workload.Spec.Placement.ExpectedRuntimeSeconds = 8 * 60
		workload.Spec.Execution.MaxRuntimeSeconds = 12 * 60
		if err := startDashboardRun(ctx, workspaceID, runID, workload, orch); err != nil {
			return err
		}
		clock.Advance(time.Second)
	}
	for index, runID := range runIDs {
		if err := finishDashboardRun(ctx, workspaceID, runID, orch); err != nil {
			return err
		}
		clock.Advance(2 * time.Second)
		if index+1 < len(runIDs) {
			if err := advanceDashboardRun(ctx, workspaceID, runIDs[index+1], orch, "dispatch"); err != nil {
				return err
			}
		}
	}
	return nil
}

func executeDeadlineCost(ctx context.Context, workspaceID string, orch *orchestrator.Orchestrator, clock *fake.Clock) error {
	long := dashboardWorkload(workspaceID, "run-batch-long", "RTX A6000", 40)
	long.Spec.Placement.ExpectedRuntimeSeconds = 20 * 60
	long.Spec.Execution.MaxRuntimeSeconds = 30 * 60
	if err := startDashboardRun(ctx, workspaceID, "run-batch-long", long, orch); err != nil {
		return err
	}
	clock.Advance(time.Second)
	urgent := dashboardWorkload(workspaceID, "run-deadline-urgent", "RTX A6000", 40)
	urgent.Spec.Placement.Objective = domain.ObjectiveBalanced
	urgent.Spec.Placement.MaxP90StartSeconds = 60
	urgent.Spec.Placement.ExpectedRuntimeSeconds = 4 * 60
	urgent.Spec.Execution.MaxRuntimeSeconds = 6 * 60
	if err := startDashboardRun(ctx, workspaceID, "run-deadline-urgent", urgent, orch); err != nil {
		return err
	}
	clock.Advance(time.Second)
	cheap := dashboardWorkload(workspaceID, "run-cost-flexible", "RTX A6000", 40)
	cheap.Spec.Placement.ExpectedRuntimeSeconds = 6 * 60
	cheap.Spec.Execution.MaxRuntimeSeconds = 10 * 60
	if err := startDashboardRun(ctx, workspaceID, "run-cost-flexible", cheap, orch); err != nil {
		return err
	}
	if err := finishDashboardRun(ctx, workspaceID, "run-deadline-urgent", orch); err != nil {
		return err
	}
	if err := finishDashboardRun(ctx, workspaceID, "run-batch-long", orch); err != nil {
		return err
	}
	if err := advanceDashboardRun(ctx, workspaceID, "run-cost-flexible", orch, "dispatch"); err != nil {
		return err
	}
	return finishDashboardRun(ctx, workspaceID, "run-cost-flexible", orch)
}

func executeFailureRebalance(ctx context.Context, workspaceID string, orch *orchestrator.Orchestrator, clock *fake.Clock) error {
	failed := dashboardWorkload(workspaceID, "run-provider-replacement", "RTX 4090", 16)
	failed.Spec.Placement.ExpectedRuntimeSeconds = 10 * 60
	failed.Spec.Execution.MaxRuntimeSeconds = 15 * 60
	if err := startDashboardRun(ctx, workspaceID, "run-provider-replacement", failed, orch); err != nil {
		return err
	}
	clock.Advance(time.Second)
	for _, runID := range []string{"run-rebalance-warm", "run-rebalance-queued"} {
		workload := dashboardWorkload(workspaceID, runID, "RTX A6000", 40)
		workload.Spec.Placement.ExpectedRuntimeSeconds = 7 * 60
		workload.Spec.Execution.MaxRuntimeSeconds = 10 * 60
		if err := startDashboardRun(ctx, workspaceID, runID, workload, orch); err != nil {
			return err
		}
		clock.Advance(time.Second)
	}
	if err := finishDashboardRun(ctx, workspaceID, "run-provider-replacement", orch); err != nil {
		return err
	}
	if err := finishDashboardRun(ctx, workspaceID, "run-rebalance-warm", orch); err != nil {
		return err
	}
	if err := advanceDashboardRun(ctx, workspaceID, "run-rebalance-queued", orch, "dispatch"); err != nil {
		return err
	}
	return finishDashboardRun(ctx, workspaceID, "run-rebalance-queued", orch)
}

func startDashboardRun(ctx context.Context, workspaceID, runID string, workload domain.WorkloadRevision, orch *orchestrator.Orchestrator) error {
	if _, err := orch.CreateRun(ctx, orchestrator.CreateRunRequest{
		WorkspaceID: workspaceID, RunID: runID, IdempotencyKey: "scenario:" + runID, Workload: workload,
	}); err != nil {
		return fmt.Errorf("create dashboard Run %s: %w", runID, err)
	}
	return advanceDashboardRun(ctx, workspaceID, runID, orch, "start")
}

func finishDashboardRun(ctx context.Context, workspaceID, runID string, orch *orchestrator.Orchestrator) error {
	return advanceDashboardRun(ctx, workspaceID, runID, orch, "finish")
}

func advanceDashboardRun(ctx context.Context, workspaceID, runID string, orch *orchestrator.Orchestrator, transition string) error {
	if err := orch.AdvanceRun(ctx, workspaceID, runID); err != nil {
		return fmt.Errorf("%s dashboard Run %s: %w", transition, runID, err)
	}
	return nil
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

func dashboardBaseline(workspaceID string, now time.Time, offers []domain.OfferSnapshot) []DashboardMessage {
	return []DashboardMessage{{
		Type: "offers_replaced",
		Catalog: &DashboardOfferCatalog{
			WorkspaceID: workspaceID,
			Revision:    "scenario-recorded-offers-v1",
			ObservedAt:  now,
			Offers:      offers,
			Failures:    []any{},
		},
	}, {Type: "ready", ThroughGlobalPosition: 0}}
}
