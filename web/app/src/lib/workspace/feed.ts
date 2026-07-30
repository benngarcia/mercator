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

import {
  CloudEvent,
  OfferCatalogReplacement,
  Ready,
} from "./contracts";
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
  | { readonly type: "message"; readonly message: WorkspaceMessage };

export interface WorkspaceEventsService {
  readonly stream: (
    workspaceId: string,
  ) => Stream.Stream<WorkspaceSignal, WorkspaceFeedError>;
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
): Effect.Effect<Option.Option<WorkspaceSignal>, WorkspaceFeedError> {
  switch (frame.event ?? "message") {
    case "domain_event":
      return decodeJson(CloudEvent, frame.data).pipe(
        Effect.map((event) =>
          Option.some<WorkspaceSignal>({
            type: "message",
            message: { type: "domain_event", event },
          }),
        ),
      );
    case "offers_replaced":
      return decodeJson(OfferCatalogReplacement, frame.data).pipe(
        Effect.map((catalog) =>
          Option.some<WorkspaceSignal>({
            type: "message",
            message: { type: "offers_replaced", catalog },
          }),
        ),
      );
    case "offers_unavailable":
      return Effect.succeed(
        Option.some<WorkspaceSignal>({
          type: "message",
          message: { type: "offers_unavailable" },
        }),
      );
    case "ready":
      return decodeJson(Ready, frame.data).pipe(
        Effect.map((ready) =>
          Option.some<WorkspaceSignal>({
            type: "message",
            message: {
              type: "ready",
              throughGlobalPosition: ready.through_global_position,
            },
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
      );
      return Stream.succeed<WorkspaceSignal>({ type: "connecting" }).pipe(
        Stream.concat(messages),
        Stream.concat(Stream.fail(disconnected())),
      );
    }),
  );
}

export const layer = Layer.effect(
  WorkspaceEvents,
  Effect.gen(function* () {
    const session = yield* Session;
    const client = yield* HttpClient.HttpClient;

    const stream = (workspaceId: string) =>
      Stream.unwrap(
        Effect.gen(function* () {
          const state = yield* session.current;
          const lastEventId = yield* Ref.make("");
          return liveConnection(workspaceId, state.token, lastEventId).pipe(
            Stream.retry(reconnectSchedule),
            Stream.provideService(HttpClient.HttpClient, client),
          );
        }),
      );

    return WorkspaceEvents.of({ stream });
  }),
);
