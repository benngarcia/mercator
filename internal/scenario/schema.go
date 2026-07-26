// Package scenario owns the versioned Mercator Lab Blueprint catalog and the
// placement-runner adapter. Blueprints describe digest-pinned images,
// immutable Artifacts, mutable Cache Mounts, Rentals, provider capacity, Run
// arrivals, faults, and public evidence.
//
// Placement decision correctness runs against simulated capacity through the
// real orchestrator, Placement implementation, and SQLite event log. Later Lab
// slices execute the same catalog through deterministic in-process and
// process-backed worlds.
//
// Green classification fails on regression. Target classification records
// unbuilt semantics as pending, and a target that starts passing must be
// deliberately promoted.
package scenario

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/benngarcia/mercator/internal/domain"
)

type Status string

const (
	// StatusGreen asserts behavior Mercator has today; a failure is a regression.
	StatusGreen Status = "green"
	// StatusTarget encodes the future contract; a failure is pending, not broken.
	StatusTarget Status = "target"
)

type Outcome string

const (
	OutcomePlace Outcome = "place"
	OutcomeFail  Outcome = "fail"
)

type BookingState string

const (
	BookingRunning BookingState = "running"
	BookingQueued  BookingState = "queued"
)

// Capability names one unbuilt semantic a target scenario is red for. The
// declaration keeps pending results attributable: a target fixture states
// exactly which capability its promotion waits on, and green scenarios may
// declare none.
type Capability string

const (
	// CapabilityRentalSchedule is the Broker ingesting, versioning, and
	// appending to ordered per-Rental schedules.
	CapabilityRentalSchedule Capability = "rental_schedule"
	// CapabilityScheduleAdvancement is the Broker advancing schedules over
	// time: dispatching the next Booking, expiring one past its latest
	// start, and re-placing its Run.
	CapabilityScheduleAdvancement Capability = "schedule_advancement"
	// CapabilityNodeRuntime is Mercator controlling a persistent host runtime
	// through an enrolled node agent, which is what makes capacity reusable at
	// all: without it, provisioned capacity executes one workload and is gone.
	CapabilityNodeRuntime Capability = "node_runtime"
	// CapabilityNodeBootstrap is provisioned capacity arriving with a node
	// agent already enrolled on it. Without it, a fresh machine is capacity
	// nothing can execute on, so only a node an operator enrolled by hand is
	// reusable.
	CapabilityNodeBootstrap Capability = "node_bootstrap"
	// CapabilityExecutionWarmsCapacity is a host keeping what its workload
	// fetched. Running an image is how a machine becomes warm, so capacity that
	// cannot retain content past one execution is cold on every Run.
	CapabilityExecutionWarmsCapacity Capability = "execution_warms_capacity"
	// CapabilityHostFacts is providers advertising SSH and NVIDIA-driver
	// facts on offers, rejected loudly when absent or false.
	CapabilityHostFacts Capability = "host_facts"
	// CapabilityArtifacts is immutable Artifact production, dependency, and
	// replica tracking.
	CapabilityArtifacts Capability = "artifacts"
	// CapabilityArtifactEvidence is per-candidate Artifact locality evidence.
	CapabilityArtifactEvidence Capability = "artifact_evidence"
	// CapabilityCacheMounts is mutable, application-owned caches carried across
	// Runs: attached by identity, compared against the generation the
	// application declared, and never shared between workspaces.
	CapabilityCacheMounts Capability = "cache_mounts"
	// CapabilityPrewarm is Mercator preparing a host for work it has queued but
	// not yet admitted: pulling an image, fetching an Artifact, and stopping
	// when the Run that wanted it goes away.
	CapabilityPrewarm Capability = "prewarm"
	// CapabilityLabExecution is deterministic execution beyond one Placement
	// decision.
	CapabilityLabExecution Capability = "lab_execution"
	// CapabilityEffectLedger is inspectable external commands and
	// consequences.
	CapabilityEffectLedger Capability = "effect_ledger"
	// CapabilityControlPlaneRestart is deterministic restart with surviving
	// external resources.
	CapabilityControlPlaneRestart Capability = "control_plane_restart"
	// CapabilityRunBundle is portable export, normalization, and replay.
	CapabilityRunBundle Capability = "run_bundle"
	// CapabilityInvariants is the transition-aware safety and bounded-liveness
	// registry.
	CapabilityInvariants Capability = "invariants"
	// CapabilityLabUI is the Lab-backed normal HTTP/SSE and console path.
	CapabilityLabUI Capability = "lab_ui"
)

var knownCapabilities = map[Capability]bool{
	CapabilityNodeRuntime:            true,
	CapabilityNodeBootstrap:          true,
	CapabilityRentalSchedule:         true,
	CapabilityScheduleAdvancement:    true,
	CapabilityExecutionWarmsCapacity: true,
	CapabilityHostFacts:              true,
	CapabilityArtifacts:              true,
	CapabilityArtifactEvidence:       true,
	CapabilityCacheMounts:            true,
	CapabilityPrewarm:                true,
	CapabilityLabExecution:           true,
	CapabilityEffectLedger:           true,
	CapabilityControlPlaneRestart:    true,
	CapabilityRunBundle:              true,
	CapabilityInvariants:             true,
	CapabilityLabUI:                  true,
}

// MaxQueuedBookings bounds every RentalSchedule: at most this many queued
// Bookings may wait behind the running Booking. A Run arriving at
// a full schedule must go elsewhere, whatever the score says.
const MaxQueuedBookings = 4

// defaultWorldStart is the scripted clock's origin when a fixture does not
// state one. Every relative moment ("+6m") resolves against it.
var defaultWorldStart = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

type Scenario struct {
	Name    string `json:"-"`
	Summary string `json:"summary"`
	Status  Status `json:"status"`
	// MissingCapabilities names the unbuilt semantics a target scenario is red
	// for. Required for target scenarios, forbidden for green ones.
	MissingCapabilities []Capability `json:"missing_capabilities,omitempty"`
	World               WorldSpec    `json:"world"`
	// Request/Expect is the single-decision shorthand; Timeline is the scripted
	// alternative for scenarios that advance the clock or submit several runs.
	Request  *RequestSpec `json:"request,omitempty"`
	Expect   *ExpectSpec  `json:"expect,omitempty"`
	Timeline []StepSpec   `json:"timeline,omitempty"`
}

type WorldSpec struct {
	Clock           time.Time              `json:"clock,omitzero"`
	Images          map[string]ImageSpec   `json:"images,omitempty"`
	Artifacts       []ArtifactSpec         `json:"artifacts,omitempty"`
	Rentals         []RentalSpec           `json:"rentals,omitempty"`
	RentalSchedules []RentalScheduleSpec   `json:"rental_schedules,omitempty"`
	Hosts           []HostSpec             `json:"hosts,omitempty"`
	Marketplace     []MarketplaceOfferSpec `json:"marketplace,omitempty"`
	Paths           []PathSpec             `json:"paths,omitempty"`
	RuntimeModels   []RuntimeModelSpec     `json:"runtime_models,omitempty"`
	// Prewarm is what this world's control plane is allowed to have in flight
	// for work it has not admitted. It is stated in the Blueprint because it is
	// the whole difference between preparation that shortens a queued Run's
	// start and preparation that starves the Run already running.
	Prewarm *PrewarmSpec `json:"prewarm,omitempty"`
	// Launch is what this world spends on the stages of a launch that nothing
	// else in a fixture can state. A machine's provisioning states its own three;
	// a path states what a transfer costs; these three are the rest.
	Launch LaunchSpec `json:"launch,omitzero"`
}

// LaunchSpec is what a launch costs in this world after its content has arrived.
// Each stage is stated as a duration rather than derived from a rate, because a
// world that computed the same arithmetic the predictor computes would make the
// prediction right by construction, and a stage whose actual agrees with its
// prediction by construction can never be calibrated.
//
// Each is a pointer because a stage a fixture did not mention and a stage a
// fixture says costs nothing are different sentences, and the second is the one
// this record exists to catch: a container that starts instantly collapses the
// observed start onto the moment the last byte landed.
type LaunchSpec struct {
	// Unpack is how long a machine here takes to turn content on its disk into a
	// layer chain a container can start on. The world spends it on every launch
	// that had content to fetch or content it holds unassembled, because bytes
	// that arrive have to be applied before anything runs on them.
	Unpack *Duration `json:"unpack,omitempty"`
	// ContainerStart is how long a container runtime here takes to create the
	// container and hold a process in it.
	ContainerStart *Duration `json:"container_start,omitempty"`
	// ApplicationReady is how long a workload here takes to report that it can do
	// work, measured from its process starting. It is the world's own answer and
	// never the Run's declaration: a fixture states both, and the gap between
	// them is what a calibration reads.
	ApplicationReady *Duration `json:"application_ready,omitempty"`
}

// UnpackSpend is what assembling content costs here.
func (spec LaunchSpec) UnpackSpend() time.Duration { return stated(spec.Unpack) }

// ContainerStartSpend is what creating a container and starting its process
// costs here.
func (spec LaunchSpec) ContainerStartSpend() time.Duration { return stated(spec.ContainerStart) }

// ApplicationReadySpend is how long after its process starts a workload here
// reports itself ready.
func (spec LaunchSpec) ApplicationReadySpend() time.Duration {
	return stated(spec.ApplicationReady)
}

// PrewarmSpec bounds speculative preparation. Both bounds are the control
// plane's own restraint rather than anything a machine enforces: a host asked
// for six transfers at once performs six, and the one that suffers is whatever
// was already fetching there.
type PrewarmSpec struct {
	// MaxConcurrent is how many pieces of content may be arriving speculatively
	// across the fleet at one moment. One is the honest default for a world of
	// one machine, because a node runs its commands in order and a second
	// prefetch would queue behind the first anyway.
	MaxConcurrent int `json:"max_concurrent"`
	// MinInterval is the shortest gap between two speculative fetches starting.
	// It bounds the rate rather than the depth: a control plane that issued a
	// prepare on every reconcile tick would keep a machine permanently busy with
	// content nobody has asked to run.
	MinInterval Duration `json:"min_interval,omitzero"`
}

func (spec PrewarmSpec) validate() error {
	if spec.MaxConcurrent < 1 {
		return fmt.Errorf("prewarm needs a positive max_concurrent, because zero is prewarming turned off rather than bounded")
	}
	if spec.MinInterval.Duration() < 0 {
		return fmt.Errorf("prewarm min_interval cannot run backwards")
	}
	return nil
}

// HostSpec is standing capacity Mercator does not control: a machine that
// already exists and will run a container now, with nothing enrolled on it to
// execute a second workload or to say what it holds. Mercator borrows a slot and
// keeps no machine, so it is standing capacity in the ephemeral lane, which is
// the local Docker daemon in production. It is the only world entry that
// separates the lane's half of OfferSnapshot.KeepsWhatItRuns from the kind's.
type HostSpec struct {
	ID string `json:"id"`
	// CachedImages is what this machine happens to hold. Nothing of Mercator's
	// runs on it, so nothing can ask it and no offer reports it: the content is
	// world truth that Placement cannot see, which is the position an operator's
	// own Docker host is in.
	CachedImages []string `json:"cached_images,omitempty"`
	// ArtifactReplicas is the immutable content this machine happens to be
	// sitting on, and it is invisible for the same reason its images are.
	// Without it a fixture could only put a copy where Mercator can already see
	// one, so the rule that borrowed capacity publishes no Artifact inventory
	// was a rule about a case no fixture could construct: silence and absence
	// were the same world every time, and deleting the rule changed nothing.
	ArtifactReplicas []ArtifactReplicaSpec `json:"artifact_replicas,omitempty"`
	RatePerHourUSD   float64               `json:"rate_per_hour_usd"`
	Billing          BillingSpec           `json:"billing,omitempty"`
	Resources        *ResourcesSpec        `json:"resources,omitempty"`
}

