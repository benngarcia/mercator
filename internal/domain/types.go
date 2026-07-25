package domain

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

func (p Platform) String() string {
	if p.OS == "" || p.Architecture == "" {
		return ""
	}
	return p.OS + "/" + p.Architecture
}

// ParsePlatform reads an "os/arch" string back into a Platform. It reports
// false for anything that does not name both halves, so a partial answer never
// half-populates a workload's platform.
func ParsePlatform(value string) (Platform, bool) {
	os, arch, found := strings.Cut(value, "/")
	if !found || os == "" || arch == "" {
		return Platform{}, false
	}
	return Platform{OS: os, Architecture: arch}, true
}

type WorkloadRevision struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspace_id"`
	WorkloadID  string       `json:"workload_id"`
	Digest      string       `json:"digest"`
	Spec        WorkloadSpec `json:"spec"`
}

type WorkloadSpec struct {
	Containers []ContainerSpec      `json:"containers"`
	Resources  ResourceRequirements `json:"resources"`
	Network    NetworkRequirements  `json:"network"`
	Placement  PlacementPolicy      `json:"placement"`
	Execution  ExecutionPolicy      `json:"execution"`
	// Artifacts is the immutable content this workload reads and publishes. A
	// declared input is a dependency on a durable Artifact rather than on any
	// particular host, which is what keeps replicas an optimisation.
	Artifacts ArtifactRequirements       `json:"artifacts"`
	Metadata  map[string]string          `json:"metadata,omitempty"`
	Raw       map[string]json.RawMessage `json:"raw,omitempty"`
}

type ContainerSpec struct {
	Name       string                `json:"name"`
	Image      string                `json:"image"`
	Platform   Platform              `json:"platform"`
	Entrypoint *[]string             `json:"entrypoint,omitempty"`
	Args       []string              `json:"args,omitempty"`
	Env        map[string]EnvBinding `json:"env,omitempty"`
	Ports      []PortSpec            `json:"ports,omitempty"`
}

type EnvBinding struct {
	Value *string `json:"value,omitempty"`
}

type PortExposure string

const (
	PortExposureNone    PortExposure = "none"
	PortExposurePublic  PortExposure = "public"
	PortExposurePrivate PortExposure = "private"
)

type PortSpec struct {
	Name          string       `json:"name"`
	ContainerPort int          `json:"container_port"`
	Protocol      string       `json:"protocol"`
	Exposure      PortExposure `json:"exposure"`
}

type ResourceRequirements struct {
	CPU           CPURequirement           `json:"cpu"`
	Memory        MemoryRequirement        `json:"memory"`
	Accelerators  []AcceleratorRequirement `json:"accelerators,omitempty"`
	EphemeralDisk DiskRequirement          `json:"ephemeral_disk"`
}

type CPURequirement struct {
	MinMillis int64 `json:"min_millis"`
}

type MemoryRequirement struct {
	MinBytes int64 `json:"min_bytes"`
}

type DiskRequirement struct {
	MinBytes int64 `json:"min_bytes"`
}

type AcceleratorRequirement struct {
	Vendor         string   `json:"vendor"`
	ModelAnyOf     []string `json:"model_any_of,omitempty"`
	Count          int      `json:"count"`
	MemoryMinBytes int64    `json:"memory_min_bytes"`
}

type InboundNetworkMode string

const (
	InboundNetworkNone       InboundNetworkMode = "none"
	InboundNetworkPublicPort InboundNetworkMode = "public_port"
)

type NetworkScope string

const (
	NetworkScopeRegistry       NetworkScope = "registry"
	NetworkScopePublicInternet NetworkScope = "public_internet"
)

type NetworkRequirements struct {
	Inbound  InboundNetworkMode          `json:"inbound"`
	Download *NetworkDownloadRequirement `json:"download,omitempty"`
}

type NetworkDownloadRequirement struct {
	Scope                    NetworkScope `json:"scope"`
	MinP10Mbps               float64      `json:"min_p10_mbps"`
	MaxMeasurementAgeSeconds int64        `json:"max_measurement_age_seconds"`
	AllowUnknown             bool         `json:"allow_unknown"`
}

type PlacementObjective string

const (
	ObjectiveCheapest          PlacementObjective = "cheapest"
	ObjectiveFastestStart      PlacementObjective = "fastest_start"
	ObjectiveFastestCompletion PlacementObjective = "fastest_completion"
	ObjectiveBalanced          PlacementObjective = "balanced"
)

