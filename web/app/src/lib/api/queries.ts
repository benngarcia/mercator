// TanStack Query hooks for every endpoint in the design spec, with the
// documented polling cadences. Conventions:
//   - workspace_id is sourced from the session unless explicitly passed; hooks
//     stay disabled until a workspace is known.
//   - query errors throw ApiError -> consumed by <ErrorState>.
//   - 404 is treated as null where the spec says so (run decision).
//   - 501 surfaces via ApiError.serviceDisabled for <ServiceDisabled>.
//   - mutations invalidate the precise keys they affect.

import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";

import { ApiError } from "./client";
import * as endpoints from "./endpoints";
import { queryKeys } from "./keys";
import type {
  ConnectionRecord,
  CreateConnectionRequest,
  CreateRevisionRequest,
  CreateRunRequest,
  CreateWorkloadRequest,
  CreateWorkloadResponse,
  CloudEvent,
  OfferSnapshot,
  PlacementDecision,
  PlacementPreviewRequest,
  ReplaySinkRequest,
  ResolveImageRequest,
  ResolvedImage,
  Run,
  RunResponse,
  SinkResult,
  SinkStatus,
  WorkloadRevision,
} from "./types";
import { getWorkspace } from "../session";
import { useSession } from "@/hooks/useSession";
import { POLL, runRefetchInterval } from "@/hooks/usePollInterval";

// useWorkspaceId resolves the active workspace: an explicit override wins,
// otherwise the session default. Returns null when none is set, which callers
// use to disable the query.
function useWorkspaceId(override?: string): string | null {
  const { workspace } = useSession();
  return override ?? workspace ?? null;
}

// Common retry policy: never retry auth (401), forbidden (403), missing (404)
// or disabled-service (501) responses — they are not transient. Other errors
// get a single retry.
function defaultRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError) {
    if ([401, 403, 404, 501].includes(error.status)) {
      return false;
    }
  }
  return failureCount < 1;
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

export interface HealthState {
  live: boolean;
  ready: boolean;
}

