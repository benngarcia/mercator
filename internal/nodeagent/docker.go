package nodeagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
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
	// acceleratorBinary is the vendor tool this node asks about its own cards and
	// the driver under them. It is the runtime's own configuration for the reason
	// the daemon binary above is: a test points it at a machine that answers
	// differently from this one, and nothing else can.
	acceleratorBinary string
	// artifactRoot is where this node keeps immutable Artifact copies. A daemon
	// has no concept of one, so it is the agent's own durable storage rather
	// than anything Docker manages. A runtime given none replicates nothing and
	// reports no Artifact inventory, which is silence rather than an empty disk.
	artifactRoot string
	// network is what this node has measured about the paths it crosses. It is
	// the runtime's own state rather than something read off the host at report
	// time, because a throughput is only knowable while a transfer is happening
	// and the transfers are this process's own work.
	network *pathMeasurements
}

// RuntimeOption configures the Docker runtime.
type RuntimeOption func(*DockerRuntime)

// WithArtifactRoot gives this node somewhere to keep immutable Artifact copies.
// It is the agent's own directory, stated by whoever started the agent, because
// only they know which filesystem has the room: a default under the daemon's
// storage would be a directory this process usually cannot write to, and one
// under a temporary directory would lose every copy on reboot.
func WithArtifactRoot(root string) RuntimeOption {
	return func(docker *DockerRuntime) { docker.artifactRoot = root }
}

// WithAcceleratorTool points this node at the vendor tool it asks about its own
// cards. It exists so a test can stand in a machine with four A100s or no
// driver at all, which is the only way the reports this node publishes about
// hardware can be exercised anywhere but on the hardware.
func WithAcceleratorTool(binary string) RuntimeOption {
	return func(docker *DockerRuntime) { docker.acceleratorBinary = binary }
}

func NewDockerRuntime(binary string, options ...RuntimeOption) *DockerRuntime {
	if binary == "" {
		binary = "docker"
	}
	runtime := &DockerRuntime{
		binary:            binary,
		acceleratorBinary: "nvidia-smi",
		now:               time.Now,
		labelPrefix:       "mercator.",
		network:           newPathMeasurements(),
	}
	for _, option := range options {
		option(runtime)
	}
	return runtime
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
		Disk:             docker.diskFacts(info.DockerRootDir),
		// What this node has measured about the paths it crosses, which is the
		// field phase 2 declared and nothing wrote. A machine that has moved no
		// content publishes nothing here, and Placement prices that silence with
		// its own stated assumption rather than mistaking it for a slow link.
		Network: docker.network.facts(facts.ObservedAt),
		// What this machine holds and what drives it, which are the three fields
		// phase 2 declared and nothing wrote. Until something did, every enrolled
		// GPU machine published an empty accelerator inventory and was struck out
		// of every accelerator placement it was perfect for.
		Accelerator: docker.acceleratorFacts(ctx),
	}
	if slices.Contains(info.runtimeNames(), "nvidia") {
		facts.Host.AcceleratorToolkit = "nvidia-container-toolkit"
	}
	store, err := docker.openImageStore(ctx, info)
	if err != nil {
		return capability.NodeFacts{}, err
	}
	images, err := docker.images(ctx, store)
	if err != nil {
		return capability.NodeFacts{}, err
	}
	facts.Images = images
	caches, err := docker.caches(ctx)
	if err != nil {
		return capability.NodeFacts{}, err
	}
	facts.Caches = caches
	facts.Artifacts = docker.artifacts()
	return facts, nil
}

// PrepareImage pulls an image by its exact manifest digest. A tag is never
// image identity, so the reference the control plane sends is already pinned.
//
// It goes to the daemon's own API rather than to the CLI, for one reason: the
// credential. The CLI reads registry material out of a config file, so pulling a
// private image through it means writing the material onto the machine and
// remembering to remove it, and a pull that is killed halfway leaves it there.
// The API takes it as a header, so the material this node was handed exists in
// this process's memory for the length of one request and nowhere else. That is
// what "the machine forgets it afterwards" has to mean on a host an operator
// rents by the hour.
func (docker *DockerRuntime) PrepareImage(ctx context.Context, command capability.PrepareImageCommand) error {
	if command.Reference == "" {
		return fmt.Errorf("prepare image: no digest-pinned reference to pull")
	}
	if err := docker.authorisedPull(command); err != nil {
		return fmt.Errorf("pull %s: %w", command.Reference, err)
	}
	return docker.pullImage(ctx, command)
}

