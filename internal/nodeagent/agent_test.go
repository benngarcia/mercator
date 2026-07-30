package nodeagent_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
	"github.com/benngarcia/mercator/internal/nodeagent"
	"github.com/benngarcia/mercator/internal/nodeapi"
)

func TestAnAgentEnrollsOutboundAndRunsTheWorkloadItIsGiven(t *testing.T) {
	harness := start(t)

	harness.launch(t, "op-launch-1")

	launched := harness.runtime.awaitLaunches(t, 1)
	if launched[0].RunID != "run-1" {
		t.Fatalf("launched run = %q, want run-1", launched[0].RunID)
	}
	harness.awaitApplied(t, "op-launch-1")
}

func TestALaunchWhoseResultWasLostIsRedeliveredAndStartsNothingAgain(t *testing.T) {
	harness := start(t)
	// The agent applies the launch and its acknowledgement never arrives, so
	// the control plane still believes the command is outstanding.
	harness.transport.dropNextResult()
	harness.launch(t, "op-launch-1")
	harness.runtime.awaitLaunches(t, 1)

	// Ending the session makes the registry redeliver everything it has not
	// seen acknowledged, which is what a reconnect after a partition does.
	harness.dropSession()

	harness.awaitApplied(t, "op-launch-1")
	if launches := harness.runtime.launches(); len(launches) != 1 {
		t.Fatalf("the runtime launched %d times, want exactly one container", len(launches))
	}
}

func TestAnAgentThatRestartsWithItsMemoryRefusesToLaunchAgain(t *testing.T) {
	harness := start(t)
	harness.transport.dropNextResult()
	harness.launch(t, "op-launch-1")
	harness.runtime.awaitLaunches(t, 1)

	// The machine reboots before the control plane ever learned the outcome.
	restarted := harness.restartAgent(t)

	restarted.awaitApplied(t, "op-launch-1")
	if launches := restarted.runtime.launches(); len(launches) != 0 {
		t.Fatalf("a restarted agent launched %d containers for an operation it already applied", len(launches))
	}
}

func TestAnAgentReportsContainerLifecycleWithoutTheApplicationSayingAnything(t *testing.T) {
	harness := start(t)
	harness.runtime.observe(capability.WorkloadObservation{
		RunID: "run-1", AttemptID: "attempt-1", Phase: capability.WorkloadPhaseExited,
		ExitCode: exitCode(0), ObservedAt: harness.clock(),
	})

	// A fresh session reports what the machine actually holds first.
	harness.reconnect(t)

	waitFor(t, func() bool {
		observation, err := harness.registry.ObserveWorkload(context.Background(), capability.WorkloadRef{
			NodeRef: harness.ref(), RunID: "run-1", AttemptID: "attempt-1",
		})
		return err == nil && observation.Phase == capability.WorkloadPhaseExited
	}, "the node's own exit observation never reached the control plane")
}

func TestAnAgentHeartbeatsItsFactsSoTheControlPlaneKeepsBelievingIt(t *testing.T) {
	harness := start(t)

	waitFor(t, func() bool {
		facts, err := harness.registry.Facts(context.Background(), harness.ref())
		return err == nil && facts.Host.ContainerRuntime == "docker"
	}, "the agent never reported its host facts")
}

// Harness wiring below. Every case drives a real agent over the real node
// protocol against the real registry, because the guarantees under test are
// about what survives a connection, not about one function's return value.

type harness struct {
	registry  *node.Registry
	runtime   *recordingRuntime
	transport *interruptibleTransport
	agent     *nodeagent.Agent
	identity  nodeagent.Identity
	stateDir  string
	server    *httptest.Server
	cancel    context.CancelFunc
	clockAt   time.Time
}

const (
	testWorkspace = "ws_agent"
	testNodeID    = "nod_agent"
	testRentalID  = "rnt_agent"
)

func start(t *testing.T) *harness {
	t.Helper()
	return startWithStateDir(t, t.TempDir())
}

