// TypeScript mirror of the Mercator domain ontology and API envelopes.
// Source of truth: internal/domain/types.go, internal/eventlog/eventlog.go,
// internal/connection/connection.go, internal/sinks/sinks.go,
// internal/ociresolver/ociresolver.go, internal/httpapi/{server,openapi}.go.
//
// Field names mirror the Go json tags EXACTLY (including the lowercase,
// underscore-free CloudEvent fields like `globalposition`).

// ---------------------------------------------------------------------------
// Primitives & enums
// ---------------------------------------------------------------------------

export interface Platform {
  os: string;
  architecture: string;
}

export type PortExposure = "none" | "public" | "private";

export type InboundNetworkMode = "none" | "public_port";

export type NetworkScope = "registry" | "public_internet";

export type PlacementObjective =
  | "cheapest"
  | "fastest_start"
  | "fastest_completion"
  | "balanced";

export type OfferKind = "standing" | "provisionable";

export type RunOutcome = "succeeded" | "failed" | "cancelled";

export type CleanupState = "not_required" | "pending" | "confirmed" | "blocked";

export type Disposition = "release" | "terminate";

// Run lifecycle phases. The server stores `phase` as a free string, but the
// V1 lifecycle is the closed set below; `closed` is the authoritative terminal
// signal used to gate polling.
export type RunPhase =
  | "requested"
  | "launching"
  | "running"
  | "cleaning_up"
  | "closed";

// ---------------------------------------------------------------------------
// Workload / revision / spec
// ---------------------------------------------------------------------------

// Env binding is a LITERAL value only. secret_ref is intentionally not modeled
// (ADR 0001 — Mercator does not own secrets).
export interface EnvBinding {
  value?: string;
}

export interface PortSpec {
  name: string;
  container_port: number;
  protocol: string;
  exposure: PortExposure;
}

export interface ContainerSpec {
  name: string;
  image: string;
  platform: Platform;
  entrypoint?: string[];
  args?: string[];
  env?: Record<string, EnvBinding>;
  ports?: PortSpec[];
}

export interface CPURequirement {
  min_millis: number;
}

export interface MemoryRequirement {
  min_bytes: number;
}

export interface DiskRequirement {
  min_bytes: number;
}

export interface AcceleratorRequirement {
  vendor: string;
  model_any_of?: string[];
  count: number;
  memory_min_bytes: number;
}

export interface ResourceRequirements {
  cpu: CPURequirement;
  memory: MemoryRequirement;
  accelerators?: AcceleratorRequirement[];
  ephemeral_disk: DiskRequirement;
}

export interface NetworkDownloadRequirement {
  scope: NetworkScope;
  min_p10_mbps: number;
  max_measurement_age_seconds: number;
  allow_unknown: boolean;
}

export interface NetworkRequirements {
  inbound: InboundNetworkMode;
  download?: NetworkDownloadRequirement;
}

export interface PlacementPolicy {
  objective: PlacementObjective;
  max_p90_start_seconds?: number;
  expected_runtime_seconds?: number;
  max_expected_cost_usd?: number;
  allow_unknown_pricing?: boolean;
}

export interface ExecutionPolicy {
  max_runtime_seconds: number;
  max_pre_start_attempts: number;
}

export interface WorkloadSpec {
  containers: ContainerSpec[];
  resources: ResourceRequirements;
  network: NetworkRequirements;
  placement: PlacementPolicy;
  execution: ExecutionPolicy;
  metadata?: Record<string, string>;
  // Opaque per-key raw JSON passthrough.
  raw?: Record<string, unknown>;
}

export interface WorkloadRevision {
  id: string;
  workspace_id: string;
  workload_id: string;
  digest: string;
  spec: WorkloadSpec;
}

// ---------------------------------------------------------------------------
// Offers & capability evidence
// ---------------------------------------------------------------------------

export interface AcceleratorInventory {
  vendor: string;
  model: string;
  count: number;
  memory_bytes: number;
}

export interface ResourceInventory {
  cpu_millis: number;
  memory_bytes: number;
  ephemeral_disk_bytes: number;
  accelerators?: AcceleratorInventory[];
}

export interface ContainerCapabilities {
  max_containers: number;
  supports_digest_refs: boolean;
  max_environment_bytes: number;
}

export interface LifecycleCapabilities {
  idempotent_launch: string;
  list_owned: boolean;
  provider_ttl: boolean;
  cancel_queued: boolean;
}

