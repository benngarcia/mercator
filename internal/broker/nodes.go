package broker

import (
	"context"
	"fmt"
	"slices"
	"time"

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
	// PrepareImage and PrepareArtifact are the reusable lane's half of
	// preparation. They have been on capability.NodeRuntime since it existed and
	// were unreachable from the control plane's own abstraction, which declared
	// only the five calls a launch needs: the prepare path was built end to end
	// and nothing above it could say the word.
	PrepareImage(ctx context.Context, command capability.PrepareImageCommand) (capability.OperationReceipt, error)
	PrepareArtifact(ctx context.Context, command capability.PrepareArtifactCommand) (capability.OperationReceipt, error)
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
		RunID:     req.RunID,
		AttemptID: req.AttemptID,
		// The digest, not the reference carrying it. The node splices this back
		// onto the repository to pin what it runs, and reports it back as what
		// it holds, so a whole reference here would both build an unrunnable
		// image name and name the image something no host could match.
		ManifestDigest: domain.ReferenceDigest(req.Image),
		Environment:    nodeEnvironment(req.Environment),
		// The caches the workload declared, carried through as declared. The
		// workspace scoping them is on the node ref below, which is what the
		// runtime derives each cache's own volume from.
		CacheMounts:       slices.Clone(req.CacheMounts),
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
	// A fresh launch carries no fencing token of its own. It is stamped with
	// whatever the node's current enrollment is at dispatch, which is what the
	// caller wants: if the node re-enrolled between resolving it and sending
	// this, the command should reach the new session rather than be refused as
	// stale. Fencing protects commands issued under a superseded enrollment,
	// and this one was not.
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

// Prepare hands one desired preparation set to the machines it names. Each item
// becomes one command with its own operation identity, so a redelivered desire
// produces one pull and a Duplicate receipt rather than a second fetch of the
// same content.
//
// Only the reusable lane can be prepared, and the Broker says so rather than
// failing the whole request: a provider Rental has no runtime of Mercator's on
// it to fetch anything, and a one-shot product holds nothing once its workload
// exits. An item Mercator sends anyway is reported unsupported, because a
// machine that cannot be prepared is a machine whose next Run pays the whole
// fetch and an operator should be able to read that rather than infer it from a
// missing effect.
//
// Withdrawal is not yet expressible here. A node's contract has one command per
// piece of content and no way to say "stop fetching that", so an item this
// desired set no longer names is a pull that runs to completion on the machine.
// The Lab world models the withdrawal because a provider seam can; earning it on
// a node is its own command, tracked rather than faked.
func (b *Broker) Prepare(ctx context.Context, request adapter.PrepareRequest) (adapter.PrepareReceipt, error) {
	receipt := adapter.PrepareReceipt{OperationKey: request.OperationKey, AcceptedAt: time.Now().UTC()}
	for _, item := range request.Wanted {
		if item.ConnectionID != nodeConnectionID {
			receipt.Unsupported = append(receipt.Unsupported, item.Content())
			continue
		}
		prepared, err := b.prepareOnNode(ctx, request, item)
		if err != nil {
			return adapter.PrepareReceipt{}, err
		}
		receipt.Started = append(receipt.Started, item.Content())
		receipt.Duplicate = receipt.Duplicate || prepared.Duplicate
	}
	return receipt, nil
}

// prepareOnNode sends one piece of content to one machine. The operation
// identity is the machine and the content, never the desired set that happened
// to name them: a set is recomputed on every sweep and two Runs can want one
// image, so an identity carrying either would have the node fetch the same bytes
// again.
//
// What that identity answers is a redelivery of the same desire, and only that.
// A node has applied an identity, is still working on it, or refused it, and the
// operation store dedupes on the identity with no regard for which: a pull that
// failed on the machine is answered Duplicate from then on, so this control plane
// never asks that host for that content again while the node's own agent is
// deliberately not remembering it so that a retry can happen. The defect is
// recorded in docs/project/capacity-broker-migration.md under the prewarming
// slice. It is not repaired here, because making a refusal reissuable changes
// what an operation identity promises and needs a world that can refuse a fetch
// before it has a specification that could fail on it.
func (b *Broker) prepareOnNode(ctx context.Context, request adapter.PrepareRequest, item adapter.PrepareItem) (capability.OperationReceipt, error) {
	ref, err := b.nodeRef(ctx, request.WorkspaceID, item.NativeRef)
	if err != nil {
		return capability.OperationReceipt{}, err
	}
	operationID := "prepare:" + string(item.Kind) + ":" + item.OfferSnapshotID + ":" + item.Content()
	if item.Kind == adapter.PrepareArtifact {
		command := capability.PrepareArtifactCommand{
			ArtifactID:    item.ArtifactID,
			ContentDigest: item.ContentDigest,
			Source:        item.Source,
			SizeBytes:     item.SizeBytes,
		}
		command.NodeRef = ref
		command.OperationID = operationID
		return b.nodes.PrepareArtifact(ctx, command)
	}
	command := capability.PrepareImageCommand{
		// The digest and the reference carrying it, exactly as a launch sends
		// them: a node pulls by reference and reports what it holds by digest.
		ManifestDigest: domain.ReferenceDigest(item.Image),
		Platform:       item.Platform,
		Reference:      item.Image,
		Unpack:         true,
	}
	command.NodeRef = ref
	command.OperationID = operationID
	return b.nodes.PrepareImage(ctx, command)
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
		// The node owns container lifecycle, so the moment its runtime says the
		// process began is the authority on when this workload started. It was
		// written by the contract and read by nobody until now, which left the run
		// stream with no start moment on the only reusable lane there is.
		StartedAt: observation.StartedAt,
		ExitCode:  observation.ExitCode,
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
