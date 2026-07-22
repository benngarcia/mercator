import * as Context from "effect/Context";
import * as Data from "effect/Data";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Option from "effect/Option";
import * as Ref from "effect/Ref";
import * as Schedule from "effect/Schedule";
import * as Schema from "effect/Schema";
import * as Stream from "effect/Stream";
import * as HttpClient from "effect/unstable/http/HttpClient";
import * as HttpClientRequest from "effect/unstable/http/HttpClientRequest";
import * as Sse from "effect/unstable/encoding/Sse";

import { Session } from "@/lib/session";

import { CloudEvent, OfferCatalogReplacement, Ready } from "./contracts";
import {
  makeScenarioPlayback,
  type ScenarioPlaybackCommand,
  type ScenarioPlaybackController,
  type ScenarioPlaybackEmission,
} from "./playback";
import type { WorkspaceMessage } from "./reducer";

export type WorkspaceFeedStatus =
  | "idle"
  | "connecting"
  | "live"
  | "degraded"
  | "error";

export class WorkspaceFeedError extends Data.TaggedError("WorkspaceFeedError")<{
  readonly status: number;
  readonly message: string;
  readonly retryable: boolean;
  readonly cause?: unknown;
}> {}

export type WorkspaceSignal =
  | { readonly type: "connecting" }
  | ScenarioPlaybackEmission;

export interface WorkspaceEventsService {
  readonly stream: (
    workspaceId: string,
  ) => Stream.Stream<WorkspaceSignal, WorkspaceFeedError>;
  readonly command: (
    workspaceId: string,
    command: ScenarioPlaybackCommand,
  ) => Effect.Effect<void>;
}

export class WorkspaceEvents extends Context.Service<
  WorkspaceEvents,
  WorkspaceEventsService
>()("@mercator/WorkspaceEvents") {}

const reconnectSchedule = Schedule.spaced("1 second").pipe(
  Schedule.while(
    ({ input }) => input instanceof WorkspaceFeedError && input.retryable,
  ),
);

function feedRequest(
  workspaceId: string,
  token: string | null,
  lastEventId: string,
) {
  let request = HttpClientRequest.get("/v1/console/events").pipe(
    HttpClientRequest.accept("text/event-stream"),
    HttpClientRequest.setUrlParam("workspace_id", workspaceId),
  );
  if (token !== null) {
    request = HttpClientRequest.bearerToken(request, token);
  }
  if (lastEventId !== "") {
    request = HttpClientRequest.setHeader(
      request,
      "Last-Event-ID",
      lastEventId,
    );
  }
  return request;
}

function decodeFailure(message: string, cause: unknown) {
  return new WorkspaceFeedError({
    status: 0,
    message,
    retryable: false,
    cause,
  });
}

function decodeJson<S extends Schema.Constraint>(schema: S, data: string) {
  return Schema.decodeUnknownEffect(Schema.fromJsonString(schema))(data).pipe(
    Effect.mapError((cause) =>
      decodeFailure("The Workspace event feed sent an invalid payload.", cause),
    ),
  );
}

function decodeFrame(
  frame: Sse.Event,
): Effect.Effect<Option.Option<WorkspaceMessage>, WorkspaceFeedError> {
  switch (frame.event ?? "message") {
    case "domain_event":
      return decodeJson(CloudEvent, frame.data).pipe(
        Effect.map((event) =>
          Option.some<WorkspaceMessage>({ type: "domain_event", event }),
        ),
      );
    case "offers_replaced":
      return decodeJson(OfferCatalogReplacement, frame.data).pipe(
        Effect.map((catalog) =>
          Option.some<WorkspaceMessage>({ type: "offers_replaced", catalog }),
        ),
      );
    case "offers_unavailable":
      return Effect.succeed(
        Option.some<WorkspaceMessage>({ type: "offers_unavailable" }),
      );
    case "ready":
      return decodeJson(Ready, frame.data).pipe(
        Effect.map((ready) =>
          Option.some<WorkspaceMessage>({
            type: "ready",
            throughGlobalPosition: ready.through_global_position,
          }),
        ),
      );
    default:
      return Effect.succeed(Option.none());
  }
}

function responseError(status: number) {
  return new WorkspaceFeedError({
    status,
    message: `Workspace event feed failed with HTTP ${status}.`,
    retryable: ![400, 401, 403, 501].includes(status),
  });
}

