package node

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
)

// DefaultLease is how long the control plane believes a node absent a
// heartbeat. It is deliberately several heartbeat intervals, so one slow
// network round does not declare a healthy machine lost.
const DefaultLease = 90 * time.Second

// DefaultSession is how long one session credential stays valid. A node renews
// by reconnecting, which is also when it reconciles.
const DefaultSession = 30 * time.Minute

// DefaultInvitation is how long an enrollment token stays redeemable. It bounds
// the window in which a leaked bootstrap could join, and is long enough for a
// slow provider to finish booting a machine.
const DefaultInvitation = 30 * time.Minute

// Registry is the control plane's authority over enrolled nodes. It implements
// capability.NodeRuntime by recording every command durably and delivering it
// over the node's own outbound session, so a command survives a control-plane
// restart and a node that reconnects is told again rather than losing the work.
type Registry struct {
	store  Store
	signer *Signer
	now    func() time.Time
	// controlPlaneURL is what a bootstrapped machine dials outbound. Nothing
	// here ever dials a node.
	controlPlaneURL string
	agentVersion    string

	lease      time.Duration
	session    time.Duration
	invitation time.Duration

	newIdentity func() string

	mu       sync.Mutex
	sessions map[string]*Session
	// draining is set once this control plane has begun shutting down, and it is
	// what makes Drain final. An agent redials every few milliseconds, so a drain
	// that only ended the sessions it happened to find would be raced by the next
	// one and the shutdown it was called for would wait on that instead.
	draining bool
}

// Option configures a Registry.
type Option func(*Registry)

// WithClock replaces the wall clock. The Lab injects a deterministic one so
// lease expiry and token windows are reproducible.
func WithClock(now func() time.Time) Option {
	return func(registry *Registry) { registry.now = now }
}

// WithLease sets how long a node is believed absent a heartbeat.
func WithLease(lease time.Duration) Option {
	return func(registry *Registry) { registry.lease = lease }
}

// WithAgentVersion pins the node agent build a bootstrap asks for.
func WithAgentVersion(version string) Option {
	return func(registry *Registry) { registry.agentVersion = version }
}

func NewRegistry(store Store, signer *Signer, controlPlaneURL string, opts ...Option) *Registry {
	registry := &Registry{
		store:           store,
		signer:          signer,
		now:             time.Now,
		controlPlaneURL: controlPlaneURL,
		agentVersion:    "dev",
		lease:           DefaultLease,
		session:         DefaultSession,
		invitation:      DefaultInvitation,
		sessions:        map[string]*Session{},
	}
	for _, opt := range opts {
		opt(registry)
	}
	return registry
}

// NodeSupport reports what this runtime performs, which is the whole point of a
// negotiated capability set: a declaration is a promise the control plane will
// place work against, so anything declared here that the Docker runtime does not
// do is Placement believing in locality nothing produces. Four of the five are
// earned today: the agent enumerates the images and layers it unpacked and
// reports the platform of each, it attaches and enumerates workspace-scoped
// cache volumes across workloads, it pulls an image it was not asked to launch
// and answers Duplicate on a redelivery, and it replicates an immutable Artifact
// from a location the control plane minted and reports the digest those bytes
// actually hashed to. It still reclaims no disk, and garbage collection becomes
// true in the slice that implements it and not before.
func (registry *Registry) NodeSupport() capability.NodeSupport {
	return capability.NodeSupport{
		ContainerRuntime:       "docker",
		ExactImageInventory:    true,
		ArtifactReplicas:       true,
		CacheMounts:            true,
		Prewarm:                true,
		MaxConcurrentWorkloads: 1,
	}
}

// Invitation is what an operator or a capacity provider states about a machine
// before it exists: which node it will be, which Rental generation it belongs
// to, and what holding it costs.
type Invitation struct {
	WorkspaceID string
	NodeID      string
	RentalID    string
	Generation  uint64
	// ShadowPriceUSDPerHour is what this machine costs to hold. Placement needs
	// a price to weigh a node against fresh capacity, and a node without one is
	// refused rather than treated as free.
	ShadowPriceUSDPerHour float64
	// Purchase is the rest of what this machine is bought on: the block of time it
	// is billed in, the kinds of work its operator holds it for, and the moment it
	// stops being Mercator's.
	Purchase Purchase
}