func startWithStateDir(t *testing.T, stateDir string) *harness {
	t.Helper()
	registry := node.NewRegistry(
		node.NewMemoryStore(),
		node.NewSigner(node.DeriveKey([]byte("agent-test-key"))),
		"",
	)
	server := httptest.NewServer(nodeapi.New(registry))
	t.Cleanup(server.Close)
	bootstrap, err := registry.Invite(context.Background(), node.Invitation{
		WorkspaceID: testWorkspace, NodeID: testNodeID, RentalID: testRentalID, Generation: 1,
		ShadowPriceUSDPerHour: 1.5,
	})
	if err != nil {
		t.Fatalf("invite node: %v", err)
	}
	bootstrap.ControlPlaneURL = server.URL
	harness := &harness{
		registry: registry,
		runtime:  newRecordingRuntime(),
		server:   server,
		stateDir: stateDir,
		clockAt:  time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
		identity: nodeagent.Identity{
			ControlPlaneURL: server.URL,
			NodeID:          bootstrap.NodeID,
			RentalID:        bootstrap.RentalID,
			Generation:      bootstrap.Generation,
			EnrollmentToken: bootstrap.EnrollmentToken,
			AgentVersion:    "test",
		},
	}
	harness.transport = &interruptibleTransport{
		HTTPTransport: nodeagent.NewHTTPTransport(server.URL, nil),
	}
	harness.runAgent(t)
	return harness
}

func (h *harness) runAgent(t *testing.T) {
	t.Helper()
	state, err := nodeagent.OpenState(filepath.Join(h.stateDir, "state.json"), h.identity.NodeID)
	if err != nil {
		t.Fatalf("open agent state: %v", err)
	}
	h.agent = nodeagent.New(
		h.identity,
		h.runtime,
		h.transport,
		state,
		nodeagent.WithHeartbeat(20*time.Millisecond),
		nodeagent.WithReconnectBackoff(5*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	stopped := make(chan struct{})
	// Waiting for Run to return is what stops the agent from writing its state
	// file into a directory the test framework is already removing. Cancelling
	// only asks it to stop.
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("the agent did not stop when its context was cancelled")
		}
	})
	go func() {
		defer close(stopped)
		_ = h.agent.Run(ctx)
	}()
}

// restartAgent stops the agent and starts a new one over the same local state,
// which is exactly what a machine reboot looks like.
func (h *harness) restartAgent(t *testing.T) *harness {
	t.Helper()
	h.cancel()
	h.runtime.reset()
	h.runAgent(t)
	return h
}

func (h *harness) clock() time.Time { return h.clockAt }

func (h *harness) ref() capability.NodeRef {
	return capability.NodeRef{
		WorkspaceID: testWorkspace,
		NodeID:      h.identity.NodeID,
		RentalID:    h.identity.RentalID,
		Generation:  h.identity.Generation,
	}
}

// heartbeats counts the facts the control plane has received, which is the only
// evidence that the agent is still telling it anything.
func (h *harness) heartbeats(t *testing.T) int {
	t.Helper()
	facts, err := h.registry.Facts(context.Background(), h.ref())
	if err != nil {
		return 0
	}
	return int(facts.Host.CPUMillis)
}

func (h *harness) prepareImage(t *testing.T, operationID string) {
	t.Helper()
	h.prepareImageReceipt(t, operationID)
}

func (h *harness) prepareImageReceipt(t *testing.T, operationID string) capability.OperationReceipt {
	t.Helper()
	command := capability.PrepareImageCommand{
		ManifestDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Reference:      "ghcr.io/acme/trainer@sha256:1111111111111111111111111111111111111111111111111111111111111111",
	}
	command.NodeRef = h.ref()
	command.OperationID = operationID
	receipt, err := h.registry.PrepareImage(context.Background(), command)
	if err != nil {
		t.Fatalf("dispatch prepare image: %v", err)
	}
	return receipt
}

func (h *harness) launch(t *testing.T, operationID string) {
	t.Helper()
	command := capability.LaunchWorkloadCommand{
		RunID:          "run-1",
		AttemptID:      "attempt-1",
		BookingID:      "bkg-1",
		ManifestDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
	}
	command.NodeRef = h.ref()
	command.OperationID = operationID
	if _, err := h.registry.LaunchWorkload(context.Background(), command); err != nil {
		t.Fatalf("dispatch launch: %v", err)
	}
}

// dropSession ends the agent's current stream, so it reconnects and the
// registry replays every command it has not seen acknowledged.
func (h *harness) dropSession() { h.transport.interrupt() }

func (h *harness) reconnect(t *testing.T) {
	t.Helper()
	waitFor(t, func() bool {
		_, err := h.registry.Facts(context.Background(), h.ref())
		return err == nil
	}, "the agent never enrolled")
}

func (h *harness) awaitApplied(t *testing.T, operationID string) {
	t.Helper()
	waitFor(t, func() bool {
		reconciliation, err := h.registry.Reconcile(context.Background(), h.ref())
		if err != nil {
			return false
		}
		for _, applied := range reconciliation.AppliedOperationIDs {
			if applied == operationID {
				return true
			}
		}
		return false
	}, "the control plane never learned that "+operationID+" was applied")
}

