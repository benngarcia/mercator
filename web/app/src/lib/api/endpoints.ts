import * as Effect from "effect/Effect";

import { Api } from "./client";
import type {
  CreateConnectionRequest,
  CreateRevisionRequest,
  CreateRunRequest,
  CreateWorkloadRequest,
  PlacementPreviewRequest,
  ReplaySinkRequest,
  ResolveImageRequest,
} from "./types";

interface WorkspaceArg {
  readonly workspaceId?: string;
}

interface RunPageArg extends WorkspaceArg {
  readonly cursor?: string;
  readonly limit?: number;
}

interface MutationArg extends WorkspaceArg {
  readonly idempotencyKey?: string;
}

const mutationKey = Effect.fn("Api.mutationKey")(function* (
  override: string | undefined,
) {
  if (override !== undefined) {
    return override;
  }
  const api = yield* Api;
  return yield* api.idempotencyKey;
});

export const getAuthSession = Effect.fn("Api.getAuthSession")(function* () {
  const api = yield* Api;
  return yield* api.getAuthSession();
});

export const logout = Effect.fn("Api.logout")(function* () {
  const api = yield* Api;
  yield* api.logout();
});

export const listWorkspaces = Effect.fn("Api.listWorkspaces")(function* (
  includeArchived: boolean,
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.listWorkspaces", (signal) =>
    api.client.GET("/v1/workspaces", {
      headers,
      params: { query: { include_archived: includeArchived } },
      signal,
    }),
  );
});

export const createWorkspace = Effect.fn("Api.createWorkspace")(function* (
  displayName: string,
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.createWorkspace", (signal) =>
    api.client.POST("/v1/workspaces", {
      body: { display_name: displayName },
      headers,
      signal,
    }),
  );
});

export const archiveWorkspace = Effect.fn("Api.archiveWorkspace")(function* (
  workspaceId: string,
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.archiveWorkspace", (signal) =>
    api.client.POST("/v1/workspaces/{workspace_id}/archive", {
      headers,
      params: { path: { workspace_id: workspaceId } },
      signal,
    }),
  );
});

export const listRuns = Effect.fn("Api.listRuns")(function* (
  arg: RunPageArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.listRuns", (signal) =>
    api.client.GET("/v1/runs", {
      headers,
      params: {
        query: {
          workspace_id: arg.workspaceId,
          cursor: arg.cursor,
          limit: arg.limit ?? 100,
        },
      },
      signal,
    }),
  );
});

export const getRun = Effect.fn("Api.getRun")(function* (
  runId: string,
  arg: WorkspaceArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.getRun", (signal) =>
    api.client.GET("/v1/runs/{run_id}", {
      headers,
      params: {
        path: { run_id: runId },
        query: { workspace_id: arg.workspaceId },
      },
      signal,
    }),
  );
});

export const getRunEvents = Effect.fn("Api.getRunEvents")(function* (
  runId: string,
  arg: WorkspaceArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.getRunEvents", (signal) =>
    api.client.GET("/v1/runs/{run_id}/events", {
      headers,
      params: {
        path: { run_id: runId },
        query: { workspace_id: arg.workspaceId },
      },
      signal,
    }),
  );
});

export const getRunDecision = Effect.fn("Api.getRunDecision")(function* (
  runId: string,
  arg: WorkspaceArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.getRunDecision", (signal) =>
    api.client.GET("/v1/runs/{run_id}/decision", {
      headers,
      params: {
        path: { run_id: runId },
        query: { workspace_id: arg.workspaceId },
      },
      signal,
    }),
  );
});

export const createRun = Effect.fn("Api.createRun")(function* (
  body: CreateRunRequest,
  arg: MutationArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  const idempotencyKey = yield* mutationKey(arg.idempotencyKey);
  return yield* api.request("Api.createRun", (signal) =>
    api.client.POST("/v1/runs", {
      body,
      headers,
      params: {
        query: { workspace_id: arg.workspaceId },
        header: { "Idempotency-Key": idempotencyKey },
      },
      signal,
    }),
  );
});

export const cancelRun = Effect.fn("Api.cancelRun")(function* (
  runId: string,
  arg: WorkspaceArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.cancelRun", (signal) =>
    api.client.POST("/v1/runs/{run_id}/cancel", {
      headers,
      params: {
        path: { run_id: runId },
        query: { workspace_id: arg.workspaceId },
      },
      signal,
    }),
  );
});

export const refreshRun = Effect.fn("Api.refreshRun")(function* (
  runId: string,
  arg: WorkspaceArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.refreshRun", (signal) =>
    api.client.POST("/v1/runs/{run_id}/refresh", {
      headers,
      params: {
        path: { run_id: runId },
        query: { workspace_id: arg.workspaceId },
      },
      signal,
    }),
  );
});

export const previewPlacement = Effect.fn("Api.previewPlacement")(function* (
  body: PlacementPreviewRequest,
  arg: WorkspaceArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.previewPlacement", (signal) =>
    api.client.POST("/v1/placements:preview", {
      body,
      headers,
      params: { query: { workspace_id: arg.workspaceId } },
      signal,
    }),
  );
});

export const listOffers = Effect.fn("Api.listOffers")(function* (
  arg: WorkspaceArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.listOffers", (signal) =>
    api.client.GET("/v1/offers", {
      headers,
      params: { query: { workspace_id: arg.workspaceId } },
      signal,
    }),
  );
});

