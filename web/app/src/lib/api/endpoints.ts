// One typed function per route in the API table. These are thin wrappers over
// apiFetch; auth + workspace_id are injected centrally by the client. Mutating
// endpoints accept an optional idempotencyKey (auto-generated otherwise).

import { apiFetch } from "./client";
import type {
  ConnectionListResponse,
  ConnectionResponse,
  CreateConnectionRequest,
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
} from "./types";

interface WorkspaceArg {
  workspaceId?: string;
  signal?: AbortSignal;
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

export interface HealthStatus {
  status: string;
}

export function getHealthLive(signal?: AbortSignal): Promise<HealthStatus> {
  return apiFetch<HealthStatus>("/health/live", { signal });
}

export function getHealthReady(signal?: AbortSignal): Promise<HealthStatus> {
  return apiFetch<HealthStatus>("/health/ready", { signal });
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

export function listRuns(arg: WorkspaceArg = {}): Promise<RunListResponse> {
  return apiFetch<RunListResponse>("/v1/runs", {
    workspaceId: arg.workspaceId,
    signal: arg.signal,
  });
}

export function getRun(
  runID: string,
  arg: WorkspaceArg = {},
): Promise<RunResponse> {
  return apiFetch<RunResponse>(`/v1/runs/${encodeURIComponent(runID)}`, {
    workspaceId: arg.workspaceId,
    signal: arg.signal,
  });
}

export function getRunEvents(
  runID: string,
  arg: WorkspaceArg = {},
): Promise<EventListResponse> {
  return apiFetch<EventListResponse>(
    `/v1/runs/${encodeURIComponent(runID)}/events`,
    { workspaceId: arg.workspaceId, signal: arg.signal },
  );
}

export function getRunDecision(
  runID: string,
  arg: WorkspaceArg = {},
): Promise<PlacementDecisionResponse> {
  return apiFetch<PlacementDecisionResponse>(
    `/v1/runs/${encodeURIComponent(runID)}/decision`,
    { workspaceId: arg.workspaceId, signal: arg.signal },
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
    workspaceId: opts.workspaceId,
  });
}

// Action endpoints use the `{run_id}:action` colon-verb convention.
export function cancelRun(
  runID: string,
  opts: { idempotencyKey?: string; workspaceId?: string } = {},
): Promise<RunResponse> {
  return apiFetch<RunResponse>(`/v1/runs/${encodeURIComponent(runID)}:cancel`, {
    method: "POST",
    idempotencyKey: opts.idempotencyKey,
    workspaceId: opts.workspaceId,
  });
}

export function refreshRun(
  runID: string,
  opts: { idempotencyKey?: string; workspaceId?: string } = {},
): Promise<RunResponse> {
  return apiFetch<RunResponse>(`/v1/runs/${encodeURIComponent(runID)}:refresh`, {
    method: "POST",
    idempotencyKey: opts.idempotencyKey,
    workspaceId: opts.workspaceId,
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
    workspaceId: opts.workspaceId,
  });
}

// ---------------------------------------------------------------------------
// Offers & connections
// ---------------------------------------------------------------------------

export function listOffers(arg: WorkspaceArg = {}): Promise<OfferListResponse> {
  return apiFetch<OfferListResponse>("/v1/offers", {
    workspaceId: arg.workspaceId,
    signal: arg.signal,
  });
}

export function listConnections(
  arg: WorkspaceArg = {},
): Promise<ConnectionListResponse> {
  return apiFetch<ConnectionListResponse>("/v1/connections", {
    workspaceId: arg.workspaceId,
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
    workspaceId: opts.workspaceId,
  });
}

export function authorizeConnection(
  connectionId: string,
  opts: { idempotencyKey?: string; workspaceId?: string } = {},
): Promise<ConnectionResponse> {
  return apiFetch<ConnectionResponse>(
    `/v1/connections/${encodeURIComponent(connectionId)}:authorize`,
    {
      method: "POST",
      idempotencyKey: opts.idempotencyKey,
      workspaceId: opts.workspaceId,
    },
  );
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
    { workspaceId: arg.workspaceId, signal: arg.signal },
  );
}

export function getWorkloadRevision(
  workloadID: string,
  revisionID: string,
  arg: WorkspaceArg = {},
): Promise<WorkloadRevisionResponse> {
  return apiFetch<WorkloadRevisionResponse>(
    `/v1/workloads/${encodeURIComponent(workloadID)}/revisions/${encodeURIComponent(revisionID)}`,
    { workspaceId: arg.workspaceId, signal: arg.signal },
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
    workspaceId: opts.workspaceId,
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
      workspaceId: opts.workspaceId,
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
    `/v1/sinks/${encodeURIComponent(sinkID)}:deliver`,
    { method: "POST", idempotencyKey: opts.idempotencyKey },
  );
}

export function replaySink(
  sinkID: string,
  body: ReplaySinkRequest,
  opts: { idempotencyKey?: string } = {},
): Promise<SinkResult> {
  return apiFetch<SinkResult>(
    `/v1/sinks/${encodeURIComponent(sinkID)}:replay`,
    { method: "POST", body, idempotencyKey: opts.idempotencyKey },
  );
}
