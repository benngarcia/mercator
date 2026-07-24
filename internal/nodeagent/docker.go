package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/capability"
)

// DockerRuntime executes workloads through the local Docker daemon. It is the
// first Runtime implementation, and it stays behind the narrow Runtime
// interface so containerd or another OCI runtime can replace it without the
// control-plane contract moving.
//
// The daemon is reached over its local socket by this process only. Nothing
// exposes it to the network, and the control plane never talks to it.
type DockerRuntime struct {
	binary string
	now    func() time.Time
	// labelPrefix namespaces the labels the agent stamps on its containers, so
	// Observe reports Mercator's workloads and nothing else running on the box.
	labelPrefix string
}

func NewDockerRuntime(binary string) *DockerRuntime {
	if binary == "" {
		binary = "docker"
	}
	return &DockerRuntime{binary: binary, now: time.Now, labelPrefix: "mercator."}
}

func (docker *DockerRuntime) Facts(ctx context.Context) (capability.NodeFacts, error) {
	facts := capability.NodeFacts{ObservedAt: docker.now().UTC()}
	info, err := docker.info(ctx)
	if err != nil {
		return capability.NodeFacts{}, err
	}
	facts.Host = capability.HostFacts{
		OS:               info.OperatingSystem,
		KernelVersion:    info.KernelVersion,
		Architecture:     dockerArchitecture(info.Architecture),
		ContainerRuntime: "docker",
		RuntimeVersion:   info.ServerVersion,
		CPUMillis:        int64(info.NCPU) * 1000,
		MemoryBytes:      info.MemTotal,
	}
	if slices.Contains(info.runtimeNames(), "nvidia") {
		facts.Host.AcceleratorToolkit = "nvidia-container-toolkit"
	}
	images, err := docker.images(ctx)
	if err != nil {
		return capability.NodeFacts{}, err
	}
	facts.Images = images
	return facts, nil
}

// PrepareImage pulls an image by its exact manifest digest. A tag is never
// image identity, so the reference the control plane sends is already pinned.
func (docker *DockerRuntime) PrepareImage(ctx context.Context, command capability.PrepareImageCommand) error {
	reference := command.Reference
	if reference == "" {
		return fmt.Errorf("prepare image: no digest-pinned reference to pull")
	}
	if _, err := docker.run(ctx, "pull", reference); err != nil {
		return fmt.Errorf("pull %s: %w", reference, err)
	}
	return nil
}

// PrepareArtifact is not implemented by the Docker runtime. Artifact
// replication is phase 3 of the migration, and claiming it here would let
// Placement believe in locality nothing produces.
func (docker *DockerRuntime) PrepareArtifact(context.Context, capability.PrepareArtifactCommand) error {
	return fmt.Errorf("%w: this node does not replicate Artifacts yet", capability.ErrCapabilityUnsupported)
}

// LaunchWorkload starts one container and returns once it is running. The
// container's name is derived from the run and attempt, so the daemon itself
// refuses a second container for the same attempt even if the agent's own
// memory were lost.
func (docker *DockerRuntime) LaunchWorkload(ctx context.Context, command capability.LaunchWorkloadCommand) error {
	if len(command.Workload.Containers) == 0 {
		return fmt.Errorf("launch workload: the workload declares no container")
	}
	container := command.Workload.Containers[0]
	image := container.Image
	if command.ManifestDigest != "" {
		image = command.ManifestDigest
		if reference, _, found := strings.Cut(container.Image, "@"); found {
			image = reference + "@" + command.ManifestDigest
		}
	}
	args := []string{"run", "--detach", "--name", docker.containerName(command.RunID, command.AttemptID)}
	args = append(args,
		"--label", docker.labelPrefix+"run="+command.RunID,
		"--label", docker.labelPrefix+"attempt="+command.AttemptID,
		"--label", docker.labelPrefix+"booking="+command.BookingID,
	)
	for _, binding := range command.Environment {
		if binding.Value == nil {
			continue
		}
		args = append(args, "--env", binding.Name+"="+*binding.Value)
	}
	if command.MaxRuntimeSeconds > 0 {
		args = append(args, "--stop-timeout", strconv.FormatInt(command.MaxRuntimeSeconds, 10))
	}
	if container.Entrypoint != nil {
		args = append(args, "--entrypoint", strings.Join(*container.Entrypoint, " "))
	}
	args = append(args, image)
	args = append(args, container.Args...)
	if _, err := docker.run(ctx, args...); err != nil {
		return fmt.Errorf("run workload for %s: %w", command.RunID, err)
	}
	return nil
}

func (docker *DockerRuntime) StopWorkload(ctx context.Context, command capability.StopWorkloadCommand) error {
	grace := command.GraceSeconds
	if grace <= 0 {
		grace = 10
	}
	name := docker.containerName(command.RunID, "")
	containers, err := docker.containers(ctx)
	if err != nil {
		return err
	}
	for _, container := range containers {
		if container.runID() != command.RunID {
			continue
		}
		name = container.Names
		break
	}
	if _, err := docker.run(ctx, "stop", "--timeout", strconv.FormatInt(grace, 10), name); err != nil {
		return fmt.Errorf("stop workload for %s: %w", command.RunID, err)
	}
	return nil
}

