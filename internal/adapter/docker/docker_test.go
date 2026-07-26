package docker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/benngarcia/mercator/internal/adapter"
	"github.com/benngarcia/mercator/internal/domain"
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
	if len(client.started) != 1 || client.started[0] != req.LaunchKey {
		t.Fatalf("launch did not start created container: %+v", client.started)
	}
	if created.Env["LOG_LEVEL"] != "info" {
		t.Fatalf("unexpected env mapping: %+v", created.Env)
	}

	observation, err := ad.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: req.LaunchKey, OwnershipToken: req.OwnershipToken, RequestHash: req.RequestHash})
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
	released, err := ad.Release(context.Background(), adapter.ReleaseRequest{OperationKey: "release_1", RequestHash: "sha256:release", LaunchKey: req.LaunchKey, OwnershipToken: req.OwnershipToken, LaunchRequestHash: req.RequestHash})
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
	// The platform is this machine's, because the container runs on this machine.
	// Naming arm64 outright was an assumption from the laptop this case was written
	// on: on an amd64 host the daemon reports the local image missing and goes to
	// the registry for a build it will never run.
	req.Platform = domain.Platform{OS: "linux", Architecture: runtime.GOARCH}
	req.LaunchKey = "mercator-integration-" + time.Now().UTC().Format("20060102150405")
	req.OperationKey = req.LaunchKey
	req.CleanupLocator = req.LaunchKey
	req.Entrypoint = nil
	req.Args = []string{"sleep", "5"}
	ad := New(NewCLIClient(""))
	t.Cleanup(func() {
		_, _ = ad.Release(context.Background(), adapter.ReleaseRequest{OperationKey: "cleanup_" + req.LaunchKey, RequestHash: "sha256:cleanup", LaunchKey: req.LaunchKey, OwnershipToken: req.OwnershipToken, LaunchRequestHash: req.RequestHash})
	})
	receipt, err := ad.Launch(context.Background(), req)
	if err != nil {
		// A registry that will not serve this machine the image is an environment
		// this case cannot be evaluated in, and reporting it as a launch defect
		// describes the wrong thing. Anything else is the adapter.
		if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "Too Many Requests") {
			t.Skipf("the registry will not serve this machine %s: %v", image, err)
		}
		t.Fatalf("live launch: %v", err)
	}
	if receipt.ExternalID == "" {
		t.Fatalf("launch missing external id: %+v", receipt)
	}
	observation, err := ad.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: req.LaunchKey, OwnershipToken: req.OwnershipToken, RequestHash: req.RequestHash})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observation.Phase != adapter.ExternalPhaseRunning {
		t.Fatalf("expected running live container after launch, got %+v", observation)
	}
	// The two moments a start latency is the difference between, both off a real
	// daemon and neither of them Mercator's. A test double agreeing with itself
	// about a field it writes proves nothing about the line that parses what this
	// engine actually printed.
	if observation.StartedAt == nil {
		t.Fatalf("the live daemon gave this container a process and the observation reports no start: %+v", observation)
	}
	if observation.StartedAt.After(observation.ObservedAt) {
		t.Fatalf("the live start %s is after the read %s that carried it",
			observation.StartedAt.Format(time.RFC3339Nano), observation.ObservedAt.Format(time.RFC3339Nano))
	}
	if observation.StartedAt.Before(receipt.AcceptedAt) {
		t.Fatalf("the live start %s is before the launch was accepted at %s, so its start latency is negative",
			observation.StartedAt.Format(time.RFC3339Nano), receipt.AcceptedAt.Format(time.RFC3339Nano))
	}
	if stated := inspectField(t, req.LaunchKey, "{{.State.StartedAt}}"); !observation.StartedAt.Equal(momentStated(t, stated)) {
		t.Fatalf("the observation reports %s and this daemon says %s",
			observation.StartedAt.Format(time.RFC3339Nano), stated)
	}
	if stated := inspectField(t, req.LaunchKey, "{{.Created}}"); !receipt.AcceptedAt.Equal(momentStated(t, stated)) {
		t.Fatalf("the receipt was accepted at %s and this daemon made the container at %s",
			receipt.AcceptedAt.Format(time.RFC3339Nano), stated)
	}
	owned, err := ad.ListOwned(context.Background(), adapter.OwnershipQuery{WorkspaceID: req.WorkspaceID})
	if err != nil {
		t.Fatalf("list owned: %v", err)
	}
	if len(owned) == 0 {
		t.Fatalf("expected owned integration container")
	}
	released, err := ad.Release(context.Background(), adapter.ReleaseRequest{OperationKey: "release_" + req.LaunchKey, RequestHash: "sha256:release", LaunchKey: req.LaunchKey, OwnershipToken: req.OwnershipToken, LaunchRequestHash: req.RequestHash})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !released.Released {
		t.Fatalf("expected release receipt: %+v", released)
	}
}