export const listConnections = Effect.fn("Api.listConnections")(function* (
  arg: WorkspaceArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.listConnections", (signal) =>
    api.client.GET("/v1/connections", {
      headers,
      params: { query: { workspace_id: arg.workspaceId } },
      signal,
    }),
  );
});

export const createConnection = Effect.fn("Api.createConnection")(function* (
  body: CreateConnectionRequest,
  arg: MutationArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  const idempotencyKey = yield* mutationKey(arg.idempotencyKey);
  return yield* api.request("Api.createConnection", (signal) =>
    api.client.POST("/v1/connections", {
      body,
      headers,
      params: {
        query: { workspace_id: arg.workspaceId },
        header: { "Idempotency-Key": idempotencyKey },
      },
      signal,
    }),
  );
});

export const authorizeConnection = Effect.fn("Api.authorizeConnection")(
  function* (connectionId: string, arg: WorkspaceArg = {}) {
    const api = yield* Api;
    const headers = yield* api.headers;
    return yield* api.request("Api.authorizeConnection", (signal) =>
      api.client.POST("/v1/connections/{connection_id}/authorize", {
        headers,
        params: {
          path: { connection_id: connectionId },
          query: { workspace_id: arg.workspaceId },
        },
        signal,
      }),
    );
  },
);

export const deleteConnection = Effect.fn("Api.deleteConnection")(function* (
  connectionId: string,
  arg: WorkspaceArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.deleteConnection", (signal) =>
    api.client.DELETE("/v1/connections/{connection_id}", {
      headers,
      params: {
        path: { connection_id: connectionId },
        query: { workspace_id: arg.workspaceId },
      },
      signal,
    }),
  );
});

export const listAdapters = Effect.fn("Api.listAdapters")(function* () {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.listAdapters", (signal) =>
    api.client.GET("/v1/adapters", { headers, signal }),
  );
});

export const listWorkloadRevisions = Effect.fn("Api.listWorkloadRevisions")(
  function* (workloadId: string, arg: WorkspaceArg = {}) {
    const api = yield* Api;
    const headers = yield* api.headers;
    return yield* api.request("Api.listWorkloadRevisions", (signal) =>
      api.client.GET("/v1/workloads/{workload_id}/revisions", {
        headers,
        params: {
          path: { workload_id: workloadId },
          query: { workspace_id: arg.workspaceId },
        },
        signal,
      }),
    );
  },
);

export const getWorkloadRevision = Effect.fn("Api.getWorkloadRevision")(
  function* (workloadId: string, revisionId: string, arg: WorkspaceArg = {}) {
    const api = yield* Api;
    const headers = yield* api.headers;
    return yield* api.request("Api.getWorkloadRevision", (signal) =>
      api.client.GET("/v1/workloads/{workload_id}/revisions/{revision_id}", {
        headers,
        params: {
          path: {
            workload_id: workloadId,
            revision_id: revisionId,
          },
          query: { workspace_id: arg.workspaceId },
        },
        signal,
      }),
    );
  },
);

export const createWorkload = Effect.fn("Api.createWorkload")(function* (
  body: CreateWorkloadRequest,
  arg: Pick<MutationArg, "idempotencyKey"> = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  const idempotencyKey = yield* mutationKey(arg.idempotencyKey);
  return yield* api.request("Api.createWorkload", (signal) =>
    api.client.POST("/v1/workloads", {
      body,
      headers,
      params: { header: { "Idempotency-Key": idempotencyKey } },
      signal,
    }),
  );
});

export const createRevision = Effect.fn("Api.createRevision")(function* (
  workloadId: string,
  body: CreateRevisionRequest,
  arg: MutationArg = {},
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  const idempotencyKey = yield* mutationKey(arg.idempotencyKey);
  return yield* api.request("Api.createRevision", (signal) =>
    api.client.POST("/v1/workloads/{workload_id}/revisions", {
      body,
      headers,
      params: {
        path: { workload_id: workloadId },
        query: { workspace_id: arg.workspaceId },
        header: { "Idempotency-Key": idempotencyKey },
      },
      signal,
    }),
  );
});

export const resolveImage = Effect.fn("Api.resolveImage")(function* (
  body: ResolveImageRequest,
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.resolveImage", (signal) =>
    api.client.POST("/v1/images:resolve", {
      body,
      headers,
      signal,
    }),
  );
});

export const getSinkStatus = Effect.fn("Api.getSinkStatus")(function* (
  sinkId: string,
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.getSinkStatus", (signal) =>
    api.client.GET("/v1/sinks/{sink_id}", {
      headers,
      params: { path: { sink_id: sinkId } },
      signal,
    }),
  );
});

export const deliverSink = Effect.fn("Api.deliverSink")(function* (
  sinkId: string,
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.deliverSink", (signal) =>
    api.client.POST("/v1/sinks/{sink_id}/deliver", {
      headers,
      params: { path: { sink_id: sinkId } },
      signal,
    }),
  );
});

export interface ReplaySinkVariables {
  readonly sinkID: string;
  readonly body: ReplaySinkRequest;
}

export const replaySink = Effect.fn("Api.replaySink")(function* (
  variables: ReplaySinkVariables,
) {
  const api = yield* Api;
  const headers = yield* api.headers;
  return yield* api.request("Api.replaySink", (signal) =>
    api.client.POST("/v1/sinks/{sink_id}/replay", {
      body: variables.body,
      headers,
      params: { path: { sink_id: variables.sinkID } },
      signal,
    }),
  );
});