// Observe reports every Mercator container this daemon knows about, running or
// exited. It is the node's own authority: the control plane learns that a
// process ended here, whatever the application did or did not report.
func (docker *DockerRuntime) Observe(ctx context.Context) ([]capability.WorkloadObservation, error) {
	containers, err := docker.containers(ctx)
	if err != nil {
		return nil, err
	}
	observations := make([]capability.WorkloadObservation, 0, len(containers))
	for _, container := range containers {
		observation := capability.WorkloadObservation{
			RunID:      container.runID(),
			AttemptID:  container.attemptID(),
			Phase:      dockerPhase(container.State),
			ObservedAt: docker.now().UTC(),
		}
		if observation.Phase.Exited() {
			code := container.exitCode()
			observation.ExitCode = &code
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

func (docker *DockerRuntime) containerName(runID, attemptID string) string {
	name := "mercator-" + runID
	if attemptID != "" {
		name += "-" + attemptID
	}
	return name
}

type dockerInfo struct {
	OperatingSystem string `json:"OperatingSystem"`
	KernelVersion   string `json:"KernelVersion"`
	Architecture    string `json:"Architecture"`
	ServerVersion   string `json:"ServerVersion"`
	NCPU            int    `json:"NCPU"`
	MemTotal        int64  `json:"MemTotal"`
	Runtimes        map[string]struct {
		Path string `json:"path"`
	} `json:"Runtimes"`
}

func (info dockerInfo) runtimeNames() []string {
	names := make([]string, 0, len(info.Runtimes))
	for name := range info.Runtimes {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (docker *DockerRuntime) info(ctx context.Context) (dockerInfo, error) {
	out, err := docker.run(ctx, "info", "--format", "{{json .}}")
	if err != nil {
		return dockerInfo{}, fmt.Errorf("docker info: %w", err)
	}
	var info dockerInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return dockerInfo{}, fmt.Errorf("decode docker info: %w", err)
	}
	return info, nil
}

// images reports the exact manifest digests this machine holds, which is what
// image locality has to be measured in. A tag would say nothing about content.
func (docker *DockerRuntime) images(ctx context.Context) ([]capability.ImageLocality, error) {
	out, err := docker.run(ctx, "images", "--digests", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker images: %w", err)
	}
	var locality []capability.ImageLocality
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var image struct {
			Digest string `json:"Digest"`
			Size   string `json:"Size"`
		}
		if err := json.Unmarshal([]byte(line), &image); err != nil {
			return nil, fmt.Errorf("decode docker image: %w", err)
		}
		if image.Digest == "" || image.Digest == "<none>" {
			continue
		}
		locality = append(locality, capability.ImageLocality{
			ManifestDigest: image.Digest,
			Unpacked:       true,
			State:          capability.LocalityHot,
			LastVerifiedAt: docker.now().UTC(),
		})
	}
	return locality, nil
}

type dockerContainer struct {
	Names  string `json:"Names"`
	State  string `json:"State"`
	Status string `json:"Status"`
	Labels string `json:"Labels"`
}

func (container dockerContainer) label(name string) string {
	for pair := range strings.SplitSeq(container.Labels, ",") {
		key, value, found := strings.Cut(pair, "=")
		if found && key == name {
			return value
		}
	}
	return ""
}

func (container dockerContainer) runID() string     { return container.label("mercator.run") }
func (container dockerContainer) attemptID() string { return container.label("mercator.attempt") }

// exitCode reads the code out of Docker's status text ("Exited (137) 2m ago"),
// which is the only place the list output carries it.
func (container dockerContainer) exitCode() int {
	_, rest, found := strings.Cut(container.Status, "(")
	if !found {
		return 0
	}
	digits, _, found := strings.Cut(rest, ")")
	if !found {
		return 0
	}
	code, err := strconv.Atoi(strings.TrimSpace(digits))
	if err != nil {
		return 0
	}
	return code
}

func (docker *DockerRuntime) containers(ctx context.Context) ([]dockerContainer, error) {
	out, err := docker.run(ctx, "ps", "--all", "--no-trunc",
		"--filter", "label="+docker.labelPrefix+"run", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	var containers []dockerContainer
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var container dockerContainer
		if err := json.Unmarshal([]byte(line), &container); err != nil {
			return nil, fmt.Errorf("decode docker container: %w", err)
		}
		containers = append(containers, container)
	}
	return containers, nil
}

func dockerPhase(state string) capability.WorkloadPhase {
	switch state {
	case "created":
		return capability.WorkloadPhaseCreated
	case "running", "restarting", "paused", "removing":
		return capability.WorkloadPhaseRunning
	case "exited", "dead":
		return capability.WorkloadPhaseExited
	default:
		return capability.WorkloadPhaseAbsent
	}
}

func dockerArchitecture(reported string) string {
	switch reported {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	case "":
		return runtime.GOARCH
	default:
		return reported
	}
}

func (docker *DockerRuntime) run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, docker.binary, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", docker.binary, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

var _ Runtime = (*DockerRuntime)(nil)
