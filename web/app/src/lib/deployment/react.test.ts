import { expect, test } from "vitest";

import type { CloudEvent } from "../api/types";

import {
  initialDeploymentFeedSnapshot,
  reduceDeploymentFeed,
} from "./snapshot";

test("Deployment feed orders the CloudEvents that drive the canvas", () => {
  const first = [cloudEvent(1), cloudEvent(2)].reduce(
    (snapshot, event) =>
      reduceDeploymentFeed(snapshot, {
        type: "message",
        message: eventMessage(event),
      }),
    initialDeploymentFeedSnapshot(),
  );

  expect(first.deployment.throughGlobalPosition).toBe(2);
  expect(first.events.map((event) => event.id)).toEqual(["event-2", "event-1"]);

  const duplicate = reduceDeploymentFeed(first, {
    type: "message",
    message: eventMessage(cloudEvent(2)),
  });
  expect(duplicate.events.map((event) => event.id)).toEqual([
    "event-2",
    "event-1",
  ]);
  expect(duplicate.deployment).toBe(first.deployment);

});

test("skips replayed events already incorporated, even outside the id window", () => {
  const live = reduceDeploymentFeed(initialDeploymentFeedSnapshot(), {
    type: "message",
    message: { type: "ready", throughGlobalPosition: 10 },
  });
  expect(live.deployment.throughGlobalPosition).toBe(10);
  expect(live.events).toEqual([]);

  const replayed = reduceDeploymentFeed(live, {
    type: "message",
    message: eventMessage(cloudEvent(5)),
  });

  expect(replayed.deployment).toBe(live.deployment);
  expect(replayed.events).toEqual([]);

  const fresh = reduceDeploymentFeed(live, {
    type: "message",
    message: eventMessage(cloudEvent(11)),
  });
  expect(fresh.events.map((event) => event.id)).toEqual(["event-11"]);
  expect(fresh.deployment.throughGlobalPosition).toBe(11);
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
    streamversion: position,
    globalposition: position,
    correlationid: "run-1",
    data: {},
  };
}
