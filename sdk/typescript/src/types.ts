export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonValue[] | { [key: string]: JsonValue };
export type JsonObject = { [key: string]: JsonValue };

export type FetchFunction = (input: string | URL, init?: RequestInit) => Promise<Response>;

export type HeadersMap = Record<string, string>;

export type QueryValue = string | number | boolean | null | undefined;
export type QueryParams = Record<string, QueryValue>;

export type RequestOptions = {
  body?: unknown;
  headers?: HeadersInit;
  idempotencyKey?: string;
  query?: QueryParams;
  signal?: AbortSignal;
};

export type MutationRequestOptions = RequestOptions & {
  idempotencyKey: string;
};

export type WorkspaceRequest = {
  workspaceId: string;
};

export type ErrorResponse = {
  code: string;
  message: string;
  details?: Violation[];
};

export type Violation = {
  code: string;
  path?: string;
  required?: unknown;
  offered?: unknown;
  message: string;
};

export type Platform = {
  os: string;
  architecture: string;
};

export type SecretReference = {
  name: string;
  version: number;
};

export type EnvBinding = {
  value?: string;
  secret_ref?: SecretReference;
};

export type PortExposure = "none" | "public" | "private" | string;

export type PortSpec = {
  name: string;
  container_port: number;
  protocol: string;
  exposure: PortExposure;
};

export type ContainerSpec = {
  name: string;
  image: string;
  platform: Platform;
  entrypoint?: string[];
  args?: string[];
  env?: Record<string, EnvBinding>;
  ports?: PortSpec[];
};

export type CPURequirement = {
  min_millis: number;
};

export type MemoryRequirement = {
  min_bytes: number;
};

export type DiskRequirement = {
  min_bytes: number;
};

export type AcceleratorRequirement = {
  vendor: string;
  model_any_of?: string[];
  count: number;
  memory_min_bytes: number;
};

export type ResourceRequirements = {
  cpu?: CPURequirement;
  memory?: MemoryRequirement;
  accelerators?: AcceleratorRequirement[];
  ephemeral_disk?: DiskRequirement;
};

export type InboundNetworkMode = "none" | "public_port" | string;
export type NetworkScope = "registry" | "public_internet" | string;

export type NetworkDownloadRequirement = {
  scope: NetworkScope;
  min_p10_mbps: number;
  max_measurement_age_seconds: number;
  allow_unknown: boolean;
};

export type NetworkRequirements = {
  inbound?: InboundNetworkMode;
  download?: NetworkDownloadRequirement;
};

export type PlacementObjective = "cheapest" | "fastest_start" | "fastest_completion" | "balanced" | string;

export type PlacementPolicy = {
  objective?: PlacementObjective;
  max_p90_start_seconds?: number;
  expected_runtime_seconds?: number;
  max_expected_cost_usd?: number;
  allow_unknown_pricing?: boolean;
};

export type ExecutionPolicy = {
  max_runtime_seconds?: number;
  max_pre_start_attempts?: number;
};

export type WorkloadSpec = {
  containers?: ContainerSpec[];
  resources?: ResourceRequirements;
  network?: NetworkRequirements;
  placement?: PlacementPolicy;
  execution?: ExecutionPolicy;
  metadata?: Record<string, string>;
  raw?: Record<string, unknown>;
};

export type WorkloadRevision = {
  id?: string;
  workspace_id?: string;
  workload_id?: string;
  digest?: string;
  spec: WorkloadSpec;
};

export type CreateRunRequest = {
  workspace_id?: string;
  run_id: string;
  workload_id?: string;
  workload_revision_id?: string;
  workload?: WorkloadRevision;
};

export type CreateRunResponse = {
  run_id: string;
  duplicate?: boolean;
};

export type RunOutcome = "succeeded" | "failed" | "cancelled" | string;
export type CleanupState = "not_required" | "pending" | "confirmed" | "blocked" | string;

export type RunRecord = {
  id: string;
  workspace_id: string;
  workload_revision_id?: string;
  phase: string;
  outcome?: RunOutcome;
  cleanup: CleanupState;
  closed: boolean;
};

export type RunResponse = {
  run: RunRecord;
  links?: Record<string, string>;
};

export type RunListResponse = {
  runs: RunRecord[];
};

export type CloudEvent<TData = unknown> = {
  specversion: "1.0" | string;
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
  data: TData;
};

export type EventListResponse<TData = unknown> = {
  events: CloudEvent<TData>[];
};

export type Estimate = {
  p50?: number;
  p90?: number;
  expected?: number;
  confidence?: number;
  source?: string;
  sample_count?: number;
  model_version?: string;
};

