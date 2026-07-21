// One typed function per route in the API table. These are thin wrappers over
// apiFetch; auth + workspace_id are injected centrally by the client. Mutating
// endpoints accept an optional idempotencyKey (auto-generated otherwise).

import { apiFetch, type WorkspaceScope } from "./client";
import type { operations } from "./contract.gen";
import type {
  AdapterListResponse,
  AuthSessionState,
  ConnectionListResponse,
  ConnectionResponse,
  CreateConnectionRequest,
  DeleteConnectionResponse,
  CreateRevisionRequest,
  CreateRunRequest,
  CreateWorkloadRequest,
  CreateWorkloadResponse,
  EventListResponse,
  OfferListResponse,
  PlacementDecisionResponse,
  PlacementPreviewRequest,
  PlacementPreviewResponse,
  ReplaySinkRequest,
  ResolveImageRequest,
  ResolveImageResponse,
  RunListResponse,
  RunResponse,
  SinkResult,
  SinkStatus,
  WorkloadRevisionListResponse,
  WorkloadRevisionResponse,
  CreateWorkspaceRequest,
  WorkspaceListResponse,
  WorkspaceResponse,
} from "./types";

interface WorkspaceArg {
  workspaceId?: string;
  signal?: AbortSignal;
}

function requestWorkspaceScope(workspaceId: string | undefined): WorkspaceScope {
  return workspaceId === undefined ? "session" : { workspaceId };
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

export type HealthStatus =
  operations["healthLive"]["responses"][200]["content"]["application/json"];

export function getHealthLive(signal?: AbortSignal): Promise<HealthStatus> {
  return apiFetch<HealthStatus>("/health/live", { signal });
}

export function getHealthReady(signal?: AbortSignal): Promise<HealthStatus> {
  return apiFetch<HealthStatus>("/health/ready", { signal });
}

// ---------------------------------------------------------------------------
// Auth session
// ---------------------------------------------------------------------------

export function getAuthSession(
  signal?: AbortSignal,
): Promise<AuthSessionState> {
  return apiFetch<AuthSessionState>("/auth/session", { signal });
}

// ---------------------------------------------------------------------------
// Workspaces
// ---------------------------------------------------------------------------

export function listWorkspaces(
  includeArchived: boolean,
  signal?: AbortSignal,
): Promise<WorkspaceListResponse> {
  return apiFetch<WorkspaceListResponse>("/v1/workspaces", {
    workspaceScope: "none",
    searchParams: { include_archived: includeArchived },
    signal,
  });
}

export function createWorkspace(
  body: CreateWorkspaceRequest,
): Promise<WorkspaceResponse> {
  return apiFetch<WorkspaceResponse>("/v1/workspaces", {
    method: "POST",
    body,
    workspaceScope: "none",
  });
}

export function archiveWorkspace(workspaceID: string): Promise<WorkspaceResponse> {
  return apiFetch<WorkspaceResponse>(
    `/v1/workspaces/${encodeURIComponent(workspaceID)}/archive`,
    { method: "POST", workspaceScope: "none" },
  );
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

export function listRuns(arg: WorkspaceArg = {}): Promise<RunListResponse> {
  return apiFetch<RunListResponse>("/v1/runs", {
    workspaceScope: requestWorkspaceScope(arg.workspaceId),
    signal: arg.signal,
  });
}

export function getRun(
  runID: string,
  arg: WorkspaceArg = {},
): Promise<RunResponse> {
  return apiFetch<RunResponse>(`/v1/runs/${encodeURIComponent(runID)}`, {
    workspaceScope: requestWorkspaceScope(arg.workspaceId),
    signal: arg.signal,
  });
}

export function getRunEvents(
  runID: string,
  arg: WorkspaceArg = {},
): Promise<EventListResponse> {
  return apiFetch<EventListResponse>(
    `/v1/runs/${encodeURIComponent(runID)}/events`,
    { workspaceScope: requestWorkspaceScope(arg.workspaceId), signal: arg.signal },
  );
}

export function getRunDecision(
  runID: string,
  arg: WorkspaceArg = {},
): Promise<PlacementDecisionResponse> {
  return apiFetch<PlacementDecisionResponse>(
    `/v1/runs/${encodeURIComponent(runID)}/decision`,
    { workspaceScope: requestWorkspaceScope(arg.workspaceId), signal: arg.signal },
  );
}

export function createRun(
  body: CreateRunRequest,
  opts: { idempotencyKey?: string; workspaceId?: string } = {},
): Promise<RunResponse> {
  return apiFetch<RunResponse>("/v1/runs", {
    method: "POST",
    body,
    idempotencyKey: opts.idempotencyKey,
    workspaceScope: requestWorkspaceScope(opts.workspaceId),
  });
}

// Action endpoints use the `{run_id}:action` colon-verb convention.
export function cancelRun(
  runID: string,
  opts: { idempotencyKey?: string; workspaceId?: string } = {},
): Promise<RunResponse> {
  return apiFetch<RunResponse>(`/v1/runs/${encodeURIComponent(runID)}/cancel`, {
    method: "POST",
    idempotencyKey: opts.idempotencyKey,
    workspaceScope: requestWorkspaceScope(opts.workspaceId),
  });
}

export function refreshRun(
  runID: string,
  opts: { idempotencyKey?: string; workspaceId?: string } = {},
): Promise<RunResponse> {
  return apiFetch<RunResponse>(`/v1/runs/${encodeURIComponent(runID)}/refresh`, {
    method: "POST",
    idempotencyKey: opts.idempotencyKey,
    workspaceScope: requestWorkspaceScope(opts.workspaceId),
  });
}

// ---------------------------------------------------------------------------
// Placements
// ---------------------------------------------------------------------------

export function previewPlacement(
  body: PlacementPreviewRequest,
  opts: { idempotencyKey?: string; workspaceId?: string } = {},
): Promise<PlacementPreviewResponse> {
  return apiFetch<PlacementPreviewResponse>("/v1/placements:preview", {
    method: "POST",
    body,
    idempotencyKey: opts.idempotencyKey,
    workspaceScope: requestWorkspaceScope(opts.workspaceId),
  });
}

// ---------------------------------------------------------------------------
// Offers & connections
// ---------------------------------------------------------------------------

export function listOffers(arg: WorkspaceArg = {}): Promise<OfferListResponse> {
  return apiFetch<OfferListResponse>("/v1/offers", {
    workspaceScope: requestWorkspaceScope(arg.workspaceId),
    signal: arg.signal,
  });
}

export function listConnections(
  arg: WorkspaceArg = {},
): Promise<ConnectionListResponse> {
  return apiFetch<ConnectionListResponse>("/v1/connections", {
    workspaceScope: requestWorkspaceScope(arg.workspaceId),
    signal: arg.signal,
  });
}

export function createConnection(
  body: CreateConnectionRequest,
  opts: { idempotencyKey?: string; workspaceId?: string } = {},
): Promise<ConnectionResponse> {
  return apiFetch<ConnectionResponse>("/v1/connections", {
    method: "POST",
    body,
    idempotencyKey: opts.idempotencyKey,
    workspaceScope: requestWorkspaceScope(opts.workspaceId),
  });
}

export function authorizeConnection(
  connectionId: string,
  opts: { idempotencyKey?: string; workspaceId?: string } = {},
): Promise<ConnectionResponse> {
  return apiFetch<ConnectionResponse>(
    `/v1/connections/${encodeURIComponent(connectionId)}/authorize`,
    {
      method: "POST",
      idempotencyKey: opts.idempotencyKey,
      workspaceScope: requestWorkspaceScope(opts.workspaceId),
    },
  );
}

export function deleteConnection(
  connectionId: string,
  opts: { workspaceId?: string } = {},
): Promise<DeleteConnectionResponse> {
  return apiFetch<DeleteConnectionResponse>(
    `/v1/connections/${encodeURIComponent(connectionId)}`,
    {
      method: "DELETE",
      workspaceScope: requestWorkspaceScope(opts.workspaceId),
    },
  );
}

// ---------------------------------------------------------------------------
// Adapter manifests
// ---------------------------------------------------------------------------

// The manifest list is static per server process and not workspace-scoped.
export function listAdapters(signal?: AbortSignal): Promise<AdapterListResponse> {
  return apiFetch<AdapterListResponse>("/v1/adapters", { signal });
}

// ---------------------------------------------------------------------------
// Workloads & revisions (501 when service disabled)
// ---------------------------------------------------------------------------

export function listWorkloadRevisions(
  workloadID: string,
  arg: WorkspaceArg = {},
): Promise<WorkloadRevisionListResponse> {
  return apiFetch<WorkloadRevisionListResponse>(
    `/v1/workloads/${encodeURIComponent(workloadID)}/revisions`,
    { workspaceScope: requestWorkspaceScope(arg.workspaceId), signal: arg.signal },
  );
}

export function getWorkloadRevision(
  workloadID: string,
  revisionID: string,
  arg: WorkspaceArg = {},
): Promise<WorkloadRevisionResponse> {
  return apiFetch<WorkloadRevisionResponse>(
    `/v1/workloads/${encodeURIComponent(workloadID)}/revisions/${encodeURIComponent(revisionID)}`,
    { workspaceScope: requestWorkspaceScope(arg.workspaceId), signal: arg.signal },
  );
}

export function createWorkload(
  body: CreateWorkloadRequest,
  opts: { idempotencyKey?: string; workspaceId?: string } = {},
): Promise<CreateWorkloadResponse> {
  return apiFetch<CreateWorkloadResponse>("/v1/workloads", {
    method: "POST",
    body,
    idempotencyKey: opts.idempotencyKey,
    workspaceScope: requestWorkspaceScope(opts.workspaceId),
  });
}

export function createRevision(
  workloadID: string,
  body: CreateRevisionRequest,
  opts: { idempotencyKey?: string; workspaceId?: string } = {},
): Promise<WorkloadRevisionResponse> {
  return apiFetch<WorkloadRevisionResponse>(
    `/v1/workloads/${encodeURIComponent(workloadID)}/revisions`,
    {
      method: "POST",
      body,
      idempotencyKey: opts.idempotencyKey,
      workspaceScope: requestWorkspaceScope(opts.workspaceId),
    },
  );
}

// ---------------------------------------------------------------------------
// Image resolver (501 when disabled)
// ---------------------------------------------------------------------------

export function resolveImage(
  body: ResolveImageRequest,
  opts: { idempotencyKey?: string } = {},
): Promise<ResolveImageResponse> {
  return apiFetch<ResolveImageResponse>("/v1/images:resolve", {
    method: "POST",
    body,
    idempotencyKey: opts.idempotencyKey,
  });
}

// ---------------------------------------------------------------------------
// Sinks (501 when disabled)
// ---------------------------------------------------------------------------

export function getSinkStatus(
  sinkID: string,
  signal?: AbortSignal,
): Promise<SinkStatus> {
  return apiFetch<SinkStatus>(`/v1/sinks/${encodeURIComponent(sinkID)}`, {
    signal,
  });
}

export function deliverSink(
  sinkID: string,
  opts: { idempotencyKey?: string } = {},
): Promise<SinkResult> {
  return apiFetch<SinkResult>(
    `/v1/sinks/${encodeURIComponent(sinkID)}/deliver`,
    { method: "POST", idempotencyKey: opts.idempotencyKey },
  );
}

export function replaySink(
  sinkID: string,
  body: ReplaySinkRequest,
  opts: { idempotencyKey?: string } = {},
): Promise<SinkResult> {
  return apiFetch<SinkResult>(
    `/v1/sinks/${encodeURIComponent(sinkID)}/replay`,
    { method: "POST", body, idempotencyKey: opts.idempotencyKey },
  );
}