type PlacementPolicy struct {
	Objective              PlacementObjective `json:"objective"`
	MaxP90StartSeconds     float64            `json:"max_p90_start_seconds,omitempty"`
	ExpectedRuntimeSeconds float64            `json:"expected_runtime_seconds,omitempty"`
	MaxExpectedCostUSD     *float64           `json:"max_expected_cost_usd,omitempty"`
	AllowUnknownPricing    bool               `json:"allow_unknown_pricing,omitempty"`
}

type ExecutionPolicy struct {
	MaxRuntimeSeconds   int64 `json:"max_runtime_seconds"`
	MaxPreStartAttempts int   `json:"max_pre_start_attempts"`
}

type Violation struct {
	Code     string `json:"code"`
	Path     string `json:"path"`
	Required any    `json:"required,omitempty"`
	Offered  any    `json:"offered,omitempty"`
	Message  string `json:"message"`
}

type OfferKind string

const (
	OfferKindStanding      OfferKind = "standing"
	OfferKindProvisionable OfferKind = "provisionable"
)

// ExecutionLane answers whether Mercator can run a second workload here
// without allocating new capacity. It is orthogonal to OfferKind, which
// answers who owns the host: a standing Docker host with an enrolled node and
// a provisioned VM with an enrolled node are both reusable, while a
// provider-native one-shot container is ephemeral however it was allocated.
//
// Only reusable offers may become Rentals, carry Rental Schedules, or accrue
// Warmth that survives a Run.
type ExecutionLane string

const (
	// LaneReusable is capacity Mercator controls through an enrolled node
	// runtime capable of executing successive workloads.
	LaneReusable ExecutionLane = "reusable"
	// LaneEphemeral is a provider-native one-shot execution product. Mercator
	// controls no host runtime between workloads.
	LaneEphemeral ExecutionLane = "ephemeral"
)

func (lane ExecutionLane) Valid() bool {
	return lane == LaneReusable || lane == LaneEphemeral
}

// Reusable reports whether this lane may become a Rental. It reads as the
// question callers actually ask.
func (lane ExecutionLane) Reusable() bool { return lane == LaneReusable }

type OfferSnapshot struct {
	ID           string    `json:"id"`
	RentalID     string    `json:"rental_id,omitempty"`
	ConnectionID string    `json:"connection_id"`
	AdapterType  string    `json:"adapter_type"`
	Kind         OfferKind `json:"kind"`
	// Lane is the offer's reuse semantics, stamped by the Broker from the
	// backend's negotiated capability Declaration rather than claimed by the
	// adapter itself. An adapter cannot advertise reuse it cannot perform.
	Lane         ExecutionLane     `json:"lane"`
	NativeRef    string            `json:"native_ref"`
	ObservedAt   time.Time         `json:"observed_at"`
	ExpiresAt    time.Time         `json:"expires_at"`
	Platform     Platform          `json:"platform"`
	Resources    ResourceInventory `json:"resources"`
	Capabilities CapabilityProfile `json:"capabilities"`
	Network      NetworkFacts      `json:"network"`
	Pricing      PriceModel        `json:"pricing"`
	Queue        *QueueSnapshot    `json:"queue,omitempty"`
	Provisioning *Estimate         `json:"provisioning,omitempty"`
	Images       ImageInventory    `json:"images"`
	// Artifacts is the immutable content this host says it holds a local copy
	// of. It is placement evidence and never a dependency's authority: a Run's
	// inputs are durable in the object store or the Run does not go anywhere.
	Artifacts   ArtifactInventory   `json:"artifacts"`
	Capacity    CapacityEvidence    `json:"capacity"`
	Reliability ReliabilityEvidence `json:"reliability,omitempty"`
}

// KeepsWhatItRuns answers whether content a workload fetches here is still here
// when the next Run asks. The two halves are separate reasons for the same
// answer: a provisionable offer names a machine that does not exist yet, so it
// is a template rather than a host, and an ephemeral-lane offer is a one-shot
// product that holds nothing once its workload exits however it was allocated.
// A standing reusable offer is the only capacity Mercator keeps, which makes it
// the only place Warmth can accumulate.
func (offer OfferSnapshot) KeepsWhatItRuns() bool {
	return offer.Kind == OfferKindStanding && offer.Lane.Reusable()
}

