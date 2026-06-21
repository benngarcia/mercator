import { MercatorAPIError, MercatorRequestError, type MercatorRequestInfo, errorResponseFromBody } from "./errors.js";
import type {
  ConnectionListResponse,
  CreateRunRequest,
  CreateRunResponse,
  CreateSecretVersionRequest,
  CreateSecretVersionResponse,
  CreateWorkloadRequest,
  CreateWorkloadResponse,
  EventListResponse,
  FetchFunction,
  GrantSecretRequest,
  MutationRequestOptions,
  OfferListResponse,
  PlacementDecisionResponse,
  PlacementPreviewRequest,
  QueryParams,
  ReplaySinkRequest,
  RequestOptions,
  ResolveImageRequest,
  ResolveImageResponse,
  RunListResponse,
  RunResponse,
  SecretGrantResponse,
  SecretMetadataListResponse,
  SinkResult,
  SinkStatus,
  WorkloadRevisionListResponse,
  WorkloadRevisionResponse,
  WorkspaceRequest,
} from "./types.js";

export type MercatorClientConfig = {
  baseUrl: string;
  token?: string;
  fetch?: FetchFunction;
  headers?: HeadersInit;
};

export class MercatorClient {
  private readonly baseUrl: string;
  private readonly defaultHeaders: Headers;
  private readonly fetchImpl: FetchFunction;
  private readonly token?: string;

  constructor(config: MercatorClientConfig) {
    if (!config.baseUrl) {
      throw new TypeError("MercatorClient requires baseUrl.");
    }
    this.baseUrl = normalizeBaseUrl(config.baseUrl);
    this.defaultHeaders = new Headers(config.headers);
    this.fetchImpl = config.fetch ?? globalThis.fetch?.bind(globalThis);
    this.token = config.token;
    if (!this.fetchImpl) {
      throw new TypeError("MercatorClient requires a fetch implementation.");
    }
  }

  async request<TResponse>(method: string, path: string, options: RequestOptions = {}): Promise<TResponse> {
    const normalizedMethod = method.toUpperCase();
    const normalizedPath = path.startsWith("/") ? path : `/${path}`;
    const url = this.urlFor(normalizedPath, options.query);
    const headers = this.headersFor(normalizedPath, options);
    const init: RequestInit = {
      headers,
      method: normalizedMethod,
      signal: options.signal,
    };
    if (options.body !== undefined) {
      init.body = JSON.stringify(options.body);
    }
    const requestInfo: MercatorRequestInfo = {
      method: normalizedMethod,
      path: normalizedPath,
      url,
    };

    let response: Response;
    try {
      response = await this.fetchImpl(url, init);
    } catch (cause) {
      throw new MercatorRequestError(`Mercator request failed: ${normalizedMethod} ${normalizedPath}`, {
        cause,
        request: requestInfo,
      });
    }

    let body: unknown;
    try {
      body = await parseResponseBody(response);
    } catch (cause) {
      throw new MercatorRequestError(`Mercator response was not valid JSON: ${normalizedMethod} ${normalizedPath}`, {
        cause,
        request: requestInfo,
      });
    }
    if (!response.ok) {
      const errorBody = errorResponseFromBody(body);
      throw new MercatorAPIError(errorBody?.message ?? `Mercator API returned HTTP ${response.status}`, {
        code: errorBody?.code ?? "HTTP_ERROR",
        details: errorBody?.details,
        request: requestInfo,
        responseBody: body,
        status: response.status,
      });
    }
    return body as TResponse;
  }

  createRun(body: CreateRunRequest, options: MutationRequestOptions): Promise<CreateRunResponse> {
    return this.request<CreateRunResponse>("POST", "/v1/runs", { ...options, body });
  }

  listRuns(params: WorkspaceRequest, options: RequestOptions = {}): Promise<RunListResponse> {
    return this.request<RunListResponse>("GET", "/v1/runs", { ...options, query: workspaceQuery(params, options.query) });
  }

  getRun(runId: string, params: WorkspaceRequest, options: RequestOptions = {}): Promise<RunResponse> {
    return this.request<RunResponse>("GET", `/v1/runs/${pathSegment(runId)}`, { ...options, query: workspaceQuery(params, options.query) });
  }

  waitRun(runId: string, params: WorkspaceRequest, options: RequestOptions = {}): Promise<RunResponse> {
    return this.request<RunResponse>("GET", `/v1/runs/${pathSegment(runId)}:wait`, { ...options, query: workspaceQuery(params, options.query) });
  }

  refreshRun(runId: string, params: WorkspaceRequest, options: RequestOptions = {}): Promise<RunResponse> {
    return this.request<RunResponse>("POST", `/v1/runs/${pathSegment(runId)}:refresh`, { ...options, query: workspaceQuery(params, options.query) });
  }

  cancelRun(runId: string, params: WorkspaceRequest, options: RequestOptions = {}): Promise<RunResponse> {
    return this.request<RunResponse>("POST", `/v1/runs/${pathSegment(runId)}:cancel`, { ...options, query: workspaceQuery(params, options.query) });
  }

  listRunEvents<TData = unknown>(runId: string, params: WorkspaceRequest, options: RequestOptions = {}): Promise<EventListResponse<TData>> {
    return this.request<EventListResponse<TData>>("GET", `/v1/runs/${pathSegment(runId)}/events`, { ...options, query: workspaceQuery(params, options.query) });
  }

  getRunDecision(runId: string, params: WorkspaceRequest, options: RequestOptions = {}): Promise<PlacementDecisionResponse> {
    return this.request<PlacementDecisionResponse>("GET", `/v1/runs/${pathSegment(runId)}/decision`, { ...options, query: workspaceQuery(params, options.query) });
  }

