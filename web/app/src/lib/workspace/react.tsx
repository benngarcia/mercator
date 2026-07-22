import { useAtomMount, useAtomSet, useAtomValue } from "@effect/atom-react";
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
  type WorkspaceSignal,
} from "./feed";
import type {
  ScenarioPlaybackCommand,
  ScenarioPlaybackSpeed,
} from "./playback";
import type { WorkspaceMessage } from "./reducer";
import {
  initialWorkspaceFeedSnapshot,
  reduceWorkspaceFeed,
  type WorkspaceFeedSnapshot,
} from "./snapshot";
import { CanvasTransition } from "./transition";

export type { WorkspaceFeedSnapshot } from "./snapshot";

export interface WorkspacePlaybackControls {
  readonly play: () => void;
  readonly pause: () => void;
  readonly previous: () => void;
  readonly next: () => void;
  readonly restart: () => void;
  readonly setSpeed: (speed: ScenarioPlaybackSpeed) => void;
}

export interface WorkspaceFeed extends WorkspaceFeedSnapshot {
  readonly controls: WorkspacePlaybackControls | null;
}

const snapshotAtom = Atom.family((workspaceId: string) =>
  Atom.make(initialWorkspaceFeedSnapshot(workspaceId)).pipe(
    Atom.setIdleTTL("30 seconds"),
  ),
);

function shouldAnimate(
  current: WorkspaceFeedSnapshot,
  signal: WorkspaceSignal,
): boolean {
  if (!current.workspace.ready) return false;
  if (signal.type === "reset") {
    return signal.playback.cursor !== current.playback?.cursor;
  }
  return signal.type === "message" && signal.message.type !== "ready";
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
                try: () => reduceWorkspaceFeed(current, signal),
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

const playbackCommandAtom = Atom.family((workspaceId: string) =>
  runtime
    .fn<ScenarioPlaybackCommand>()(
      (command) =>
        Effect.gen(function* () {
          const events = yield* WorkspaceEvents;
          yield* events.command(workspaceId, command);
        }),
      { concurrent: true },
    )
    .pipe(Atom.setIdleTTL("30 seconds")),
);

const inactiveSnapshotAtom = Atom.make<WorkspaceFeedSnapshot | null>(null);
const inactiveControllerAtom = Atom.make(null);

export function useWorkspaceFeed(): WorkspaceFeed | null {
  const { token, workspace } = useSession();
  const controller =
    workspace === null
      ? inactiveControllerAtom
      : controllerAtom(workspace)(token);
  const snapshot =
    workspace === null ? inactiveSnapshotAtom : snapshotAtom(workspace);
  const sendPlaybackCommand = useAtomSet(playbackCommandAtom(workspace ?? ""));
  useAtomMount(controller);
  const value = useAtomValue(snapshot);
  if (value === null) return null;
  const controls =
    value.playback === null
      ? null
      : {
          play: () => sendPlaybackCommand({ type: "play" }),
          pause: () => sendPlaybackCommand({ type: "pause" }),
          previous: () => sendPlaybackCommand({ type: "previous" }),
          next: () => sendPlaybackCommand({ type: "next" }),
          restart: () => sendPlaybackCommand({ type: "restart" }),
          setSpeed: (speed: ScenarioPlaybackSpeed) =>
            sendPlaybackCommand({ type: "set_speed", speed }),
        };
  return { ...value, controls };
}