// ExecutionLane reports what this offer becomes once allocated, defaulting to
// reusable capacity.
func (spec MarketplaceOfferSpec) ExecutionLane() domain.ExecutionLane {
	if spec.Lane == "" {
		return domain.LaneReusable
	}
	return spec.Lane
}

// ArtifactSpec declares one immutable version of content this world knows
// about. The version ID is its identity, the content digest is what its bytes
// hash to, and the object store is where the durable copy lives: a host holding
// a copy is an optimisation over that and never what makes the content exist.
type ArtifactSpec struct {
	ID string `json:"id"`
	// ContentDigest is what these bytes hash to. It is stated rather than
	// derived from the version ID, because checking a local copy against it is
	// what makes the copy worth reading, and a digest a name implied could
	// never disagree with the name.
	ContentDigest string   `json:"content_digest"`
	Size          ByteSize `json:"size"`
	// ProducedBy names the Run in this Blueprint that publishes this version.
	// An Artifact with a producer is not durable at virtual time zero: it
	// exists when that Run's publication reaches the object store, and no
	// machine may be seeded holding a copy of content nothing has produced.
	ProducedBy string `json:"produced_by,omitempty"`
}

// Prepublished reports whether the object store already held this version when
// the world started, which is true of every Artifact no Run in this Blueprint
// produces.
func (spec ArtifactSpec) Prepublished() bool { return spec.ProducedBy == "" }

// Version is the catalog entry this declaration names, scoped to one workspace.
// It states what the version is and never whether its bytes are here: a
// publication is a moment, and only a world that has one may stamp it. The
// object-store address is derived rather than authored, because a version is
// immutable and there is exactly one place its bytes can be.
func (spec ArtifactSpec) Version(workspaceID string) domain.ArtifactVersion {
	return domain.ArtifactVersion{
		ID:            spec.ID,
		WorkspaceID:   workspaceID,
		ContentDigest: spec.ContentDigest,
		SizeBytes:     int64(spec.Size),
		Location:      domain.ArtifactLocation(workspaceID, spec.ID),
	}
}

// ArtifactReplicaSpec is a host-local copy one machine was found holding, and
// what that copy is worth. The state is stated rather than defaulted: a copy
// nobody checked and a copy that matches the catalog are different facts, and a
// fixture that could not tell them apart would be asserting the conflation this
// model exists to prevent.
type ArtifactReplicaSpec struct {
	Artifact string                      `json:"artifact"`
	State    domain.ArtifactReplicaState `json:"state"`
	// ContentDigest is what this copy claims its bytes hash to, when that is not
	// what the catalog says the version is. It is how a fixture states the
	// machine an operator restored an older snapshot of a volume onto: the host
	// reports a checked copy filed under the version's name and the bytes under
	// it belong to the version before. Omitted means the copy claims what the
	// catalog claims, which is every ordinary case.
	ContentDigest string `json:"content_digest,omitempty"`
}

// Digest is what this copy claims. Only the control plane can say what the
// claim is worth: the host reports what it checked against, the catalog says
// what the version is, and a copy naming content this version does not have is
// worth exactly what no copy is worth.
func (spec ArtifactReplicaSpec) Digest(artifact ArtifactSpec) string {
	if spec.ContentDigest != "" {
		return spec.ContentDigest
	}
	return artifact.ContentDigest
}

// Start is the scripted clock's origin for this world.
func (w WorldSpec) Start() time.Time {
	if w.Clock.IsZero() {
		return defaultWorldStart
	}
	return w.Clock.UTC()
}

type ImageSpec struct {
	// Layers is what this image is made of, which is world truth and stands
	// however the registry answers. A host can hold an image nothing can look
	// up, which is what makes a refused manifest uncertainty rather than
	// absence.
	Layers []LayerSpec `json:"layers,omitempty"`
	// Registry is what the simulated registry answers when asked to resolve
	// this image. The default is a manifest. The alternatives are the other
	// ways a real registry says no, which a scenario needs to be able to tell
	// apart because an operator acts on them differently.
	Registry RegistryAnswer `json:"registry,omitempty"`
}

// diffIDCount is how many of this image's layers state the name a container
// daemon knows them by.
func (image ImageSpec) diffIDCount() int {
	named := 0
	for _, layer := range image.Layers {
		if layer.DiffID != "" {
			named++
		}
	}
	return named
}

// RegistryAnswer is how the simulated registry responds to a resolution. An
// image can exist in the world and still be unresolvable: the world is what is
// running, the registry is what can be read about it.
type RegistryAnswer string

const (
	RegistryResolves     RegistryAnswer = ""
	RegistryUnresolvable RegistryAnswer = "unresolvable"
	RegistryUnauthorized RegistryAnswer = "unauthorized"
	// RegistryThrottled is a registry refusing reads for now, and
	// RegistryUnreachable one that answered nothing at all. They are separate
	// answers because an operator waits the first one out and repairs the
	// second, and a fixture that could not tell them apart would be asserting
	// the collapse this vocabulary exists to prevent.
	RegistryThrottled   RegistryAnswer = "throttled"
	RegistryUnreachable RegistryAnswer = "unreachable"
)

func (answer RegistryAnswer) valid() bool {
	switch answer {
	case RegistryResolves, RegistryUnresolvable, RegistryUnauthorized, RegistryThrottled, RegistryUnreachable:
		return true
	default:
		return false
	}
}

// LayerSpec identifies one image layer in both digest spaces at once: Digest is
// the compressed blob a registry serves, DiffID the uncompressed content a
// container daemon enumerates. A host reports whichever its runtime can see, so
// a fixture that states only one of them cannot express a Docker host being
// recognised as warm against a registry manifest.
type LayerSpec struct {
	Digest string   `json:"digest"`
	DiffID string   `json:"diff_id,omitempty"`
	Size   ByteSize `json:"size"`
}

// RentalSpec is reusable machine capacity the broker owns. Its schedule is
// broker state; the machine itself receives only the running Booking through
// its standard Docker endpoint.
type RentalSpec struct {
	ID     string `json:"id"`
	Region string `json:"region,omitempty"`
	// IdleLeaseExpiresIn bounds how long the rental survives idle, measured
	// from the world clock's start. Omitted means the lease outlives the
	// scenario.
	IdleLeaseExpiresIn *Duration `json:"idle_lease_expires_in,omitempty"`
	// CachedImages holds every layer of the named images; CachedLayers adds
	// individual layers (for a rental warm from a previous image version).
	CachedImages []string `json:"cached_images,omitempty"`
	CachedLayers []string `json:"cached_layers,omitempty"`
	// ReportsDiffIDs makes this host enumerate its layers the way a Docker
	// daemon does, in uncompressed diff IDs and never in the compressed blob
	// digests a registry manifest lists. It changes nothing about what the host
	// holds, only which name it has for it.
	ReportsDiffIDs bool `json:"reports_diff_ids,omitempty"`
	// Unpacked states whether the cached content above is assembled into a
	// layer chain a container can start on. Fetching and unpacking are separate
	// acts: a host can hold every byte of an image and still be minutes of local
	// work from running it, and a fixture that could not say so could not tell a
	// machine that is ready from one that is only close. Omitted means unpacked,
	// which is what a completed pull leaves behind.
	Unpacked         *bool                 `json:"unpacked,omitempty"`
	ArtifactReplicas []ArtifactReplicaSpec `json:"artifact_replicas,omitempty"`
	// CacheMounts is the mutable, application-owned state this machine was
	// already holding when the world began, each entry under the workspace that
	// owns it.
	CacheMounts    []HeldCacheSpec `json:"cache_mounts,omitempty"`
	RatePerHourUSD float64         `json:"rate_per_hour_usd"`
	// Unpriced states that nobody has quoted a price for this machine, which is
	// what an enrolled node whose operator configured no shadow price publishes.
	// It is not a rate of zero: a rate of zero is a machine somebody says is free,
	// and this is a machine nobody can say anything about the cost of. A fixture
	// states it instead of a rate, never beside one.
	Unpriced  bool           `json:"unpriced,omitempty"`
	Billing   BillingSpec    `json:"billing,omitempty"`
	Resources *ResourcesSpec `json:"resources,omitempty"`
	// CapacityConfidence is how sure whoever published this machine's capacity
	// claim was of it. Omitted means certain, which is what every simulated
	// provider says about a machine it can see. A fixture states less when the
	// world under test is one where the answers a placement rests on are worth
	// different amounts, which is the only way the uncertainty term can be shown
	// pricing anything.
	CapacityConfidence *float64 `json:"capacity_confidence,omitempty"`
}

// ReliabilitySpec is a published risk history, in the terms the one production
// publisher of it states: rates in [0,1] and how much their publisher stands
// behind them. It is a fact about the machine rather than world truth, which is
// why it carries a confidence and why a fixture may state a history that is
// wrong about what the world then does.
//
// It hangs off a marketplace listing and not off a Rental, because that is where
// the fact comes from in production: Vast publishes reliability2 on an unrented
// ask, before anything is rented, and the two builders of standing reusable
// offers, an enrolled node's own projection and the local Docker daemon, publish
// no history at all. A Rental is a machine Mercator holds through a node it
// enrolled, and how often that machine refuses to start a container is a
// lifecycle fact the node owns rather than a history its provider published, so
// it comes back on RentalSpec in the slice that has a writer for it. Stating it
// here is also the only way the corpus can see a rate matter: expected redo cost
// is a probability times a predicted start, and a warm Rental's predicted start
// is about a second.
//
// Each rate is stated or omitted, and omitted is what production mostly does: the
// one publisher in this tree measures interruptions and nothing about refused
// starts. A rate a fixture leaves out is unmeasured, and a rate it states as zero
// is a machine measured and never seen to fail, which are two different worlds a
// fixture has to be able to tell apart.
//
// Nothing prices either rate yet. They are here because a decision that records no
// risk at all cannot be the input to a slice that prices one.
type ReliabilitySpec struct {
	StartFailureRate *float64 `json:"start_failure_rate,omitempty"`
	InterruptionRate *float64 `json:"interruption_rate,omitempty"`
	// Confidence is how much the publisher stands behind the rates it stated. A
	// fixture states it because a measurement over three starts and one over three
	// thousand are different answers, and because it is what carries a stated rate
	// of zero as a measurement rather than as an absence.
	Confidence float64 `json:"confidence"`
}

// Evidence is the risk record this declaration states, in the vocabulary an
// offer carries it in. Whichever rates the fixture stated are stated at the
// confidence it declared, and the rest are silence.
func (spec ReliabilitySpec) Evidence() domain.ReliabilityEvidence {
	return domain.ReliabilityEvidence{
		StartFailures: statedRate(spec.StartFailureRate, spec.Confidence),
		Interruptions: statedRate(spec.InterruptionRate, spec.Confidence),
	}
}

func statedRate(rate *float64, confidence float64) domain.StatedRate {
	if rate == nil {
		return domain.StatedRate{}
	}
	return domain.StatedRate{Rate: *rate, Confidence: confidence}
}