  previewPlacement(body: PlacementPreviewRequest, options: RequestOptions = {}): Promise<PlacementDecisionResponse> {
    return this.request<PlacementDecisionResponse>("POST", "/v1/placements:preview", { ...options, body });
  }

  createWorkload(body: CreateWorkloadRequest, options: MutationRequestOptions): Promise<CreateWorkloadResponse> {
    return this.request<CreateWorkloadResponse>("POST", "/v1/workloads", { ...options, body });
  }

  createWorkloadRevision(workloadId: string, body: { revision: WorkloadRevisionResponse["revision"] } & WorkspaceRequest, options: MutationRequestOptions): Promise<WorkloadRevisionResponse> {
    const { workspaceId, revision } = body;
    return this.request<WorkloadRevisionResponse>("POST", `/v1/workloads/${pathSegment(workloadId)}/revisions`, {
      ...options,
      body: { revision },
      query: workspaceQuery({ workspaceId }, options.query),
    });
  }

  listWorkloadRevisions(workloadId: string, params: WorkspaceRequest, options: RequestOptions = {}): Promise<WorkloadRevisionListResponse> {
    return this.request<WorkloadRevisionListResponse>("GET", `/v1/workloads/${pathSegment(workloadId)}/revisions`, {
      ...options,
      query: workspaceQuery(params, options.query),
    });
  }

  getWorkloadRevision(workloadId: string, revisionId: string, params: WorkspaceRequest, options: RequestOptions = {}): Promise<WorkloadRevisionResponse> {
    return this.request<WorkloadRevisionResponse>("GET", `/v1/workloads/${pathSegment(workloadId)}/revisions/${pathSegment(revisionId)}`, {
      ...options,
      query: workspaceQuery(params, options.query),
    });
  }

  resolveImage(body: ResolveImageRequest, options: RequestOptions = {}): Promise<ResolveImageResponse> {
    return this.request<ResolveImageResponse>("POST", "/v1/images:resolve", { ...options, body });
  }

  listConnections(params: WorkspaceRequest, options: RequestOptions = {}): Promise<ConnectionListResponse> {
    return this.request<ConnectionListResponse>("GET", "/v1/connections", { ...options, query: workspaceQuery(params, options.query) });
  }

  listOffers(params: WorkspaceRequest, options: RequestOptions = {}): Promise<OfferListResponse> {
    return this.request<OfferListResponse>("GET", "/v1/offers", { ...options, query: workspaceQuery(params, options.query) });
  }

  listSecrets(params: WorkspaceRequest, options: RequestOptions = {}): Promise<SecretMetadataListResponse> {
    return this.request<SecretMetadataListResponse>("GET", "/v1/secrets", { ...options, query: workspaceQuery(params, options.query) });
  }

  createSecretVersion(secretId: string, body: CreateSecretVersionRequest, options: MutationRequestOptions): Promise<CreateSecretVersionResponse> {
    return this.request<CreateSecretVersionResponse>("POST", `/v1/secrets/${pathSegment(secretId)}/versions`, { ...options, body });
  }

  grantSecret(secretId: string, body: GrantSecretRequest, options: RequestOptions = {}): Promise<SecretGrantResponse> {
    return this.request<SecretGrantResponse>("POST", `/v1/secrets/${pathSegment(secretId)}/grants`, { ...options, body });
  }

  getSinkStatus(sinkId: string, options: RequestOptions = {}): Promise<SinkStatus> {
    return this.request<SinkStatus>("GET", `/v1/sinks/${pathSegment(sinkId)}`, options);
  }

  deliverSink(sinkId: string, options: RequestOptions = {}): Promise<SinkResult> {
    return this.request<SinkResult>("POST", `/v1/sinks/${pathSegment(sinkId)}:deliver`, options);
  }

  replaySink(sinkId: string, body: ReplaySinkRequest, options: RequestOptions = {}): Promise<SinkResult> {
    return this.request<SinkResult>("POST", `/v1/sinks/${pathSegment(sinkId)}:replay`, { ...options, body });
  }

  private urlFor(path: string, query?: QueryParams): string {
    const url = new URL(`${this.baseUrl}${path}`);
    for (const [key, value] of Object.entries(query ?? {})) {
      if (value !== undefined && value !== null) {
        url.searchParams.set(key, String(value));
      }
    }
    return url.toString();
  }

  private headersFor(path: string, options: RequestOptions): Headers {
    const headers = new Headers(this.defaultHeaders);
    for (const [key, value] of new Headers(options.headers)) {
      headers.set(key, value);
    }
    if (this.token && path.startsWith("/v1/")) {
      headers.set("Authorization", `Bearer ${this.token}`);
    }
    if (options.idempotencyKey) {
      headers.set("Idempotency-Key", options.idempotencyKey);
    }
    if (options.body !== undefined && !headers.has("Content-Type")) {
      headers.set("Content-Type", "application/json");
    }
    return headers;
  }
}

function normalizeBaseUrl(baseUrl: string): string {
  return baseUrl.replace(/\/+$/, "");
}

function pathSegment(value: string): string {
  return encodeURIComponent(value);
}

function workspaceQuery(params: WorkspaceRequest, existing?: QueryParams): QueryParams {
  return { ...existing, workspace_id: params.workspaceId };
}

async function parseResponseBody(response: Response): Promise<unknown> {
  if (response.status === 204 || response.status === 205) {
    return undefined;
  }
  const text = await response.text();
  if (text === "") {
    return undefined;
  }
  const contentType = response.headers.get("content-type") ?? "";
  if (contentType.includes("application/json")) {
    return JSON.parse(text);
  }
  return text;
}
