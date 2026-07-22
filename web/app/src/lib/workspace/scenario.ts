import scenario from "../../../../../internal/scenario/scenarios/full-schedule-forces-fresh-capacity.json";

import type {
  BookingDecision,
  CloudEvent,
  Estimate,
  OfferSnapshot,
  WorkloadRevision,
} from "@/lib/api/types";

import type { WorkspaceMessage } from "./reducer";

interface ScenarioBooking {
  booking: string;
  run: string;
  max_runtime?: string;
  expected_runtime?: string;
  remaining_max_runtime?: string;
}

const GIB = 1024 ** 3;

export function fullScheduleScenarioMessages(
  workspaceID: string,
  now = new Date(),
): WorkspaceMessage[] {
  const messages: WorkspaceMessage[] = [];
  let position = 0;
  const append = (
    runID: string,
    type: string,
    data: unknown,
    time = now,
  ) => {
    position += 1;
    messages.push({
      type: "domain_event",
      event: cloudEvent(workspaceID, runID, type, data, time, position),
    });
  };
  const schedule = scenario.world.rental_schedules[0];
  if (!schedule) throw new Error("full schedule scenario requires a RentalSchedule");
  const running = schedule.running;
  const runningMax = parseDuration(running.remaining_max_runtime);
  append(
    running.run,
    "compute.run.requested.v1",
    {
      run_id: running.run,
      workload_revision: workload(
        workspaceID,
        running.run,
        runningMax,
        runningMax,
      ),
    },
    new Date(now.getTime() - 30_000),
  );
  append(
    running.run,
    "compute.run.booking_decided.v1",
    {
      decision: decision(
        running.run,
        "rental-warm",
        running.booking,
        "rental-warm",
        "running",
        schedule.version,
      ),
    },
    new Date(now.getTime() - 29_000),
  );

  let projectedStart = runningMax;
  let predecessor = running.booking;
  for (const queued of schedule.queued as ScenarioBooking[]) {
    const expected = parseDuration(queued.expected_runtime ?? queued.max_runtime);
    const max = parseDuration(queued.max_runtime);
    append(queued.run, "compute.run.requested.v1", {
      run_id: queued.run,
      workload_revision: workload(workspaceID, queued.run, expected, max),
    });
    append(queued.run, "compute.run.booking_decided.v1", {
      decision: decision(
        queued.run,
        "rental-warm",
        queued.booking,
        "rental-warm",
        "queued",
        schedule.version,
        predecessor,
        new Date(now.getTime() + projectedStart * 1000).toISOString(),
      ),
    });
    projectedStart += expected;
    predecessor = queued.booking;
  }

  messages.push({
    type: "offers_replaced",
    catalog: {
      workspace_id: workspaceID,
      revision: "scenario-full-schedule",
      observed_at: now.toISOString(),
      offers: [standingOffer(now), marketplaceOffer(now)],
      failures: [],
    },
  });
  messages.push({ type: "ready", throughGlobalPosition: position });

  const freshRunID = "run-fifth";
  append(freshRunID, "compute.run.requested.v1", {
    run_id: freshRunID,
    workload_revision: workload(
      workspaceID,
      freshRunID,
      parseDuration(scenario.request.expected_runtime),
      parseDuration(scenario.request.max_runtime),
    ),
  });
  append(freshRunID, "compute.run.booking_decided.v1", {
    decision: decision(
      freshRunID,
      scenario.expect.offer,
      "booking-fifth",
      "rental-fresh",
      "running",
      1,
    ),
  });
  append(freshRunID, "compute.run.launch_intent_recorded.v1", {
    disposition: "terminate",
  });
  return messages;
}

function cloudEvent(
  workspaceID: string,
  runID: string,
  type: string,
  data: unknown,
  time: Date,
  position: number,
): CloudEvent {
  return {
    specversion: "1.0",
    id: `scenario-${position}`,
    source: `compute-control-plane/workspaces/${workspaceID}`,
    type,
    subject: `runs/${runID}`,
    time: time.toISOString(),
    workspaceid: workspaceID,
    streamversion: position,
    globalposition: position,
    correlationid: runID,
    data,
  };
}

function workload(
  workspaceID: string,
  runID: string,
  expectedRuntimeSeconds: number,
  maxRuntimeSeconds: number,
): WorkloadRevision {
  return {
    id: `wrev-${runID}`,
    workspace_id: workspaceID,
    workload_id: "worker",
    digest: `sha256:${runID.padEnd(64, "0").slice(0, 64)}`,
    spec: {
      containers: [
        {
          name: "worker",
          image: "worker:v1",
          platform: { os: "linux", architecture: "amd64" },
        },
      ],
      resources: {
        cpu: { min_millis: 4_000 },
        memory: { min_bytes: 16 * GIB },
        ephemeral_disk: { min_bytes: 40 * GIB },
      },
      network: { inbound: "none" },
      placement: {
        objective: "balanced",
        expected_runtime_seconds: expectedRuntimeSeconds,
      },
      execution: {
        max_runtime_seconds: maxRuntimeSeconds,
        max_pre_start_attempts: 3,
      },
    },
  };
}

