import { effect, expect } from "@effect/vitest";
import * as Effect from "effect/Effect";
import * as Ref from "effect/Ref";
import * as Stream from "effect/Stream";
import { TestClock } from "effect/testing";

import {
  makeScenarioPlayback,
  type ScenarioPlaybackEmission,
} from "./playback";
import type { ScenarioScript } from "./scenario";

effect("scenario playback pauses, changes speed, and restarts", () =>
  Effect.gen(function* () {
    yield* TestClock.setTime(1_000_000);
    const controller = yield* makeScenarioPlayback(testScript, true);
    const emissions = yield* Ref.make<readonly ScenarioPlaybackEmission[]>([]);
    yield* controller.stream.pipe(
      Stream.runForEach((emission) =>
        Ref.update(emissions, (current) => [...current, emission]),
      ),
      Effect.forkChild,
    );
    yield* Effect.yieldNow;

    yield* TestClock.adjust("1 second");
    const afterFirstCue = yield* Ref.get(emissions);
    expect(messageCount(afterFirstCue)).toBe(1);
    expect(
      afterFirstCue.find((emission) => emission.type === "message"),
    ).toMatchObject({
      message: {
        type: "domain_event",
        event: { time: new Date(1_001_000).toISOString() },
      },
    });

    yield* controller.command({ type: "pause" });
    yield* TestClock.adjust("5 seconds");
    expect(messageCount(yield* Ref.get(emissions))).toBe(1);

    yield* controller.command({ type: "set_speed", speed: 4 });
    yield* controller.command({ type: "play" });
    yield* TestClock.adjust("250 millis");
    expect(messageCount(yield* Ref.get(emissions))).toBe(2);

    yield* controller.command({ type: "restart" });
    yield* Effect.yieldNow;
    const latest = (yield* Ref.get(emissions)).at(-1);
    expect(latest).toMatchObject({
      type: "reset",
      playback: { elapsedMillis: 0, status: "playing", speed: 4 },
    });
  }),
);

effect("scenario playback steps backward and forward one event at a time", () =>
  Effect.gen(function* () {
    yield* TestClock.setTime(1_000_000);
    const controller = yield* makeScenarioPlayback(testScript, true);
    const emissions = yield* Ref.make<readonly ScenarioPlaybackEmission[]>([]);
    yield* controller.stream.pipe(
      Stream.runForEach((emission) =>
        Ref.update(emissions, (current) => [...current, emission]),
      ),
      Effect.forkChild,
    );
    yield* Effect.yieldNow;

    yield* controller.command({ type: "next" });
    yield* Effect.yieldNow;
    expect((yield* Ref.get(emissions)).at(-1)).toMatchObject({
      type: "reset",
      messages: [
        { type: "ready" },
        { type: "domain_event", event: { id: "event-1" } },
      ],
      playback: {
        status: "paused",
        cursor: 1,
        cueCount: 2,
        elapsedMillis: 1_000,
      },
    });

    yield* controller.command({ type: "next" });
    yield* Effect.yieldNow;
    expect((yield* Ref.get(emissions)).at(-1)).toMatchObject({
      type: "reset",
      messages: [
        { type: "ready" },
        { type: "domain_event", event: { id: "event-1" } },
        { type: "ready" },
      ],
      playback: {
        status: "finished",
        cursor: 2,
        cueCount: 2,
        elapsedMillis: 2_000,
      },
    });

    yield* controller.command({ type: "previous" });
    yield* Effect.yieldNow;
    expect((yield* Ref.get(emissions)).at(-1)).toMatchObject({
      type: "reset",
      messages: [
        { type: "ready" },
        { type: "domain_event", event: { id: "event-1" } },
      ],
      playback: {
        status: "paused",
        cursor: 1,
        cueCount: 2,
        elapsedMillis: 1_000,
      },
    });
  }),
);

const testScript: ScenarioScript = {
  durationMillis: 2_000,
  initialMessages: [{ type: "ready", throughGlobalPosition: 0 }],
  cues: [
    {
      atMillis: 1_000,
      message: {
        type: "domain_event",
        event: {
          specversion: "1.0",
          id: "event-1",
          source: "test",
          type: "compute.test.event.v1",
          subject: "runs/run-1",
          time: "2030-01-01T00:00:00Z",
          workspaceid: "ws_1",
          streamversion: 1,
          globalposition: 1,
          correlationid: "run-1",
          data: {},
        },
      },
    },
    { atMillis: 2_000, message: { type: "ready", throughGlobalPosition: 0 } },
  ],
};

function messageCount(emissions: readonly ScenarioPlaybackEmission[]): number {
  return emissions.filter((emission) => emission.type === "message").length;
}