// Risk is what the provider of this listing publishes about how the machine
// behind it behaves, and nothing where no fixture stated a history. Silence is
// not a clean record: a machine nobody has measured has published no rate to
// read, and reading it as zero failures would be a claim its provider never made.
func (spec MarketplaceOfferSpec) Risk() domain.ReliabilityEvidence {
	if spec.Reliability == nil {
		return domain.ReliabilityEvidence{}
	}
	return spec.Reliability.Evidence()
}

// Confidence is how sure this machine's publisher is of its capacity claim, and
// certainty where the fixture stated nothing.
func (spec RentalSpec) Confidence() float64 {
	if spec.CapacityConfidence == nil {
		return 1
	}
	return *spec.CapacityConfidence
}

// HeldCacheSpec is one mutable cache a machine was found holding. The workspace
// is part of it because a cache's identity is workspace-scoped: a fixture that
// could only say "this machine holds compiler-cache" could not state the world
// this whole model exists for, two tenants with one cache name on one host.
type HeldCacheSpec struct {
	// Workspace is the label of the workspace that owns this cache. Empty means
	// the Blueprint's default workspace, which is where a fixture with one tenant
	// puts everything.
	Workspace string `json:"workspace,omitempty"`
	Name      string `json:"name"`
	// CompatibilityKey is the generation of content the application last wrote
	// under this name.
	CompatibilityKey string `json:"compatibility_key,omitempty"`
	// Size is how much room this cache takes on the machine. It is world truth a
	// fixture states, because nothing on a real node measures it.
	Size ByteSize `json:"size,omitempty"`
}

// Requirement is the cache identity this declaration names, in the vocabulary
// the control plane compares.
func (spec HeldCacheSpec) Requirement() domain.CacheMountRequirement {
	return domain.CacheMountRequirement{
		Name:             spec.Name,
		CompatibilityKey: spec.CompatibilityKey,
		SizeBytes:        int64(spec.Size),
	}
}

// IsUnpacked reports whether this host assembled the content it was seeded
// with. A fixture that says nothing is describing the ordinary case: a machine
// whose pulls completed.
func (spec RentalSpec) IsUnpacked() bool { return spec.Unpacked == nil || *spec.Unpacked }

// RentalScheduleSpec is Mercator's ordered sequence of nonterminal Bookings
// assigned to one Rental. At most one Booking runs; any number may wait.
type RentalScheduleSpec struct {
	RentalID string              `json:"rental"`
	Version  uint64              `json:"version,omitempty"`
	Running  *RunningBookingSpec `json:"running,omitempty"`
	Queued   []QueuedBookingSpec `json:"queued,omitempty"`
}

type RunningBookingSpec struct {
	BookingID string `json:"booking"`
	RunID     string `json:"run"`
	// RemainingMaxRuntime is the recorded, enforced upper bound: the basis for
	// latest-start guarantees.
	RemainingMaxRuntime Duration `json:"remaining_max_runtime"`
	// RemainingExpectedRuntime is the p50 remaining runtime, the basis for
	// projected starts and queue-delay scoring. Defaults to the max bound.
	RemainingExpectedRuntime *Duration `json:"remaining_expected_runtime,omitempty"`
	// CompletesAfter is when completion is actually observed, defaulting to
	// the expected remaining runtime. Another value models a run finishing
	// early or overrunning its estimate up to the enforced bound.
	CompletesAfter *Duration `json:"completes_after,omitempty"`
}

func (p RunningBookingSpec) expectedRemaining() Duration {
	if p.RemainingExpectedRuntime != nil {
		return *p.RemainingExpectedRuntime
	}
	return p.RemainingMaxRuntime
}

type QueuedBookingSpec struct {
	BookingID  string   `json:"booking"`
	RunID      string   `json:"run"`
	MaxRuntime Duration `json:"max_runtime"`
	// ExpectedRuntime is the p50 runtime used for projected starts and
	// queue-delay scoring. Defaults to the max bound.
	ExpectedRuntime *Duration `json:"expected_runtime,omitempty"`
	// LatestStart is the last acceptable start time for this Booking. When it
	// expires, Mercator removes the Booking and re-evaluates its Run.
	LatestStart *Moment `json:"latest_start,omitempty"`
}

func (p QueuedBookingSpec) expected() Duration {
	if p.ExpectedRuntime != nil {
		return *p.ExpectedRuntime
	}
	return p.MaxRuntime
}

type MarketplaceOfferSpec struct {
	ID       string `json:"id"`
	Provider string `json:"provider,omitempty"`
	// Lane is what this offer becomes once allocated. "reusable" capacity is
	// held across Runs through an enrolled node; "ephemeral" is a
	// provider-native one-shot product that holds nothing afterwards.
	// Defaulting to reusable keeps a marketplace offer meaning the same thing
	// it always has in this corpus; a scenario about the one-shot lane says so.
	Lane           domain.ExecutionLane `json:"lane,omitempty"`
	Region         string               `json:"region,omitempty"`
	Available      *bool                `json:"available,omitempty"`
	RatePerHourUSD float64              `json:"rate_per_hour_usd"`
	Billing        BillingSpec          `json:"billing,omitempty"`
	Provisioning   ProvisioningSpec     `json:"provisioning"`
	Resources      *ResourcesSpec       `json:"resources,omitempty"`
	// Reliability is the history this listing's provider publishes about the
	// machine behind it: how often it refuses to start the work it is given, and
	// how often it drops the work it is running. Omitted means nobody has measured
	// this machine, which is what every simulated provider but one says today, and
	// what makes silence and a clean record two different worlds a fixture must be
	// able to tell apart.
	Reliability *ReliabilitySpec `json:"reliability,omitempty"`
	// Facts are the hardware facts providers owe on the offer (SSH root
	// access, working NVIDIA driver). Omitted map entries are unknown facts;
	// an offer missing or failing one must be rejected loudly. Target
	// ontology: no offer field carries these yet.
	Facts map[string]bool `json:"facts,omitempty"`
}

type BillingSpec struct {
	SetupFeeUSD   float64   `json:"setup_fee_usd,omitempty"`
	MinimumCharge *Duration `json:"minimum_charge,omitempty"`
}

// PathSpec is one link a machine in this world has published a measurement of.
// It is the machine's own answer rather than world truth: what a fixture states
// here is what the host says about itself, which is why it carries a confidence.
type PathSpec struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	Scope   string  `json:"scope"`
	P10Mbps float64 `json:"p10_mbps"`
	// StatedConfidence is how much the publisher of this measurement stands behind
	// it. Omitted means fully, which is what a simulated host that measured its
	// own link says. Zero is the case worth stating: a publisher that disowns its
	// own number has measured nothing, and Mercator has to read that as the
	// silence it is rather than as a fast answer nobody has to doubt.
	StatedConfidence *float64 `json:"confidence,omitempty"`
}

// Confidence is how much this measurement's publisher stands behind it, and
// certainty where the fixture stated nothing.
func (spec PathSpec) Confidence() float64 {
	if spec.StatedConfidence == nil {
		return 1
	}
	return *spec.StatedConfidence
}

type RuntimeModelSpec struct {
	Run       string   `json:"run,omitempty"`
	Candidate string   `json:"candidate"`
	Minimum   Duration `json:"minimum"`
	Maximum   Duration `json:"maximum"`
}

// ProvisioningSpec is bringing one machine up, said twice on purpose. Expected
// and P90 are what the provider published about it, which is a claim Mercator
// predicts from. The three stages are what the world spends doing it, which is
// what a prediction is calibrated against. They are stated separately because a
// fixture whose actual is derived from the published claim proves nothing about
// either: this offer's provisioning was a claim the world never spent at all, so
// every stage before a container existed cost zero seconds and the three
// earliest stages of a launch had no ground truth to be predicted against.
type ProvisioningSpec struct {
	Expected Duration  `json:"expected"`
	P90      *Duration `json:"p90,omitempty"`
	// Acquisition is the provider allocating the machine, Boot is it reaching a
	// usable operating system, and AgentReady is Mercator's node runtime
	// enrolling on it. They are three stages rather than one number because each
	// is a separate prediction with its own fallback level, and because they fail
	// for different reasons: a marketplace with no stock, a host that never came
	// up, and a machine Mercator cannot reach.
	//
	// Each is a pointer because a stage a fixture did not mention and a stage a
	// fixture says costs nothing are different sentences, and the second is the
	// one this whole record exists to catch: a machine that boots instantly
	// collapses the observed start onto the accepted launch. Read as one value,
	// silence would restore the world the stages were added to end.
	Acquisition *Duration `json:"acquisition,omitempty"`
	Boot        *Duration `json:"boot,omitempty"`
	AgentReady  *Duration `json:"agent_ready,omitempty"`
}

// Spend is how long this world takes to make the machine, which is the sum of
// the stages it goes through. It is deliberately not derived from Expected: the
// provider's expectation is a claim, and a world that spent exactly what was
// claimed would make the prediction right by construction.
func (spec ProvisioningSpec) Spend() time.Duration {
	return spec.AcquisitionSpend() + spec.BootSpend() + spec.AgentReadySpend()
}

// AcquisitionSpend, BootSpend, and AgentReadySpend are what this world takes over
// each stage on its own. They are read one at a time because each has its own
// prediction to be measured against: a record that carried only the sum could not
// say whether a machine was slow to come out of a marketplace or slow to boot.
func (spec ProvisioningSpec) AcquisitionSpend() time.Duration { return stated(spec.Acquisition) }
func (spec ProvisioningSpec) BootSpend() time.Duration        { return stated(spec.Boot) }
func (spec ProvisioningSpec) AgentReadySpend() time.Duration  { return stated(spec.AgentReady) }

func stated(duration *Duration) time.Duration {
	if duration == nil {
		return 0
	}
	return duration.Duration()
}

// ResourcesSpec describes machine inventory (rentals, marketplace offers) or
// run requirements (requests). Omitted fields default host-side to a generous
// GPU-box shape (8 CPUs, 32GB memory, 200GB disk) and request-side to the
// workload defaults, so fixtures state only what the scenario is about.
type ResourcesSpec struct {
	CPUMillis int64    `json:"cpu_millis,omitempty"`
	Memory    ByteSize `json:"memory,omitempty"`
	// Disk separates a machine with no room from a machine whose fixture did
	// not mention its disk, because zero is a state real capacity is in: an
	// enrolled node that could not measure its disk offers exactly none, and it
	// is the case an operator most needs the corpus to hold. Read as one value,
	// a fixture that writes "0GB" to model that machine gets the 200GB default
	// and states the opposite of what it meant. Zero CPU and zero memory
	// describe no machine any offer can carry, so they stay a single number.
	Disk *ByteSize `json:"disk,omitempty"`
	GPU  *GPUSpec  `json:"gpu,omitempty"`
}

type GPUSpec struct {
	Model  string   `json:"model"`
	Count  int      `json:"count,omitempty"`
	Memory ByteSize `json:"memory,omitempty"`
}

