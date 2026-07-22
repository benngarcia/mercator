import * as Context from "effect/Context";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Stream from "effect/Stream";
import * as SubscriptionRef from "effect/SubscriptionRef";

const TOKEN_KEY = "mercator.token";
const WORKSPACE_KEY = "mercator.workspace";

export interface SessionState {
  readonly token: string | null;
  readonly workspace: string | null;
}

export interface SessionService {
  readonly current: Effect.Effect<SessionState>;
  readonly changes: Stream.Stream<SessionState>;
  readonly setToken: (token: string | null) => Effect.Effect<void>;
  readonly setWorkspace: (workspace: string | null) => Effect.Effect<void>;
}

export class Session extends Context.Service<Session, SessionService>()(
  "@mercator/Session",
) {}

function browserStorage(): Storage | null {
  try {
    return globalThis.localStorage;
  } catch {
    return null;
  }
}

function readStoredValue(key: string): string | null {
  try {
    return browserStorage()?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

export function readBrowserSession(): SessionState {
  const workspace = readStoredValue(WORKSPACE_KEY)?.trim() || null;
  return {
    token: readStoredValue(TOKEN_KEY),
    workspace,
  };
}

function persist(key: string, value: string | null): void {
  const storage = browserStorage();
  if (storage === null) {
    return;
  }
  try {
    if (value === null || value === "") {
      storage.removeItem(key);
    } else {
      storage.setItem(key, value);
    }
  } catch {
    // Browsers may deny persistent storage in private or embedded contexts.
    // The live SubscriptionRef still owns the session for this tab.
  }
}

function isSessionStorageEvent(event: StorageEvent): boolean {
  return (
    event.key === null || event.key === TOKEN_KEY || event.key === WORKSPACE_KEY
  );
}

export const layer = Layer.effect(
  Session,
  Effect.gen(function* () {
    const state = yield* SubscriptionRef.make(readBrowserSession());

    if (typeof window !== "undefined") {
      const onStorage = (event: StorageEvent) => {
        if (isSessionStorageEvent(event)) {
          Effect.runSync(SubscriptionRef.set(state, readBrowserSession()));
        }
      };
      window.addEventListener("storage", onStorage);
      yield* Effect.addFinalizer(() =>
        Effect.sync(() => window.removeEventListener("storage", onStorage)),
      );
    }

    const update = Effect.fn("Session.update")(function* (next: SessionState) {
      yield* SubscriptionRef.set(state, next);
    });

    const setToken = Effect.fn("Session.setToken")(function* (
      token: string | null,
    ) {
      persist(TOKEN_KEY, token);
      const current = yield* SubscriptionRef.get(state);
      yield* update({ ...current, token });
    });

    const setWorkspace = Effect.fn("Session.setWorkspace")(function* (
      workspace: string | null,
    ) {
      persist(WORKSPACE_KEY, workspace);
      const current = yield* SubscriptionRef.get(state);
      yield* update({ ...current, workspace: workspace?.trim() || null });
    });

    return Session.of({
      current: SubscriptionRef.get(state),
      changes: SubscriptionRef.changes(state),
      setToken,
      setWorkspace,
    });
  }),
);

export const testLayer = (initial: SessionState) =>
  Layer.effect(
    Session,
    Effect.gen(function* () {
      const state = yield* SubscriptionRef.make(initial);
      return Session.of({
        current: SubscriptionRef.get(state),
        changes: SubscriptionRef.changes(state),
        setToken: (token) =>
          SubscriptionRef.update(state, (current) => ({ ...current, token })),
        setWorkspace: (workspace) =>
          SubscriptionRef.update(state, (current) => ({
            ...current,
            workspace,
          })),
      });
    }),
  );