// Invite reserves a node identity and mints the bootstrap material a machine
// needs to reach the control plane. The identity exists before the machine
// does, so capacity provisioned by a request whose response was lost is still
// reconcilable.
func (registry *Registry) Invite(ctx context.Context, invitation Invitation) (capability.NodeBootstrap, error) {
	nodeID, rentalID, generation := invitation.NodeID, invitation.RentalID, invitation.Generation
	if generation == 0 {
		generation = 1
	}
	if nodeID == "" {
		nodeID = "nod_" + registry.identity()
	}
	if rentalID == "" {
		rentalID = "rnt_" + registry.identity()
	}
	expires := registry.now().UTC().Add(registry.invitation)
	token, err := registry.signer.Enrollment(nodeID, rentalID, generation, expires)
	if err != nil {
		return capability.NodeBootstrap{}, err
	}
	record := Record{
		ID:                    nodeID,
		WorkspaceID:           invitation.WorkspaceID,
		RentalID:              rentalID,
		Generation:            generation,
		State:                 StateEnrolling,
		EnrollmentTokenID:     TokenID(token),
		EnrollmentExpires:     expires,
		ShadowPriceUSDPerHour: invitation.ShadowPriceUSDPerHour,
		Purchase:              invitation.Purchase,
	}
	if err := registry.store.Invite(ctx, record); err != nil {
		return capability.NodeBootstrap{}, err
	}
	return capability.NodeBootstrap{
		ControlPlaneURL: registry.controlPlaneURL,
		NodeID:          nodeID,
		RentalID:        rentalID,
		Generation:      generation,
		EnrollmentToken: token,
		AgentVersion:    registry.agentVersion,
	}, nil
}

// Enroll redeems an invitation for an authenticated session. Identity is not
// negotiable: the request must name the node and generation the invitation was
// minted for, and the invitation is spent by redeeming it.
func (registry *Registry) Enroll(ctx context.Context, request capability.EnrollmentRequest) (capability.Enrollment, error) {
	now := registry.now().UTC()
	record, err := registry.lookupInvited(ctx, request)
	if err != nil {
		return capability.Enrollment{}, err
	}
	if !registry.signer.VerifyEnrollment(request.NodeID, request.RentalID, request.Generation, request.EnrollmentToken, now) {
		return capability.Enrollment{}, fmt.Errorf("node: enrollment token is not valid for %q generation %d", request.NodeID, request.Generation)
	}
	enrolled, err := registry.store.Enroll(ctx, record.WorkspaceID, record.ID, Enrollment{
		EnrollmentTokenID: TokenID(request.EnrollmentToken),
		AgentVersion:      request.AgentVersion,
		Facts:             request.Facts.Established(),
		EnrolledAt:        now,
		LeaseExpires:      now.Add(registry.lease),
	})
	if err != nil {
		return capability.Enrollment{}, err
	}
	// A new enrollment supersedes any open session. Closing it here is what
	// makes the fencing token meaningful rather than advisory.
	registry.closeSession(enrolled.WorkspaceID, enrolled.ID)
	sessionExpires := now.Add(registry.session)
	token, err := registry.signer.Session(enrolled.ID, enrolled.FencingToken, sessionExpires)
	if err != nil {
		return capability.Enrollment{}, err
	}
	return capability.Enrollment{
		NodeID:         enrolled.ID,
		SessionToken:   token,
		SessionExpires: sessionExpires,
		FencingToken:   enrolled.FencingToken,
		LeaseExpires:   enrolled.LeaseExpires,
		Duplicate:      false,
	}, nil
}

func (registry *Registry) lookupInvited(ctx context.Context, request capability.EnrollmentRequest) (Record, error) {
	record, err := registry.store.Find(ctx, request.NodeID)
	if err != nil {
		return Record{}, err
	}
	switch {
	case record.RentalID != request.RentalID:
		return Record{}, fmt.Errorf("node: %q belongs to Rental %q, not %q", request.NodeID, record.RentalID, request.RentalID)
	case record.Generation != request.Generation:
		return Record{}, fmt.Errorf("node: %q is generation %d, not %d", request.NodeID, record.Generation, request.Generation)
	default:
		return record, nil
	}
}

// Facts returns the node's latest reported host and inventory facts. It is an
// observation with an age, not a live read: callers weigh staleness rather than
// assuming currency.
func (registry *Registry) Facts(ctx context.Context, ref capability.NodeRef) (capability.NodeFacts, error) {
	record, err := registry.record(ctx, ref)
	if err != nil {
		return capability.NodeFacts{}, err
	}
	return record.Facts, nil
}

func (registry *Registry) PrepareImage(ctx context.Context, command capability.PrepareImageCommand) (capability.OperationReceipt, error) {
	return registry.dispatch(ctx, command.NodeRef, command.OperationID, command.FencingToken, CommandPrepareImage, command)
}

