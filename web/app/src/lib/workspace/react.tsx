import { useAtomMount, useAtomValue } from "@effect/atom-react";
import * as Effect from "effect/Effect";
import * as Stream from "effect/Stream";
import * as Atom from "effect/unstable/reactivity/Atom";
import * as Reactivity from "effect/unstable/reactivity/Reactivity";

import { useSession } from "@/hooks/useSession";
import { resourceKey } from "@/lib/api/atoms";
import { runtime } from "@/lib/runtime";

import {
  WorkspaceEvents,
  WorkspaceFeedError,
  type WorkspaceFeedStatus,
  type WorkspaceSignal,
} from "./feed";
import {
  createWorkspace,
  reduceWorkspace,
  type Workspace,
  type WorkspaceMessage,
} from "./reducer";
import { CanvasTransition } from "./transition";

export interface WorkspaceFeedSnapshot {
  readonly workspace: Workspace;
  readonly status: WorkspaceFeedStatus;
  readonly error: WorkspaceFeedError | null;
}

function initialSnapshot(workspaceId: string): WorkspaceFeedSnapshot {
  return {
    workspace: createWorkspace(workspaceId),
    status: "idle",
    error: null,
  };
}

const snapshotAtom = Atom.family((workspaceId: string) =>
  Atom.make(initialSnapshot(workspaceId)).pipe(Atom.setIdleTTL("30 seconds")),
);

function messageStatus(
  current: WorkspaceFeedStatus,
  message: WorkspaceMessage,
  workspace: Workspace,
): WorkspaceFeedStatus {
  if (message.type === "ready") return "live";
  if (message.type === "offers_unavailable") return "degraded";
  if (message.type === "offers_replaced" && workspace.ready) return "live";
  return current;
}

function nextSnapshot(
  current: WorkspaceFeedSnapshot,
  signal: WorkspaceSignal,
): WorkspaceFeedSnapshot {
  if (signal.type === "connecting") {
    return { ...current, status: "connecting" };
  }
  const workspace = reduceWorkspace(current.workspace, signal.message);
  return {
    workspace,
    status: messageStatus(current.status, signal.message, workspace),
    error: null,
  };
}

function shouldAnimate(
  current: WorkspaceFeedSnapshot,
  signal: WorkspaceSignal,
): boolean {
  return (
    signal.type === "message" &&
    current.workspace.ready &&
    signal.message.type !== "ready"
  );
}

function runIdForEvent(message: WorkspaceMessage): string | null {
  if (message.type !== "domain_event") return null;
  const event = message.event;
  if (event.correlationid) return event.correlationid;
  return event.subject.startsWith("runs/")
    ? event.subject.slice("runs/".length)
    : null;
}

const invalidateMessage = Effect.fn("Workspace.invalidateMessage")(function* (
  workspaceId: string,
  message: WorkspaceMessage,
) {
  const reactivity = yield* Reactivity.Reactivity;
  if (message.type === "offers_replaced") {
    yield* reactivity.invalidate([resourceKey.offers(workspaceId)]);
    return;
  }
  if (message.type === "ready") {
    yield* reactivity.invalidate([
      resourceKey.runs(workspaceId),
      resourceKey.connections(workspaceId),
    ]);
    return;
  }
  if (message.type !== "domain_event") return;
  if (message.event.type.startsWith("compute.connection.")) {
    yield* reactivity.invalidate([resourceKey.connections(workspaceId)]);
    return;
  }
  if (!message.event.type.startsWith("compute.run.")) return;
  const runId = runIdForEvent(message);
  if (runId === null) return;
  const keys = [
    resourceKey.runs(workspaceId),
    resourceKey.run(workspaceId, runId),
    resourceKey.runEvents(workspaceId, runId),
  ];
  if (message.event.type === "compute.run.booking_decided.v1") {
    keys.push(resourceKey.runDecision(workspaceId, runId));
  }
  yield* reactivity.invalidate(keys);
});

const controllerAtom = Atom.family((workspaceId: string) =>
  Atom.family((_token: string | null) =>
    runtime
      .atom((get) =>
        Stream.unwrap(
          Effect.gen(function* () {
            const events = yield* WorkspaceEvents;
            const transition = yield* CanvasTransition;
            const state = snapshotAtom(workspaceId);

            const commitSignal = Effect.fn("Workspace.commitSignal")(function* (
              signal: WorkspaceSignal,
            ) {
              const current = get.registry.get(state);
              const next = yield* Effect.try({
                try: () => nextSnapshot(current, signal),
                catch: (cause) =>
                  new WorkspaceFeedError({
                    status: 0,
                    message:
                      "A Workspace event violated the canvas projection contract.",
                    retryable: false,
                    cause,
                  }),
              });
              yield* transition.commit(shouldAnimate(current, signal), () =>
                get.registry.set(state, next),
              );
              if (signal.type === "message") {
                yield* invalidateMessage(workspaceId, signal.message);
              }
              return next;
            });

            const fail = (error: WorkspaceFeedError) =>
              Stream.fromEffect(
                transition
                  .commit(false, () => {
                    const current = get.registry.get(state);
                    get.registry.set(state, {
                      ...current,
                      status: "error",
                      error,
                    });
                  })
                  .pipe(
                    Effect.andThen(Effect.sync(() => get.registry.get(state))),
                  ),
              );

            return events
              .stream(workspaceId)
              .pipe(
                Stream.mapEffect(commitSignal),
                Stream.catchTag("WorkspaceFeedError", fail),
              );
          }),
        ),
      )
      .pipe(Atom.setIdleTTL("30 seconds")),
  ),
);

const inactiveSnapshotAtom = Atom.make<WorkspaceFeedSnapshot | null>(null);
const inactiveControllerAtom = Atom.make(null);

export function useWorkspaceFeed(): WorkspaceFeedSnapshot | null {
  const { token, workspace } = useSession();
  const controller =
    workspace === null
      ? inactiveControllerAtom
      : controllerAtom(workspace)(token);
  const snapshot =
    workspace === null ? inactiveSnapshotAtom : snapshotAtom(workspace);
  useAtomMount(controller);
  return useAtomValue(snapshot);
}
