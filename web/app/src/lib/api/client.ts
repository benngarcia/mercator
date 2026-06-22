// apiFetch is the single same-origin HTTP entrypoint. It injects the bearer
// token and ?workspace_id centrally (never in components), parses the
// { code, message, details } error envelope, throws a typed ApiError on
// non-2xx, and auto-generates an Idempotency-Key for mutating requests unless
// one is supplied. Auth and workspace are NEVER added by callers.

import type { ErrorEnvelope, Violation } from "./types";
import { getToken, getWorkspace } from "../session";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details?: Violation[];

  constructor(
    status: number,
    code: string,
    message: string,
    details?: Violation[],
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.details = details;
  }

  // serviceDisabled is true for a 501 response, which the UI degrades to a
  // <ServiceDisabled> panel rather than a hard error. The Go server returns
  // 501 with codes like WORKLOAD_SERVICE_DISABLED / SINKS_DISABLED /
  // IMAGE_RESOLVER_DISABLED.
  get serviceDisabled(): boolean {
    return this.status === 501;
  }

  // unauthorized is true for a 401, which the UI maps to a friendly "set a
  // token" prompt.
  get unauthorized(): boolean {
    return this.status === 401;
  }

  // notFound is true for a 404; query hooks that treat missing resources as
  // null (e.g. useRunDecision) check this.
  get notFound(): boolean {
    return this.status === 404;
  }
}

type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

export interface ApiFetchOptions {
  method?: Method;
  body?: unknown;
  // idempotencyKey overrides the auto-generated UUID for mutating methods.
  idempotencyKey?: string;
  // searchParams are merged onto the path's query string. workspace_id is
  // injected automatically from the session unless explicitly provided here.
  searchParams?: Record<string, string | number | boolean | undefined | null>;
  signal?: AbortSignal;
  // workspaceId overrides the session default workspace for this request.
  workspaceId?: string;
}

const MUTATING_METHODS: ReadonlySet<string> = new Set([
  "POST",
  "PUT",
  "PATCH",
  "DELETE",
]);

function isMutating(method: Method): boolean {
  return MUTATING_METHODS.has(method);
}

function newIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  // Extremely defensive fallback; modern browsers always have randomUUID.
  return `idk-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function buildUrl(
  path: string,
  searchParams: ApiFetchOptions["searchParams"],
  workspaceId: string | undefined,
): string {
  // Resolve against the current origin so callers pass bare paths like
  // "/v1/runs". apiFetch is always same-origin.
  const base = typeof window !== "undefined" ? window.location.origin : "http://localhost";
  const url = new URL(path, base);

  // Inject workspace_id from the explicit override or session default unless
  // the caller already supplied it in searchParams.
  const callerWorkspace =
    searchParams && Object.prototype.hasOwnProperty.call(searchParams, "workspace_id")
      ? searchParams["workspace_id"]
      : undefined;
  const resolvedWorkspace = workspaceId ?? getWorkspace() ?? undefined;
  if (
    (callerWorkspace === undefined || callerWorkspace === null) &&
    resolvedWorkspace
  ) {
    url.searchParams.set("workspace_id", resolvedWorkspace);
  }

  if (searchParams) {
    for (const [key, value] of Object.entries(searchParams)) {
      if (value === undefined || value === null) {
        continue;
      }
      url.searchParams.set(key, String(value));
    }
  }

  return url.pathname + url.search;
}

async function parseError(response: Response): Promise<ApiError> {
  let code = `HTTP_${response.status}`;
  let message = response.statusText || `Request failed with ${response.status}`;
  let details: Violation[] | undefined;

  try {
    const text = await response.text();
    if (text) {
      const envelope = JSON.parse(text) as Partial<ErrorEnvelope>;
      if (typeof envelope.code === "string" && envelope.code) {
        code = envelope.code;
      }
      if (typeof envelope.message === "string" && envelope.message) {
        message = envelope.message;
      }
      if (Array.isArray(envelope.details)) {
        details = envelope.details;
      }
    }
  } catch {
    // Non-JSON error body; keep the status-derived defaults.
  }

  return new ApiError(response.status, code, message, details);
}

export async function apiFetch<T>(
  path: string,
  opts: ApiFetchOptions = {},
): Promise<T> {
  const method: Method = opts.method ?? "GET";
  const headers = new Headers();
  headers.set("Accept", "application/json");

  const token = getToken();
  if (token) {
    headers.set("Authorization", `Bearer ${token}`);
  }

  let bodyInit: BodyInit | undefined;
  if (opts.body !== undefined) {
    headers.set("Content-Type", "application/json");
    bodyInit = JSON.stringify(opts.body);
  }

  if (isMutating(method)) {
    headers.set("Idempotency-Key", opts.idempotencyKey ?? newIdempotencyKey());
  }

  const url = buildUrl(path, opts.searchParams, opts.workspaceId);

  const response = await fetch(url, {
    method,
    headers,
    body: bodyInit,
    signal: opts.signal,
    credentials: "same-origin",
  });

  if (!response.ok) {
    throw await parseError(response);
  }

  // 204 No Content and empty bodies parse to undefined.
  if (response.status === 204) {
    return undefined as T;
  }
  const text = await response.text();
  if (!text) {
    return undefined as T;
  }
  return JSON.parse(text) as T;
}