// inspectField and momentStated ask the daemon directly, so the live case compares
// the adapter's answer against the engine's own words rather than against another
// copy of the adapter's parsing.
func inspectField(t *testing.T, name, format string) string {
	t.Helper()
	output, err := exec.Command("docker", "inspect", "-f", format, name).Output()
	if err != nil {
		t.Fatalf("docker inspect -f %s %s: %v", format, name, err)
	}
	return strings.TrimSpace(string(output))
}

func momentStated(t *testing.T, stated string) time.Time {
	t.Helper()
	moment, err := time.Parse(time.RFC3339Nano, stated)
	if err != nil {
		t.Fatalf("this daemon states %q, which is not a moment: %v", stated, err)
	}
	return moment.UTC()
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
	if len(client.started) != 1 {
		t.Fatalf("duplicate launch should not restart running container: %+v", client.started)
	}
}

func TestAdapterLaunchRejectsForeignContainerWithSameName(t *testing.T) {
	client := newFakeClient()
	ad := New(client)
	req := launchRequest()
	client.objects[req.LaunchKey] = Container{
		ID:     "docker-foreign",
		Name:   req.LaunchKey,
		Labels: map[string]string{"mercator.workspace_id": "ws_other"},
		State:  "running",
	}

	_, err := ad.Launch(context.Background(), req)
	if !errors.Is(err, adapter.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict for foreign duplicate, got %v", err)
	}
}

func TestAdapterLaunchTreatsStartFailureAfterCreateAsIndeterminate(t *testing.T) {
	client := newFakeClient()
	client.startErr = errors.New("docker daemon disconnected")
	ad := New(client)

	_, err := ad.Launch(context.Background(), launchRequest())

	if !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("launch error = %v, want adapter.ErrLaunchIndeterminate", err)
	}
}

func TestAdapterLaunchTreatsInspectionFailureAfterCreateAsIndeterminate(t *testing.T) {
	client := newFakeClient()
	client.inspectErr = errors.New("docker daemon disconnected")
	ad := New(client)

	_, err := ad.Launch(context.Background(), launchRequest())

	if !errors.Is(err, adapter.ErrLaunchIndeterminate) {
		t.Fatalf("launch error = %v, want adapter.ErrLaunchIndeterminate", err)
	}
}

func TestAdapterObserveAndReleaseRejectForeignContainerWithSameName(t *testing.T) {
	client := newFakeClient()
	ad := New(client)
	req := launchRequest()
	client.objects[req.LaunchKey] = Container{
		ID:     "docker-foreign",
		Name:   req.LaunchKey,
		Labels: map[string]string{"mercator.ownership_token": "other", "mercator.request_hash": "sha256:other"},
		State:  "running",
	}

	_, err := ad.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: req.LaunchKey, OwnershipToken: req.OwnershipToken, RequestHash: req.RequestHash})
	if !errors.Is(err, adapter.ErrIdempotencyConflict) {
		t.Fatalf("expected observe ownership conflict, got %v", err)
	}
	_, err = ad.Release(context.Background(), adapter.ReleaseRequest{OperationKey: "release_foreign", RequestHash: "sha256:release", LaunchKey: req.LaunchKey, OwnershipToken: req.OwnershipToken, LaunchRequestHash: req.RequestHash})
	if !errors.Is(err, adapter.ErrIdempotencyConflict) {
		t.Fatalf("expected release ownership conflict, got %v", err)
	}
	if _, ok := client.objects[req.LaunchKey]; !ok {
		t.Fatalf("foreign container should not be removed")
	}
}

