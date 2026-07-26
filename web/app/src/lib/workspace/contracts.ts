import * as Schema from "effect/Schema";

const mutableArray = <S extends Schema.Constraint>(schema: S) =>
  Schema.mutable(Schema.Array(schema));

const StringRecord = Schema.Record(Schema.String, Schema.String);
const UnknownRecord = Schema.Record(Schema.String, Schema.Unknown);

const Platform = Schema.Struct({
  os: Schema.String,
  architecture: Schema.String,
});

const EnvBinding = Schema.Struct({ value: Schema.optionalKey(Schema.String) });
const PortSpec = Schema.Struct({
  name: Schema.String,
  container_port: Schema.Number,
  protocol: Schema.String,
  exposure: Schema.Literals(["none", "public", "private"]),
});
const ContainerSpec = Schema.Struct({
  name: Schema.String,
  image: Schema.String,
  platform: Platform,
  entrypoint: Schema.optionalKey(mutableArray(Schema.String)),
  args: Schema.optionalKey(mutableArray(Schema.String)),
  env: Schema.optionalKey(Schema.Record(Schema.String, EnvBinding)),
  ports: Schema.optionalKey(mutableArray(PortSpec)),
});
const AcceleratorRequirement = Schema.Struct({
  vendor: Schema.String,
  model_any_of: Schema.optionalKey(mutableArray(Schema.String)),
  count: Schema.Number,
  memory_min_bytes: Schema.Number,
});
const ResourceRequirements = Schema.Struct({
  cpu: Schema.Struct({ min_millis: Schema.Number }),
  memory: Schema.Struct({ min_bytes: Schema.Number }),
  accelerators: Schema.optionalKey(mutableArray(AcceleratorRequirement)),
  ephemeral_disk: Schema.Struct({ min_bytes: Schema.Number }),
});
const NetworkRequirements = Schema.Struct({
  inbound: Schema.Literals(["none", "public_port"]),
  download: Schema.optionalKey(
    Schema.Struct({
      scope: Schema.Literals(["registry", "public_internet"]),
      min_p10_mbps: Schema.Number,
      max_measurement_age_seconds: Schema.Number,
      allow_unknown: Schema.Boolean,
    }),
  ),
});
// PlacementPolicy carries the class of work a Run is, which is the only thing
// that says what waiting is worth to it, beside the hard bounds it refuses to
// cross. The class replaced a placement objective outright: an objective named a
// quantity to minimise and never what a second of it cost, so the console shows
// the class and the rates it declares rather than a word nothing was computed
// from.
const PlacementPolicy = Schema.Struct({
  service_class: Schema.Literals([
    "interactive",
    "standard",
    "batch",
    "experimental",
    "opportunistic",
  ]),
  max_p90_start_seconds: Schema.optionalKey(Schema.Number),
  expected_runtime_seconds: Schema.optionalKey(Schema.Number),
  max_expected_cost_usd: Schema.optionalKey(Schema.Number),
  allow_unknown_pricing: Schema.optionalKey(Schema.Boolean),
});
// ArtifactRequirements is what a workload reads and publishes, by Artifact
// version. It is a dependency on durable content in the object store rather
// than on any host, which is why the console never renders it as locality.
const ArtifactRequirements = Schema.Struct({
  consumes: Schema.optionalKey(mutableArray(Schema.String)),
  produces: Schema.optionalKey(mutableArray(Schema.String)),
});
const ArtifactReplica = Schema.Struct({
  artifact_id: Schema.String,
  content_digest: Schema.String,
  size_bytes: Schema.Number,
  state: Schema.Literals(["verified", "unverified"]),
  verified_at: Schema.optionalKey(Schema.String),
});
const WorkloadRevision = Schema.Struct({
  id: Schema.String,
  workspace_id: Schema.String,
  workload_id: Schema.String,
  digest: Schema.String,
  spec: Schema.Struct({
    containers: mutableArray(ContainerSpec),
    resources: ResourceRequirements,
    network: NetworkRequirements,
    placement: PlacementPolicy,
    execution: Schema.Struct({
      max_runtime_seconds: Schema.Number,
      max_pre_start_attempts: Schema.Number,
    }),
    artifacts: ArtifactRequirements,
    metadata: Schema.optionalKey(StringRecord),
    raw: Schema.optionalKey(UnknownRecord),
  }),
});

