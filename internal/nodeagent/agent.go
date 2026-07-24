// Package nodeagent is the Mercator node agent: the process that runs on a
// reusable machine and executes successive workloads there.
//
// It connects outbound and never listens. It keeps enough local state to
// refuse a command it already applied, which is what stops a redelivered
// launch from starting a second container. It spools the facts it owes the
// control plane while disconnected, so a container that exited during a
// network partition is still reported rather than inferred.
package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/node"
)

// Runtime is the container runtime the agent drives. It is deliberately narrow:
// Docker is the first implementation, and nothing above this line knows which
// one is running, so containerd or another OCI runtime can be added without
// touching the control-plane contract.
type Runtime interface {
	// Facts reports the host, accelerators, driver, runtime, disk, and exact
	// content inventory this machine holds.
	Facts(ctx context.Context) (capability.NodeFacts, error)
	PrepareImage(ctx context.Context, command capability.PrepareImageCommand) error
	PrepareArtifact(ctx context.Context, command capability.PrepareArtifactCommand) error
	// LaunchWorkload starts the container and returns once it is created. The
	// agent observes its lifecycle separately, so a launch that returns does
	// not claim the workload succeeded.
	LaunchWorkload(ctx context.Context, command capability.LaunchWorkloadCommand) error
	StopWorkload(ctx context.Context, command capability.StopWorkloadCommand) error
	// Observe reports every workload this runtime currently knows about,
	// including recently exited ones the agent has not yet reported.
	Observe(ctx context.Context) ([]capability.WorkloadObservation, error)
}

// Transport is the agent's outbound connection to the control plane.
type Transport interface {
	Enroll(ctx context.Context, request capability.EnrollmentRequest) (capability.Enrollment, error)
	// Session opens the command stream. It returns when the connection ends;
	// the agent reconnects.
	Session(ctx context.Context, nodeID, sessionToken string, commands chan<- node.Command) error
	SendEvents(ctx context.Context, nodeID, sessionToken string, events []node.Event) error
	SendResult(ctx context.Context, nodeID, sessionToken string, result node.Result) error
}

// Identity is the immutable material a machine was bootstrapped with. The agent
// never invents any of it.
type Identity struct {
	ControlPlaneURL string
	NodeID          string
	RentalID        string
	Generation      uint64
	EnrollmentToken string
	AgentVersion    string
}

// Agent runs one machine's side of the node protocol.
type Agent struct {
	identity  Identity
	runtime   Runtime
	transport Transport
	state     *State
	logger    *slog.Logger
	now       func() time.Time

	heartbeat time.Duration
	backoff   time.Duration
}

// Option configures an Agent.
type Option func(*Agent)

// WithHeartbeat sets how often the agent reports its facts. It must stay well
// inside the control plane's lease, or a healthy machine will be declared lost.
func WithHeartbeat(interval time.Duration) Option {
	return func(agent *Agent) { agent.heartbeat = interval }
}

// WithClock replaces the wall clock, so a test states time rather than waits.
func WithClock(now func() time.Time) Option {
	return func(agent *Agent) { agent.now = now }
}

// WithLogger replaces the agent's logger.
func WithLogger(logger *slog.Logger) Option {
	return func(agent *Agent) { agent.logger = logger }
}

// WithReconnectBackoff sets how long the agent waits before reopening a
// dropped session.
func WithReconnectBackoff(backoff time.Duration) Option {
	return func(agent *Agent) { agent.backoff = backoff }
}

func New(identity Identity, runtime Runtime, transport Transport, state *State, opts ...Option) *Agent {
	agent := &Agent{
		identity:  identity,
		runtime:   runtime,
		transport: transport,
		state:     state,
		logger:    slog.Default(),
		now:       time.Now,
		heartbeat: 20 * time.Second,
		backoff:   2 * time.Second,
	}
	for _, opt := range opts {
		opt(agent)
	}
	return agent
}

