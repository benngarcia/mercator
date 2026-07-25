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
	"github.com/benngarcia/mercator/internal/domain"
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

// images reports the exact content this machine holds, which is what image
// locality has to be measured in. A tag would say nothing about it.
//
// The daemon answers in two vocabularies at once and only one of them is the
// registry's: the manifest digest an image was pulled by, and the diff IDs of
// its unpacked layers. It has no way to name the compressed blobs the registry
// served, because it discarded them, so the layers are reported as what the
// daemon can actually see and matched against a resolved manifest that carries
// both names.
//
// One image the daemon will not describe is one image reported unknown. It is
// not the whole report: a host with forty images, one of which was pruned
// between the listing and the read, holds thirty-nine images that nothing has
// stopped it from stating. Failing the report would have cost this node its
// session, and a node with no session yet its enrollment, over a fact about one
// image.
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
			ID     string `json:"ID"`
			Digest string `json:"Digest"`
		}
		if err := json.Unmarshal([]byte(line), &image); err != nil {
			return nil, fmt.Errorf("decode docker image: %w", err)
		}
		if image.Digest == "" || image.Digest == "<none>" {
			continue
		}
		described, err := docker.describe(ctx, image.ID)
		if err != nil {
			// A read that ended because this agent is shutting down says
			// nothing about the machine, so it is the report that failed.
			if ctx.Err() != nil {
				return nil, err
			}
			locality = append(locality, capability.ImageLocality{
				ManifestDigest: image.Digest,
				State:          capability.LocalityUnknown,
				LastVerifiedAt: docker.now().UTC(),
			})
			continue
		}
		locality = append(locality, capability.ImageLocality{
			ManifestDigest: image.Digest,
			Platform:       described.platform(),
			LayerDiffIDs:   described.DiffIDs,
			Unpacked:       true,
			State:          capability.LocalityHot,
			LastVerifiedAt: docker.now().UTC(),
		})
	}
	return locality, nil
}

// describedImage is what the daemon can say about one image it holds: which
// build it is, and the uncompressed layer identities it unpacked. The build
// matters because a multi-platform image is listed under one index digest
// whichever platform was pulled, so a host that fetched the arm64 build and a
// host that fetched the amd64 build report the same name for different content.
type describedImage struct {
	OS           string   `json:"os"`
	Architecture string   `json:"architecture"`
	DiffIDs      []string `json:"diff_ids"`
}

func (described describedImage) platform() domain.Platform {
	return domain.Platform{OS: described.OS, Architecture: described.Architecture}
}

// describe reads what one image is and what it is made of. An image reported
// hot with no layers is indistinguishable downstream from a host holding no
// part of it, and an image reported without its platform is one nothing can
// tell from another platform's build under the same digest, so a daemon that
// will not answer yields an unknown image rather than a confident wrong one.
func (docker *DockerRuntime) describe(ctx context.Context, imageID string) (describedImage, error) {
	if imageID == "" {
		return describedImage{}, fmt.Errorf("the daemon listed an image with no ID, so nothing can read it")
	}
	out, err := docker.run(ctx, "image", "inspect", imageID, "--format",
		`{"os":"{{.Os}}","architecture":"{{.Architecture}}","diff_ids":{{json .RootFS.Layers}}}`)
	if err != nil {
		return describedImage{}, fmt.Errorf("read image %s: %w", imageID, err)
	}
	var described describedImage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &described); err != nil {
		return describedImage{}, fmt.Errorf("decode image %s: %w", imageID, err)
	}
	return described, nil
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