func waitFor(t *testing.T, satisfied func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if satisfied() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if message != "" {
		t.Fatal(message)
	}
}

type recordingRuntime struct {
	mu           sync.Mutex
	launched     []capability.LaunchWorkloadCommand
	observations []capability.WorkloadObservation
	// facts counts how many times the agent has read this machine's facts,
	// which is how a case sees whether heartbeats are still flowing.
	facts           int
	attempted       int
	failNextCommand bool
	blockPrepare    chan struct{}
	prepareStarted  chan struct{}
}

func newRecordingRuntime() *recordingRuntime { return &recordingRuntime{} }

func (runtime *recordingRuntime) Facts(context.Context) (capability.NodeFacts, error) {
	runtime.mu.Lock()
	runtime.facts++
	count := runtime.facts
	runtime.mu.Unlock()
	return capability.NodeFacts{
		// CPUMillis carries the read count so a case can watch heartbeats
		// arrive through the control plane's own view rather than the agent's.
		Host: capability.HostFacts{OS: "linux", Architecture: "amd64", ContainerRuntime: "docker", CPUMillis: int64(count)},
	}, nil
}

func (runtime *recordingRuntime) PrepareImage(context.Context, capability.PrepareImageCommand) error {
	runtime.mu.Lock()
	runtime.attempted++
	block, started := runtime.blockPrepare, runtime.prepareStarted
	runtime.blockPrepare = nil
	fail := runtime.failNextCommand
	runtime.failNextCommand = false
	runtime.mu.Unlock()
	if started != nil {
		close(started)
	}
	if block != nil {
		<-block
	}
	if fail {
		return errors.New("nodeagent test: the pull failed")
	}
	return nil
}

func (runtime *recordingRuntime) failNext() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.failNextCommand = true
}

// blockNextPrepare makes the next preparation hang until the returned release
// is called, and releases it at cleanup so a case can never leak the worker.
func (runtime *recordingRuntime) blockNextPrepare(t *testing.T) func() {
	t.Helper()
	runtime.mu.Lock()
	runtime.blockPrepare = make(chan struct{})
	runtime.prepareStarted = make(chan struct{})
	block := runtime.blockPrepare
	runtime.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(block) }) }
	t.Cleanup(release)
	return release
}

func (runtime *recordingRuntime) awaitPrepareStarted(t *testing.T) {
	t.Helper()
	runtime.mu.Lock()
	started := runtime.prepareStarted
	runtime.mu.Unlock()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the runtime was never asked to prepare the image")
	}
}

func (runtime *recordingRuntime) attempts() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.attempted
}

func (runtime *recordingRuntime) awaitAttempts(t *testing.T, count int) {
	t.Helper()
	waitFor(t, func() bool { return runtime.attempts() >= count }, "the runtime was never asked to do the work")
}

func (runtime *recordingRuntime) PrepareArtifact(context.Context, capability.PrepareArtifactCommand) error {
	return nil
}

func (runtime *recordingRuntime) LaunchWorkload(_ context.Context, command capability.LaunchWorkloadCommand) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.attempted++
	runtime.launched = append(runtime.launched, command)
	if runtime.failNextCommand {
		runtime.failNextCommand = false
		return errors.New("nodeagent test: the launch failed")
	}
	return nil
}

func (runtime *recordingRuntime) StopWorkload(context.Context, capability.StopWorkloadCommand) error {
	return nil
}

func (runtime *recordingRuntime) Observe(context.Context) ([]capability.WorkloadObservation, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return append([]capability.WorkloadObservation(nil), runtime.observations...), nil
}

func (runtime *recordingRuntime) observe(observation capability.WorkloadObservation) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.observations = append(runtime.observations, observation)
}

func (runtime *recordingRuntime) launches() []capability.LaunchWorkloadCommand {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return append([]capability.LaunchWorkloadCommand(nil), runtime.launched...)
}

func (runtime *recordingRuntime) reset() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.launched = nil
}

func (runtime *recordingRuntime) awaitLaunches(t *testing.T, count int) []capability.LaunchWorkloadCommand {
	t.Helper()
	var launched []capability.LaunchWorkloadCommand
	waitFor(t, func() bool {
		launched = runtime.launches()
		return len(launched) >= count
	}, "the runtime never received the launch")
	return launched
}