func TestAdapterObserveAndReleaseRequireOwnershipMaterial(t *testing.T) {
	client := newFakeClient()
	ad := New(client)
	req := launchRequest()
	if _, err := ad.Launch(context.Background(), req); err != nil {
		t.Fatalf("launch: %v", err)
	}

	if _, err := ad.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: req.LaunchKey}); !errors.Is(err, adapter.ErrIdempotencyConflict) {
		t.Fatalf("expected observe ownership material conflict, got %v", err)
	}
	if _, err := ad.Release(context.Background(), adapter.ReleaseRequest{OperationKey: "release_no_owner", RequestHash: "sha256:release", LaunchKey: req.LaunchKey}); !errors.Is(err, adapter.ErrIdempotencyConflict) {
		t.Fatalf("expected release ownership material conflict, got %v", err)
	}
}

func TestAdapterReleaseIsIdempotentWhenContainerAlreadyRemoved(t *testing.T) {
	ad := New(newFakeClient())

	released, err := ad.Release(context.Background(), adapter.ReleaseRequest{OperationKey: "release_missing", RequestHash: "sha256:release", LaunchKey: "missing"})
	if err != nil {
		t.Fatalf("release missing container: %v", err)
	}
	if !released.Released {
		t.Fatalf("expected idempotent release receipt, got %+v", released)
	}
}

// Local Docker is a STANDING pool: there is no broker-owned host to destroy, so
// Terminate is an explicit, contract-documented error rather than a silent
// no-op or container removal.
func TestAdapterTerminateIsUnsupportedForStandingPool(t *testing.T) {
	ad := New(newFakeClient())

	_, err := ad.Terminate(context.Background(), adapter.TerminateRequest{OperationKey: "terminate_1", RequestHash: "sha256:terminate", LaunchKey: "any"})
	if !errors.Is(err, adapter.ErrTerminateUnsupported) {
		t.Fatalf("expected ErrTerminateUnsupported, got %v", err)
	}
}

func TestPhaseFromStateDoesNotMarkCreatedContainerRunning(t *testing.T) {
	if phase := phaseFromState("created", nil); phase != adapter.ExternalPhaseQueued {
		t.Fatalf("created container should be queued, got %s", phase)
	}
}

// Docker inspect reports .State.ExitCode == 0 for containers that are still
// running; surfacing it let event consumers treat a live container as
// exited. The observation must omit the exit code until the phase is an
// actual exit.
func TestObserveOmitsExitCodeUntilExited(t *testing.T) {
	client := newFakeClient()
	ad := New(client)
	req := launchRequest()
	if _, err := ad.Launch(context.Background(), req); err != nil {
		t.Fatalf("launch: %v", err)
	}

	running := client.objects[req.LaunchKey]
	running.State = "running"
	zero := 0
	running.ExitCode = &zero
	client.objects[req.LaunchKey] = running

	observation, err := ad.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: req.LaunchKey, OwnershipToken: req.OwnershipToken, RequestHash: req.RequestHash})
	if err != nil {
		t.Fatalf("observe running: %v", err)
	}
	if observation.Phase != adapter.ExternalPhaseRunning || observation.ExitCode != nil {
		t.Fatalf("running observation must carry no exit code: %+v", observation)
	}

	exited := client.objects[req.LaunchKey]
	exited.State = "exited"
	client.objects[req.LaunchKey] = exited

	observation, err = ad.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: req.LaunchKey, OwnershipToken: req.OwnershipToken, RequestHash: req.RequestHash})
	if err != nil {
		t.Fatalf("observe exited: %v", err)
	}
	if observation.Phase != adapter.ExternalPhaseSucceeded || observation.ExitCode == nil || *observation.ExitCode != 0 {
		t.Fatalf("exited observation must carry the exit code: %+v", observation)
	}
}

