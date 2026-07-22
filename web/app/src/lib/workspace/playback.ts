import * as Clock from "effect/Clock";
import * as Effect from "effect/Effect";
import * as Queue from "effect/Queue";
import * as Schedule from "effect/Schedule";
import * as Stream from "effect/Stream";
import * as SynchronizedRef from "effect/SynchronizedRef";

import type { WorkspaceMessage } from "./reducer";
import type { ScenarioScript } from "./scenario";

const TICK_MILLIS = 250;

export type ScenarioPlaybackSpeed = 1 | 2 | 4;
export type ScenarioPlaybackStatus = "playing" | "paused" | "finished";

export interface ScenarioPlaybackSnapshot {
  readonly status: ScenarioPlaybackStatus;
  readonly elapsedMillis: number;
  readonly durationMillis: number;
  readonly speed: ScenarioPlaybackSpeed;
}

export type ScenarioPlaybackCommand =
  | { readonly type: "play" }
  | { readonly type: "pause" }
  | { readonly type: "restart" }
  | {
      readonly type: "set_speed";
      readonly speed: ScenarioPlaybackSpeed;
    };

export type ScenarioPlaybackEmission =
  | {
      readonly type: "reset";
      readonly messages: readonly WorkspaceMessage[];
      readonly playback: ScenarioPlaybackSnapshot;
    }
  | { readonly type: "message"; readonly message: WorkspaceMessage }
  | {
      readonly type: "playback";
      readonly playback: ScenarioPlaybackSnapshot;
    };

export interface ScenarioPlaybackController {
  readonly stream: Stream.Stream<ScenarioPlaybackEmission>;
  readonly command: (
    command: ScenarioPlaybackCommand,
  ) => Effect.Effect<void>;
}

interface PlaybackState extends ScenarioPlaybackSnapshot {
  readonly nextCue: number;
}

interface PlaybackTransition {
  readonly state: PlaybackState;
  readonly emissions: readonly ScenarioPlaybackEmission[];
}

export const makeScenarioPlayback = Effect.fn("ScenarioPlayback.make")(
  function* (script: ScenarioScript, autoplay: boolean) {
    const output = yield* Queue.bounded<ScenarioPlaybackEmission>(1);
    yield* Effect.addFinalizer(() => Queue.shutdown(output));
    const initial = initialTransition(script, autoplay);
    const state = yield* SynchronizedRef.make(initial.state);
    yield* Queue.offerAll(output, initial.emissions);

    const commit = Effect.fn("ScenarioPlayback.commit")(function* (
      transition: (current: PlaybackState) => PlaybackTransition,
    ) {
      yield* SynchronizedRef.modifyEffect(state, (current) => {
        const next = transition(current);
        return Queue.offerAll(output, next.emissions).pipe(
          Effect.as([undefined, next.state] as const),
        );
      });
    });

    yield* Stream.fromSchedule(Schedule.spaced(`${TICK_MILLIS} millis`)).pipe(
      Stream.runForEach(() =>
        Clock.currentTimeMillis.pipe(
          Effect.flatMap((now) =>
            commit((current) => tick(script, current, now)),
          ),
        ),
      ),
      Effect.forkScoped,
    );

    const command = Effect.fn("ScenarioPlayback.command")(function* (
      value: ScenarioPlaybackCommand,
    ) {
      yield* commit((current) => applyCommand(script, current, value));
    });

    return {
      stream: Stream.fromQueue(output),
      command,
    } satisfies ScenarioPlaybackController;
  },
);

function initialTransition(
  script: ScenarioScript,
  autoplay: boolean,
): PlaybackTransition {
  const state: PlaybackState = autoplay
    ? {
        status: "playing",
        elapsedMillis: 0,
        durationMillis: script.durationMillis,
        speed: 1,
        nextCue: 0,
      }
    : {
        status: "finished",
        elapsedMillis: script.durationMillis,
        durationMillis: script.durationMillis,
        speed: 1,
        nextCue: script.cues.length,
      };
  const messages = autoplay
    ? script.initialMessages
    : [
        ...script.initialMessages,
        ...script.cues.map((cue) => cue.message),
      ];
  return {
    state,
    emissions: [{ type: "reset", messages, playback: snapshot(state) }],
  };
}

function tick(
  script: ScenarioScript,
  current: PlaybackState,
  now: number,
): PlaybackTransition {
  if (current.status !== "playing") {
    return { state: current, emissions: [] };
  }
  const elapsedMillis = Math.min(
    script.durationMillis,
    current.elapsedMillis + TICK_MILLIS * current.speed,
  );
  let nextCue = current.nextCue;
  const emissions: ScenarioPlaybackEmission[] = [];
  while (
    nextCue < script.cues.length &&
    script.cues[nextCue]!.atMillis <= elapsedMillis
  ) {
    emissions.push({
      type: "message",
      message: observedNow(script.cues[nextCue]!.message, now),
    });
    nextCue += 1;
  }
  const state: PlaybackState = {
    ...current,
    status: elapsedMillis === script.durationMillis ? "finished" : "playing",
    elapsedMillis,
    nextCue,
  };
  emissions.push({ type: "playback", playback: snapshot(state) });
  return { state, emissions };
}

function observedNow(message: WorkspaceMessage, now: number): WorkspaceMessage {
  if (message.type !== "domain_event") return message;
  return {
    ...message,
    event: { ...message.event, time: new Date(now).toISOString() },
  };
}

function applyCommand(
  script: ScenarioScript,
  current: PlaybackState,
  command: ScenarioPlaybackCommand,
): PlaybackTransition {
  switch (command.type) {
    case "restart":
      return resetTransition(script, current.speed);
    case "play":
      if (current.status === "finished") {
        return resetTransition(script, current.speed);
      }
      return updateStatus(current, "playing");
    case "pause":
      return updateStatus(current, "paused");
    case "set_speed": {
      const state = { ...current, speed: command.speed };
      return {
        state,
        emissions: [{ type: "playback", playback: snapshot(state) }],
      };
    }
  }
}

function resetTransition(
  script: ScenarioScript,
  speed: ScenarioPlaybackSpeed,
): PlaybackTransition {
  const state: PlaybackState = {
    status: "playing",
    elapsedMillis: 0,
    durationMillis: script.durationMillis,
    speed,
    nextCue: 0,
  };
  return {
    state,
    emissions: [
      {
        type: "reset",
        messages: script.initialMessages,
        playback: snapshot(state),
      },
    ],
  };
}

function updateStatus(
  current: PlaybackState,
  status: ScenarioPlaybackStatus,
): PlaybackTransition {
  if (current.status === status) return { state: current, emissions: [] };
  const state = { ...current, status };
  return {
    state,
    emissions: [{ type: "playback", playback: snapshot(state) }],
  };
}

function snapshot(state: PlaybackState): ScenarioPlaybackSnapshot {
  return {
    status: state.status,
    elapsedMillis: state.elapsedMillis,
    durationMillis: state.durationMillis,
    speed: state.speed,
  };
}