// DefaultRegistryDownloadMbps is what a host is assumed to pull image content
// at when nothing has measured its link to a registry. It is an assumption, so
// it is stated once: a predictor and a reference model that disagree about the
// unmeasured case would disagree about every cold candidate for a reason that
// has nothing to do with either model.
const DefaultRegistryDownloadMbps = 500.0

// AssumedLinkConfidence is how much a transfer duration is worth when the bytes
// that have to move are measured and the link they cross is not. It is
// deliberately short of certainty: nothing measures a host's registry
// throughput today, so a full-confidence duration would be an assumption
// wearing a measurement's clothes. What is certain in that case is the byte
// count, which is what tells a warm candidate from a cold one; the seconds it
// takes are the guess.
const AssumedLinkConfidence = 0.5

// AssumedUnpackMBps is how fast a host is assumed to decompress content it
// already holds into a runnable layer chain, when nothing has measured its
// storage. It stands beside DefaultRegistryDownloadMbps for the same reason and
// with the same standing: a stated assumption rather than a measurement, so a
// duration derived from it is worth AssumedLinkConfidence, which is what any
// duration over an unmeasured rate is worth.
const AssumedUnpackMBps = 250.0

// LinkSpeed is how fast content moves onto a host and how much a duration
// derived from that number is worth. The two travel together because they are
// one answer: a speed nothing stands behind cannot produce a confident
// duration, however precise the arithmetic on it looks.
type LinkSpeed struct {
	Mbps       float64
	Confidence float64
}

// RegistryDownload is this host's pessimistic (p10) registry throughput. A
// published fact is only worth what its publisher said it was worth, and only
// while it is still valid as of the moment this offer was observed: a fact
// carries its own confidence and its own expiry, and reading the mere existence
// of one as a measurement is how an unmeasured constant becomes a certainty.
// Absent a valid fact the answer is the standing assumption, saying so.
func (offer OfferSnapshot) RegistryDownload() LinkSpeed {
	for _, fact := range offer.Network.Download {
		if fact.Scope != NetworkScopeRegistry || fact.Statistic != "p10" || fact.ValueMbps <= 0 {
			continue
		}
		if !fact.ValidUntil.IsZero() && !fact.ValidUntil.After(offer.ObservedAt) {
			continue
		}
		return LinkSpeed{Mbps: fact.ValueMbps, Confidence: fact.Confidence}
	}
	return LinkSpeed{Mbps: DefaultRegistryDownloadMbps, Confidence: AssumedLinkConfidence}
}

// RegistryDownloadMbps is the speed alone, for the simulators that have to move
// bytes rather than predict how long moving them takes.
func (offer OfferSnapshot) RegistryDownloadMbps() float64 {
	return offer.RegistryDownload().Mbps
}

type ResourceInventory struct {
	CPUMillis          int64                  `json:"cpu_millis"`
	MemoryBytes        int64                  `json:"memory_bytes"`
	EphemeralDiskBytes int64                  `json:"ephemeral_disk_bytes"`
	Accelerators       []AcceleratorInventory `json:"accelerators,omitempty"`
}

type AcceleratorInventory struct {
	Vendor string `json:"vendor"`
	Model  string `json:"model"`
	// CanonicalModel is the provider-agnostic GPU id (e.g. "nvidia-a6000")
	// the scheduler matches AcceleratorRequirement.ModelAnyOf against. Adapters
	// derive it from their native model string via internal/gpunorm; Model keeps
	// the provider's raw display name for provenance.
	CanonicalModel string `json:"canonical_model,omitempty"`
	Count          int    `json:"count"`
	MemoryBytes    int64  `json:"memory_bytes"`
}

type CapabilityProfile struct {
	OfferKinds    []OfferKind               `json:"offer_kinds,omitempty"`
	Container     ContainerCapabilities     `json:"container"`
	Lifecycle     LifecycleCapabilities     `json:"lifecycle"`
	Resources     ResourceCapabilities      `json:"resources"`
	Network       NetworkCapabilities       `json:"network"`
	Pricing       PricingCapabilities       `json:"pricing"`
	Observability ObservabilityCapabilities `json:"observability"`
}

