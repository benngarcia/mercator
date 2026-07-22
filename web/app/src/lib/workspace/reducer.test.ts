import { describe, expect, test } from "vitest";

import {
  createWorkspace,
  reduceWorkspace,
  type Workspace,
  type WorkspaceMessage,
} from "./reducer";
import { fullScheduleScenarioScript } from "./scenario";

describe("Workspace event reducer", () => {
  test("projects the full-schedule fixture into time-lane state", () => {
    const workspace = reduceAll(
      createWorkspace("ws_scenario"),
      scenarioMessages(
        "ws_scenario",
        new Date("2030-01-01T00:00:00Z"),
      ),
    );

    expect(workspace.ready).toBe(true);
    expect(workspace.lastChange).toBe("live");
    expect(workspace.rentals["rental-warm"]?.runningBookingID).toBe(
      "booking-q1",
    );
    expect(workspace.rentals["rental-warm"]?.queuedBookingIDs).toEqual([
      "booking-q2",
      "booking-q3",
      "booking-q4",
      "booking-sixth",
    ]);
    expect(workspace.rentals["rental-fresh"]?.phase).toBe("idle");
    expect(workspace.runs["run-fifth"]?.phase).toBe("closed");
    expect(workspace.runs["run-q1"]?.expectedRuntimeSeconds).toBe(60);
    expect(workspace.runs["run-q1"]?.maxRuntimeSeconds).toBe(120);
    expect(workspace.offers.map((offer) => offer.id)).toEqual([
      "rental-warm",
      "fresh-slow",
    ]);
  });

  test("is deterministic for the same ordered feed", () => {
    const messages = scenarioMessages(
      "ws_scenario",
      new Date("2030-01-01T00:00:00Z"),
    );
    const first = reduceAll(createWorkspace("ws_scenario"), messages);
    const second = reduceAll(createWorkspace("ws_scenario"), messages);

    expect(second).toEqual(first);
  });

  test("rejects an expected runtime beyond the enforced maximum", () => {
    const messages = scenarioMessages(
      "ws_scenario",
      new Date("2030-01-01T00:00:00Z"),
    );
    const requested = messages.find(
      (message) =>
        message.type === "domain_event" &&
        message.event.type === "compute.run.requested.v1",
    );
    if (!requested || requested.type !== "domain_event") {
      throw new Error("fixture needs a requested event");
    }
    const data = structuredClone(requested.event.data) as {
      workload_revision: {
        spec: {
          placement: { expected_runtime_seconds: number };
          execution: { max_runtime_seconds: number };
        };
      };
    };
    data.workload_revision.spec.placement.expected_runtime_seconds = 121;
    data.workload_revision.spec.execution.max_runtime_seconds = 120;

    expect(() =>
      reduceWorkspace(createWorkspace("ws_scenario"), {
        ...requested,
        event: { ...requested.event, data },
      }),
    ).toThrow("expected runtime exceeds enforced max");
  });
});

function reduceAll(
  workspace: Workspace,
  messages: WorkspaceMessage[],
): Workspace {
  return messages.reduce(reduceWorkspace, workspace);
}

function scenarioMessages(workspaceID: string, now: Date): WorkspaceMessage[] {
  const script = fullScheduleScenarioScript(workspaceID, now);
  return [
    ...script.initialMessages,
    ...script.cues.map((cue) => cue.message),
  ];
}