type RequestSpec struct {
	Image           string         `json:"image"`
	Resources       *ResourcesSpec `json:"resources,omitempty"`
	MaxRuntime      *Duration      `json:"max_runtime,omitempty"`
	ExpectedRuntime *Duration      `json:"expected_runtime,omitempty"`
	// ExpectedReady is how long this workload says it takes to become ready for
	// work once its process is running. It is the only prediction of the
	// application-ready stage there is, because readiness is the application's own
	// semantics. What the world then spends is WorldSpec.launch.application_ready,
	// and the two are stated separately so neither derives the other.
	ExpectedReady *Duration `json:"expected_ready,omitempty"`
	// ServiceClass is the kind of work this Run says it is, and the only thing
	// that says what waiting is worth to it. It is a string rather than the domain
	// type so a fixture can state a class Mercator does not know, which is a world
	// the corpus has to be able to build: the refusal is the behaviour under test.
	ServiceClass string `json:"service_class,omitempty"`
	// AllowUnknownPricing says this Run would rather run on a machine nobody has
	// quoted a price for than not run at all. It is what admits such a machine as
	// a candidate, and it is never a preference for one: an unpriced candidate is
	// ranked behind every candidate somebody priced.
	AllowUnknownPricing bool `json:"allow_unknown_pricing,omitempty"`
	// MaxStartLatency is the p90 start latency this Run refuses to exceed. It
	// is the only hard bound in the request that image locality feeds, which is
	// what makes it the one place a candidate can be struck out for what it was
	// found to hold.
	MaxStartLatency *Duration `json:"max_start_latency,omitempty"`
	// Download is the floor this Run states on how fast a candidate reaches
	// content over one link, and what it says about running on a machine nobody
	// measured. The corpus could state no download floor at all, so no Blueprint
	// could reach the half of that rule which decides feasibility, and a machine
	// whose measurement its own publisher disowned could be struck out where an
	// identical silent machine was admitted with nothing in the tree able to
	// say so.
	Download *DownloadRequirementSpec `json:"download,omitempty"`
	// CacheMounts declare mutable, application-owned caches by their
	// workspace-scoped names.
	CacheMounts []CacheMountSpec `json:"cache_mounts,omitempty"`
	// ConsumesArtifacts and ProducesArtifacts refer to immutable Artifact IDs
	// declared in the Blueprint world.
	ConsumesArtifacts []string            `json:"consumes_artifacts,omitempty"`
	ProducesArtifacts []string            `json:"produces_artifacts,omitempty"`
	Phases            []WorkloadPhaseSpec `json:"phases,omitempty"`
}

// DownloadRequirementSpec is a Run's hard floor on how fast a candidate reaches
// content over one link, stated in the same terms a host publishes: the scope of
// the link and its pessimistic quantile.
type DownloadRequirementSpec struct {
	Scope      string  `json:"scope"`
	MinP10Mbps float64 `json:"min_p10_mbps"`
	// AllowUnknown says this Run would rather run on a machine nobody has
	// measured than not run at all. It is what a fixture states to put the two
	// silences beside each other: a host that published nothing and a host whose
	// published number its own publisher disowned have to buy their publisher the
	// same thing.
	AllowUnknown bool `json:"allow_unknown,omitempty"`
}

// Requirement is this floor in the control plane's own vocabulary.
func (spec DownloadRequirementSpec) Requirement() *domain.NetworkDownloadRequirement {
	return &domain.NetworkDownloadRequirement{
		Scope:        domain.NetworkScope(spec.Scope),
		MinP10Mbps:   spec.MinP10Mbps,
		AllowUnknown: spec.AllowUnknown,
	}
}

// CacheMountSpec is one mutable cache a Run declares. Its identity is the name,
// scoped to the workspace the Run belongs to; the compatibility key is the
// application's own statement of which generation of content it can use, which
// Mercator compares and never interprets.
type CacheMountSpec struct {
	Name string `json:"name"`
	// CompatibilityKey separates generations of content under one name. A Run
	// declaring a new key is a Run that has said the previous content is no
	// longer usable, so a host holding it is not warm for this Run.
	CompatibilityKey string `json:"compatibility_key,omitempty"`
	// Size is how much room the application expects this cache to take. It is a
	// declaration rather than a measurement, which is what disk reservation will
	// read when prewarming exists.
	Size ByteSize `json:"size,omitempty"`
}

// Requirement is what the control plane compares this declaration against a
// host's caches with.
func (spec CacheMountSpec) Requirement() domain.CacheMountRequirement {
	return domain.CacheMountRequirement{
		Name:             spec.Name,
		CompatibilityKey: spec.CompatibilityKey,
		SizeBytes:        int64(spec.Size),
	}
}

// CacheRequirements is what a request declares, in the control plane's
// vocabulary.
func (req RequestSpec) CacheRequirements() []domain.CacheMountRequirement {
	required := make([]domain.CacheMountRequirement, 0, len(req.CacheMounts))
	for _, mount := range req.CacheMounts {
		required = append(required, mount.Requirement())
	}
	return required
}

type WorkloadPhaseSpec struct {
	Name     string   `json:"name"`
	Duration Duration `json:"duration"`
}

type ExpectSpec struct {
	// Outcome is the decision the event log must record: "place" (a selected
	// offer) or "fail" (a recorded decision with no feasible offers). Selecting
	// a busy Rental creates the Booking described by Booking.
	Outcome Outcome             `json:"outcome"`
	Offer   string              `json:"offer,omitempty"`
	Reasons []string            `json:"reasons,omitempty"`
	Booking *BookingExpectation `json:"booking,omitempty"`
	// Disposition asserts the recorded cleanup intent on the launch intent:
	// "release" for standing rentals, "terminate" for provisioned hosts.
	Disposition string `json:"disposition,omitempty"`
	// StartLatency asserts how long this Run waited between its launch being
	// accepted and its workload actually beginning, read out of Mercator's own run
	// stream. It is the only way this corpus can say that a start is a moment
	// somebody observed rather than the moment the launch was taken: a fixture that
	// says the world spends five minutes making a machine and then asserts nothing
	// here would go green against a control plane that recorded the acceptance as
	// the start.
	StartLatency *Bound `json:"start_latency_seconds,omitempty"`
	// NoStartObserved asserts that nothing reported a start moment for this Run, so
	// the record states the stage absent. It is its own field rather than a bound of
	// zero because those are different sentences, and the difference is the whole
	// point: zero is a workload that began the instant its launch was taken, and
	// this is a workload nobody saw begin.
	NoStartObserved bool `json:"no_start_observed,omitempty"`
	// ReadyLatency asserts how long after its process started this Run's
	// application reported that it can do work, read out of Mercator's own run
	// stream. It is the actual of the last launch stage, and the only way this
	// corpus can say that a running workload is not a ready one.
	ReadyLatency *Bound `json:"ready_latency_seconds,omitempty"`
	// NoReadyReported asserts that this Run's application has said nothing about
	// its own readiness, so the record states the stage absent. It is its own field
	// rather than a bound of zero for the reason NoStartObserved is: zero is a
	// workload that was serving the instant its process appeared, and this is a
	// workload that has not spoken.
	NoReadyReported bool `json:"no_ready_reported,omitempty"`
	// Candidates assert the per-candidate evidence the decision weighed,
	// keyed by rental or marketplace offer ID.
	Candidates map[string]CandidateExpectation `json:"candidates,omitempty"`
}

type BookingExpectation struct {
	BookingID       string       `json:"id"`
	RentalID        string       `json:"rental"`
	State           BookingState `json:"state"`
	AfterBooking    string       `json:"after,omitempty"`
	ProjectedStart  *Duration    `json:"projected_start_in,omitempty"`
	LatestStart     *Moment      `json:"latest_start,omitempty"`
	ScheduleVersion uint64       `json:"schedule_version"`
}

type CandidateExpectation struct {
	Feasible *bool `json:"feasible,omitempty"`
	// Disposition asserts what Placement recorded this candidate as: reusing,
	// queueing on, or provisioning a Rental, or launching a one-shot ephemeral
	// execution that holds nothing afterwards.
	Disposition  domain.CandidateDisposition `json:"disposition,omitempty"`
	Rejected     []RejectionSpec             `json:"rejected,omitempty"`
	QueueSeconds *Bound                      `json:"queue_seconds,omitempty"`
	// Stages asserts what this candidate was predicted to spend on each stage of
	// the launch, named by the stage. One vocabulary replaces the five fields
	// that stated four quantities between them: a fixture states the stages it is
	// about and says nothing about the rest, and a stage added to the record
	// cannot be added without a way to state it.
	Stages map[string]StageExpectation `json:"stages,omitempty"`
	// ImageLocality asserts how much of the Run's image this candidate was
	// found to have: hot, partial, cold, or unknown. It is the qualitative half
	// of the image answer, and the only one that separates a machine that has to
	// fetch from one that only has to finish unpacking.
	ImageLocality domain.LocalityState `json:"image_locality,omitempty"`
	// Schedule asserts the ordered broker-owned schedule evidence weighed for
	// this Rental candidate.
	Schedule *ScheduleEvidenceExpectation `json:"rental_schedule,omitempty"`
	// NoSchedule asserts this candidate recorded no schedule at all, which is what
	// capacity with nothing assigned to it must record: a machine that does not
	// exist yet has no Rental to hold a queue, and a Rental nothing is waiting for
	// has no queue to have been read. Recording an empty schedule for either would
	// publish a version of zero and a wait of nothing as a queue that was measured.
	// It is a field of its own rather than an empty Schedule because a fixture that
	// says nothing about the schedule asserts nothing about it, and this asserts
	// the absence.
	NoSchedule bool `json:"no_rental_schedule,omitempty"`
	// Artifacts asserts what this candidate was recorded as holding of each
	// Artifact the Run reads: "hit", "miss", or "unknown" for a machine that
	// could not enumerate its copies at all.
	Artifacts map[string]string `json:"artifact_evidence,omitempty"`
	// Caches asserts what this candidate was recorded as holding of each Cache
	// Mount the Run declares, by name: "hit" for the generation the workload
	// asked for, "miss" for a machine holding none it can use, and "unknown" for
	// one that could not say. There are no seconds to assert beside it, because
	// what a warm cache saves is work inside the application and nothing here
	// has measured that.
	Caches map[string]string `json:"cache_evidence,omitempty"`
	// Uncertainty asserts how far the answers this candidate was scored on fell
	// short of certainty, in points. One point is one answer worth nothing. It is
	// stated separately from the score because it is the term a fixture is usually
	// about: a machine nobody could ask and a machine that answered and holds
	// nothing owe the same seconds, and this is what says whether the silence was
	// charged once or twice.
	Uncertainty *Bound `json:"uncertainty,omitempty"`
	// ScoreUSD asserts what this candidate was worth to this Run, in dollars. It
	// is the whole arithmetic in one number, which is what makes a fixture able to
	// state that a class's exchange rate was applied rather than merely that the
	// winner came out right.
	ScoreUSD *Bound `json:"score_usd,omitempty"`
	// StartFailureRate and InterruptionRate assert the risk history the decision
	// recorded for this candidate. They are exact rather than bounded, because
	// they are a published fact carried through rather than an arithmetic answer:
	// a rate that arrives changed arrived from somewhere else. Zero is worth
	// asserting, and it is why they are pointers: a machine whose provider has
	// measured it and never seen it fail states nothing to fear, and a machine
	// nobody has measured states nothing at all.
	StartFailureRate *float64 `json:"start_failure_rate,omitempty"`
	InterruptionRate *float64 `json:"interruption_rate,omitempty"`
	// RiskConfidence asserts how much the publisher of that history stands behind
	// it, and it is asked of every rate the record states. It is the one part of the
	// history the score used to read, and a corpus that asserted the rates and not
	// the confidence beside them left the number that mattered unpinned.
	RiskConfidence *float64 `json:"risk_confidence,omitempty"`
	// NoRiskHistory asserts that the decision recorded nothing about how this
	// machine behaves. It is what a fixture states for a machine nobody measured,
	// and it is not the same claim as two rates of zero.
	NoRiskHistory bool `json:"no_risk_history,omitempty"`
}

