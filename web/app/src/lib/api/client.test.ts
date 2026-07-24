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
    // The projection owns its page size; the console does not restate it.
    expect(new URL(request.url).searchParams.get("limit")).toBeNull();
  }).pipe(Effect.provide(testApiLayer)),
);

effect("reads every page of Runs so the newest are not hidden", () =>
  Effect.gen(function* () {
    const cursors: (string | null)[] = [];
    globalThis.fetch = Object.assign(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const request =
          input instanceof Request ? input : new Request(input, init);
        const cursor = new URL(request.url).searchParams.get("cursor");
        cursors.push(cursor);
        if (cursor === null) {
          return Response.json({
            runs: [{ id: "run_oldest" }],
            next_cursor: "run_oldest",
          });
        }
        return Response.json({ runs: [{ id: "run_newest" }] });
      },
      { preconnect: originalFetch.preconnect },
    );

    const runs = yield* endpoints.listAllRuns({ workspaceId: "ws_paged" });

    expect(cursors).toEqual([null, "run_oldest"]);
    expect(runs.map((run) => run.id)).toEqual(["run_oldest", "run_newest"]);
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
