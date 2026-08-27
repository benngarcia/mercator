import * as Effect from "effect/Effect";
import * as Option from "effect/Option";
import * as AsyncResult from "effect/unstable/reactivity/AsyncResult";
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
} from "./types";

export const resourceKey = {
  adapters: "adapters",
  authSession: "auth-session",
  connections: "connections",
  offers: "offers",
  run: (runId: string) => `run:${runId}`,
  runDecision: (runId: string) => `run-decision:${runId}`,
  runEvents: (runId: string) => `run-events:${runId}`,
  runs: "runs",
  sinkStatus: (sinkId: string) => `sink-status:${sinkId}`,
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

export const runsAtom = resource(resourceKey.runs, endpoints.listAllRuns());

export const runAtom = Atom.family((runId: string) =>
  resource(
    resourceKey.run(runId),
    endpoints.getRun(runId).pipe(Effect.map((response) => response.run)),
  ),
);

export const runEventsAtom = Atom.family((runId: string) =>
  resource(
    resourceKey.runEvents(runId),
    endpoints
      .getRunEvents(runId)
      .pipe(Effect.map((response) => response.events)),
  ),
);

// runDecisionsAtom reads the whole chain of decisions recorded for one Run,
// oldest first. A decision is appended and never rewritten, so the answer that
// stands is the last entry and the ones before it are what it replaced.
export const runDecisionsAtom = Atom.family((runId: string) =>
  resource(
    resourceKey.runDecision(runId),
    endpoints.getRunDecision(runId).pipe(
      Effect.map((response) => response.decisions),
      Effect.catchIf(
        (error) => error.notFound,
        () => Effect.succeed(null),
      ),
    ),
  ),
);

export const offersAtom = resource(
  resourceKey.offers,
  endpoints.listOffers().pipe(Effect.map((response) => response.offers)),
);

export const connectionsAtom = resource(
  resourceKey.connections,
  endpoints
    .listConnections()
    .pipe(Effect.map((response) => response.connections)),
);

export const adaptersAtom = resource<AdapterManifest[]>(
  resourceKey.adapters,
  endpoints.listAdapters().pipe(Effect.map((response) => response.adapters)),
);

export const sinkStatusAtom = Atom.family((sinkId: string) =>
  resource(resourceKey.sinkStatus(sinkId), endpoints.getSinkStatus(sinkId)),
);

interface CreateRunVariables {
  readonly body: CreateRunRequest;
}

export const createRunAtom = runtime.fn<CreateRunVariables>()(
  Effect.fn("Run.create")(function* ({ body }) {
    const response = yield* endpoints.createRun(body);
    yield* invalidate(resourceKey.runs, resourceKey.run(response.run_id));
    return response;
  }),
);

interface RunActionVariables {
  readonly runId: string;
}

function invalidateRun(response: RunResponse) {
  return invalidate(resourceKey.runs, resourceKey.run(response.run_id));
}

export const cancelRunAtom = runtime.fn<RunActionVariables>()(
  Effect.fn("Run.cancel")(function* ({ runId }) {
    const response = yield* endpoints.cancelRun(runId);
    yield* invalidateRun(response);
    return response;
  }),
);

export const refreshRunAtom = runtime.fn<RunActionVariables>()(
  Effect.fn("Run.refresh")(function* ({ runId }) {
    const response = yield* endpoints.refreshRun(runId);
    yield* invalidateRun(response);
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
  readonly body: CreateConnectionRequest;
}

function invalidateConnections() {
  return invalidate(resourceKey.connections, resourceKey.offers);
}

export const createConnectionAtom = runtime.fn<ConnectionMutationVariables>()(
  Effect.fn("Connection.create")(function* ({ body }) {
    const response = yield* endpoints.createConnection(body);
    yield* invalidateConnections();
    return response.connection;
  }),
);

interface ConnectionActionVariables {
  readonly connectionId: string;
}

export const deleteConnectionAtom = runtime.fn<ConnectionActionVariables>()(
  Effect.fn("Connection.delete")(function* ({ connectionId }) {
    yield* endpoints.deleteConnection(connectionId);
    yield* invalidateConnections();
  }),
);

export const authorizeConnectionAtom = runtime.fn<ConnectionActionVariables>()(
  Effect.fn("Connection.authorize")(function* ({ connectionId }) {
    const response = yield* endpoints.authorizeConnection(connectionId);
    yield* invalidateConnections();
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
};

// deploymentDecisionsAtom is every placed Run's decision chain in one atom, so a
// view that needs all of them reads once instead of calling a hook per Run.
//
// It exists because a hook cannot be called in a loop over a list whose length
// changes between renders, and because this app forbids direct React effects
// (test/architecture/no-direct-react-effects), so the usual workaround of
// lifting each child's result into parent state through an effect is not
// available and would be the wrong shape anyway. An atom that reads other atoms
// is the shape the reactivity layer already has: it re-derives when any Run's
// decision changes and nothing synchronises anything.
//
// The key is the run list itself because the family caches per key and a
// deployment whose Runs changed is a different question.
export const deploymentDecisionsAtom = Atom.family((key: string) => {
  const runIds = JSON.parse(key) as readonly string[];
  return Atom.make((get) =>
    runIds.map((runId) => ({
      runId,
      decisions: Option.getOrUndefined(
        AsyncResult.value(get.get(runDecisionsAtom(runId))),
      ),
    })),
  );
});

// deploymentDecisionsKey builds that key. Run IDs are sorted so two renders of the
// same set ask the same question.
export const deploymentDecisionsKey = (runIds: readonly string[]): string =>
  JSON.stringify([...runIds].sort());