const AcceleratorInventory = Schema.Struct({
  vendor: Schema.String,
  model: Schema.String,
  canonical_model: Schema.optionalKey(Schema.String),
  count: Schema.Number,
  memory_bytes: Schema.Number,
});
const ResourceInventory = Schema.Struct({
  cpu_millis: Schema.Number,
  memory_bytes: Schema.Number,
  ephemeral_disk_bytes: Schema.Number,
  accelerators: Schema.optionalKey(mutableArray(AcceleratorInventory)),
});
const CapabilityProfile = Schema.Struct({
  offer_kinds: Schema.optionalKey(
    mutableArray(Schema.Literals(["standing", "provisionable"])),
  ),
  container: Schema.Struct({
    max_containers: Schema.Number,
    supports_digest_refs: Schema.Boolean,
    supports_entrypoint_override: Schema.Boolean,
    max_environment_bytes: Schema.Number,
  }),
  lifecycle: Schema.Struct({
    idempotent_launch: Schema.String,
    list_owned: Schema.Boolean,
    provider_ttl: Schema.Boolean,
    cancel_queued: Schema.Boolean,
  }),
  resources: Schema.Struct({
    gpu_vendors: Schema.optionalKey(mutableArray(Schema.String)),
  }),
  network: Schema.Struct({
    inbound: Schema.Literals(["none", "public_port"]),
    protocols: Schema.optionalKey(mutableArray(Schema.String)),
    public_ipv4: Schema.Boolean,
  }),
  pricing: Schema.Struct({ known: Schema.Boolean }),
  observability: Schema.Struct({
    logs: Schema.String,
    metrics: Schema.String,
    shell: Schema.String,
  }),
});
const NetworkFact = Schema.Struct({
  scope: Schema.Literals(["registry", "public_internet"]),
  statistic: Schema.String,
  value_mbps: Schema.Number,
  source: Schema.String,
  sample_count: Schema.Number,
  observed_at: Schema.String,
  valid_until: Schema.String,
  confidence: Schema.Number,
});
const Estimate = Schema.Struct({
  p50: Schema.optionalKey(Schema.Number),
  p90: Schema.optionalKey(Schema.Number),
  expected: Schema.optionalKey(Schema.Number),
  confidence: Schema.optionalKey(Schema.Number),
  source: Schema.optionalKey(Schema.String),
  sample_count: Schema.optionalKey(Schema.Number),
  model_version: Schema.optionalKey(Schema.String),
});

export const OfferSnapshot = Schema.Struct({
  id: Schema.String,
  rental_id: Schema.optionalKey(Schema.String),
  connection_id: Schema.String,
  adapter_type: Schema.String,
  kind: Schema.Literals(["standing", "provisionable"]),
  lane: Schema.Literals(["reusable", "ephemeral"]),
  native_ref: Schema.String,
  observed_at: Schema.String,
  expires_at: Schema.String,
  platform: Platform,
  resources: ResourceInventory,
  capabilities: CapabilityProfile,
  network: Schema.Struct({
    download: Schema.optionalKey(mutableArray(NetworkFact)),
  }),
  pricing: Schema.Struct({
    currency: Schema.String,
    setup_fee_usd: Schema.Number,
    rate_per_second_usd: Schema.Number,
    minimum_charge_seconds: Schema.Number,
    granularity_seconds: Schema.Number,
    known: Schema.Boolean,
  }),
  queue: Schema.optionalKey(
    Schema.Struct({
      queued_work_seconds: Schema.Number,
      active_slots: Schema.Number,
    }),
  ),
  provisioning: Schema.optionalKey(Estimate),
  images: Schema.Struct({
    known: Schema.Boolean,
    observed_at: Schema.optionalKey(Schema.String),
    image_digests: Schema.optionalKey(mutableArray(Schema.String)),
    layer_digests: Schema.optionalKey(mutableArray(Schema.String)),
  }),
  artifacts: Schema.Struct({
    known: Schema.Boolean,
    observed_at: Schema.optionalKey(Schema.String),
    replicas: Schema.optionalKey(mutableArray(ArtifactReplica)),
  }),
  capacity: Schema.Struct({
    available: Schema.Boolean,
    confidence: Schema.Number,
  }),
  reliability: Schema.Struct({
    start_failure_rate: Schema.optionalKey(Schema.Number),
    interruption_rate: Schema.optionalKey(Schema.Number),
    confidence: Schema.optionalKey(Schema.Number),
  }),
});

