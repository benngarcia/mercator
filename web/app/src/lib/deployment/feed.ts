import * as Context from "effect/Context";
import * as Data from "effect/Data";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Option from "effect/Option";
import * as Schedule from "effect/Schedule";
import * as Schema from "effect/Schema";
import * as Stream from "effect/Stream";
import * as HttpClient from "effect/unstable/http/HttpClient";
import * as HttpClientRequest from "effect/unstable/http/HttpClientRequest";
import * as Sse from "effect/unstable/encoding/Sse";

import { Session } from "@/lib/session";

import {
  CloudEvent,
  DashboardMessage,
  DashboardPlayback,
  DashboardReset,
  OfferCatalogReplacement,
  Ready,
} from "./contracts";
import {
  type ScenarioPlaybackEmission,
  type ScenarioFidelity,
  type ScenarioPlaybackSnapshot,
} from "./playback";
import type { DeploymentMessage } from "./reducer";

export type DeploymentFeedStatus =
  "idle" | "connecting" | "live" | "degraded" | "error";

export class DeploymentFeedError extends Data.TaggedError(
  "DeploymentFeedError",
)<{
  readonly status: number;
  readonly message: string;
  readonly retryable: boolean;
  readonly cause?: unknown;
}> {}

export type DeploymentSignal =
  { readonly type: "connecting" } | ScenarioPlaybackEmission;

export interface DeploymentEventsService {
  readonly stream: () => Stream.Stream<DeploymentSignal, DeploymentFeedError>;
}

export class DeploymentEvents extends Context.Service<
  DeploymentEvents,
  DeploymentEventsService
>()("@mercator/DeploymentEvents") {}

const reconnectSchedule = Schedule.spaced("1 second").pipe(
  Schedule.while(
    ({ input }) => input instanceof DeploymentFeedError && input.retryable,
  ),
);

function feedRequest(
  token: string | null,
  scenario: ReturnType<typeof activeScenario>,
) {
  let request = HttpClientRequest.get("/v1/console/events").pipe(
    HttpClientRequest.accept("text/event-stream"),
  );
  if (token !== null) {
    request = HttpClientRequest.bearerToken(request, token);
  }
  if (scenario !== null) {
    request = HttpClientRequest.setUrlParam(request, "scenario", scenario.name);
    request = HttpClientRequest.setUrlParam(
      request,
      "play",
      scenario.autoplay ? "1" : "0",
    );
  }
  return request;
}

function decodeFailure(message: string, cause: unknown) {
  return new DeploymentFeedError({
    status: 0,
    message,
    retryable: false,
    cause,
  });
}

function decodeJson<S extends Schema.Constraint>(schema: S, data: string) {
  return Schema.decodeUnknownEffect(Schema.fromJsonString(schema))(data).pipe(
    Effect.mapError((cause) =>
      decodeFailure(
        "The Deployment event feed sent an invalid payload.",
        cause,
      ),
    ),
  );
}

function playbackSnapshot(
  playback: Schema.Schema.Type<typeof DashboardPlayback>,
): ScenarioPlaybackSnapshot {
  return {
    status: playback.status,
    cursor: playback.cursor,
    cueCount: playback.cue_count,
    elapsedMillis: playback.elapsed_millis,
    durationMillis: playback.duration_millis,
    speed: playback.speed,
  };
}

function scenarioFidelity(
  fidelity: Schema.Schema.Type<typeof DashboardReset>["fidelity"],
): ScenarioFidelity {
  return {
    offerSource: fidelity.offer_source,
    provenCapabilities: fidelity.proven_capabilities,
    targetCapabilities: fidelity.target_capabilities,
  };
}

function deploymentMessage(
  message: Schema.Schema.Type<typeof DashboardMessage>,
): DeploymentMessage {
  switch (message.type) {
    case "domain_event":
      return { type: "domain_event", event: message.event };
    case "offers_replaced":
      return { type: "offers_replaced", catalog: message.catalog };
    case "offers_unavailable":
      return { type: "offers_unavailable" };
    case "ready":
      return {
        type: "ready",
        throughGlobalPosition: message.through_global_position,
      };
  }
}

