package node_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
)

func TestAnInvitedMachineEnrollsAndReceivesASession(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)

	enrollment := enroll(t, registry, bootstrap)

	if enrollment.NodeID != bootstrap.NodeID {
		t.Fatalf("enrolled node = %q, want %q", enrollment.NodeID, bootstrap.NodeID)
	}
	if enrollment.SessionToken == "" {
		t.Fatal("enrollment must issue a session credential")
	}
	if enrollment.FencingToken == 0 {
		t.Fatal("enrollment must issue a fencing token")
	}
	if !enrollment.LeaseExpires.After(clock.Now()) {
		t.Fatalf("lease expires at %s, which is not after now %s", enrollment.LeaseExpires, clock.Now())
	}
}

func TestAnInvitationCannotBeRedeemedTwice(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enroll(t, registry, bootstrap)

	_, err := registry.Enroll(context.Background(), enrollmentRequest(bootstrap))

	if !errors.Is(err, node.ErrEnrollmentSpent) {
		t.Fatalf("second redemption of one invitation = %v, want ErrEnrollmentSpent", err)
	}
}

func TestEnrollmentRefusesAMachineClaimingAnotherGeneration(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	request := enrollmentRequest(bootstrap)
	request.Generation = bootstrap.Generation + 1

	_, err := registry.Enroll(context.Background(), request)

	if err == nil {
		t.Fatal("a machine must not enroll as a generation it was not invited for")
	}
}

func TestACommandIsDeliveredOverTheNodesOwnSession(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	session := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)

	receipt, err := registry.LaunchWorkload(context.Background(), launchCommand(bootstrap, enrollment, "op-launch-1"))
	if err != nil {
		t.Fatalf("launch workload: %v", err)
	}

	if receipt.Duplicate {
		t.Fatal("the first launch must not report as a duplicate")
	}
	command := receiveCommand(t, session)
	if command.OperationID != "op-launch-1" || command.Kind != node.CommandLaunchWorkload {
		t.Fatalf("delivered command = %+v, want the launch operation", command)
	}
	if command.FencingToken != enrollment.FencingToken {
		t.Fatalf("command fencing token = %d, want %d", command.FencingToken, enrollment.FencingToken)
	}
}

func TestRepeatingAnOperationIDDeliversNothingAndReportsDuplicate(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	session := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)
	if _, err := registry.LaunchWorkload(context.Background(), launchCommand(bootstrap, enrollment, "op-launch-1")); err != nil {
		t.Fatalf("first launch: %v", err)
	}
	receiveCommand(t, session)

	receipt, err := registry.LaunchWorkload(context.Background(), launchCommand(bootstrap, enrollment, "op-launch-1"))
	if err != nil {
		t.Fatalf("repeat launch: %v", err)
	}

	if !receipt.Duplicate {
		t.Fatal("a repeated operation ID must report as a duplicate")
	}
	select {
	case command := <-session.Commands():
		t.Fatalf("a repeated operation ID must deliver nothing, got %+v", command)
	default:
	}
}

func TestAReconnectingNodeReceivesTheCommandsItNeverAcknowledged(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	first := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)
	if _, err := registry.LaunchWorkload(context.Background(), launchCommand(bootstrap, enrollment, "op-launch-1")); err != nil {
		t.Fatalf("launch workload: %v", err)
	}
	// The node never drains this session: the connection dropped instead.
	registry.CloseSession(first)

	reconnected := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)

	command := receiveCommand(t, reconnected)
	if command.OperationID != "op-launch-1" {
		t.Fatalf("redelivered command = %q, want the unacknowledged launch", command.OperationID)
	}
}

