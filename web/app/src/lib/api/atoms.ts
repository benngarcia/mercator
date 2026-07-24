import * as Effect from "effect/Effect";
import * as Atom from "effect/unstable/reactivity/Atom";
import * as Reactivity from "effect/unstable/reactivity/Reactivity";

import { runtime } from "../runtime";
import { sessionAtom } from "../session-atoms";
import { Api, ApiError } from "./client";
import * as endpoints from "./endpoints";
import type {
  AdapterManifest,
  AuthSessionState,
  BookingDecision,
  CloudEvent,
  ConnectionRecord,
  CreateConnectionRequest,
  CreateRunRequest,
  OfferSnapshot,
  ResolvedImage,
  ResolveImageRequest,
  Run,
  RunResponse,
  SinkResult,
  SinkStatus,
  Workspace,
} from "./types";

export const resourceKey = {
  adapters: "adapters",
  authSession: "auth-session",
  connections: (workspaceId: string) => `connections:${workspaceId}`,
  offers: (workspaceId: string) => `offers:${workspaceId}`,
  run: (workspaceId: string, runId: string) => `run:${workspaceId}:${runId}`,
  runDecision: (workspaceId: string, runId: string) =>
    `run-decision:${workspaceId}:${runId}`,
  runEvents: (workspaceId: string, runId: string) =>
    `run-events:${workspaceId}:${runId}`,
  runs: (workspaceId: string) => `runs:${workspaceId}`,
  sinkStatus: (sinkId: string) => `sink-status:${sinkId}`,
  workspaces: (includeArchived: boolean) =>
    `workspaces:${includeArchived ? "all" : "active"}`,
} as const;

function isTransient(error: ApiError): boolean {
  return (
    error.status === 0 ||
    error.status === 408 ||
    error.status === 429 ||
    error.status >= 502
  );
}

function resource<A>(key: string, load: Effect.Effect<A, ApiError, Api>) {
  return runtime
    .atom((get) => {
      get(sessionAtom);
      return load.pipe(Effect.retry({ times: 1, while: isTransient }));
    })
    .pipe(Atom.setIdleTTL("5 seconds"), runtime.factory.withReactivity([key]));
}

const invalidate = (...keys: ReadonlyArray<string>) =>
  Effect.gen(function* () {
    const reactivity = yield* Reactivity.Reactivity;
    yield* reactivity.invalidate(keys);
  });

export const authSessionAtom = resource<AuthSessionState>(
  resourceKey.authSession,
  endpoints.getAuthSession(),
);

export const logoutAtom = runtime.fn<void>()(
  Effect.fn("Auth.logout")(function* () {
    yield* endpoints.logout();
  }),
);

export const workspacesAtom = Atom.family((includeArchived: boolean) =>
  resource(
    resourceKey.workspaces(includeArchived),
    endpoints
      .listWorkspaces(includeArchived)
      .pipe(Effect.map((response) => response.workspaces)),
  ),
);

export const runsAtom = Atom.family((workspaceId: string) =>
  resource(resourceKey.runs(workspaceId), endpoints.listAllRuns({ workspaceId })),
);

const runFamily = Atom.family((workspaceId: string) =>
  Atom.family((runId: string) =>
    resource(
      resourceKey.run(workspaceId, runId),
      endpoints
        .getRun(runId, { workspaceId })
        .pipe(Effect.map((response) => response.run)),
    ),
  ),
);

export const runAtom = (workspaceId: string, runId: string) =>
  runFamily(workspaceId)(runId);

const runEventsFamily = Atom.family((workspaceId: string) =>
  Atom.family((runId: string) =>
    resource(
      resourceKey.runEvents(workspaceId, runId),
      endpoints
        .getRunEvents(runId, { workspaceId })
        .pipe(Effect.map((response) => response.events)),
    ),
  ),
);

export const runEventsAtom = (workspaceId: string, runId: string) =>
  runEventsFamily(workspaceId)(runId);

const runDecisionFamily = Atom.family((workspaceId: string) =>
  Atom.family((runId: string) =>
    resource(
      resourceKey.runDecision(workspaceId, runId),
      endpoints.getRunDecision(runId, { workspaceId }).pipe(
        Effect.map((response) => response.decision),
        Effect.catchIf(
          (error) => error.notFound,
          () => Effect.succeed(null),
        ),
      ),
    ),
  ),
);

export const runDecisionAtom = (workspaceId: string, runId: string) =>
  runDecisionFamily(workspaceId)(runId);

export const offersAtom = Atom.family((workspaceId: string) =>
  resource(
    resourceKey.offers(workspaceId),
    endpoints
      .listOffers({ workspaceId })
      .pipe(Effect.map((response) => response.offers)),
  ),
);

export const connectionsAtom = Atom.family((workspaceId: string) =>
  resource(
    resourceKey.connections(workspaceId),
    endpoints
      .listConnections({ workspaceId })
      .pipe(Effect.map((response) => response.connections)),
  ),
);