function decodeFrame(
  frame: Sse.Event,
): Effect.Effect<Option.Option<DeploymentSignal>, DeploymentFeedError> {
  switch (frame.event ?? "message") {
    case "domain_event":
      return decodeJson(CloudEvent, frame.data).pipe(
        Effect.map((event) =>
          Option.some<DeploymentSignal>({
            type: "message",
            message: { type: "domain_event", event },
          }),
        ),
      );
    case "offers_replaced":
      return decodeJson(OfferCatalogReplacement, frame.data).pipe(
        Effect.map((catalog) =>
          Option.some<DeploymentSignal>({
            type: "message",
            message: { type: "offers_replaced", catalog },
          }),
        ),
      );
    case "offers_unavailable":
      return Effect.succeed(
        Option.some<DeploymentSignal>({
          type: "message",
          message: { type: "offers_unavailable" },
        }),
      );
    case "ready":
      return decodeJson(Ready, frame.data).pipe(
        Effect.map((ready) =>
          Option.some<DeploymentSignal>({
            type: "message",
            message: {
              type: "ready",
              throughGlobalPosition: ready.through_global_position,
            },
          }),
        ),
      );
    case "reset":
      return decodeJson(DashboardReset, frame.data).pipe(
        Effect.map((reset) =>
          Option.some<DeploymentSignal>({
            type: "reset",
            messages: reset.messages.map(deploymentMessage),
            playback: playbackSnapshot(reset.playback),
            fidelity: scenarioFidelity(reset.fidelity),
          }),
        ),
      );
    case "message":
      return decodeJson(DashboardMessage, frame.data).pipe(
        Effect.map((message) =>
          Option.some<DeploymentSignal>({
            type: "message",
            message: deploymentMessage(message),
          }),
        ),
      );
    case "playback":
      return decodeJson(DashboardPlayback, frame.data).pipe(
        Effect.map((playback) =>
          Option.some<DeploymentSignal>({
            type: "playback",
            playback: playbackSnapshot(playback),
          }),
        ),
      );
    default:
      return Effect.succeed(Option.none());
  }
}

function responseError(status: number) {
  return new DeploymentFeedError({
    status,
    message: `Deployment event feed failed with HTTP ${status}.`,
    retryable: ![400, 401, 403, 501].includes(status),
  });
}

function disconnected() {
  return new DeploymentFeedError({
    status: 0,
    message: "Deployment event feed disconnected.",
    retryable: true,
  });
}

function liveConnection(
  token: string | null,
  scenario: ReturnType<typeof activeScenario>,
) {
  return Stream.unwrap(
    Effect.gen(function* () {
      const response = yield* HttpClient.execute(
        feedRequest(token, scenario),
      ).pipe(
        Effect.mapError(
          (cause) =>
            new DeploymentFeedError({
              status: 0,
              message: "Deployment event feed could not connect.",
              retryable: true,
              cause,
            }),
        ),
      );
      if (response.status < 200 || response.status >= 300) {
        return yield* responseError(response.status);
      }
      const messages = response.stream.pipe(
        Stream.mapError(
          (cause) =>
            new DeploymentFeedError({
              status: 0,
              message: "Deployment event feed failed while reading.",
              retryable: true,
              cause,
            }),
        ),
        Stream.decodeText,
        Stream.pipeThroughChannel(Sse.decode()),
        Stream.mapError((cause) =>
          cause instanceof DeploymentFeedError
            ? cause
            : new DeploymentFeedError({
                status: 0,
                message: "Deployment event feed contained invalid SSE framing.",
                retryable: true,
                cause,
              }),
        ),
        Stream.mapEffect(decodeFrame),
        Stream.flatMap((message) =>
          Option.match(message, {
            onNone: () => Stream.empty,
            onSome: Stream.succeed,
          }),
        ),
      );
      return Stream.succeed<DeploymentSignal>({ type: "connecting" }).pipe(
        Stream.concat(messages),
        Stream.concat(Stream.fail(disconnected())),
      );
    }),
  );
}

function activeScenario() {
  if (process.env.NODE_ENV === "production" || typeof window === "undefined") {
    return null;
  }
  const search = new URLSearchParams(window.location.search);
  const name = search.get("scenario");
  const play = search.get("play");
  return name === null
    ? null
    : { name, autoplay: play === "1" || play === '"1"' };
}

export const layer = Layer.effect(
  DeploymentEvents,
  Effect.gen(function* () {
    const session = yield* Session;
    const client = yield* HttpClient.HttpClient;

    const stream = () =>
      Stream.unwrap(
        Effect.gen(function* () {
          const scenario = activeScenario();
          const state = yield* session.current;
          return liveConnection(state.token, scenario).pipe(
            Stream.retry(reconnectSchedule),
            Stream.provideService(HttpClient.HttpClient, client),
          );
        }),
      );

    return DeploymentEvents.of({ stream });
  }),
);
