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
const PlacementPolicy = Schema.Struct({
  objective: Schema.Literals([
    "cheapest",
    "fastest_start",
    "fastest_completion",
    "balanced",
  ]),
  max_p90_start_seconds: Schema.optionalKey(Schema.Number),
  expected_runtime_seconds: Schema.optionalKey(Schema.Number),
  max_expected_cost_usd: Schema.optionalKey(Schema.Number),
  allow_unknown_pricing: Schema.optionalKey(Schema.Boolean),
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
  image_cache: Schema.Struct({
    manifest_cached: Schema.Boolean,
    missing_bytes: Schema.Number,
    known: Schema.Boolean,
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
const CandidateEstimateSet = Schema.Struct({
  queue_seconds: Estimate,
  provision_seconds: Estimate,
  pull_seconds: Estimate,
  start_seconds: Estimate,
  cost_usd: Estimate,
});
const CandidateDisposition = Schema.Literals([
  "run_now_existing_rental",
  "queue_existing_rental",
  "provision_fresh_rental",
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

const DashboardDomainEventMessage = Schema.Struct({
  type: Schema.Literals(["domain_event"]),
  event: CloudEvent,
});
const DashboardOfferMessage = Schema.Struct({
  type: Schema.Literals(["offers_replaced"]),
  catalog: OfferCatalogReplacement,
});
const DashboardOffersUnavailableMessage = Schema.Struct({
  type: Schema.Literals(["offers_unavailable"]),
});
const DashboardReadyMessage = Schema.Struct({
  type: Schema.Literals(["ready"]),
  through_global_position: Schema.Number,
});
export const DashboardMessage = Schema.Union([
  DashboardDomainEventMessage,
  DashboardOfferMessage,
  DashboardOffersUnavailableMessage,
  DashboardReadyMessage,
]);
export const DashboardPlayback = Schema.Struct({
  status: Schema.Literals(["playing", "paused", "finished"]),
  cursor: Schema.Number,
  cue_count: Schema.Number,
  elapsed_millis: Schema.Number,
  duration_millis: Schema.Number,
  speed: Schema.Literals([1, 2, 4]),
});
export const DashboardReset = Schema.Struct({
  messages: mutableArray(DashboardMessage),
  playback: DashboardPlayback,
  fidelity: Schema.Struct({
    offer_source: Schema.String,
    proven_capabilities: mutableArray(Schema.String),
    target_capabilities: mutableArray(Schema.String),
  }),
});

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
