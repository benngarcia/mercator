import { expect, test } from "vitest";

import replacementBookingEvents from "./testdata/replacement-booking-events.json";
import requestedEvent from "./testdata/requested-event.json";
import {
  createDeployment,
  reduceDeployment,
  type DeploymentMessage,
} from "./reducer";

test("degrades a run whose expected runtime exceeds the enforced maximum", () => {
  const requested = structuredClone(requestedEvent) as DeploymentMessage;
  if (requested.type !== "domain_event") {
    throw new Error("fixture needs a requested event");
  }
  const data = requested.event.data as {
    run_id: string;
    workload_revision: {
      spec: {
        placement: { expected_runtime_seconds: number };
        execution: { max_runtime_seconds: number };
      };
    };
  };
  data.workload_revision.spec.placement.expected_runtime_seconds = 121;

  const deployment = reduceDeployment(createDeployment("ws_scenario"), requested);

  const run = deployment.runs[data.run_id];
  expect(run?.phase).toBe("requested");
  expect(run?.expectedRuntimeSeconds).toBeNull();
  expect(run?.maxRuntimeSeconds).toBe(120);
});

test("keeps a rental active when a queued booking detaches while another runs", () => {
  const running = bookingDecidedMessage({
    eventID: "evt_booking_running",
    globalPosition: 2,
    runID: "run-active",
    bookingID: "booking-active",
    state: "running",
  });
  const queued = bookingDecidedMessage({
    eventID: "evt_booking_queued",
    globalPosition: 4,
    runID: "run-queued",
    bookingID: "booking-queued",
    state: "queued",
    afterBookingID: "booking-active",
  });
  const closed: DeploymentMessage = {
    type: "domain_event",
    event: {
      specversion: "1.0",
      id: "evt_closed_queued",
      source: "test",
      type: "compute.run.closed.v1",
      subject: "runs/run-queued",
      time: "2030-01-01T00:00:05Z",
      streamversion: 3,
      globalposition: 5,
      correlationid: "run-queued",
      data: { closed: true },
    },
  } as unknown as DeploymentMessage;

  const deployment = [
    requestedMessage("run-active", "evt_requested_active", 1),
    running,
    requestedMessage("run-queued", "evt_requested_queued", 3),
    queued,
    closed,
  ].reduce(reduceDeployment, createDeployment("ws_scenario"));

  const rental = deployment.rentals["rental-warm"];
  expect(rental?.phase).toBe("active");
  expect(rental?.runningBookingID).toBe("booking-active");
  expect(rental?.queuedBookingIDs).toEqual([]);
  expect(deployment.runs["run-queued"]?.phase).toBe("closed");
});

function requestedMessage(
  runID: string,
  eventID: string,
  globalPosition: number,
): DeploymentMessage {
  const requested = structuredClone(requestedEvent) as DeploymentMessage;
  if (requested.type !== "domain_event") {
    throw new Error("fixture needs a requested event");
  }
  requested.event.id = eventID;
  requested.event.subject = `runs/${runID}`;
  requested.event.correlationid = runID;
  requested.event.globalposition = globalPosition;
  (requested.event.data as { run_id: string }).run_id = runID;
  return requested;
}

// A one-shot execution on a provider-native product is the whole ephemeral
// lane, and the console could not read one: "launch_ephemeral" was missing from
// the hand-written disposition literals, so every Booking Decision that
// recorded the common case threw on decode instead of reaching the timeline.
test("reads a Booking Decision that launched a one-shot ephemeral execution", () => {
  const decided = bookingDecidedMessage({
    eventID: "evt_booking_ephemeral",
    globalPosition: 2,
    runID: "run-one-shot",
    bookingID: "booking-one-shot",
    state: "running",
    candidateDisposition: "launch_ephemeral",
  });

  const deployment = [
    requestedMessage("run-one-shot", "evt_requested_one_shot", 1),
    decided,
  ].reduce(reduceDeployment, createDeployment("ws_scenario"));

  // The decision reaching the canvas at all is the assertion. A disposition the
  // schema cannot spell throws on decode, and the reduce that throws leaves the
  // Run where it was requested with no Booking on it.
  expect(deployment.runs["run-one-shot"]?.bookingID).toBe("booking-one-shot");
  expect(deployment.runs["run-one-shot"]?.phase).toBe("running");
});

function bookingDecidedMessage(input: {
  eventID: string;
  globalPosition: number;
  runID: string;
  bookingID: string;
  state: "running" | "queued";
  afterBookingID?: string;
  candidateDisposition?: string;
}): DeploymentMessage {
  return {
    type: "domain_event",
    event: {
      specversion: "1.0",
      id: input.eventID,
      source: "test",
      type: "compute.run.booking_decided.v1",
      subject: `runs/${input.runID}`,
      time: "2030-01-01T00:00:01Z",
      streamversion: 2,
      globalposition: input.globalPosition,
      correlationid: input.runID,
      data: {
        decision: {
          id: `decision-${input.bookingID}`,
          run_id: input.runID,
          workload_revision_digest: "sha256:fixture",
          evaluated_at: "2030-01-01T00:00:01Z",
          model_version: "scheduler-v1",
          policy: { service_class: "batch" },
          weights: { completion_latency_usd_per_second: 0.0001 },
          collection_report: {},
          candidates: input.candidateDisposition
            ? [
                {
                  offer_snapshot_id: "offer-warm",
                  disposition: input.candidateDisposition,
                  feasible: true,
                  estimates: {
                    queue_seconds: {},
                    stages: {
                      acquisition_seconds: {},
                      boot_seconds: {},
                      agent_ready_seconds: {},
                      image_fetch_seconds: {},
                      unpack_seconds: {},
                      artifact_fetch_seconds: {},
                      container_start_seconds: {},
                      application_ready_seconds: {},
                    },
                    start_seconds: {},
                    established_start_seconds: {},
                    cost_usd: {},
                  },
                },
              ]
            : [],
          selected_offer_snapshot_id: "offer-warm",
          booking: {
            id: input.bookingID,
            run_id: input.runID,
            rental_id: "rental-warm",
            state: input.state,
            ...(input.afterBookingID
              ? { after_booking_id: input.afterBookingID }
              : {}),
            schedule_version: 1,
          },
          selection_reason_codes: ["FEASIBLE", "SERVICE_CLASS_BATCH"],
        },
      },
    },
  } as unknown as DeploymentMessage;
}

