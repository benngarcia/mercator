import { describe, expect, test } from "vitest";

import { createWorkspace, reduceWorkspace, type Workspace } from "./reducer";
import { fullScheduleScenarioScript } from "./scenario";

describe("full schedule scenario", () => {
  test("plays a ninety-second placement and schedule lifecycle", () => {
    const script = fullScheduleScenarioScript(
      "ws_scenario",
      new Date("2030-01-01T00:00:00Z"),
    );

    expect(script.durationMillis).toBe(90_000);
    expect(workspaceAt(script, 0).rentals["rental-warm"]?.queuedBookingIDs)
      .toHaveLength(4);
    expect(workspaceAt(script, 30_000).runs["run-fifth"]?.phase).toBe(
      "running",
    );
    expect(workspaceAt(script, 55_000).rentals["rental-warm"])
      .toMatchObject({
        runningBookingID: "booking-q1",
        queuedBookingIDs: ["booking-q2", "booking-q3", "booking-q4"],
      });
    expect(workspaceAt(script, 68_000).rentals["rental-warm"]?.queuedBookingIDs)
      .toEqual([
        "booking-q2",
        "booking-q3",
        "booking-q4",
        "booking-sixth",
      ]);
    expect(workspaceAt(script, 90_000).runs["run-fifth"]).toMatchObject({
      phase: "closed",
      outcome: "succeeded",
    });
  });
});

function workspaceAt(
  script: ReturnType<typeof fullScheduleScenarioScript>,
  elapsedMillis: number,
): Workspace {
  const messages = [
    ...script.initialMessages,
    ...script.cues
      .filter((cue) => cue.atMillis <= elapsedMillis)
      .map((cue) => cue.message),
  ];
  return messages.reduce(reduceWorkspace, createWorkspace("ws_scenario"));
}
