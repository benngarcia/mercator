// Package nodetest is the shared conformance suite every node.Store must pass.
//
// The in-memory store and the SQLite store make the same promises, and the
// promises are the reason the reusable lane is safe: an operation applied once
// stays applied, an event recorded once changes nothing when replayed, and a
// control plane that restarts can still tell what a node already did. Running
// one suite against both is what keeps those promises from drifting apart.
package nodetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
)

// NewStore builds one empty store for a single case.
type NewStore func(t *testing.T) node.Store

const (
	nodeID   = "nod_conformance"
	rentalID = "rnt_conformance"
)

var start = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// RunStoreSuite exercises every promise the node registry relies on.
func RunStoreSuite(t *testing.T, newStore NewStore) {
	t.Helper()
	t.Run("an invited identity is readable before any machine enrolls", func(t *testing.T) {
		store := invited(t, newStore)

		record, err := store.Get(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("get invited node: %v", err)
		}

		if record.State != node.StateEnrolling {
			t.Fatalf("state = %q, want %q", record.State, node.StateEnrolling)
		}
		if record.RentalID != rentalID || record.Generation != 1 {
			t.Fatalf("identity = %q generation %d, want %q generation 1", record.RentalID, record.Generation, rentalID)
		}
	})

	// Provisioning asks for an identity again on every attempt, because nothing
	// can know whether an earlier attempt landed a machine until the provider
	// answers. So the second invitation of one identity is a routine question
	// with one right answer, and a store that reported it as an opaque failure
	// would strand every Rental whose first attempt was ambiguous.
	t.Run("inviting an identity that already exists names the collision", func(t *testing.T) {
		store := invited(t, newStore)

		err := store.Invite(context.Background(), inviteRecord())

		if !errors.Is(err, node.ErrIdentityExists) {
			t.Fatalf("inviting %q twice = %v, want ErrIdentityExists", nodeID, err)
		}
	})

	t.Run("an identity resolves directly", func(t *testing.T) {
		store := invited(t, newStore)

		record, err := store.Find(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("find node: %v", err)
		}
		if record.ID != nodeID {
			t.Fatalf("node = %q, want %q", record.ID, nodeID)
		}
	})

	t.Run("enrollment spends the invitation and raises the fencing token", func(t *testing.T) {
		store := invited(t, newStore)

		enrolled := mustEnroll(t, store, "token-1")

		if enrolled.FencingToken != 1 {
			t.Fatalf("fencing token = %d, want 1", enrolled.FencingToken)
		}
		if _, err := store.Enroll(context.Background(), nodeID, enrollment("token-1")); !errors.Is(err, node.ErrEnrollmentSpent) {
			t.Fatalf("redeeming a spent invitation = %v, want ErrEnrollmentSpent", err)
		}
	})

	t.Run("a reinvitation is redeemable and raises the fencing token again", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")

		if err := store.Reinvite(context.Background(), nodeID, "token-2", start.Add(time.Hour)); err != nil {
			t.Fatalf("reinvite: %v", err)
		}
		second := mustEnroll(t, store, "token-2")

		if second.FencingToken != 2 {
			t.Fatalf("fencing token = %d, want 2", second.FencingToken)
		}
	})

	t.Run("a repeated operation ID is reported as already recorded", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")
		if _, _, err := store.AppendOperation(context.Background(), operation("op-1")); err != nil {
			t.Fatalf("append operation: %v", err)
		}

		stored, duplicate, err := store.AppendOperation(context.Background(), operation("op-1"))
		if err != nil {
			t.Fatalf("append operation again: %v", err)
		}

		if !duplicate {
			t.Fatal("a repeated operation ID must report as already recorded")
		}
		if stored.OperationID != "op-1" {
			t.Fatalf("returned operation = %q, want the recorded one", stored.OperationID)
		}
	})

	t.Run("a refused preparation is asked again rather than answered from the failed record", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")
		if _, _, err := store.AppendOperation(context.Background(), prepareOperation("op-pull")); err != nil {
			t.Fatalf("append preparation: %v", err)
		}
		if err := store.SettleOperation(context.Background(), nodeID, node.Result{
			OperationID: "op-pull", Applied: false, Failure: "pull failed: registry unreachable", ReportedAt: start,
		}); err != nil {
			t.Fatalf("settle refusal: %v", err)
		}

		stored, duplicate, err := store.AppendOperation(context.Background(), prepareOperation("op-pull"))
		if err != nil {
			t.Fatalf("append preparation again: %v", err)
		}

		if duplicate {
			t.Fatal("a preparation the node refused must be askable again, not answered as already recorded")
		}
		if stored.State != node.OperationPending || stored.Failure != "" {
			t.Fatalf("reissued operation = %+v, want a fresh pending command", stored)
		}
		pending, err := store.PendingOperations(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("pending operations: %v", err)
		}
		if len(pending) != 1 || pending[0].OperationID != "op-pull" {
			t.Fatalf("pending = %+v, want the reissued preparation waiting for the node", pending)
		}
	})

	t.Run("a refused launch keeps its identity spent", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")
		if _, _, err := store.AppendOperation(context.Background(), operation("op-1")); err != nil {
			t.Fatalf("append operation: %v", err)
		}
		if err := store.SettleOperation(context.Background(), nodeID, node.Result{
			OperationID: "op-1", Applied: false, Failure: "container create failed", ReportedAt: start,
		}); err != nil {
			t.Fatalf("settle refusal: %v", err)
		}

		_, duplicate, err := store.AppendOperation(context.Background(), operation("op-1"))
		if err != nil {
			t.Fatalf("append operation again: %v", err)
		}

		if !duplicate {
			t.Fatal("a launch that failed may have made the container, so its identity must stay spent")
		}
	})

	t.Run("an unacknowledged operation stays pending and an applied one does not", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")
		for _, id := range []string{"op-applied", "op-pending"} {
			if _, _, err := store.AppendOperation(context.Background(), operation(id)); err != nil {
				t.Fatalf("append %s: %v", id, err)
			}
		}

		if err := store.SettleOperation(context.Background(), nodeID, node.Result{
			OperationID: "op-applied", Applied: true, ReportedAt: start,
		}); err != nil {
			t.Fatalf("settle operation: %v", err)
		}

		pending, err := store.PendingOperations(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("pending operations: %v", err)
		}
		if len(pending) != 1 || pending[0].OperationID != "op-pending" {
			t.Fatalf("pending = %+v, want only the unacknowledged operation", pending)
		}
		applied, err := store.AppliedOperationIDs(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("applied operations: %v", err)
		}
		if len(applied) != 1 || applied[0] != "op-applied" {
			t.Fatalf("applied = %v, want only the acknowledged operation", applied)
		}
	})

	t.Run("settling one operation twice is accepted rather than an error", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")
		if _, _, err := store.AppendOperation(context.Background(), operation("op-1")); err != nil {
			t.Fatalf("append operation: %v", err)
		}
		result := node.Result{OperationID: "op-1", Applied: true, ReportedAt: start}
		if err := store.SettleOperation(context.Background(), nodeID, result); err != nil {
			t.Fatalf("first settle: %v", err)
		}

		err := store.SettleOperation(context.Background(), nodeID, result)

		if err != nil {
			t.Fatalf("a node reporting twice after a lost response must not be an error: %v", err)
		}
	})

	t.Run("a replayed event changes nothing", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")
		exit := workloadEvent("evt-exit", capability.WorkloadPhaseExited, start.Add(time.Minute))

		first, err := store.RecordEvent(context.Background(), exit)
		if err != nil {
			t.Fatalf("record event: %v", err)
		}
		second, err := store.RecordEvent(context.Background(), exit)
		if err != nil {
			t.Fatalf("replay event: %v", err)
		}

		if !first || second {
			t.Fatalf("record = %v, replay = %v; want the replay to be recognized", first, second)
		}
		observation, found, err := store.LatestWorkload(context.Background(), nodeID, "run-1", "attempt-1")
		if err != nil || !found {
			t.Fatalf("latest workload: found=%v err=%v", found, err)
		}
		if observation.Phase != capability.WorkloadPhaseExited {
			t.Fatalf("phase = %q, want %q", observation.Phase, capability.WorkloadPhaseExited)
		}
	})

	t.Run("an out-of-order workload event does not undo a later observation", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")
		if _, err := store.RecordEvent(context.Background(), workloadEvent("evt-exit", capability.WorkloadPhaseExited, start.Add(time.Minute))); err != nil {
			t.Fatalf("record exit: %v", err)
		}

		if _, err := store.RecordEvent(context.Background(), workloadEvent("evt-running", capability.WorkloadPhaseRunning, start)); err != nil {
			t.Fatalf("record stale running: %v", err)
		}

		observation, _, err := store.LatestWorkload(context.Background(), nodeID, "run-1", "attempt-1")
		if err != nil {
			t.Fatalf("latest workload: %v", err)
		}
		if observation.Phase != capability.WorkloadPhaseExited {
			t.Fatalf("phase = %q, want the exit to survive a late-arriving earlier observation", observation.Phase)
		}
	})

	t.Run("a lease that elapsed marks the node lost exactly once", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")

		expired, err := store.ExpireLeases(context.Background(), start.Add(2*time.Hour))
		if err != nil {
			t.Fatalf("expire leases: %v", err)
		}
		again, err := store.ExpireLeases(context.Background(), start.Add(3*time.Hour))
		if err != nil {
			t.Fatalf("expire leases again: %v", err)
		}

		if len(expired) != 1 || expired[0].State != node.StateLost {
			t.Fatalf("first expiry = %+v, want one lost node", expired)
		}
		if len(again) != 0 {
			t.Fatalf("second expiry = %+v, want nothing left to expire", again)
		}
	})

	t.Run("a heartbeat renews the lease and replaces the facts", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")

		if _, err := store.Heartbeat(context.Background(), nodeID, capability.NodeFacts{
			ObservedAt: start.Add(time.Minute),
			Host:       capability.HostFacts{OS: "linux", ContainerRuntime: "docker", Disk: capability.DiskFacts{Known: true, FreeBytes: 42}},
		}, start.Add(2*time.Hour)); err != nil {
			t.Fatalf("heartbeat: %v", err)
		}

		expired, err := store.ExpireLeases(context.Background(), start.Add(time.Hour))
		if err != nil {
			t.Fatalf("expire leases: %v", err)
		}
		if len(expired) != 0 {
			t.Fatalf("a heartbeating node must keep its lease, got %+v", expired)
		}
		record, err := store.Get(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("get node: %v", err)
		}
		if record.Facts.Host.Disk.FreeBytes != 42 {
			t.Fatalf("facts were not replaced: %+v", record.Facts.Host)
		}
	})

	// The three cases below are one promise read from three sides: the generation
	// this identity was invited for is over, so nothing brings the machine back
	// into the fleet. It is the promise the Rental lifecycle rests on, because
	// ending a generation is how Mercator gives a machine up and the record is the
	// only thing that stops the agent on it being answered as though it had not.
	t.Run("a retired node can never enroll again", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")
		if err := store.Reinvite(context.Background(), nodeID, "token-2", start.Add(time.Hour)); err != nil {
			t.Fatalf("reinvite: %v", err)
		}
		if err := store.Retire(context.Background(), nodeID); err != nil {
			t.Fatalf("retire node: %v", err)
		}

		_, err := store.Enroll(context.Background(), nodeID, enrollment("token-2"))

		if !errors.Is(err, node.ErrRetired) {
			t.Fatalf("enrolling a retired node = %v, want ErrRetired", err)
		}
		record, err := store.Get(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("get retired node: %v", err)
		}
		if record.State != node.StateRetired {
			t.Fatalf("state = %q, want %q", record.State, node.StateRetired)
		}
	})

	t.Run("a retired node renews no lease with its next heartbeat", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")
		if err := store.Retire(context.Background(), nodeID); err != nil {
			t.Fatalf("retire node: %v", err)
		}

		_, err := store.Heartbeat(context.Background(), nodeID, capability.NodeFacts{
			ObservedAt: start.Add(time.Minute),
			Host:       capability.HostFacts{OS: "linux", ContainerRuntime: "docker"},
		}, start.Add(2*time.Hour))

		if !errors.Is(err, node.ErrRetired) {
			t.Fatalf("heartbeat from a retired node = %v, want ErrRetired", err)
		}
		record, err := store.Get(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("get retired node: %v", err)
		}
		if record.State != node.StateRetired {
			t.Fatalf("state = %q, want the node to stay %q", record.State, node.StateRetired)
		}
	})

	t.Run("retiring a retired node changes nothing", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")
		if err := store.Retire(context.Background(), nodeID); err != nil {
			t.Fatalf("retire node: %v", err)
		}

		err := store.Retire(context.Background(), nodeID)

		if err != nil {
			t.Fatalf("retiring a retired node = %v, want a generation's end to be repeatable", err)
		}
	})

	t.Run("an identity nobody invited cannot be retired", func(t *testing.T) {
		store := newStore(t)

		err := store.Retire(context.Background(), "nod_missing")

		if !errors.Is(err, node.ErrNotFound) {
			t.Fatalf("retiring an unknown identity = %v, want ErrNotFound", err)
		}
	})

	t.Run("an unknown node is not found rather than empty", func(t *testing.T) {
		store := newStore(t)

		_, err := store.Get(context.Background(), "nod_missing")

		if !errors.Is(err, node.ErrNotFound) {
			t.Fatalf("get unknown node = %v, want ErrNotFound", err)
		}
	})
}