export type CollectionReport = {
  connections_queried?: string[];
  connections_from_cache?: string[];
  excluded_connections?: string[];
};

export type CandidateEstimates = {
  queue_seconds: Estimate;
  provision_seconds: Estimate;
  pull_seconds: Estimate;
  start_seconds: Estimate;
  cost_usd: Estimate;
};

export type CandidateDecision = {
  offer_snapshot_id: string;
  connection_id?: string;
  adapter_type?: string;
  native_ref?: string;
  feasible: boolean;
  rejections?: Violation[];
  estimates: CandidateEstimates;
  score_usd?: number;
};

export type PlacementDecision = {
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
};

export type PlacementDecisionResponse = {
  decision: PlacementDecision;
};

export type PlacementPreviewRequest = {
  run_id?: string;
  workspace_id?: string;
  workload: WorkloadRevision;
};

export type CreateWorkloadRequest = {
  workspace_id: string;
  workload_id: string;
  name: string;
};

export type CreateWorkloadResponse = {
  workload_id: string;
};

export type CreateRevisionRequest = {
  revision: WorkloadRevision;
};

export type WorkloadRevisionResponse = {
  revision: WorkloadRevision;
};

export type WorkloadRevisionListResponse = {
  revisions: WorkloadRevision[];
};

export type ResolveImageRequest = {
  image: string;
  platform: string;
};

export type ResolvedImage = {
  image: string;
  digest: string;
  platform: string;
  already_pinned?: boolean;
};

export type ResolveImageResponse = {
  image: ResolvedImage;
};

export type ConnectionRecord = {
  id: string;
  workspace_id: string;
  adapter_type: string;
  authorization_schema?: Record<string, string>;
  authorized: boolean;
};

export type ConnectionListResponse = {
  connections: ConnectionRecord[];
};

export type OfferKind = "standing" | "provisionable" | string;

export type AcceleratorInventory = {
  vendor: string;
  model: string;
  count: number;
  memory_bytes: number;
};

export type ResourceInventory = {
  cpu_millis: number;
  memory_bytes: number;
  ephemeral_disk_bytes: number;
  accelerators?: AcceleratorInventory[];
};

export type CapabilityProfile = {
  offer_kinds?: OfferKind[];
  container?: Record<string, unknown>;
  lifecycle?: Record<string, unknown>;
  resources?: Record<string, unknown>;
  network?: Record<string, unknown>;
  pricing?: Record<string, unknown>;
  secrets?: Record<string, unknown>;
  observability?: Record<string, unknown>;
};

export type NetworkFact = {
  scope: NetworkScope;
  statistic: string;
  value_mbps: number;
  source: string;
  sample_count: number;
  observed_at: string;
  valid_until: string;
  confidence: number;
};

export type NetworkFacts = {
  download?: NetworkFact[];
};

export type PriceModel = {
  currency: string;
  setup_fee_usd: number;
  rate_per_second_usd: number;
  minimum_charge_seconds: number;
  granularity_seconds: number;
  known: boolean;
};

export type QueueSnapshot = {
  queued_work_seconds: number;
  active_slots: number;
};

export type ImageCacheEvidence = {
  manifest_cached: boolean;
  missing_bytes: number;
  known: boolean;
};

export type CapacityEvidence = {
  available: boolean;
  confidence: number;
};

export type ReliabilityEvidence = {
  start_failure_rate?: number;
  interruption_rate?: number;
  confidence?: number;
};

export type OfferSnapshot = {
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
};

export type OfferListResponse = {
  offers: OfferSnapshot[];
};

export type SecretMetadata = {
  secret_id: string;
  version: number;
};

export type SecretMetadataListResponse = {
  secrets: SecretMetadata[];
};

export type CreateSecretVersionRequest = {
  workspace_id: string;
  secret_id?: string;
  value: string;
};

export type CreateSecretVersionResponse = {
  secret_id: string;
  version: number;
};

export type GrantSecretRequest = {
  workspace_id: string;
  secret_id?: string;
  version: number;
  scope_type: string;
  scope_id: string;
};

export type SecretGrant = {
  id: string;
  secret_id: string;
  version: number;
  scope_type: string;
  scope_id: string;
  revoked: boolean;
};

export type SecretGrantResponse = {
  grant: SecretGrant;
};

export type SinkStatus = {
  sink_id: string;
  cursor: number;
  has_cursor: boolean;
};

export type SinkResult = {
  sink_id: string;
  delivered: number;
  last_position: number;
  failed_event_id?: string;
  replay_id?: string;
};

export type ReplaySinkRequest = {
  from_exclusive?: number;
  limit?: number;
  replay_id?: string;
};