func (registry *Registry) PrepareArtifact(ctx context.Context, command capability.PrepareArtifactCommand) (capability.OperationReceipt, error) {
	return registry.dispatch(ctx, command.NodeRef, command.OperationID, command.FencingToken, CommandPrepareArtifact, command)
}

func (registry *Registry) LaunchWorkload(ctx context.Context, command capability.LaunchWorkloadCommand) (capability.OperationReceipt, error) {
	return registry.dispatch(ctx, command.NodeRef, command.OperationID, command.FencingToken, CommandLaunchWorkload, command)
}

func (registry *Registry) StopWorkload(ctx context.Context, command capability.StopWorkloadCommand) (capability.OperationReceipt, error) {
	return registry.dispatch(ctx, command.NodeRef, command.OperationID, command.FencingToken, CommandStopWorkload, command)
}

// ObserveWorkload returns what the node last reported about one workload. A
// node that has never mentioned it answers absent, which after a launch is
// materially different from an exit and must not be read as one.
func (registry *Registry) ObserveWorkload(ctx context.Context, ref capability.WorkloadRef) (capability.WorkloadObservation, error) {
	if _, err := registry.record(ctx, ref.NodeRef); err != nil {
		return capability.WorkloadObservation{}, err
	}
	observation, found, err := registry.store.LatestWorkload(ctx, ref.WorkspaceID, ref.NodeID, ref.RunID, ref.AttemptID)
	if err != nil {
		return capability.WorkloadObservation{}, err
	}
	if !found {
		// Absence is the registry's own observation and not the node's, so both
		// moments are the control plane's clock: it looked now, and it learned now.
		now := registry.now().UTC()
		return capability.WorkloadObservation{
			RunID:      ref.RunID,
			AttemptID:  ref.AttemptID,
			Phase:      capability.WorkloadPhaseAbsent,
			ObservedAt: now,
			ReceivedAt: now,
		}, nil
	}
	return observation, nil
}

// Reconcile reports what the node actually holds: which operations it has
// applied and which workloads it knows about. It is how a restarted control
// plane learns it must not launch again.
func (registry *Registry) Reconcile(ctx context.Context, ref capability.NodeRef) (capability.Reconciliation, error) {
	record, err := registry.record(ctx, ref)
	if err != nil {
		return capability.Reconciliation{}, err
	}
	applied, err := registry.store.AppliedOperationIDs(ctx, record.WorkspaceID, record.ID)
	if err != nil {
		return capability.Reconciliation{}, err
	}
	workloads, err := registry.store.Workloads(ctx, record.WorkspaceID, record.ID)
	if err != nil {
		return capability.Reconciliation{}, err
	}
	return capability.Reconciliation{
		NodeID:              record.ID,
		Generation:          record.Generation,
		FencingToken:        record.FencingToken,
		AppliedOperationIDs: applied,
		Workloads:           workloads,
		Facts:               record.Facts,
	}, nil
}

func (registry *Registry) dispatch(
	ctx context.Context,
	ref capability.NodeRef,
	operationID string,
	fencingToken uint64,
	kind CommandKind,
	command any,
) (capability.OperationReceipt, error) {
	if operationID == "" {
		return capability.OperationReceipt{}, fmt.Errorf("node: %s needs an operation ID to be idempotent", kind)
	}
	record, err := registry.record(ctx, ref)
	if err != nil {
		return capability.OperationReceipt{}, err
	}
	// A retired node is a machine Mercator gave up, and a command appended for it
	// is durable: it would be delivered to whatever reconnects on that identity
	// rather than expiring with the decision that issued it. The reference this
	// call carries was resolved before the generation ended, so this is the only
	// place the answer can still change.
	if record.Retired() {
		return capability.OperationReceipt{}, fmt.Errorf("%w: %q is not asked to %s", ErrRetired, record.ID, kind)
	}
	if fencingToken != 0 && fencingToken < record.FencingToken {
		return capability.OperationReceipt{}, fmt.Errorf("%w: %s carries %d under %d", ErrFenced, kind, fencingToken, record.FencingToken)
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return capability.OperationReceipt{}, fmt.Errorf("node: encode %s: %w", kind, err)
	}
	now := registry.now().UTC()
	stored, duplicate, err := registry.store.AppendOperation(ctx, Operation{
		OperationID:  operationID,
		NodeID:       record.ID,
		WorkspaceID:  record.WorkspaceID,
		Kind:         kind,
		FencingToken: record.FencingToken,
		State:        OperationPending,
		IssuedAt:     now,
		Payload:      payload,
	})
	if err != nil {
		return capability.OperationReceipt{}, err
	}
	if duplicate {
		return capability.OperationReceipt{OperationID: operationID, AcceptedAt: stored.IssuedAt, Duplicate: true}, nil
	}
	// Delivery is best effort on purpose. The command is durable now, so a node
	// that is disconnected receives it on its next session rather than the work
	// being lost or the caller blocking on a machine that may never answer.
	registry.deliver(record.WorkspaceID, record.ID, commandFrom(stored))
	return capability.OperationReceipt{OperationID: operationID, AcceptedAt: now}, nil
}