// TestObserveReportsWhenTheDaemonGaveTheContainerAProcess is the observation's
// start moment. A provider reports running from the moment it accepts a launch, so
// the phase can never establish when a workload began, and predicted start latency
// is calibrated against started minus accepted: this is the only field that
// subtraction can be made from. A container the daemon created and never ran
// carries no start, because zero is the epoch and not an instant a workload began.
func TestObserveReportsWhenTheDaemonGaveTheContainerAProcess(t *testing.T) {
	client := newFakeClient()
	ad := New(client)
	req := launchRequest()
	if _, err := ad.Launch(context.Background(), req); err != nil {
		t.Fatalf("launch: %v", err)
	}
	observe := adapter.ObserveRequest{LaunchKey: req.LaunchKey, OwnershipToken: req.OwnershipToken, RequestHash: req.RequestHash}

	observation, err := ad.Observe(context.Background(), observe)

	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	started := client.objects[req.LaunchKey].StartedAt
	if observation.StartedAt == nil || !observation.StartedAt.Equal(started) {
		t.Fatalf("the observation reports %v as the start and the daemon says %s", observation.StartedAt, started.Format(time.RFC3339Nano))
	}
	if observation.StartedAt.After(observation.ObservedAt) {
		t.Fatalf("the reported start %s is after the moment of the read %s, so nothing observed it",
			observation.StartedAt.Format(time.RFC3339Nano), observation.ObservedAt.Format(time.RFC3339Nano))
	}

	created := client.objects[req.LaunchKey]
	created.State = "created"
	created.StartedAt = time.Time{}
	client.objects[req.LaunchKey] = created

	observation, err = ad.Observe(context.Background(), observe)

	if err != nil {
		t.Fatalf("observe a created container: %v", err)
	}
	if observation.StartedAt != nil {
		t.Fatalf("a container that never ran reports a start of %s", observation.StartedAt.Format(time.RFC3339Nano))
	}
}

// TestLaunchReportsTheMomentTheDaemonTookTheLaunch is the other half of the
// subtraction. A start latency is started minus accepted, so an accepted moment
// stamped with Mercator's clock after the call returned is later than the start
// the same daemon reports, and the measurement is negative for every container in
// this lane. It is negative by the whole retry gap for a launch that resolves as a
// duplicate: the container was made and given a process by the first attempt, and
// only the accepted moment would move.
func TestLaunchReportsTheMomentTheDaemonTookTheLaunch(t *testing.T) {
	client := newFakeClient()
	ad := New(client)
	req := launchRequest()

	receipt, err := ad.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	observation, err := ad.Observe(context.Background(), adapter.ObserveRequest{LaunchKey: req.LaunchKey, OwnershipToken: req.OwnershipToken, RequestHash: req.RequestHash})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	made := client.objects[req.LaunchKey].CreatedAt
	if !receipt.AcceptedAt.Equal(made) {
		t.Fatalf("the receipt was accepted at %s and the daemon made the container at %s",
			receipt.AcceptedAt.Format(time.RFC3339Nano), made.Format(time.RFC3339Nano))
	}
	if observation.StartedAt.Before(receipt.AcceptedAt) {
		t.Fatalf("the container started at %s, before the launch was accepted at %s, so its start latency is negative",
			observation.StartedAt.Format(time.RFC3339Nano), receipt.AcceptedAt.Format(time.RFC3339Nano))
	}

	retried, err := ad.Launch(context.Background(), req)

	if err != nil {
		t.Fatalf("retry the same launch: %v", err)
	}
	if !retried.Duplicate {
		t.Fatalf("the second launch of %q resolved as a new container: %+v", req.LaunchKey, retried)
	}
	if !retried.AcceptedAt.Equal(receipt.AcceptedAt) {
		t.Fatalf("the retry re-dated the acceptance to %s, and the container it resolved to was taken at %s",
			retried.AcceptedAt.Format(time.RFC3339Nano), receipt.AcceptedAt.Format(time.RFC3339Nano))
	}
}

