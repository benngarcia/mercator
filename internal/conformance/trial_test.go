package conformance

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/conformanceprobe"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

func TestRunnerVerifiesARealReportedRunAndConfirmedCleanup(t *testing.T) {
	provider := &reportingProvider{Provider: fake.New(
		fake.WithOffers([]domain.OfferSnapshot{trialOffer(0.0001)}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	)}
	runner := testRunner(t, provider)

	evidence, err := runner.Verify(context.Background(), dockerTrial(10*time.Second, 0.50))

	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if evidence.Verdict != VerdictPassed {
		t.Fatalf("verdict = %q, evidence = %+v", evidence.Verdict, evidence)
	}
	if evidence.Run.Outcome != string(domain.RunOutcomeSucceeded) || evidence.Run.ExitCode == nil || *evidence.Run.ExitCode != 0 {
		t.Fatalf("run evidence = %+v", evidence.Run)
	}
	if evidence.Run.Cleanup != string(domain.CleanupConfirmed) || !evidence.Run.Closed {
		t.Fatalf("run did not close with confirmed cleanup: %+v", evidence.Run)
	}
	if !contains(evidence.Run.EventTypes, orchestrator.EventRunReported) {
		t.Fatalf("events = %v, want %s", evidence.Run.EventTypes, orchestrator.EventRunReported)
	}
	if evidence.Inventory.Owned != 0 {
		t.Fatalf("owned inventory = %d, want zero", evidence.Inventory.Owned)
	}
}

func TestRunnerRejectsAnOfferWhoseTimeoutCostExceedsTheBudget(t *testing.T) {
	provider := &reportingProvider{Provider: fake.New(
		fake.WithOffers([]domain.OfferSnapshot{trialOffer(1)}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	)}
	runner := testRunner(t, provider)

	evidence, err := runner.Verify(context.Background(), dockerTrial(time.Minute, 0.50))

	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if evidence.Verdict != VerdictBlocked || evidence.Failure == nil || evidence.Failure.Code != "BUDGET_EXCEEDED" {
		t.Fatalf("evidence = %+v, want blocked budget verdict", evidence)
	}
	if provider.launches.Load() != 0 {
		t.Fatalf("provider launches = %d, want zero", provider.launches.Load())
	}
}

func TestRunnerCancelsAndConfirmsCleanupAfterTheTrialTimesOut(t *testing.T) {
	provider := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{trialOffer(0.0001)}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	)
	runner := testRunner(t, provider)

	evidence, err := runner.Verify(context.Background(), dockerTrial(50*time.Millisecond, 0.50))

	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if evidence.Verdict != VerdictFailed || evidence.Failure == nil {
		t.Fatalf("evidence = %+v, want failed timeout verdict", evidence)
	}
	if evidence.Run.Cleanup != string(domain.CleanupConfirmed) || !evidence.Run.Closed {
		t.Fatalf("run did not close with confirmed cleanup: %+v", evidence.Run)
	}
	if evidence.Inventory.Owned != 0 {
		t.Fatalf("owned inventory = %d, want zero", evidence.Inventory.Owned)
	}
}

func testRunner(t *testing.T, provider adapter.Provider) *Runner {
	t.Helper()
	factory := broker.NewFactory()
	factory.Register(dockeradapter.Manifest(), func(map[string]string, string) (adapter.Provider, error) {
		return provider, nil
	})
	return newRunner(RunnerConfig{Environment: map[string]string{}}, withProviderFactory(factory), withTempRoot(t.TempDir()))
}

func dockerTrial(timeout time.Duration, budget float64) Trial {
	return Trial{
		AdapterType:        "docker",
		Image:              "ghcr.io/benngarcia/mercator-conformance-probe@sha256:" + strings.Repeat("0", 64),
		MaxExpectedCostUSD: budget,
		Timeout:            timeout,
	}
}

func trialOffer(rate float64) domain.OfferSnapshot {
	now := time.Now().UTC()
	return domain.OfferSnapshot{
		ID:         "offer_fixture",
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
		Pricing:    domain.PriceModel{Currency: "USD", RatePerSecondUSD: rate, Known: true},
		Queue:      &domain.QueueSnapshot{},
		Capacity:   domain.CapacityEvidence{Available: true, Confidence: 1},
		ImageCache: domain.ImageCacheEvidence{Known: true, ManifestCached: true},
	}
}

type reportingProvider struct {
	adapter.Provider
	launches atomic.Int64
}

func (provider *reportingProvider) Launch(ctx context.Context, request adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	receipt, err := provider.Provider.Launch(ctx, request)
	if err != nil {
		return receipt, err
	}
	provider.launches.Add(1)
	environment := make(map[string]string, len(request.Environment))
	for _, binding := range request.Environment {
		if binding.Value != nil {
			environment[binding.Name] = *binding.Value
		}
	}
	go conformanceprobe.Run(context.Background(), []string{"success"}, environment, io.Discard, io.Discard)
	return receipt, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
