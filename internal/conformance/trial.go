package conformance

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/orchestrator"
)

type RunnerConfig struct {
	Environment   map[string]string
	ListenAddress string
	PublicURL     string
}

type Runner struct {
	config          RunnerConfig
	providerFactory *broker.Factory
	tempRoot        string
}

type runnerOption func(*Runner)

func withProviderFactory(factory *broker.Factory) runnerOption {
	return func(runner *Runner) { runner.providerFactory = factory }
}

func withTempRoot(root string) runnerOption {
	return func(runner *Runner) { runner.tempRoot = root }
}

func NewRunner(config RunnerConfig) *Runner {
	return newRunner(config)
}

func newRunner(config RunnerConfig, options ...runnerOption) *Runner {
	if config.ListenAddress == "" {
		config.ListenAddress = "127.0.0.1:0"
	}
	config.Environment = cloneEnvironment(config.Environment)
	runner := &Runner{config: config}
	for _, option := range options {
		option(runner)
	}
	return runner
}

// Verify launches one probe Run through Mercator's authenticated HTTP
// lifecycle and returns only after provider inventory proves cleanup.
func (runner *Runner) Verify(ctx context.Context, trial Trial) (evidence Evidence, err error) {
	started := time.Now().UTC()
	evidence = Evidence{AdapterType: trial.AdapterType, StartedAt: started}
	defer func() {
		evidence.Duration = time.Since(started)
		evidence.DurationSecs = evidence.Duration.Seconds()
	}()
	if err := ValidateTrial(trial, runner.lookupEnv); err != nil {
		return evidence, err
	}
	if trial.AdapterType != "docker" && runner.config.PublicURL == "" && runner.providerFactory == nil {
		return evidence, errors.New("conformance: MERCATOR_CONFORMANCE_PUBLIC_URL is required for cloud probe reports")
	}

	trialCtx, cancel := context.WithTimeout(ctx, trial.Timeout)
	defer cancel()
	identity, err := newTrialIdentity(trial.AdapterType)
	if err != nil {
		return evidence, err
	}
	evidence.TrialID = identity.trialID
	evidence.WorkspaceID = identity.workspaceID
	evidence.ConnectionID = identity.connectionID

	root, err := os.MkdirTemp(runner.tempRoot, "mercator-conformance-")
	if err != nil {
		return evidence, fmt.Errorf("create private trial directory: %w", err)
	}
	defer os.RemoveAll(root)
	listener, err := net.Listen("tcp", runner.config.ListenAddress)
	if err != nil {
		return evidence, fmt.Errorf("bind trial listener: %w", err)
	}
	operatorToken, masterKey, err := trialSecrets()
	if err != nil {
		_ = listener.Close()
		return evidence, err
	}
	publicURL := runner.publicURL(listener.Addr(), trial.AdapterType)
	runtimeCtx, stopRuntime := context.WithCancel(context.Background())
	runtime, err := daemon.New(runtimeCtx, daemon.Config{
		SQLiteDSN:       "file:" + filepath.Join(root, "mercator.db"),
		OperatorToken:   operatorToken,
		MasterKey:       masterKey,
		PublicURL:       publicURL,
		Getenv:          runner.getenv,
		ProviderFactory: runner.providerFactory,
	})
	if err != nil {
		stopRuntime()
		_ = listener.Close()
		return evidence, err
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.Serve(listener) }()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		shutdownErr := runtime.Shutdown(shutdownCtx)
		stopRuntime()
		serveResult := <-serveErr
		if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) {
			shutdownErr = errors.Join(shutdownErr, serveResult)
		}
		if err == nil && shutdownErr != nil {
			err = shutdownErr
		}
	}()

	client := trialClient{
		baseURL: "http://" + listener.Addr().String(),
		token:   operatorToken,
		client:  &http.Client{Timeout: 40 * time.Second},
	}
	if err := client.ready(trialCtx); err != nil {
		return evidence, err
	}
	if err := client.createAndAuthorizeConnection(trialCtx, identity, trial); err != nil {
		return evidence, err
	}
	offer, failure, err := client.affordableOffer(trialCtx, identity.workspaceID, trial)
	if err != nil {
		return evidence, err
	}
	if failure != nil {
		evidence.Verdict = VerdictBlocked
		evidence.Failure = failure
		return evidence, nil
	}
	evidence.Offer = offerEvidence(offer, trial.Timeout)

	runID, err := randomID("run_conformance")
	if err != nil {
		return evidence, err
	}
	evidence.Run.ID = runID
	run, err := client.createRun(trialCtx, identity.workspaceID, runID, trial, offer)
	if err != nil {
		evidence.Verdict = VerdictFailed
		evidence.Failure = &TrialFailure{Code: "RUN_CREATE_FAILED", Message: err.Error()}
		return runner.finish(runtime, client, evidence, trial.Timeout)
	}
	evidence.Run.ID = run.Run.ID
	run, waitErr := client.waitClosed(trialCtx, identity.workspaceID, run.Run.ID)
	if waitErr == nil {
		evidence.Run = runEvidence(run.Run)
		evidence.Run.EventTypes, waitErr = client.eventTypes(trialCtx, identity.workspaceID, run.Run.ID)
	}
	if waitErr != nil {
		evidence.Verdict = VerdictFailed
		evidence.Failure = &TrialFailure{Code: "RUN_DID_NOT_COMPLETE", Message: waitErr.Error()}
		return runner.finish(runtime, client, evidence, trial.Timeout)
	}
	if failure := successfulRunFailure(evidence.Run); failure != nil {
		evidence.Verdict = VerdictFailed
		evidence.Failure = failure
		return runner.finish(runtime, client, evidence, trial.Timeout)
	}
	evidence.Verdict = VerdictPassed
	return runner.finish(runtime, client, evidence, trial.Timeout)
}