// TestContainerFromInspectReadsTheDaemonsOwnMoments reads a payload this host's
// Docker Engine actually produced. The moments in it are the only place a start
// latency can come from in this lane, and they arrive as strings: a daemon that
// states one in a form this adapter cannot read is a daemon it does not
// understand, not a container that never started. Reading it as the zero moment
// would publish no start for the whole lane and degrade every start-latency row to
// unobserved silently.
func TestContainerFromInspectReadsTheDaemonsOwnMoments(t *testing.T) {
	container, err := containerFromInspect([]byte(runningContainerInspectPayload))

	if err != nil {
		t.Fatalf("read a running container: %v", err)
	}
	made := time.Date(2026, 7, 26, 11, 56, 20, 618952831, time.UTC)
	given := time.Date(2026, 7, 26, 11, 56, 20, 807652173, time.UTC)
	if !container.CreatedAt.Equal(made) || !container.StartedAt.Equal(given) {
		t.Fatalf("the daemon made the container at %s and gave it a process at %s, and this reads %s and %s",
			made.Format(time.RFC3339Nano), given.Format(time.RFC3339Nano),
			container.CreatedAt.Format(time.RFC3339Nano), container.StartedAt.Format(time.RFC3339Nano))
	}
	if container.Name != "mercator-fixture-probe" || container.State != "running" || container.Labels["mercator.launch_key"] != "lk1" {
		t.Fatalf("unexpected container: %+v", container)
	}
}

func TestContainerFromInspectSaysTheEpochIsNoStartAtAll(t *testing.T) {
	container, err := containerFromInspect([]byte(createdContainerInspectPayload))

	if err != nil {
		t.Fatalf("read a created container: %v", err)
	}
	if !container.StartedAt.IsZero() {
		t.Fatalf("a container the daemon never ran reports a start of %s", container.StartedAt.Format(time.RFC3339Nano))
	}
}

// TestContainerFromInspectRefusesAMomentItCannotRead covers both moments this
// payload carries, because both are load-bearing and only one of them was held.
// Reading State.StartedAt as the zero moment reports a running container as never
// started and publishes no start for the whole lane. Reading Created as the zero
// moment is worse: Launch returns it as the launch's accepted moment, and
// invalidLaunchReceipt refuses a receipt with no acceptance, so every reduce of that
// Run's stream wedges. A daemon states both in one format, so a case that rewrites
// one and not the other leaves half the promise unbreakable.
func TestContainerFromInspectRefusesAMomentItCannotRead(t *testing.T) {
	unreadable := map[string]string{
		"State.StartedAt": `"2026-07-26T11:56:20.807652173Z"`,
		"Created":         `"2026-07-26T11:56:20.618952831Z"`,
	}

	for field, stated := range unreadable {
		t.Run(field, func(t *testing.T) {
			payload := strings.Replace(runningContainerInspectPayload, stated, `"Sun Jul 26 11:56:20 2026"`, 1)

			_, err := containerFromInspect([]byte(payload))

			if err == nil {
				t.Fatalf("a daemon whose %s this adapter cannot read was read anyway", field)
			}
			if !strings.Contains(err.Error(), field) {
				t.Fatalf("the error does not name the field the daemon stated unreadably: %v", err)
			}
		})
	}
}

// runningContainerInspectPayload and createdContainerInspectPayload are trimmed
// captures of `docker inspect` from Docker Engine 29.6.2: one container the daemon
// gave a process 188ms after making it, and one it made and never ran. The epoch is
// how the daemon says a container has never started.
const runningContainerInspectPayload = `[
  {
    "Id": "c2265cf568f5ff81c470d39a6ae35742881e2e86ad969f15f46238ed7c5386a6",
    "Created": "2026-07-26T11:56:20.618952831Z",
    "Name": "/mercator-fixture-probe",
    "State": {
      "Status": "running",
      "Running": true,
      "ExitCode": 0,
      "StartedAt": "2026-07-26T11:56:20.807652173Z",
      "FinishedAt": "0001-01-01T00:00:00Z"
    },
    "Config": {"Labels": {"mercator.launch_key": "lk1"}}
  }
]`