func TestAnAcknowledgedCommandIsNotRedeliveredAfterAReconnect(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	session := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)
	if _, err := registry.LaunchWorkload(context.Background(), launchCommand(bootstrap, enrollment, "op-launch-1")); err != nil {
		t.Fatalf("launch workload: %v", err)
	}
	receiveCommand(t, session)
	settle(t, registry, bootstrap.NodeID, enrollment.SessionToken, node.Result{
		OperationID: "op-launch-1",
		Applied:     true,
		ReportedAt:  clock.Now(),
	})
	registry.CloseSession(session)

	reconnected := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)

	select {
	case command := <-reconnected.Commands():
		t.Fatalf("an applied command must not be redelivered, got %+v", command)
	default:
	}
}

func TestReconciliationTellsARestartedControlPlaneWhatTheNodeAlreadyDid(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	session := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)
	if _, err := registry.LaunchWorkload(context.Background(), launchCommand(bootstrap, enrollment, "op-launch-1")); err != nil {
		t.Fatalf("launch workload: %v", err)
	}
	receiveCommand(t, session)
	settle(t, registry, bootstrap.NodeID, enrollment.SessionToken, node.Result{
		OperationID: "op-launch-1", Applied: true, ReportedAt: clock.Now(),
	})
	report(t, registry, bootstrap.NodeID, enrollment.SessionToken, node.Event{
		ID:         "evt-started",
		Kind:       node.EventWorkload,
		ObservedAt: clock.Now(),
		Workload: &capability.WorkloadObservation{
			RunID: "run-1", AttemptID: "attempt-1", Phase: capability.WorkloadPhaseRunning, ObservedAt: clock.Now(),
		},
	})

	reconciliation, err := registry.Reconcile(context.Background(), nodeRef(bootstrap))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if len(reconciliation.AppliedOperationIDs) != 1 || reconciliation.AppliedOperationIDs[0] != "op-launch-1" {
		t.Fatalf("applied operations = %v, want the launch it already performed", reconciliation.AppliedOperationIDs)
	}
	if len(reconciliation.Workloads) != 1 || reconciliation.Workloads[0].RunID != "run-1" {
		t.Fatalf("reported workloads = %+v, want the running workload", reconciliation.Workloads)
	}
	if reconciliation.FencingToken != enrollment.FencingToken {
		t.Fatalf("fencing token = %d, want %d", reconciliation.FencingToken, enrollment.FencingToken)
	}
}

func TestAWorkloadTheNodeNeverMentionedIsAbsentRatherThanExited(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enroll(t, registry, bootstrap)

	observation, err := registry.ObserveWorkload(context.Background(), capability.WorkloadRef{
		NodeRef: nodeRef(bootstrap), RunID: "run-unknown", AttemptID: "attempt-1",
	})
	if err != nil {
		t.Fatalf("observe workload: %v", err)
	}

	if observation.Phase != capability.WorkloadPhaseAbsent {
		t.Fatalf("phase = %q, want %q", observation.Phase, capability.WorkloadPhaseAbsent)
	}
	if observation.Phase.Exited() {
		t.Fatal("an absent workload must never read as exited")
	}
}

func TestASupersededSessionIsClosedWhenTheNodeEnrollsAgain(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	session := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)
	// Re-inviting the same identity is what a Rental generation restart does.
	second := reinvite(t, registry, bootstrap)

	if _, err := registry.Enroll(context.Background(), enrollmentRequest(second)); err != nil {
		t.Fatalf("second enrollment: %v", err)
	}

	select {
	case <-session.Done():
	default:
		t.Fatal("a superseded session must be closed, or fencing is advisory")
	}
}

func TestACommandCarryingASupersededFencingTokenIsRefused(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	stale := enroll(t, registry, bootstrap)
	second := reinvite(t, registry, bootstrap)
	if _, err := registry.Enroll(context.Background(), enrollmentRequest(second)); err != nil {
		t.Fatalf("second enrollment: %v", err)
	}

	_, err := registry.LaunchWorkload(context.Background(), launchCommand(bootstrap, stale, "op-stale"))

	if !errors.Is(err, node.ErrFenced) {
		t.Fatalf("stale-token command = %v, want ErrFenced", err)
	}
}