type ContainerCapabilities struct {
	MaxContainers      int  `json:"max_containers"`
	SupportsDigestRefs bool `json:"supports_digest_refs"`
	// SupportsEntrypointOverride reports whether the adapter can replace the
	// image's entrypoint at launch. Providers whose container API has no
	// entrypoint field (e.g. Shadeform's docker launch configuration) leave
	// this false so the scheduler never places an entrypoint-overriding
	// workload where the launch would be rejected.
	SupportsEntrypointOverride bool `json:"supports_entrypoint_override"`
	MaxEnvironmentBytes        int  `json:"max_environment_bytes"`
}

type LifecycleCapabilities struct {
	IdempotentLaunch string `json:"idempotent_launch"`
	ListOwned        bool   `json:"list_owned"`
	ProviderTTL      bool   `json:"provider_ttl"`
	CancelQueued     bool   `json:"cancel_queued"`
}

type ResourceCapabilities struct {
	GPUVendors []string `json:"gpu_vendors,omitempty"`
}

type NetworkCapabilities struct {
	Inbound    InboundNetworkMode `json:"inbound"`
	Protocols  []string           `json:"protocols,omitempty"`
	PublicIPv4 bool               `json:"public_ipv4"`
}

type PricingCapabilities struct {
	Known bool `json:"known"`
}

type ObservabilityCapabilities struct {
	Logs    string `json:"logs"`
	Metrics string `json:"metrics"`
	Shell   string `json:"shell"`
}

type NetworkFacts struct {
	Download []NetworkFact `json:"download,omitempty"`
}

type NetworkFact struct {
	Scope       NetworkScope `json:"scope"`
	Statistic   string       `json:"statistic"`
	ValueMbps   float64      `json:"value_mbps"`
	Source      string       `json:"source"`
	SampleCount int          `json:"sample_count"`
	ObservedAt  time.Time    `json:"observed_at"`
	ValidUntil  time.Time    `json:"valid_until"`
	Confidence  float64      `json:"confidence"`
}

type PriceModel struct {
	Currency             string  `json:"currency"`
	SetupFeeUSD          float64 `json:"setup_fee_usd"`
	RatePerSecondUSD     float64 `json:"rate_per_second_usd"`
	MinimumChargeSeconds int64   `json:"minimum_charge_seconds"`
	GranularitySeconds   int64   `json:"granularity_seconds"`
	Known                bool    `json:"known"`
}

type QueueSnapshot struct {
	QueuedWorkSeconds float64 `json:"queued_work_seconds"`
	ActiveSlots       int     `json:"active_slots"`
}

type Estimate struct {
	P50          float64 `json:"p50,omitempty"`
	P90          float64 `json:"p90,omitempty"`
	Expected     float64 `json:"expected,omitempty"`
	Confidence   float64 `json:"confidence,omitempty"`
	Source       string  `json:"source,omitempty"`
	SampleCount  int     `json:"sample_count,omitempty"`
	ModelVersion string  `json:"model_version,omitempty"`
}

// ImageInventory is what one host SAYS IT HOLDS. It answers "what is here" and
// never "what is missing", because missing is a function of what is here and
// what the Run needs, and only the scheduler holds both. The evidence it
// replaced carried a MissingBytes that asserted an answer about an image the
// offer never named, which is why every offer in the tree claimed zero missing
// bytes and every candidate looked fully warm.
type ImageInventory struct {
	// Known is whether the holder enumerated its content at all. False is an
	// honest answer, not a failure: a provider that cannot tell Mercator what a
	// fresh machine holds says so, and the uncertainty is priced rather than
	// mistaken for warmth.
	Known bool `json:"known"`
	// ObservedAt is when the holder last looked. Locality decays: content can
	// be reclaimed between one heartbeat and the next, so the age of this
	// answer is material to how much it is worth. How long anyone stands behind
	// it is the offer's own expiry: the enumeration and the capacity claim come
	// from one observation, and Placement refuses an expired offer outright
	// rather than reading half of it.
	ObservedAt time.Time `json:"observed_at,omitzero"`
	// ImageDigests is every image manifest the host holds whole AND has
	// unpacked, so it can start a container on it now.
	ImageDigests []string `json:"image_digests,omitempty"`
	// PulledImageDigests is every image whose content arrived here and which is
	// not assembled into a runnable layer chain. Fetching and unpacking are
	// separate acts, and a host that has done the first and not the second is
	// neither warm nor cold: what is left is local work rather than a pull, and
	// an operator told "cold" about a machine sitting on the image would go
	// looking for a network problem that is not there.
	PulledImageDigests []string `json:"pulled_image_digests,omitempty"`
	// UnknownImageDigests is every image this host looked at and could not
	// account for. A host that enumerates itself can still fail on one image: a
	// runtime that will not describe it, or a store reporting some of its
	// content present and unable to name which. Listing it here is the host
	// saying "I looked at this one and cannot answer", which is priced as the
	// silence it is. Without it an image absent from the other lists reads as
	// the confident claim that none of it is here, which is exactly the guess
	// this contract exists to keep a node from making.
	UnknownImageDigests []string `json:"unknown_image_digests,omitempty"`
	// LayerDigests is every compressed layer blob the host holds, named the way
	// a registry manifest names it. A host can hold layers of an image it has
	// never held whole, which is the entire reason a second version of the same
	// image starts faster than a first.
	LayerDigests []string `json:"layer_digests,omitempty"`
	// LayerDiffIDs is the same content named the way a container daemon names
	// it: the digest of the uncompressed layer. A Docker daemon can enumerate
	// only these, so a host that reports them and a manifest that lists blob
	// digests are talking about the same bytes in two vocabularies that never
	// meet. The manifest carries both, which is what lets them be compared.
	LayerDiffIDs []string `json:"layer_diff_ids,omitempty"`
}

