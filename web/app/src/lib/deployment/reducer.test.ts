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

  const deployment = reduceDeployment(
    createDeployment(),
    requested,
  );

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
  ].reduce(reduceDeployment, createDeployment());

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

function bookingDecidedMessage(input: {
  eventID: string;
  globalPosition: number;
  runID: string;
  bookingID: string;
  state: "running" | "queued";
  afterBookingID?: string;
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
          policy: { objective: "cheapest" },
          collection_report: {},
          candidates: [],
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
          selection_reason_codes: ["LOWEST_SCORE"],
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
      reduceDeployment(createDeployment(), requested),
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

  expect(result.runs["run-1"]?.bookingID).toBe("booking-replacement-provider");
  expect(Object.keys(result.bookings)).toEqual([
    "booking-replacement-provider",
  ]);
  expect(result.rentals["rental-failed-provider"]).toBeUndefined();
  expect(result.rentals["rental-replacement-provider"]?.runningBookingID).toBe(
    "booking-replacement-provider",
  );
});

test("applies a stored decision whose candidates predate dispositions", () => {
  // Durable history holds booking decisions written before candidates carried
  // a disposition. Replaying one must degrade to "unrecorded", not throw --
  // a reducer throw is a non-retryable feed error that bricks the canvas.
  const requested = structuredClone(requestedEvent) as DeploymentMessage;
  if (requested.type !== "domain_event") {
    throw new Error("fixture needs a requested event");
  }
  requested.event.subject = "runs/run-legacy";
  requested.event.correlationid = "run-legacy";
  (requested.event.data as { run_id: string }).run_id = "run-legacy";

  const decided = bookingDecidedMessage({
    eventID: "evt_booking_legacy",
    globalPosition: 2,
    runID: "run-legacy",
    bookingID: "booking-legacy",
    state: "running",
  });
  if (decided.type !== "domain_event") {
    throw new Error("helper returns a domain event");
  }
  (
    decided.event.data as {
      decision: { candidates: Record<string, unknown>[] };
    }
  ).decision.candidates = [
    {
      offer_snapshot_id: "offer-legacy",
      connection_id: "conn-legacy",
      adapter_type: "docker",
      native_ref: "loopback",
      // no disposition: this is the pre-disposition wire shape
      feasible: true,
      estimates: {
        queue_seconds: { source: "offer" },
        provision_seconds: { source: "offer" },
        pull_seconds: { source: "image_cache" },
        start_seconds: { p50: 1, p90: 2 },
        cost_usd: { expected: 0 },
      },
    },
  ];

  const deployment = [requested, decided].reduce(
    reduceDeployment,
    createDeployment(),
  );

  const run = deployment.runs["run-legacy"];
  expect(run?.decision?.candidates[0]?.offer_snapshot_id).toBe("offer-legacy");
  expect(run?.decision?.candidates[0]?.disposition).toBeUndefined();
});
