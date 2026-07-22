import { expect, test } from "vitest";

import replacementBookingEvents from "./testdata/replacement-booking-events.json";
import requestedEvent from "./testdata/requested-event.json";
import {
  createWorkspace,
  reduceWorkspace,
  type WorkspaceMessage,
} from "./reducer";

test("rejects an expected runtime beyond the enforced maximum", () => {
  const requested = structuredClone(requestedEvent) as WorkspaceMessage;
  if (requested.type !== "domain_event") {
    throw new Error("fixture needs a requested event");
  }
  const data = requested.event.data as {
    workload_revision: {
      spec: {
        placement: { expected_runtime_seconds: number };
        execution: { max_runtime_seconds: number };
      };
    };
  };
  data.workload_revision.spec.placement.expected_runtime_seconds = 121;

  expect(() =>
    reduceWorkspace(createWorkspace("ws_scenario"), requested),
  ).toThrow("expected runtime exceeds enforced max");
});

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
  const messages = replacementBookingEvents as unknown as WorkspaceMessage[];
  const requested = structuredClone(requestedEvent) as WorkspaceMessage;
  if (requested.type !== "domain_event") {
    throw new Error("fixture needs a requested event");
  }
  requested.event.subject = "runs/run-1";
  requested.event.correlationid = "run-1";
  (requested.event.data as { run_id: string }).run_id = "run-1";

  const result = messages.reduce(
    reduceWorkspace,
    reduceWorkspace(
      reduceWorkspace(createWorkspace("ws_scenario"), requested),
      {
        type: "offers_replaced",
        catalog: {
          workspace_id: "ws_scenario",
          revision: "replacement-fixture",
          observed_at: "2026-07-22T12:00:00Z",
          offers,
          failures: [],
        },
      } as unknown as WorkspaceMessage,
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