func TestASpooledEventReplayedAfterAReconnectChangesNothing(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	exited := node.Event{
		ID:         "evt-exit",
		Kind:       node.EventWorkload,
		ObservedAt: clock.Now(),
		Workload: &capability.WorkloadObservation{
			RunID: "run-1", AttemptID: "attempt-1", Phase: capability.WorkloadPhaseExited,
			ExitCode: exitCode(0), ObservedAt: clock.Now(),
		},
	}
	report(t, registry, bootstrap.NodeID, enrollment.SessionToken, exited)

	report(t, registry, bootstrap.NodeID, enrollment.SessionToken, exited)

	observation, err := registry.ObserveWorkload(context.Background(), capability.WorkloadRef{
		NodeRef: nodeRef(bootstrap), RunID: "run-1", AttemptID: "attempt-1",
	})
	if err != nil {
		t.Fatalf("observe workload: %v", err)
	}
	if observation.Phase != capability.WorkloadPhaseExited {
		t.Fatalf("phase = %q, want %q", observation.Phase, capability.WorkloadPhaseExited)
	}
}

func TestANodeThatStopsHeartbeatingBecomesLostRatherThanDead(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)

	clock.Advance(node.DefaultLease + time.Second)
	expired, err := registry.ExpireLeases(context.Background())
	if err != nil {
		t.Fatalf("expire leases: %v", err)
	}

	if len(expired) != 1 || expired[0].ID != bootstrap.NodeID {
		t.Fatalf("expired nodes = %+v, want the silent node", expired)
	}
	if expired[0].State != node.StateLost {
		t.Fatalf("state = %q, want %q", expired[0].State, node.StateLost)
	}
	if expired[0].Alive(clock.Now()) {
		t.Fatal("a lost node must not read as alive")
	}
}

func TestAHeartbeatRenewsTheLeaseAndReplacesTheNodesFacts(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)

	clock.Advance(node.DefaultLease / 2)
	report(t, registry, bootstrap.NodeID, enrollment.SessionToken, node.Event{
		ID:         "evt-heartbeat-1",
		Kind:       node.EventHeartbeat,
		ObservedAt: clock.Now(),
		Facts: &capability.NodeFacts{
			ObservedAt: clock.Now(),
			Host:       capability.HostFacts{OS: "linux", ContainerRuntime: "docker", DiskFreeBytes: 500 << 30},
		},
	})
	clock.Advance(node.DefaultLease/2 + time.Second)
	expired, err := registry.ExpireLeases(context.Background())
	if err != nil {
		t.Fatalf("expire leases: %v", err)
	}

	if len(expired) != 0 {
		t.Fatalf("a heartbeating node must keep its lease, got %+v", expired)
	}
	facts, err := registry.Facts(context.Background(), nodeRef(bootstrap))
	if err != nil {
		t.Fatalf("facts: %v", err)
	}
	if facts.Host.DiskFreeBytes != 500<<30 {
		t.Fatalf("facts were not replaced by the heartbeat: %+v", facts.Host)
	}
}

func TestASessionCredentialFromASupersededEnrollmentIsRejected(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	stale := enroll(t, registry, bootstrap)
	second := reinvite(t, registry, bootstrap)
	if _, err := registry.Enroll(context.Background(), enrollmentRequest(second)); err != nil {
		t.Fatalf("second enrollment: %v", err)
	}

	_, err := registry.OpenSession(context.Background(), bootstrap.NodeID, stale.SessionToken)

	if err == nil {
		t.Fatal("a session credential from a superseded enrollment must not authenticate")
	}
}

// Helpers below keep each case to arrange, act, assert.

const (
	testWorkspace = "ws_nodes"
	testNode      = "nod_alpha"
	testRental    = "rnt_alpha"
)

func newRegistry(t *testing.T) (*node.Registry, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	registry := node.NewRegistry(
		node.NewMemoryStore(),
		node.NewSigner(node.DeriveKey([]byte("test-master-key"))),
		"https://mercator.test",
		node.WithClock(clock.Now),
	)
	return registry, clock
}