// Holds reports whether this host holds one image whole and ready to run.
func (inventory ImageInventory) Holds(imageDigest string) bool {
	return imageDigest != "" && slices.Contains(inventory.ImageDigests, imageDigest)
}

// Pulled reports whether this host fetched one image and has not finished
// assembling it.
func (inventory ImageInventory) Pulled(imageDigest string) bool {
	return imageDigest != "" && slices.Contains(inventory.PulledImageDigests, imageDigest)
}

// Undescribed reports whether this host looked at one image and could not say
// what it holds of it. It is silence about one image on a host that answered
// about the rest, which is a weaker claim than the whole enumeration failing
// and a much weaker one than "none of it is here".
func (inventory ImageInventory) Undescribed(imageDigest string) bool {
	return imageDigest != "" && slices.Contains(inventory.UnknownImageDigests, imageDigest)
}

// LocalityState is how much of some content a host has, as an answer rather
// than a number. Unknown is first-class: it says nobody could look, which is
// uncertainty to price and never infeasibility.
type LocalityState string

const (
	LocalityHot     LocalityState = "hot"
	LocalityPartial LocalityState = "partial"
	LocalityCold    LocalityState = "cold"
	LocalityUnknown LocalityState = "unknown"
)

// HoldsLayer reports whether this host holds one layer, in either digest space.
// Which space a host answers in is a property of its runtime and not of the
// content, so a layer it reports as a diff ID is as present as one it reports
// as a blob digest.
func (inventory ImageInventory) HoldsLayer(layer ImageLayer) bool {
	return layer.Digest != "" && slices.Contains(inventory.LayerDigests, layer.Digest) ||
		layer.DiffID != "" && slices.Contains(inventory.LayerDiffIDs, layer.DiffID)
}

// ImageManifest is one image's exact content. It is a property of the image, so
// it travels with the request rather than on an offer: every candidate is being
// asked about the same image, and an offer that restated it could disagree with
// the others.
type ImageManifest struct {
	// Known is false when nothing resolved the manifest. Then no candidate can
	// be told apart on image locality, so the term is zero for all of them and
	// the comparison is unaffected. The only cost is understating absolute
	// start latency, which is recorded rather than hidden.
	Known bool `json:"known"`
	// Unreadable is why nothing could state this image's content, when Known is
	// false. An image nobody pushed, a platform nothing was built for,
	// credentials a registry refused, and a registry that could not be reached
	// at all are four different things for an operator to fix, and the Booking
	// Decision is where they look for the reason a placement stopped telling
	// warm hosts from cold ones.
	Unreadable string `json:"unreadable,omitempty"`
	// Digest is the identity the Run is pinned by, which is the same digest a
	// host reports having pulled. For a multi-platform image that is the index
	// digest and not the platform manifest underneath it: the layers below
	// describe one platform's build, but the name both sides can agree on is
	// the one the reference carries.
	Digest string       `json:"digest,omitempty"`
	Layers []ImageLayer `json:"layers,omitempty"`
}

// ImageWork is what one host still owes before this image can run: bytes to
// fetch from a registry, and bytes already on the machine that are not yet
// unpacked into a layer chain a container can be started on. The two are
// separate because they are different work over different resources, and
// because a host that already paid the network is not cold however much local
// assembly is left.
type ImageWork struct {
	TransferBytes int64
	UnpackBytes   int64
}

