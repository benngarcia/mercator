package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/domain"
)

var ErrAlreadyExists = errors.New("docker: container already exists")
var ErrNotFound = errors.New("docker: container not found")

type Client interface {
	CreateContainer(ctx context.Context, req CreateContainerRequest) (Container, error)
	StartContainer(ctx context.Context, name string) error
	InspectContainer(ctx context.Context, name string) (Container, error)
	RemoveContainer(ctx context.Context, name string) error
	ListContainers(ctx context.Context, labels map[string]string) ([]Container, error)
}

type CreateContainerRequest struct {
	Name       string
	Image      string
	Platform   string
	Entrypoint []string
	Args       []string
	Env        map[string]string
	Ports      []int
	Labels     map[string]string
}

type Container struct {
	ID        string
	Name      string
	Labels    map[string]string
	State     string
	ExitCode  *int
	CreatedAt time.Time
}

type Adapter struct {
	client Client
	now    func() time.Time
}

func New(client Client) *Adapter {
	return &Adapter{client: client, now: time.Now}
}

func (a *Adapter) ListOffers(context.Context, adapter.OfferRequest) ([]domain.OfferSnapshot, error) {
	return nil, fmt.Errorf("docker: offer collection is provided by offer service in this slice")
}

func (a *Adapter) Launch(ctx context.Context, req adapter.LaunchRequest) (adapter.LaunchReceipt, error) {
	name := containerName(req)
	env, err := dockerEnv(req.Environment)
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	container, err := a.client.CreateContainer(ctx, CreateContainerRequest{
		Name:       name,
		Image:      req.Image,
		Platform:   req.Platform.String(),
		Entrypoint: stringSlicePtr(req.Entrypoint),
		Args:       append([]string(nil), req.Args...),
		Env:        env,
		Ports:      dockerPorts(req),
		Labels:     dockerLabels(req),
	})
	duplicate := false
	if errors.Is(err, ErrAlreadyExists) {
		container, err = a.client.InspectContainer(ctx, name)
		duplicate = true
		if err == nil && !labelsMatch(container.Labels, dockerLabels(req)) {
			return adapter.LaunchReceipt{}, adapter.ErrIdempotencyConflict
		}
	}
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	if !duplicate || phaseFromState(container.State, container.ExitCode) == adapter.ExternalPhaseQueued {
		if err := a.client.StartContainer(ctx, name); err != nil {
			return adapter.LaunchReceipt{}, err
		}
		container, err = a.client.InspectContainer(ctx, name)
		if err != nil {
			return adapter.LaunchReceipt{}, err
		}
	}
	return adapter.LaunchReceipt{
		ExternalID:     container.ID,
		LaunchKey:      req.LaunchKey,
		OwnershipToken: req.OwnershipToken,
		CleanupLocator: req.CleanupLocator,
		Phase:          phaseFromState(container.State, container.ExitCode),
		AcceptedAt:     a.now().UTC(),
		Duplicate:      duplicate,
	}, nil
}

func (a *Adapter) Observe(ctx context.Context, req adapter.ObserveRequest) (adapter.ExternalObservation, error) {
	container, err := a.client.InspectContainer(ctx, req.LaunchKey)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return adapter.ExternalObservation{LaunchKey: req.LaunchKey, Phase: adapter.ExternalPhaseReleased, ObservedAt: a.now().UTC()}, nil
		}
		return adapter.ExternalObservation{}, err
	}
	if !ownershipMatches(container.Labels, req.OwnershipToken, req.RequestHash) {
		return adapter.ExternalObservation{}, adapter.ErrIdempotencyConflict
	}
	return adapter.ExternalObservation{ExternalID: container.ID, LaunchKey: req.LaunchKey, Phase: phaseFromState(container.State, container.ExitCode), ExitCode: container.ExitCode, ObservedAt: a.now().UTC()}, nil
}

func (a *Adapter) Cancel(context.Context, adapter.CancelRequest) (adapter.CancelReceipt, error) {
	return adapter.CancelReceipt{Cancelled: true}, nil
}

