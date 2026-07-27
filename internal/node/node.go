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
	"github.com/benngarcia/mercator/internal/domain"
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
	// ShadowPriceUSDPerHour is what holding this machine costs, as the operator
	// configured it. Placement needs a price to weigh a node against fresh
	// capacity; a node with none has unknown pricing and is refused rather than
	// treated as free.
	ShadowPriceUSDPerHour float64 `json:"shadow_price_usd_per_hour,omitempty"`
	// BillingIntervalSeconds is the block of time this machine is bought in, as
	// its operator states it. It is what makes an hour Mercator has committed to
	// an hour it pays for whether or not a Run uses the rest of it: work that
	// runs past the end of one interval commits Mercator to the next whole one,
	// and the part of that nothing uses is charged to the placement that bought
	// it rather than to nobody.
	//
	// Zero is a machine bought in no increments at all, which is the operator's own
	// hardware: Mercator holds it continuously, so no second of it is a fresh
	// commitment and there is no tail to charge. It is not a fallback for an
	// operator who did not answer, it is the answer, and it is the same silence
	// PriceModel.GranularitySeconds already means.
	BillingIntervalSeconds int64 `json:"billing_interval_seconds,omitempty"`
	// EligibleClasses is the kinds of work this machine may be used for, as its
	// operator reserved it. Empty is a machine held for nobody in particular.
	// Placement refuses every other class outright rather than pricing it, because
	// a reservation is a statement about what the machine is for and no amount of
	// waiting changes it.
	EligibleClasses []domain.ServiceClass `json:"eligible_classes,omitempty"`
	// AvailableUntil is the moment this machine stops being Mercator's to use, as
	// its operator declared. Zero is a machine with no such moment. It is a window
	// somebody stated rather than capacity that can vanish without notice, so work
	// that could still be running then is refused before it starts and work that
	// finishes inside it is never at risk.
	AvailableUntil time.Time `json:"available_until,omitzero"`
}

// Terms is what this machine was sold to Mercator on, as the offer built from
// this record publishes them. They are derived from the operator's own
// configuration and from the moment Mercator started paying, because a billing
// interval is a repeating block anchored to the beginning of the lease and
// nothing else in the record can say where the current one ends.
func (record Record) Terms(now time.Time) domain.CapacityTerms {
	return domain.CapacityTerms{
		CommittedUntil:  record.CommittedUntil(now),
		EligibleClasses: record.EligibleClasses,
		AvailableUntil:  record.AvailableUntil,
	}
}

// CommittedUntil is the end of the billing interval this machine is inside right
// now, and nothing for a machine bought in no increments: Mercator holds such a
// machine continuously, so there is no interval whose end is a decision.
//
// The intervals are counted from enrollment, because that is the moment Mercator
// started paying for this generation of this machine. A node that has not
// enrolled is not being paid for and is not offered either.
func (record Record) CommittedUntil(now time.Time) time.Time {
	if record.BillingIntervalSeconds <= 0 || record.EnrolledAt.IsZero() {
		return time.Time{}
	}
	interval := time.Duration(record.BillingIntervalSeconds) * time.Second
	elapsed := now.Sub(record.EnrolledAt)
	if elapsed < 0 {
		elapsed = 0
	}
	return record.EnrolledAt.Add((elapsed/interval + 1) * interval)
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

// DiskReport is what Mercator can say about one node's disk, which is three
// answers and not two. Each sends an operator somewhere different, and a
// boolean can only carry two, so the third gets told as one of the others.
type DiskReport string

const (
	// DiskNeverReported is an identity nobody has heard from: invited and not
	// yet enrolled, or enrolled by an agent whose first heartbeat has not
	// landed. Nothing has been measured because nothing has been asked, and
	// saying "this node could not measure its disk" about a machine that has
	// never spoken states a fact about a daemon Mercator has never seen.
	DiskNeverReported DiskReport = "never_reported"
	// DiskUnmeasurable is a node that reported and could not measure: its
	// daemon keeps content somewhere its agent cannot see. It stays in the
	// fleet, it reports its containers and their exits, and it wins no
	// placement that declares a disk floor.
	DiskUnmeasurable DiskReport = "unmeasurable"
	// DiskMeasured is a number this node established. Zero free bytes is a full
	// machine, which is a thing to go and clear out rather than a thing nobody
	// could read.
	DiskMeasured DiskReport = "measured"
)

// Disk is which of the three this node's latest facts amount to. A node states
// its own observation time on every report, which is what separates a machine
// that has said nothing from one that answered.
func (record Record) Disk() DiskReport {
	switch {
	case record.Facts.ObservedAt.IsZero():
		return DiskNeverReported
	case !record.Facts.Host.Disk.Known:
		return DiskUnmeasurable
	default:
		return DiskMeasured
	}
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

// MayLeaveEffectOnFailure reports whether a command that returned an error
// might still have changed the machine. Launching and stopping a workload
// might: the container may exist, or may already have been signalled, and doing
// either twice is worse than not retrying. Preparing content cannot: a failed
// pull or fetch leaves nothing, and it is idempotent, so retrying is both safe
// and necessary.
func (kind CommandKind) MayLeaveEffectOnFailure() bool {
	return kind == CommandLaunchWorkload || kind == CommandStopWorkload
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

// receive stamps the moment the control plane accepted this report onto the
// workload observation it carries, replacing whatever a node put there: the stamp
// is Mercator's own clock and a node has no standing to state it. The observation
// is copied rather than written through, so the sender's own record of what it
// reported stays what it reported.
//
// A heartbeat needs no stamp. The registry dates the lease it renews with its own
// clock already, and no rule compares a fact against Mercator's frame.
func (event *Event) receive(at time.Time) {
	if event.Workload == nil {
		return
	}
	received := *event.Workload
	received.ReceivedAt = at
	event.Workload = &received
}

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