// None reports that this image can start here with nothing fetched and nothing
// assembled first.
func (work ImageWork) None() bool { return work.TransferBytes == 0 && work.UnpackBytes == 0 }

// StartWork is what this host still owes before the image can start, and what
// that amounts to as an answer. LocalityUnknown means nobody could say what is
// here, which is not the same as nobody owing anything: an image has to arrive
// from somewhere, so a host that will not enumerate itself owes the whole image
// until something says otherwise. Pricing that silence at zero seconds scored a
// machine nobody can describe exactly like one that is provably ready, which is
// the "silence is warmth" error in the one place it costs a placement.
func (manifest ImageManifest) StartWork(inventory ImageInventory) (ImageWork, LocalityState) {
	if !manifest.Known {
		// Nothing resolved the image, so no candidate can be told from another
		// on locality: the term is the same silence for every one of them and
		// the comparison is unaffected.
		return ImageWork{}, LocalityUnknown
	}
	if inventory.Known && inventory.Holds(manifest.Digest) {
		return ImageWork{}, LocalityHot
	}
	// A manifest that names no layers can confirm a hit and cannot price a
	// miss. Subtracting an empty layer set would charge a host that holds
	// nothing the same zero as one holding the whole image, which is the error
	// this type replaced.
	if len(manifest.Layers) == 0 {
		return ImageWork{}, LocalityUnknown
	}
	if !inventory.Known {
		return ImageWork{TransferBytes: manifest.compressedBytes()}, LocalityUnknown
	}
	// A host that says it pulled this image and cannot run it holds the bytes
	// of every layer it did not enumerate as unpacked: what it owes on those is
	// assembly, not a transfer. Charging it the network again would price a
	// machine sitting on 18GB exactly like one that has never seen the image.
	pulled := inventory.Pulled(manifest.Digest)
	work, here := ImageWork{}, 0
	for _, layer := range manifest.Layers {
		switch {
		case inventory.HoldsLayer(layer):
			here++
		case pulled:
			work.UnpackBytes += layer.CompressedBytes
			here++
		default:
			work.TransferBytes += layer.CompressedBytes
		}
	}
	switch {
	case inventory.Undescribed(manifest.Digest):
		// Layers this host enumerated are here whatever it could not say about
		// the image as a whole, so the bytes still come from its evidence. The
		// answer is unknown all the same: what it could not describe is exactly
		// the part that decides whether those bytes are enough to start on.
		return work, LocalityUnknown
	case work.None():
		return work, LocalityHot
	case here == 0:
		return work, LocalityCold
	default:
		return work, LocalityPartial
	}
}

// compressedBytes is everything this image would cost to fetch from a registry.
func (manifest ImageManifest) compressedBytes() int64 {
	total := int64(0)
	for _, layer := range manifest.Layers {
		total += layer.CompressedBytes
	}
	return total
}

// ImageLayer is one layer named in both digest spaces at once. Digest is the
// compressed blob a registry serves and the only identity a pull can be issued
// against; DiffID is the uncompressed content a container daemon enumerates.
// Carrying both is what makes a host that answers in either vocabulary
// comparable against the same manifest. CompressedBytes is the only size that
// predicts transfer; what it costs on disk once unpacked answers a different
// question.
type ImageLayer struct {
	Digest          string `json:"digest"`
	DiffID          string `json:"diff_id,omitempty"`
	CompressedBytes int64  `json:"compressed_bytes"`
}

// ReferenceDigest is the digest an image reference is pinned to, and empty for
// a reference that names a tag instead. It is the only part of a reference two
// machines can compare: a registry host and a repository path are how content
// is reached, and the digest is what the content is.
func ReferenceDigest(reference string) string {
	_, digest, found := strings.Cut(reference, "@")
	if !found {
		return ""
	}
	return digest
}

type CapacityEvidence struct {
	Available  bool    `json:"available"`
	Confidence float64 `json:"confidence"`
}

type ReliabilityEvidence struct {
	StartFailureRate float64 `json:"start_failure_rate,omitempty"`
	InterruptionRate float64 `json:"interruption_rate,omitempty"`
	Confidence       float64 `json:"confidence,omitempty"`
}

type BookingState string

const (
	BookingStateRunning BookingState = "running"
	BookingStateQueued  BookingState = "queued"
)

