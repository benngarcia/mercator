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
	bootstrap, err := registry.Invite(context.Background(), testWorkspace, testNodeID, testRentalID, 1)
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
	t.Cleanup(cancel)
	go func() { _ = h.agent.Run(ctx) }()
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
}

func newRecordingRuntime() *recordingRuntime { return &recordingRuntime{} }

func (runtime *recordingRuntime) Facts(context.Context) (capability.NodeFacts, error) {
	return capability.NodeFacts{
		Host: capability.HostFacts{OS: "linux", Architecture: "amd64", ContainerRuntime: "docker"},
	}, nil
}

func (runtime *recordingRuntime) PrepareImage(context.Context, capability.PrepareImageCommand) error {
	return nil
}

func (runtime *recordingRuntime) PrepareArtifact(context.Context, capability.PrepareArtifactCommand) error {
	return nil
}

func (runtime *recordingRuntime) LaunchWorkload(_ context.Context, command capability.LaunchWorkloadCommand) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.launched = append(runtime.launched, command)
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
