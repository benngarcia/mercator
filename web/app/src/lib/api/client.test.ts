import { afterEach, effect, expect } from "@effect/vitest";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";

import { layer as sessionLayer } from "../session";
import * as endpoints from "./endpoints";
import { layer as apiLayer } from "./client";

const originalFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = originalFetch;
});

const testApiLayer = apiLayer.pipe(Layer.provide(sessionLayer));

effect("uses the explicit Workspace from the OpenAPI query", () =>
  Effect.gen(function* () {
    const requests: Request[] = [];
    globalThis.fetch = Object.assign(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push(
          input instanceof Request ? input : new Request(input, init),
        );
        return Response.json({ runs: [] });
      },
      { preconnect: originalFetch.preconnect },
    );

    yield* endpoints.listRuns({ workspaceId: "ws_explicit" });

    const request = requests[0];
    if (request === undefined) {
      throw new Error("Expected the API to issue a request");
    }
    expect(new URL(request.url).searchParams.get("workspace_id")).toBe(
      "ws_explicit",
    );
  }).pipe(Effect.provide(testApiLayer)),
);

effect("omits Workspace scope from the Workspace catalog", () =>
  Effect.gen(function* () {
    const requests: Request[] = [];
    globalThis.fetch = Object.assign(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push(
          input instanceof Request ? input : new Request(input, init),
        );
        return Response.json({ workspaces: [] });
      },
      { preconnect: originalFetch.preconnect },
    );

    yield* endpoints.listWorkspaces(false);

    const request = requests[0];
    if (request === undefined) {
      throw new Error("Expected the API to issue a request");
    }
    expect(new URL(request.url).searchParams.has("workspace_id")).toBe(false);
  }).pipe(Effect.provide(testApiLayer)),
);

effect("decodes the local browser session at the auth boundary", () =>
  Effect.gen(function* () {
    // Arrange
    globalThis.fetch = Object.assign(
      async () =>
        Response.json({
          mode: "local",
          enabled: true,
          email: "developer@localhost",
        }),
      { preconnect: originalFetch.preconnect },
    );

    // Act
    const session = yield* endpoints.getAuthSession();

    // Assert
    expect(session).toEqual({
      mode: "local",
      enabled: true,
      email: "developer@localhost",
    });
  }).pipe(Effect.provide(testApiLayer)),
);
