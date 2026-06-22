package domain

import (
	"encoding/json"
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

type WorkloadRevision struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspace_id"`
	WorkloadID  string       `json:"workload_id"`
	Digest      string       `json:"digest"`
	Spec        WorkloadSpec `json:"spec"`
}

type WorkloadSpec struct {
	Containers []ContainerSpec            `json:"containers"`
	Resources  ResourceRequirements       `json:"resources"`
	Network    NetworkRequirements        `json:"network"`
	Placement  PlacementPolicy            `json:"placement"`
	Execution  ExecutionPolicy            `json:"execution"`
	Metadata   map[string]string          `json:"metadata,omitempty"`
	Raw        map[string]json.RawMessage `json:"raw,omitempty"`
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
	Value     *string          `json:"value,omitempty"`
	SecretRef *SecretReference `json:"secret_ref,omitempty"`
}

type SecretReference struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
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

type OfferSnapshot struct {
	ID           string              `json:"id"`
	ConnectionID string              `json:"connection_id"`
	AdapterType  string              `json:"adapter_type"`
	Kind         OfferKind           `json:"kind"`
	NativeRef    string              `json:"native_ref"`
	ObservedAt   time.Time           `json:"observed_at"`
	ExpiresAt    time.Time           `json:"expires_at"`
	Platform     Platform            `json:"platform"`
	Resources    ResourceInventory   `json:"resources"`
	Capabilities CapabilityProfile   `json:"capabilities"`
	Network      NetworkFacts        `json:"network"`
	Pricing      PriceModel          `json:"pricing"`
	Queue        *QueueSnapshot      `json:"queue,omitempty"`
	Provisioning *Estimate           `json:"provisioning,omitempty"`
	ImageCache   ImageCacheEvidence  `json:"image_cache"`
	Capacity     CapacityEvidence    `json:"capacity"`
	Reliability  ReliabilityEvidence `json:"reliability,omitempty"`
}

type ResourceInventory struct {
	CPUMillis          int64                  `json:"cpu_millis"`
	MemoryBytes        int64                  `json:"memory_bytes"`
	EphemeralDiskBytes int64                  `json:"ephemeral_disk_bytes"`
	Accelerators       []AcceleratorInventory `json:"accelerators,omitempty"`
}

type AcceleratorInventory struct {
	Vendor      string `json:"vendor"`
	Model       string `json:"model"`
	Count       int    `json:"count"`
	MemoryBytes int64  `json:"memory_bytes"`
}

type CapabilityProfile struct {
	OfferKinds    []OfferKind                `json:"offer_kinds,omitempty"`
	Container     ContainerCapabilities      `json:"container"`
	Lifecycle     LifecycleCapabilities      `json:"lifecycle"`
	Resources     ResourceCapabilities       `json:"resources"`
	Network       NetworkCapabilities        `json:"network"`
	Pricing       PricingCapabilities        `json:"pricing"`
	Secrets       SecretDeliveryCapabilities `json:"secrets"`
	Observability ObservabilityCapabilities  `json:"observability"`
}

type ContainerCapabilities struct {
	MaxContainers       int  `json:"max_containers"`
	SupportsDigestRefs  bool `json:"supports_digest_refs"`
	MaxEnvironmentBytes int  `json:"max_environment_bytes"`
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

type SecretDeliveryCapabilities struct {
	Delivery               string `json:"delivery"`
	ProviderPersistsValues bool   `json:"provider_persists_values"`
	CleanupSupported       bool   `json:"cleanup_supported"`
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

type ImageCacheEvidence struct {
	ManifestCached bool  `json:"manifest_cached"`
	MissingBytes   int64 `json:"missing_bytes"`
	Known          bool  `json:"known"`
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

type PlacementDecision struct {
	ID                      string              `json:"id"`
	RunID                   string              `json:"run_id,omitempty"`
	WorkloadRevisionDigest  string              `json:"workload_revision_digest"`
	EvaluatedAt             time.Time           `json:"evaluated_at"`
	ModelVersion            string              `json:"model_version"`
	Policy                  PlacementPolicy     `json:"policy"`
	CollectionReport        CollectionReport    `json:"collection_report"`
	Candidates              []CandidateDecision `json:"candidates"`
	SelectedOfferSnapshotID string              `json:"selected_offer_snapshot_id,omitempty"`
	SelectionReasonCodes    []string            `json:"selection_reason_codes"`
}

type CollectionReport struct {
	ConnectionsQueried   []string `json:"connections_queried,omitempty"`
	ConnectionsFromCache []string `json:"connections_from_cache,omitempty"`
	ExcludedConnections  []string `json:"excluded_connections,omitempty"`
}

type CandidateDecision struct {
	OfferSnapshotID string             `json:"offer_snapshot_id"`
	ConnectionID    string             `json:"connection_id,omitempty"`
	AdapterType     string             `json:"adapter_type,omitempty"`
	NativeRef       string             `json:"native_ref,omitempty"`
	Feasible        bool               `json:"feasible"`
	Rejections      []Violation        `json:"rejections,omitempty"`
	Estimates       CandidateEstimates `json:"estimates"`
	ScoreUSD        float64            `json:"score_usd,omitempty"`
}

type CandidateEstimates struct {
	QueueSeconds     Estimate `json:"queue_seconds"`
	ProvisionSeconds Estimate `json:"provision_seconds"`
	PullSeconds      Estimate `json:"pull_seconds"`
	StartSeconds     Estimate `json:"start_seconds"`
	CostUSD          Estimate `json:"cost_usd"`
}

type RunOutcome string

const (
	RunOutcomeSucceeded RunOutcome = "succeeded"
	RunOutcomeFailed    RunOutcome = "failed"
	RunOutcomeCancelled RunOutcome = "cancelled"
)

type CleanupState string

const (
	CleanupNotRequired CleanupState = "not_required"
	CleanupPending     CleanupState = "pending"
	CleanupConfirmed   CleanupState = "confirmed"
	CleanupBlocked     CleanupState = "blocked"
)

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

// DispositionForOfferKind maps the natural source of disposition — the selected
// offer's Kind — to the cleanup disposition. A provisionable offer means we
// provisioned (and own) the host, so cleanup must terminate it; a standing
// offer means we borrowed a slot in a pool we don't own, so cleanup only
// releases our job. Any unknown/empty kind defaults to release, the safe option
// that NEVER destroys a host.
func DispositionForOfferKind(kind OfferKind) Disposition {
	if kind == OfferKindProvisionable {
		return DispositionTerminate
	}
	return DispositionRelease
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
	Disposition Disposition `json:"disposition,omitempty"`
	Closed      bool        `json:"closed"`
}

type AttemptRecord struct {
	ID             string `json:"id"`
	RunID          string `json:"run_id"`
	LaunchKey      string `json:"launch_key"`
	OwnershipToken string `json:"ownership_token"`
}