const createdContainerInspectPayload = `[
  {
    "Id": "f1d3e1e0a1b24c4f8f5a6d7c8b9a0e1f2d3c4b5a69788796a5b4c3d2e1f00112",
    "Created": "2026-07-26T11:56:29.784471331Z",
    "Name": "/mercator-created-probe",
    "State": {
      "Status": "created",
      "Running": false,
      "ExitCode": 0,
      "StartedAt": "0001-01-01T00:00:00Z",
      "FinishedAt": "0001-01-01T00:00:00Z"
    },
    "Config": {"Labels": {"mercator.launch_key": "lk1"}}
  }
]`

func TestPhaseFromStateUsesExitCode(t *testing.T) {
	if phase := phaseFromState("exited", intPtr(0)); phase != adapter.ExternalPhaseSucceeded {
		t.Fatalf("exit 0 should succeed, got %s", phase)
	}
	if phase := phaseFromState("exited", intPtr(42)); phase != adapter.ExternalPhaseFailed {
		t.Fatalf("nonzero exit should fail, got %s", phase)
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
		Environment:               []adapter.EnvironmentBinding{{Name: "LOG_LEVEL", Value: &literal}},
		Ports:                     []domain.PortSpec{{Name: "http", ContainerPort: 8080, Protocol: "tcp", Exposure: domain.PortExposurePublic}},
		Resources:                 domain.ResourceRequirements{CPU: domain.CPURequirement{MinMillis: 500}, Memory: domain.MemoryRequirement{MinBytes: 256 << 20}},
		SelectedOfferSnapshotID:   "offer_1",
		SelectedOfferConnectionID: "conn_1",
		SelectedOfferAdapterType:  "docker",
		SelectedOfferNativeRef:    "local",
	}
}

type fakeClient struct {
	created    []CreateContainerRequest
	started    []string
	objects    map[string]Container
	startErr   error
	inspectErr error
}

func newFakeClient() *fakeClient {
	return &fakeClient{objects: map[string]Container{}}
}

func (f *fakeClient) CreateContainer(_ context.Context, req CreateContainerRequest) (string, error) {
	if existing, ok := f.objects[req.Name]; ok {
		return existing.ID, ErrAlreadyExists
	}
	// A container this daemon made a second ago, so the moment it was then given a
	// process is in the past by the time anything observes it. A fake that created
	// everything "now" would let an observation reporting its own read moment pass.
	container := Container{ID: "docker-" + req.Name, Name: req.Name, Labels: req.Labels, State: "created", CreatedAt: time.Now().UTC().Add(-time.Second)}
	f.objects[req.Name] = container
	f.created = append(f.created, req)
	return container.ID, nil
}

func (f *fakeClient) StartContainer(_ context.Context, name string) error {
	if f.startErr != nil {
		return f.startErr
	}
	container, ok := f.objects[name]
	if !ok {
		return ErrNotFound
	}
	container.State = "running"
	// Starting a container is when the daemon gives it a process, which is the
	// moment State.StartedAt records and a moment later than its creation.
	container.StartedAt = container.CreatedAt.Add(200 * time.Millisecond)
	f.objects[name] = container
	f.started = append(f.started, name)
	return nil
}

func (f *fakeClient) InspectContainer(_ context.Context, name string) (Container, error) {
	if f.inspectErr != nil {
		return Container{}, f.inspectErr
	}
	container, ok := f.objects[name]
	if !ok {
		return Container{}, ErrNotFound
	}
	return container, nil
}

