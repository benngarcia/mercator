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
	trial = normalizeTrial(trial)
	started := time.Now().UTC()
	evidence = Evidence{AdapterType: trial.AdapterType, Mode: trial.Mode, StartedAt: started}
	defer func() {
		evidence.Duration = time.Since(started)
		evidence.DurationSecs = evidence.Duration.Seconds()
	}()
	if err := ValidateTrial(trial, runner.lookupEnv); err != nil {
		return evidence, err
	}
	if err := validateTopology(trial, runner.config); err != nil {
		return evidence, err
	}

	trialCtx, cancel := context.WithTimeout(ctx, trial.Timeout)
	defer cancel()
	identity, err := newTrialIdentity(trial.AdapterType)
	if err != nil {
		return evidence, err
	}
	evidence.TrialID = identity.trialID
	evidence.ConnectionID = identity.connectionID

	root, err := os.MkdirTemp(runner.tempRoot, "mercator-conformance-")
	if err != nil {
		return evidence, fmt.Errorf("create private trial directory: %w", err)
	}
	defer os.RemoveAll(root)
	listener, err := net.Listen("tcp", runner.listenAddress())
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
	identity.workspaceID, err = client.createWorkspace(trialCtx, "Conformance "+identity.trialID)
	if err != nil {
		return evidence, err
	}
	evidence.WorkspaceID = identity.workspaceID
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

	runStarted := time.Now().UTC()
	runID, err := randomID("run_conformance")
	if err != nil {
		return evidence, err
	}
	evidence.Run.ID = runID
	run, err := client.createRun(trialCtx, identity.workspaceID, runID, trial, offer)
	if err != nil {
		evidence.Verdict = VerdictFailed
		evidence.Failure = &TrialFailure{Code: "RUN_CREATE_FAILED", Message: err.Error()}
		return runner.finish(runtime, client, evidence, runStarted, trial.Timeout)
	}
	evidence.Run.ID = run.Run.ID
	if trial.Mode == ModeLaunchCancel {
		if cancelErr := client.cancelRun(trialCtx, identity.workspaceID, run.Run.ID); cancelErr != nil {
			evidence.Verdict = VerdictFailed
			evidence.Failure = &TrialFailure{Code: "RUN_CANCEL_FAILED", Message: cancelErr.Error()}
			return runner.finish(runtime, client, evidence, runStarted, trial.Timeout)
		}
	}
	run, waitErr := client.waitClosed(trialCtx, identity.workspaceID, run.Run.ID)
	if waitErr == nil {
		evidence.Run, waitErr = client.captureRunEvidence(trialCtx, identity.workspaceID, run, runStarted)
	}
	if waitErr != nil {
		evidence.Verdict = VerdictFailed
		evidence.Failure = &TrialFailure{Code: "RUN_DID_NOT_COMPLETE", Message: waitErr.Error()}
		return runner.finish(runtime, client, evidence, runStarted, trial.Timeout)
	}
	if failure := successfulRunFailure(trial.Mode, evidence.Run); failure != nil {
		evidence.Verdict = VerdictFailed
		evidence.Failure = failure
		return runner.finish(runtime, client, evidence, runStarted, trial.Timeout)
	}
	evidence.Verdict = VerdictPassed
	return runner.finish(runtime, client, evidence, runStarted, trial.Timeout)
}

func (runner *Runner) finish(runtime *daemon.Runtime, client trialClient, evidence Evidence, runStarted time.Time, timeout time.Duration) (Evidence, error) {
	cleanupTimeout := timeout
	if cleanupTimeout < 30*time.Second {
		cleanupTimeout = 30 * time.Second
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		cleanupComplete, attemptErr := reconcileCleanup(cleanupCtx, runtime, client, &evidence, runStarted)
		lastErr = attemptErr
		if cleanupComplete {
			return evidence, nil
		}
		select {
		case <-cleanupCtx.Done():
			if evidence.Inventory.Owned != 0 {
				lastErr = errors.Join(lastErr, fmt.Errorf("provider still lists %d owned objects", evidence.Inventory.Owned))
			}
			evidence.Verdict = VerdictFailed
			evidence.CleanupFailure = &TrialFailure{Code: "CLEANUP_FAILED", Message: errors.Join(lastErr, cleanupCtx.Err()).Error()}
			return evidence, nil
		case <-ticker.C:
		}
	}
}

func reconcileCleanup(ctx context.Context, runtime *daemon.Runtime, client trialClient, evidence *Evidence, runStarted time.Time) (bool, error) {
	var cancelErr error
	if evidence.Run.ID != "" && !evidence.Run.Closed {
		cancelErr = client.cancelRun(ctx, evidence.WorkspaceID, evidence.Run.ID)
	}
	reconciled, reconcileErr := runtime.ReconcileWorkspace(ctx, evidence.WorkspaceID)
	evidence.Inventory.Owned = len(reconciled.Owned)
	var evidenceErr error
	if evidence.Run.ID != "" {
		if run, getErr := client.getRun(ctx, evidence.WorkspaceID, evidence.Run.ID); getErr == nil {
			evidence.Run, evidenceErr = client.captureRunEvidence(ctx, evidence.WorkspaceID, run, runStarted)
		} else {
			evidenceErr = getErr
		}
	}
	attemptErr := errors.Join(cancelErr, reconcileErr, evidenceErr)
	runClosed := evidence.Run.ID == "" || evidence.Run.Closed
	return attemptErr == nil && runClosed && len(reconciled.Owned) == 0, attemptErr
}

func successfulRunFailure(mode Mode, run RunEvidence) *TrialFailure {
	if mode == ModeLaunchCancel {
		if run.Outcome != string(domain.RunOutcomeCancelled) || run.Cleanup != string(domain.CleanupConfirmed) || !run.Closed {
			return &TrialFailure{Code: "CANCEL_SCENARIO_FAILED", Message: "launch-cancel Run did not close cancelled with confirmed cleanup"}
		}
		return nil
	}
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
	if publicURL := strings.TrimSpace(runner.config.PublicURL); publicURL != "" {
		return strings.TrimRight(publicURL, "/")
	}
	_, port, err := net.SplitHostPort(address.String())
	if err != nil || adapterType != "docker" {
		return ""
	}
	return "http://host.docker.internal:" + port
}

func (runner *Runner) listenAddress() string {
	if address := strings.TrimSpace(runner.config.ListenAddress); address != "" {
		return address
	}
	return "127.0.0.1:0"
}
