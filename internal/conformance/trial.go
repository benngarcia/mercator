package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/broker"
	"github.com/benngarcia/mercator/internal/credential"
	"github.com/benngarcia/mercator/internal/daemon"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/httpapi"
)

type trialOptions struct {
	providerFactory *broker.Factory
	tempRoot        string
	environment     map[string]string
}

type Option func(*trialOptions)

func withProviderFactory(factory *broker.Factory) Option {
	return func(options *trialOptions) { options.providerFactory = factory }
}

func withTempRoot(root string) Option {
	return func(options *trialOptions) { options.tempRoot = root }
}

func WithEnvironment(environment map[string]string) Option {
	return func(options *trialOptions) {
		options.environment = make(map[string]string, len(environment))
		for name, value := range environment {
			options.environment[name] = value
		}
	}
}

// Run executes one isolated Connection through Mercator's authenticated HTTP
// lifecycle, then independently proves that its provider inventory is empty.
func Run(ctx context.Context, spec TrialSpec, options ...Option) (report TrialReport, err error) {
	started := time.Now().UTC()
	report = TrialReport{AdapterType: spec.AdapterType, Mode: spec.Mode, StartedAt: started}
	settings := trialOptions{}
	for _, option := range options {
		option(&settings)
	}
	lookup := os.LookupEnv
	getenv := os.Getenv
	if settings.environment != nil {
		lookup = func(name string) (string, bool) {
			value, found := settings.environment[name]
			return value, found
		}
		getenv = func(name string) string { return settings.environment[name] }
	}
	if err := ValidateSpec(spec, lookup); err != nil {
		return report, err
	}
	trialCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	trialID, err := randomID("trial")
	if err != nil {
		return report, err
	}
	report.TrialID = trialID
	report.ConnectionID = "conn_" + spec.AdapterType + "_" + strings.TrimPrefix(trialID, "trial_")

	root, err := os.MkdirTemp(settings.tempRoot, "mercator-conformance-")
	if err != nil {
		return report, fmt.Errorf("create private trial directory: %w", err)
	}
	defer os.RemoveAll(root)
	listenAddress := spec.ListenAddress
	if listenAddress == "" {
		listenAddress = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return report, fmt.Errorf("bind trial listener: %w", err)
	}
	operatorToken, err := randomSecret()
	if err != nil {
		_ = listener.Close()
		return report, err
	}
	masterKey := make([]byte, 32)
	if _, err := rand.Read(masterKey); err != nil {
		_ = listener.Close()
		return report, fmt.Errorf("generate trial master key: %w", err)
	}
	publicURL := strings.TrimRight(spec.PublicURL, "/")
	if publicURL == "" {
		publicURL = localPublicURL(listener.Addr())
	}
	runtime, err := daemon.New(trialCtx, daemon.Config{
		SQLiteDSN:       "file:" + filepath.Join(root, "mercator.db"),
		OperatorToken:   operatorToken,
		MasterKey:       masterKey,
		PublicURL:       publicURL,
		Getenv:          getenv,
		ProviderFactory: settings.providerFactory,
	})
	if err != nil {
		_ = listener.Close()
		return report, err
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- runtime.Serve(listener) }()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		shutdownErr := runtime.Shutdown(shutdownCtx)
		serveResult := <-serveErr
		if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) {
			shutdownErr = errors.Join(shutdownErr, serveResult)
		}
		if err == nil && shutdownErr != nil {
			err = shutdownErr
		}
		report.DurationMS = time.Since(started).Milliseconds()
	}()

	client := trialClient{baseURL: "http://" + listener.Addr().String(), token: operatorToken, client: http.DefaultClient}
	if err := client.ready(trialCtx); err != nil {
		return report, err
	}
	workspaceID, err := client.createWorkspace(trialCtx, trialID)
	if err != nil {
		return report, err
	}
	report.WorkspaceID = workspaceID
	execution := scenarioExecution{}
	defer func() {
		finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer finalizeCancel()
		finalizeTrial(finalizeCtx, client, runtime, &report, execution)
	}()
	if err := client.createAndAuthorizeConnection(trialCtx, report.WorkspaceID, report.ConnectionID, spec); err != nil {
		return report, err
	}
	scenario, startedExecution, scenarioErr := client.runScenario(trialCtx, report.WorkspaceID, spec)
	execution = startedExecution
	if scenarioErr != nil {
		report.Verdict = VerdictBlocked
		report.Failure = classifyScenarioFailure(scenarioErr)
		return report, nil
	}
	report.Scenarios = []ScenarioEvidence{scenario}
	if !scenarioPassed(spec.Mode, scenario.Run) {
		report.Verdict = VerdictFailed
		report.Failure = &TrialFailure{Code: "SCENARIO_FAILED", Message: fmt.Sprintf("%s scenario did not reach its expected terminal state with confirmed cleanup", spec.Mode)}
		return report, nil
	}
	report.Verdict = VerdictPassed
	return report, nil
}

type trialClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func (client trialClient) ready(ctx context.Context) error {
	deadline := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	for {
		var response map[string]string
		err := client.do(ctx, http.MethodGet, "/health/ready", "", nil, &response)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
		}
	}
}

func (client trialClient) createWorkspace(ctx context.Context, trialID string) (string, error) {
	request := httpapi.CreateWorkspaceRequest{DisplayName: "Conformance " + trialID}
	var response httpapi.WorkspaceResponse
	if err := client.do(ctx, http.MethodPost, "/v1/workspaces", "", request, &response); err != nil {
		return "", fmt.Errorf("create trial workspace: %w", err)
	}
	return response.Workspace.ID, nil
}

func (client trialClient) createAndAuthorizeConnection(ctx context.Context, workspaceID, connectionID string, spec TrialSpec) error {
	request := httpapi.CreateConnectionRequest{
		WorkspaceId:  workspaceID,
		ConnectionId: connectionID,
		AdapterType:  spec.AdapterType,
		Config:       spec.Config,
	}
	if spec.CredentialEnv != "" {
		request.Credential = credential.Credential{Source: credential.SourceEnv, Ref: spec.CredentialEnv}
	}
	if err := client.do(ctx, http.MethodPost, "/v1/connections", "connection:create:"+connectionID, request, &httpapi.ConnectionResponse{}); err != nil {
		return fmt.Errorf("create trial connection: %w", err)
	}
	path := "/v1/connections/" + url.PathEscape(connectionID) + "/authorize?workspace_id=" + url.QueryEscape(workspaceID)
	if err := client.do(ctx, http.MethodPost, path, "", nil, &httpapi.ConnectionResponse{}); err != nil {
		return fmt.Errorf("authorize trial connection: %w", err)
	}
	return nil
}

type scenarioPlan struct {
	name      string
	runPrefix string
	arguments []string
	cancel    bool
}

type scenarioExecution struct {
	name      string
	startedAt time.Time
	runID     string
	closed    bool
}

func planForMode(mode Mode) scenarioPlan {
	if mode == LaunchCancelMode {
		return scenarioPlan{name: "launch-cancel", runPrefix: "run_cancel", arguments: []string{"wait-for-cancel"}, cancel: true}
	}
	return scenarioPlan{name: "success", runPrefix: "run_success", arguments: []string{"success"}}
}

func (client trialClient) runScenario(ctx context.Context, workspaceID string, spec TrialSpec) (ScenarioEvidence, scenarioExecution, error) {
	started := time.Now().UTC()
	plan := planForMode(spec.Mode)
	execution := scenarioExecution{name: plan.name, startedAt: started}
	var offers httpapi.OfferListResponse
	if err := client.do(ctx, http.MethodGet, "/v1/offers?workspace_id="+url.QueryEscape(workspaceID), "", nil, &offers); err != nil {
		return ScenarioEvidence{}, execution, fmt.Errorf("list trial offers: %w", err)
	}
	if len(offers.Offers) == 0 {
		return ScenarioEvidence{}, execution, errors.New("provider returned no placeable offers")
	}
	runID, err := randomID(plan.runPrefix)
	if err != nil {
		return ScenarioEvidence{}, execution, err
	}
	execution.runID = runID
	request := httpapi.CreateRunRequest{
		WorkspaceId: workspaceID,
		RunId:       runID,
		Workload:    scenarioWorkload(workspaceID, spec, offers.Offers[0].Platform, plan),
	}
	var run httpapi.RunResponse
	if err := client.do(ctx, http.MethodPost, "/v1/runs", "run:create:"+runID, request, &run); err != nil {
		return ScenarioEvidence{}, execution, fmt.Errorf("create %s run: %w", plan.name, err)
	}
	if plan.cancel {
		path := "/v1/runs/" + url.PathEscape(runID) + "/cancel?workspace_id=" + url.QueryEscape(workspaceID)
		if err := client.do(ctx, http.MethodPost, path, "", nil, &run); err != nil {
			return ScenarioEvidence{}, execution, fmt.Errorf("cancel %s run: %w", plan.name, err)
		}
	}
	if err := client.waitForClosed(ctx, workspaceID, runID, &run); err != nil {
		return ScenarioEvidence{}, execution, fmt.Errorf("wait for %s run: %w", plan.name, err)
	}
	execution.closed = true
	evidence, err := client.captureEvidence(ctx, workspaceID, plan.name, started, run)
	return evidence, execution, err
}

