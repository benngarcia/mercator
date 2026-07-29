package node_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/node"
	"github.com/benngarcia/mercator/internal/nodeagent"
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

// TestAContentRequestTheNodeRefusedReachesItAgain is the whole of what a refusal
// means. The machine could not pull the image and said so, which left nothing on
// its disk, so the control plane asking for that content again is a fresh command
// rather than a redelivery. The identity is the machine and the content, so the
// second ask carries the same one, and the record of the failure is what used to
// answer it.
func TestAContentRequestTheNodeRefusedReachesItAgain(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	session := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)
	if _, err := registry.PrepareImage(context.Background(), prepareCommand(bootstrap, enrollment, "op-prepare-1")); err != nil {
		t.Fatalf("first preparation: %v", err)
	}
	receiveCommand(t, session)
	if err := registry.RecordResult(context.Background(), bootstrap.NodeID, enrollment.SessionToken, node.Result{
		OperationID: "op-prepare-1",
		Applied:     false,
		Failure:     "pull failed: registry unreachable",
	}); err != nil {
		t.Fatalf("report the refusal: %v", err)
	}

	receipt, err := registry.PrepareImage(context.Background(), prepareCommand(bootstrap, enrollment, "op-prepare-1"))
	if err != nil {
		t.Fatalf("second preparation: %v", err)
	}

	if receipt.Duplicate {
		t.Fatal("content the node refused must be askable again, not answered as a duplicate")
	}
	command := receiveCommand(t, session)
	if command.OperationID != "op-prepare-1" || command.Kind != node.CommandPrepareImage {
		t.Fatalf("delivered command = %+v, want the preparation asked for again", command)
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

// TestADrainedRegistryOpensNoFurtherSession is the half of the drain that makes it
// final. Ending the open sessions is what lets a shutdown finish; refusing the next
// one is what stops it being undone while the shutdown waits.
//
// The window is the ordinary case rather than a race. http.Server closes its
// listeners and keeps every already-open keep-alive connection usable, and an agent
// posts its events and opens its session over one http.Transport, so a session
// request landing on a connection the sweep did not close starts a fresh long-lived
// read that Shutdown then waits out. That is the whole fifteen seconds the
// production binary allows and then exit 1 on a deadline it could not have met, and
// nothing in the tree asked for it: the flag and the refusal could both be deleted
// with every package green.
func TestADrainedRegistryOpensNoFurtherSession(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)

	registry.Drain()

	if _, err := registry.OpenSession(context.Background(), bootstrap.NodeID, enrollment.SessionToken); err == nil {
		t.Fatal("a control plane that has drained opened a node another long-lived read")
	}
}

// TestADrainEndsTheSessionANodeIsHoldingOpen is the other half, stated at the object
// that owns the sessions. The daemon case holds the same fact through a real
// shutdown; this one holds it here, so a registry that stopped ending them fails
// without an HTTP server in the way.
func TestADrainEndsTheSessionANodeIsHoldingOpen(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	session := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)

	registry.Drain()

	select {
	case <-session.Done():
	default:
		t.Fatal("a drained control plane left a node holding its session open")
	}
}

// TestRetiringARuntimeEndsTheSessionItIsHoldingOpen is the live half of a
// generation's end. The record refuses the machine everything else, and this is
// what stops the connection it already has from carrying one more command down to
// a host Mercator gave up.
func TestRetiringARuntimeEndsTheSessionItIsHoldingOpen(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	session := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)

	if err := registry.Retire(context.Background(), testWorkspace, bootstrap.NodeID); err != nil {
		t.Fatalf("retire the runtime: %v", err)
	}

	select {
	case <-session.Done():
	default:
		t.Fatal("a retired runtime was left holding its session open")
	}
}

