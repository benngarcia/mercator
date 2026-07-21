package conformance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/domain"
)

func TestTrialProvesSuccessfulRunAndZeroOwnedResourcesThroughRealHTTP(t *testing.T) {
	provider := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{conformanceOffer()}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseSucceeded),
	)
	factory := broker.NewFactory()
	factory.Register(dockeradapter.Manifest(), func(map[string]string, string) (adapter.Provider, error) {
		return provider, nil
	})

	report, err := Run(context.Background(), TrialSpec{
		AdapterType:        "docker",
		Config:             map[string]string{},
		Image:              "ghcr.io/benngarcia/mercator-conformance@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Mode:               ProbeMode,
		MaxExpectedCostUSD: 0.50,
		Timeout:            2 * time.Second,
	}, withProviderFactory(factory), withTempRoot(t.TempDir()))

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Verdict != VerdictPassed {
		t.Fatalf("verdict = %q, report = %+v", report.Verdict, report)
	}
	if report.WorkspaceID == "" || report.ConnectionID == "" {
		t.Fatalf("report has no stable trial identities: %+v", report)
	}
	if len(report.Scenarios) != 1 {
		t.Fatalf("scenarios = %+v, want one", report.Scenarios)
	}
	scenario := report.Scenarios[0]
	if scenario.Name != "success" || scenario.Run.Outcome != "succeeded" || scenario.Run.ExitCode == nil || *scenario.Run.ExitCode != 0 {
		t.Fatalf("scenario = %+v", scenario)
	}
	if scenario.Run.Cleanup != "confirmed" || !scenario.Run.Closed {
		t.Fatalf("scenario did not close cleanly: %+v", scenario)
	}
	if scenario.Placement.SelectedOfferSnapshotID == "" || len(scenario.Placement.Candidates) != 1 || scenario.Placement.Candidates[0].NativeRef != "fixture-capacity" || len(scenario.Events) == 0 || scenario.StartedAt.IsZero() {
		t.Fatalf("scenario evidence is incomplete: %+v", scenario)
	}
	if report.Inventory.Owned != 0 {
		t.Fatalf("owned inventory = %d, want zero", report.Inventory.Owned)
	}
}

func TestTrialLaunchCancelCancelsAndCleansUpThroughRealHTTP(t *testing.T) {
	provider := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{conformanceOffer()}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	)
	factory := broker.NewFactory()
	factory.Register(dockeradapter.Manifest(), func(map[string]string, string) (adapter.Provider, error) {
		return provider, nil
	})

	report, err := Run(context.Background(), TrialSpec{
		AdapterType:        "docker",
		Config:             map[string]string{},
		Image:              "ghcr.io/benngarcia/mercator-conformance@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Mode:               LaunchCancelMode,
		MaxExpectedCostUSD: 0.50,
		Timeout:            200 * time.Millisecond,
	}, withProviderFactory(factory), withTempRoot(t.TempDir()))

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Verdict != VerdictPassed {
		t.Fatalf("verdict = %q, report = %+v", report.Verdict, report)
	}
	if len(report.Scenarios) != 1 {
		t.Fatalf("scenarios = %+v, want one", report.Scenarios)
	}
	scenario := report.Scenarios[0]
	if scenario.Name != "launch-cancel" || scenario.Run.Outcome != "cancelled" {
		t.Fatalf("scenario = %+v", scenario)
	}
	if scenario.Run.Cleanup != "confirmed" || !scenario.Run.Closed {
		t.Fatalf("scenario did not close cleanly: %+v", scenario)
	}
	if report.Inventory.Owned != 0 {
		t.Fatalf("owned inventory = %d, want zero", report.Inventory.Owned)
	}
}

