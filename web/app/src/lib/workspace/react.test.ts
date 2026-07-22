import { expect, test } from "vitest";

import type { CloudEvent } from "../api/types";

import {
  initialWorkspaceFeedSnapshot,
  reduceWorkspaceFeed,
} from "./snapshot";

test("Workspace feed resets and orders the CloudEvents that drive the canvas", () => {
  const playback = {
    status: "playing" as const,
    elapsedMillis: 0,
    durationMillis: 90_000,
    speed: 1 as const,
  };
  const first = reduceWorkspaceFeed(initialWorkspaceFeedSnapshot("ws_1"), {
    type: "reset",
    messages: [
      eventMessage(cloudEvent(1)),
      eventMessage(cloudEvent(2)),
      {
        type: "ready",
        throughGlobalPosition: 2,
      },
    ],
    playback,
  });

  expect(first.workspace.throughGlobalPosition).toBe(2);
  expect(first.events.map((event) => event.id)).toEqual(["event-2", "event-1"]);
  expect(first.playback).toEqual(playback);

  const duplicate = reduceWorkspaceFeed(first, {
    type: "message",
    message: eventMessage(cloudEvent(2)),
  });
  expect(duplicate.events.map((event) => event.id)).toEqual([
    "event-2",
    "event-1",
  ]);
  expect(duplicate.workspace).toBe(first.workspace);

  const restarted = reduceWorkspaceFeed(first, {
    type: "reset",
    messages: [
      eventMessage(cloudEvent(3)),
      {
        type: "ready",
        throughGlobalPosition: 3,
      },
    ],
    playback,
  });

  expect(restarted.events.map((event) => event.id)).toEqual(["event-3"]);
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
