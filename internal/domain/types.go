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
	Artifacts ArtifactRequirements `json:"artifacts"`
	// Caches is the mutable state this workload wants mounted across Runs. It is
	// best-effort by construction: a cache that is not here costs the
	// application the work of rebuilding what was in it and never keeps the Run
	// from running. Every name is scoped to the Run's own workspace, which is
	// what makes two tenants naming one cache two caches.
	Caches   []CacheMountRequirement    `json:"caches,omitempty"`
	Metadata map[string]string          `json:"metadata,omitempty"`
	Raw      map[string]json.RawMessage `json:"raw,omitempty"`
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
	NetworkScopeRegistry NetworkScope = "registry"
	// NetworkScopeObjectStore is the path a host reads durable Artifact content
	// over. It is a scope of its own because it is a different path from the
	// registry one and routinely a different speed: a machine beside the object
	// store and a machine beside the registry are different machines, and until
	// this existed no fact anybody published could steer an Artifact read at all.
	NetworkScopeObjectStore    NetworkScope = "object_store"
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
	// AllowUnknown says this Run would rather run on a machine that has published
	// nothing about the link than not run at all. It is asked when nobody
	// answered, and never instead of a comparison: a candidate whose published
	// fact answers and falls below the floor is refused whatever this says.
	AllowUnknown bool `json:"allow_unknown"`
}

// Answer is what this candidate's published facts say about the floor this
// requirement states, and whether anything said it.
//
// A fact answers when its publisher stands behind it (NetworkFact.Answers), it
// describes the link the Run asked about, and it is fresh enough for the Run that
// asked. Nothing else is an answer, which is what makes a number its own
// publisher disowned worth exactly what saying nothing is worth: the caller then
// decides what silence buys through AllowUnknown, rather than a disowned fact
// striking a candidate out where an identical silent one is admitted.
//
// It is the same fact OfferSnapshot.DownloadRate prices the transfer from, so
// the bound and the prediction are asked of one measurement rather than of two
// facts that happen to be published together.
func (req NetworkDownloadRequirement) Answer(facts NetworkFacts, at time.Time) (NetworkFact, bool) {
	fact, answered := facts.DownloadP10(req.Scope, at)
	if !answered {
		return NetworkFact{}, false
	}
	if req.MaxMeasurementAgeSeconds > 0 && at.Sub(fact.ObservedAt) > time.Duration(req.MaxMeasurementAgeSeconds)*time.Second {
		return NetworkFact{}, false
	}
	return fact, true
}