// StageExpectation is what one launch stage's prediction has to say. Seconds is
// the answer, Source is whose evidence it rests on, and Confidence is what that
// evidence is worth. The three travel together because zero seconds means two
// opposite things: nothing to do when an inventory answered, and nobody could say
// when it did not, and only the source and the confidence beside it tell them
// apart.
type StageExpectation struct {
	Seconds    *Bound   `json:"seconds,omitempty"`
	Source     string   `json:"source,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
}

type ScheduleEvidenceExpectation struct {
	Version        uint64                  `json:"version"`
	Running        *RunningBookingEvidence `json:"running,omitempty"`
	Preceding      []QueuedBookingEvidence `json:"preceding,omitempty"`
	ProjectedStart Duration                `json:"projected_start_in"`
}

type RunningBookingEvidence struct {
	BookingID           string   `json:"booking"`
	RunID               string   `json:"run"`
	RemainingMaxRuntime Duration `json:"remaining_max_runtime"`
	// RemainingExpectedRuntime is the recorded p50; defaults to the max bound.
	RemainingExpectedRuntime *Duration `json:"remaining_expected_runtime,omitempty"`
	// Overrun is how far past the runtime Mercator enforces the record says this
	// Booking has run. It is the only field that separates a Rental with nothing
	// left to project from a Rental a moment from free, because both remaining
	// runtimes above read zero on each. Omitted asserts none.
	Overrun *Duration `json:"overrun,omitempty"`
}

func (e RunningBookingEvidence) expectedRemaining() Duration {
	if e.RemainingExpectedRuntime != nil {
		return *e.RemainingExpectedRuntime
	}
	return e.RemainingMaxRuntime
}

type QueuedBookingEvidence struct {
	BookingID  string   `json:"booking"`
	RunID      string   `json:"run"`
	MaxRuntime Duration `json:"max_runtime"`
	// ExpectedRuntime is the recorded p50; defaults to the max bound.
	ExpectedRuntime *Duration `json:"expected_runtime,omitempty"`
}

func (e QueuedBookingEvidence) expected() Duration {
	if e.ExpectedRuntime != nil {
		return *e.ExpectedRuntime
	}
	return e.MaxRuntime
}

type RejectionSpec struct {
	Code string `json:"code"`
	Path string `json:"path"`
}

// StepSpec is one timeline entry: exactly one of Submit (a named Run with its
// request and expectation), Advance (move the scripted clock), or Reconcile
// (drive Broker advancement for a named Run after relevant world state changed).
type StepSpec struct {
	Submit    string       `json:"submit,omitempty"`
	Request   *RequestSpec `json:"request,omitempty"`
	Advance   *Duration    `json:"advance,omitempty"`
	Reconcile string       `json:"reconcile,omitempty"`
	Expect    *ExpectSpec  `json:"expect,omitempty"`
}

// Duration is a JSON string in Go duration syntax ("6m", "1h30m").
type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("durations are strings like \"6m\": %w", err)
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration().String())
}

func (d Duration) Duration() time.Duration { return time.Duration(d) }

// ByteSize is a JSON string with a decimal unit ("40GB", "512MB", "1.5TB")
// or a bare number of bytes.
type ByteSize int64

// Bytes is a size a fixture stated, and zero where it stated none. It reads
// through a nil pointer on purpose: the caller that wants the difference asks
// for the pointer, and everything else wants the number.
func (b *ByteSize) Bytes() int64 {
	if b == nil {
		return 0
	}
	return int64(*b)
}

// StatedBytes builds a size a fixture stated, which is how Go code writes what
// a Blueprint writes as a string.
func StatedBytes(size int64) *ByteSize {
	stated := ByteSize(size)
	return &stated
}

var byteSizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*(B|KB|MB|GB|TB)$`)
var ociDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var ociImageRefPattern = regexp.MustCompile(`^(?:[^@\s]+@)?sha256:[0-9a-f]{64}$`)

var byteSizeUnits = map[string]float64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12}

func (b *ByteSize) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] != '"' {
		var raw int64
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		*b = ByteSize(raw)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	match := byteSizePattern.FindStringSubmatch(strings.TrimSpace(text))
	if match == nil {
		return fmt.Errorf("byte sizes look like \"40GB\" or \"512MB\", got %q", text)
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return err
	}
	*b = ByteSize(int64(math.Round(value * byteSizeUnits[match[2]])))
	return nil
}

// Moment is an instant written relative to the world clock's start: "+6m".
type Moment struct {
	Offset time.Duration
}

func (m *Moment) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	if !strings.HasPrefix(text, "+") {
		return fmt.Errorf("moments are offsets from the world clock start like \"+6m\", got %q", text)
	}
	offset, err := time.ParseDuration(text[1:])
	if err != nil {
		return err
	}
	m.Offset = offset
	return nil
}

func (m Moment) MarshalJSON() ([]byte, error) {
	return json.Marshal("+" + m.Offset.String())
}

// Resolve returns the absolute instant for a world started at start.
func (m Moment) Resolve(start time.Time) time.Time { return start.Add(m.Offset) }

// Bound is a numeric expectation: a bare number asserts exact equality (to a
// millionth), an object asserts {"at_least": x} and/or {"at_most": y}.
type Bound struct {
	Exactly *float64 `json:"-"`
	AtLeast *float64 `json:"at_least,omitempty"`
	AtMost  *float64 `json:"at_most,omitempty"`
}

func (b *Bound) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] != '{' {
		var value float64
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		b.Exactly = &value
		return nil
	}
	type bare Bound
	return strictUnmarshal(data, (*bare)(b))
}

func (b Bound) MarshalJSON() ([]byte, error) {
	if b.Exactly != nil {
		return json.Marshal(*b.Exactly)
	}
	type bare Bound
	return json.Marshal(bare(b))
}

// Check reports "" when actual satisfies the bound, else a description.
func (b Bound) Check(actual float64) string {
	if b.Exactly != nil && math.Abs(actual-*b.Exactly) > 1e-6 {
		return fmt.Sprintf("want exactly %v, got %v", *b.Exactly, actual)
	}
	if b.AtLeast != nil && actual < *b.AtLeast {
		return fmt.Sprintf("want at least %v, got %v", *b.AtLeast, actual)
	}
	if b.AtMost != nil && actual > *b.AtMost {
		return fmt.Sprintf("want at most %v, got %v", *b.AtMost, actual)
	}
	return ""
}

func strictUnmarshal(data []byte, v any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("trailing content after document")
	}
	return nil
}

// Load adapts one canonical Blueprint or legacy fixture for the placement
// runner.
func Load(path string) (Scenario, error) {
	blueprint, err := LoadBlueprint(path)
	if err != nil {
		return Scenario{}, err
	}
	scenario, ok := blueprint.PlacementScenario()
	if !ok {
		return Scenario{}, fmt.Errorf("%s: Blueprint is not a placement fixture", path)
	}
	return scenario, nil
}

// LoadCorpus reads every *.json scenario in dir, sorted by name.
func LoadCorpus(dir string) ([]Scenario, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	scenarios := make([]Scenario, 0, len(paths))
	for _, path := range paths {
		sc, err := Load(path)
		if err != nil {
			return nil, err
		}
		scenarios = append(scenarios, sc)
	}
	return scenarios, nil
}

// Steps returns the scenario's timeline, synthesizing a single submit step
// from the request/expect shorthand.
func (sc Scenario) Steps() []StepSpec {
	if len(sc.Timeline) > 0 {
		return sc.Timeline
	}
	return []StepSpec{{Submit: "run", Request: sc.Request, Expect: sc.Expect}}
}

func (sc Scenario) validate() error {
	if sc.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if err := validateClassification(sc.Status, sc.MissingCapabilities); err != nil {
		return err
	}
	if err := sc.World.validate(); err != nil {
		return err
	}
	hasShorthand := sc.Request != nil || sc.Expect != nil
	if hasShorthand && len(sc.Timeline) > 0 {
		return fmt.Errorf("use request/expect or timeline, not both")
	}
	if hasShorthand && (sc.Request == nil || sc.Expect == nil) {
		return fmt.Errorf("request and expect are both required")
	}
	if !hasShorthand && len(sc.Timeline) == 0 {
		return fmt.Errorf("a scenario needs a request/expect or a timeline")
	}
	submitted := map[string]bool{}
	for i, step := range sc.Steps() {
		if err := step.validate(submitted); err != nil {
			return fmt.Errorf("timeline[%d]: %w", i, err)
		}
	}
	for i, step := range sc.Steps() {
		if step.Request != nil {
			if err := sc.World.validRequest(*step.Request); err != nil {
				return fmt.Errorf("timeline[%d]: %w", i, err)
			}
		}
		if step.Expect != nil {
			if err := sc.World.validExpect(*step.Expect); err != nil {
				return fmt.Errorf("timeline[%d]: %w", i, err)
			}
		}
	}
	return sc.validateScheduleTimeline()
}

func validateClassification(status Status, missing []Capability) error {
	if status != StatusGreen && status != StatusTarget {
		return fmt.Errorf("classification status must be %q or %q, got %q", StatusGreen, StatusTarget, status)
	}
	if status == StatusTarget && len(missing) == 0 {
		return fmt.Errorf("target scenarios declare the missing_capabilities their promotion waits on")
	}
	if status == StatusGreen && len(missing) > 0 {
		return fmt.Errorf("green scenarios declare no missing_capabilities")
	}
	for _, capability := range missing {
		if !knownCapabilities[capability] {
			return fmt.Errorf("unknown capability %q", capability)
		}
	}
	return nil
}

// validateScheduleTimeline proves that each fixture's expected schedule
// versions, predecessors, and projected starts follow from the decisions
// before it. A target scenario may be red in execution; its model must still
// be internally coherent.
func (sc Scenario) validateScheduleTimeline() error {
	schedules := make(map[string]RentalScheduleSpec, len(sc.World.Rentals))
	for _, rental := range sc.World.Rentals {
		schedules[rental.ID] = RentalScheduleSpec{RentalID: rental.ID}
	}
	for _, schedule := range sc.World.RentalSchedules {
		schedules[schedule.RentalID] = schedule
	}
	bookingIDs, runIDs := scheduleIdentities(schedules)
	requests := map[string]RequestSpec{}
	var elapsed time.Duration
	for i, step := range sc.Steps() {
		if step.Advance != nil {
			elapsed += step.Advance.Duration()
			continue
		}
		if step.Reconcile != "" {
			if expired, ok := expireQueuedBooking(schedules, "run-"+step.Reconcile, sc.World.Start(), sc.World.Start().Add(elapsed)); ok {
				delete(bookingIDs, expired.BookingID)
				delete(runIDs, expired.RunID)
			}
		}
		runName := step.Submit
		request := step.Request
		if step.Submit != "" {
			requests[step.Submit] = *step.Request
		} else if step.Reconcile != "" {
			runName = step.Reconcile
			original := requests[step.Reconcile]
			request = &original
		}
		for rentalID, candidate := range step.Expect.Candidates {
			if err := validateCandidateSchedule(schedules[rentalID], elapsed, candidate); err != nil {
				return fmt.Errorf("timeline[%d]: candidate %q: %w", i, rentalID, err)
			}
		}
		if step.Expect.Booking == nil {
			continue
		}
		booking := *step.Expect.Booking
		runID := "run-" + runName
		if bookingIDs[booking.BookingID] {
			return fmt.Errorf("timeline[%d]: Booking %q already exists", i, booking.BookingID)
		}
		if runIDs[runID] {
			return fmt.Errorf("timeline[%d]: Run %q already has a nonterminal Booking", i, runID)
		}
		schedule := schedules[booking.RentalID]
		if err := validateBookingDecision(schedule, elapsed, request, booking); err != nil {
			return fmt.Errorf("timeline[%d]: %w", i, err)
		}
		schedule.Version = booking.ScheduleVersion
		if booking.State == BookingRunning {
			schedule.Running = &RunningBookingSpec{
				BookingID:                booking.BookingID,
				RunID:                    runID,
				RemainingMaxRuntime:      *request.MaxRuntime,
				RemainingExpectedRuntime: request.ExpectedRuntime,
			}
		} else {
			schedule.Queued = append(schedule.Queued, QueuedBookingSpec{
				BookingID:       booking.BookingID,
				RunID:           runID,
				MaxRuntime:      *request.MaxRuntime,
				ExpectedRuntime: request.ExpectedRuntime,
				LatestStart:     booking.LatestStart,
			})
		}
		bookingIDs[booking.BookingID] = true
		runIDs[runID] = true
		schedules[booking.RentalID] = schedule
	}
	return nil
}

