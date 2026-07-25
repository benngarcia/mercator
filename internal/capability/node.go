package capability

import (
	"context"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

// NodeRuntime executes successive workloads on capacity Mercator controls
// through an enrolled agent. It is the only contract that can make capacity
// reusable: without a node runtime there is no host runtime to hand a second
// workload to.
//
// Every command carries an operation identity and a fencing token. The runtime
// promises that one operation identity produces one effect however many times
// the control plane sends it, and that a command stamped with a superseded
// fencing token is refused rather than applied late.
type NodeRuntime interface {
	// NodeSupport reports what this runtime implementation can do. A Docker
	// runtime and a future containerd runtime differ here, not in the contract.
	NodeSupport() NodeSupport
	// Enroll redeems a short-lived enrollment token for an authenticated
	// session bound to one immutable node identity and Rental generation.
	Enroll(ctx context.Context, request EnrollmentRequest) (Enrollment, error)
	// Facts returns the node's latest reported host, accelerator, runtime,
	// disk, network, and locality inventory. It is an observation, so callers
	// treat its age as material.
	Facts(ctx context.Context, ref NodeRef) (NodeFacts, error)
	PrepareImage(ctx context.Context, command PrepareImageCommand) (OperationReceipt, error)
	PrepareArtifact(ctx context.Context, command PrepareArtifactCommand) (OperationReceipt, error)
	LaunchWorkload(ctx context.Context, command LaunchWorkloadCommand) (OperationReceipt, error)
	ObserveWorkload(ctx context.Context, ref WorkloadRef) (WorkloadObservation, error)
	StopWorkload(ctx context.Context, command StopWorkloadCommand) (OperationReceipt, error)
	// Reconcile reports what the node actually holds after either side
	// restarted or reconnected: which operations it has already applied and
	// which workloads it is still running. It is how the control plane learns
	// it must not launch again.
	Reconcile(ctx context.Context, ref NodeRef) (Reconciliation, error)
}

// NodeSupport is one runtime implementation's negotiated capability set.
type NodeSupport struct {
	// ContainerRuntime names the implementation ("docker"). It is provenance
	// for operators; the control-plane contract does not branch on it.
	ContainerRuntime string `json:"container_runtime"`
	// ExactImageInventory reports whether the node can enumerate image and
	// layer digests it holds, rather than only answering whether a reference
	// is present. Without it, image locality is an estimate.
	ExactImageInventory bool `json:"exact_image_inventory"`
	// ArtifactReplicas reports whether the node stores and verifies immutable
	// artifact replicas locally.
	ArtifactReplicas bool `json:"artifact_replicas"`
	// CacheMounts reports whether the node can hold mutable, named application
	// caches across workloads.
	CacheMounts bool `json:"cache_mounts"`
	// Prewarm reports whether the node accepts preparation work for a workload
	// it has not been asked to launch.
	Prewarm bool `json:"prewarm"`
	// GarbageCollection reports whether the node reclaims disk on its own
	// within the bounds the control plane sets.
	GarbageCollection bool `json:"garbage_collection"`
	// MaxConcurrentWorkloads is how many workloads may execute at once. One
	// means the node serializes, which is what makes a Rental Schedule a queue.
	MaxConcurrentWorkloads int `json:"max_concurrent_workloads"`
}

// NodeRef names one enrolled node and the generation it is bound to. A ref
// whose generation no longer matches the Rental is stale by construction.
type NodeRef struct {
	WorkspaceID string
	NodeID      string
	RentalID    string
	Generation  uint64
}

// EnrollmentRequest is what a node presents to join. Identity is immutable:
// the node does not choose its own ID, and it cannot claim a generation it was
// not provisioned for.
type EnrollmentRequest struct {
	NodeID     string `json:"node_id"`
	RentalID   string `json:"rental_id"`
	Generation uint64 `json:"generation"`
	// EnrollmentToken is the short-lived, single-use material the node
	// received through its bootstrap.
	EnrollmentToken string `json:"enrollment_token"`
	// AgentVersion is the build actually running, which may differ from the
	// version the bootstrap pinned.
	AgentVersion string    `json:"agent_version"`
	Facts        NodeFacts `json:"facts"`
}

// Enrollment is the authenticated session the control plane grants.
type Enrollment struct {
	NodeID string `json:"node_id"`
	// SessionToken authenticates subsequent calls for this session only. It
	// expires; the node renews it while its lease holds.
	SessionToken   string    `json:"session_token"`
	SessionExpires time.Time `json:"session_expires"`
	// FencingToken increases on every enrollment. A command carrying a lower
	// token is refused, which is what stops a partitioned old session from
	// acting after a new one took over.
	FencingToken uint64 `json:"fencing_token"`
	// LeaseExpires is when the control plane stops believing this node is
	// alive absent a heartbeat.
	LeaseExpires time.Time `json:"lease_expires"`
	// Duplicate reports that this node identity and generation were already
	// enrolled, so the caller resumed rather than joined.
	Duplicate bool `json:"duplicate"`
}

// NodeFacts is everything the node reports about itself. Each group has one
// authority: the node observes its own host and inventory, and nothing else
// does.
type NodeFacts struct {
	ObservedAt time.Time `json:"observed_at"`
	Host       HostFacts `json:"host"`
	// Images is the exact OCI inventory the node holds.
	Images []ImageLocality `json:"images,omitempty"`
	// Artifacts is what this node established about the immutable Artifact
	// copies on its disk: whether it enumerated them at all, and each copy it
	// found worth what checking it against the catalog said it is worth. It is
	// the same record the control plane keeps, because a second vocabulary for
	// one answer is how the two drift: a node reporting a copy "verified" beside
	// a separate "state" would leave the control plane deciding which of the two
	// it believes. Object storage remains the authority either way.
	//
	// Whether anything enumerated is this node's own claim, never a derivation
	// from the fact that it answered about its host. A runtime with no replica
	// store has not looked for a copy and will never find one, so a control
	// plane reading "the node reported, therefore it enumerated" would publish
	// "I hold no copy" as a fact on behalf of every machine in the fleet, and a
	// hard start bound would then strike those machines out for content nobody
	// ever looked for. A node that does not enumerate says nothing here, and
	// silence is priced rather than refused.
	Artifacts domain.ArtifactInventory `json:"artifacts,omitzero"`
	// Caches is the mutable, application-owned state on this node's disk:
	// whether it enumerated at all, and each cache it found under the workspace
	// that owns it. It is best-effort by construction, because the contents are
	// the application's business and nothing here can be checked against
	// anything. It is the record the control plane already keeps, for the same
	// reason the Artifact inventory is: a second vocabulary for one answer is
	// how the two drift.
	Caches domain.CacheInventory `json:"caches,omitzero"`
}

// HostFacts is the substrate the node runs on, which is separate from what a
// workload image carries. Mercator matches a workload's compatibility contract
// against these, and never installs a workload's accelerator stack onto the
// host.
type HostFacts struct {
	OS                 string `json:"os"`
	KernelVersion      string `json:"kernel_version"`
	Architecture       string `json:"architecture"`
	ContainerRuntime   string `json:"container_runtime"`
	RuntimeVersion     string `json:"runtime_version"`
	AcceleratorToolkit string `json:"accelerator_toolkit,omitempty"`
	DriverVersion      string `json:"driver_version,omitempty"`
	// DriverCapability is the highest accelerator capability the driver
	// supports, expressed in the vendor's own versioning.
	DriverCapability string                        `json:"driver_capability,omitempty"`
	Accelerators     []domain.AcceleratorInventory `json:"accelerators,omitempty"`
	CPUMillis        int64                         `json:"cpu_millis"`
	MemoryBytes      int64                         `json:"memory_bytes"`
	DiskTotalBytes   int64                         `json:"disk_total_bytes"`
	DiskFreeBytes    int64                         `json:"disk_free_bytes"`
	Network          []domain.NetworkFact          `json:"network,omitempty"`
}

// ImageLocality is exact OCI image presence on one node. Every field states
// what this machine HOLDS. What an image still needs is a subtraction between
// this and a resolved manifest, and only the control plane holds both: a node
// that answered it would be restating a manifest it has no way to read.
type ImageLocality struct {
	// ManifestDigest is the digest this image was pulled by, which for a
	// multi-platform image is the index digest and never the platform manifest
	// underneath it. It is the identity a Run is pinned to. A tag is never
	// image identity.
	ManifestDigest string `json:"manifest_digest"`
	// Platform is which build of that digest this machine holds. An index
	// digest names one image per platform, so the digest alone cannot say
	// whether what is here is what this host would run: a machine that fetched
	// another platform's build holds the same name and none of the bytes.
	Platform domain.Platform `json:"platform"`
	// LayerDigests is the compressed layer blobs this node holds unpacked,
	// named the way a registry names them. A container daemon cannot enumerate
	// these: it discards the compressed form when it unpacks an image, which is
	// why LayerDiffIDs sits beside this rather than instead of it.
	LayerDigests []string `json:"layer_digests,omitempty"`
	// LayerDiffIDs is the same unpacked content named by its uncompressed form.
	// This is what a Docker daemon can actually report, and comparing it
	// against a registry manifest is what a resolved manifest carrying both
	// spaces makes possible. Both lists name layers this node has assembled
	// into a mountable chain: content a runtime has fetched and not unpacked is
	// content it cannot name a layer identity for, which is why an image that
	// is here and not ready says so with ContentPresent instead.
	LayerDiffIDs []string `json:"layer_diff_ids,omitempty"`
	// ContentPresent reports that every byte of this image is on this machine,
	// whether or not the chain a container starts on is assembled. Fetching and
	// unpacking are separate acts and only the node can say which it has done:
	// what is left on content already here is local work rather than a pull,
	// and an operator sent after a network problem for bytes on the disk finds
	// nothing to fix. A runtime that cannot establish it says so with State
	// unknown rather than guessing in either direction.
	ContentPresent bool `json:"content_present"`
	// State is how much of this image the node established it has: hot when the
	// whole chain is assembled and a container can start on it now, partial
	// when some of it is here, cold when none of it is, and unknown when the
	// runtime could not establish which.
	State          domain.LocalityState `json:"state"`
	LastVerifiedAt time.Time            `json:"last_verified_at"`
}

// OperationReceipt acknowledges one node command. Duplicate is how a node says
// it already applied this operation identity, which is what makes retry after
// a lost response safe.
type OperationReceipt struct {
	OperationID string    `json:"operation_id"`
	AcceptedAt  time.Time `json:"accepted_at"`
	Duplicate   bool      `json:"duplicate"`
}

// nodeCommand is the identity every node command carries.
type nodeCommand struct {
	NodeRef
	// OperationID makes the command idempotent across retries and restarts.
	OperationID string
	// FencingToken must match the node's current enrollment or the command is
	// refused.
	FencingToken uint64
}

type PrepareImageCommand struct {
	nodeCommand
	ManifestDigest string
	Platform       domain.Platform
	// Reference is the registry reference to pull from, pinned to
	// ManifestDigest.
	Reference string
	// RegistryCredential is short-lived material scoped to this pull. It is
	// never logged, never persisted on the node, and never enters an event.
	RegistryCredential string
	// Unpack requests the image be made ready to run, not merely fetched.
	Unpack bool
}

type PrepareArtifactCommand struct {
	nodeCommand
	ArtifactID    string
	ContentDigest string
	// Source is the durable object-store location to replicate from.
	Source string
	// SourceCredential is short-lived material scoped to this fetch.
	SourceCredential string
	SizeBytes        int64
}

type LaunchWorkloadCommand struct {
	nodeCommand
	RunID     string
	AttemptID string
	BookingID string
	Workload  domain.WorkloadSpec
	// ManifestDigest pins exactly what runs, independent of any tag in the
	// workload spec.
	ManifestDigest string
	Environment    []EnvironmentBinding
	// CacheMounts is the mutable caches to attach, as the workload declared
	// them. The workspace they belong to is the one on this command's NodeRef:
	// a cache's identity is workspace-scoped, so a runtime that took a name
	// alone would be free to hand one workspace another's bytes.
	CacheMounts []domain.CacheMountRequirement
	// ArtifactMounts names the immutable replicas to attach read-only.
	ArtifactMounts    []string
	MaxRuntimeSeconds int64
}

// EnvironmentBinding is one environment variable delivered to a workload.
type EnvironmentBinding struct {
	Name  string  `json:"name"`
	Value *string `json:"value,omitempty"`
}

type StopWorkloadCommand struct {
	nodeCommand
	RunID string
	// GraceSeconds is how long the workload gets to exit before the runtime
	// kills it.
	GraceSeconds int64
}

// WorkloadRef names one workload execution on one node.
type WorkloadRef struct {
	NodeRef
	RunID     string
	AttemptID string
}

// WorkloadPhase is the node's authority: what the container actually did.
// Application readiness and semantic success are the workload's authority and
// arrive separately.
type WorkloadPhase string

const (
	WorkloadPhasePreparing WorkloadPhase = "preparing"
	WorkloadPhaseCreated   WorkloadPhase = "created"
	WorkloadPhaseRunning   WorkloadPhase = "running"
	WorkloadPhaseExited    WorkloadPhase = "exited"
	// WorkloadPhaseAbsent means the node has no record of this workload, which
	// after a launch command is materially different from an exit.
	WorkloadPhaseAbsent WorkloadPhase = "absent"
)

// Exited reports whether an exit code carries meaning for this phase.
func (phase WorkloadPhase) Exited() bool { return phase == WorkloadPhaseExited }

type WorkloadObservation struct {
	RunID      string        `json:"run_id"`
	AttemptID  string        `json:"attempt_id"`
	Phase      WorkloadPhase `json:"phase"`
	ObservedAt time.Time     `json:"observed_at"`
	// ExitCode is meaningful only when Phase.Exited() holds.
	ExitCode *int `json:"exit_code,omitempty"`
	// OOMKilled and FailureReason are resource-level facts the node owns,
	// distinct from anything the application reports about itself.
	OOMKilled     bool       `json:"oom_killed,omitempty"`
	FailureReason string     `json:"failure_reason,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

// Reconciliation is what a node reports after reconnecting: enough for the
// control plane to decide, without guessing, whether a command it sent took
// effect.
type Reconciliation struct {
	NodeID string `json:"node_id"`
	// Generation and FencingToken let the control plane detect that the node
	// it is talking to is not the one it thought.
	Generation   uint64 `json:"generation"`
	FencingToken uint64 `json:"fencing_token"`
	// AppliedOperationIDs is every operation the node has already applied and
	// still remembers. Re-sending one of these is safe and returns Duplicate.
	AppliedOperationIDs []string `json:"applied_operation_ids,omitempty"`
	// Workloads is every workload the node currently knows about, running or
	// recently exited but unacknowledged.
	Workloads []WorkloadObservation `json:"workloads,omitempty"`
	Facts     NodeFacts             `json:"facts"`
}