export const adaptersAtom = resource<AdapterManifest[]>(
  resourceKey.adapters,
  endpoints.listAdapters().pipe(Effect.map((response) => response.adapters)),
);

export const sinkStatusAtom = Atom.family((sinkId: string) =>
  resource(resourceKey.sinkStatus(sinkId), endpoints.getSinkStatus(sinkId)),
);

export const createWorkspaceAtom = runtime.fn<string>()(
  Effect.fn("Workspace.create")(function* (displayName) {
    const response = yield* endpoints.createWorkspace(displayName);
    yield* invalidate(
      resourceKey.workspaces(false),
      resourceKey.workspaces(true),
    );
    return response.workspace;
  }),
);

export const archiveWorkspaceAtom = runtime.fn<string>()(
  Effect.fn("Workspace.archive")(function* (workspaceId) {
    const response = yield* endpoints.archiveWorkspace(workspaceId);
    yield* invalidate(
      resourceKey.workspaces(false),
      resourceKey.workspaces(true),
    );
    return response.workspace;
  }),
);

interface CreateRunVariables {
  readonly body: CreateRunRequest;
  readonly workspaceId?: string;
}

export const createRunAtom = runtime.fn<CreateRunVariables>()(
  Effect.fn("Run.create")(function* ({ body, workspaceId }) {
    const response = yield* endpoints.createRun(body, { workspaceId });
    if (workspaceId !== undefined) {
      yield* invalidate(
        resourceKey.runs(workspaceId),
        resourceKey.run(workspaceId, response.run_id),
      );
    }
    return response;
  }),
);

interface RunActionVariables {
  readonly runId: string;
  readonly workspaceId?: string;
}

function invalidateRun(response: RunResponse, workspaceId: string | undefined) {
  return workspaceId === undefined
    ? Effect.void
    : invalidate(
        resourceKey.runs(workspaceId),
        resourceKey.run(workspaceId, response.run_id),
      );
}

export const cancelRunAtom = runtime.fn<RunActionVariables>()(
  Effect.fn("Run.cancel")(function* ({ runId, workspaceId }) {
    const response = yield* endpoints.cancelRun(runId, { workspaceId });
    yield* invalidateRun(response, workspaceId);
    return response;
  }),
);

export const refreshRunAtom = runtime.fn<RunActionVariables>()(
  Effect.fn("Run.refresh")(function* ({ runId, workspaceId }) {
    const response = yield* endpoints.refreshRun(runId, { workspaceId });
    yield* invalidateRun(response, workspaceId);
    return response;
  }),
);

export const resolveImageAtom = runtime.fn<ResolveImageRequest>()(
  Effect.fn("Image.resolve")(function* (body) {
    const response = yield* endpoints.resolveImage(body);
    return response.image;
  }),
);

export const deliverSinkAtom = runtime.fn<string>()(
  Effect.fn("Sink.deliver")(function* (sinkId) {
    const result = yield* endpoints.deliverSink(sinkId);
    yield* invalidate(resourceKey.sinkStatus(result.sink_id));
    return result;
  }),
);

export const replaySinkAtom = runtime.fn<endpoints.ReplaySinkVariables>()(
  Effect.fn("Sink.replay")(function* (variables) {
    const result = yield* endpoints.replaySink(variables);
    yield* invalidate(resourceKey.sinkStatus(result.sink_id));
    return result;
  }),
);

interface ConnectionMutationVariables {
  readonly workspaceId?: string;
  readonly body: CreateConnectionRequest;
}

function invalidateConnections(workspaceId: string | undefined) {
  return workspaceId === undefined
    ? Effect.void
    : invalidate(
        resourceKey.connections(workspaceId),
        resourceKey.offers(workspaceId),
      );
}

export const createConnectionAtom = runtime.fn<ConnectionMutationVariables>()(
  Effect.fn("Connection.create")(function* ({ body, workspaceId }) {
    const response = yield* endpoints.createConnection(body, { workspaceId });
    yield* invalidateConnections(workspaceId);
    return response.connection;
  }),
);

interface ConnectionActionVariables {
  readonly connectionId: string;
  readonly workspaceId?: string;
}

export const deleteConnectionAtom = runtime.fn<ConnectionActionVariables>()(
  Effect.fn("Connection.delete")(function* ({ connectionId, workspaceId }) {
    yield* endpoints.deleteConnection(connectionId, { workspaceId });
    yield* invalidateConnections(workspaceId);
  }),
);

export const authorizeConnectionAtom = runtime.fn<ConnectionActionVariables>()(
  Effect.fn("Connection.authorize")(function* ({ connectionId, workspaceId }) {
    const response = yield* endpoints.authorizeConnection(connectionId, {
      workspaceId,
    });
    yield* invalidateConnections(workspaceId);
    return response.connection;
  }),
);

export type {
  BookingDecision,
  CloudEvent,
  ConnectionRecord,
  OfferSnapshot,
  ResolvedImage,
  Run,
  SinkResult,
  SinkStatus,
  Workspace,
};
