package docker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bengarcia/mercator/internal/adapter"
	"github.com/bengarcia/mercator/internal/domain"
)

func TestAdapterLaunchObserveReleaseAndListOwned(t *testing.T) {
	client := newFakeClient()
	ad := New(client)
	req := launchRequest()

	receipt, err := ad.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if receipt.ExternalID == "" || receipt.CleanupLocator != req.CleanupLocator {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	created := client.created[0]
	if created.Image != req.Image || created.Platform != "linux/amd64" || created.Entrypoint[0] != "/bin/app" || created.Args[0] != "--serve" {
		t.Fatalf("launch did not pass OCI command/platform: %+v", created)
	}
	if created.Labels["mercator.launch_key"] != req.LaunchKey || created.Labels["mercator.ownership_token"] != req.OwnershipToken {
		t.Fatalf("launch did not set ownership labels: %+v", created.Labels)
	}
	if created.Env["LOG_LEVEL"] != "info" || created.Env["API_TOKEN"] != "secret:api-token:2" {
		t.Fatalf("unexpected env mapping: %+v", created.Env)
	}

	observation, err := ad.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: req.LaunchKey})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observation.Phase != adapter.ExternalPhaseRunning {
		t.Fatalf("unexpected observation: %+v", observation)
	}
	owned, err := ad.ListOwned(context.Background(), adapter.OwnershipQuery{WorkspaceID: req.WorkspaceID})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) != 1 || owned[0].LaunchKey != req.LaunchKey {
		t.Fatalf("unexpected owned objects: %+v", owned)
	}
	released, err := ad.Release(context.Background(), adapter.ReleaseRequest{OperationKey: "release_1", RequestHash: "sha256:release", LaunchKey: req.LaunchKey})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !released.Released {
		t.Fatalf("expected release receipt, got %+v", released)
	}
}

func TestIntegrationDockerAdapterLaunchObserveRelease(t *testing.T) {
	if os.Getenv("MERCATOR_DOCKER_INTEGRATION") != "1" {
		t.Skip("set MERCATOR_DOCKER_INTEGRATION=1 to run live Docker adapter integration")
	}
	image := os.Getenv("MERCATOR_DOCKER_IMAGE")
	if image == "" {
		image = "alpine:latest@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b"
	}
	req := launchRequest()
	req.Image = image
	req.Platform = domain.Platform{OS: "linux", Architecture: "arm64"}
	req.LaunchKey = "mercator-integration-" + time.Now().UTC().Format("20060102150405")
	req.OperationKey = req.LaunchKey
	req.CleanupLocator = req.LaunchKey
	ad := New(NewCLIClient(""))
	t.Cleanup(func() {
		_, _ = ad.Release(context.Background(), adapter.ReleaseRequest{OperationKey: "cleanup_" + req.LaunchKey, RequestHash: "sha256:cleanup", LaunchKey: req.LaunchKey})
	})
	receipt, err := ad.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("live launch: %v", err)
	}
	if receipt.ExternalID == "" {
		t.Fatalf("launch missing external id: %+v", receipt)
	}
	owned, err := ad.ListOwned(context.Background(), adapter.OwnershipQuery{WorkspaceID: req.WorkspaceID})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) == 0 {
		t.Fatalf("expected owned integration container")
	}
	released, err := ad.Release(context.Background(), adapter.ReleaseRequest{OperationKey: "release_" + req.LaunchKey, RequestHash: "sha256:release", LaunchKey: req.LaunchKey})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !released.Released {
		t.Fatalf("expected release receipt: %+v", released)
	}
}

func TestAdapterLaunchIsIdempotentByDeterministicName(t *testing.T) {
	client := newFakeClient()
	ad := New(client)
	req := launchRequest()
	first, err := ad.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("first launch: %v", err)
	}
	second, err := ad.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("second launch: %v", err)
	}
	if first.ExternalID != second.ExternalID || !second.Duplicate || len(client.created) != 1 {
		t.Fatalf("expected idempotent launch, first=%+v second=%+v creates=%d", first, second, len(client.created))
	}
}

func launchRequest() adapter.LaunchRequest {
	entrypoint := []string{"/bin/app"}
	literal := "info"
	return adapter.LaunchRequest{
		OperationKey:              "launch_1",
		RequestHash:               "sha256:launch",
		WorkspaceID:               "ws_1",
		RunID:                     "run_1",
		AttemptID:                 "att_1",
		WorkloadID:                "wrk_1",
		WorkloadRevisionID:        "wrev_1",
		OwnershipToken:            "own_1",
		LaunchKey:                 "launch_key_1",
		CleanupLocator:            "cleanup_1",
		Image:                     "ghcr.io/acme/app@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Platform:                  domain.Platform{OS: "linux", Architecture: "amd64"},
		Entrypoint:                &entrypoint,
		Args:                      []string{"--serve"},
		Environment:               []adapter.EnvironmentBinding{{Name: "LOG_LEVEL", Value: &literal}, {Name: "API_TOKEN", SecretRef: &domain.SecretReference{Name: "api-token", Version: 2}}},
		Ports:                     []domain.PortSpec{{Name: "http", ContainerPort: 8080, Protocol: "tcp", Exposure: domain.PortExposurePublic}},
		Resources:                 domain.ResourceRequirements{CPU: domain.CPURequirement{MinMillis: 500}, Memory: domain.MemoryRequirement{MinBytes: 256 << 20}},
		SelectedOfferSnapshotID:   "offer_1",
		SelectedOfferConnectionID: "conn_1",
		SelectedOfferAdapterType:  "docker",
		SelectedOfferNativeRef:    "local",
	}
}

type fakeClient struct {
	created []CreateContainerRequest
	objects map[string]Container
}

func newFakeClient() *fakeClient {
	return &fakeClient{objects: map[string]Container{}}
}

func (f *fakeClient) CreateContainer(_ context.Context, req CreateContainerRequest) (Container, error) {
	if existing, ok := f.objects[req.Name]; ok {
		return existing, ErrAlreadyExists
	}
	container := Container{ID: "docker-" + req.Name, Name: req.Name, Labels: req.Labels, State: "running", CreatedAt: time.Now().UTC()}
	f.objects[req.Name] = container
	f.created = append(f.created, req)
	return container, nil
}

func (f *fakeClient) InspectContainer(_ context.Context, name string) (Container, error) {
	container, ok := f.objects[name]
	if !ok {
		return Container{}, ErrNotFound
	}
	return container, nil
}

func (f *fakeClient) RemoveContainer(_ context.Context, name string) error {
	delete(f.objects, name)
	return nil
}

func (f *fakeClient) ListContainers(_ context.Context, labels map[string]string) ([]Container, error) {
	var containers []Container
	for _, container := range f.objects {
		match := true
		for key, value := range labels {
			if container.Labels[key] != value {
				match = false
				break
			}
		}
		if match {
			containers = append(containers, container)
		}
	}
	return containers, nil
}