type Booking struct {
	ID               string       `json:"id"`
	RunID            string       `json:"run_id"`
	RentalID         string       `json:"rental_id"`
	State            BookingState `json:"state"`
	AfterBookingID   string       `json:"after_booking_id,omitempty"`
	ProjectedStartAt *time.Time   `json:"projected_start_at,omitempty"`
	LatestStartAt    *time.Time   `json:"latest_start_at,omitempty"`
	ScheduleVersion  uint64       `json:"schedule_version"`
}

type BookingDecision struct {
	ID                      string              `json:"id"`
	RunID                   string              `json:"run_id,omitempty"`
	WorkloadRevisionDigest  string              `json:"workload_revision_digest"`
	EvaluatedAt             time.Time           `json:"evaluated_at"`
	ModelVersion            string              `json:"model_version"`
	Policy                  PlacementPolicy     `json:"policy"`
	CollectionReport        CollectionReport    `json:"collection_report"`
	Candidates              []CandidateDecision `json:"candidates"`
	SelectedOfferSnapshotID string              `json:"selected_offer_snapshot_id,omitempty"`
	Booking                 *Booking            `json:"booking,omitempty"`
	SelectionReasonCodes    []string            `json:"selection_reason_codes"`
}

type CollectionReport struct {
	ConnectionsQueried   []string `json:"connections_queried,omitempty"`
	ConnectionsFromCache []string `json:"connections_from_cache,omitempty"`
	ExcludedConnections  []string `json:"excluded_connections,omitempty"`
}

type CandidateDecision struct {
	OfferSnapshotID string               `json:"offer_snapshot_id"`
	ConnectionID    string               `json:"connection_id,omitempty"`
	AdapterType     string               `json:"adapter_type,omitempty"`
	NativeRef       string               `json:"native_ref,omitempty"`
	Disposition     CandidateDisposition `json:"disposition"`
	Feasible        bool                 `json:"feasible"`
	Rejections      []Violation          `json:"rejections,omitempty"`
	// ImageLocality is how much of the Run's image this candidate was found to
	// have. It is the qualitative half of the pull estimate, and only the
	// control plane can state it: the host says what it holds, the manifest
	// says what the image is, and the answer is the subtraction. A reader of
	// the decision needs it to tell a machine that has to pull from one that
	// only has to finish unpacking, which are the same seconds and different
	// problems.
	ImageLocality LocalityState `json:"image_locality,omitempty"`
	// ArtifactEvidence is what this candidate was found holding of the
	// immutable content the Run reads, one entry per declared input. It stands
	// beside ImageLocality rather than folded into it, because they are answers
	// about different content: an image is what the runtime fetches to start a
	// container, an Artifact is what the workload reads once it is running, and
	// one host is routinely warm for one and cold for the other.
	ArtifactEvidence []ArtifactEvidence `json:"artifact_evidence,omitempty"`
	Estimates        CandidateEstimates `json:"estimates"`
	ScoreUSD         float64            `json:"score_usd,omitempty"`
}

type CandidateDisposition string

const (
	CandidateDispositionRunNow    CandidateDisposition = "run_now_existing_rental"
	CandidateDispositionQueue     CandidateDisposition = "queue_existing_rental"
	CandidateDispositionProvision CandidateDisposition = "provision_fresh_rental"
	// CandidateDispositionEphemeral is a one-shot launch on a provider-native
	// execution product. It reads differently from the three Rental
	// dispositions because it is a different thing: nothing is held afterwards,
	// so no later Run can queue behind it or inherit its warmth.
	CandidateDispositionEphemeral CandidateDisposition = "launch_ephemeral"
)

type CandidateEstimates struct {
	QueueSeconds     Estimate `json:"queue_seconds"`
	ProvisionSeconds Estimate `json:"provision_seconds"`
	PullSeconds      Estimate `json:"pull_seconds"`
	// ArtifactSeconds is what this candidate would still spend reading the
	// Run's declared inputs out of the object store. It is separate from
	// PullSeconds because it is a different transfer over different content
	// from a different authority, and folding the two together would leave a
	// reader unable to tell a machine that has to fetch an image from one that
	// has to fetch a dataset forty times its size.
	ArtifactSeconds Estimate `json:"artifact_seconds"`
	StartSeconds    Estimate `json:"start_seconds"`
	CostUSD         Estimate `json:"cost_usd"`
}