// Run drives the agent until ctx ends. Each pass enrolls if needed, drains the
// spool, opens a session, and serves commands until the connection drops.
func (agent *Agent) Run(ctx context.Context) error {
	for {
		if err := agent.serve(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			agent.logger.WarnContext(ctx, "node session ended", "node_id", agent.identity.NodeID, "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(agent.backoff):
		}
	}
}

func (agent *Agent) serve(ctx context.Context) error {
	session, err := agent.session(ctx)
	if err != nil {
		return err
	}
	// Reporting what this machine actually holds is the first thing a
	// reconnected agent does, so the control plane reconciles against
	// observations rather than assumptions.
	if err := agent.reportObservations(ctx, session); err != nil {
		return err
	}
	if err := agent.flushSpool(ctx, session); err != nil {
		return err
	}

	commands := make(chan node.Command, 16)
	sessionCtx, endSession := context.WithCancel(ctx)
	defer endSession()
	streamed := make(chan error, 1)
	go func() {
		streamed <- agent.transport.Session(sessionCtx, agent.identity.NodeID, session, commands)
	}()

	ticker := time.NewTicker(agent.heartbeat)
	defer ticker.Stop()
	if err := agent.sendHeartbeat(ctx, session); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-streamed:
			return err
		case <-ticker.C:
			if err := agent.sendHeartbeat(ctx, session); err != nil {
				agent.logger.WarnContext(ctx, "heartbeat spooled", "error", err)
			}
			// Container lifecycle is the node's own authority, so the agent
			// watches the runtime rather than waiting for the application to
			// say something. Without this, an exit would only surface on the
			// next reconnection.
			if err := agent.reportObservations(ctx, session); err != nil {
				agent.logger.WarnContext(ctx, "workload observation spooled", "error", err)
			}
		case command := <-commands:
			agent.apply(ctx, session, command)
		}
	}
}

// session returns a usable session credential, enrolling only when the agent
// has none. An agent that restarts with its state intact resumes rather than
// re-enrolling, which is what keeps its fencing token and its applied-operation
// memory aligned with the control plane.
func (agent *Agent) session(ctx context.Context) (string, error) {
	if token, fencing, ok := agent.state.Session(agent.now()); ok {
		agent.logger.DebugContext(ctx, "resuming node session", "fencing_token", fencing)
		return token, nil
	}
	facts, err := agent.runtime.Facts(ctx)
	if err != nil {
		return "", fmt.Errorf("read host facts: %w", err)
	}
	enrollment, err := agent.transport.Enroll(ctx, capability.EnrollmentRequest{
		NodeID:          agent.identity.NodeID,
		RentalID:        agent.identity.RentalID,
		Generation:      agent.identity.Generation,
		EnrollmentToken: agent.identity.EnrollmentToken,
		AgentVersion:    agent.identity.AgentVersion,
		Facts:           facts,
	})
	if err != nil {
		return "", fmt.Errorf("enroll: %w", err)
	}
	if err := agent.state.Enrolled(enrollment); err != nil {
		return "", err
	}
	return enrollment.SessionToken, nil
}

// apply performs one command exactly once. An operation the agent has already
// applied is acknowledged again without touching the runtime, which is what
// makes a redelivered launch safe.
func (agent *Agent) apply(ctx context.Context, session string, command node.Command) {
	if command.FencingToken != 0 && command.FencingToken < agent.state.FencingToken() {
		agent.report(ctx, session, node.Result{
			OperationID: command.OperationID,
			Applied:     false,
			Failure:     "command carries a superseded fencing token",
			ReportedAt:  agent.now().UTC(),
		})
		return
	}
	if agent.state.Applied(command.OperationID) {
		agent.report(ctx, session, node.Result{
			OperationID: command.OperationID,
			Applied:     true,
			Duplicate:   true,
			ReportedAt:  agent.now().UTC(),
		})
		return
	}
	failure := agent.perform(ctx, command)
	// The operation is recorded as applied before the result is reported. A
	// crash between the two costs a duplicate acknowledgement, never a
	// duplicate container.
	if err := agent.state.MarkApplied(command.OperationID); err != nil {
		agent.logger.ErrorContext(ctx, "could not record an applied operation", "operation_id", command.OperationID, "error", err)
	}
	result := node.Result{OperationID: command.OperationID, Applied: failure == nil, ReportedAt: agent.now().UTC()}
	if failure != nil {
		result.Failure = failure.Error()
	}
	agent.report(ctx, session, result)
}

