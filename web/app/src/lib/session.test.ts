import { effect, expect } from "@effect/vitest";
import * as Effect from "effect/Effect";
import * as Fiber from "effect/Fiber";
import * as Stream from "effect/Stream";

import { Session, testLayer } from "./session";

effect("stores the selected Workspace in the Session service", () =>
  Effect.gen(function* () {
    const session = yield* Session;

    yield* session.setWorkspace("ws_42");
    const current = yield* session.current;

    expect(current.workspace).toBe("ws_42");
  }).pipe(Effect.provide(testLayer({ token: null, workspace: null }))),
);

effect("publishes Session changes", () =>
  Effect.gen(function* () {
    const session = yield* Session;
    const changesFiber = yield* session.changes.pipe(
      Stream.take(2),
      Stream.runCollect,
      Effect.forkChild,
    );
    yield* Effect.yieldNow;

    yield* session.setToken("secret");
    const changes = yield* Fiber.join(changesFiber);

    expect(Array.from(changes)).toEqual([
      { token: null, workspace: null },
      { token: "secret", workspace: null },
    ]);
  }).pipe(Effect.provide(testLayer({ token: null, workspace: null }))),
);
