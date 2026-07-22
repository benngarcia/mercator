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
  readonly cursor: number;
  readonly cueCount: number;
  readonly elapsedMillis: number;
  readonly durationMillis: number;
  readonly speed: ScenarioPlaybackSpeed;
}

export type ScenarioPlaybackCommand =
  | { readonly type: "play" }
  | { readonly type: "pause" }
  | { readonly type: "previous" }
  | { readonly type: "next" }
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
  readonly observedCues: readonly WorkspaceMessage[];
}

interface PlaybackTransition {
  readonly state: PlaybackState;
  readonly emissions: readonly ScenarioPlaybackEmission[];
}

export const makeScenarioPlayback = Effect.fn("ScenarioPlayback.make")(
  function* (script: ScenarioScript, autoplay: boolean) {
    const output = yield* Queue.bounded<ScenarioPlaybackEmission>(1);
    yield* Effect.addFinalizer(() => Queue.shutdown(output));
    const now = yield* Clock.currentTimeMillis;
    const initial = initialTransition(script, autoplay, now);
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
      const now = yield* Clock.currentTimeMillis;
      yield* commit((current) => applyCommand(script, current, value, now));
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
  now: number,
): PlaybackTransition {
  const observedCues = autoplay
    ? []
    : script.cues.map((cue) => observedNow(cue.message, now));
  const state: PlaybackState = autoplay
    ? {
        status: "playing",
        cursor: 0,
        cueCount: script.cues.length,
        elapsedMillis: 0,
        durationMillis: script.durationMillis,
        speed: 1,
        observedCues,
      }
    : {
        status: "finished",
        cursor: script.cues.length,
        cueCount: script.cues.length,
        elapsedMillis: script.durationMillis,
        durationMillis: script.durationMillis,
        speed: 1,
        observedCues,
      };
  const messages = autoplay
    ? script.initialMessages
    : [...script.initialMessages, ...observedCues];
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
  let cursor = current.cursor;
  const observedCues = [...current.observedCues];
  const emissions: ScenarioPlaybackEmission[] = [];
  while (
    cursor < script.cues.length &&
    script.cues[cursor]!.atMillis <= elapsedMillis
  ) {
    const message = observedNow(script.cues[cursor]!.message, now);
    emissions.push({
      type: "message",
      message,
    });
    observedCues.push(message);
    cursor += 1;
  }
  const state: PlaybackState = {
    ...current,
    status: elapsedMillis === script.durationMillis ? "finished" : "playing",
    cursor,
    elapsedMillis,
    observedCues,
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
  now: number,
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
    case "previous":
      return stepPrevious(script, current);
    case "next":
      return stepNext(script, current, now);
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
    cursor: 0,
    cueCount: script.cues.length,
    elapsedMillis: 0,
    durationMillis: script.durationMillis,
    speed,
    observedCues: [],
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

function stepPrevious(
  script: ScenarioScript,
  current: PlaybackState,
): PlaybackTransition {
  if (current.cursor === 0) return updateStatus(current, "paused");
  const cursor = current.cursor - 1;
  const observedCues = current.observedCues.slice(0, cursor);
  const state: PlaybackState = {
    ...current,
    status: "paused",
    cursor,
    elapsedMillis: cursor === 0 ? 0 : script.cues[cursor - 1]!.atMillis,
    observedCues,
  };
  return resetToCursor(script, state);
}

function stepNext(
  script: ScenarioScript,
  current: PlaybackState,
  now: number,
): PlaybackTransition {
  if (current.cursor === script.cues.length) {
    return { state: current, emissions: [] };
  }
  const cue = script.cues[current.cursor]!;
  const observedCues = [
    ...current.observedCues,
    observedNow(cue.message, now),
  ];
  const cursor = current.cursor + 1;
  const state: PlaybackState = {
    ...current,
    status: cursor === script.cues.length ? "finished" : "paused",
    cursor,
    elapsedMillis: cue.atMillis,
    observedCues,
  };
  return resetToCursor(script, state);
}

function resetToCursor(
  script: ScenarioScript,
  state: PlaybackState,
): PlaybackTransition {
  return {
    state,
    emissions: [
      {
        type: "reset",
        messages: [...script.initialMessages, ...state.observedCues],
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
    cursor: state.cursor,
    cueCount: state.cueCount,
    elapsedMillis: state.elapsedMillis,
    durationMillis: state.durationMillis,
    speed: state.speed,
  };
}
