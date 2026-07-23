import {
  RegistryContext,
  useAtomRefresh,
  useAtomValue,
} from "@effect/atom-react";
import { useCallback, useContext } from "react";
import * as Effect from "effect/Effect";
import * as Option from "effect/Option";
import * as AsyncResult from "effect/unstable/reactivity/AsyncResult";
import * as Atom from "effect/unstable/reactivity/Atom";
import * as AtomRegistry from "effect/unstable/reactivity/AtomRegistry";

import { useSession } from "@/hooks/useSession";
import {
  adaptersAtom,
  archiveWorkspaceAtom,
  authSessionAtom,
  authorizeConnectionAtom,
  cancelRunAtom,
  connectionsAtom,
  createConnectionAtom,
  createRunAtom,
  createWorkspaceAtom,
  deleteConnectionAtom,
  deliverSinkAtom,
  logoutAtom,
  offersAtom,
  refreshRunAtom,
  replaySinkAtom,
  resolveImageAtom,
  runAtom,
  runDecisionAtom,
  runEventsAtom,
  runsAtom,
  sinkStatusAtom,
  workspacesAtom,
} from "./atoms";
import { ApiError } from "./client";
import type {
  AdapterManifest,
  AuthSessionState,
  BookingDecision,
  CloudEvent,
  ConnectionRecord,
  CreateConnectionRequest,
  CreateRunRequest,
  OfferSnapshot,
  ReplaySinkRequest,
  ResolvedImage,
  ResolveImageRequest,
  Run,
  RunResponse,
  SinkResult,
  SinkStatus,
  Workspace,
} from "./types";

interface ResourceResultBase<A> {
  readonly data: A | undefined;
  readonly isFetching: boolean;
  readonly isLoading: boolean;
  readonly refetch: () => void;
}

export type ResourceResult<A> =
  | (ResourceResultBase<A> & {
      readonly error: ApiError;
      readonly isError: true;
    })
  | (ResourceResultBase<A> & {
      readonly error: null;
      readonly isError: false;
    });

export interface MutationOptions<A> {
  readonly onSuccess?: (data: A) => void;
  readonly onError?: (error: ApiError) => void;
  readonly onSettled?: () => void;
}

export interface MutationResult<A, Variables> {
  readonly data: A | undefined;
  readonly error: ApiError | null;
  readonly isPending: boolean;
  readonly mutate: (variables: Variables, options?: MutationOptions<A>) => void;
  readonly mutateAsync: (variables: Variables) => Promise<A>;
  readonly reset: () => void;
}

function inactiveResource<A>() {
  return Atom.make(AsyncResult.initial<A, ApiError>());
}

const inactiveRunsAtom = inactiveResource<Run[]>();
const inactiveRunAtom = inactiveResource<Run>();
const inactiveRunEventsAtom = inactiveResource<CloudEvent[]>();
const inactiveRunDecisionAtom = inactiveResource<BookingDecision | null>();
const inactiveOffersAtom = inactiveResource<OfferSnapshot[]>();
const inactiveConnectionsAtom = inactiveResource<ConnectionRecord[]>();
const inactiveSinkStatusAtom = inactiveResource<SinkStatus>();

function resultError<A>(result: AsyncResult.AsyncResult<A, ApiError>) {
  return Option.getOrElse(
    AsyncResult.error(result),
    () =>
      new ApiError({
        status: 0,
        code: "EFFECT_FAILURE",
        message: "The operation failed outside its typed error channel.",
      }),
  );
}

function useResource<A>(
  atom: Atom.Atom<AsyncResult.AsyncResult<A, ApiError>>,
  enabled = true,
): ResourceResult<A> {
  const result = useAtomValue(atom);
  const refetch = useAtomRefresh(atom);
  const data = Option.getOrUndefined(AsyncResult.value(result));

  const base = {
    data,
    isFetching: enabled && result.waiting,
    isLoading: enabled && data === undefined && result.waiting,
    refetch,
  };
  if (enabled && AsyncResult.isFailure(result)) {
    return { ...base, error: resultError(result), isError: true };
  }
  return { ...base, error: null, isError: false };
}

function useMutation<A, Input, Variables>(
  atom: Atom.AtomResultFn<Input, A, ApiError>,
  mapVariables: (variables: Variables) => Input,
): MutationResult<A, Variables> {
  const registry = useContext(RegistryContext);
  const result = useAtomValue(atom);

  const mutateAsync = useCallback(
    (variables: Variables) => {
      registry.set(atom, mapVariables(variables));
      return Effect.runPromise(
        AtomRegistry.getResult(registry, atom, { suspendOnWaiting: true }),
      );
    },
    [atom, mapVariables, registry],
  );

  const mutate = useCallback(
    (variables: Variables, options?: MutationOptions<A>) => {
      void mutateAsync(variables)
        .then((data) => options?.onSuccess?.(data))
        .catch((error: unknown) => {
          if (error instanceof ApiError) {
            options?.onError?.(error);
            return;
          }
          queueMicrotask(() => {
            throw error;
          });
        })
        .finally(() => options?.onSettled?.());
    },
    [mutateAsync],
  );

  const reset = useCallback(
    () => registry.set(atom, Atom.Reset),
    [atom, registry],
  );

  return {
    data: Option.getOrUndefined(AsyncResult.value(result)),
    error: AsyncResult.isFailure(result) ? resultError(result) : null,
    isPending: result.waiting,
    mutate,
    mutateAsync,
    reset,
  };
}

function useWorkspaceId(override?: string): string | null {
  const { workspace } = useSession();
  return override ?? workspace;
}

