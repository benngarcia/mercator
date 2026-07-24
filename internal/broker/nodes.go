package broker

import (
	"context"
	"fmt"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/capability"
	"github.com/benngarcia/mercator/internal/domain"
)

// Nodes is what the Broker needs to place Runs on machines Mercator holds. It
// is the reusable lane's counterpart to a provider connection: enrolled nodes
// offer capacity, and the run lifecycle reaches them through the node runtime
// rather than through a provider API.
type Nodes interface {
	Offers(ctx context.Context, workspaceID string) ([]domain.OfferSnapshot, error)
	// Ref resolves a node's current identity, including the generation a
	// command must be stamped with.
	Ref(ctx context.Context, workspaceID, nodeID string) (capability.NodeRef, error)
	LaunchWorkload(ctx context.Context, command capability.LaunchWorkloadCommand) (capability.OperationReceipt, error)
	ObserveWorkload(ctx context.Context, ref capability.WorkloadRef) (capability.WorkloadObservation, error)
	StopWorkload(ctx context.Context, command capability.StopWorkloadCommand) (capability.OperationReceipt, error)
}

// WithNodes gives the Broker the enrolled nodes it can place Runs on. Without
// it the Broker serves only the ephemeral lane, which is what a deployment with
// no node agents actually has.
func WithNodes(nodes Nodes) Option {
	return func(b *Broker) { b.nodes = nodes }
}

// launchOnNode hands one Run to the machine that will execute it. The launch
// key is the operation identity, so a redelivered launch produces one container
// and the node answers Duplicate rather than starting a second.
func (b *Broker) launchOnNode(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	ref, err := b.nodeRef(ctx, req.WorkspaceID, req.SelectedOfferNativeRef)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	command := capability.LaunchWorkloadCommand{
		RunID:             req.RunID,
		AttemptID:         req.AttemptID,
		ManifestDigest:    req.Image,
		Environment:       nodeEnvironment(req.Environment),
		MaxRuntimeSeconds: req.MaxRuntimeSeconds,
		Workload: domain.WorkloadSpec{
			Containers: []domain.ContainerSpec{{
				Name:       "main",
				Image:      req.Image,
				Platform:   req.Platform,
				Entrypoint: req.Entrypoint,
				Args:       req.Args,
				Ports:      req.Ports,
			}},
			Resources: req.Resources,
		},
	}
	command.NodeRef = ref
	command.OperationID = req.OperationKey
	command.FencingToken = 0
	receipt, err := b.nodes.LaunchWorkload(ctx, command)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	return adapter.LaunchReceipt{
		ExternalID:     ref.NodeID + "/" + req.RunID,
		LaunchKey:      req.LaunchKey,
		OwnershipToken: req.OwnershipToken,
		CleanupLocator: req.CleanupLocator,
		// The node has accepted the command, not started the container. Saying
		// queued keeps the difference between accepted and running, which is
		// the difference reconciliation depends on.
		Phase:      adapter.ExternalPhaseQueued,
		AcceptedAt: receipt.AcceptedAt,
		Duplicate:  receipt.Duplicate,
	}, nil
}

// observeOnNode reads what the node itself said about the container. It is the
// node's authority, independent of anything the application reported.
func (b *Broker) observeOnNode(ctx context.Context, req adapter.ObserveRequest, nodeID, runID, attemptID string) (adapter.ExternalObservation, error) {
	ref, err := b.nodeRef(ctx, req.WorkspaceID, nodeID)
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	observation, err := b.nodes.ObserveWorkload(ctx, capability.WorkloadRef{
		NodeRef: ref, RunID: runID, AttemptID: attemptID,
	})
	if err != nil {
		return adapter.ExternalObservation{}, err
	}
	return adapter.ExternalObservation{
		ExternalID: nodeID + "/" + runID,
		LaunchKey:  req.LaunchKey,
		Phase:      externalPhase(observation),
		ObservedAt: observation.ObservedAt,
		ExitCode:   observation.ExitCode,
	}, nil
}

// releaseOnNode removes Mercator's container from a machine it keeps. It never
// destroys the node: the whole point of the reusable lane is that the machine
// outlives the Run.
func (b *Broker) releaseOnNode(ctx context.Context, req adapter.ReleaseRequest, nodeID, runID string) (adapter.ReleaseReceipt, error) {
	ref, err := b.nodeRef(ctx, req.WorkspaceID, nodeID)
	if err != nil {
		return adapter.ReleaseReceipt{}, err
	}
	command := capability.StopWorkloadCommand{RunID: runID, GraceSeconds: 10}
	command.NodeRef = ref
	command.OperationID = req.OperationKey
	receipt, err := b.nodes.StopWorkload(ctx, command)
	if err != nil {
		return adapter.ReleaseReceipt{}, err
	}
	return adapter.ReleaseReceipt{Released: true, Duplicate: receipt.Duplicate}, nil
}

func (b *Broker) nodeRef(ctx context.Context, workspaceID, nodeID string) (capability.NodeRef, error) {
	if b.nodes == nil {
		return capability.NodeRef{}, fmt.Errorf(
			"%w: this Mercator has no enrolled nodes, so nothing can execute a reusable-lane Run",
			capability.ErrCapabilityUnsupported,
		)
	}
	return b.nodes.Ref(ctx, workspaceID, nodeID)
}

// externalPhase translates the node's container vocabulary into the run
// lifecycle's. A workload the node has never mentioned is queued, not exited:
// treating silence as an exit would close a Run that is still starting.
func externalPhase(observation capability.WorkloadObservation) adapter.ExternalPhase {
	switch observation.Phase {
	case capability.WorkloadPhaseRunning:
		return adapter.ExternalPhaseRunning
	case capability.WorkloadPhaseExited:
		if observation.ExitCode != nil && *observation.ExitCode == 0 {
			return adapter.ExternalPhaseSucceeded
		}
		return adapter.ExternalPhaseFailed
	default:
		return adapter.ExternalPhaseQueued
	}
}

func nodeEnvironment(bindings []adapter.EnvironmentBinding) []capability.EnvironmentBinding {
	translated := make([]capability.EnvironmentBinding, 0, len(bindings))
	for _, binding := range bindings {
		translated = append(translated, capability.EnvironmentBinding{Name: binding.Name, Value: binding.Value})
	}
	return translated
}

// nodeConnectionID and nodeAdapterType name node-backed capacity in aggregation
// reports. They mirror the node package's own constants without importing them
// into the offer path, so the Broker depends on the Nodes interface alone.
const (
	nodeConnectionID = "connection:nodes"
	nodeAdapterType  = "node"
)