func invited(t *testing.T, newStore NewStore) node.Store {
	t.Helper()
	store := newStore(t)
	if err := store.Invite(context.Background(), inviteRecord()); err != nil {
		t.Fatalf("invite node: %v", err)
	}
	return store
}

func inviteRecord() node.Record {
	return node.Record{
		ID: nodeID,

		RentalID:          rentalID,
		Generation:        1,
		State:             node.StateEnrolling,
		EnrollmentTokenID: "token-1",
		EnrollmentExpires: start.Add(time.Hour),
	}
}

func mustEnroll(t *testing.T, store node.Store, tokenID string) node.Record {
	t.Helper()
	record, err := store.Enroll(context.Background(), nodeID, enrollment(tokenID))
	if err != nil {
		t.Fatalf("enroll node: %v", err)
	}
	return record
}

func enrollment(tokenID string) node.Enrollment {
	return node.Enrollment{
		EnrollmentTokenID: tokenID,
		AgentVersion:      "test",
		Facts: capability.NodeFacts{
			ObservedAt: start,
			Host:       capability.HostFacts{OS: "linux", ContainerRuntime: "docker"},
		},
		EnrolledAt:   start,
		LeaseExpires: start.Add(time.Minute),
	}
}

func operation(operationID string) node.Operation {
	return node.Operation{
		OperationID: operationID,
		NodeID:      nodeID,

		Kind:         node.CommandLaunchWorkload,
		FencingToken: 1,
		State:        node.OperationPending,
		IssuedAt:     start,
		Payload:      []byte(`{"run_id":"run-1"}`),
	}
}

// prepareOperation is one piece of content a machine is told to fetch. It is a
// separate fixture from a launch because the two answer a refusal differently: a
// failed pull left nothing behind and a failed launch may have made the container.
func prepareOperation(operationID string) node.Operation {
	command := operation(operationID)
	command.Kind = node.CommandPrepareImage
	command.Payload = []byte(`{"reference":"trainer@sha256:aaaa"}`)
	return command
}

func workloadEvent(eventID string, phase capability.WorkloadPhase, observedAt time.Time) node.Event {
	return node.Event{
		ID:     eventID,
		NodeID: nodeID,

		Kind:       node.EventWorkload,
		ObservedAt: observedAt,
		Workload: &capability.WorkloadObservation{
			RunID:      "run-1",
			AttemptID:  "attempt-1",
			Phase:      phase,
			ObservedAt: observedAt,
		},
	}
}