func (f *fakeClient) RemoveContainer(_ context.Context, name string) error {
	if _, ok := f.objects[name]; !ok {
		return ErrNotFound
	}
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

func intPtr(value int) *int {
	return &value
}

func TestLaunchPassesGPUCountForAcceleratorWorkloads(t *testing.T) {
	client := newFakeClient()
	ad := New(client)
	req := launchRequest()
	req.Resources.Accelerators = []domain.AcceleratorRequirement{{Vendor: "nvidia", ModelAnyOf: []string{"nvidia-rtx-5090"}, Count: 1}}

	if _, err := ad.Launch(context.Background(), req); err != nil {
		t.Fatalf("launch: %v", err)
	}

	if client.created[0].GPUCount != 1 {
		t.Fatalf("GPUCount = %d, want 1 (--gpus passthrough of the accelerator requirement)", client.created[0].GPUCount)
	}
}

func TestLaunchGrantsNoGPUAccessWithoutAcceleratorRequirement(t *testing.T) {
	client := newFakeClient()
	ad := New(client)

	if _, err := ad.Launch(context.Background(), launchRequest()); err != nil {
		t.Fatalf("launch: %v", err)
	}

	if client.created[0].GPUCount != 0 {
		t.Fatalf("GPUCount = %d, want 0 (no GPU access unless the workload asked)", client.created[0].GPUCount)
	}
}

// TestIntegrationOneDaemonReachedTwoWaysIsOneMachine is the live half of the
// machine handle, and it is a live case because the fact it rests on is one only
// an engine can state. It reaches this host's daemon twice, once through the
// ambient endpoint and once through a docker context created for the test, and
// the two routes have to name one machine: the endpoint identity calls them
// "loopback" and the context name, so anything derived from the endpoint says
// they are two machines, and a launch history keyed that way orphans this host's
// samples the moment an operator changes how Mercator reaches it.
func TestIntegrationOneDaemonReachedTwoWaysIsOneMachine(t *testing.T) {
	if os.Getenv("MERCATOR_DOCKER_INTEGRATION") != "1" {
		t.Skip("set MERCATOR_DOCKER_INTEGRATION=1 to run live Docker adapter integration")
	}
	now := time.Now().UTC()
	ambient := NewCLIClient("")
	info, err := ambient.Info(context.Background())
	if err != nil {
		t.Fatalf("live docker info: %v", err)
	}
	if info.ID == "" {
		t.Fatalf("this engine states no ID, so nothing in its answer names the machine: %+v", info)
	}

	viaContext := &CLIClient{Binary: "docker", Context: liveContextTo(t, ambient)}
	relabelled, err := viaContext.Info(context.Background())
	if err != nil {
		t.Fatalf("live docker info through a context: %v", err)
	}

	if relabelled.ID != info.ID {
		t.Fatalf("one daemon named two machines, %q and %q", info.ID, relabelled.ID)
	}
	direct := StandingOffer(DeriveIdentity("", ""), "", info, 0, nil, now)
	labelled := StandingOffer(DeriveIdentity("", viaContext.Context), "", relabelled, 0, nil, now)
	if direct.ID == labelled.ID {
		t.Fatalf("this case is about two listings of one machine; both are %q", direct.ID)
	}
	directKey := domain.CandidateIdentityOf(aggregated(direct), "sha256:image").Candidate(true)
	labelledKey := domain.CandidateIdentityOf(aggregated(labelled), "sha256:image").Candidate(true)
	// This engine's own ID is in the key, checked before the two keys are compared:
	// two keys agreeing because neither names a machine is how this case passes
	// against a daemon it never reached.
	if !strings.Contains(directKey, "machine="+info.ID) {
		t.Fatalf("key %q does not name the engine %q that answered", directKey, info.ID)
	}
	if directKey != labelledKey {
		t.Fatalf("one machine keyed two ways:\n%s\n%s", directKey, labelledKey)
	}
	if strings.Contains(directKey, viaContext.Context) || strings.Contains(directKey, "loopback") {
		t.Fatalf("key %q names how Mercator reached the machine", directKey)
	}
}

// liveContextTo is a second route to the daemon the ambient endpoint reaches: a
// docker context pointing at the same socket, which is the change an operator
// makes when a host stops being reachable the way it was.
func liveContextTo(t *testing.T, ambient *CLIClient) string {
	t.Helper()
	endpoint, err := ambient.run(context.Background(), "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	if err != nil {
		t.Fatalf("read the ambient endpoint: %v: %s", err, endpoint)
	}
	name := "mercator-machine-" + time.Now().UTC().Format("20060102150405")
	if output, err := ambient.run(context.Background(),
		"context", "create", name, "--docker", "host="+strings.TrimSpace(endpoint)); err != nil {
		t.Fatalf("create a second route to this daemon: %v: %s", err, output)
	}
	t.Cleanup(func() {
		_, _ = ambient.run(context.Background(), "context", "rm", "-f", name)
	})
	return name
}
