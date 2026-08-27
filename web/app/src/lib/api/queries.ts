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

import {
  adaptersAtom,
  authSessionAtom,
  authorizeConnectionAtom,
  cancelRunAtom,
  connectionsAtom,
  createConnectionAtom,
  createRunAtom,
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

const inactiveRunAtom = inactiveResource<Run>();
const inactiveRunEventsAtom = inactiveResource<CloudEvent[]>();
const inactiveRunDecisionAtom = inactiveResource<BookingDecision | null>();
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

export function useAuthSession(): ResourceResult<AuthSessionState> {
  return useResource(authSessionAtom);
}

export function useLogout(): MutationResult<void, void> {
  return useMutation(logoutAtom, () => undefined);
}

export function useRuns(): ResourceResult<Run[]> {
  return useResource(runsAtom);
}

export function useRun(runId: string | undefined): ResourceResult<Run> {
  const enabled = runId !== undefined;
  return useResource(enabled ? runAtom(runId) : inactiveRunAtom, enabled);
}

export function useRunEvents(runId: string | undefined): ResourceResult<CloudEvent[]> {
  const enabled = runId !== undefined;
  return useResource(enabled ? runEventsAtom(runId) : inactiveRunEventsAtom, enabled);
}

export function useRunDecision(runId: string | undefined): ResourceResult<BookingDecision | null> {
  const enabled = runId !== undefined;
  return useResource(enabled ? runDecisionAtom(runId) : inactiveRunDecisionAtom, enabled);
}

export function useOffers(): ResourceResult<OfferSnapshot[]> {
  return useResource(offersAtom);
}

export function useConnections(): ResourceResult<ConnectionRecord[]> {
  return useResource(connectionsAtom);
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

export function useCreateRun(): MutationResult<RunResponse, CreateRunRequest> {
  return useMutation(createRunAtom, (body) => ({ body }));
}

export function useCancelRun(): MutationResult<RunResponse, string> {
  return useMutation(cancelRunAtom, (runId) => ({ runId }));
}

export function useRefreshRun(): MutationResult<RunResponse, string> {
  return useMutation(refreshRunAtom, (runId) => ({ runId }));
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

export function useCreateConnection(): MutationResult<ConnectionRecord, CreateConnectionRequest> {
  return useMutation(createConnectionAtom, (body) => ({ body }));
}

export function useDeleteConnection(): MutationResult<void, string> {
  return useMutation(deleteConnectionAtom, (connectionId) => ({
    connectionId,
  }));
}

export function useAuthorizeConnection(): MutationResult<ConnectionRecord, string> {
  return useMutation(authorizeConnectionAtom, (connectionId) => ({
    connectionId,
  }));
}
