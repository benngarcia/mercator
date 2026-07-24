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
	workspaceID = "ws_conformance"
	nodeID      = "nod_conformance"
	rentalID    = "rnt_conformance"
)

var start = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// RunStoreSuite exercises every promise the node registry relies on.
func RunStoreSuite(t *testing.T, newStore NewStore) {
	t.Helper()
	t.Run("an invited identity is readable before any machine enrolls", func(t *testing.T) {
		store := invited(t, newStore)

		record, err := store.Get(context.Background(), workspaceID, nodeID)
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

	t.Run("an identity resolves without knowing its workspace", func(t *testing.T) {
		store := invited(t, newStore)

		record, err := store.Find(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("find node: %v", err)
		}

		if record.WorkspaceID != workspaceID {
			t.Fatalf("workspace = %q, want %q", record.WorkspaceID, workspaceID)
		}
	})

	t.Run("enrollment spends the invitation and raises the fencing token", func(t *testing.T) {
		store := invited(t, newStore)

		enrolled := mustEnroll(t, store, "token-1")

		if enrolled.FencingToken != 1 {
			t.Fatalf("fencing token = %d, want 1", enrolled.FencingToken)
		}
		if _, err := store.Enroll(context.Background(), workspaceID, nodeID, enrollment("token-1")); !errors.Is(err, node.ErrEnrollmentSpent) {
			t.Fatalf("redeeming a spent invitation = %v, want ErrEnrollmentSpent", err)
		}
	})

	t.Run("a reinvitation is redeemable and raises the fencing token again", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")

		if err := store.Reinvite(context.Background(), workspaceID, nodeID, "token-2", start.Add(time.Hour)); err != nil {
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

	t.Run("an unacknowledged operation stays pending and an applied one does not", func(t *testing.T) {
		store := invited(t, newStore)
		mustEnroll(t, store, "token-1")
		for _, id := range []string{"op-applied", "op-pending"} {
			if _, _, err := store.AppendOperation(context.Background(), operation(id)); err != nil {
				t.Fatalf("append %s: %v", id, err)
			}
		}

		if err := store.SettleOperation(context.Background(), workspaceID, nodeID, node.Result{
			OperationID: "op-applied", Applied: true, ReportedAt: start,
		}); err != nil {
			t.Fatalf("settle operation: %v", err)
		}

		pending, err := store.PendingOperations(context.Background(), workspaceID, nodeID)
		if err != nil {
			t.Fatalf("pending operations: %v", err)
		}
		if len(pending) != 1 || pending[0].OperationID != "op-pending" {
			t.Fatalf("pending = %+v, want only the unacknowledged operation", pending)
		}
		applied, err := store.AppliedOperationIDs(context.Background(), workspaceID, nodeID)
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
		if err := store.SettleOperation(context.Background(), workspaceID, nodeID, result); err != nil {
			t.Fatalf("first settle: %v", err)
		}

		err := store.SettleOperation(context.Background(), workspaceID, nodeID, result)

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
		observation, found, err := store.LatestWorkload(context.Background(), workspaceID, nodeID, "run-1", "attempt-1")
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

		observation, _, err := store.LatestWorkload(context.Background(), workspaceID, nodeID, "run-1", "attempt-1")
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

		if _, err := store.Heartbeat(context.Background(), workspaceID, nodeID, capability.NodeFacts{
			ObservedAt: start.Add(time.Minute),
			Host:       capability.HostFacts{OS: "linux", ContainerRuntime: "docker", DiskFreeBytes: 42},
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
		record, err := store.Get(context.Background(), workspaceID, nodeID)
		if err != nil {
			t.Fatalf("get node: %v", err)
		}
		if record.Facts.Host.DiskFreeBytes != 42 {
			t.Fatalf("facts were not replaced: %+v", record.Facts.Host)
		}
	})

	t.Run("an unknown node is not found rather than empty", func(t *testing.T) {
		store := newStore(t)

		_, err := store.Get(context.Background(), workspaceID, "nod_missing")

		if !errors.Is(err, node.ErrNotFound) {
			t.Fatalf("get unknown node = %v, want ErrNotFound", err)
		}
	})
}

func invited(t *testing.T, newStore NewStore) node.Store {
	t.Helper()
	store := newStore(t)
	if err := store.Invite(context.Background(), node.Record{
		ID:                nodeID,
		WorkspaceID:       workspaceID,
		RentalID:          rentalID,
		Generation:        1,
		State:             node.StateEnrolling,
		EnrollmentTokenID: "token-1",
		EnrollmentExpires: start.Add(time.Hour),
	}); err != nil {
		t.Fatalf("invite node: %v", err)
	}
	return store
}

func mustEnroll(t *testing.T, store node.Store, tokenID string) node.Record {
	t.Helper()
	record, err := store.Enroll(context.Background(), workspaceID, nodeID, enrollment(tokenID))
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
		OperationID:  operationID,
		NodeID:       nodeID,
		WorkspaceID:  workspaceID,
		Kind:         node.CommandLaunchWorkload,
		FencingToken: 1,
		State:        node.OperationPending,
		IssuedAt:     start,
		Payload:      []byte(`{"run_id":"run-1"}`),
	}
}

func workloadEvent(eventID string, phase capability.WorkloadPhase, observedAt time.Time) node.Event {
	return node.Event{
		ID:          eventID,
		NodeID:      nodeID,
		WorkspaceID: workspaceID,
		Kind:        node.EventWorkload,
		ObservedAt:  observedAt,
		Workload: &capability.WorkloadObservation{
			RunID:      "run-1",
			AttemptID:  "attempt-1",
			Phase:      phase,
			ObservedAt: observedAt,
		},
	}
}
