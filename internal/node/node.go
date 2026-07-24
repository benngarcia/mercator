// Package node is the control plane's authority over enrolled Mercator nodes.
//
// A Node is the runtime Mercator controls on one Rental generation. It is what
// makes capacity reusable: without a Node there is no host runtime to hand a
// second workload to. This package owns node identity, leases and fencing,
// command delivery and deduplication, and the reconciliation a node performs
// after either side restarts. It implements capability.NodeRuntime, so the rest
// of the control plane talks to nodes through the same contract a simulated
// node satisfies in the Lab.
//
// Nodes connect outbound. Nothing here dials a node, and no node exposes a
// listener or a Docker socket.
package node

import (
	"fmt"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
)

// State is what the control plane believes about a node right now.
type State string

const (
	// StateEnrolling is an identity that has been invited but has not yet
	// redeemed its enrollment token.
	StateEnrolling State = "enrolling"
	// StateReady is an enrolled node inside its lease.
	StateReady State = "ready"
	// StateLost is an enrolled node whose lease expired without a heartbeat.
	// Its workloads are unobserved, not dead: only the node or the provider can
	// say what actually happened.
	StateLost State = "lost"
	// StateRetired is a node whose Rental generation is over. It can never
	// enroll again under the same generation.
	StateRetired State = "retired"
)

func (state State) Valid() bool {
	switch state {
	case StateEnrolling, StateReady, StateLost, StateRetired:
		return true
	default:
		return false
	}
}

// Record is one node's durable identity and latest reported facts. Identity is
// immutable: a node does not choose its ID, and it cannot claim a generation it
// was not invited for.
type Record struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	RentalID    string `json:"rental_id"`
	// Generation is the Rental lifecycle cycle this node belongs to. Capacity
	// that stops and resumes comes back as a new generation, so a node from the
	// previous one can never be mistaken for the current runtime.
	Generation uint64 `json:"generation"`
	State      State  `json:"state"`
	// FencingToken increases on every enrollment. A command carrying a lower
	// token is refused, which is what stops a partitioned old session from
	// acting after a new one took over.
	FencingToken uint64 `json:"fencing_token"`
	// EnrollmentTokenID identifies the invitation this node may redeem, exactly
	// once.
	EnrollmentTokenID string               `json:"enrollment_token_id"`
	EnrollmentExpires time.Time            `json:"enrollment_expires"`
	EnrolledAt        time.Time            `json:"enrolled_at,omitzero"`
	LeaseExpires      time.Time            `json:"lease_expires,omitzero"`
	LastHeartbeatAt   time.Time            `json:"last_heartbeat_at,omitzero"`
	AgentVersion      string               `json:"agent_version,omitempty"`
	Facts             capability.NodeFacts `json:"facts"`
}

// Ref is this record's address in the capability contract.
func (record Record) Ref() capability.NodeRef {
	return capability.NodeRef{
		WorkspaceID: record.WorkspaceID,
		NodeID:      record.ID,
		RentalID:    record.RentalID,
		Generation:  record.Generation,
	}
}

// Alive reports whether the control plane still believes this node at now.
// An expired lease is not a death certificate: it means Mercator has stopped
// hearing from the node and must reconcile rather than assume.
func (record Record) Alive(now time.Time) bool {
	return record.State == StateReady && now.Before(record.LeaseExpires)
}

// CommandKind names one instruction a node can be asked to perform. The wire
// carries the kind beside an opaque payload so the transport stays narrow
// enough for a second runtime implementation to reuse.
type CommandKind string

const (
	CommandPrepareImage    CommandKind = "prepare_image"
	CommandPrepareArtifact CommandKind = "prepare_artifact"
	CommandLaunchWorkload  CommandKind = "launch_workload"
	CommandStopWorkload    CommandKind = "stop_workload"
)

func (kind CommandKind) Valid() bool {
	switch kind {
	case CommandPrepareImage, CommandPrepareArtifact, CommandLaunchWorkload, CommandStopWorkload:
		return true
	default:
		return false
	}
}