func (agent *Agent) perform(ctx context.Context, command node.Command) error {
	switch command.Kind {
	case node.CommandPrepareImage:
		return decodeAnd(command, agent.runtime.PrepareImage, ctx)
	case node.CommandPrepareArtifact:
		return decodeAnd(command, agent.runtime.PrepareArtifact, ctx)
	case node.CommandLaunchWorkload:
		return decodeAnd(command, agent.runtime.LaunchWorkload, ctx)
	case node.CommandStopWorkload:
		return decodeAnd(command, agent.runtime.StopWorkload, ctx)
	default:
		return fmt.Errorf("unknown node command %q", command.Kind)
	}
}

func decodeAnd[T any](command node.Command, perform func(context.Context, T) error, ctx context.Context) error {
	var typed T
	if err := json.Unmarshal(command.Payload, &typed); err != nil {
		return fmt.Errorf("decode %s: %w", command.Kind, err)
	}
	return perform(ctx, typed)
}

func (agent *Agent) report(ctx context.Context, session string, result node.Result) {
	if err := agent.transport.SendResult(ctx, agent.identity.NodeID, session, result); err != nil {
		agent.logger.WarnContext(ctx, "could not report a command result", "operation_id", result.OperationID, "error", err)
	}
}

func (agent *Agent) sendHeartbeat(ctx context.Context, session string) error {
	facts, err := agent.runtime.Facts(ctx)
	if err != nil {
		return fmt.Errorf("read host facts: %w", err)
	}
	facts.ObservedAt = agent.now().UTC()
	return agent.send(ctx, session, node.Event{
		ID:         agent.state.NextEventID(),
		NodeID:     agent.identity.NodeID,
		Kind:       node.EventHeartbeat,
		ObservedAt: facts.ObservedAt,
		Facts:      &facts,
	})
}

// reportObservations tells the control plane what containers this machine
// actually has. It is how an exit reaches Mercator whatever the application
// did or did not report, and how a restart on either side converges without
// guessing.
//
// Only transitions are sent. Repeating an unchanged phase every tick would
// bury the record in noise without telling anyone anything new.
func (agent *Agent) reportObservations(ctx context.Context, session string) error {
	observations, err := agent.runtime.Observe(ctx)
	if err != nil {
		return fmt.Errorf("observe workloads: %w", err)
	}
	for _, observation := range observations {
		if !agent.state.WorkloadChanged(observation) {
			continue
		}
		if err := agent.send(ctx, session, node.Event{
			ID:         agent.state.NextEventID(),
			NodeID:     agent.identity.NodeID,
			Kind:       node.EventWorkload,
			ObservedAt: observation.ObservedAt,
			Workload:   &observation,
		}); err != nil {
			return err
		}
		agent.state.RecordWorkload(observation)
	}
	return nil
}

// send delivers one event, spooling it when the control plane is unreachable.
// A spooled event keeps its ID, so replaying it after a reconnection changes
// nothing.
func (agent *Agent) send(ctx context.Context, session string, event node.Event) error {
	if err := agent.transport.SendEvents(ctx, agent.identity.NodeID, session, []node.Event{event}); err != nil {
		return errors.Join(err, agent.state.Spool(event))
	}
	return nil
}

func (agent *Agent) flushSpool(ctx context.Context, session string) error {
	spooled := agent.state.Spooled()
	if len(spooled) == 0 {
		return nil
	}
	if err := agent.transport.SendEvents(ctx, agent.identity.NodeID, session, spooled); err != nil {
		return err
	}
	return agent.state.ClearSpool()
}