export function useAuthSession(): ResourceResult<AuthSessionState> {
  return useResource(authSessionAtom);
}

export function useLogout(): MutationResult<void, void> {
  return useMutation(logoutAtom, () => undefined);
}

export function useWorkspaces(
  includeArchived = false,
): ResourceResult<Workspace[]> {
  return useResource(workspacesAtom(includeArchived));
}

export function useCreateWorkspace(): MutationResult<Workspace, string> {
  return useMutation(createWorkspaceAtom, (displayName) => displayName);
}

export function useArchiveWorkspace(): MutationResult<Workspace, string> {
  return useMutation(archiveWorkspaceAtom, (workspaceId) => workspaceId);
}

export function useRuns(workspaceOverride?: string): ResourceResult<Run[]> {
  const workspaceId = useWorkspaceId(workspaceOverride);
  return useResource(
    workspaceId === null ? inactiveRunsAtom : runsAtom(workspaceId),
    workspaceId !== null,
  );
}

export function useRun(
  runId: string | undefined,
  workspaceOverride?: string,
): ResourceResult<Run> {
  const workspaceId = useWorkspaceId(workspaceOverride);
  const enabled = workspaceId !== null && runId !== undefined;
  return useResource(
    enabled ? runAtom(workspaceId, runId) : inactiveRunAtom,
    enabled,
  );
}

export function useRunEvents(
  runId: string | undefined,
  workspaceOverride?: string,
): ResourceResult<CloudEvent[]> {
  const workspaceId = useWorkspaceId(workspaceOverride);
  const enabled = workspaceId !== null && runId !== undefined;
  return useResource(
    enabled ? runEventsAtom(workspaceId, runId) : inactiveRunEventsAtom,
    enabled,
  );
}

export function useRunDecision(
  runId: string | undefined,
  workspaceOverride?: string,
): ResourceResult<BookingDecision | null> {
  const workspaceId = useWorkspaceId(workspaceOverride);
  const enabled = workspaceId !== null && runId !== undefined;
  return useResource(
    enabled ? runDecisionAtom(workspaceId, runId) : inactiveRunDecisionAtom,
    enabled,
  );
}

export function useOffers(
  workspaceOverride?: string,
): ResourceResult<OfferSnapshot[]> {
  const workspaceId = useWorkspaceId(workspaceOverride);
  return useResource(
    workspaceId === null ? inactiveOffersAtom : offersAtom(workspaceId),
    workspaceId !== null,
  );
}

export function useConnections(
  workspaceOverride?: string,
): ResourceResult<ConnectionRecord[]> {
  const workspaceId = useWorkspaceId(workspaceOverride);
  return useResource(
    workspaceId === null
      ? inactiveConnectionsAtom
      : connectionsAtom(workspaceId),
    workspaceId !== null,
  );
}

export function useAdapters(): ResourceResult<AdapterManifest[]> {
  return useResource(adaptersAtom);
}

export function useSinkStatus(
  sinkId: string | undefined,
): ResourceResult<SinkStatus> {
  return useResource(
    sinkId === undefined ? inactiveSinkStatusAtom : sinkStatusAtom(sinkId),
    sinkId !== undefined,
  );
}

export function useCreateRun(
  workspaceOverride?: string,
): MutationResult<RunResponse, CreateRunRequest> {
  const workspaceId = useWorkspaceId(workspaceOverride) ?? undefined;
  return useMutation(createRunAtom, (body) => ({ body, workspaceId }));
}

export function useCancelRun(
  workspaceOverride?: string,
): MutationResult<RunResponse, string> {
  const workspaceId = useWorkspaceId(workspaceOverride) ?? undefined;
  return useMutation(cancelRunAtom, (runId) => ({ runId, workspaceId }));
}

export function useRefreshRun(
  workspaceOverride?: string,
): MutationResult<RunResponse, string> {
  const workspaceId = useWorkspaceId(workspaceOverride) ?? undefined;
  return useMutation(refreshRunAtom, (runId) => ({ runId, workspaceId }));
}

export function useResolveImage(): MutationResult<
  ResolvedImage,
  ResolveImageRequest
> {
  return useMutation(resolveImageAtom, (body) => body);
}

export function useDeliverSink(): MutationResult<SinkResult, string> {
  return useMutation(deliverSinkAtom, (sinkId) => sinkId);
}

export interface ReplaySinkVariables {
  readonly sinkID: string;
  readonly body: ReplaySinkRequest;
}

export function useReplaySink(): MutationResult<
  SinkResult,
  ReplaySinkVariables
> {
  return useMutation(replaySinkAtom, (variables) => variables);
}

export function useCreateConnection(
  workspaceOverride?: string,
): MutationResult<ConnectionRecord, CreateConnectionRequest> {
  const workspaceId = useWorkspaceId(workspaceOverride) ?? undefined;
  return useMutation(createConnectionAtom, (body) => ({ body, workspaceId }));
}

export function useDeleteConnection(
  workspaceOverride?: string,
): MutationResult<void, string> {
  const workspaceId = useWorkspaceId(workspaceOverride) ?? undefined;
  return useMutation(deleteConnectionAtom, (connectionId) => ({
    connectionId,
    workspaceId,
  }));
}

export function useAuthorizeConnection(
  workspaceOverride?: string,
): MutationResult<ConnectionRecord, string> {
  const workspaceId = useWorkspaceId(workspaceOverride) ?? undefined;
  return useMutation(authorizeConnectionAtom, (connectionId) => ({
    connectionId,
    workspaceId,
  }));
}