function decision(
  runID: string,
  offerID: string,
  bookingID: string,
  rentalID: string,
  state: "running" | "queued",
  scheduleVersion: number,
  afterBookingID?: string,
  projectedStartAt?: string,
): BookingDecision {
  return {
    id: `dec-${runID}`,
    run_id: runID,
    workload_revision_digest: `sha256:${runID.padEnd(64, "0").slice(0, 64)}`,
    evaluated_at: new Date().toISOString(),
    model_version: "scenario",
    policy: { objective: "balanced" },
    collection_report: {},
    candidates: [
      {
        offer_snapshot_id: offerID,
        feasible: true,
        estimates: {
          queue_seconds: estimate(0),
          provision_seconds: estimate(
            offerID === "fresh-slow" ? parseDuration("30m") : 0,
          ),
          pull_seconds: estimate(0),
          start_seconds: estimate(
            offerID === "fresh-slow" ? parseDuration("30m") : 0,
          ),
          cost_usd: estimate(0),
        },
      },
    ],
    selected_offer_snapshot_id: offerID,
    booking: {
      id: bookingID,
      rental_id: rentalID,
      state,
      after_booking_id: afterBookingID,
      projected_start_at: projectedStartAt,
      schedule_version: scheduleVersion,
    },
    selection_reason_codes: ["FEASIBLE", "LOWEST_SCORE"],
  };
}

function estimate(expected: number): Estimate {
  return { expected, p50: expected, p90: expected, source: "scenario" };
}

function standingOffer(now: Date): OfferSnapshot {
  return offer(now, {
    id: "rental-warm",
    rental_id: "rental-warm",
    kind: "standing",
    connection_id: "conn-rentals",
    adapter_type: "docker",
    native_ref: "rental-warm",
    ratePerHour: scenario.world.rentals[0]?.rate_per_hour_usd ?? 0,
    provisioning: undefined,
    cached: true,
  });
}

function marketplaceOffer(now: Date): OfferSnapshot {
  const source = scenario.world.marketplace[0];
  if (!source) throw new Error("full schedule scenario requires a marketplace Offer");
  return offer(now, {
    id: source.id,
    kind: "provisionable",
    connection_id: "conn-marketplace",
    adapter_type: source.provider,
    native_ref: source.id,
    ratePerHour: source.rate_per_hour_usd,
    provisioning: {
      expected: parseDuration(source.provisioning.expected),
      p50: parseDuration(source.provisioning.expected),
      p90: parseDuration(source.provisioning.p90),
      source: "scenario",
    },
    cached: false,
  });
}

function offer(
  now: Date,
  values: {
    id: string;
    rental_id?: string;
    connection_id: string;
    adapter_type: string;
    kind: "standing" | "provisionable";
    native_ref: string;
    ratePerHour: number;
    provisioning?: Estimate;
    cached: boolean;
  },
): OfferSnapshot {
  return {
    id: values.id,
    rental_id: values.rental_id,
    connection_id: values.connection_id,
    adapter_type: values.adapter_type,
    kind: values.kind,
    native_ref: values.native_ref,
    observed_at: now.toISOString(),
    expires_at: new Date(now.getTime() + 60 * 60 * 1000).toISOString(),
    platform: { os: "linux", architecture: "amd64" },
    resources: {
      cpu_millis: 8_000,
      memory_bytes: 32 * GIB,
      ephemeral_disk_bytes: 200 * GIB,
    },
    capabilities: {
      offer_kinds: [values.kind],
      container: {
        max_containers: 1,
        supports_digest_refs: true,
        supports_entrypoint_override: true,
        max_environment_bytes: 32_768,
      },
      lifecycle: {
        idempotent_launch: "launch_key",
        list_owned: true,
        provider_ttl: true,
        cancel_queued: true,
      },
      resources: {},
      network: { inbound: "none", public_ipv4: false },
      pricing: { known: true },
      observability: { logs: "native", metrics: "none", shell: "native" },
    },
    network: {},
    pricing: {
      currency: "USD",
      setup_fee_usd: 0,
      rate_per_second_usd: values.ratePerHour / 3600,
      minimum_charge_seconds: 1,
      granularity_seconds: 1,
      known: true,
    },
    provisioning: values.provisioning,
    image_cache: {
      manifest_cached: values.cached,
      missing_bytes: values.cached ? 0 : 4 * GIB,
      known: true,
    },
    capacity: { available: true, confidence: 1 },
    reliability: { confidence: 1 },
  };
}

function parseDuration(value: string | undefined): number {
  if (!value) throw new Error("scenario duration is required");
  const match = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(value);
  if (!match) throw new Error(`unsupported scenario duration ${value}`);
  return (
    Number(match[1] ?? 0) * 3600 +
    Number(match[2] ?? 0) * 60 +
    Number(match[3] ?? 0)
  );
}