// authorisedPull is the machine checking its own material before it presents it.
// The control plane minted it for one operation, one workspace and one digest,
// and a node that presented whatever it was handed would make that scope a claim
// in a comment rather than something either end enforces: the case it catches is
// a credential for one workspace's private image arriving on a command to pull
// another's.
//
// No credential is not a refusal. An image any anonymous reader can have is
// minted nothing, and a node that failed here would be unable to pull the public
// images most workloads run.
//
// A bound with no material behind it is a refusal, and a distinct one. It is
// what a command replayed on a later session carries: the control plane's record
// of a pull holds what was authorised and never the secret, so an agent
// reconnecting to a command issued while it was down is handed the record rather
// than the pull. Presenting it would reach the registry as an empty password and
// come back in the registry's vocabulary instead of Mercator's.
func (docker *DockerRuntime) authorisedPull(command capability.PrepareImageCommand) error {
	credential := command.RegistryCredential
	if credential.Zero() {
		return nil
	}
	if credential.Secret == "" {
		return fmt.Errorf(
			"this node was handed the record of a pull minted for %s rather than the pull itself, so there is nothing to present",
			credential.Content,
		)
	}
	if err := credential.Authorises(command.OperationID, command.WorkspaceID, command.ManifestDigest, docker.now().UTC()); err != nil {
		return err
	}
	if host := domain.ReferenceRegistry(command.Reference); credential.Registry != host {
		return fmt.Errorf("this credential is %s's and the reference is served from %s", credential.Registry, host)
	}
	return nil
}