// TestARetiredRuntimeCannotHeartbeatItselfBackIntoTheFleet is the resurrection
// this rule exists to stop. An agent whose machine is being torn down keeps
// reporting on the session credential it already has, and a heartbeat is what
// puts a node back into the one state the registry publishes as capacity. Read as
// an ordinary report it would undo the retirement every time the agent spoke.
//
// The report is still kept. What the machine says about itself is history the
// registry holds, and only the standing it would confer is withdrawn.
func TestARetiredRuntimeCannotHeartbeatItselfBackIntoTheFleet(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	if err := registry.Retire(context.Background(), testWorkspace, bootstrap.NodeID); err != nil {
		t.Fatalf("retire the runtime: %v", err)
	}

	clock.Advance(time.Minute)
	err := registry.RecordEvents(context.Background(), bootstrap.NodeID, enrollment.SessionToken, []node.Event{{
		ID:         "evt-heartbeat-after-retirement",
		Kind:       node.EventHeartbeat,
		ObservedAt: clock.Now(),
		Facts: &capability.NodeFacts{
			ObservedAt: clock.Now(),
			Host:       capability.HostFacts{OS: "linux", ContainerRuntime: "docker"},
		},
	}})

	if err != nil {
		t.Fatalf("a retired runtime reporting its liveness: %v", err)
	}
	offers, err := registry.Offers(context.Background(), testWorkspace)
	if err != nil {
		t.Fatalf("read the offers: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("offers = %+v, want a machine Mercator gave up published to nobody", offers)
	}
	fleet, err := registry.List(context.Background(), testWorkspace)
	if err != nil {
		t.Fatalf("list the fleet: %v", err)
	}
	if len(fleet) != 1 || fleet[0].State != node.StateRetired {
		t.Fatalf("fleet = %+v, want the identity still retired after it spoke", fleet)
	}
}

// TestARetiredRuntimeOpensNoFurtherSessionOnTheCredentialItHolds is what makes
// ending the session worth anything. An agent's transport reconnects the instant
// the connection drops, so a retirement that only closed the session it found
// would be undone milliseconds later by the same credential, and the fresh
// session arrives preloaded with every command the previous one never
// acknowledged: the machine Mercator gave up launches the container anyway.
func TestARetiredRuntimeOpensNoFurtherSessionOnTheCredentialItHolds(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)
	if _, err := registry.LaunchWorkload(context.Background(), launchCommand(bootstrap, enrollment, "op-launch-1")); err != nil {
		t.Fatalf("launch the workload: %v", err)
	}
	if err := registry.Retire(context.Background(), testWorkspace, bootstrap.NodeID); err != nil {
		t.Fatalf("retire the runtime: %v", err)
	}

	_, err := registry.OpenSession(context.Background(), bootstrap.NodeID, enrollment.SessionToken)

	if !errors.Is(err, node.ErrRetired) {
		t.Fatalf("a retired runtime reconnecting = %v, want ErrRetired", err)
	}
}

// TestARetiredRuntimeStillReportsWhatItsContainerDid is the half retirement must
// not take. A provider reclaims the machine, Mercator ends the generation, and the
// agent is still alive inside the interruption window when the container exits.
// The node owns exit codes and there is no second authority on them, so an
// identity that could not report would leave a Run that finished looking
// unobserved forever.
func TestARetiredRuntimeStillReportsWhatItsContainerDid(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	if _, err := registry.LaunchWorkload(context.Background(), launchCommand(bootstrap, enrollment, "op-launch-1")); err != nil {
		t.Fatalf("launch the workload: %v", err)
	}
	if err := registry.Retire(context.Background(), testWorkspace, bootstrap.NodeID); err != nil {
		t.Fatalf("retire the runtime: %v", err)
	}

	clock.Advance(time.Minute)
	err := registry.RecordEvents(context.Background(), bootstrap.NodeID, enrollment.SessionToken, []node.Event{{
		ID:         "evt-exited-after-retirement",
		Kind:       node.EventWorkload,
		ObservedAt: clock.Now(),
		Workload: &capability.WorkloadObservation{
			RunID: "run-1", AttemptID: "attempt-1", Phase: capability.WorkloadPhaseExited,
			ExitCode: exitCode(0), ObservedAt: clock.Now(),
		},
	}})

	if err != nil {
		t.Fatalf("a retired runtime reporting its container's exit: %v", err)
	}
	observation, err := registry.ObserveWorkload(context.Background(), capability.WorkloadRef{
		NodeRef: nodeRef(bootstrap), RunID: "run-1", AttemptID: "attempt-1",
	})
	if err != nil {
		t.Fatalf("observe the workload: %v", err)
	}
	if observation.Phase != capability.WorkloadPhaseExited {
		t.Fatalf("phase = %q, want the exit the machine reported", observation.Phase)
	}
	if observation.ExitCode == nil || *observation.ExitCode != 0 {
		t.Fatalf("exit code = %v, want the 0 the machine reported", observation.ExitCode)
	}
}

