import { effect, expect } from "@effect/vitest";
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
import * as Stream from "effect/Stream";

import { Session, testLayer } from "./session";

effect("publishes Session changes", () =>
  Effect.gen(function* () {
    const session = yield* Session;
    const changesFiber = yield* session.changes.pipe(
      Stream.take(2),
      Stream.runCollect,
      Effect.forkChild({ startImmediately: true }),
    );

    yield* session.setToken("secret");
    const changes = yield* Fiber.join(changesFiber);

    expect(Array.from(changes)).toEqual([
      { token: null },
      { token: "secret" },
    ]);
  }).pipe(Effect.provide(testLayer({ token: null }))),
);