const Violation = Schema.Struct({
  code: Schema.String,
  path: Schema.String,
  required: Schema.optionalKey(Schema.Unknown),
  offered: Schema.optionalKey(Schema.Unknown),
  message: Schema.String,
});
// Confidence is one answer a placement rested on and what its source said it was
// worth. The uncertainty term of a score is the shortfall summed over exactly
// these, which is what lets a reader re-derive a score rather than trust it.
const Confidence = Schema.Struct({
  answer: Schema.String,
  value: Schema.Number,
});
// ScoreWeights is the exchange rates a service class declares. Every decision
// records the ones it was scored at, because a rate that changed would otherwise
// silently rewrite the arithmetic of every decision already taken.
const ScoreWeights = Schema.Struct({
  start_latency_usd_per_second: Schema.optionalKey(Schema.Number),
  completion_latency_usd_per_second: Schema.optionalKey(Schema.Number),
  uncertainty_penalty_usd: Schema.optionalKey(Schema.Number),
});
const CandidateEstimateSet = Schema.Struct({
  queue_seconds: Estimate,
  provision_seconds: Estimate,
  pull_seconds: Estimate,
  artifact_seconds: Estimate,
  start_seconds: Estimate,
  established_start_seconds: Estimate,
  cost_usd: Estimate,
});
// CandidateDisposition is what Placement recorded a candidate as. The three
// Rental dispositions reuse, queue on, or provision capacity Mercator keeps;
// launch_ephemeral is a one-shot execution that holds nothing once its workload
// exits. Leaving it out made the common case undecodable: every candidate on a
// provider-native execution product failed this schema, which is the whole
// ephemeral lane.
const CandidateDisposition = Schema.Literals([
  "run_now_existing_rental",
  "queue_existing_rental",
  "provision_fresh_rental",
  "launch_ephemeral",
]);
export const Booking = Schema.Struct({
  id: Schema.String,
  run_id: Schema.String,
  rental_id: Schema.String,
  state: Schema.Literals(["running", "queued"]),
  after_booking_id: Schema.optionalKey(Schema.String),
  projected_start_at: Schema.optionalKey(Schema.String),
  latest_start_at: Schema.optionalKey(Schema.String),
  schedule_version: Schema.Number,
});
export const BookingDecision = Schema.Struct({
  id: Schema.String,
  run_id: Schema.optionalKey(Schema.String),
  workload_revision_digest: Schema.String,
  evaluated_at: Schema.String,
  model_version: Schema.String,
  policy: PlacementPolicy,
  weights: ScoreWeights,
  collection_report: Schema.Struct({
    connections_queried: Schema.optionalKey(mutableArray(Schema.String)),
    connections_from_cache: Schema.optionalKey(mutableArray(Schema.String)),
    excluded_connections: Schema.optionalKey(mutableArray(Schema.String)),
  }),
  candidates: mutableArray(
    Schema.Struct({
      offer_snapshot_id: Schema.String,
      connection_id: Schema.optionalKey(Schema.String),
      adapter_type: Schema.optionalKey(Schema.String),
      native_ref: Schema.optionalKey(Schema.String),
      disposition: CandidateDisposition,
      feasible: Schema.Boolean,
      rejections: Schema.optionalKey(mutableArray(Violation)),
      estimates: CandidateEstimateSet,
      confidences: Schema.optionalKey(mutableArray(Confidence)),
      score_usd: Schema.optionalKey(Schema.Number),
    }),
  ),
  selected_offer_snapshot_id: Schema.optionalKey(Schema.String),
  booking: Schema.optionalKey(Booking),
  selection_reason_codes: mutableArray(Schema.String),
});

export const CloudEvent = Schema.Struct({
  specversion: Schema.String,
  id: Schema.String,
  source: Schema.String,
  type: Schema.String,
  subject: Schema.String,
  time: Schema.String,
  workspaceid: Schema.String,
  streamversion: Schema.Number,
  globalposition: Schema.Number,
  correlationid: Schema.optionalKey(Schema.String),
  causationid: Schema.optionalKey(Schema.String),
  data: Schema.Unknown,
});

export const OfferCatalogReplacement = Schema.Struct({
  workspace_id: Schema.String,
  revision: Schema.String,
  observed_at: Schema.String,
  offers: mutableArray(OfferSnapshot),
  failures: mutableArray(Schema.Unknown),
});

export const Ready = Schema.Struct({ through_global_position: Schema.Number });

export const RequestedData = Schema.Struct({
  run_id: Schema.String,
  workload_revision: WorkloadRevision,
});
export const BookingDecidedData = Schema.Struct({ decision: BookingDecision });
export const BookingDispatchedData = Schema.Struct({ booking: Booking });
export const LaunchIntentData = Schema.Struct({ disposition: Schema.String });
export const ObservedRunData = Schema.Struct({ phase: Schema.String });
export const OutcomeData = Schema.Struct({
  outcome: Schema.optionalKey(Schema.String),
});
const RentalBooking = Schema.Struct({
  id: Schema.String,
  rental_id: Schema.String,
  after_booking_id: Schema.optionalKey(Schema.String),
  projected_start_at: Schema.optionalKey(Schema.String),
  latest_start_at: Schema.optionalKey(Schema.String),
  schedule_version: Schema.Number,
});
export const RentalBookingData = Schema.Struct({
  run_id: Schema.String,
  booking: Schema.optionalKey(RentalBooking),
  id: Schema.optionalKey(Schema.String),
  rental_id: Schema.optionalKey(Schema.String),
  after_booking_id: Schema.optionalKey(Schema.String),
  projected_start_at: Schema.optionalKey(Schema.String),
  latest_start_at: Schema.optionalKey(Schema.String),
  schedule_version: Schema.optionalKey(Schema.Number),
});
export const RentalRemovalData = Schema.Struct({
  booking_id: Schema.optionalKey(Schema.String),
  id: Schema.optionalKey(Schema.String),
});
