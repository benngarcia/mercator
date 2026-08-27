import { useAtomMount, useAtomValue } from "@effect/atom-react";
import * as Data from "effect/Data";
import * as Effect from "effect/Effect";
import * as Stream from "effect/Stream";
import * as Atom from "effect/unstable/reactivity/Atom";
import * as Reactivity from "effect/unstable/reactivity/Reactivity";
import { useCallback, useRef, useState } from "react";

import { useSession } from "@/hooks/useSession";
import { resourceKey } from "@/lib/api/atoms";
import { runtime } from "@/lib/runtime";

import {
  DeploymentEvents,
  DeploymentFeedError,
  type DeploymentSignal,
} from "./feed";
import { sendScenarioPlaybackCommand } from "./playback";
import type {
  ScenarioPlaybackCommand,
  ScenarioPlaybackSpeed,
} from "./playback";
import type { DeploymentMessage } from "./reducer";
import {
  initialDeploymentFeedSnapshot,
  reduceDeploymentFeed,
  type DeploymentFeedSnapshot,
} from "./snapshot";
import { CanvasTransition } from "./transition";

export type { DeploymentFeedSnapshot } from "./snapshot";

export interface DeploymentPlaybackControls {
  readonly busy: boolean;
  readonly play: () => Promise<void>;
  readonly pause: () => Promise<void>;
  readonly previous: () => Promise<void>;
  readonly next: () => Promise<void>;
  readonly restart: () => Promise<void>;
  readonly setSpeed: (speed: ScenarioPlaybackSpeed) => Promise<void>;
}

export interface DeploymentFeed extends DeploymentFeedSnapshot {
  readonly controls: DeploymentPlaybackControls | null;
}

const snapshotAtom = Atom.make(initialDeploymentFeedSnapshot()).pipe(
  Atom.setIdleTTL("30 seconds"),
);

class DeploymentControllerKey extends Data.Class<{
  readonly token: string | null;
}> {}

class PlaybackCommandQueue {
  private tail: Promise<void> = Promise.resolve();

  enqueue(send: () => Promise<void>): Promise<void> {
    const request = this.tail.then(send);
    this.tail = request.catch(() => undefined);
    return request;
  }
}

function commandQueueFor(
  current: React.RefObject<PlaybackCommandQueue | null>,
): PlaybackCommandQueue {
  if (current.current !== null) return current.current;
  const commands = new PlaybackCommandQueue();
  current.current = commands;
  return commands;
}

function shouldAnimate(
  current: DeploymentFeedSnapshot,
  signal: DeploymentSignal,
): boolean {
  if (!current.deployment.ready) return false;
  if (signal.type === "reset") {
    return signal.playback.cursor !== current.playback?.cursor;
  }
  return signal.type === "message" && signal.message.type !== "ready";
}

function runIdForEvent(message: DeploymentMessage): string | null {
  if (message.type !== "domain_event") return null;
  const event = message.event;
  if (event.correlationid) return event.correlationid;
  return event.subject.startsWith("runs/")
    ? event.subject.slice("runs/".length)
    : null;
}

const invalidateMessage = Effect.fn("Deployment.invalidateMessage")(function* (
  message: DeploymentMessage,
) {
  const reactivity = yield* Reactivity.Reactivity;
  if (message.type === "offers_replaced") {
    yield* reactivity.invalidate([resourceKey.offers]);
    return;
  }
  if (message.type === "ready") {
    yield* reactivity.invalidate([resourceKey.runs, resourceKey.connections]);
    return;
  }
  if (message.type !== "domain_event") return;
  if (message.event.type.startsWith("compute.connection.")) {
    yield* reactivity.invalidate([resourceKey.connections]);
    return;
  }
  if (!message.event.type.startsWith("compute.run.")) return;
  const runId = runIdForEvent(message);
  if (runId === null) return;
  const keys = [
    resourceKey.runs,
    resourceKey.run(runId),
    resourceKey.runEvents(runId),
  ];
  if (message.event.type === "compute.run.booking_decided.v1") {
    keys.push(resourceKey.runDecision(runId));
  }
  yield* reactivity.invalidate(keys);
});

const controllerAtom = Atom.family((_: DeploymentControllerKey) =>
  runtime.atom((get) =>
    Stream.unwrap(
      Effect.gen(function* () {
        const events = yield* DeploymentEvents;
        const transition = yield* CanvasTransition;
        const state = snapshotAtom;

        const commitSignal = Effect.fn("Deployment.commitSignal")(function* (
          signal: DeploymentSignal,
        ) {
          const current = get.registry.get(state);
          const next = yield* Effect.try({
            try: () => reduceDeploymentFeed(current, signal),
            catch: (cause) =>
              new DeploymentFeedError({
                status: 0,
                message:
                  "A Deployment event violated the canvas projection contract.",
                retryable: false,
                cause,
              }),
          });
          yield* transition.commit(shouldAnimate(current, signal), () =>
            get.registry.set(state, next),
          );
          if (signal.type === "message") {
            yield* invalidateMessage(signal.message);
          }
          return next;
        });

        const fail = (error: DeploymentFeedError) =>
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
              .pipe(Effect.andThen(Effect.sync(() => get.registry.get(state)))),
          );

        return events
          .stream()
          .pipe(
            Stream.mapEffect(commitSignal),
            Stream.catchTag("DeploymentFeedError", fail),
          );
      }),
    ),
  ),
);

export function useDeploymentFeed(): DeploymentFeed {
  const { token } = useSession();
  const controller = controllerAtom(new DeploymentControllerKey({ token }));
  const [pendingCommands, setPendingCommands] = useState(0);
  const commandQueue = useRef<PlaybackCommandQueue | null>(null);
  const sendPlaybackCommand = useCallback(
    (command: ScenarioPlaybackCommand) => {
      setPendingCommands((pending) => pending + 1);
      const request = commandQueueFor(commandQueue).enqueue(() =>
        sendScenarioPlaybackCommand(token, command),
      );
      const clear = () => {
        setPendingCommands((pending) => Math.max(0, pending - 1));
      };
      void request.then(clear, clear);
      return request;
    },
    [token],
  );
  useAtomMount(controller);
  const value = useAtomValue(snapshotAtom);
  const controls =
    value.playback === null
      ? null
      : {
          busy: pendingCommands > 0,
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