func commandFrom(operation Operation) Command {
	return Command{
		OperationID:  operation.OperationID,
		NodeID:       operation.NodeID,
		Kind:         operation.Kind,
		FencingToken: operation.FencingToken,
		IssuedAt:     operation.IssuedAt,
		Payload:      operation.Payload,
	}
}

func (registry *Registry) record(ctx context.Context, ref capability.NodeRef) (Record, error) {
	record, err := registry.store.Get(ctx, ref.WorkspaceID, ref.NodeID)
	if err != nil {
		return Record{}, err
	}
	if ref.Generation != 0 && record.Generation != ref.Generation {
		return Record{}, fmt.Errorf("node: %q is generation %d, not %d", ref.NodeID, record.Generation, ref.Generation)
	}
	return record, nil
}

// Reinvite mints a fresh invitation for an identity that already exists. An
// agent whose machine came back without its session credential, or a Rental
// generation that restarted, joins through this rather than replaying a spent
// invitation. The existing enrollment stays valid until the new one is
// redeemed, so a healthy node is never cut off by an invitation nobody uses.
func (registry *Registry) Reinvite(ctx context.Context, workspaceID, nodeID string) (capability.NodeBootstrap, error) {
	record, err := registry.store.Get(ctx, workspaceID, nodeID)
	if err != nil {
		return capability.NodeBootstrap{}, err
	}
	if record.Retired() {
		return capability.NodeBootstrap{}, fmt.Errorf("node: %q is retired and cannot be invited again", nodeID)
	}
	expires := registry.now().UTC().Add(registry.invitation)
	token, err := registry.signer.Enrollment(record.ID, record.RentalID, record.Generation, expires)
	if err != nil {
		return capability.NodeBootstrap{}, err
	}
	if err := registry.store.Reinvite(ctx, workspaceID, nodeID, TokenID(token), expires); err != nil {
		return capability.NodeBootstrap{}, err
	}
	return capability.NodeBootstrap{
		ControlPlaneURL: registry.controlPlaneURL,
		NodeID:          record.ID,
		RentalID:        record.RentalID,
		Generation:      record.Generation,
		EnrollmentToken: token,
		AgentVersion:    registry.agentVersion,
	}, nil
}

// Retire ends this node's working life because the Rental generation it was
// invited for is over. The identity stays in the fleet as history: what it was
// told and what it reported is what a later reconciliation reads, and deleting it
// would leave the machine's last word nowhere.
//
// The record is written before the session is ended, in that order. The record is
// what refuses the machine everything: closing the session only drops the
// connection it happens to hold, and an agent redials in milliseconds, so a
// control plane that ended the session and failed to write would hand the same
// credential a fresh session preloaded with every command the last one never
// acknowledged. Written first, a control plane that stops in between still offers
// the machine to nobody and answers its next connection with ErrRetired.
func (registry *Registry) Retire(ctx context.Context, workspaceID, nodeID string) error {
	if err := registry.store.Retire(ctx, workspaceID, nodeID); err != nil {
		return err
	}
	registry.closeSession(workspaceID, nodeID)
	return nil
}

// WithIdentitySource replaces how the registry mints identities for machines an
// operator did not name. Production uses random material; the Lab and tests
// inject a deterministic source so an invitation replays identically.
func WithIdentitySource(identity func() string) Option {
	return func(registry *Registry) { registry.newIdentity = identity }
}

func (registry *Registry) identity() string {
	if registry.newIdentity != nil {
		return registry.newIdentity()
	}
	material := make([]byte, 12)
	if _, err := rand.Read(material); err != nil {
		// A registry that cannot mint unguessable identities must not fall back
		// to a guessable one, so it names the failure in the identity itself
		// and the invitation write refuses the collision.
		return "unavailable"
	}
	return base64.RawURLEncoding.EncodeToString(material)
}
