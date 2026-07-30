import { expect, test } from "vitest";

import type { CloudEvent } from "../api/types";

import {
  initialWorkspaceFeedSnapshot,
  reduceWorkspaceFeed,
} from "./snapshot";

test("Workspace feed orders the CloudEvents that drive the canvas", () => {
  const first = [cloudEvent(1), cloudEvent(2)].reduce(
    (snapshot, event) =>
      reduceWorkspaceFeed(snapshot, {
        type: "message",
        message: eventMessage(event),
      }),
    initialWorkspaceFeedSnapshot("ws_1"),
  );

  expect(first.workspace.throughGlobalPosition).toBe(2);
  expect(first.events.map((event) => event.id)).toEqual(["event-2", "event-1"]);

  const duplicate = reduceWorkspaceFeed(first, {
    type: "message",
    message: eventMessage(cloudEvent(2)),
  });
  expect(duplicate.events.map((event) => event.id)).toEqual([
    "event-2",
    "event-1",
  ]);
  expect(duplicate.workspace).toBe(first.workspace);

});

test("skips replayed events already incorporated, even outside the id window", () => {
  const live = reduceWorkspaceFeed(initialWorkspaceFeedSnapshot("ws_1"), {
    type: "message",
    message: { type: "ready", throughGlobalPosition: 10 },
  });
  expect(live.workspace.throughGlobalPosition).toBe(10);
  expect(live.events).toEqual([]);

  const replayed = reduceWorkspaceFeed(live, {
    type: "message",
    message: eventMessage(cloudEvent(5)),
  });

  expect(replayed.workspace).toBe(live.workspace);
  expect(replayed.events).toEqual([]);

  const fresh = reduceWorkspaceFeed(live, {
    type: "message",
    message: eventMessage(cloudEvent(11)),
  });
  expect(fresh.events.map((event) => event.id)).toEqual(["event-11"]);
  expect(fresh.workspace.throughGlobalPosition).toBe(11);
});

function eventMessage(event: CloudEvent) {
  return { type: "domain_event" as const, event };
}

function cloudEvent(position: number): CloudEvent {
  return {
    specversion: "1.0",
    id: `event-${position}`,
    source: "test",
    type: "compute.test.event.v1",
    subject: "runs/run-1",
    time: "2030-01-01T00:00:00Z",
    workspaceid: "ws_1",
    streamversion: position,
    globalposition: position,
    correlationid: "run-1",
    data: {},
  };
}