// Command is one instruction in flight to a node. OperationID makes it
// idempotent: however many times it is delivered, the node applies it once and
// reports Duplicate afterwards.
type Command struct {
	OperationID  string      `json:"operation_id"`
	NodeID       string      `json:"node_id"`
	Kind         CommandKind `json:"kind"`
	FencingToken uint64      `json:"fencing_token"`
	IssuedAt     time.Time   `json:"issued_at"`
	// Payload is the typed command from the capability contract, encoded for
	// the wire.
	Payload []byte `json:"payload"`
}

// OperationState is how far one command has travelled.
type OperationState string

const (
	// OperationPending is recorded and not yet acknowledged by the node. It
	// survives a control-plane restart, which is what lets the node be told
	// again rather than the work being lost.
	OperationPending OperationState = "pending"
	// OperationApplied is acknowledged by the node with a result.
	OperationApplied OperationState = "applied"
	// OperationRefused is a command the node declined, most often because its
	// fencing token was superseded.
	OperationRefused OperationState = "refused"
)

// Operation is the durable record of one command and what became of it.
type Operation struct {
	OperationID  string         `json:"operation_id"`
	NodeID       string         `json:"node_id"`
	WorkspaceID  string         `json:"workspace_id"`
	Kind         CommandKind    `json:"kind"`
	FencingToken uint64         `json:"fencing_token"`
	State        OperationState `json:"state"`
	IssuedAt     time.Time      `json:"issued_at"`
	SettledAt    time.Time      `json:"settled_at,omitzero"`
	Payload      []byte         `json:"payload"`
	// Failure is the node's own explanation when the operation did not succeed.
	// It never carries credential material.
	Failure string `json:"failure,omitempty"`
}

// Result is what a node reports back about one operation.
type Result struct {
	OperationID string `json:"operation_id"`
	// Applied is false only when the node refused the command outright.
	Applied bool   `json:"applied"`
	Failure string `json:"failure,omitempty"`
	// Duplicate reports that the node had already applied this operation,
	// which is the answer that makes retry after a lost response safe.
	Duplicate  bool      `json:"duplicate"`
	ReportedAt time.Time `json:"reported_at"`
}

// Event is one fact a node reports on its own authority: its liveness and
// inventory, or a container's lifecycle. Application semantics arrive
// separately, through the run's own reporting path.
type Event struct {
	// ID deduplicates a spooled event replayed after a reconnection.
	ID          string                          `json:"id"`
	NodeID      string                          `json:"node_id"`
	WorkspaceID string                          `json:"workspace_id"`
	Kind        EventKind                       `json:"kind"`
	ObservedAt  time.Time                       `json:"observed_at"`
	Facts       *capability.NodeFacts           `json:"facts,omitempty"`
	Workload    *capability.WorkloadObservation `json:"workload,omitempty"`
}

// EventKind names what a node is reporting.
type EventKind string

const (
	// EventHeartbeat carries the node's current host and inventory facts and
	// renews its lease.
	EventHeartbeat EventKind = "heartbeat"
	// EventWorkload carries a container lifecycle transition the node observed
	// directly, independently of anything the application reports.
	EventWorkload EventKind = "workload"
)

func (event Event) Validate() error {
	switch {
	case event.ID == "":
		return fmt.Errorf("node event needs an ID to deduplicate a replayed spool")
	case event.NodeID == "":
		return fmt.Errorf("node event needs a node ID")
	case event.ObservedAt.IsZero():
		return fmt.Errorf("node event needs an observation time")
	case event.Kind == EventHeartbeat && event.Facts == nil:
		return fmt.Errorf("a heartbeat carries the node's facts")
	case event.Kind == EventWorkload && event.Workload == nil:
		return fmt.Errorf("a workload event carries the container observation")
	case event.Kind != EventHeartbeat && event.Kind != EventWorkload:
		return fmt.Errorf("unknown node event kind %q", event.Kind)
	default:
		return nil
	}
}
