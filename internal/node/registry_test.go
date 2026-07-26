package node_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
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

// TestAReportedWorkloadIsDatedByTheClockMercatorKeeps is the moment that makes a
// node's report comparable with anything. Every moment in the report is the node's
// own clock, and this machine's is an hour ahead: it says it looked at 13:00 and
// that its container started at 12:59, which are consistent with each other and
// with nothing the control plane knows. The registry stamps when it accepted the
// report, so a rule downstream has one moment in Mercator's frame to measure the
// node's claims against.
//
// The node's own stamp is replaced rather than kept where it is absent, because a
// moment a machine can set is a moment a machine can set wrong.
func TestAReportedWorkloadIsDatedByTheClockMercatorKeeps(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	ahead := clock.Now().Add(time.Hour)
	startedAhead := ahead.Add(-time.Minute)

	report(t, registry, bootstrap.NodeID, enrollment.SessionToken, node.Event{
		ID:         "evt-running",
		Kind:       node.EventWorkload,
		ObservedAt: ahead,
		Workload: &capability.WorkloadObservation{
			RunID: "run-1", AttemptID: "attempt-1", Phase: capability.WorkloadPhaseRunning,
			ObservedAt: ahead, StartedAt: &startedAhead, ReceivedAt: ahead,
		},
	})

	observation, err := registry.ObserveWorkload(context.Background(), capability.WorkloadRef{
		NodeRef: nodeRef(bootstrap), RunID: "run-1", AttemptID: "attempt-1",
	})
	if err != nil {
		t.Fatalf("observe workload: %v", err)
	}
	if !observation.ReceivedAt.Equal(clock.Now()) {
		t.Fatalf("the stored report says Mercator received it at %s, and Mercator's clock read %s",
			observation.ReceivedAt.Format(time.RFC3339Nano), clock.Now().Format(time.RFC3339Nano))
	}
	if !observation.ObservedAt.Equal(ahead) {
		t.Fatalf("the node said it looked at %s and the record kept %s",
			ahead.Format(time.RFC3339Nano), observation.ObservedAt.Format(time.RFC3339Nano))
	}
}