type RunOutcome string

const (
	RunOutcomeSucceeded RunOutcome = "succeeded"
	RunOutcomeFailed    RunOutcome = "failed"
	RunOutcomeCancelled RunOutcome = "cancelled"
)

func (outcome RunOutcome) Valid() bool {
	switch outcome {
	case RunOutcomeSucceeded, RunOutcomeFailed, RunOutcomeCancelled:
		return true
	default:
		return false
	}
}

type CleanupState string

const (
	CleanupNotRequired CleanupState = "not_required"
	CleanupPending     CleanupState = "pending"
	CleanupConfirmed   CleanupState = "confirmed"
	CleanupBlocked     CleanupState = "blocked"
)

type ProviderError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
	SideEffect string `json:"side_effect,omitempty"`
	LaunchKey  string `json:"launch_key"`
}

func (providerError ProviderError) Validate() error {
	switch {
	case providerError.Code == "":
		return fmt.Errorf("code is required")
	case providerError.Message == "":
		return fmt.Errorf("message is required")
	case providerError.LaunchKey == "":
		return fmt.Errorf("launch_key is required")
	case providerError.SideEffect != "" && providerError.SideEffect != "none" && providerError.SideEffect != "indeterminate":
		return fmt.Errorf("unknown side effect certainty %q", providerError.SideEffect)
	default:
		return nil
	}
}

type CleanupError struct {
	ProviderError
	Disposition Disposition `json:"disposition"`
}

func (cleanupError CleanupError) Validate() error {
	if err := cleanupError.ProviderError.Validate(); err != nil {
		return err
	}
	if !cleanupError.Disposition.Valid() {
		return fmt.Errorf("unknown disposition %q", cleanupError.Disposition)
	}
	return nil
}

// Disposition is the cost-safety discriminator that records, at launch time,
// what cleanup must do for a run. It is recorded explicitly on the launch
// intent and the cleanup path dispatches on the RECORDED value; it is never
// re-inferred from live offers/state at cleanup time. This is what makes
// teardown crash-safe and orphan-free.
//
//   - DispositionTerminate: the run created a resource WE OWN (a provisioned
//     host/instance) that MUST be destroyed on cleanup.
//   - DispositionRelease: the run occupies a slot in a pool we DON'T own (a
//     standing pool); cleanup removes only our job/container and never touches
//     the host.
type Disposition string

const (
	DispositionRelease   Disposition = "release"
	DispositionTerminate Disposition = "terminate"
)

func (disposition Disposition) Valid() bool {
	return disposition == DispositionRelease || disposition == DispositionTerminate
}

// DispositionForOfferKind maps the selected offer's ownership model to its
// required cleanup action.
func DispositionForOfferKind(kind OfferKind) (Disposition, error) {
	switch kind {
	case OfferKindProvisionable:
		return DispositionTerminate, nil
	case OfferKindStanding:
		return DispositionRelease, nil
	default:
		return "", fmt.Errorf("domain: cleanup disposition for unknown offer kind %q", kind)
	}
}

type RunRecord struct {
	ID                 string       `json:"id"`
	WorkspaceID        string       `json:"workspace_id"`
	WorkloadRevisionID string       `json:"workload_revision_id"`
	Phase              string       `json:"phase"`
	Outcome            RunOutcome   `json:"outcome,omitempty"`
	ExitCode           *int         `json:"exit_code,omitempty"`
	Cleanup            CleanupState `json:"cleanup"`
	// Disposition surfaces the RECORDED cleanup disposition (terminate vs
	// release) so operators can see whether this run will destroy a host it owns
	// or merely release a borrowed slot. Empty until a launch intent is recorded.
	Disposition  Disposition   `json:"disposition,omitempty"`
	CleanupError *CleanupError `json:"cleanup_error,omitempty"`
	Closed       bool          `json:"closed"`
	// CreatedBy and CancelledBy are the audited principals of the create and
	// cancel commands: a signed-in operator's email, or "bearer" for
	// machine-token calls. Empty on runs recorded before auditing existed or
	// with auth disabled.
	CreatedBy   string `json:"created_by,omitempty"`
	CancelledBy string `json:"cancelled_by,omitempty"`
}

type AttemptRecord struct {
	ID             string `json:"id"`
	RunID          string `json:"run_id"`
	LaunchKey      string `json:"launch_key"`
	OwnershipToken string `json:"ownership_token"`
}