test("replaces a failed provider booking for the same Run", () => {
  const offers = [
    {
      id: "offer-failed-provider",
      kind: "provisionable",
    },
    {
      id: "offer-replacement-provider",
      kind: "provisionable",
    },
  ];
  const messages = replacementBookingEvents as unknown as DeploymentMessage[];
  const requested = structuredClone(requestedEvent) as DeploymentMessage;
  if (requested.type !== "domain_event") {
    throw new Error("fixture needs a requested event");
  }
  requested.event.subject = "runs/run-1";
  requested.event.correlationid = "run-1";
  (requested.event.data as { run_id: string }).run_id = "run-1";

  const result = messages.reduce(
    reduceDeployment,
    reduceDeployment(
      reduceDeployment(createDeployment("ws_scenario"), requested),
      {
        type: "offers_replaced",
        catalog: {
          revision: "replacement-fixture",
          observed_at: "2026-07-22T12:00:00Z",
          offers,
          failures: [],
        },
      } as unknown as DeploymentMessage,
    ),
  );

  expect(result.runs["run-1"]?.bookingID).toBe(
    "booking-replacement-provider",
  );
  expect(Object.keys(result.bookings)).toEqual([
    "booking-replacement-provider",
  ]);
  expect(result.rentals["rental-failed-provider"]).toBeUndefined();
  expect(result.rentals["rental-replacement-provider"]?.runningBookingID).toBe(
    "booking-replacement-provider",
  );
});

// The console's elapsed runtime is measured from the moment the machine said its
// container began, which arrives on its own event. It used to be stamped twice
// from Mercator's own clock: once when the Booking Decision was recorded, which
// for a provisioned machine is before that machine existed, and again on every
// observation, so a reconnecting console reported a workload as newly started.
test("counts a workload's runtime from the moment its machine said it began", () => {
  const decided = bookingDecidedMessage({
    eventID: "evt_booking_started",
    globalPosition: 2,
    runID: "run-observed",
    bookingID: "booking-observed",
    state: "running",
  });
  const started = executionStartedMessage(
    "run-observed",
    "2030-01-01T00:04:48Z",
  );

  const deployment = [
    requestedMessage("run-observed", "evt_requested_observed", 1),
    decided,
    started,
  ].reduce(reduceDeployment, createDeployment("ws_scenario"));

  const run = deployment.runs["run-observed"];
  expect(run?.startedAt).toBe("2030-01-01T00:04:48Z");
  expect(run?.startedAt).not.toBe(decided.type === "domain_event" ? decided.event.time : undefined);
});

// A Run nothing has reported a start for carries none. The console shows no
// elapsed runtime for it rather than counting from whenever Mercator last looked.
test("records no start moment for a Run nothing observed starting", () => {
  const deployment = [
    requestedMessage("run-quiet", "evt_requested_quiet", 1),
    bookingDecidedMessage({
      eventID: "evt_booking_quiet",
      globalPosition: 2,
      runID: "run-quiet",
      bookingID: "booking-quiet",
      state: "running",
    }),
  ].reduce(reduceDeployment, createDeployment("ws_scenario"));

  expect(deployment.runs["run-quiet"]?.startedAt).toBeUndefined();
});

function executionStartedMessage(
  runID: string,
  startedAt: string,
): DeploymentMessage {
  return {
    type: "domain_event",
    event: {
      specversion: "1.0",
      id: `evt_started_${runID}`,
      source: "test",
      type: "compute.run.execution_started.v1",
      subject: `runs/${runID}`,
      time: "2030-01-01T00:05:00Z",
      streamversion: 4,
      globalposition: 6,
      correlationid: runID,
      data: { launch_key: `launch-${runID}`, started_at: startedAt },
    },
  } as unknown as DeploymentMessage;
}

test("applies a stored decision whose candidates predate dispositions", () => {
  // Durable history holds booking decisions written before candidates carried
  // a disposition. Replaying one must degrade to "unrecorded", not throw --
  // a reducer throw is a non-retryable feed error that bricks the canvas.
  const decided = bookingDecidedMessage({
    eventID: "evt_booking_legacy",
    globalPosition: 2,
    runID: "run-legacy",
    bookingID: "booking-legacy",
    state: "running",
    candidateDisposition: "run_now_existing_rental",
  });
  if (decided.type !== "domain_event") {
    throw new Error("helper returns a domain event");
  }
  const candidates = (
    decided.event.data as {
      decision: { candidates: Record<string, unknown>[] };
    }
  ).decision.candidates;
  delete candidates[0]?.disposition;

  const deployment = [
    requestedMessage("run-legacy", "evt_requested_legacy", 1),
    decided,
  ].reduce(reduceDeployment, createDeployment("ws_scenario"));

  // The canvas does not store the decision itself; applying without a throw
  // and advancing the Run is the whole claim -- decodeEventData rejected this
  // event outright when disposition was a required key.
  const run = deployment.runs["run-legacy"];
  expect(run?.phase).toBe("running");
  expect(run?.bookingID).toBe("booking-legacy");
});