func scheduleIdentities(schedules map[string]RentalScheduleSpec) (map[string]bool, map[string]bool) {
	bookingIDs := map[string]bool{}
	runIDs := map[string]bool{}
	for _, schedule := range schedules {
		if schedule.Running != nil {
			bookingIDs[schedule.Running.BookingID] = true
			runIDs[schedule.Running.RunID] = true
		}
		for _, booking := range schedule.Queued {
			bookingIDs[booking.BookingID] = true
			runIDs[booking.RunID] = true
		}
	}
	return bookingIDs, runIDs
}

func validateBookingDecision(schedule RentalScheduleSpec, elapsed time.Duration, request *RequestSpec, booking BookingExpectation) error {
	if request == nil || request.MaxRuntime == nil {
		return fmt.Errorf("Booking %q requires its submitted Run's max_runtime", booking.BookingID)
	}
	if want := schedule.Version + 1; booking.ScheduleVersion != want {
		return fmt.Errorf("Booking %q schedule_version is %d, want %d", booking.BookingID, booking.ScheduleVersion, want)
	}
	if booking.State == BookingRunning {
		if schedule.Running != nil {
			return fmt.Errorf("RunningBooking %q requires an empty RentalSchedule", booking.BookingID)
		}
		return nil
	}
	if schedule.Running == nil {
		return fmt.Errorf("QueuedBooking %q requires a RunningBooking", booking.BookingID)
	}
	if len(schedule.Queued) >= MaxQueuedBookings {
		return fmt.Errorf("QueuedBooking %q appends to a full RentalSchedule; at most %d Bookings may wait", booking.BookingID, MaxQueuedBookings)
	}
	if want := schedule.tailBookingID(); booking.AfterBooking != want {
		return fmt.Errorf("QueuedBooking %q follows %q, want current tail %q", booking.BookingID, booking.AfterBooking, want)
	}
	wait := schedule.projectedWait(elapsed)
	if booking.ProjectedStart == nil || booking.ProjectedStart.Duration() != wait {
		return fmt.Errorf("QueuedBooking %q projected_start_in is %v, want %v from preceding expected runtimes", booking.BookingID, durationValue(booking.ProjectedStart), wait)
	}
	return nil
}

// validateCandidateSchedule proves the fixture's two schedule claims about one
// candidate are the ones its own world supports: evidence has to match the
// schedule this Rental holds at this point in the timeline, and an assertion that
// nothing was recorded has to be made about a Rental holding nothing.
func validateCandidateSchedule(schedule RentalScheduleSpec, elapsed time.Duration, candidate CandidateExpectation) error {
	if candidate.NoSchedule && candidate.Schedule != nil {
		return fmt.Errorf("a candidate cannot both record a schedule and record none")
	}
	if candidate.NoSchedule && schedule.Running != nil {
		return fmt.Errorf("no schedule is expected, and this Rental holds Booking %q", schedule.Running.BookingID)
	}
	if candidate.Schedule == nil {
		return nil
	}
	return validateScheduleEvidence(schedule, elapsed, *candidate.Schedule)
}

func validateScheduleEvidence(schedule RentalScheduleSpec, elapsed time.Duration, expect ScheduleEvidenceExpectation) error {
	if expect.Version != schedule.Version {
		return fmt.Errorf("schedule version is %d, want %d", expect.Version, schedule.Version)
	}
	if schedule.Running == nil || expect.Running == nil ||
		expect.Running.BookingID != schedule.Running.BookingID ||
		expect.Running.RunID != schedule.Running.RunID ||
		expect.Running.RemainingMaxRuntime.Duration() != schedule.runningMaxRemaining(elapsed) ||
		expect.Running.expectedRemaining().Duration() != schedule.runningExpectedRemaining(elapsed) ||
		durationValue(expect.Running.Overrun) != schedule.runningOverrun(elapsed) {
		return fmt.Errorf("RunningBooking evidence does not match the current schedule")
	}
	if len(expect.Preceding) != len(schedule.Queued) {
		return fmt.Errorf("preceding has %d Bookings, want %d", len(expect.Preceding), len(schedule.Queued))
	}
	for i, booking := range schedule.Queued {
		actual := expect.Preceding[i]
		if actual.BookingID != booking.BookingID || actual.RunID != booking.RunID ||
			actual.MaxRuntime.Duration() != booking.MaxRuntime.Duration() ||
			actual.expected().Duration() != booking.expected().Duration() {
			return fmt.Errorf("preceding[%d] does not match QueuedBooking %q", i, booking.BookingID)
		}
	}
	if want := schedule.projectedWait(elapsed); expect.ProjectedStart.Duration() != want {
		return fmt.Errorf("projected_start_in is %v, want %v", expect.ProjectedStart.Duration(), want)
	}
	return nil
}

func expireQueuedBooking(schedules map[string]RentalScheduleSpec, runID string, start, now time.Time) (QueuedBookingSpec, bool) {
	for rentalID, schedule := range schedules {
		for i, booking := range schedule.Queued {
			if booking.RunID != runID || booking.LatestStart == nil || booking.LatestStart.Resolve(start).After(now) {
				continue
			}
			schedule.Queued = slices.Delete(schedule.Queued, i, i+1)
			schedule.Version++
			schedules[rentalID] = schedule
			return booking, true
		}
	}
	return QueuedBookingSpec{}, false
}

func (schedule RentalScheduleSpec) tailBookingID() string {
	if len(schedule.Queued) > 0 {
		return schedule.Queued[len(schedule.Queued)-1].BookingID
	}
	return schedule.Running.BookingID
}

// projectedWait is the p50 wait for the next arriving Booking: the running
// Booking's expected remaining runtime plus every waiting Booking's
// expected runtime. Max runtimes stay the enforced ceiling behind
// latest-start guarantees; expectations drive projections and scoring.
func (schedule RentalScheduleSpec) projectedWait(elapsed time.Duration) time.Duration {
	wait := schedule.runningExpectedRemaining(elapsed)
	for _, booking := range schedule.Queued {
		wait += booking.expected().Duration()
	}
	return wait
}

func (schedule RentalScheduleSpec) runningMaxRemaining(elapsed time.Duration) time.Duration {
	if schedule.Running == nil {
		return 0
	}
	return max(0, schedule.Running.RemainingMaxRuntime.Duration()-elapsed)
}

// runningOverrun is how far past its enforced maximum the running Booking has
// gone by this point in the timeline. It is the arithmetic the remaining runtimes
// stop being able to express: both bottom out at zero, so a fixture asserting a
// machine nothing can project from and one asserting a machine a moment from
// free would state the same evidence.
func (schedule RentalScheduleSpec) runningOverrun(elapsed time.Duration) time.Duration {
	if schedule.Running == nil {
		return 0
	}
	return max(0, elapsed-schedule.Running.RemainingMaxRuntime.Duration())
}

func (schedule RentalScheduleSpec) runningExpectedRemaining(elapsed time.Duration) time.Duration {
	if schedule.Running == nil {
		return 0
	}
	return max(0, schedule.Running.expectedRemaining().Duration()-elapsed)
}

func durationValue(value *Duration) time.Duration {
	if value == nil {
		return 0
	}
	return value.Duration()
}

func (step StepSpec) validate(submitted map[string]bool) error {
	actions := 0
	if step.Submit != "" {
		actions++
	}
	if step.Advance != nil {
		actions++
	}
	if step.Reconcile != "" {
		actions++
	}
	if actions != 1 {
		return fmt.Errorf("each step is exactly one of submit, advance, or reconcile")
	}
	switch {
	case step.Submit != "":
		if step.Request == nil || step.Expect == nil {
			return fmt.Errorf("submit %q requires a request and an expect", step.Submit)
		}
		if submitted[step.Submit] {
			return fmt.Errorf("run %q submitted twice", step.Submit)
		}
		submitted[step.Submit] = true
	case step.Advance != nil:
		if step.Request != nil || step.Expect != nil {
			return fmt.Errorf("advance carries no request or expect")
		}
	case step.Reconcile != "":
		if step.Expect == nil {
			return fmt.Errorf("reconcile %q requires an expect", step.Reconcile)
		}
		if step.Request != nil {
			return fmt.Errorf("reconcile carries no request")
		}
		if !submitted[step.Reconcile] {
			return fmt.Errorf("reconcile %q references a run never submitted", step.Reconcile)
		}
	}
	return nil
}

