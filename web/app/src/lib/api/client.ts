import * as Context from "effect/Context";
import * as Data from "effect/Data";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Schema from "effect/Schema";
import createClient from "openapi-fetch";

import type { paths } from "./contract.gen";
import { AuthSessionState, type ErrorEnvelope, type Violation } from "./types";
import { Session } from "../session";

export class ApiError extends Data.TaggedError("ApiError")<{
  readonly status: number;
  readonly code: string;
  readonly message: string;
  readonly details?: Violation[];
  readonly cause?: unknown;
}> {
  get serviceDisabled(): boolean {
    return this.status === 501;
  }

  get unauthorized(): boolean {
    return this.status === 401;
  }

  get notFound(): boolean {
    return this.status === 404;
  }
}

type ApiResult<T> = {
  readonly data?: T;
  readonly error?: ErrorEnvelope;
  readonly response: Response;
};

function transportError(operation: string, cause: unknown): ApiError {
  return new ApiError({
    status: 0,
    code: "NETWORK_ERROR",
    message: `${operation} could not reach the Mercator API.`,
    cause,
  });
}

function responseError(
  operation: string,
  response: Response,
  envelope: ErrorEnvelope | undefined,
): ApiError {
  return new ApiError({
    status: response.status,
    code: envelope?.code || `HTTP_${response.status}`,
    message:
      envelope?.message ||
      response.statusText ||
      `${operation} failed with HTTP ${response.status}.`,
    details: envelope?.details,
  });
}

function decodeResult<T>(
  operation: string,
  result: ApiResult<T>,
): Effect.Effect<T, ApiError> {
  if (result.data !== undefined) {
    return Effect.succeed(result.data);
  }
  return Effect.fail(responseError(operation, result.response, result.error));
}

function request<T>(
  operation: string,
  run: (signal: AbortSignal) => Promise<ApiResult<T>>,
): Effect.Effect<T, ApiError> {
  return Effect.tryPromise({
    try: run,
    catch: (cause) => transportError(operation, cause),
  }).pipe(Effect.flatMap((result) => decodeResult(operation, result)));
}

function requestHeaders(token: string | null): HeadersInit {
  return token === null ? {} : { Authorization: `Bearer ${token}` };
}

function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

export interface ApiService {
  readonly getAuthSession: () => Effect.Effect<AuthSessionState, ApiError>;
  readonly logout: () => Effect.Effect<void, ApiError>;
  readonly client: ReturnType<typeof createClient<paths>>;
  readonly headers: Effect.Effect<HeadersInit>;
  readonly request: <T>(
    operation: string,
    run: (signal: AbortSignal) => Promise<ApiResult<T>>,
  ) => Effect.Effect<T, ApiError>;
  readonly idempotencyKey: Effect.Effect<string>;
}

export class Api extends Context.Service<Api, ApiService>()("@mercator/Api") {}

export const layer = Layer.effect(
  Api,
  Effect.gen(function* () {
    const session = yield* Session;
    const baseUrl =
      typeof window === "undefined"
        ? "http://localhost"
        : window.location.origin;
    const client = createClient<paths>({
      baseUrl,
      credentials: "same-origin",
      fetch: (request: Request) => globalThis.fetch(request),
    });
    const headers = session.current.pipe(
      Effect.map((state) => requestHeaders(state.token)),
    );

    const getAuthSession = Effect.fn("Api.getAuthSession")(function* () {
      const response = yield* Effect.tryPromise({
        try: (signal) =>
          fetch("/auth/session", {
            credentials: "same-origin",
            headers: { Accept: "application/json" },
            signal,
          }),
        catch: (cause) => transportError("Api.getAuthSession", cause),
      });
      if (!response.ok) {
        return yield* Effect.fail(
          responseError("Api.getAuthSession", response, undefined),
        );
      }
      const json = yield* Effect.tryPromise({
        try: () => response.json(),
        catch: (cause) => transportError("Api.getAuthSession.decode", cause),
      });
      const auth = yield* Schema.decodeUnknownEffect(AuthSessionState)(json).pipe(
        Effect.mapError(
          (cause) =>
            new ApiError({
              status: response.status,
              code: "INVALID_AUTH_SESSION",
              message: "The authentication session response was invalid.",
              cause,
            }),
        ),
      );
      const browserSession = yield* session.current;
      if (auth.mode !== "token" && browserSession.token !== null) {
        yield* session.setToken(null);
      }
      return auth;
    });

    const logout = Effect.fn("Api.logout")(function* () {
      const response = yield* Effect.tryPromise({
        try: (signal) =>
          globalThis.fetch("/auth/logout", {
            method: "POST",
            redirect: "manual",
            signal,
          }),
        catch: (cause) => transportError("Api.logout", cause),
      });
      if (response.status >= 400) {
        return yield* Effect.fail(
          responseError("Api.logout", response, undefined),
        );
      }
    });

    return Api.of({
      client,
      headers,
      request,
      getAuthSession,
      logout,
      idempotencyKey: Effect.sync(newIdempotencyKey),
    });
  }),
);