export interface ResourceCapabilities {
  gpu_vendors?: string[];
}

export interface NetworkCapabilities {
  inbound: InboundNetworkMode;
  protocols?: string[];
  public_ipv4: boolean;
}

export interface PricingCapabilities {
  known: boolean;
}

export interface ObservabilityCapabilities {
  logs: string;
  metrics: string;
  shell: string;
}

export interface CapabilityProfile {
  offer_kinds?: OfferKind[];
  container: ContainerCapabilities;
  lifecycle: LifecycleCapabilities;
  resources: ResourceCapabilities;
  network: NetworkCapabilities;
  pricing: PricingCapabilities;
  observability: ObservabilityCapabilities;
}

export interface NetworkFact {
  scope: NetworkScope;
  statistic: string;
  value_mbps: number;
  source: string;
  sample_count: number;
  observed_at: string;
  valid_until: string;
  confidence: number;
}

export interface NetworkFacts {
  download?: NetworkFact[];
}

export interface PriceModel {
  currency: string;
  setup_fee_usd: number;
  rate_per_second_usd: number;
  minimum_charge_seconds: number;
  granularity_seconds: number;
  known: boolean;
}

export interface QueueSnapshot {
  queued_work_seconds: number;
  active_slots: number;
}

export interface Estimate {
  p50?: number;
  p90?: number;
  expected?: number;
  confidence?: number;
  source?: string;
  sample_count?: number;
  model_version?: string;
}

export interface ImageCacheEvidence {
  manifest_cached: boolean;
  missing_bytes: number;
  known: boolean;
}

export interface CapacityEvidence {
  available: boolean;
  confidence: number;
}

export interface ReliabilityEvidence {
  start_failure_rate?: number;
  interruption_rate?: number;
  confidence?: number;
}

export interface OfferSnapshot {
  id: string;
  connection_id: string;
  adapter_type: string;
  kind: OfferKind;
  native_ref: string;
  observed_at: string;
  expires_at: string;
  platform: Platform;
  resources: ResourceInventory;
  capabilities: CapabilityProfile;
  network: NetworkFacts;
  pricing: PriceModel;
  queue?: QueueSnapshot;
  provisioning?: Estimate;
  image_cache: ImageCacheEvidence;
  capacity: CapacityEvidence;
  reliability?: ReliabilityEvidence;
}

// ---------------------------------------------------------------------------
// Placement decision
// ---------------------------------------------------------------------------

export interface Violation {
  code: string;
  path: string;
  required?: unknown;
  offered?: unknown;
  message: string;
}

export interface CandidateEstimates {
  queue_seconds: Estimate;
  provision_seconds: Estimate;
  pull_seconds: Estimate;
  start_seconds: Estimate;
  cost_usd: Estimate;
}

export interface CandidateDecision {
  offer_snapshot_id: string;
  connection_id?: string;
  adapter_type?: string;
  native_ref?: string;
  feasible: boolean;
  rejections?: Violation[];
  estimates: CandidateEstimates;
  score_usd?: number;
}

export interface CollectionReport {
  connections_queried?: string[];
  connections_from_cache?: string[];
  excluded_connections?: string[];
}

export interface PlacementDecision {
  id: string;
  run_id?: string;
  workload_revision_digest: string;
  evaluated_at: string;
  model_version: string;
  policy: PlacementPolicy;
  collection_report: CollectionReport;
  candidates: CandidateDecision[];
  selected_offer_snapshot_id?: string;
  selection_reason_codes: string[];
}

// ---------------------------------------------------------------------------
// Runs & attempts
// ---------------------------------------------------------------------------

export interface RunRecord {
  id: string;
  workspace_id: string;
  workload_revision_id: string;
  phase: string;
  outcome?: RunOutcome;
  exit_code?: number;
  cleanup: CleanupState;
  disposition?: Disposition;
  closed: boolean;
  // Audited principals: a signed-in operator email, or "bearer" for
  // machine-token calls. Absent on runs recorded without a principal.
  created_by?: string;
  cancelled_by?: string;
}

// Alias used throughout the UI; identical shape to the wire RunRecord.
export type Run = RunRecord;

export interface AttemptRecord {
  id: string;
  run_id: string;
  launch_key: string;
  ownership_token: string;
}

