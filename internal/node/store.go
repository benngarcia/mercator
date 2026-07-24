package node

import (
	"context"
	"errors"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
)

// ErrNotFound is returned for a node identity the control plane never invited.
var ErrNotFound = errors.New("node: not found")

// ErrEnrollmentSpent is returned when an invitation has already been redeemed.
// An agent that lost its session credential needs a fresh invitation rather
// than a replay of the old one.
var ErrEnrollmentSpent = errors.New("node: enrollment token already redeemed")

// ErrFenced is returned when a command carries a superseded fencing token. It
// is the durable half of the fencing guarantee: even if a partitioned session
// reaches the control plane, its work is refused rather than applied late.
var ErrFenced = errors.New("node: fencing token superseded")

// Store is the durable half of the node registry. Everything a restart must
// not forget lives here: who a node is, what it has been told, and what it
// reported back. Session channels are deliberately not in this interface,
// because a session cannot survive a restart and pretending otherwise is how
// duplicate launches happen.
type Store interface {
	// Invite records a node identity before any machine exists to fill it, so
	// an accepted-but-lost provision is still reconcilable.
	Invite(ctx context.Context, record Record) error
	Get(ctx context.Context, workspaceID, nodeID string) (Record, error)
	// Find resolves a node by identity alone. An enrolling machine knows which
	// node it is and nothing else, so enrollment cannot be workspace-scoped.
	Find(ctx context.Context, nodeID string) (Record, error)
	List(ctx context.Context, workspaceID string) ([]Record, error)
	// Enroll redeems an invitation exactly once, bumping the fencing token and
	// opening a lease. Redeeming a spent invitation returns ErrEnrollmentSpent.
	Enroll(ctx context.Context, workspaceID, nodeID string, enrollment Enrollment) (Record, error)
	// Reinvite replaces an existing identity's redeemable invitation without
	// disturbing its current enrollment.
	Reinvite(ctx context.Context, workspaceID, nodeID, enrollmentTokenID string, expires time.Time) error
	// Heartbeat renews a lease and stores the node's latest facts.
	Heartbeat(ctx context.Context, workspaceID, nodeID string, facts capability.NodeFacts, leaseExpires time.Time) (Record, error)
	// RecordEvent stores one node-authored fact, reporting false when this
	// event ID was already recorded so a replayed spool changes nothing.
	RecordEvent(ctx context.Context, event Event) (bool, error)
	// LatestWorkload returns the node's most recent observation of one
	// workload, and whether the node has ever reported it.
	LatestWorkload(ctx context.Context, workspaceID, nodeID, runID, attemptID string) (capability.WorkloadObservation, bool, error)
	// Workloads returns the node's latest observation of every workload it has
	// reported, which is what reconciliation compares against.
	Workloads(ctx context.Context, workspaceID, nodeID string) ([]capability.WorkloadObservation, error)
	// AppendOperation records one command durably, reporting true when this
	// operation ID was already recorded.
	AppendOperation(ctx context.Context, operation Operation) (Operation, bool, error)
	// SettleOperation records what the node did with one command.
	SettleOperation(ctx context.Context, workspaceID, nodeID string, result Result) error
	// PendingOperations returns commands the node has not acknowledged, oldest
	// first. They are redelivered when a node reconnects.
	PendingOperations(ctx context.Context, workspaceID, nodeID string) ([]Operation, error)
	// AppliedOperationIDs returns every operation the node acknowledged, which
	// is what tells a restarted control plane not to act twice.
	AppliedOperationIDs(ctx context.Context, workspaceID, nodeID string) ([]string, error)
	// ExpireLeases marks every node whose lease elapsed as lost and returns
	// them, so a caller can reconcile what those nodes were running.
	ExpireLeases(ctx context.Context, now time.Time) ([]Record, error)
}

// Enrollment is the durable outcome of redeeming one invitation.
type Enrollment struct {
	EnrollmentTokenID string
	AgentVersion      string
	Facts             capability.NodeFacts
	EnrolledAt        time.Time
	LeaseExpires      time.Time
}