// TestARetiredRuntimeSettlesTheCommandItAlreadyApplied is the same rule for the
// other thing only the machine knows. The stop was dispatched while the
// generation stood and the agent applied it; the generation ended before the
// answer landed. Refusing the answer would strand an operation the machine really
// performed as pending forever, and dispatching it again is refused, so nothing
// could ever tell the control plane otherwise.
func TestARetiredRuntimeSettlesTheCommandItAlreadyApplied(t *testing.T) {
	registry, clock := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	stop := capability.StopWorkloadCommand{RunID: "run-1", GraceSeconds: 30}
	stop.NodeRef = nodeRef(bootstrap)
	stop.OperationID = "op-stop-1"
	stop.FencingToken = enrollment.FencingToken
	if _, err := registry.StopWorkload(context.Background(), stop); err != nil {
		t.Fatalf("stop the workload: %v", err)
	}
	if err := registry.Retire(context.Background(), testWorkspace, bootstrap.NodeID); err != nil {
		t.Fatalf("retire the runtime: %v", err)
	}

	clock.Advance(time.Minute)
	err := registry.RecordResult(context.Background(), bootstrap.NodeID, enrollment.SessionToken, node.Result{
		OperationID: "op-stop-1", Applied: true, ReportedAt: clock.Now(),
	})

	if err != nil {
		t.Fatalf("a retired runtime settling the command it applied: %v", err)
	}
	reconciliation, err := registry.Reconcile(context.Background(), nodeRef(bootstrap))
	if err != nil {
		t.Fatalf("reconcile the retired identity: %v", err)
	}
	if !slices.Contains(reconciliation.AppliedOperationIDs, "op-stop-1") {
		t.Fatalf("applied = %v, want the stop the machine really performed", reconciliation.AppliedOperationIDs)
	}
}

