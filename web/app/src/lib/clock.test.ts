import { expect, test } from "vitest";
import * as Effect from "effect/Effect";
import * as Stream from "effect/Stream";

import { clock } from "./clock";

test("publishes clock time at the requested interval", async () => {
  const values = await Effect.runPromise(
    clock(5).pipe(Stream.take(3), Stream.runCollect),
  );

  const collected = Array.from(values);
  expect(collected).toHaveLength(3);
  const [first, second, third] = collected;
  if (first === undefined || second === undefined || third === undefined) {
    throw new Error("clock stream did not publish three values");
  }
  expect(second - first).toBeGreaterThanOrEqual(5);
  expect(third - second).toBeGreaterThanOrEqual(5);
});