// testClock is the scripted clock every case shares, so lease expiry is stated
// rather than waited for.
type testClock struct{ now time.Time }

func (clock *testClock) Now() time.Time { return clock.now }

func (clock *testClock) Advance(by time.Duration) { clock.now = clock.now.Add(by) }

func invite(t *testing.T, registry *node.Registry) capability.NodeBootstrap {
	t.Helper()
	bootstrap, err := registry.Invite(context.Background(), node.Invitation{
		WorkspaceID: testWorkspace, NodeID: testNode, RentalID: testRental, Generation: 1,
		ShadowPriceUSDPerHour: 1.5,
	})
	if err != nil {
		t.Fatalf("invite node: %v", err)
	}
	return bootstrap
}

// reinvite issues a fresh invitation for the same identity, which is what a
// Rental generation restart does when its agent needs to join again.
func reinvite(t *testing.T, registry *node.Registry, previous capability.NodeBootstrap) capability.NodeBootstrap {
	t.Helper()
	bootstrap, err := registry.Reinvite(context.Background(), testWorkspace, previous.NodeID)
	if err != nil {
		t.Fatalf("reinvite node: %v", err)
	}
	return bootstrap
}

func enroll(t *testing.T, registry *node.Registry, bootstrap capability.NodeBootstrap) capability.Enrollment {
	t.Helper()
	enrollment, err := registry.Enroll(context.Background(), enrollmentRequest(bootstrap))
	if err != nil {
		t.Fatalf("enroll node: %v", err)
	}
	return enrollment
}

func enrollmentRequest(bootstrap capability.NodeBootstrap) capability.EnrollmentRequest {
	return capability.EnrollmentRequest{
		NodeID:          bootstrap.NodeID,
		RentalID:        bootstrap.RentalID,
		Generation:      bootstrap.Generation,
		EnrollmentToken: bootstrap.EnrollmentToken,
		AgentVersion:    "test",
		Facts: capability.NodeFacts{
			Host: capability.HostFacts{OS: "linux", ContainerRuntime: "docker"},
		},
	}
}

func nodeRef(bootstrap capability.NodeBootstrap) capability.NodeRef {
	return capability.NodeRef{
		WorkspaceID: testWorkspace,
		NodeID:      bootstrap.NodeID,
		RentalID:    bootstrap.RentalID,
		Generation:  bootstrap.Generation,
	}
}

func launchCommand(bootstrap capability.NodeBootstrap, enrollment capability.Enrollment, operationID string) capability.LaunchWorkloadCommand {
	command := capability.LaunchWorkloadCommand{
		RunID:          "run-1",
		AttemptID:      "attempt-1",
		BookingID:      "bkg-1",
		ManifestDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
	}
	command.NodeRef = nodeRef(bootstrap)
	command.OperationID = operationID
	command.FencingToken = enrollment.FencingToken
	return command
}

func openSession(t *testing.T, registry *node.Registry, nodeID, sessionToken string) *node.Session {
	t.Helper()
	session, err := registry.OpenSession(context.Background(), nodeID, sessionToken)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { registry.CloseSession(session) })
	return session
}

func receiveCommand(t *testing.T, session *node.Session) node.Command {
	t.Helper()
	select {
	case command := <-session.Commands():
		return command
	case <-time.After(time.Second):
		t.Fatal("no command arrived on the node's session")
		return node.Command{}
	}
}

func settle(t *testing.T, registry *node.Registry, nodeID, sessionToken string, result node.Result) {
	t.Helper()
	if err := registry.RecordResult(context.Background(), nodeID, sessionToken, result); err != nil {
		t.Fatalf("record result: %v", err)
	}
}

func report(t *testing.T, registry *node.Registry, nodeID, sessionToken string, event node.Event) {
	t.Helper()
	if err := registry.RecordEvents(context.Background(), nodeID, sessionToken, []node.Event{event}); err != nil {
		t.Fatalf("record events: %v", err)
	}
}

func exitCode(code int) *int { return &code }