type PlacementPolicy struct {
	// Class is the kind of work this Run is, and the only thing that says what
	// waiting is worth to it. See serviceclass.go: the class carries the exchange
	// rates the score is computed over, which is why it replaced the placement
	// objective rather than sitting beside one.
	Class                  ServiceClass `json:"service_class"`
	MaxP90StartSeconds     float64      `json:"max_p90_start_seconds,omitempty"`
	ExpectedRuntimeSeconds float64      `json:"expected_runtime_seconds,omitempty"`
	// ExpectedReadySeconds is how long this workload says it takes to become
	// ready for work once its process is running. It is the only prediction of
	// the application-ready stage there is: readiness is the application's own
	// semantics, so the workload is the only authority that can state it, and a
	// Run that states nothing is predicted nothing rather than charged a prior
	// this model invented for every workload in the fleet.
	ExpectedReadySeconds float64  `json:"expected_ready_seconds,omitempty"`
	MaxExpectedCostUSD   *float64 `json:"max_expected_cost_usd,omitempty"`
	AllowUnknownPricing  bool     `json:"allow_unknown_pricing,omitempty"`
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
	ID       string `json:"id"`
	RentalID string `json:"rental_id,omitempty"`
	// MachineID is the machine behind this capacity, named by the handle the
	// backend has for the machine itself. It is stated only by a backend that can
	// name one: an enrolled node states its node ID, a Docker endpoint states the
	// daemon's own ID, and a provider catalog listing states none, because the
	// machine it describes does not exist yet.
	//
	// It is what a launch history is filed under wherever it exists, which is why
	// it is neither the Rental nor anything about the route Mercator took to reach
	// the machine. A Rental is a lease and two machines can be invited against
	// one; an endpoint label is derived from a DOCKER_HOST or a context name, so
	// two daemons on one box share it and one daemon changes it whenever an
	// operator changes how Mercator reaches it. Either would merge two machines'
	// histories or orphan a machine from its own.
	MachineID    string    `json:"machine_id,omitempty"`
	ConnectionID string    `json:"connection_id"`
	AdapterType  string    `json:"adapter_type"`
	Kind         OfferKind `json:"kind"`
	// Lane is the offer's reuse semantics, stamped by the Broker from the
	// backend's negotiated capability Declaration rather than claimed by the
	// adapter itself. An adapter cannot advertise reuse it cannot perform.
	Lane      ExecutionLane `json:"lane"`
	NativeRef string        `json:"native_ref"`
	// Region is where this capacity is, in the provider's own vocabulary. It is
	// stated by the adapter because only the adapter knows: a Shadeform listing
	// is placed by an explicit cloud and region, a Vast ask carries a
	// geolocation, and a machine Mercator reaches through its own runtime is
	// wherever its operator put it.
	//
	// It exists so a launch prediction has something to fall back to that is
	// narrower than the whole provider. The Blueprint schema has carried a region
	// on rentals and marketplace listings since it was authored and nothing has
	// ever read it, because there was no offer field to read it into.
	Region string `json:"region,omitempty"`
	// InstanceType is the product name a provider sells this capacity under,
	// where it sells one. It is the machine half of a recurring identity: two
	// listings of one instance type in one region are the same product however
	// differently the provider numbered them.
	//
	// A provider that sells no such thing states none. Vast sells asks against
	// individual machines and has no product name, so its listings are
	// distinguished by region and accelerator, which is what it does publish.
	InstanceType string            `json:"instance_type,omitempty"`
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
	Artifacts ArtifactInventory `json:"artifacts"`
	// Caches is the mutable, application-owned state this host says it holds.
	// Every entry names the workspace that owns it, because a cache's identity
	// is workspace-scoped and an offer is read by every workspace's Runs.
	Caches   CacheInventory   `json:"caches,omitzero"`
	Capacity CapacityEvidence `json:"capacity"`
	// Reliability is what this machine's publisher has measured about how it
	// behaves. A machine nobody measured carries none of it, and omitzero is what
	// keeps that off the wire: omitempty never drops a struct, so every offer in the
	// fleet used to publish an empty history and a reader could not tell the
	// unmeasured machines from the ones whose rates happened to be zero.
	Reliability ReliabilityEvidence `json:"reliability,omitzero"`
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

// The names a transfer prediction gives the constant it was priced from when
// nothing measured the path it crosses. They are recorded rather than derived,
// because an operator reading a decision has to be able to tell a machine
// Mercator measured from one it merely assumed, and a bare number cannot say
// which it was.
const (
	AssumptionRegistryRate    = "assumed_registry_rate"
	AssumptionObjectStoreRate = "assumed_object_store_rate"
	AssumptionUnpackRate      = "assumed_unpack_rate"
)

// AssumedLinkConfidence is how much a transfer duration is worth when the bytes
// that have to move are measured and the link they cross is not. It is
// deliberately short of certainty: nothing measures a host's registry
// throughput today, so a full-confidence duration would be an assumption
// wearing a measurement's clothes. What is certain in that case is the byte
// count, which is what tells a warm candidate from a cold one; the seconds it
// takes are the guess.
const AssumedLinkConfidence = 0.5

// AssumedContainerStartSeconds is what asking a container runtime to create a
// container and hold a process in it costs on a machine already holding
// everything it needs. It is an assumption like the rates beside it, stated once
// for the same reason: a predictor and a reference model that disagreed about it
// would disagree about every warm candidate.
//
// It replaces a constant that stood for agent enrollment, container creation,
// and the application becoming ready all at once. One number for three stages
// cannot be calibrated: an actual for it would be the sum of three durations
// with three different causes, and a measurement of any one of them could not
// replace it without replacing the other two.
const AssumedContainerStartSeconds = 1.0

// AssumedUnpackMbps is how fast a host is assumed to decompress content it
// already holds into a runnable layer chain, when nothing has measured its
// storage. It stands beside DefaultRegistryDownloadMbps for the same reason and
// with the same standing: a stated assumption rather than a measurement, so a
// duration derived from it is worth AssumedLinkConfidence, which is what any
// duration over an unmeasured rate is worth.
//
// It is 250 MB/s, stated in the unit every other rate in a decision is stated
// in. One unit is what lets one arithmetic price every stage of a launch and one
// record hold every rate a candidate was priced at, and a record that mixed the
// two would put 250 and 2000 side by side for the same speed.
const AssumedUnpackMbps = 2000.0

// LinkSpeed is how fast content moves onto a host, how much a duration derived
// from that number is worth, and where the number came from. The three travel
// together because they are one answer: a speed nothing stands behind cannot
// produce a confident duration however precise the arithmetic on it looks, and a
// speed whose provenance is lost cannot be told from one somebody measured.
//
// Exactly one of Measurement and Assumption is set. Both empty is a rate nothing
// stated, which is what a scope nobody declared an assumption for produces, and
// it prices nothing: a caller reaching for it is asking about a transfer this
// model cannot price.
type LinkSpeed struct {
	Mbps       float64
	Confidence float64
	// Measurement names the published fact this speed is, in the words of
	// whoever published it.
	Measurement string
	// Assumption names the stated constant this speed is when nothing measured
	// the path.
	Assumption string
}

// DownloadRate is this host's pessimistic (p10) throughput over one kind of
// path. A published fact is only worth what its publisher said it was worth, and
// only while it is still valid as of the moment this offer was observed: a fact
// carries its own confidence and its own expiry, and reading the mere existence
// of one as a measurement is how an unmeasured constant becomes a certainty.
// Absent an answer the reply is the standing assumption for that path, saying so.
//
// It is asked per scope rather than once, because an image crosses a link to a
// registry and a dataset crosses a link to an object store, and one number for
// both cannot tell a machine beside the data from a machine beside the images.
func (offer OfferSnapshot) DownloadRate(scope NetworkScope) LinkSpeed {
	if fact, answered := offer.Network.DownloadP10(scope, offer.ObservedAt); answered {
		return LinkSpeed{Mbps: fact.ValueMbps, Confidence: fact.Confidence, Measurement: fact.Source}
	}
	return AssumedDownloadRate(scope)
}

// AssumedDownloadRate is what Mercator falls back to over a path nobody
// measured, named so a decision says it was an assumption. A scope with no
// stated assumption answers with nothing at all rather than a zero somebody
// could divide by: this model has no opinion about how fast content crosses a
// path it has never described.
func AssumedDownloadRate(scope NetworkScope) LinkSpeed {
	switch scope {
	case NetworkScopeRegistry:
		return LinkSpeed{Mbps: DefaultRegistryDownloadMbps, Confidence: AssumedLinkConfidence, Assumption: AssumptionRegistryRate}
	case NetworkScopeObjectStore:
		return LinkSpeed{Mbps: DefaultObjectStoreDownloadMbps, Confidence: AssumedLinkConfidence, Assumption: AssumptionObjectStoreRate}
	default:
		return LinkSpeed{}
	}
}

// UnpackRate is how fast content already on the disk becomes a runnable layer
// chain. Nothing measures a host's storage today, so it is the stated assumption
// and says so. It is a rate of its own rather than the download rate reused,
// because assembling bytes that are already here is different work over a
// different resource: a machine on a slow link with fast disks is a real
// machine, and one rate for both could not describe it.
func UnpackRate() LinkSpeed {
	return LinkSpeed{Mbps: AssumedUnpackMbps, Confidence: AssumedLinkConfidence, Assumption: AssumptionUnpackRate}
}

// TransferSeconds is how long these bytes take to cross this path, for the
// readers that hold a rate and have to price a transfer over it. The Lab's
// reference model keeps its own arithmetic on purpose, so that two models
// disagreeing about a duration is a disagreement about the models.
//
// Nothing to move is nothing to wait for. A rate nobody stated is not, and is
// deliberately not floored to zero: a stage that reads as free is a stage a
// reader will believe, where an infinity is a placement nobody can record.
func (speed LinkSpeed) TransferSeconds(bytes int64) float64 {
	if bytes <= 0 {
		return 0
	}
	return float64(bytes*8) / 1_000_000 / speed.Mbps
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

// DownloadP10 is the one rule for which published fact answers a question about
// how fast content reaches this host over one link. A fact answers when it
// describes that link, states the pessimistic quantile every reader here asks
// for, names a speed, and is one its own publisher stands behind as of the moment
// it is read. Everything else is silence, and every reader of a fact asks this
// rather than deciding for itself what counts.
func (facts NetworkFacts) DownloadP10(scope NetworkScope, at time.Time) (NetworkFact, bool) {
	for _, fact := range facts.Download {
		if fact.Scope != scope || fact.Statistic != "p10" || fact.ValueMbps <= 0 {
			continue
		}
		if !fact.Answers(at) {
			continue
		}
		return fact, true
	}
	return NetworkFact{}, false
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

// Answers reports whether Mercator may act on this fact as of the moment it is
// read. Two things stop it, and both are the publisher's own statement about
// their own measurement.
//
// A publisher that puts no confidence in a number has not measured anything, and
// a fact stated to be worth nothing is not a cheaper answer than no fact at all.
// Acting on it computed a duration over a speed nobody stands behind and then
// charged no doubt for it, because a confidence of zero records no doubt: a host
// publishing 5 Gbps it disowned outranked the host that published 750 Mbps and
// stood behind it, and satisfied a Run's hard p10 bound on the way past. A
// confidence outside the unit interval is the same kind of statement and is not
// an answer either.
//
// An expired fact is no answer for the reason it stopped being one: it describes
// a link as it used to be.
func (fact NetworkFact) Answers(at time.Time) bool {
	if fact.Confidence <= 0 || fact.Confidence > 1 {
		return false
	}
	return fact.ValidUntil.IsZero() || fact.ValidUntil.After(at)
}

type PriceModel struct {
	Currency             string  `json:"currency"`
	SetupFeeUSD          float64 `json:"setup_fee_usd"`
	RatePerSecondUSD     float64 `json:"rate_per_second_usd"`
	MinimumChargeSeconds int64   `json:"minimum_charge_seconds"`
	GranularitySeconds   int64   `json:"granularity_seconds"`
	Known                bool    `json:"known"`
}

// CostUnpriced is the source a cost estimate names when nobody quoted a price for
// the machine. It is a stated absence and not a prediction of nothing: there are
// no dollars to compare, which is what CandidateDecision.Priced reads and what
// keeps a machine Mercator would actually pay for from being scored as free.
const CostUnpriced = "unpriced"

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
// fetch from a registry, and bytes that have to be unpacked into a layer chain a
// container can be started on. The two are separate because they are different
// work over different resources, and because a host that already paid the
// network is not cold however much local assembly is left.
//
// Bytes counted in both are the normal case rather than double counting. A layer
// that has to be fetched has to be applied afterwards, and both simulated worlds
// spend the assembly of anything a launch fetched. Charging a transfer alone said
// a machine fetching eighteen gigabytes owed no assembly at all, and said it at
// full confidence, so the stage the launch was about to spend most of its time in
// was structurally missing from the number a start bound is enforced against.
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
		whole := manifest.compressedBytes()
		return ImageWork{TransferBytes: whole, UnpackBytes: whole}, LocalityUnknown
	}
	// A host that says it pulled this image and cannot run it holds the bytes
	// of every layer it did not enumerate as unpacked: what it owes on those is
	// assembly, not a transfer. Charging it the network again would price a
	// machine sitting on 18GB exactly like one that has never seen the image.
	//
	// A layer this host has neither pulled nor unpacked owes both, because bytes
	// that arrive still have to be applied before a container can be started on
	// them. That is what makes the fetch and the assembly separate stages rather
	// than two names for the same answer: the same layer set can owe one, the
	// other, or both.
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
			work.UnpackBytes += layer.CompressedBytes
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

// ResidentBytes is how much of this image is already on a host that still owes
// this much work for it: everything the manifest names, less what would have to
// be fetched. Bytes here and not yet unpacked count, because they are taking up
// the disk either way, and content nobody could enumerate does not, because
// nothing said it is here.
func (manifest ImageManifest) ResidentBytes(work ImageWork) int64 {
	return manifest.compressedBytes() - work.TransferBytes
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

// StatedRate is one share of a machine's history somebody measured, and how much
// the publisher of that measurement stands behind it. The confidence is what says
// the measurement happened at all: a rate nobody stands behind is silence, which
// is the reading a disowned network measurement already gets here. A rate of zero
// at a confidence somebody stated is a machine measured and never seen to fail; a
// zero with no confidence beside it is a machine nobody measured.
type StatedRate struct {
	Rate       float64 `json:"rate"`
	Confidence float64 `json:"confidence"`
}

// Stated reports whether anybody published this rate.
func (rate StatedRate) Stated() bool {
	return rate.Confidence > 0
}

// ReliabilityEvidence is the risk history a machine's publisher states about it:
// how often it refuses to start the work it is given, and how often it drops the
// work it is already running.
//
// Each rate stands on its own measurement, because a publisher measures what it
// measures. Vast states an uptime score and says nothing about refused starts,
// and while the two rates shared one confidence, every Vast candidate's decision
// record carried a start failure rate of zero asserted at full confidence, which
// is a claim its publisher never made. An unmeasured rate is absent here, because
// absence is the only honest reading of a measurement nobody took.
type ReliabilityEvidence struct {
	StartFailures StatedRate `json:"start_failures,omitzero"`
	Interruptions StatedRate `json:"interruptions,omitzero"`
}

// Measured reports whether anybody has published anything about how this machine
// behaves.
func (evidence ReliabilityEvidence) Measured() bool {
	return evidence.StartFailures.Stated() || evidence.Interruptions.Stated()
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
	ID                     string          `json:"id"`
	RunID                  string          `json:"run_id,omitempty"`
	WorkloadRevisionDigest string          `json:"workload_revision_digest"`
	EvaluatedAt            time.Time       `json:"evaluated_at"`
	ModelVersion           string          `json:"model_version"`
	Policy                 PlacementPolicy `json:"policy"`
	// Weights is the exchange rate every candidate below was scored at, which the
	// Run's ServiceClass declared. It is recorded rather than left to be looked up,
	// because a rate that changes would silently rewrite the arithmetic of every
	// decision already taken: with it here, a reader re-derives ScoreUSD from the
	// record instead of trusting it.
	Weights                 ScoreWeights        `json:"weights"`
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
	// CacheEvidence is what this candidate was found holding of the mutable
	// caches the Run declared, one entry per name. It is recorded and never
	// priced: what a warm cache saves is work inside the application, and no
	// term of this model has measured that. It is here so the record says what
	// each candidate held, and so a reader can tell a machine that never did
	// this work from one holding the generation before the one now asked for.
	CacheEvidence []CacheEvidence `json:"cache_evidence,omitempty"`
	// Disk is what this Run asked of this candidate's room and what the machine
	// had left. It is the one answer in the record that can refuse a candidate
	// outright, so it is stated rather than left to be inferred from a violation:
	// a Run that landed nowhere has to be explainable, and a reader who could see
	// only the seconds could not tell a machine that was passed over from one
	// with nowhere to put the work.
	Disk DiskDemand `json:"disk,omitzero"`
	// Reliability is the risk history this candidate's publisher stated: how often
	// this machine refuses to start, and how often it drops what it is running. It
	// is recorded and never priced, and it is never doubted either. What a refusal
	// costs is a probability times a predicted start, nothing here predicts either
	// yet, and a flat penalty invented for it would be an exchange rate this model
	// made up.
	//
	// It is recorded for the reason the cache warmth above is: this is the account
	// of what was known when the placement was taken, and a fact no record carries
	// is one the slice that prices it cannot be held to. Charging the confidence
	// beside it through Confidences was tried and was worse than not pricing the
	// history at all, because a doubt about an answer the score never reads is a
	// charge for having answered; see Uncertainty.
	Reliability ReliabilityEvidence `json:"reliability,omitzero"`
	// RentalSchedule is the Broker state this candidate was weighed against,
	// recorded only for a Rental that has Bookings on it. The queue seconds
	// beside it are the projection; this is what the projection was read from,
	// which is what keeps a wait auditable after the schedule has moved on. A
	// Rental nothing is assigned to records none, because an empty schedule
	// offered as evidence is a queue nobody has.
	RentalSchedule *ScheduleEvidence  `json:"rental_schedule,omitempty"`
	Estimates      CandidateEstimates `json:"estimates"`
	// TransferRates is the throughput each transfer stage of this launch was
	// priced at and where that number came from, one entry per stage that had
	// bytes to move. A stage with nothing to move records none: there is no
	// transfer to have priced, and an entry for it would be a rate the decision
	// never used.
	//
	// It is recorded for the reason the confidences beside it are. The seconds a
	// candidate is charged are bytes divided by a rate, the bytes are already
	// explained by the locality evidence above, and without this the rate is the
	// one half of the answer no reader could retrace. A machine priced at a
	// throughput nothing on it ever reported and a machine priced at Mercator's
	// own assumption produce the same seconds and are different claims, and
	// safety.transfer_rate_is_attributed is stated over exactly this.
	TransferRates []TransferRate `json:"transfer_rates,omitempty"`
	// Confidences is what each answer this candidate was scored on is worth, one
	// entry per source that stated one. It is the whole input to the uncertainty
	// term, recorded so the score can be re-derived from the record: a term whose
	// input is not here is a term no reader can check and no rule can police,
	// which is how two definitions of uncertainty came to disagree about every
	// borrowed host while both were multiplied by zero.
	Confidences []Confidence `json:"confidences,omitempty"`
	// ScoreUSD is the dollars this candidate is known to be worth to this Run,
	// lowest first, and it is never the whole ranking on its own. A candidate
	// nobody quoted has no price to put in it, so its score states only the
	// waiting and the doubt: read as a total it makes the unquoted machine the
	// cheapest thing in the fleet, which is the inverse of how it is ranked.
	// CandidateDecision.Preferred is the order, it asks Priced before it compares
	// any dollars, and the decision names that rule in its selection reasons.
	ScoreUSD float64 `json:"score_usd,omitempty"`
}

// Confidence is one answer a placement rested on and what its source said it was
// worth. Zero is not recorded: an answer nobody stated a confidence for states no
// opinion, which is different from an answer stated to be worthless.
type Confidence struct {
	// Answer names what was being answered, in the vocabulary of the thing that
	// answered: the capacity claim, the image transfer, the Artifact read. Only an
	// answer the score itself reads belongs here, because this list is what the
	// uncertainty term charges for.
	Answer string  `json:"answer"`
	Value  float64 `json:"value"`
}

// TransferRate is the throughput one stage of a launch was priced at, named by
// the stage and by the path it crosses. It is the provenance of a duration
// rather than the duration: what it exists to hold is that a rate somebody
// measured and a rate Mercator assumed are different claims about the same
// arithmetic, and that a decision must say which of the two it made.
//
// Scope is the path the rate describes, and is empty for a rate that crosses no
// path: assembling content already on the disk is priced from a storage rate, and
// naming a link for it would invite a reader to check it against a measurement of
// something else.
type TransferRate struct {
	Stage LaunchStage  `json:"stage"`
	Scope NetworkScope `json:"scope,omitempty"`
	Mbps  float64      `json:"mbps"`
	// Bytes is what had to move at that rate. It is here so the seconds the
	// stage records can be retraced from the record alone rather than from the
	// inventory arithmetic that produced them.
	Bytes      int64   `json:"bytes"`
	Confidence float64 `json:"confidence"`
	// Measurement names the published fact this rate came from, in the words of
	// whoever published it. Empty when nothing measured this path.
	Measurement string `json:"measurement,omitempty"`
	// Assumption names the stated constant this rate is when nothing measured
	// the path. Empty when something did.
	Assumption string `json:"assumption,omitempty"`
}

// Attributed reports whether this rate says where it came from. A rate that
// names both a measurement and an assumption is as unattributed as one that
// names neither: it describes a decision that cannot have been taken twice.
func (rate TransferRate) Attributed() bool {
	return (rate.Measurement == "") != (rate.Assumption == "")
}

// TransferRateFor is the record of one stage priced at one rate over these
// bytes. The stage's estimate and this entry come from the same LinkSpeed, which
// is what stops a decision from recording a rate it did not divide by.
func TransferRateFor(stage LaunchStage, scope NetworkScope, bytes int64, speed LinkSpeed) TransferRate {
	return TransferRate{
		Stage:       stage,
		Scope:       scope,
		Mbps:        speed.Mbps,
		Bytes:       bytes,
		Confidence:  speed.Confidence,
		Measurement: speed.Measurement,
		Assumption:  speed.Assumption,
	}
}

// ScheduleEvidence is one Rental Schedule as a placement decision read it: the
// version that answered, the Booking holding the Rental, the Bookings already
// waiting in front of this Run, and the wait that projects from them. A schedule
// moves, so the wait a Run was priced was read from one version of it at one
// moment, and a decision that recorded only the seconds leaves nobody able to
// retrace them.
type ScheduleEvidence struct {
	Version   uint64                   `json:"version"`
	Running   *RunningBookingEvidence  `json:"running,omitempty"`
	Preceding []WaitingBookingEvidence `json:"preceding,omitempty"`
	// ProjectedStartSeconds is how long work arriving now waits for this Rental,
	// projected from where the Bookings above actually are.
	ProjectedStartSeconds float64 `json:"projected_start_seconds"`
}

// RunningBookingEvidence is the Booking holding the Rental. Its runtimes are
// what it has left rather than what its Run declared, because a Booking
// twenty-nine minutes into half an hour is one minute of waiting.
type RunningBookingEvidence struct {
	BookingID                       string  `json:"booking_id"`
	RunID                           string  `json:"run_id"`
	RemainingMaxRuntimeSeconds      float64 `json:"remaining_max_runtime_seconds"`
	RemainingExpectedRuntimeSeconds float64 `json:"remaining_expected_runtime_seconds"`
	// OverrunSeconds is how far past the runtime Mercator enforces this Booking
	// has run. It is recorded because both remainders above bottom out at zero,
	// and a record of nothing left is otherwise the same record a Rental a moment
	// from free writes: the difference is the whole reason the candidate carrying
	// it was refused.
	OverrunSeconds float64 `json:"overrun_seconds,omitzero"`
}

// WaitingBookingEvidence is a Booking that has not started. It still owes every
// second its Run declared, which is why these fields carry the declared names
// rather than the remaining ones: one field name cannot mean both.
type WaitingBookingEvidence struct {
	BookingID              string  `json:"booking_id"`
	RunID                  string  `json:"run_id"`
	MaxRuntimeSeconds      float64 `json:"max_runtime_seconds"`
	ExpectedRuntimeSeconds float64 `json:"expected_runtime_seconds"`
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

// LaunchStage is one step between a provider taking a launch and the workload
// being ready to do work. There are eight of them, and they are eight rather
// than four because each one is answered by a different authority, fails for a
// different reason, and has its own actual: a marketplace with no stock, a host
// that never came up, a machine Mercator cannot reach, a registry, a disk, an
// object store, a container runtime, and the application itself.
type LaunchStage string

const (
	// StageAcquisition is the provider allocating the machine.
	StageAcquisition LaunchStage = "acquisition"
	// StageBoot is that machine reaching a usable operating system.
	StageBoot LaunchStage = "boot"
	// StageAgentReady is Mercator's node runtime enrolling on it.
	StageAgentReady LaunchStage = "agent_ready"
	// StageImageFetch is the image bytes this host does not hold crossing the
	// link from a registry.
	StageImageFetch LaunchStage = "image_fetch"
	// StageUnpack is content already on the disk becoming a layer chain a
	// container can start on. It is a different stage from the fetch because it
	// is different work over a different resource: a host holding every byte of
	// an image it never assembled owes this and no transfer.
	StageUnpack LaunchStage = "unpack"
	// StageArtifactFetch is the Run's declared inputs being read out of the
	// object store.
	StageArtifactFetch LaunchStage = "artifact_fetch"
	// StageContainerStart is the container runtime creating the container and
	// holding a process in it.
	StageContainerStart LaunchStage = "container_start"
	// StageApplicationReady is the workload itself reporting that it can do
	// work. Only the application can state it, which is why nothing before this
	// slice keyed on it.
	StageApplicationReady LaunchStage = "application_ready"
)

// LaunchStages is the eight of them in the order a launch goes through them.
// Every reader that iterates stages reads this, so a stage cannot be added to
// the record and left out of a bundle, an invariant, or a console.
var LaunchStages = []LaunchStage{
	StageAcquisition,
	StageBoot,
	StageAgentReady,
	StageImageFetch,
	StageUnpack,
	StageArtifactFetch,
	StageContainerStart,
	StageApplicationReady,
}

// LaunchStageEstimates is what this candidate is predicted to spend on each
// stage of a launch. Every stage carries its own distribution, because a
// prediction that cannot be told apart from the stage beside it cannot be
// calibrated: the actual for a folded pair is the sum of two durations with two
// causes, and measuring either one could not replace it.
type LaunchStageEstimates struct {
	Acquisition Estimate `json:"acquisition_seconds"`
	Boot        Estimate `json:"boot_seconds"`
	AgentReady  Estimate `json:"agent_ready_seconds"`
	ImageFetch  Estimate `json:"image_fetch_seconds"`
	Unpack      Estimate `json:"unpack_seconds"`
	// ArtifactFetch is what this candidate would still spend reading the Run's
	// declared inputs out of the object store. It is separate from the image
	// fetch because it is a different transfer over different content from a
	// different authority, and folding the two together would leave a reader
	// unable to tell a machine that has to fetch an image from one that has to
	// fetch a dataset forty times its size.
	ArtifactFetch  Estimate `json:"artifact_fetch_seconds"`
	ContainerStart Estimate `json:"container_start_seconds"`
	// ApplicationReady is how long the workload says it takes to become ready
	// once its process is running. It is predicted from the declaration and
	// never from a prior of Mercator's: readiness is the application's own
	// semantics, so a Run that declared nothing is predicted nothing rather than
	// charged a number this model made up.
	ApplicationReady Estimate `json:"application_ready_seconds"`
}

// Stage is one stage's estimate by name, which is what a reader iterating
// LaunchStages needs and what keeps the eight from being enumerated twice.
func (stages LaunchStageEstimates) Stage(stage LaunchStage) Estimate {
	switch stage {
	case StageAcquisition:
		return stages.Acquisition
	case StageBoot:
		return stages.Boot
	case StageAgentReady:
		return stages.AgentReady
	case StageImageFetch:
		return stages.ImageFetch
	case StageUnpack:
		return stages.Unpack
	case StageArtifactFetch:
		return stages.ArtifactFetch
	case StageContainerStart:
		return stages.ContainerStart
	case StageApplicationReady:
		return stages.ApplicationReady
	default:
		return Estimate{}
	}
}

type CandidateEstimates struct {
	QueueSeconds Estimate `json:"queue_seconds"`
	// Stages is the waterfall this launch is predicted to go through, one
	// distribution per stage.
	Stages       LaunchStageEstimates `json:"stages"`
	StartSeconds Estimate             `json:"start_seconds"`
	// EstablishedStartSeconds is the part of that prediction somebody
	// established: provisioning as the provider published it, the wait Mercator
	// projects from Bookings it holds, and the content an inventory actually
	// answered about. What it leaves out is what content nobody could describe
	// would cost from nowhere, which is a price and never a measurement.
	//
	// Established is a claim about the bytes and the queue rather than about the
	// rates. A host that enumerated and holds no copy has established that the
	// content is not here; how long moving it takes is still Mercator's stated
	// assumption about a link nothing has measured, applied identically to every
	// candidate and carried on the estimate as its confidence. A caller that set
	// a start bound asked to be refused rather than kept waiting, so a
	// prediction over that assumption is what the bound is enforced against.
	//
	// It exists because those are the only seconds a hard start bound may
	// strike a candidate out on. Refusing a machine over content it merely
	// failed to enumerate refuses it for a guess; waiving the bound wholesale
	// whenever anything was unreadable lets a machine with fifteen minutes of
	// stated queue escape a three-minute bound because one input could not be
	// enumerated. Splitting the prediction is what lets each second be judged
	// by what it rests on.
	EstablishedStartSeconds Estimate `json:"established_start_seconds"`
	CostUSD                 Estimate `json:"cost_usd"`
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
	// StartedAt is when this Run's workload actually began, as the machine holding
	// it reported the moment. It is absent until something observed one, and it is
	// never the moment the launch was accepted: the difference between those two is
	// what a start-latency prediction is calibrated against, so a Run whose holder
	// publishes no start moment reads as a stage with no actual rather than as one
	// that took no time.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// ReadyAt is when this Run's application reported that it can do work, as the
	// application itself stated the moment. Only the workload knows: a running
	// process is not a ready one, and no provider, node, or container runtime can
	// tell the difference. It is absent until a report arrives, which is a stage
	// with no actual rather than a workload that was ready the instant it started.
	ReadyAt *time.Time `json:"ready_at,omitempty"`
	Closed  bool       `json:"closed"`
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