function disconnected() {
  return new WorkspaceFeedError({
    status: 0,
    message: "Workspace event feed disconnected.",
    retryable: true,
  });
}

function liveConnection(
  workspaceId: string,
  token: string | null,
  lastEventId: Ref.Ref<string>,
) {
  return Stream.unwrap(
    Effect.gen(function* () {
      const currentLastEventId = yield* Ref.get(lastEventId);
      const response = yield* HttpClient.execute(
        feedRequest(workspaceId, token, currentLastEventId),
      ).pipe(
        Effect.mapError(
          (cause) =>
            new WorkspaceFeedError({
              status: 0,
              message: "Workspace event feed could not connect.",
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
            new WorkspaceFeedError({
              status: 0,
              message: "Workspace event feed failed while reading.",
              retryable: true,
              cause,
            }),
        ),
        Stream.decodeText,
        Stream.pipeThroughChannel(Sse.decode()),
        Stream.mapError((cause) =>
          cause instanceof WorkspaceFeedError
            ? cause
            : new WorkspaceFeedError({
                status: 0,
                message: "Workspace event feed contained invalid SSE framing.",
                retryable: true,
                cause,
              }),
        ),
        Stream.mapEffect((frame) =>
          Effect.gen(function* () {
            if (frame.id !== undefined && frame.id !== "") {
              yield* Ref.set(lastEventId, frame.id);
            }
            return yield* decodeFrame(frame);
          }),
        ),
        Stream.flatMap((message) =>
          Option.match(message, {
            onNone: () => Stream.empty,
            onSome: Stream.succeed,
          }),
        ),
        Stream.map(
          (message): WorkspaceSignal => ({ type: "message", message }),
        ),
      );
      return Stream.succeed<WorkspaceSignal>({ type: "connecting" }).pipe(
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
  WorkspaceEvents,
  Effect.gen(function* () {
    const session = yield* Session;
    const client = yield* HttpClient.HttpClient;
    const controllers = yield* Ref.make(
      new Map<string, ReadonlySet<ScenarioPlaybackController>>(),
    );

    const stream = (workspaceId: string) =>
      Stream.unwrap(
        Effect.gen(function* () {
          const scenario = activeScenario();
          if (
            process.env.NODE_ENV !== "production" &&
            scenario?.name === "full-schedule-forces-fresh-capacity"
          ) {
            const script = yield* Effect.tryPromise({
              try: () =>
                import("./scenario").then(({ fullScheduleScenarioScript }) =>
                  fullScheduleScenarioScript(workspaceId),
                ),
              catch: (cause) =>
                decodeFailure("The Workspace fixture could not load.", cause),
            });
            const controller = yield* makeScenarioPlayback(
              script,
              scenario.autoplay,
            );
            yield* Ref.update(controllers, (current) => {
              const next = new Map(current);
              const active = new Set(next.get(workspaceId) ?? []);
              active.add(controller);
              next.set(workspaceId, active);
              return next;
            });
            yield* Effect.addFinalizer(() =>
              Ref.update(controllers, (current) => {
                const next = new Map(current);
                const active = new Set(next.get(workspaceId) ?? []);
                active.delete(controller);
                if (active.size === 0) {
                  next.delete(workspaceId);
                } else {
                  next.set(workspaceId, active);
                }
                return next;
              }),
            );
            return controller.stream.pipe(
              Stream.prepend<WorkspaceSignal>([{ type: "connecting" }]),
            );
          }
          const state = yield* session.current;
          const lastEventId = yield* Ref.make("");
          return liveConnection(workspaceId, state.token, lastEventId).pipe(
            Stream.retry(reconnectSchedule),
            Stream.provideService(HttpClient.HttpClient, client),
          );
        }),
      );

    const command = Effect.fn("WorkspaceEvents.command")(function* (
      workspaceId: string,
      value: ScenarioPlaybackCommand,
    ) {
      const active = (yield* Ref.get(controllers)).get(workspaceId);
      if (active === undefined || active.size === 0) {
        return yield* Effect.die(
          new Error(`Workspace ${workspaceId} has no active scenario playback`),
        );
      }
      yield* Effect.forEach(
        active,
        (controller) => controller.command(value),
        { discard: true, concurrency: "unbounded" },
      );
    });

    return WorkspaceEvents.of({ stream, command });
  }),
);
