import { effect, expect } from "@effect/vitest";
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
import * as Stream from "effect/Stream";
import { TestClock } from "effect/testing";

import { clock } from "./clock";

effect("publishes clock time at the requested interval", () =>
  Effect.gen(function* () {
    const valuesFiber = yield* clock(1_000).pipe(
      Stream.take(3),
      Stream.runCollect,
      Effect.forkChild,
    );
    yield* Effect.yieldNow;

    yield* TestClock.adjust("2 seconds");
    const values = yield* Fiber.join(valuesFiber);

    expect(Array.from(values)).toEqual([0, 1_000, 2_000]);
  }),
);