func TestTrialTimeoutCancelsRunAndRetainsBlockedEvidence(t *testing.T) {
	provider := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{conformanceOffer()}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	)
	factory := broker.NewFactory()
	factory.Register(dockeradapter.Manifest(), func(map[string]string, string) (adapter.Provider, error) {
		return provider, nil
	})

	report, err := Run(context.Background(), TrialSpec{
		AdapterType:        "docker",
		Config:             map[string]string{},
		Image:              "ghcr.io/benngarcia/mercator-conformance@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Mode:               ProbeMode,
		MaxExpectedCostUSD: 0.50,
		Timeout:            100 * time.Millisecond,
	}, withProviderFactory(factory), withTempRoot(t.TempDir()))

	if err != nil {
		t.Fatalf("Run() error = %v, want a structured blocked report", err)
	}
	if report.Verdict != VerdictBlocked || report.Failure == nil || report.Failure.Code != "SCENARIO_TIMEOUT" {
		t.Fatalf("report = %+v, want blocked scenario timeout", report)
	}
	owned, err := provider.ListOwned(context.Background(), adapter.OwnershipQuery{WorkspaceID: report.WorkspaceID})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 0 || report.Inventory.Owned != 0 {
		t.Fatalf("cleanup left provider objects = %+v report inventory = %+v", owned, report.Inventory)
	}
}

func TestTrialCleanupRetriesTransientProviderFailuresUntilInventoryIsEmpty(t *testing.T) {
	provider := &transientReleaseProvider{
		Provider: fake.New(
			fake.WithOffers([]domain.OfferSnapshot{conformanceOffer()}),
			fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
		),
		failuresRemaining: 3,
	}
	factory := broker.NewFactory()
	factory.Register(dockeradapter.Manifest(), func(map[string]string, string) (adapter.Provider, error) {
		return provider, nil
	})

	report, err := Run(context.Background(), TrialSpec{
		AdapterType:        "docker",
		Config:             map[string]string{},
		Image:              "ghcr.io/benngarcia/mercator-conformance@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Mode:               LaunchCancelMode,
		MaxExpectedCostUSD: 0.50,
		Timeout:            time.Second,
	}, withProviderFactory(factory), withTempRoot(t.TempDir()))

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Inventory.Owned != 0 {
		t.Fatalf("cleanup left provider inventory: %+v", report.Inventory)
	}
	if report.Verdict != VerdictBlocked || report.Failure == nil || report.Failure.Code != "SCENARIO_BLOCKED" {
		t.Fatalf("report = %+v, want the scenario failure retained after successful cleanup", report)
	}
	if provider.releaseAttempts() < 4 {
		t.Fatalf("release attempts = %d, want cleanup retries after transient failures", provider.releaseAttempts())
	}
}

type transientReleaseProvider struct {
	adapter.Provider
	mu                sync.Mutex
	failuresRemaining int
	attempts          int
}

func (provider *transientReleaseProvider) Release(ctx context.Context, request adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	provider.mu.Lock()
	provider.attempts++
	if provider.failuresRemaining > 0 {
		provider.failuresRemaining--
		provider.mu.Unlock()
		return adapter.ReleaseReceipt{}, adapter.ErrRetryableFailure
	}
	provider.mu.Unlock()
	return provider.Provider.Release(ctx, request)
}

func (provider *transientReleaseProvider) releaseAttempts() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.attempts
}

func conformanceOffer() domain.OfferSnapshot {
	now := time.Now().UTC()
	return domain.OfferSnapshot{
		ID:         "off_fixture",
		Kind:       domain.OfferKindStanding,
		NativeRef:  "fixture-capacity",
		ObservedAt: now.Add(-time.Second),
		ExpiresAt:  now.Add(time.Minute),
		Platform:   domain.Platform{OS: "linux", Architecture: "amd64"},
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
		},
		Pricing:    domain.PriceModel{Currency: "USD", RatePerSecondUSD: 0.0001, Known: true},
		Queue:      &domain.QueueSnapshot{},
		Capacity:   domain.CapacityEvidence{Available: true, Confidence: 1},
		ImageCache: domain.ImageCacheEvidence{Known: true, ManifestCached: true},
	}
}
