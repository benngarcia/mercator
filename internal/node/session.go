package node

import (
	"context"
	"fmt"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
)

// sessionBuffer is how many commands one session holds before delivery blocks.
// Commands are already durable when they reach a session, so a full buffer
// costs a redelivery on reconnect rather than the work.
const sessionBuffer = 64

// Session is one node's open outbound connection. The control plane never
// dials a node: the node opens this, and commands travel back down it.
type Session struct {
	NodeID       string
	WorkspaceID  string
	FencingToken uint64

	commands chan Command
	closed   chan struct{}
}

// Commands is the stream a transport writes to the node.
func (session *Session) Commands() <-chan Command { return session.commands }

// Done closes when the control plane supersedes or ends this session, which is
// the transport's signal to close the connection.
func (session *Session) Done() <-chan struct{} { return session.closed }

// OpenSession authenticates a node's outbound connection and returns its
// command stream, beginning with every command it has not acknowledged. A node
// that was disconnected, or that reconnected to a restarted control plane,
// therefore receives the work it missed instead of the work being lost.
func (registry *Registry) OpenSession(ctx context.Context, nodeID, sessionToken string) (*Session, error) {
	record, err := registry.authenticate(ctx, nodeID, sessionToken)
	if err != nil {
		return nil, err
	}
	pending, err := registry.store.PendingOperations(ctx, record.WorkspaceID, record.ID)
	if err != nil {
		return nil, err
	}
	session := &Session{
		NodeID:       record.ID,
		WorkspaceID:  record.WorkspaceID,
		FencingToken: record.FencingToken,
		commands:     make(chan Command, max(sessionBuffer, len(pending))),
		closed:       make(chan struct{}),
	}
	for _, operation := range pending {
		session.commands <- commandFrom(operation)
	}
	registry.mu.Lock()
	if previous, open := registry.sessions[nodeKey(record.WorkspaceID, record.ID)]; open {
		close(previous.closed)
	}
	registry.sessions[nodeKey(record.WorkspaceID, record.ID)] = session
	registry.mu.Unlock()
	return session, nil
}

// CloseSession ends one node's session, which a transport calls when its
// connection drops.
func (registry *Registry) CloseSession(session *Session) {
	if session == nil {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, open := registry.sessions[nodeKey(session.WorkspaceID, session.NodeID)]
	if !open || current != session {
		return
	}
	delete(registry.sessions, nodeKey(session.WorkspaceID, session.NodeID))
	select {
	case <-session.closed:
	default:
		close(session.closed)
	}
}

// RecordEvents accepts facts a node reports on its own authority: its liveness
// and inventory, and container lifecycle transitions. Events carry IDs so a
// spool replayed after a reconnection changes nothing, which is what lets an
// agent keep reporting while disconnected.
func (registry *Registry) RecordEvents(ctx context.Context, nodeID, sessionToken string, events []Event) error {
	record, err := registry.authenticate(ctx, nodeID, sessionToken)
	if err != nil {
		return err
	}
	var latestFacts *capability.NodeFacts
	for _, event := range events {
		event.NodeID = record.ID
		event.WorkspaceID = record.WorkspaceID
		if err := event.Validate(); err != nil {
			return err
		}
		fresh, err := registry.store.RecordEvent(ctx, event)
		if err != nil {
			return err
		}
		if fresh && event.Kind == EventHeartbeat {
			latestFacts = event.Facts
		}
	}
	if latestFacts == nil {
		return nil
	}
	_, err = registry.store.Heartbeat(ctx, record.WorkspaceID, record.ID, *latestFacts, registry.now().UTC().Add(registry.lease))
	return err
}

// RecordResult accepts a node's answer about one command. A node that already
// applied the operation reports Duplicate, which is what makes redelivery after
// a lost response safe rather than doubling the effect.
func (registry *Registry) RecordResult(ctx context.Context, nodeID, sessionToken string, result Result) error {
	record, err := registry.authenticate(ctx, nodeID, sessionToken)
	if err != nil {
		return err
	}
	if result.OperationID == "" {
		return fmt.Errorf("node: a result names the operation it settles")
	}
	if result.ReportedAt.IsZero() {
		result.ReportedAt = registry.now().UTC()
	}
	return registry.store.SettleOperation(ctx, record.WorkspaceID, record.ID, result)
}

// ExpireLeases marks nodes the control plane has stopped hearing from as lost
// and returns them. A lost node is unobserved, not dead: its workloads need
// reconciliation, and only the node or its provider can say what happened.
func (registry *Registry) ExpireLeases(ctx context.Context) ([]Record, error) {
	expired, err := registry.store.ExpireLeases(ctx, registry.now().UTC())
	if err != nil {
		return nil, err
	}
	for _, record := range expired {
		registry.closeSession(record.WorkspaceID, record.ID)
	}
	return expired, nil
}

// List returns every node identity in a workspace, whatever its state, so
// operators and reconciliation can see capacity that never enrolled as readily
// as capacity that did.
func (registry *Registry) List(ctx context.Context, workspaceID string) ([]Record, error) {
	return registry.store.List(ctx, workspaceID)
}

func (registry *Registry) authenticate(ctx context.Context, nodeID, sessionToken string) (Record, error) {
	record, err := registry.store.Find(ctx, nodeID)
	if err != nil {
		return Record{}, err
	}
	if !registry.signer.VerifySession(record.ID, record.FencingToken, sessionToken, registry.now().UTC()) {
		return Record{}, fmt.Errorf("node: session credential is not valid for %q at fencing token %d", nodeID, record.FencingToken)
	}
	return record, nil
}

func (registry *Registry) deliver(workspaceID, nodeID string, command Command) {
	registry.mu.Lock()
	session, open := registry.sessions[nodeKey(workspaceID, nodeID)]
	registry.mu.Unlock()
	if !open {
		return
	}
	select {
	case session.commands <- command:
	case <-session.closed:
	default:
		// The session is backed up. The command is already durable, so the node
		// receives it on its next session rather than blocking the caller on a
		// machine that may never drain.
	}
}

func (registry *Registry) closeSession(workspaceID, nodeID string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	session, open := registry.sessions[nodeKey(workspaceID, nodeID)]
	if !open {
		return
	}
	delete(registry.sessions, nodeKey(workspaceID, nodeID))
	select {
	case <-session.closed:
	default:
		close(session.closed)
	}
}

// LeaseWindow is how long the registry believes a node absent a heartbeat.
// Agents use it to choose a heartbeat interval that keeps them inside it.
func (registry *Registry) LeaseWindow() time.Duration { return registry.lease }

var _ capability.NodeRuntime = (*Registry)(nil)