func (w WorldSpec) validate() error {
	for ref, image := range w.Images {
		if !ociImageRefPattern.MatchString(ref) {
			return fmt.Errorf("image %q must be digest-pinned", ref)
		}
		if !image.Registry.valid() {
			return fmt.Errorf("image %q: unknown registry answer %q", ref, image.Registry)
		}
		// An image states its content whatever the registry will say about it.
		// What is running and what can be read about it are two different facts,
		// and the commonest real failure is exactly their disagreement: a
		// control plane that cannot resolve a manifest against a node that can
		// still pull the image.
		if len(image.Layers) == 0 {
			return fmt.Errorf("image %q needs at least one layer", ref)
		}
		for _, layer := range image.Layers {
			if !ociDigestPattern.MatchString(layer.Digest) || layer.Size <= 0 {
				return fmt.Errorf("image %q: layers need an exact sha256 digest and a positive size", ref)
			}
			if layer.DiffID != "" && !ociDigestPattern.MatchString(layer.DiffID) {
				return fmt.Errorf("image %q: layer %s states an inexact diff ID %q", ref, layer.Digest, layer.DiffID)
			}
		}
		// A real resolver reads diff IDs from one config blob that names one for
		// every layer, so an image names them all or names none. Half would let
		// a fixture encode a manifest production cannot produce, and silently
		// double-charge exactly the layers that omitted one.
		if named := image.diffIDCount(); named != 0 && named != len(image.Layers) {
			return fmt.Errorf(
				"image %q names %d diff IDs for %d layers: a config blob names one for every layer or none",
				ref, named, len(image.Layers),
			)
		}
	}
	if err := w.validateDiffIDReporting(); err != nil {
		return err
	}
	if w.Prewarm != nil {
		if err := w.Prewarm.validate(); err != nil {
			return err
		}
	}
	artifacts := map[string]ArtifactSpec{}
	for _, artifact := range w.Artifacts {
		if artifact.ID == "" || artifact.Size <= 0 {
			return fmt.Errorf("Artifacts need an id and a positive size")
		}
		if !ociDigestPattern.MatchString(artifact.ContentDigest) {
			return fmt.Errorf("Artifact %q needs an exact sha256 content digest", artifact.ID)
		}
		if _, exists := artifacts[artifact.ID]; exists {
			return fmt.Errorf("duplicate Artifact %q", artifact.ID)
		}
		artifacts[artifact.ID] = artifact
	}
	layerDigests := w.layerDigests()
	ids := map[string]bool{}
	for _, rental := range w.Rentals {
		if rental.ID == "" {
			return fmt.Errorf("rentals need an id")
		}
		if ids[rental.ID] {
			return fmt.Errorf("duplicate id %q", rental.ID)
		}
		ids[rental.ID] = true
		for _, ref := range rental.CachedImages {
			if _, ok := w.Images[ref]; !ok {
				return fmt.Errorf("rental %q caches undefined image %q", rental.ID, ref)
			}
		}
		for _, digest := range rental.CachedLayers {
			if !layerDigests[digest] {
				return fmt.Errorf("rental %q caches undefined layer %q", rental.ID, digest)
			}
		}
		if err := validateArtifactReplicas("rental "+rental.ID, rental.ArtifactReplicas, artifacts); err != nil {
			return err
		}
		cacheMounts := map[string]bool{}
		for _, held := range rental.CacheMounts {
			if !domain.ValidCacheName(held.Name) {
				return fmt.Errorf("rental %q holds a Cache Mount named %q, which is not a cache name", rental.ID, held.Name)
			}
			// One machine holds one cache per identity, and the identity carries
			// the workspace. Two workspaces holding "compiler-cache" here is the
			// world this whole model exists to keep apart, so it is not a
			// duplicate.
			identity := domain.CacheIdentity(held.Workspace, held.Requirement())
			if cacheMounts[identity] {
				return fmt.Errorf("rental %q holds Cache Mount %q twice", rental.ID, identity)
			}
			cacheMounts[identity] = true
		}
		if rental.Unpriced && rental.RatePerHourUSD != 0 {
			return fmt.Errorf("rental %q is unpriced and states a rate_per_hour_usd of %v", rental.ID, rental.RatePerHourUSD)
		}
		if !rental.Unpriced && rental.RatePerHourUSD <= 0 {
			return fmt.Errorf("rental %q needs a positive rate_per_hour_usd", rental.ID)
		}
		if err := rental.Billing.validate("rental " + rental.ID); err != nil {
			return err
		}
	}
	rentalsWithSchedules := map[string]bool{}
	bookingOwners := map[string]string{}
	runOwners := map[string]string{}
	for _, schedule := range w.RentalSchedules {
		if !ids[schedule.RentalID] {
			return fmt.Errorf("RentalSchedule references unknown Rental %q", schedule.RentalID)
		}
		if rentalsWithSchedules[schedule.RentalID] {
			return fmt.Errorf("Rental %q has more than one RentalSchedule", schedule.RentalID)
		}
		rentalsWithSchedules[schedule.RentalID] = true
		if err := schedule.validate(w.Start()); err != nil {
			return err
		}
		if err := validateScheduleOwnership(schedule, bookingOwners, runOwners); err != nil {
			return err
		}
		if schedule.Running != nil && w.rental(schedule.RentalID).IdleLeaseExpiresIn != nil {
			return fmt.Errorf("rental %q: only an empty RentalSchedule may carry an idle lease", schedule.RentalID)
		}
	}
	for _, host := range w.Hosts {
		if host.ID == "" {
			return fmt.Errorf("hosts need an id")
		}
		if ids[host.ID] {
			return fmt.Errorf("duplicate id %q", host.ID)
		}
		ids[host.ID] = true
		if err := validateArtifactReplicas("host "+host.ID, host.ArtifactReplicas, artifacts); err != nil {
			return err
		}
		if host.RatePerHourUSD <= 0 {
			return fmt.Errorf("host %q needs a positive rate_per_hour_usd", host.ID)
		}
		if err := host.Billing.validate("host " + host.ID); err != nil {
			return err
		}
	}
	for _, offer := range w.Marketplace {
		if offer.ID == "" {
			return fmt.Errorf("marketplace offers need an id")
		}
		if ids[offer.ID] {
			return fmt.Errorf("duplicate id %q", offer.ID)
		}
		ids[offer.ID] = true
		if offer.RatePerHourUSD <= 0 {
			return fmt.Errorf("marketplace offer %q needs a positive rate_per_hour_usd", offer.ID)
		}
		if err := offer.Billing.validate("marketplace offer " + offer.ID); err != nil {
			return err
		}
		if offer.Provisioning.Expected.Duration() <= 0 {
			return fmt.Errorf("marketplace offer %q needs a provisioning estimate", offer.ID)
		}
		// Every stage is stated, including a stage this fixture wants to cost
		// nothing. Zero is a world worth writing down and silence is how the whole
		// of provisioning came to be free: an offer publishing ten minutes of
		// provisioning that the world spent none of put its execution straight into
		// running, so a Run's start was the moment its launch was accepted and the
		// three earliest stages of every launch had no actual at all.
		for stage, spent := range map[string]*Duration{
			"acquisition": offer.Provisioning.Acquisition,
			"boot":        offer.Provisioning.Boot,
			"agent_ready": offer.Provisioning.AgentReady,
		} {
			if spent == nil {
				return fmt.Errorf("marketplace offer %q does not say what it spends on %s", offer.ID, stage)
			}
			if spent.Duration() < 0 {
				return fmt.Errorf("marketplace offer %q spends %v on %s", offer.ID, spent.Duration(), stage)
			}
		}
		if err := validateReliability("marketplace offer "+offer.ID, offer.Reliability); err != nil {
			return err
		}
	}
	pathIDs := map[string]bool{}
	for _, path := range w.Paths {
		if !ids[path.From] || path.To == "" || path.Scope == "" || path.P10Mbps <= 0 {
			return fmt.Errorf("paths need a known source, destination, scope, and positive p10_mbps")
		}
		// A confidence outside the unit interval is not a statement about how much
		// a publisher stands behind their measurement, and a fixture that made one
		// would be asserting about a fact Mercator refuses to read at all.
		if path.Confidence() < 0 || path.Confidence() > 1 {
			return fmt.Errorf("path %q states confidence %v, which is not a share of certainty", path.From+"/"+path.To, path.Confidence())
		}
		key := path.From + "/" + path.To + "/" + path.Scope
		if pathIDs[key] {
			return fmt.Errorf("duplicate path %q", key)
		}
		pathIDs[key] = true
	}
	runtimeModels := map[string]bool{}
	for _, model := range w.RuntimeModels {
		if !ids[model.Candidate] || model.Minimum.Duration() <= 0 || model.Maximum.Duration() < model.Minimum.Duration() {
			return fmt.Errorf("runtime models need a known candidate and positive ordered bounds")
		}
		key := model.Run + "/" + model.Candidate
		if runtimeModels[key] {
			return fmt.Errorf("duplicate runtime model %q", key)
		}
		runtimeModels[key] = true
	}
	return nil
}

// validateReliability refuses a history no provider could publish. A rate outside
// the unit interval is not a share of this machine's starts; a history nobody
// stands behind is not a measurement, because a rate at zero confidence would
// reach the record as a fact nobody owns, which is the disowned measurement this
// corpus already refuses to state for a link; and a history that states no rate at
// all measured nothing, so a fixture that means silence omits the whole block.
func validateReliability(owner string, spec *ReliabilitySpec) error {
	if spec == nil {
		return nil
	}
	for answer, rate := range map[string]*float64{
		"start_failure_rate": spec.StartFailureRate,
		"interruption_rate":  spec.InterruptionRate,
	} {
		if rate != nil && (*rate < 0 || *rate > 1) {
			return fmt.Errorf("%s states %s %v, which is not a share of its starts", owner, answer, *rate)
		}
	}
	if spec.StartFailureRate == nil && spec.InterruptionRate == nil {
		return fmt.Errorf("%s publishes a reliability history that states no rate, and a machine nobody measured publishes no history at all", owner)
	}
	if spec.Confidence <= 0 || spec.Confidence > 1 {
		return fmt.Errorf("%s publishes a reliability history at confidence %v, and a history nobody stands behind is not one", owner, spec.Confidence)
	}
	return nil
}

// validateDiffIDReporting refuses a world where a host speaks a vocabulary the
// catalog never writes. A Rental that reports diff IDs enumerates nothing at all
// for a layer that names none, so it would offer an inventory that is Known and
// empty: a machine holding every byte, priced a full cold pull at full
// confidence, with the fixture green either way. That is a world the Lab must
// not be able to state.
func (w WorldSpec) validateDiffIDReporting() error {
	reporter := ""
	for _, rental := range w.Rentals {
		if rental.ReportsDiffIDs {
			reporter = rental.ID
			break
		}
	}
	if reporter == "" {
		return nil
	}
	for ref, image := range w.Images {
		if image.diffIDCount() == 0 {
			return fmt.Errorf(
				"rental %q reports diff IDs and image %q names none, so that host would report holding nothing it holds",
				reporter, ref,
			)
		}
	}
	return nil
}

func (billing BillingSpec) validate(owner string) error {
	if billing.SetupFeeUSD < 0 {
		return fmt.Errorf("%s setup fee cannot be negative", owner)
	}
	if billing.MinimumCharge != nil && billing.MinimumCharge.Duration() <= 0 {
		return fmt.Errorf("%s minimum charge must be positive", owner)
	}
	return nil
}

// validateArtifactReplicas is what any machine in this world may be declared
// holding, whether Mercator controls it or borrows a slot on it. Both are
// places a copy can genuinely be, and they differ in what an offer can say
// about the copy rather than in whether it may exist.
func validateArtifactReplicas(machine string, replicas []ArtifactReplicaSpec, artifacts map[string]ArtifactSpec) error {
	for _, replica := range replicas {
		artifact, defined := artifacts[replica.Artifact]
		if !defined {
			return fmt.Errorf("%s holds undefined Artifact %q", machine, replica.Artifact)
		}
		if !replica.State.Valid() {
			return fmt.Errorf(
				"%s holds Artifact %q in state %q, which is neither verified nor unverified",
				machine, replica.Artifact, replica.State,
			)
		}
		if replica.ContentDigest != "" && !ociDigestPattern.MatchString(replica.ContentDigest) {
			return fmt.Errorf("%s holds Artifact %q under a claim that is not an exact sha256 digest", machine, replica.Artifact)
		}
		// A copy of content nothing has produced is a copy of nothing. The
		// object store is what makes an Artifact exist, so a machine can only
		// be found holding a version that was published before this world
		// started.
		if !artifact.Prepublished() {
			return fmt.Errorf(
				"%s holds Artifact %q, which Run %q has not produced yet",
				machine, replica.Artifact, artifact.ProducedBy,
			)
		}
	}
	return nil
}