export function useHealth(): UseQueryResult<HealthState, ApiError> {
  return useQuery<HealthState, ApiError>({
    queryKey: queryKeys.health(),
    queryFn: async ({ signal }) => {
      const [live, ready] = await Promise.allSettled([
        endpoints.getHealthLive(signal),
        endpoints.getHealthReady(signal),
      ]);
      return {
        live: live.status === "fulfilled",
        ready: ready.status === "fulfilled",
      };
    },
    refetchInterval: POLL.offers,
    retry: false,
  });
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

export function useRuns(
  workspaceOverride?: string,
): UseQueryResult<Run[], ApiError> {
  const workspaceID = useWorkspaceId(workspaceOverride);
  return useQuery<Run[], ApiError>({
    queryKey: queryKeys.runs(workspaceID ?? ""),
    queryFn: async ({ signal }) => {
      const res = await endpoints.listRuns({ workspaceId: workspaceID ?? undefined, signal });
      return res.runs;
    },
    enabled: Boolean(workspaceID),
    refetchInterval: POLL.runs,
    retry: defaultRetry,
  });
}

export function useRun(
  runID: string | undefined,
  workspaceOverride?: string,
): UseQueryResult<Run, ApiError> {
  const workspaceID = useWorkspaceId(workspaceOverride);
  return useQuery<Run, ApiError>({
    queryKey: queryKeys.run(workspaceID ?? "", runID ?? ""),
    queryFn: async ({ signal }) => {
      const res = await endpoints.getRun(runID as string, {
        workspaceId: workspaceID ?? undefined,
        signal,
      });
      return res.run;
    },
    enabled: Boolean(workspaceID) && Boolean(runID),
    // Poll every 2s while the run is open; stop once closed.
    refetchInterval: (query) => runRefetchInterval(query.state.data, POLL.run),
    retry: defaultRetry,
  });
}

export function useRunEvents(
  runID: string | undefined,
  options?: { run?: Run | null; workspaceId?: string },
): UseQueryResult<CloudEvent[], ApiError> {
  const workspaceID = useWorkspaceId(options?.workspaceId);
  return useQuery<CloudEvent[], ApiError>({
    queryKey: queryKeys.runEvents(workspaceID ?? "", runID ?? ""),
    queryFn: async ({ signal }) => {
      const res = await endpoints.getRunEvents(runID as string, {
        workspaceId: workspaceID ?? undefined,
        signal,
      });
      return res.events;
    },
    enabled: Boolean(workspaceID) && Boolean(runID),
    // Poll alongside the run: stop once the (passed-in) run is closed.
    refetchInterval: () => runRefetchInterval(options?.run, POLL.events),
    retry: defaultRetry,
  });
}

// useRunDecision fetches the placement decision once / on demand. A 404 means
// the decision has not been recorded yet and resolves to null rather than an
// error (so the panel can show a "no decision yet" state).
export function useRunDecision(
  runID: string | undefined,
  workspaceOverride?: string,
): UseQueryResult<PlacementDecision | null, ApiError> {
  const workspaceID = useWorkspaceId(workspaceOverride);
  return useQuery<PlacementDecision | null, ApiError>({
    queryKey: queryKeys.runDecision(workspaceID ?? "", runID ?? ""),
    queryFn: async ({ signal }) => {
      try {
        const res = await endpoints.getRunDecision(runID as string, {
          workspaceId: workspaceID ?? undefined,
          signal,
        });
        return res.decision;
      } catch (error) {
        if (error instanceof ApiError && error.notFound) {
          return null;
        }
        throw error;
      }
    },
    enabled: Boolean(workspaceID) && Boolean(runID),
    retry: defaultRetry,
  });
}

// ---------------------------------------------------------------------------
// Offers & connections
// ---------------------------------------------------------------------------

export function useOffers(
  workspaceOverride?: string,
): UseQueryResult<OfferSnapshot[], ApiError> {
  const workspaceID = useWorkspaceId(workspaceOverride);
  return useQuery<OfferSnapshot[], ApiError>({
    queryKey: queryKeys.offers(workspaceID ?? ""),
    queryFn: async ({ signal }) => {
      const res = await endpoints.listOffers({ workspaceId: workspaceID ?? undefined, signal });
      return res.offers;
    },
    enabled: Boolean(workspaceID),
    refetchInterval: POLL.offers,
    retry: defaultRetry,
  });
}

export function useConnections(
  workspaceOverride?: string,
): UseQueryResult<ConnectionRecord[], ApiError> {
  const workspaceID = useWorkspaceId(workspaceOverride);
  return useQuery<ConnectionRecord[], ApiError>({
    queryKey: queryKeys.connections(workspaceID ?? ""),
    queryFn: async ({ signal }) => {
      const res = await endpoints.listConnections({
        workspaceId: workspaceID ?? undefined,
        signal,
      });
      return res.connections;
    },
    enabled: Boolean(workspaceID),
    refetchInterval: POLL.connections,
    retry: defaultRetry,
  });
}

// ---------------------------------------------------------------------------
// Workloads & revisions (501 -> ApiError.serviceDisabled)
// ---------------------------------------------------------------------------

export function useWorkloadRevisions(
  workloadID: string | undefined,
  workspaceOverride?: string,
): UseQueryResult<WorkloadRevision[], ApiError> {
  const workspaceID = useWorkspaceId(workspaceOverride);
  return useQuery<WorkloadRevision[], ApiError>({
    queryKey: queryKeys.workloadRevisions(workspaceID ?? "", workloadID ?? ""),
    queryFn: async ({ signal }) => {
      const res = await endpoints.listWorkloadRevisions(workloadID as string, {
        workspaceId: workspaceID ?? undefined,
        signal,
      });
      return res.revisions;
    },
    enabled: Boolean(workspaceID) && Boolean(workloadID),
    retry: defaultRetry,
  });
}

export function useRevision(
  workloadID: string | undefined,
  revisionID: string | undefined,
  workspaceOverride?: string,
): UseQueryResult<WorkloadRevision, ApiError> {
  const workspaceID = useWorkspaceId(workspaceOverride);
  return useQuery<WorkloadRevision, ApiError>({
    queryKey: queryKeys.revision(
      workspaceID ?? "",
      workloadID ?? "",
      revisionID ?? "",
    ),
    queryFn: async ({ signal }) => {
      const res = await endpoints.getWorkloadRevision(
        workloadID as string,
        revisionID as string,
        { workspaceId: workspaceID ?? undefined, signal },
      );
      return res.revision;
    },
    enabled:
      Boolean(workspaceID) && Boolean(workloadID) && Boolean(revisionID),
    retry: defaultRetry,
  });
}

// ---------------------------------------------------------------------------
// Sinks (501 -> ApiError.serviceDisabled)
// ---------------------------------------------------------------------------

export function useSinkStatus(
  sinkID: string | undefined,
): UseQueryResult<SinkStatus, ApiError> {
  return useQuery<SinkStatus, ApiError>({
    queryKey: queryKeys.sinkStatus(sinkID ?? ""),
    queryFn: ({ signal }) => endpoints.getSinkStatus(sinkID as string, signal),
    enabled: Boolean(sinkID),
    retry: defaultRetry,
  });
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

// useCreateRun returns the created run's id (for navigation) on success, and
// invalidates the runs list + seeds the run detail cache.
export function useCreateRun(
  workspaceOverride?: string,
): UseMutationResult<RunResponse, ApiError, CreateRunRequest> {
  const queryClient = useQueryClient();
  const { workspace } = useSession();
  const workspaceID = workspaceOverride ?? workspace ?? undefined;
  return useMutation<RunResponse, ApiError, CreateRunRequest>({
    mutationFn: (body) => endpoints.createRun(body, { workspaceId: workspaceID }),
    onSuccess: (res) => {
      const ws = workspaceID ?? getWorkspace() ?? "";
      void queryClient.invalidateQueries({ queryKey: queryKeys.runs(ws) });
      queryClient.setQueryData(queryKeys.run(ws, res.run_id), res.run);
    },
  });
}

export function useCancelRun(
  workspaceOverride?: string,
): UseMutationResult<RunResponse, ApiError, string> {
  const queryClient = useQueryClient();
  const { workspace } = useSession();
  const workspaceID = workspaceOverride ?? workspace ?? undefined;
  return useMutation<RunResponse, ApiError, string>({
    mutationFn: (runID) => endpoints.cancelRun(runID, { workspaceId: workspaceID }),
    onSuccess: (res) => {
      const ws = workspaceID ?? getWorkspace() ?? "";
      queryClient.setQueryData(queryKeys.run(ws, res.run_id), res.run);
      void queryClient.invalidateQueries({ queryKey: queryKeys.runs(ws) });
    },
  });
}

export function useRefreshRun(
  workspaceOverride?: string,
): UseMutationResult<RunResponse, ApiError, string> {
  const queryClient = useQueryClient();
  const { workspace } = useSession();
  const workspaceID = workspaceOverride ?? workspace ?? undefined;
  return useMutation<RunResponse, ApiError, string>({
    mutationFn: (runID) => endpoints.refreshRun(runID, { workspaceId: workspaceID }),
    onSuccess: (res) => {
      const ws = workspaceID ?? getWorkspace() ?? "";
      queryClient.setQueryData(queryKeys.run(ws, res.run_id), res.run);
      void queryClient.invalidateQueries({ queryKey: queryKeys.runs(ws) });
    },
  });
}

export function useCreateWorkload(
  workspaceOverride?: string,
): UseMutationResult<CreateWorkloadResponse, ApiError, CreateWorkloadRequest> {
  const queryClient = useQueryClient();
  const { workspace } = useSession();
  const workspaceID = workspaceOverride ?? workspace ?? undefined;
  return useMutation<CreateWorkloadResponse, ApiError, CreateWorkloadRequest>({
    mutationFn: (body) =>
      endpoints.createWorkload(body, { workspaceId: workspaceID }),
    onSuccess: (res) => {
      const ws = workspaceID ?? getWorkspace() ?? "";
      void queryClient.invalidateQueries({
        queryKey: queryKeys.workloadRevisions(ws, res.workload_id),
      });
    },
  });
}

export interface CreateRevisionVariables {
  workloadID: string;
  body: CreateRevisionRequest;
}

export function useCreateRevision(
  workspaceOverride?: string,
): UseMutationResult<WorkloadRevision, ApiError, CreateRevisionVariables> {
  const queryClient = useQueryClient();
  const { workspace } = useSession();
  const workspaceID = workspaceOverride ?? workspace ?? undefined;
  return useMutation<WorkloadRevision, ApiError, CreateRevisionVariables>({
    mutationFn: async ({ workloadID, body }) => {
      const res = await endpoints.createRevision(workloadID, body, {
        workspaceId: workspaceID,
      });
      return res.revision;
    },
    onSuccess: (_revision, { workloadID }) => {
      const ws = workspaceID ?? getWorkspace() ?? "";
      void queryClient.invalidateQueries({
        queryKey: queryKeys.workloadRevisions(ws, workloadID),
      });
    },
  });
}

export function usePreviewPlacement(
  workspaceOverride?: string,
): UseMutationResult<PlacementDecision, ApiError, PlacementPreviewRequest> {
  const { workspace } = useSession();
  const workspaceID = workspaceOverride ?? workspace ?? undefined;
  return useMutation<PlacementDecision, ApiError, PlacementPreviewRequest>({
    mutationFn: async (body) => {
      const res = await endpoints.previewPlacement(body, {
        workspaceId: workspaceID,
      });
      return res.decision;
    },
  });
}

export function useResolveImage(): UseMutationResult<
  ResolvedImage,
  ApiError,
  ResolveImageRequest
> {
  return useMutation<ResolvedImage, ApiError, ResolveImageRequest>({
    mutationFn: async (body) => {
      const res = await endpoints.resolveImage(body);
      return res.image;
    },
  });
}

export function useDeliverSink(): UseMutationResult<SinkResult, ApiError, string> {
  const queryClient = useQueryClient();
  return useMutation<SinkResult, ApiError, string>({
    mutationFn: (sinkID) => endpoints.deliverSink(sinkID),
    onSuccess: (res) => {
      void queryClient.invalidateQueries({
        queryKey: queryKeys.sinkStatus(res.sink_id),
      });
    },
  });
}

export interface ReplaySinkVariables {
  sinkID: string;
  body: ReplaySinkRequest;
}

export function useReplaySink(): UseMutationResult<
  SinkResult,
  ApiError,
  ReplaySinkVariables
> {
  const queryClient = useQueryClient();
  return useMutation<SinkResult, ApiError, ReplaySinkVariables>({
    mutationFn: ({ sinkID, body }) => endpoints.replaySink(sinkID, body),
    onSuccess: (res) => {
      void queryClient.invalidateQueries({
        queryKey: queryKeys.sinkStatus(res.sink_id),
      });
    },
  });
}

export function useCreateConnection(
  workspaceOverride?: string,
): UseMutationResult<ConnectionRecord, ApiError, CreateConnectionRequest> {
  const queryClient = useQueryClient();
  const { workspace } = useSession();
  const workspaceID = workspaceOverride ?? workspace ?? undefined;
  return useMutation<ConnectionRecord, ApiError, CreateConnectionRequest>({
    mutationFn: async (body) => {
      const res = await endpoints.createConnection(body, {
        workspaceId: workspaceID,
      });
      return res.connection;
    },
    onSuccess: () => {
      const ws = workspaceID ?? getWorkspace() ?? "";
      void queryClient.invalidateQueries({
        queryKey: queryKeys.connections(ws),
      });
      void queryClient.invalidateQueries({
        queryKey: queryKeys.offers(ws),
      });
    },
  });
}

export function useAuthorizeConnection(
  workspaceOverride?: string,
): UseMutationResult<ConnectionRecord, ApiError, string> {
  const queryClient = useQueryClient();
  const { workspace } = useSession();
  const workspaceID = workspaceOverride ?? workspace ?? undefined;
  return useMutation<ConnectionRecord, ApiError, string>({
    mutationFn: async (connectionId) => {
      const res = await endpoints.authorizeConnection(connectionId, {
        workspaceId: workspaceID,
      });
      return res.connection;
    },
    onSuccess: () => {
      const ws = workspaceID ?? getWorkspace() ?? "";
      void queryClient.invalidateQueries({
        queryKey: queryKeys.connections(ws),
      });
      void queryClient.invalidateQueries({
        queryKey: queryKeys.offers(ws),
      });
    },
  });
}