func (a *Adapter) Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	container, err := a.client.InspectContainer(ctx, req.LaunchKey)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return adapter.ReleaseReceipt{Released: true}, nil
		}
		return adapter.ReleaseReceipt{}, err
	}
	if !ownershipMatches(container.Labels, req.OwnershipToken, req.LaunchRequestHash) {
		return adapter.ReleaseReceipt{}, adapter.ErrIdempotencyConflict
	}
	if err := a.client.RemoveContainer(ctx, req.LaunchKey); err != nil {
		if errors.Is(err, ErrNotFound) {
			return adapter.ReleaseReceipt{Released: true}, nil
		}
		return adapter.ReleaseReceipt{}, err
	}
	return adapter.ReleaseReceipt{Released: true}, nil
}

// Terminate is unsupported for local Docker: it is a STANDING pool, so the
// broker owns no host to destroy — it only removes the container it created
// (that is Release). A run placed on a Docker (standing) offer always records
// disposition=release, so the orchestrator never routes Terminate here in
// practice; if it ever does, that is a misrouted cleanup and we surface it
// explicitly rather than silently destroying or no-op'ing.
func (a *Adapter) Terminate(context.Context, adapter.TerminateRequest) (adapter.TerminateReceipt, error) {
	return adapter.TerminateReceipt{}, adapter.ErrTerminateUnsupported
}

func (a *Adapter) ListOwned(ctx context.Context, req adapter.OwnershipQuery) ([]adapter.OwnedExternalObject, error) {
	containers, err := a.client.ListContainers(ctx, map[string]string{"mercator.workspace_id": req.WorkspaceID})
	if err != nil {
		return nil, err
	}
	owned := make([]adapter.OwnedExternalObject, 0, len(containers))
	for _, container := range containers {
		owned = append(owned, adapter.OwnedExternalObject{
			ExternalID:     container.ID,
			WorkspaceID:    container.Labels["mercator.workspace_id"],
			RunID:          container.Labels["mercator.run_id"],
			AttemptID:      container.Labels["mercator.attempt_id"],
			OwnershipToken: container.Labels["mercator.ownership_token"],
			LaunchKey:      container.Labels["mercator.launch_key"],
			CleanupLocator: container.Labels["mercator.cleanup_locator"],
			RequestHash:    container.Labels["mercator.request_hash"],
			Phase:          phaseFromState(container.State, container.ExitCode),
		})
	}
	return owned, nil
}

func containerName(req adapter.LaunchRequest) string {
	return req.LaunchKey
}

func dockerLabels(req adapter.LaunchRequest) map[string]string {
	return map[string]string{
		"mercator.workspace_id":    req.WorkspaceID,
		"mercator.run_id":          req.RunID,
		"mercator.attempt_id":      req.AttemptID,
		"mercator.launch_key":      req.LaunchKey,
		"mercator.ownership_token": req.OwnershipToken,
		"mercator.cleanup_locator": req.CleanupLocator,
		"mercator.request_hash":    req.RequestHash,
		"mercator.workload_id":     req.WorkloadID,
		"mercator.revision_id":     req.WorkloadRevisionID,
	}
}

func dockerEnv(bindings []adapter.EnvironmentBinding) (map[string]string, error) {
	env := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if binding.Value != nil {
			env[binding.Name] = *binding.Value
		}
	}
	return env, nil
}

func dockerPorts(req adapter.LaunchRequest) []int {
	ports := make([]int, 0, len(req.Ports))
	for _, port := range req.Ports {
		ports = append(ports, port.ContainerPort)
	}
	return ports
}

func stringSlicePtr(values *[]string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), (*values)...)
}

func labelsMatch(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func ownershipMatches(labels map[string]string, ownershipToken, requestHash string) bool {
	if ownershipToken == "" || requestHash == "" {
		return false
	}
	return labels["mercator.ownership_token"] == ownershipToken && labels["mercator.request_hash"] == requestHash
}

func phaseFromState(state string, exitCode *int) adapter.ExternalPhase {
	switch strings.ToLower(state) {
	case "created":
		return adapter.ExternalPhaseQueued
	case "running", "restarting", "paused":
		return adapter.ExternalPhaseRunning
	case "exited":
		if exitCode != nil && *exitCode != 0 {
			return adapter.ExternalPhaseFailed
		}
		return adapter.ExternalPhaseSucceeded
	case "dead":
		return adapter.ExternalPhaseFailed
	case "removing", "removed":
		return adapter.ExternalPhaseReleased
	default:
		return adapter.ExternalPhaseQueued
	}
}
