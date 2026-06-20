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
	container, err := a.client.CreateContainer(ctx, CreateContainerRequest{
		Name:       name,
		Image:      req.Image,
		Platform:   req.Platform.String(),
		Entrypoint: stringSlicePtr(req.Entrypoint),
		Args:       append([]string(nil), req.Args...),
		Env:        dockerEnv(req.Environment),
		Ports:      dockerPorts(req),
		Labels:     dockerLabels(req),
	})
	duplicate := false
	if errors.Is(err, ErrAlreadyExists) {
		container, err = a.client.InspectContainer(ctx, name)
		duplicate = true
	}
	if err != nil {
		return adapter.LaunchReceipt{}, err
	}
	return adapter.LaunchReceipt{
		ExternalID:     container.ID,
		LaunchKey:      req.LaunchKey,
		OwnershipToken: req.OwnershipToken,
		CleanupLocator: req.CleanupLocator,
		Phase:          phaseFromState(container.State),
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
	return adapter.ExternalObservation{ExternalID: container.ID, LaunchKey: req.LaunchKey, Phase: phaseFromState(container.State), ObservedAt: a.now().UTC()}, nil
}

func (a *Adapter) Cancel(context.Context, adapter.CancelRequest) (adapter.CancelReceipt, error) {
	return adapter.CancelReceipt{Cancelled: true}, nil
}

func (a *Adapter) Release(ctx context.Context, req adapter.ReleaseRequest) (adapter.ReleaseReceipt, error) {
	if err := a.client.RemoveContainer(ctx, req.LaunchKey); err != nil {
		return adapter.ReleaseReceipt{}, err
	}
	return adapter.ReleaseReceipt{Released: true}, nil
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
			Phase:          phaseFromState(container.State),
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

func dockerEnv(bindings []adapter.EnvironmentBinding) map[string]string {
	env := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if binding.Value != nil {
			env[binding.Name] = *binding.Value
			continue
		}
		if binding.SecretRef != nil {
			env[binding.Name] = fmt.Sprintf("secret:%s:%d", binding.SecretRef.Name, binding.SecretRef.Version)
		}
	}
	return env
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

func phaseFromState(state string) adapter.ExternalPhase {
	switch strings.ToLower(state) {
	case "exited":
		return adapter.ExternalPhaseSucceeded
	case "removed":
		return adapter.ExternalPhaseReleased
	default:
		return adapter.ExternalPhaseRunning
	}
}