// TestARetiredRuntimeIsAskedForNothingFurther closes the other half. The node
// reference a caller carries was resolved before the generation ended, and a
// command appended for a retired identity is durable: it would outlive the
// decision that issued it and be delivered to whatever next answers on that
// identity. The identity stays readable, because what it was told and what it
// reported is what a later reconciliation reads.
func TestARetiredRuntimeIsAskedForNothingFurther(t *testing.T) {
	registry, _ := newRegistry(t)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	if err := registry.Retire(context.Background(), testWorkspace, bootstrap.NodeID); err != nil {
		t.Fatalf("retire the runtime: %v", err)
	}

	_, launchErr := registry.LaunchWorkload(context.Background(), launchCommand(bootstrap, enrollment, "op-launch-after-retirement"))
	_, prepareErr := registry.PrepareImage(context.Background(), prepareCommand(bootstrap, enrollment, "op-prepare-after-retirement"))

	if !errors.Is(launchErr, node.ErrRetired) {
		t.Fatalf("a launch dispatched to a retired runtime = %v, want ErrRetired", launchErr)
	}
	if !errors.Is(prepareErr, node.ErrRetired) {
		t.Fatalf("a prepare dispatched to a retired runtime = %v, want ErrRetired", prepareErr)
	}
	if _, err := registry.Reconcile(context.Background(), nodeRef(bootstrap)); err != nil {
		t.Fatalf("a retired identity is history a reconciliation must still be able to read: %v", err)
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

func prepareCommand(bootstrap capability.NodeBootstrap, enrollment capability.Enrollment, operationID string) capability.PrepareImageCommand {
	command := capability.PrepareImageCommand{
		ManifestDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Reference:      "trainer@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Unpack:         true,
	}
	command.NodeRef = nodeRef(bootstrap)
	command.OperationID = operationID
	command.FencingToken = enrollment.FencingToken
	return command
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
					Source:      nodeagent.ArtifactCopySource,
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
	rate := offers[0].DownloadRate(domain.NetworkScopeObjectStore, offers[0].ObservedAt)
	if rate.Mbps != 1750 || rate.Measurement != nodeagent.ArtifactCopySource {
		t.Fatalf("the offer prices an Artifact read at %+v, and this node measured 1750 Mbps itself", rate)
	}
	// The registry path this node never crossed. A node that measured one link is
	// not a node that measured them all, and the answer for the other is the
	// standing assumption saying so.
	if registryRate := offers[0].DownloadRate(domain.NetworkScopeRegistry, offers[0].ObservedAt); registryRate.Assumption != domain.AssumptionRegistryRate {
		t.Fatalf("the offer prices an image pull at %+v, and nothing has measured this node's link to a registry", registryRate)
	}
}

// pullSecret and readLocation are the two things a machine may present and the
// control plane's record must never hold: the password behind a private
// registry, and a presigned GET, which is a bearer credential written as a URL.
const (
	pullSecret   = "registry-password-nobody-may-keep"
	readLocation = "https://objects.test/bucket/corpus?X-Amz-Signature=deadbeef"
)

// TestTheRecordOfAPullHoldsWhatWasAuthorisedAndNotTheMaterial is the line
// between a durable desire and a minted credential. A node command is written
// down so a machine that was disconnected still receives it, and node_operations
// is kept for the life of the deployment and pruned by nothing, so material
// written there outlives its own fifteen minute window by years in a file an
// operator backs up. What the record holds instead is the bound: which pull was
// authorised, for whom, and until when, which is presentable to nobody.
func TestTheRecordOfAPullHoldsWhatWasAuthorisedAndNotTheMaterial(t *testing.T) {
	store := node.NewMemoryStore()
	registry, clock := newRegistryOn(t, store)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	session := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)

	if _, err := registry.PrepareImage(context.Background(),
		privatePullCommand(bootstrap, enrollment, "op-prepare-1", clock.Now())); err != nil {
		t.Fatalf("prepare the private image: %v", err)
	}

	delivered := receiveCommand(t, session)
	if !strings.Contains(string(delivered.Payload), pullSecret) {
		t.Fatal("the machine was handed a pull it cannot present, so nothing it was told to fetch could be fetched")
	}
	recorded := onlyPendingOperation(t, store)
	if strings.Contains(string(recorded.Payload), pullSecret) {
		t.Fatalf("the registry password was written into the durable record of the command: %s", recorded.Payload)
	}
	if !strings.Contains(string(recorded.Payload), "op-prepare-1") {
		t.Fatalf("the record states nothing about which pull was authorised: %s", recorded.Payload)
	}
}

// TestTheRecordOfAnArtifactFetchHoldsNoSignedRead is the same rule for the other
// fetch. The durable location the catalog states is a name for content and stays
// in the record; the signed one beside it is a working read of the object and
// does not.
func TestTheRecordOfAnArtifactFetchHoldsNoSignedRead(t *testing.T) {
	store := node.NewMemoryStore()
	registry, clock := newRegistryOn(t, store)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	session := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)

	if _, err := registry.PrepareArtifact(context.Background(),
		artifactFetchCommand(bootstrap, enrollment, "op-fetch-1", clock.Now())); err != nil {
		t.Fatalf("prepare the Artifact: %v", err)
	}

	delivered := receiveCommand(t, session)
	if !strings.Contains(string(delivered.Payload), readLocation) {
		t.Fatal("the machine was handed no read, so the object store would refuse the fetch it was told to make")
	}
	recorded := onlyPendingOperation(t, store)
	if strings.Contains(string(recorded.Payload), "X-Amz-Signature") {
		t.Fatalf("a signed read of the object store was written into the durable record: %s", recorded.Payload)
	}
	if !strings.Contains(string(recorded.Payload), "objects://corpus/v3") {
		t.Fatalf("the record states nothing about which version was authorised: %s", recorded.Payload)
	}
}

