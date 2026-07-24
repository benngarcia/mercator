package conformance

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	dockeradapter "github.com/benngarcia/mercator/internal/adapter/docker"
	"github.com/benngarcia/mercator/internal/adapter/fake"
	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/conformanceprobe"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

func TestRunEvidenceSerializesBookingDecisionVocabulary(t *testing.T) {
	encoded, err := json.Marshal(RunEvidence{
		ID:              "run-1",
		BookingDecision: domain.BookingDecision{ID: "decision-1"},
	})
	if err != nil {
		t.Fatalf("marshal run evidence: %v", err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"booking_decision"`) || strings.Contains(text, `"placement"`) {
		t.Fatalf("run evidence uses superseded decision vocabulary: %s", text)
	}
}

func TestRunnerVerifiesARealReportedRunAndConfirmedCleanup(t *testing.T) {
	provider := &reportingProvider{EphemeralExecutor: fake.New(
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
	if evidence.Run.BookingDecision.SelectedOfferSnapshotID == "" || len(evidence.Run.Events) == 0 || evidence.Run.StartedAt.IsZero() {
		t.Fatalf("run evidence is incomplete: %+v", evidence.Run)
	}
}

func TestRunnerLaunchCancelProvesARealLaunchAndConfirmedCleanup(t *testing.T) {
	provider := fake.New(
		fake.WithOffers([]domain.OfferSnapshot{trialOffer(0.0001)}),
		fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
	)
	runner := testRunner(t, provider)
	trial := dockerTrial(10*time.Second, 0.50)
	trial.Mode = ModeLaunchCancel

	evidence, err := runner.Verify(context.Background(), trial)

	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if evidence.Verdict != VerdictPassed || evidence.Mode != ModeLaunchCancel {
		t.Fatalf("evidence = %+v, want passed launch-cancel", evidence)
	}
	if evidence.Run.Outcome != string(domain.RunOutcomeCancelled) || evidence.Run.Cleanup != string(domain.CleanupConfirmed) || !evidence.Run.Closed {
		t.Fatalf("run evidence = %+v", evidence.Run)
	}
	if evidence.Inventory.Owned != 0 {
		t.Fatalf("owned inventory = %d, want zero", evidence.Inventory.Owned)
	}
}

func TestRunnerRetriesCleanupAndRetainsTheScenarioFailureEvidence(t *testing.T) {
	provider := &transientReleaseProvider{
		EphemeralExecutor: fake.New(
			fake.WithOffers([]domain.OfferSnapshot{trialOffer(0.0001)}),
			fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
		),
		failuresRemaining: 3,
	}
	runner := testRunner(t, provider)
	trial := dockerTrial(10*time.Second, 0.50)
	trial.Mode = ModeLaunchCancel

	evidence, err := runner.Verify(context.Background(), trial)

	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if evidence.Verdict != VerdictFailed || evidence.Failure == nil {
		t.Fatalf("evidence = %+v, want retained scenario failure", evidence)
	}
	if evidence.CleanupFailure != nil || evidence.Inventory.Owned != 0 {
		t.Fatalf("cleanup evidence = %+v inventory = %+v", evidence.CleanupFailure, evidence.Inventory)
	}
	if evidence.Run.Cleanup != string(domain.CleanupConfirmed) || !evidence.Run.Closed || len(evidence.Run.Events) == 0 {
		t.Fatalf("partial run evidence was not completed after cleanup: %+v", evidence.Run)
	}
	if provider.releaseAttempts() < 4 {
		t.Fatalf("release attempts = %d, want at least four", provider.releaseAttempts())
	}
}

func TestRunnerRejectsUnreachableRemoteCallbackTopologyBeforeProviderContact(t *testing.T) {
	provider := &contactCountingProvider{EphemeralExecutor: fake.New()}
	factory := broker.NewFactory()
	factory.Register(dockeradapter.Manifest(), func(map[string]string, string) (capability.Backend, error) { return provider, nil })
	runner := newRunner(RunnerConfig{Environment: map[string]string{}}, withProviderFactory(factory), withTempRoot(t.TempDir()))
	trial := dockerTrial(time.Minute, 0.50)
	trial.Config = map[string]string{"host": "tcp://gpu.example:2376"}

	_, err := runner.Verify(context.Background(), trial)

	if err == nil || !strings.Contains(err.Error(), "MERCATOR_CONFORMANCE_LISTEN_ADDR") {
		t.Fatalf("Verify() error = %v, want explicit listener diagnostic", err)
	}
	if provider.contacts.Load() != 0 {
		t.Fatalf("provider contacts = %d, want zero", provider.contacts.Load())
	}
}

func TestRemoteCallbackTopologyRequiresAFixedListenerAndReachableOrigin(t *testing.T) {
	trial := dockerTrial(time.Minute, 0.50)
	trial.Config = map[string]string{"host": "tcp://gpu.example:2376"}
	tests := []struct {
		name   string
		config RunnerConfig
		want   string
	}{
		{name: "listener", config: RunnerConfig{}, want: "MERCATOR_CONFORMANCE_LISTEN_ADDR is required"},
		{name: "fixed port", config: RunnerConfig{ListenAddress: "0.0.0.0:0", PublicURL: "https://reports.example.com"}, want: "must use a fixed port"},
		{name: "public url", config: RunnerConfig{ListenAddress: "0.0.0.0:8082"}, want: "MERCATOR_CONFORMANCE_PUBLIC_URL is required"},
		{name: "origin", config: RunnerConfig{ListenAddress: "0.0.0.0:8082", PublicURL: "https://reports.example.com/callback"}, want: "must be an origin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTopology(trial, test.config)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateTopology() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestRunnerRejectsAnOfferWhoseTimeoutCostExceedsTheBudget(t *testing.T) {
	provider := &reportingProvider{EphemeralExecutor: fake.New(
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

func TestRunnerCancelsAndConfirmsCleanupWhenTheTrialEndsAfterLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &cancellingProvider{
		EphemeralExecutor: fake.New(
			fake.WithOffers([]domain.OfferSnapshot{trialOffer(0.0001)}),
			fake.WithLaunchOutcome(adapter.ExternalPhaseRunning),
		),
		cancel: cancel,
	}
	runner := testRunner(t, provider)

	evidence, err := runner.Verify(ctx, dockerTrial(10*time.Second, 0.50))

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

func testRunner(t *testing.T, provider capability.EphemeralExecutor) *Runner {
	t.Helper()
	factory := broker.NewFactory()
	factory.Register(dockeradapter.Manifest(), func(map[string]string, string) (capability.Backend, error) {
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
		RentalID:   "offer_fixture",
		Kind:       domain.OfferKindStanding,
		Lane:       domain.LaneReusable,
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
	capability.EphemeralExecutor
	launches atomic.Int64
}

type cancellingProvider struct {
	capability.EphemeralExecutor
	cancel context.CancelFunc
}

type transientReleaseProvider struct {
	capability.EphemeralExecutor
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
	return provider.EphemeralExecutor.Release(ctx, request)
}

func (provider *transientReleaseProvider) releaseAttempts() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.attempts
}

type contactCountingProvider struct {
	capability.EphemeralExecutor
	contacts atomic.Int64
}

func (provider *contactCountingProvider) Verify(ctx context.Context) error {
	provider.contacts.Add(1)
	return provider.EphemeralExecutor.Verify(ctx)
}

func (provider *contactCountingProvider) ListOffers(ctx context.Context, request adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	provider.contacts.Add(1)
	return provider.EphemeralExecutor.ListOffers(ctx, request)
}

func (provider *cancellingProvider) Launch(ctx context.Context, request adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	receipt, err := provider.EphemeralExecutor.Launch(ctx, request)
	provider.cancel()
	return receipt, err
}

func (provider *reportingProvider) Launch(ctx context.Context, request adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	receipt, err := provider.EphemeralExecutor.Launch(ctx, request)
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