// ---------------------------------------------------------------------------
// CloudEvents (public run events)
// ---------------------------------------------------------------------------

// NOTE: CloudEvent json tags are deliberately lowercase and underscore-free.
export interface CloudEvent {
  specversion: string;
  id: string;
  source: string;
  type: string;
  subject: string;
  time: string;
  workspaceid: string;
  streamversion: number;
  globalposition: number;
  correlationid?: string;
  causationid?: string;
  data: unknown;
}

// ---------------------------------------------------------------------------
// Connections
// ---------------------------------------------------------------------------

export type CredentialSource = "env" | "mercator";

export interface CredentialRef {
  source: CredentialSource;
  ref: string;
}

export interface ConnectionRecord {
  id: string;
  workspace_id: string;
  adapter_type: string;
  authorization_schema?: Record<string, string>;
  authorized: boolean;
  config?: Record<string, string>;
  credential?: CredentialRef;
  // Audited principals of the create and authorize commands.
  created_by?: string;
  authorized_by?: string;
}

// ---------------------------------------------------------------------------
// Auth session (GET /auth/session)
// ---------------------------------------------------------------------------

// AuthSessionState reports whether OIDC login is configured on the server and,
// when a valid session cookie accompanied the request, who is signed in.
export interface AuthSessionState {
  enabled: boolean;
  email?: string;
}

export interface CreateConnectionRequest {
  workspace_id?: string;
  connection_id?: string;
  adapter_type: string;
  config?: Record<string, string>;
  credential?: CredentialRef;
  secret?: string;
}

export interface ConnectionResponse {
  connection: ConnectionRecord;
}

// ---------------------------------------------------------------------------
// Sinks
// ---------------------------------------------------------------------------

export interface SinkStatus {
  sink_id: string;
  cursor: number;
  has_cursor: boolean;
}

export interface SinkResult {
  sink_id: string;
  delivered: number;
  last_position: number;
  failed_event_id?: string;
  replay_id?: string;
}

export interface ReplaySinkRequest {
  from_exclusive?: number;
  limit?: number;
  replay_id?: string;
}

// ---------------------------------------------------------------------------
// Image resolution
// ---------------------------------------------------------------------------

export interface ResolvedImage {
  image: string;
  digest: string;
  platform: string;
  already_pinned?: boolean;
}

export interface ResolveImageRequest {
  image: string;
  platform: string;
}

// ---------------------------------------------------------------------------
// Request bodies
// ---------------------------------------------------------------------------

export interface CreateRunRequest {
  workspace_id?: string;
  run_id?: string;
  workload_id?: string;
  workload_revision_id?: string;
  // Full workload revision spec. Takes precedence over the image shorthand.
  workload?: WorkloadRevision;
  // Image shorthand fields.
  image?: string;
  args?: string[];
  env?: Record<string, EnvBinding>;
}

export interface CreateWorkloadRequest {
  workspace_id: string;
  workload_id: string;
  name: string;
}

export interface CreateRevisionRequest {
  revision: WorkloadRevision;
}

export interface PlacementPreviewRequest {
  run_id?: string;
  workspace_id?: string;
  workload: WorkloadRevision;
}

// ---------------------------------------------------------------------------
// Response envelopes
// ---------------------------------------------------------------------------

export interface RunResponse {
  run_id: string;
  run: RunRecord;
  metadata?: Record<string, unknown>;
  links?: Record<string, string>;
  duplicate?: boolean;
}

export interface RunListResponse {
  runs: RunRecord[];
}

export interface EventListResponse {
  events: CloudEvent[];
}

export interface PlacementDecisionResponse {
  decision: PlacementDecision;
}

export interface PlacementPreviewResponse {
  decision: PlacementDecision;
}

export interface OfferListResponse {
  offers: OfferSnapshot[];
}

export interface ConnectionListResponse {
  connections: ConnectionRecord[];
}

export interface WorkloadRevisionResponse {
  revision: WorkloadRevision;
}

export interface WorkloadRevisionListResponse {
  revisions: WorkloadRevision[];
}

export interface ResolveImageResponse {
  image: ResolvedImage;
}

export interface CreateWorkloadResponse {
  workload_id: string;
}

// ---------------------------------------------------------------------------
// Error envelope
// ---------------------------------------------------------------------------

export interface ErrorEnvelope {
  code: string;
  message: string;
  details?: Violation[];
}
