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
  /**
   * Workspace for this mutation. Applied to createRun's request body when the
   * body does not already carry workspace_id; overrides the client default.
   */
  workspaceId?: string;
};

export type WorkspaceRequest = {
  workspaceId?: string;
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

export type EnvBinding = {
  value: string;
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
  /**
   * Optional. When omitted the server generates a uuidv7-based run id and
   * returns it at `response.run.id`.
   */
  run_id?: string;
  workload_id?: string;
  workload_revision_id?: string;
  /**
   * Image shorthand: the only required field for the minimal create form. The
   * server synthesizes the single container and defaults everything else.
   * Ignored when a full `workload` (or `workload_revision_id`) is supplied.
   */
  image?: string;
  /** Container args for the image shorthand. */
  args?: string[];
  /** Container env bindings for the image shorthand. */
  env?: Record<string, EnvBinding>;
  /** Full workload revision. Takes precedence over the image shorthand. */
  workload?: WorkloadRevision;
};

export type RunOutcome = "succeeded" | "failed" | "cancelled" | string;
export type CleanupState = "not_required" | "pending" | "confirmed" | "blocked" | string;
export type Disposition = "release" | "terminate" | string;

export type RunRecord = {
  id: string;
  workspace_id: string;
  workload_revision_id?: string;
  phase: string;
  outcome?: RunOutcome;
  /**
   * Container exit code, surfaced directly on the run once a terminal
   * observation is recorded. `undefined` means "not yet known"; a present `0`
   * is a real success exit. Read this instead of digging through the
   * `compute.run.external_state_observed.v1` event payload.
   */
  exit_code?: number;
  cleanup: CleanupState;
  /**
   * Recorded cleanup disposition. `terminate` means the run provisioned a host
   * we own that is destroyed on cleanup; `release` means the run borrowed a slot
   * in a standing pool we do not own and cleanup removes only our job. Recorded
   * at launch time and dispatched on the recorded value, never re-inferred at
   * cleanup time. `undefined` until a launch intent is recorded.
   */
  disposition?: Disposition;
  closed: boolean;
};

export type RunResponse = {
  /**
   * Convenience top-level run identifier, equal to `run.id`. Returned on every
   * run response alongside the full run record.
   */
  run_id: string;
  run: RunRecord;
  /** Reserved for per-response metadata. */
  metadata?: Record<string, unknown>;
  links?: Record<string, string>;
  /** True when a create was a safe idempotent replay of an existing run. */
  duplicate?: boolean;
};

/**
 * createRun returns the same envelope as get/wait/cancel: a convenience
 * top-level `run_id` alongside the full `run` record.
 */
export type CreateRunResponse = RunResponse;

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