func finalizeTrial(ctx context.Context, client trialClient, runtime *daemon.Runtime, report *TrialReport, execution scenarioExecution) {
	reconciled, cleanupErr := reconcileUntilClean(ctx, client, runtime, report.WorkspaceID, execution)
	report.Inventory.Owned = len(reconciled.Owned)
	if len(report.Scenarios) == 0 && execution.runID != "" {
		if run, getErr := client.getRun(ctx, report.WorkspaceID, execution.runID); getErr == nil {
			if evidence, evidenceErr := client.captureEvidence(ctx, report.WorkspaceID, execution.name, execution.startedAt, run); evidenceErr == nil {
				report.Scenarios = []ScenarioEvidence{evidence}
			}
		}
	}
	if cleanupErr != nil {
		report.Verdict = VerdictBlocked
		report.Failure = &TrialFailure{Code: "CLEANUP_FAILED", Message: cleanupErr.Error()}
	}
}

func reconcileUntilClean(ctx context.Context, client trialClient, runtime *daemon.Runtime, workspaceID string, execution scenarioExecution) (daemon.ReconcileResult, error) {
	const retryInterval = 100 * time.Millisecond
	var lastResult daemon.ReconcileResult
	var lastErr error
	for {
		var cancelErr error
		if execution.runID != "" && !execution.closed {
			cancelErr = client.cancelRun(ctx, workspaceID, execution.runID)
		}
		result, reconcileErr := runtime.ReconcileWorkspace(ctx, workspaceID)
		lastResult = result
		lastErr = errors.Join(cancelErr, reconcileErr)
		allOpenRunsClosed := result.Advanced.Open == result.Advanced.Closed
		if lastErr == nil && allOpenRunsClosed && len(result.Owned) == 0 {
			return result, nil
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if len(lastResult.Owned) != 0 {
				lastErr = errors.Join(lastErr, fmt.Errorf("provider still lists %d owned objects", len(lastResult.Owned)))
			}
			return lastResult, errors.Join(lastErr, ctx.Err())
		case <-timer.C:
		}
	}
}

func classifyScenarioFailure(err error) *TrialFailure {
	if errors.Is(err, context.DeadlineExceeded) {
		return &TrialFailure{Code: "SCENARIO_TIMEOUT", Message: "Scenario exceeded the configured trial timeout."}
	}
	return &TrialFailure{Code: "SCENARIO_BLOCKED", Message: err.Error()}
}

func (client trialClient) cancelRun(ctx context.Context, workspaceID, runID string) error {
	path := "/v1/runs/" + url.PathEscape(runID) + "/cancel?workspace_id=" + url.QueryEscape(workspaceID)
	var run httpapi.RunResponse
	err := client.do(ctx, http.MethodPost, path, "", nil, &run)
	var responseErr *httpResponseError
	if errors.As(err, &responseErr) && responseErr.status == http.StatusNotFound {
		return nil
	}
	return err
}

func (client trialClient) getRun(ctx context.Context, workspaceID, runID string) (httpapi.RunResponse, error) {
	path := "/v1/runs/" + url.PathEscape(runID) + "?workspace_id=" + url.QueryEscape(workspaceID)
	var run httpapi.RunResponse
	err := client.do(ctx, http.MethodGet, path, "", nil, &run)
	return run, err
}