// interruptibleTransport is the real HTTP transport with two faults a test can
// inject: a lost command result, and a dropped session. Both are ordinary
// network conditions, and both are what the idempotency guarantees exist for.
type interruptibleTransport struct {
	*nodeagent.HTTPTransport

	mu         sync.Mutex
	dropResult bool
	endSession func()
}

func (transport *interruptibleTransport) dropNextResult() {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.dropResult = true
}

func (transport *interruptibleTransport) interrupt() {
	transport.mu.Lock()
	end := transport.endSession
	transport.mu.Unlock()
	if end != nil {
		end()
	}
}

func (transport *interruptibleTransport) Session(ctx context.Context, nodeID, sessionToken string, commands chan<- node.Command) error {
	streamCtx, end := context.WithCancel(ctx)
	defer end()
	transport.mu.Lock()
	transport.endSession = end
	transport.mu.Unlock()
	return transport.HTTPTransport.Session(streamCtx, nodeID, sessionToken, commands)
}

func (transport *interruptibleTransport) SendResult(ctx context.Context, nodeID, sessionToken string, result node.Result) error {
	transport.mu.Lock()
	drop := transport.dropResult
	transport.dropResult = false
	transport.mu.Unlock()
	if drop {
		return errLostResult
	}
	return transport.HTTPTransport.SendResult(ctx, nodeID, sessionToken, result)
}

var errLostResult = errors.New("nodeagent test: the command result was lost in flight")

func exitCode(code int) *int { return &code }

func TestALongRunningCommandDoesNotCostTheNodeItsLease(t *testing.T) {
	harness := start(t)
	// A multi-gigabyte pull takes far longer than a lease. Blocking stands in
	// for one: the command does not return until this case releases it.
	release := harness.runtime.blockNextPrepare(t)
	harness.prepareImage(t, "op-slow-pull")
	harness.runtime.awaitPrepareStarted(t)
	before := harness.heartbeats(t)

	// Act: wait for the control plane to hear from the node again while the
	// pull is still running.
	heard := false
	waitFor(t, func() bool {
		heard = harness.heartbeats(t) > before
		return heard
	}, "")
	release()

	if !heard {
		t.Fatalf("heartbeats stopped at %d while a command was running; the control plane would declare this node lost mid-pull", before)
	}
}

// TestARedeliveredPreparationPullsOnce is the promise that makes preparation
// safe to retry at all. A prepare is a multi-gigabyte transfer, and the control
// plane redelivers whenever it did not hear an answer: a lost response, a
// dropped session, a restart. One operation identity is one pull however many
// times it is sent, and the second delivery says so out loud rather than
// silently doing nothing.
func TestARedeliveredPreparationPullsOnce(t *testing.T) {
	harness := start(t)

	first := harness.prepareImageReceipt(t, "op-prepare-once")
	harness.runtime.awaitAttempts(t, 1)
	second := harness.prepareImageReceipt(t, "op-prepare-once")

	if first.Duplicate {
		t.Fatal("the first preparation was reported a duplicate, and nothing had asked for it before")
	}
	if !second.Duplicate {
		t.Fatalf("a redelivered preparation was accepted as new work: %+v", second)
	}
	if attempts := harness.runtime.attempts(); attempts != 1 {
		t.Fatalf("the runtime pulled %d times for one operation identity, want one", attempts)
	}
}

func TestAFailedPreparationIsRetriedAndAFailedLaunchIsNot(t *testing.T) {
	cases := map[string]struct {
		dispatch      func(*harness, *testing.T, string)
		attemptsAfter int
		why           string
	}{
		"a failed pull left nothing behind, so it runs again": {
			dispatch:      (*harness).prepareImage,
			attemptsAfter: 2,
			why:           "a pull that errored left nothing on disk, and remembering it would tell the control plane an image is present that is not",
		},
		"a failed launch may have created a container, so it does not": {
			dispatch:      (*harness).launch,
			attemptsAfter: 1,
			why:           "a launch that errored may still have created a container, and a second one is worse than a missed retry",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			harness := start(t)
			// The command fails and its result is lost in flight, so the
			// control plane still believes the command is outstanding and
			// redelivers it on reconnect.
			harness.transport.dropNextResult()
			harness.runtime.failNext()
			testCase.dispatch(harness, t, "op-1")
			harness.runtime.awaitAttempts(t, 1)

			harness.dropSession()
			harness.awaitApplied(t, "op-1")

			if attempts := harness.runtime.attempts(); attempts != testCase.attemptsAfter {
				t.Fatalf("the runtime was asked %d times, want %d: %s", attempts, testCase.attemptsAfter, testCase.why)
			}
		})
	}
}