// TestACommandReplayedOnALaterSessionCarriesNoMaterial states what the two cases
// above cost, so nobody reads them as free. A command is durable and the
// credential inside it is not, so an agent that was down when the sweep issued
// one is handed the record rather than the pull. The machine refuses that rather
// than presenting an empty password to a registry, and the fetch is asked for
// again; what must never happen is the alternative, which is the material
// sitting in the database until somebody replays it.
func TestACommandReplayedOnALaterSessionCarriesNoMaterial(t *testing.T) {
	store := node.NewMemoryStore()
	registry, clock := newRegistryOn(t, store)
	bootstrap := invite(t, registry)
	enrollment := enroll(t, registry, bootstrap)
	first := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)
	if _, err := registry.PrepareImage(context.Background(),
		privatePullCommand(bootstrap, enrollment, "op-prepare-1", clock.Now())); err != nil {
		t.Fatalf("prepare the private image: %v", err)
	}
	receiveCommand(t, first)
	registry.CloseSession(first)

	second := openSession(t, registry, bootstrap.NodeID, enrollment.SessionToken)

	replayed := receiveCommand(t, second)
	if strings.Contains(string(replayed.Payload), pullSecret) {
		t.Fatal("a command replayed on a later session carried material the record was supposed not to hold")
	}
}

func newRegistryOn(t *testing.T, store node.Store) (*node.Registry, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	registry := node.NewRegistry(
		store,
		node.NewSigner(node.DeriveKey([]byte("test-master-key"))),
		"https://mercator.test",
		node.WithClock(clock.Now),
	)
	return registry, clock
}

func onlyPendingOperation(t *testing.T, store node.Store) node.Operation {
	t.Helper()
	pending, err := store.PendingOperations(context.Background(), testWorkspace, testNode)
	if err != nil {
		t.Fatalf("read the durable record of what the node was told: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("the record holds %d pending operations, want the one that was dispatched", len(pending))
	}
	return pending[0]
}

func privatePullCommand(
	bootstrap capability.NodeBootstrap,
	enrollment capability.Enrollment,
	operationID string,
	now time.Time,
) capability.PrepareImageCommand {
	command := prepareCommand(bootstrap, enrollment, operationID)
	command.Reference = "registry.test/analyst@" + command.ManifestDigest
	command.RegistryCredential = domain.RegistryPull{
		ContentCredentialScope: domain.ContentCredentialScope{
			Operation:   operationID,
			WorkspaceID: testWorkspace,
			Content:     command.ManifestDigest,
			ExpiresAt:   now.Add(15 * time.Minute),
		},
		Registry: "registry.test",
		Username: "mercator",
		Secret:   pullSecret,
	}
	return command
}

func artifactFetchCommand(
	bootstrap capability.NodeBootstrap,
	enrollment capability.Enrollment,
	operationID string,
	now time.Time,
) capability.PrepareArtifactCommand {
	command := capability.PrepareArtifactCommand{
		ArtifactID:    "artifact:corpus:v3",
		ContentDigest: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		Source:        "objects://corpus/v3",
		SourceCredential: domain.ArtifactRead{
			ContentCredentialScope: domain.ContentCredentialScope{
				Operation:   operationID,
				WorkspaceID: testWorkspace,
				Content:     "artifact:corpus:v3",
				ExpiresAt:   now.Add(15 * time.Minute),
			},
			Location: readLocation,
		},
		SizeBytes: 4096,
	}
	command.NodeRef = nodeRef(bootstrap)
	command.OperationID = operationID
	command.FencingToken = enrollment.FencingToken
	return command
}