// pullImage asks the daemon for the content, carrying whatever this node was
// given to prove it may have it. The reference is split the way the API takes
// it, with the digest as the tag, so what is fetched is the content the control
// plane named rather than whatever a label points at now.
func (docker *DockerRuntime) pullImage(ctx context.Context, command capability.PrepareImageCommand) error {
	endpoint, err := docker.endpoint(ctx)
	if err != nil {
		return err
	}
	client, base, err := daemonClient(endpoint)
	if err != nil {
		return err
	}
	repository, digest, _ := strings.Cut(command.Reference, "@")
	query := url.Values{"fromImage": {repository}}
	if digest != "" {
		query.Set("tag", digest)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/"+contentStoreAPIVersion+"/images/create?"+query.Encode(), nil)
	if err != nil {
		return fmt.Errorf("pull %s: %w", command.Reference, err)
	}
	if header := registryAuthHeader(command.RegistryCredential); header != "" {
		request.Header.Set("X-Registry-Auth", header)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("pull %s: %w", command.Reference, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("pull %s: the daemon answered %s: %s", command.Reference, response.Status, strings.TrimSpace(string(body)))
	}
	return pullOutcome(response.Body, command.Reference)
}

// registryAuthHeader is the material as the daemon takes it: one JSON object,
// base64url encoded, on the request that uses it. It is empty for content that
// needs none, and an empty header is not sent at all: a daemon given an empty
// credential answers 500 rather than pulling anonymously.
func registryAuthHeader(credential domain.RegistryPull) string {
	if credential.Zero() {
		return ""
	}
	encoded, err := json.Marshal(map[string]string{
		"username":      credential.Username,
		"password":      credential.Secret,
		"serveraddress": credential.Registry,
	})
	if err != nil {
		return ""
	}
	return base64.URLEncoding.EncodeToString(encoded)
}

// pullOutcome reads the daemon's progress stream to its end and answers with
// what the pull did. The stream has to be drained whatever happens, because the
// daemon performs the pull while it writes it and a caller that stopped reading
// would leave the fetch half done.
//
// A refused pull arrives inside a successful response. The daemon accepted the
// request, began answering, and then found it could not have the content, so the
// failure is a line in the body rather than a status: a node that read the status
// alone would report every private image it may not read as content it holds.
func pullOutcome(body io.Reader, reference string) error {
	decoder := json.NewDecoder(body)
	var failure string
	for {
		var message struct {
			Error string `json:"error"`
		}
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("pull %s: read what the daemon was doing: %w", reference, err)
		}
		if message.Error != "" {
			failure = message.Error
		}
	}
	if failure != "" {
		return fmt.Errorf("pull %s: %s", reference, failure)
	}
	return nil
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
	for _, mount := range command.CacheMounts {
		attachment, err := docker.cacheMount(command.WorkspaceID, mount)
		if err != nil {
			return err
		}
		args = append(args, "--mount", attachment)
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
// exited, and when each of them really started. It is the node's own authority:
// the control plane learns that a process began and ended here, whatever the
// application did or did not report.
//
// The start moment is a second read, because `docker ps` does not carry it in any
// format: the daemon reports `State.StartedAt` only on inspect. It is one inspect
// for the whole list rather than one per container, and a container the daemon
// will not describe is reported with no start moment rather than with a guess. A
// moment the daemon does state and this runtime cannot read fails the whole read
// instead, which is the same answer the Docker adapter gives to the same daemon:
// reporting it absent would publish no start for any container on this machine and
// degrade every start-latency row on the only reusable lane there is, with nothing
// anywhere saying that is what happened.
func (docker *DockerRuntime) Observe(ctx context.Context) ([]capability.WorkloadObservation, error) {
	containers, err := docker.containers(ctx)
	if err != nil {
		return nil, err
	}
	started, err := docker.startMoments(ctx, containers)
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
		if moment, known := started[container.Names]; known {
			observation.StartedAt = &moment
		}
		if observation.Phase.Exited() {
			code := container.exitCode()
			observation.ExitCode = &code
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

// startMoments is when each of these containers began, keyed by the name the
// listing gave it. It is the one fact in this report that has to come from
// inspect, and it is asked once for the whole list.
//
// A read that answers for some containers and not others keeps what came back:
// the daemon prints the objects it could describe and exits non-zero for the rest,
// which is the same shape the cache volume read has, and a container pruned
// between the two calls must not cost Mercator the exits of every other workload
// on this machine. A container with no start moment is reported without one, which
// is what an absent stage is: nothing here invents a moment from the launch.
//
// A moment stated unreadably is the other case, and it fails. A missing line is a
// container that was not there to describe; an unreadable value is a runtime whose
// moments this agent does not understand, and every container on that machine has
// the same problem, so tolerating it bought silence over the whole lane rather than
// resilience over one container.
func (docker *DockerRuntime) startMoments(ctx context.Context, containers []dockerContainer) (map[string]time.Time, error) {
	if len(containers) == 0 {
		return nil, nil
	}
	args := []string{"inspect", "--format", `{{json .Name}} {{json .State.StartedAt}}`}
	for _, container := range containers {
		args = append(args, container.Names)
	}
	out, _ := docker.run(ctx, args...)
	moments := make(map[string]time.Time, len(containers))
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		name, startedAt, err := parseStartMoment(line)
		if err != nil {
			return nil, err
		}
		if name == "" || startedAt.IsZero() {
			continue
		}
		moments[name] = startedAt
	}
	return moments, nil
}

// parseStartMoment reads one line of the inspect above as the container it names
// and the moment that container states. It answers three different things.
//
// A line that is not one of these objects at all names no container: that is the
// shape a container pruned between the two calls leaves, and the caller skips it.
// The epoch, which the daemon writes as "0001-01-01T00:00:00Z", is a container that
// was made and never ran, so it names a container with no start rather than a start
// in year one. A moment stated in any other form is an error, because reading it as
// the epoch would report every container on this machine as never started.
func parseStartMoment(line string) (string, time.Time, error) {
	rawName, rawMoment, found := strings.Cut(strings.TrimSpace(line), " ")
	if !found {
		return "", time.Time{}, nil
	}
	var name, moment string
	if json.Unmarshal([]byte(rawName), &name) != nil || json.Unmarshal([]byte(rawMoment), &moment) != nil {
		return "", time.Time{}, nil
	}
	startedAt, err := time.Parse(time.RFC3339Nano, moment)
	if err != nil {
		return "", time.Time{}, fmt.Errorf(
			"docker inspect: container %s states State.StartedAt as %q, which is not a moment this runtime can read: %w",
			name, moment, err,
		)
	}
	return strings.TrimPrefix(name, "/"), startedAt.UTC(), nil
}

// cacheLabel names one part of a cache's identity in the daemon's own label
// space. Each part is stamped separately rather than packed into the volume
// name, because the volume name has to survive a compatibility key that is an
// application's arbitrary string and these have to be readable back out.
func (docker *DockerRuntime) cacheLabel(part string) string {
	return docker.labelPrefix + "cache." + part
}

// cacheMount is the volume flag one Cache Mount contributes to `docker run`.
// The volume's name is derived from the workspace, the cache's name, and the
// compatibility key together, so a second workspace asking for "compiler-cache"
// gets its own storage by construction rather than by a comparison this function
// could forget to make, and a new compatibility key gets an empty cache rather
// than the generation the application has just said it cannot use.
//
// Creating the container is what creates the cache. The daemon makes a volume
// the container asks for and does not have, stamped with the labels named here,
// so a launch that never reaches container creation leaves nothing behind: an
// image this machine cannot resolve, a full disk, a refused command. The agent
// used to open the volume itself before dispatching the run, which made every
// failed launch a machine reporting a cache no workload of that tenant and
// generation had ever been attached to, and the next Run's decision recorded it
// as warmth.
//
// What is left behind is the previous generation's volume, which nothing
// reclaims: garbage collection is its own capability and this runtime still
// declares it unsupported.
func (docker *DockerRuntime) cacheMount(workspaceID string, mount domain.CacheMountRequirement) (string, error) {
	if workspaceID == "" {
		return "", fmt.Errorf("cache mount %q has no workspace, and a cache's identity is workspace-scoped", mount.Name)
	}
	if !domain.ValidCacheName(mount.Name) {
		return "", fmt.Errorf("cache mount %q is not a name a volume can be derived from", mount.Name)
	}
	// The key is an application's own string and this stamps it into an option
	// list the daemon parses on commas. A key that cannot be written down here
	// is a generation this machine could never report holding, so it is refused
	// rather than escaped into something else.
	if !domain.ValidCacheCompatibilityKey(mount.CompatibilityKey) {
		return "", fmt.Errorf("cache mount %q names generation %q, which no volume label can carry", mount.Name, mount.CompatibilityKey)
	}
	return strings.Join([]string{
		"type=volume",
		"source=" + domain.CacheVolumeName(workspaceID, mount),
		"target=" + domain.CacheMountPath(mount.Name),
		"volume-label=" + docker.cacheLabel("workspace") + "=" + workspaceID,
		"volume-label=" + docker.cacheLabel("name") + "=" + mount.Name,
		"volume-label=" + docker.cacheLabel("key") + "=" + mount.CompatibilityKey,
	}, ","), nil
}

// caches is the mutable, application-owned state this machine holds, read back
// out of the labels the daemon stamped when a workload's container first asked
// for each volume. Only volumes made for a Mercator cache are reported: another
// tool's volume on the same daemon is not one, whatever it is called.
//
// A volume is a cache once a workload of this node's has run against it, and the
// daemon makes the volume one step earlier than that. `docker run` resolves the
// image, creates the container and the mount points it names, and only then asks
// the runtime for a process. An entrypoint this image does not carry, a device
// this host lacks, or a memory limit the kernel refuses therefore exits non-zero
// with the labelled volume already on the disk and nothing ever run against it.
// Reporting that empty directory is the machine claiming warmth it has not
// earned, which is the distinction CacheEvidence exists to make.
//
// So the report is the intersection of two facts the daemon holds: the volumes
// stamped as caches, and the volumes some container of this node's was attached
// to while running. Nothing is reclaimed here. Removing a volume because a
// launch failed would delete a tenant's warm cache whenever the failing launch
// was the second one, and reclaiming storage is garbage collection, which this
// runtime still declares unsupported. A cache nothing can be shown to have run
// against is left on the disk and left out of the report.
//
// A cache read never fails the node's report. A cache is best-effort by
// construction and silence about one is already expressible, while failing here
// would end the agent's session and, on an agent with no session yet, block its
// enrollment: an operator pruning volumes on a working machine would take it out
// of the fleet. So a volume that vanished between the listing and the read is
// one cache left out, and a daemon that will not answer at all leaves this node
// saying nothing rather than claiming it enumerated and found none.
//
// No size is reported. moby prices a volume only through GET /system/df, which
// walks every volume on the host and took 4.8 seconds for 342 of them on the
// machine this was written on, so it is not a read a heartbeat may make; and a
// zero reported here would be this node claiming an empty cache it may be
// holding gigabytes in. What the daemon can state is when each cache generation
// began, which it does state, because each generation gets its own volume.
func (docker *DockerRuntime) caches(ctx context.Context) (domain.CacheInventory, error) {
	inventory := domain.CacheInventory{Known: true, ObservedAt: docker.now().UTC()}
	names, err := docker.run(ctx, "volume", "ls",
		"--filter", "label="+docker.cacheLabel("name"), "--format", "{{.Name}}")
	if err != nil {
		return docker.unreadableCaches(ctx, err)
	}
	volumes := strings.Fields(names)
	if len(volumes) == 0 {
		return inventory, nil
	}
	attached, err := docker.attachedVolumes(ctx)
	if err != nil {
		return docker.unreadableCaches(ctx, err)
	}
	volumes = slices.DeleteFunc(volumes, func(volume string) bool { return !attached[volume] })
	if len(volumes) == 0 {
		return inventory, nil
	}
	// The daemon prints the volumes it could describe and exits non-zero for
	// the ones it could not, so what came back is read either way.
	described, err := docker.run(ctx, append(append([]string{"volume", "inspect"}, volumes...),
		"--format", `{"name":"{{.Name}}","created_at":"{{.CreatedAt}}","labels":{{json .Labels}}}`)...)
	if err != nil && strings.TrimSpace(described) == "" {
		return docker.unreadableCaches(ctx, err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(described), "\n") {
		if line == "" {
			continue
		}
		var volume describedVolume
		if err := json.Unmarshal([]byte(line), &volume); err != nil {
			return docker.unreadableCaches(ctx, fmt.Errorf("decode cache volume: %w", err))
		}
		mount, ok := volume.cache(docker.cacheLabel)
		if !ok {
			continue
		}
		inventory.Mounts = append(inventory.Mounts, mount)
	}
	return inventory, nil
}

// attachedVolumes is the storage a workload of this node's was actually run
// against, taken from the containers the daemon still accounts for. A container
// the daemon created and could not start is exactly the case this answers: it
// holds the mount and never held a process, so its volumes are not caches.
//
// The evidence is as durable as the container record, so a machine whose
// containers an operator removed reports fewer caches than it holds. That is the
// safe direction of the same trade the whole slice is about: a cache left out is
// work an application repeats, and a cache claimed without evidence is a Run
// placed on a machine that never did the work.
func (docker *DockerRuntime) attachedVolumes(ctx context.Context) (map[string]bool, error) {
	containers, err := docker.containers(ctx)
	if err != nil {
		return nil, err
	}
	attached := map[string]bool{}
	for _, container := range containers {
		if !container.ran() {
			continue
		}
		for volume := range strings.SplitSeq(container.Mounts, ",") {
			attached[volume] = true
		}
	}
	return attached, nil
}

// unreadableCaches is a node that cannot say what mutable state it holds. It is
// silence rather than an empty disk, which is the difference between "I looked
// and there is nothing" and "nobody could ask me". A read that ended because
// this agent is shutting down says nothing about the machine at all, so that one
// fails the report.
func (docker *DockerRuntime) unreadableCaches(ctx context.Context, err error) (domain.CacheInventory, error) {
	if ctx.Err() != nil {
		return domain.CacheInventory{}, fmt.Errorf("read this machine's caches: %w", err)
	}
	return domain.CacheInventory{}, nil
}

// describedVolume is one volume as the daemon accounts for it. The labels are
// the identity: a cache is a workspace, a name, and the generation of content
// the application declared, and none of that can be read off a volume's bytes.
type describedVolume struct {
	Name      string            `json:"name"`
	CreatedAt string            `json:"created_at"`
	Labels    map[string]string `json:"labels"`
}

// cache is the Cache Mount this volume holds, or nothing when its labels do not
// name one. A volume missing a workspace or a name is not a cache this control
// plane can address, and reporting it under a guessed identity is how one
// workspace ends up reading another's bytes.
func (volume describedVolume) cache(label func(string) string) (domain.CacheMount, bool) {
	mount := domain.CacheMount{
		WorkspaceID:      volume.Labels[label("workspace")],
		Name:             volume.Labels[label("name")],
		CompatibilityKey: volume.Labels[label("key")],
	}
	if mount.WorkspaceID == "" || mount.Name == "" {
		return domain.CacheMount{}, false
	}
	if created, err := time.Parse(time.RFC3339, volume.CreatedAt); err == nil {
		mount.CreatedAt = created.UTC()
	}
	return mount, true
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
	// DockerRootDir is where this daemon keeps everything it stores: image
	// layers, volumes, and container writable layers. It is the daemon's own
	// name for the filesystem a workload's content lands on, which is what makes
	// the disk this node reports the disk its workloads get.
	DockerRootDir string `json:"DockerRootDir"`
	Runtimes      map[string]struct {
		Path string `json:"path"`
	} `json:"Runtimes"`
	// DriverStatus is the image store's own account of itself. It is the only
	// thing in `docker info` that tells the two Docker image stores apart: both
	// report a driver name, and the snapshotter's "overlayfs" and the graph
	// driver's "overlay2" are different machinery answering different questions
	// about the same image.
	DriverStatus [][2]string `json:"DriverStatus"`
}

// snapshotterDriverType is what a daemon keeping images in the containerd
// content store reports its storage backend to be (moby, LayerStoreStatus in
// daemon/containerd/service.go). That store is the default for Docker Engine 29
// on Linux, so this is the ordinary case rather than the exotic one.
const snapshotterDriverType = "io.containerd.snapshotter.v1"

func (info dockerInfo) keepsContentStore() bool {
	for _, status := range info.DriverStatus {
		if status[0] == "driver-type" && status[1] == snapshotterDriverType {
			return true
		}
	}
	return false
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
// Being listed is not being runnable. An image whose content is here and whose
// layer chain is not assembled cannot start a container, and this node used to
// call every image it could list hot and unpacked, which is a claim about
// readiness made from a listing that says nothing about it. Each image now
// states what its image store established, and the two Docker image stores
// establish it in different ways.
//
// One image the daemon will not describe is one image reported unknown. It is
// not the whole report: a host with forty images, one of which was pruned
// between the listing and the read, holds thirty-nine images that nothing has
// stopped it from stating. Failing the report would have cost this node its
// session, and a node with no session yet its enrollment, over a fact about one
// image.
func (docker *DockerRuntime) images(ctx context.Context, store imageStore) ([]capability.ImageLocality, error) {
	listed, err := docker.listImages(ctx)
	if err != nil {
		return nil, err
	}
	var locality []capability.ImageLocality
	for _, image := range listed {
		pinned := store.pinnedDigest(image)
		if pinned == "" {
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
				ManifestDigest: pinned,
				State:          domain.LocalityUnknown,
				LastVerifiedAt: docker.now().UTC(),
			})
			continue
		}
		locality = append(locality, described.locality(pinned, store.assembly(image, described), docker.now().UTC()))
	}
	return locality, nil
}

// listedImage is one line of the daemon's own image list. ID is what every
// other read of this image is addressed by. Digest is the reference digest the
// CLI prints, which is the name a Run is pinned by on a graph-driver daemon and
// is not on a content-store one, so which of them means anything is the image
// store's answer rather than this function's.
type listedImage struct {
	ID     string `json:"ID"`
	Digest string `json:"Digest"`
}

func (docker *DockerRuntime) listImages(ctx context.Context) ([]listedImage, error) {
	out, err := docker.run(ctx, "images", "--digests", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker images: %w", err)
	}
	var listed []listedImage
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var image listedImage
		if err := json.Unmarshal([]byte(line), &image); err != nil {
			return nil, fmt.Errorf("decode docker image: %w", err)
		}
		if image.Digest == "<none>" {
			image.Digest = ""
		}
		listed = append(listed, image)
	}
	return listed, nil
}

// describedImage is what the daemon can say about one image it holds: which
// build it is, and the uncompressed layer identities its config declares. The
// build matters because a multi-platform image is listed under one index digest
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

// locality is what this daemon established about one image, and nothing beyond
// it. Layers are reported held only for an image a container can be started on:
// a chain the daemon has not assembled is content nothing can be mounted from,
// and reporting it would tell Placement this machine is ready when it is
// minutes of local work away.
func (described describedImage) locality(manifestDigest string, assembly imageAssembly, at time.Time) capability.ImageLocality {
	reported := capability.ImageLocality{
		ManifestDigest: manifestDigest,
		Platform:       described.platform(),
		ContentPresent: assembly.ContentPresent,
		State:          assembly.State,
		LastVerifiedAt: at,
	}
	if assembly.State == domain.LocalityHot {
		reported.LayerDiffIDs = described.DiffIDs
	}
	return reported
}

// describe reads what one image is and what it is made of. An image reported
// with no layers is indistinguishable downstream from a host holding no part of
// it, and an image reported without its platform is one nothing can tell from
// another platform's build under the same digest, so a daemon that will not
// answer either yields an unknown image rather than a confident wrong one.
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
	if len(described.DiffIDs) == 0 {
		return describedImage{}, fmt.Errorf("the daemon described image %s without naming any of its content", imageID)
	}
	return described, nil
}

// imageAssembly is what an image store established about one image: whether a
// container can be started on it now, and whether its bytes are on this machine
// at all. The two are separate answers because fetching and unpacking are
// separate acts, and a host that has done the first and not the second owes
// local work rather than a pull.
type imageAssembly struct {
	State          domain.LocalityState
	ContentPresent bool
}

// imageStore is the daemon's own account of its images: the name a Run pinned
// to one of them carries, and what a container can be started on. Docker has
// two image stores and they establish both differently, so which one is
// answering decides what counts as evidence. Reading one store's evidence on
// the other is how a node ends up stating a measurement it never made, or
// filing a true measurement under a name nothing can match.
type imageStore interface {
	pinnedDigest(listed listedImage) string
	assembly(listed listedImage, described describedImage) imageAssembly
}

// openImageStore picks the evidence this daemon actually offers.
func (docker *DockerRuntime) openImageStore(ctx context.Context, info dockerInfo) (imageStore, error) {
	if !info.keepsContentStore() {
		return graphDriverStore{}, nil
	}
	return docker.readContentStore(ctx)
}

// graphDriverStore is a daemon whose images live in a graph driver. Its layer
// store holds applied layers only and an image is registered once the last of
// its layers is unpacked, so an image this daemon lists and describes is one a
// container can start on, and the bytes are here by the same construction.
//
// The chain a driver names for an image is not evidence of anything further and
// most drivers name none: btrfs returns no metadata at all, vfs and zfs return
// one directory rather than a chain, and only overlay2 names one entry per
// layer. Reading it told working hosts on three of the four drivers that they
// held nothing.
type graphDriverStore struct{}

// pinnedDigest is the reference digest the daemon prints. This store records an
// image under the digest it was pulled by, which for a multi-platform image is
// the index above the platform manifest, and that is the name the control plane
// pins a Run to.
func (graphDriverStore) pinnedDigest(listed listedImage) string { return listed.Digest }

func (graphDriverStore) assembly(listedImage, describedImage) imageAssembly {
	return imageAssembly{State: domain.LocalityHot, ContentPresent: true}
}

// contentStore is a daemon whose images live in the containerd content store,
// which is the default for Docker Engine 29 on Linux. Content and snapshots are
// separate there: an image can be listed with its content missing, and it can
// hold every byte with no snapshot chain to start from. Its CLI reports neither
// (moby returns GraphDriver.Data as null for this store, unconditionally, in
// daemon/containerd/image_inspect.go), so the agent asks the daemon's API for
// the only account of it that exists.
type contentStore struct {
	images map[string]storedImage
}

// storedImage is one image as this store accounts for it: the descriptor the
// daemon resolved the pull to, and one summary per build underneath it.
type storedImage struct {
	Descriptor struct {
		Digest string `json:"digest"`
	} `json:"Descriptor"`
	Manifests []manifestSummary `json:"Manifests"`
}

// pinnedDigest is the digest the daemon's own image record targets, which for a
// multi-platform image is the index and is the name the control plane pins a
// Run to.
//
// Neither name the CLI prints for such an image is that one. This store lists
// an image under the platform manifest it selected and builds RepoDigests from
// the same value (moby, singlePlatformImage: `target := rawImg.Target.Digest`
// over an ImageManifest whose Target NewImageManifest replaced with the
// platform descriptor), so both `.ID` and `.Digest` name a manifest no Run is
// ever pinned to. Reading either would file every locality answer this store
// produces under a name the scheduler's subtraction can never match, and a host
// holding the image whole would be priced a full fetch.
func (store contentStore) pinnedDigest(listed listedImage) string {
	return store.images[listed.ID].Descriptor.Digest
}

func (store contentStore) assembly(listed listedImage, described describedImage) imageAssembly {
	for _, summary := range store.images[listed.ID].Manifests {
		if !summary.describes(described.platform()) {
			continue
		}
		return summary.assembly()
	}
	return imageAssembly{State: domain.LocalityUnknown}
}

// manifestSummary is one platform's build inside one listed image, as the
// daemon accounts for it. Available is whether every blob it references is
// here. Content is how many of those bytes are, which is the only thing that
// separates a machine holding none of an image from one holding all but the
// last layer of it. Unpacked is the usage of the snapshot named by the image's
// full chain ID, which the daemon reports as zero when that snapshot does not
// exist (moby, ImageManifest.SnapshotUsage), so anything above zero is the
// whole chain and exactly the question "can a container start on this".
type manifestSummary struct {
	Available bool   `json:"Available"`
	Kind      string `json:"Kind"`
	Size      struct {
		Content int64 `json:"Content"`
	} `json:"Size"`
	ImageData *struct {
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"Platform"`
		Size struct {
			Unpacked int64 `json:"Unpacked"`
		} `json:"Size"`
	} `json:"ImageData"`
}

// assembly is what this summary establishes about starting a container here.
//
// An interrupted pull is the case the fourth answer exists for. moby's
// Available is all-or-nothing over every blob the manifest references, so a
// machine that fetched seventeen of eighteen layers reports it false while
// holding almost every byte. Calling that cold would be this node asserting
// "none of it is here" about a disk that is nearly full of it, and steering the
// retry at a machine holding none. What it holds cannot be named either: the
// daemon reports the bytes and never which layers they belong to. So the node
// says it looked and cannot account for this one, which is the only claim the
// evidence supports.
func (summary manifestSummary) assembly() imageAssembly {
	switch {
	case summary.ImageData.Size.Unpacked > 0:
		return imageAssembly{State: domain.LocalityHot, ContentPresent: summary.Available}
	case summary.Available:
		return imageAssembly{State: domain.LocalityPartial, ContentPresent: true}
	case summary.Size.Content > 0:
		return imageAssembly{State: domain.LocalityUnknown}
	default:
		return imageAssembly{State: domain.LocalityCold}
	}
}

// describes reports whether this entry is the build the daemon would run here.
// An index lists one manifest per platform and its attestations besides, and
// another platform's entry says nothing about the content this host would use.
func (summary manifestSummary) describes(platform domain.Platform) bool {
	return summary.Kind == "image" && summary.ImageData != nil &&
		domain.Platform{OS: summary.ImageData.Platform.OS, Architecture: summary.ImageData.Platform.Architecture} == platform
}

// contentStoreAPIVersion is the first Engine API version that reports per
// manifest content availability and unpacked size. A daemon on the content
// store below it cannot say which of its images are runnable, and a node that
// cannot say that must not be guessing about it.
const contentStoreAPIVersion = "v1.48"

// readContentStore asks the daemon what it holds and what it has unpacked. It
// fails rather than degrading: an image store this agent cannot read is a node
// whose warmth is unknowable, and reporting that machine cold would send its own
// work elsewhere while reporting it hot would promise starts it cannot make.
func (docker *DockerRuntime) readContentStore(ctx context.Context) (contentStore, error) {
	endpoint, err := docker.endpoint(ctx)
	if err != nil {
		return contentStore{}, err
	}
	client, base, err := daemonClient(endpoint)
	if err != nil {
		return contentStore{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/"+contentStoreAPIVersion+"/images/json?manifests=1", nil)
	if err != nil {
		return contentStore{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return contentStore{}, fmt.Errorf("read the daemon's image store at %s: %w", endpoint, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return contentStore{}, fmt.Errorf(
			"the daemon at %s answered %s for its image store, so this node cannot say which of its images are runnable",
			endpoint, response.Status)
	}
	var listed []struct {
		ID string `json:"Id"`
		storedImage
	}
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		return contentStore{}, fmt.Errorf("decode the daemon's image store: %w", err)
	}
	store := contentStore{images: make(map[string]storedImage, len(listed))}
	for _, image := range listed {
		store.images[image.ID] = image.storedImage
	}
	return store, nil
}

// endpoint is where the daemon this agent drives listens, taken from the same
// place the CLI takes it so that the two are describing one machine.
func (docker *DockerRuntime) endpoint(ctx context.Context) (string, error) {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return host, nil
	}
	out, err := docker.run(ctx, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	if err != nil {
		return "", fmt.Errorf("find the daemon this agent drives: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// daemonClient dials one daemon endpoint. A local socket and a TCP port are the
// two a node agent meets, because the agent runs on the machine it reports. An
// endpoint reached over SSH or TLS is a daemon this code cannot open, and it
// says so rather than reporting a machine that holds everything as holding
// nothing.
func daemonClient(endpoint string) (*http.Client, string, error) {
	transport, address, found := strings.Cut(endpoint, "://")
	if !found {
		return nil, "", fmt.Errorf("the daemon endpoint %q names no transport", endpoint)
	}
	switch transport {
	case "unix":
		return &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", address)
			},
		}}, "http://docker", nil
	case "tcp", "http":
		return &http.Client{}, "http://" + address, nil
	default:
		return nil, "", fmt.Errorf(
			"this agent cannot read the image store of a daemon reached over %q, so it cannot say what %s holds",
			transport, endpoint)
	}
}

type dockerContainer struct {
	Names  string `json:"Names"`
	State  string `json:"State"`
	Status string `json:"Status"`
	Labels string `json:"Labels"`
	// Mounts names the volumes this container is attached to, which is the only
	// place the daemon says which storage a workload was actually run against.
	Mounts string `json:"Mounts"`
}

// ran reports whether a process ever started in this container. The daemon
// creates a container, its mount points, and the volumes those mount points
// name before it asks the runtime for a process, so a container that never left
// "created" is one whose storage nothing has written to.
func (container dockerContainer) ran() bool {
	phase := dockerPhase(container.State)
	return phase == capability.WorkloadPhaseRunning || phase.Exited()
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

// run is one CLI call to the daemon. It answers with what the command printed
// as well as with what went wrong, because the two are not exclusive: a read
// over several objects prints the ones it could describe and exits non-zero for
// the rest, and throwing that away is how one missing object becomes a machine
// that reported nothing.
func (docker *DockerRuntime) run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, docker.binary, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", docker.binary, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

var _ Runtime = (*DockerRuntime)(nil)
