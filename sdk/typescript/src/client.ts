import { MercatorAPIError, MercatorRequestError, type MercatorRequestInfo, errorResponseFromBody } from "./errors.js";
import type {
  ConnectionListResponse,
  CreateRunRequest,
  CreateRunResponse,
  CreateSecretVersionRequest,
  CreateSecretVersionResponse,
  CreateWorkloadRequest,
  CreateWorkloadResponse,
  EnvBinding,
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
  /**
   * Default workspace applied to every call (query param on reads, body field
   * on createRun) when a per-call workspaceId is not supplied. Per-call
   * overrides always win.
   */
  workspaceId?: string;
};

export type RunImageOptions = RequestOptions & {
  /** Container args for the image. */
  args?: string[];
  /** Container env bindings for the image. */
  env?: Record<string, EnvBinding>;
  /**
   * Optional run id. Omit it and the server generates one, returned at
   * `response.run.id`.
   */
  runId?: string;
  /** Workspace for this run; overrides the client default. */
  workspaceId?: string;
  /**
   * Idempotency key. Required by the server; when omitted a stable key is
   * derived from `runId` (retry-safe) or a random one is minted for a
   * generated run.
   */
  idempotencyKey?: string;
};

export type WaitUntilTerminalOptions = RequestOptions & {
  workspaceId?: string;
  /**
   * Overall budget for the poll-until-terminal loop, in milliseconds. The
   * server long-polls each `:wait` for up to ~30s and returns 202 while the run
   * is still open; this helper re-issues the wait until the run closes or this
   * deadline elapses. Defaults to 5 minutes.
   */
  deadlineMs?: number;
};

export class MercatorClient {
  private readonly baseUrl: string;
  private readonly defaultHeaders: Headers;
  private readonly fetchImpl: FetchFunction;
  private readonly token?: string;
  private readonly workspaceId?: string;

  constructor(config: MercatorClientConfig) {
    if (!config.baseUrl) {
      throw new TypeError("MercatorClient requires baseUrl.");
    }
    this.baseUrl = normalizeBaseUrl(config.baseUrl);
    this.defaultHeaders = new Headers(config.headers);
    this.fetchImpl = config.fetch ?? globalThis.fetch?.bind(globalThis);
    this.token = config.token;
    this.workspaceId = config.workspaceId;
    if (!this.fetchImpl) {
      throw new TypeError("MercatorClient requires a fetch implementation.");
    }
  }

  /** Resolve the effective workspace id for a call, honoring per-call overrides. */
  private resolveWorkspaceId(params?: WorkspaceRequest): string | undefined {
    return params?.workspaceId ?? this.workspaceId;
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
    const workspaceId = options.workspaceId ?? this.workspaceId;
    const effectiveBody = workspaceId && body.workspace_id === undefined ? { ...body, workspace_id: workspaceId } : body;
    return this.request<CreateRunResponse>("POST", "/v1/runs", { ...options, body: effectiveBody });
  }

  /**
   * Create a run from just an image (the server shorthand form). Only `image`
   * is required. `runId` is optional: omit it and the server generates one,
   * which you read from `response.run.id`. The server applies all other
   * defaults (container name=main, platform=linux/amd64, resources, network,
   * placement, execution). Returns the same envelope as {@link createRun}.
   *
   * `idempotencyKey` is required by the server; when omitted and a `runId` is
   * supplied this derives a stable, retry-safe key as `` `${runId}:create` ``.
   * When neither is supplied there is no stable identifier to derive a
   * retry-safe key from -- silently minting a fresh random key per call would
   * break the server's at-most-once guarantee (a transport retry would create
   * a SECOND run instead of replaying the first), so this throws instead. Pass
   * an explicit `idempotencyKey` (reused verbatim across retries of the same
   * logical operation) or a `runId` to get retry-safe behavior on the
   * server-generated-run_id path.
   */
  runImage(image: string, options: RunImageOptions = {}): Promise<CreateRunResponse> {
    const { args, env, runId, workspaceId, idempotencyKey, ...requestOptions } = options;
    const body: CreateRunRequest = { image };
    if (args !== undefined) {
      body.args = args;
    }
    if (env !== undefined) {
      body.env = env;
    }
    if (runId !== undefined) {
      body.run_id = runId;
    }
    let key = idempotencyKey;
    if (key === undefined) {
      if (runId === undefined) {
        return Promise.reject(
          new Error(
            "runImage requires an explicit idempotencyKey when runId is omitted: an " +
              "auto-generated random key is per-attempt, not per-logical-operation, so a " +
              "transport retry would create a second run instead of replaying the first. " +
              "Pass idempotencyKey (reused across retries) or supply runId.",
          ),
        );
      }
      key = `${runId}:create`;
    }
    return this.createRun(body, { ...requestOptions, idempotencyKey: key, workspaceId });
  }