func (client trialClient) waitForClosed(ctx context.Context, workspaceID, runID string, run *httpapi.RunResponse) error {
	for !run.Run.Closed {
		path := "/v1/runs/" + url.PathEscape(runID) + "/wait?workspace_id=" + url.QueryEscape(workspaceID)
		if err := client.do(ctx, http.MethodGet, path, "", nil, run); err != nil {
			return err
		}
	}
	return nil
}

func (client trialClient) captureEvidence(ctx context.Context, workspaceID, name string, started time.Time, run httpapi.RunResponse) (ScenarioEvidence, error) {
	var events httpapi.EventListResponse
	path := "/v1/runs/" + url.PathEscape(run.Run.ID) + "/events?workspace_id=" + url.QueryEscape(workspaceID)
	if err := client.do(ctx, http.MethodGet, path, "", nil, &events); err != nil {
		return ScenarioEvidence{}, fmt.Errorf("read %s events: %w", name, err)
	}
	var decision httpapi.PlacementDecisionResponse
	path = "/v1/runs/" + url.PathEscape(run.Run.ID) + "/decision?workspace_id=" + url.QueryEscape(workspaceID)
	if err := client.do(ctx, http.MethodGet, path, "", nil, &decision); err != nil {
		return ScenarioEvidence{}, fmt.Errorf("read %s placement: %w", name, err)
	}
	return ScenarioEvidence{
		Name:       name,
		StartedAt:  started,
		DurationMS: time.Since(started).Milliseconds(),
		Run:        run.Run,
		Placement:  decision.Decision,
		Events:     events.Events,
	}, nil
}

func scenarioWorkload(workspaceID string, spec TrialSpec, platform domain.Platform, plan scenarioPlan) domain.WorkloadRevision {
	maxCost := spec.MaxExpectedCostUSD
	resources := domain.ResourceRequirements{}
	if spec.AdapterType != "docker" {
		resources.Accelerators = []domain.AcceleratorRequirement{{Vendor: "nvidia", Count: 1}}
	}
	return domain.WorkloadRevision{
		ID:          "wrev_conformance_" + strings.ReplaceAll(plan.name, "-", "_"),
		WorkspaceID: workspaceID,
		WorkloadID:  "wrk_conformance_probe",
		Digest:      "sha256:conformance-" + plan.name,
		Spec: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{Name: "main", Image: spec.Image, Platform: platform, Args: plan.arguments}},
			Resources:  resources,
			Placement: domain.PlacementPolicy{
				Objective:              domain.ObjectiveBalanced,
				ExpectedRuntimeSeconds: spec.Timeout.Seconds(),
				MaxExpectedCostUSD:     &maxCost,
			},
			Execution: domain.ExecutionPolicy{MaxRuntimeSeconds: int64(spec.Timeout.Seconds()), MaxPreStartAttempts: 1},
		},
	}
}

func scenarioPassed(mode Mode, run domain.RunRecord) bool {
	if !run.Closed || run.Cleanup != domain.CleanupConfirmed {
		return false
	}
	if mode == LaunchCancelMode {
		return run.Outcome == domain.RunOutcomeCancelled
	}
	return run.Outcome == domain.RunOutcomeSucceeded && run.ExitCode != nil && *run.ExitCode == 0
}

func (client trialClient) do(ctx context.Context, method, path, idempotencyKey string, body, response any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	result, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer result.Body.Close()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(result.Body, 4096))
		return &httpResponseError{method: method, path: path, status: result.StatusCode, body: strings.TrimSpace(string(raw))}
	}
	if response == nil {
		return nil
	}
	if err := json.NewDecoder(result.Body).Decode(response); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return nil
}

type httpResponseError struct {
	method string
	path   string
	status int
	body   string
}

func (err *httpResponseError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", err.method, err.path, err.status, err.body)
}

func randomID(prefix string) (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(raw), nil
}

func randomSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate operator token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func localPublicURL(address net.Addr) string {
	_, port, err := net.SplitHostPort(address.String())
	if err != nil {
		return ""
	}
	return "http://host.docker.internal:" + port
}