func validateScheduleOwnership(schedule RentalScheduleSpec, bookingOwners, runOwners map[string]string) error {
	check := func(bookingID, runID string) error {
		if owner := bookingOwners[bookingID]; owner != "" {
			return fmt.Errorf("Booking %q belongs to both Rental %q and Rental %q", bookingID, owner, schedule.RentalID)
		}
		if owner := runOwners[runID]; owner != "" {
			return fmt.Errorf("Run %q has nonterminal Bookings on both Rental %q and Rental %q", runID, owner, schedule.RentalID)
		}
		bookingOwners[bookingID] = schedule.RentalID
		runOwners[runID] = schedule.RentalID
		return nil
	}
	if schedule.Running != nil {
		if err := check(schedule.Running.BookingID, schedule.Running.RunID); err != nil {
			return err
		}
	}
	for _, booking := range schedule.Queued {
		if err := check(booking.BookingID, booking.RunID); err != nil {
			return err
		}
	}
	return nil
}

func (schedule RentalScheduleSpec) validate(start time.Time) error {
	rentalID := schedule.RentalID
	if schedule.Running == nil && len(schedule.Queued) > 0 {
		return fmt.Errorf("rental %q: QueuedBookings require a RunningBooking", rentalID)
	}
	if schedule.Running == nil {
		if schedule.Version != 0 {
			return fmt.Errorf("rental %q: an empty RentalSchedule omits its version", rentalID)
		}
		return nil
	}
	// A version counts every transition this schedule has seen, and each Booking
	// on it took one to get there, so a fixture may state more than it holds and
	// never fewer. Stating fewer is a history Mercator cannot have had, and the
	// arriving Run's Booking would be minted at a version one of these already
	// consumed.
	if occupants := uint64(1 + len(schedule.Queued)); schedule.Version < occupants {
		return fmt.Errorf(
			"rental %q: a RentalSchedule holding %d Bookings is at version %d, and each of them took a transition to get there",
			rentalID, occupants, schedule.Version,
		)
	}
	ids := map[string]bool{}
	runs := map[string]bool{}
	if err := validateBookingIdentity(rentalID, schedule.Running.BookingID, schedule.Running.RunID, ids, runs); err != nil {
		return err
	}
	if schedule.Running.RemainingMaxRuntime.Duration() <= 0 {
		return fmt.Errorf("rental %q: RunningBooking %q needs a positive remaining_max_runtime", rentalID, schedule.Running.BookingID)
	}
	if expected := schedule.Running.RemainingExpectedRuntime; expected != nil &&
		(expected.Duration() <= 0 || expected.Duration() > schedule.Running.RemainingMaxRuntime.Duration()) {
		return fmt.Errorf("rental %q: RunningBooking %q remaining_expected_runtime must be positive and within the max bound", rentalID, schedule.Running.BookingID)
	}
	if completes := schedule.Running.CompletesAfter; completes != nil && completes.Duration() <= 0 {
		return fmt.Errorf("rental %q: RunningBooking %q needs a positive completes_after", rentalID, schedule.Running.BookingID)
	}
	if len(schedule.Queued) > MaxQueuedBookings {
		return fmt.Errorf("rental %q: at most %d QueuedBookings may wait, got %d", rentalID, MaxQueuedBookings, len(schedule.Queued))
	}
	for _, booking := range schedule.Queued {
		if err := validateBookingIdentity(rentalID, booking.BookingID, booking.RunID, ids, runs); err != nil {
			return err
		}
		if booking.MaxRuntime.Duration() <= 0 {
			return fmt.Errorf("rental %q: QueuedBooking %q needs a positive max_runtime", rentalID, booking.BookingID)
		}
		if expected := booking.ExpectedRuntime; expected != nil &&
			(expected.Duration() <= 0 || expected.Duration() > booking.MaxRuntime.Duration()) {
			return fmt.Errorf("rental %q: QueuedBooking %q expected_runtime must be positive and within the max bound", rentalID, booking.BookingID)
		}
		if booking.LatestStart != nil && !booking.LatestStart.Resolve(start).After(start) {
			return fmt.Errorf("rental %q: QueuedBooking %q latest_start must be after the world start", rentalID, booking.BookingID)
		}
	}
	return nil
}

func (w WorldSpec) rental(id string) RentalSpec {
	for _, rental := range w.Rentals {
		if rental.ID == id {
			return rental
		}
	}
	return RentalSpec{}
}

func (w WorldSpec) rentalSchedule(rentalID string) RentalScheduleSpec {
	for _, schedule := range w.RentalSchedules {
		if schedule.RentalID == rentalID {
			return schedule
		}
	}
	return RentalScheduleSpec{RentalID: rentalID}
}

func validateBookingIdentity(rentalID, bookingID, runID string, bookingIDs, runIDs map[string]bool) error {
	if bookingID == "" || runID == "" {
		return fmt.Errorf("rental %q: Bookings need stable booking and run IDs", rentalID)
	}
	if bookingIDs[bookingID] {
		return fmt.Errorf("rental %q: duplicate Booking %q", rentalID, bookingID)
	}
	if runIDs[runID] {
		return fmt.Errorf("rental %q: Run %q appears in more than one Booking", rentalID, runID)
	}
	bookingIDs[bookingID] = true
	runIDs[runID] = true
	return nil
}

func (w WorldSpec) layerDigests() map[string]bool {
	digests := map[string]bool{}
	for _, image := range w.Images {
		for _, layer := range image.Layers {
			digests[layer.Digest] = true
		}
	}
	return digests
}

func (w WorldSpec) candidateIDs() map[string]bool {
	ids := map[string]bool{}
	for _, rental := range w.Rentals {
		ids[rental.ID] = true
	}
	for _, host := range w.Hosts {
		ids[host.ID] = true
	}
	for _, offer := range w.Marketplace {
		ids[offer.ID] = true
	}
	return ids
}

func (w WorldSpec) validRequest(req RequestSpec) error {
	if req.Image == "" {
		return fmt.Errorf("requests need an image")
	}
	if len(w.Images) > 0 {
		if _, ok := w.Images[req.Image]; !ok {
			return fmt.Errorf("request image %q is not defined in the world", req.Image)
		}
	}
	artifacts := w.artifactsByID()
	for _, artifactID := range append(slices.Clone(req.ConsumesArtifacts), req.ProducesArtifacts...) {
		if _, defined := artifacts[artifactID]; !defined {
			return fmt.Errorf("request references undefined Artifact %q", artifactID)
		}
	}
	for _, artifactID := range req.ProducesArtifacts {
		// An Artifact states which Run publishes it, so a request that
		// publishes one the catalog says nobody produces is two documents
		// disagreeing about where content comes from.
		if artifacts[artifactID].Prepublished() {
			return fmt.Errorf("request produces Artifact %q, which the world says was published before it started", artifactID)
		}
	}
	if download := req.Download; download != nil {
		if download.Scope == "" || download.MinP10Mbps <= 0 {
			return fmt.Errorf("a download floor names the scope of the link and a positive min_p10_mbps")
		}
	}
	cacheMounts := map[string]bool{}
	for _, mount := range req.CacheMounts {
		if !domain.ValidCacheName(mount.Name) {
			return fmt.Errorf("Cache Mount %q is not a cache name", mount.Name)
		}
		if cacheMounts[mount.Name] {
			return fmt.Errorf("request has duplicate Cache Mount %q", mount.Name)
		}
		cacheMounts[mount.Name] = true
	}
	phases := map[string]bool{}
	for _, phase := range req.Phases {
		if phase.Name == "" || phase.Duration.Duration() <= 0 {
			return fmt.Errorf("workload phases need a name and positive duration")
		}
		if phases[phase.Name] {
			return fmt.Errorf("request has duplicate workload phase %q", phase.Name)
		}
		phases[phase.Name] = true
	}
	return nil
}

func (w WorldSpec) validExpect(expect ExpectSpec) error {
	ids := w.candidateIDs()
	switch expect.Outcome {
	case OutcomePlace:
		if expect.Offer == "" {
			return fmt.Errorf("outcome \"place\" names the winning offer")
		}
		if !ids[expect.Offer] {
			return fmt.Errorf("expected offer %q is not in the world", expect.Offer)
		}
	case OutcomeFail:
		if expect.Offer != "" || expect.Booking != nil {
			return fmt.Errorf("outcome \"fail\" selects no offer and creates no Booking")
		}
	default:
		return fmt.Errorf("outcome must be \"place\" or \"fail\", got %q", expect.Outcome)
	}
	if booking := expect.Booking; booking != nil {
		if booking.BookingID == "" || booking.RentalID == "" || booking.ScheduleVersion == 0 {
			return fmt.Errorf("expected Booking needs id, rental, and a positive schedule_version")
		}
		if !slices.ContainsFunc(w.Rentals, func(r RentalSpec) bool { return r.ID == booking.RentalID }) {
			return fmt.Errorf("expected Booking Rental %q is not in the world", booking.RentalID)
		}
		if expect.Offer != booking.RentalID {
			return fmt.Errorf("expected Booking Rental %q must be the winning offer", booking.RentalID)
		}
		switch booking.State {
		case BookingRunning:
			if booking.AfterBooking != "" || booking.ProjectedStart != nil {
				return fmt.Errorf("a running Booking has no predecessor or projected_start_in")
			}
		case BookingQueued:
			if booking.AfterBooking == "" || booking.ProjectedStart == nil {
				return fmt.Errorf("a QueuedBooking needs after and projected_start_in")
			}
		default:
			return fmt.Errorf("Booking state must be \"running\" or \"queued\", got %q", booking.State)
		}
	}
	if expect.Disposition != "" && expect.Disposition != "release" && expect.Disposition != "terminate" {
		return fmt.Errorf("disposition must be \"release\" or \"terminate\", got %q", expect.Disposition)
	}
	for id, candidate := range expect.Candidates {
		if !ids[id] {
			return fmt.Errorf("candidate %q is not in the world", id)
		}
		for artifactID, want := range candidate.Artifacts {
			if _, defined := w.artifactsByID()[artifactID]; !defined {
				return fmt.Errorf("candidate %q references undefined Artifact %q", id, artifactID)
			}
			if _, stated := ArtifactExpectations[want]; !stated {
				return fmt.Errorf("candidate %q Artifact %q expects \"hit\", \"miss\", or \"unknown\", got %q", id, artifactID, want)
			}
		}
		for name, want := range candidate.Caches {
			if _, stated := CacheExpectations[want]; !stated {
				return fmt.Errorf("candidate %q cache %q expects \"hit\", \"miss\", or \"unknown\", got %q", id, name, want)
			}
		}
		if candidate.Schedule != nil {
			if !slices.ContainsFunc(w.Rentals, func(r RentalSpec) bool { return r.ID == id }) {
				return fmt.Errorf("candidate %q is not a Rental and cannot carry RentalSchedule evidence", id)
			}
			if candidate.Schedule.Version == 0 || candidate.Schedule.Running == nil {
				return fmt.Errorf("candidate %q schedule evidence needs a version and RunningBooking", id)
			}
		}
	}
	return nil
}

// Artifact is what this world says one version is. The Lab builds its object
// store from the same declarations the placement simulator does, so a copy's
// claim reads the same in both worlds or the two disagree about a fixture.
func (w WorldSpec) Artifact(id string) ArtifactSpec {
	return w.artifactsByID()[id]
}

func (w WorldSpec) artifactsByID() map[string]ArtifactSpec {
	artifacts := make(map[string]ArtifactSpec, len(w.Artifacts))
	for _, artifact := range w.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	return artifacts
}