  listRuns(params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<RunListResponse> {
    return this.request<RunListResponse>("GET", "/v1/runs", { ...options, query: this.workspaceQuery(params, options.query) });
  }

  getRun(runId: string, params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<RunResponse> {
    return this.request<RunResponse>("GET", `/v1/runs/${pathSegment(runId)}`, { ...options, query: this.workspaceQuery(params, options.query) });
  }

  waitRun(runId: string, params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<RunResponse> {
    return this.request<RunResponse>("GET", `/v1/runs/${pathSegment(runId)}:wait`, { ...options, query: this.workspaceQuery(params, options.query) });
  }

  /**
   * Block until a run reaches a terminal (closed) state, honoring the server's
   * long-poll semantics: `:wait` returns 202 with the latest still-open run at
   * its internal deadline, and this helper re-issues the wait until the run
   * closes (HTTP 200) or `deadlineMs` elapses. Returns the latest run envelope
   * either way; inspect `result.run.closed` to distinguish terminal from
   * timed-out.
   */
  async waitRunUntilTerminal(runId: string, options: WaitUntilTerminalOptions = {}): Promise<RunResponse> {
    const { deadlineMs = 5 * 60 * 1000, workspaceId, ...requestOptions } = options;
    const params: WorkspaceRequest = { workspaceId };
    const path = `/v1/runs/${pathSegment(runId)}:wait`;
    const query = this.workspaceQuery(params, requestOptions.query);
    const deadline = Date.now() + deadlineMs;
    let latest: RunResponse;
    do {
      latest = await this.request<RunResponse>("GET", path, { ...requestOptions, query });
      if (latest.run.closed) {
        return latest;
      }
    } while (Date.now() < deadline);
    return latest;
  }

  refreshRun(runId: string, params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<RunResponse> {
    return this.request<RunResponse>("POST", `/v1/runs/${pathSegment(runId)}:refresh`, { ...options, query: this.workspaceQuery(params, options.query) });
  }

  cancelRun(runId: string, params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<RunResponse> {
    return this.request<RunResponse>("POST", `/v1/runs/${pathSegment(runId)}:cancel`, { ...options, query: this.workspaceQuery(params, options.query) });
  }

  listRunEvents<TData = unknown>(runId: string, params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<EventListResponse<TData>> {
    return this.request<EventListResponse<TData>>("GET", `/v1/runs/${pathSegment(runId)}/events`, { ...options, query: this.workspaceQuery(params, options.query) });
  }

  getRunDecision(runId: string, params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<PlacementDecisionResponse> {
    return this.request<PlacementDecisionResponse>("GET", `/v1/runs/${pathSegment(runId)}/decision`, { ...options, query: this.workspaceQuery(params, options.query) });
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
      query: this.workspaceQuery({ workspaceId }, options.query),
    });
  }

  listWorkloadRevisions(workloadId: string, params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<WorkloadRevisionListResponse> {
    return this.request<WorkloadRevisionListResponse>("GET", `/v1/workloads/${pathSegment(workloadId)}/revisions`, {
      ...options,
      query: this.workspaceQuery(params, options.query),
    });
  }

  getWorkloadRevision(workloadId: string, revisionId: string, params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<WorkloadRevisionResponse> {
    return this.request<WorkloadRevisionResponse>("GET", `/v1/workloads/${pathSegment(workloadId)}/revisions/${pathSegment(revisionId)}`, {
      ...options,
      query: this.workspaceQuery(params, options.query),
    });
  }

  resolveImage(body: ResolveImageRequest, options: RequestOptions = {}): Promise<ResolveImageResponse> {
    return this.request<ResolveImageResponse>("POST", "/v1/images:resolve", { ...options, body });
  }

  listConnections(params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<ConnectionListResponse> {
    return this.request<ConnectionListResponse>("GET", "/v1/connections", { ...options, query: this.workspaceQuery(params, options.query) });
  }

  listOffers(params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<OfferListResponse> {
    return this.request<OfferListResponse>("GET", "/v1/offers", { ...options, query: this.workspaceQuery(params, options.query) });
  }

  listSecrets(params: WorkspaceRequest = {}, options: RequestOptions = {}): Promise<SecretMetadataListResponse> {
    return this.request<SecretMetadataListResponse>("GET", "/v1/secrets", { ...options, query: this.workspaceQuery(params, options.query) });
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

  private workspaceQuery(params: WorkspaceRequest, existing?: QueryParams): QueryParams {
    const workspaceId = this.resolveWorkspaceId(params);
    if (workspaceId === undefined) {
      return { ...existing };
    }
    return { ...existing, workspace_id: workspaceId };
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
