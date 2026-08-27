import { expect, test } from "vitest";

import type { CloudEvent } from "../api/types";

import {
  initialDeploymentFeedSnapshot,
  reduceDeploymentFeed,
} from "./snapshot";

test("Deployment feed resets and orders the CloudEvents that drive the canvas", () => {
  const playback = {
    status: "playing" as const,
    cursor: 0,
    cueCount: 14,
    elapsedMillis: 0,
    durationMillis: 90_000,
    speed: 1 as const,
  };
  const fidelity = {
    offerSource: "sanitized_recordings",
    provenCapabilities: ["placement"],
    targetCapabilities: ["rental_schedule"],
  };
  const first = reduceDeploymentFeed(initialDeploymentFeedSnapshot(), {
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
    fidelity,
  });

  expect(first.deployment.throughGlobalPosition).toBe(2);
  expect(first.events.map((event) => event.id)).toEqual(["event-2", "event-1"]);
  expect(first.playback).toEqual(playback);
  expect(first.fidelity).toEqual(fidelity);

  const duplicate = reduceDeploymentFeed(first, {
    type: "message",
    message: eventMessage(cloudEvent(2)),
  });
  expect(duplicate.events.map((event) => event.id)).toEqual([
    "event-2",
    "event-1",
  ]);
  expect(duplicate.deployment).toBe(first.deployment);

  const restarted = reduceDeploymentFeed(first, {
    type: "reset",
    messages: [
      eventMessage(cloudEvent(3)),
      {
        type: "ready",
        throughGlobalPosition: 3,
      },
    ],
    playback,
    fidelity,
  });

  expect(restarted.events.map((event) => event.id)).toEqual(["event-3"]);
});

test("reconnect discards the retained projection before authoritative replay", () => {
  const current = [cloudEvent(1), cloudEvent(2)].reduce(
    (snapshot, event) =>
      reduceDeploymentFeed(snapshot, {
        type: "message",
        message: eventMessage(event),
      }),
    initialDeploymentFeedSnapshot(),
  );

  const reconnecting = reduceDeploymentFeed(current, { type: "connecting" });

  expect(reconnecting.status).toBe("connecting");
  expect(reconnecting.deployment).toEqual({
    ready: false,
    throughGlobalPosition: 0,
    lastChange: "initial",
    offersAvailable: true,
    offers: [],
    runs: {},
    bookings: {},
    rentals: {},
  });
  expect(reconnecting.events).toEqual([]);
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