// TestAnAbsentWorkloadIsDatedByTheRegistryThatLooked keeps the one observation the
// control plane makes for itself inside the same rule. Nothing was reported, so
// both moments are Mercator's own: it looked now, and it learned now.
func TestAnAbsentWorkloadIsDatedByTheRegistryThatLooked(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enroll(t, registry, bootstrap)

	observation, err := registry.ObserveWorkload(context.Background(), capability.WorkloadRef{
		NodeRef: nodeRef(bootstrap), RunID: "run-unknown", AttemptID: "attempt-1",
	})
	if err != nil {
		t.Fatalf("observe workload: %v", err)
	}

	if !observation.ReceivedAt.Equal(clock.Now()) {
		t.Fatalf("an absence Mercator observed itself is dated %s, and its clock read %s",
			observation.ReceivedAt.Format(time.RFC3339Nano), clock.Now().Format(time.RFC3339Nano))
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
			Host:       capability.HostFacts{OS: "linux", ContainerRuntime: "docker", Disk: capability.DiskFacts{Known: true, FreeBytes: 500 << 30}},
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
	if facts.Host.Disk.FreeBytes != 500<<30 {
		t.Fatalf("facts were not replaced by the heartbeat: %+v", facts.Host)
	}
}

// TestARoomNobodyMeasuredIsNeverOfferedAsRoom is the coupling between the two
// halves of one report. A node states the room it has left and separately
// whether it established the number, and nothing in the wire contract stops a
// report from carrying both a measurement and a denial that it made one: an
// agent that keeps a previous answer while marking it unestablished, an older
// build, or another implementation of the NodeRuntime protocol. Read as two
// independent fields it advertises 400GiB nobody measured, a Run with a floor
// is admitted onto it, and the fleet listing states the contradiction back to
// the operator. What the control plane keeps is the half the machine stands
// behind, once, where the report comes in.
func TestARoomNobodyMeasuredIsNeverOfferedAsRoom(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)

	report(t, registry, bootstrap.NodeID, enrollment.SessionToken, node.Event{
		ID:         "evt-heartbeat-unestablished-disk",
		Kind:       node.EventHeartbeat,
		ObservedAt: clock.Now(),
		Facts: &capability.NodeFacts{
			ObservedAt: clock.Now(),
			Host: capability.HostFacts{
				OS:               "linux",
				ContainerRuntime: "docker",
				Disk:             capability.DiskFacts{Known: false, TotalBytes: 500 << 30, FreeBytes: 400 << 30},
			},
		},
	})

	offers, err := registry.Offers(context.Background(), testWorkspace)
	if err != nil {
		t.Fatalf("list node offers: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("offers = %d, want the one enrolled node", len(offers))
	}
	if offers[0].Resources.EphemeralDiskBytes != 0 {
		t.Fatalf("the offer advertises %d bytes of room from a measurement the node says it did not make",
			offers[0].Resources.EphemeralDiskBytes)
	}
	records, err := registry.List(context.Background(), testWorkspace)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if got := records[0].Facts.Host.Disk.FreeBytes; got != 0 {
		t.Fatalf("the record kept %d bytes nobody established", got)
	}
	if got := records[0].Disk(); got != node.DiskUnmeasurable {
		t.Fatalf("the node's disk reads %q, and the machine said it could not measure it", got)
	}
}

// TestAnInvitedNodeHasReportedNothingRatherThanFailedToMeasure separates the
// two answers a zero disk could mean. An identity exists before the machine
// does, and until its agent speaks nobody has attempted a measurement, so
// "this daemon cannot be measured" is a claim about a host Mercator has never
// heard from.
func TestAnInvitedNodeHasReportedNothingRatherThanFailedToMeasure(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)

	records, err := registry.List(context.Background(), testWorkspace)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}

	if len(records) != 1 || records[0].ID != bootstrap.NodeID {
		t.Fatalf("records = %+v, want the invited identity", records)
	}
	if got := records[0].Disk(); got != node.DiskNeverReported {
		t.Fatalf("an invited node's disk reads %q before its agent has said anything", got)
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

// TestANodeDeclaresOnlyWhatItsRuntimePerforms is ADR 0005 one layer down. A
// negotiated capability set is a promise Placement will route work against, so
// a declaration the runtime cannot honour is capacity Mercator believes in and
// does not have. This node declared Artifact replicas, Cache Mounts, prewarming,
// and garbage collection while the Docker runtime implemented none of them; each
// becomes true again in the slice that earns it, and only garbage collection is
// still owed.
func TestANodeDeclaresOnlyWhatItsRuntimePerforms(t *testing.T) {
	registry, _ := newRegistry(t)

	support := registry.NodeSupport()

	earned := map[string]bool{
		"exact_image_inventory": support.ExactImageInventory,
		"cache_mounts":          support.CacheMounts,
		"prewarm":               support.Prewarm,
		"artifact_replicas":     support.ArtifactReplicas,
	}
	for name, declared := range earned {
		if !declared {
			t.Errorf("the agent performs %s, so the node has to declare it or Placement routes no work against it", name)
		}
	}
	if support.GarbageCollection {
		t.Error("the node declares garbage_collection, and nothing on the machine reclaims a byte")
	}
}

// TestAPathANodeMeasuredReachesTheOfferItPrices is the last step of the only
// measurement of a link anything in this tree makes. An enrolled node times its
// own Artifact reads and reports what it found; unless the offer carries it,
// Placement still prices every read in the fleet at Mercator's fleet-wide guess,
// and the measurement is a number in a heartbeat that changes nothing.
//
// The rate the offer answers with is what decides a placement, so this asserts
// the answer rather than the field: DownloadRate is the one rule both the
// prediction and a Run's hard floor read, and a fact that reached the offer and
// not that answer would be published and still unread.
func TestAPathANodeMeasuredReachesTheOfferItPrices(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)

	report(t, registry, bootstrap.NodeID, enrollment.SessionToken, node.Event{
		ID:         "evt-heartbeat-measured-path",
		Kind:       node.EventHeartbeat,
		ObservedAt: clock.Now(),
		Facts: &capability.NodeFacts{
			ObservedAt: clock.Now(),
			Host: capability.HostFacts{
				OS:               "linux",
				ContainerRuntime: "docker",
				Network: []domain.NetworkFact{{
					Scope:       domain.NetworkScopeObjectStore,
					Statistic:   "p10",
					ValueMbps:   1750,
					Source:      "node_transfer",
					SampleCount: 3,
					ObservedAt:  clock.Now(),
					ValidUntil:  clock.Now().Add(time.Hour),
					Confidence:  0.9,
				}},
			},
		},
	})

	offers, err := registry.Offers(context.Background(), testWorkspace)
	if err != nil {
		t.Fatalf("list node offers: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("offers = %d, want the one enrolled node", len(offers))
	}
	rate := offers[0].DownloadRate(domain.NetworkScopeObjectStore)
	if rate.Mbps != 1750 || rate.Measurement != "node_transfer" {
		t.Fatalf("the offer prices an Artifact read at %+v, and this node measured 1750 Mbps itself", rate)
	}
	// The registry path this node never crossed. A node that measured one link is
	// not a node that measured them all, and the answer for the other is the
	// standing assumption saying so.
	if registryRate := offers[0].DownloadRate(domain.NetworkScopeRegistry); registryRate.Assumption != domain.AssumptionRegistryRate {
		t.Fatalf("the offer prices an image pull at %+v, and nothing has measured this node's link to a registry", registryRate)
	}
}
