import * as Effect from "effect/Effect";
import * as Stream from "effect/Stream";

import { runtime } from "./runtime";
import { readBrowserSession, Session } from "./session";

export const sessionAtom = runtime.atom(
  Stream.unwrap(
    Effect.gen(function* () {
      const session = yield* Session;
      return session.changes;
    }),
  ),
  { initialValue: readBrowserSession() },
);

export const setTokenAtom = runtime.fn<string | null>()(
  Effect.fn("SessionAtom.setToken")(function* (token) {
    const session = yield* Session;
    yield* session.setToken(token);
  }),
);

export const setWorkspaceAtom = runtime.fn<string | null>()(
  Effect.fn("SessionAtom.setWorkspace")(function* (workspace) {
    const session = yield* Session;
    yield* session.setWorkspace(workspace);
  }),
);