func (runner *Runner) finish(runtime *daemon.Runtime, client trialClient, evidence Evidence, timeout time.Duration) (Evidence, error) {
	cleanupTimeout := timeout
	if cleanupTimeout < 30*time.Second {
		cleanupTimeout = 30 * time.Second
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if evidence.Run.ID != "" && !evidence.Run.Closed {
		_ = client.cancelRun(cleanupCtx, evidence.WorkspaceID, evidence.Run.ID)
		if run, err := client.waitClosed(cleanupCtx, evidence.WorkspaceID, evidence.Run.ID); err == nil {
			evidence.Run = runEvidence(run.Run)
			evidence.Run.EventTypes, _ = client.eventTypes(cleanupCtx, evidence.WorkspaceID, evidence.Run.ID)
		}
	}
	owned, inventoryErr := runtime.ListOwned(cleanupCtx, evidence.WorkspaceID)
	evidence.Inventory.Owned = len(owned)
	if inventoryErr != nil {
		evidence.Verdict = VerdictFailed
		evidence.Failure = &TrialFailure{Code: "INVENTORY_UNAVAILABLE", Message: inventoryErr.Error()}
		return evidence, nil
	}
	if len(owned) != 0 {
		evidence.Verdict = VerdictFailed
		evidence.Failure = &TrialFailure{Code: "OWNED_RESOURCES_REMAIN", Message: fmt.Sprintf("provider still lists %d owned objects", len(owned))}
	}
	return evidence, nil
}

func successfulRunFailure(run RunEvidence) *TrialFailure {
	if !containsEvent(run.EventTypes, orchestrator.EventRunReported) {
		return &TrialFailure{Code: "PROBE_REPORT_MISSING", Message: "probe Run closed without a signed workload report"}
	}
	if run.Outcome != string(domain.RunOutcomeSucceeded) || run.ExitCode == nil || *run.ExitCode != 0 {
		return &TrialFailure{Code: "PROBE_FAILED", Message: "probe Run did not finish with exit code zero"}
	}
	if run.Cleanup != string(domain.CleanupConfirmed) || !run.Closed {
		return &TrialFailure{Code: "CLEANUP_UNCONFIRMED", Message: "probe Run did not close with confirmed cleanup"}
	}
	return nil
}

func containsEvent(events []string, target string) bool {
	for _, event := range events {
		if event == target {
			return true
		}
	}
	return false
}

func (runner *Runner) lookupEnv(name string) (string, bool) {
	value, found := runner.config.Environment[name]
	return value, found
}

func (runner *Runner) getenv(name string) string { return runner.config.Environment[name] }

func (runner *Runner) publicURL(address net.Addr, adapterType string) string {
	if runner.providerFactory != nil {
		return "http://" + address.String()
	}
	if runner.config.PublicURL != "" {
		return strings.TrimRight(runner.config.PublicURL, "/")
	}
	_, port, err := net.SplitHostPort(address.String())
	if err != nil || adapterType != "docker" {
		return ""
	}
	return "http://host.docker.internal:" + port
}
