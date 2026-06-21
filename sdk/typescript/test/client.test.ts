import assert from "node:assert/strict";
import { test } from "node:test";

import {
  MercatorAPIError,
  MercatorClient,
  MercatorRequestError,
} from "../src/index.js";

type FetchFunction = (input: string | URL, init?: RequestInit) => Promise<Response>;

type CapturedRequest = {
  url: string;
  init: RequestInit;
  body: unknown;
};

type MockResponse = {
  status?: number;
  headers?: Record<string, string>;
  body?: unknown;
};

function jsonResponse(response: MockResponse = {}): Response {
  const { status = 200, headers = {}, body = status === 204 || status === 205 ? undefined : {} } = response;
  const normalizedHeaders = new Headers(headers);
  if (body !== undefined && !normalizedHeaders.has("content-type")) {
    normalizedHeaders.set("content-type", "application/json");
  }
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: normalizedHeaders,
  });
}

function createMockFetch(responses: MockResponse[] | ((request: CapturedRequest) => MockResponse)): {
  fetch: FetchFunction;
  requests: CapturedRequest[];
} {
  const requests: CapturedRequest[] = [];
  const fetch: FetchFunction = async (input: string | URL, init: RequestInit = {}) => {
    const body = typeof init.body === "string" ? JSON.parse(init.body) : undefined;
    const request = { url: String(input), init, body };
    requests.push(request);
    const response = typeof responses === "function" ? responses(request) : responses.shift();
    if (!response) {
      throw new Error("No mock response queued");
    }
    return jsonResponse(response);
  };
  return { fetch, requests };
}

function headersOf(request: CapturedRequest): Headers {
  return new Headers(request.init.headers);
}

test("createRun sends bearer auth, JSON body, workspace fallback, and idempotency key", async () => {
  const { fetch, requests } = createMockFetch([{ status: 202, body: { run_id: "run_1" } }]);
  const client = new MercatorClient({
    baseUrl: "https://mercator.example/api/",
    token: "secret-token",
    fetch,
  });
  const workload = {
    workspace_id: "ws_1",
    workload_id: "wl_1",
    spec: {
      containers: [{ name: "main", image: "repo/app@sha256:abc", platform: { os: "linux", architecture: "amd64" } }],
      resources: { cpu: { min_millis: 500 }, memory: { min_bytes: 268435456 }, ephemeral_disk: { min_bytes: 1073741824 } },
      network: { inbound: "none" },
      placement: { objective: "balanced" },
      execution: { max_runtime_seconds: 60, max_pre_start_attempts: 1 },
    },
  };

  const response = await client.createRun({ run_id: "run_1", workload }, { idempotencyKey: "idem-1" });

  assert.deepEqual(response, { run_id: "run_1" });
  assert.equal(requests[0]?.url, "https://mercator.example/api/v1/runs");
  assert.equal(requests[0]?.init.method, "POST");
  assert.deepEqual(requests[0]?.body, { run_id: "run_1", workload });
  const headers = headersOf(requests[0]!);
  assert.equal(headers.get("authorization"), "Bearer secret-token");
  assert.equal(headers.get("content-type"), "application/json");
  assert.equal(headers.get("idempotency-key"), "idem-1");
});

test("createRun can reference a stored workload revision without an inline workload", async () => {
  const { fetch, requests } = createMockFetch([{ status: 202, body: { run_id: "run_from_revision" } }]);
  const client = new MercatorClient({ baseUrl: "https://mercator.example", fetch });

  await client.createRun(
    {
      workspace_id: "ws_1",
      run_id: "run_from_revision",
      workload_id: "wl_1",
      workload_revision_id: "wrev_1",
    },
    { idempotencyKey: "idem-revision-run" },
  );

  assert.deepEqual(requests[0]?.body, {
    workspace_id: "ws_1",
    run_id: "run_from_revision",
    workload_id: "wl_1",
    workload_revision_id: "wrev_1",
  });
});

test("list and action methods build encoded paths and query strings", async () => {
  const { fetch, requests } = createMockFetch([
    { body: { runs: [{ id: "run/a", workspace_id: "ws 1", phase: "succeeded", cleanup: "not_required", closed: true }] } },
    { body: { run: { id: "run/a", workspace_id: "ws 1", phase: "succeeded", cleanup: "not_required", closed: true } } },
    { body: { events: [{ specversion: "1.0", id: "evt_1", source: "source", type: "type", subject: "runs/run/a", time: "2026-06-20T00:00:00Z", workspaceid: "ws 1", streamversion: 1, globalposition: 1, data: {} }] } },
    { body: { decision: { id: "decision_1", workload_revision_digest: "sha256:x", evaluated_at: "2026-06-20T00:00:00Z", model_version: "latency-v1", policy: { objective: "balanced" }, collection_report: {}, candidates: [], selection_reason_codes: [] } } },
    { body: { run: { id: "run/a", workspace_id: "ws 1", phase: "cancelled", cleanup: "not_required", closed: true } } },
  ]);
  const client = new MercatorClient({ baseUrl: "https://mercator.example", fetch });

  await client.listRuns({ workspaceId: "ws 1" });
  await client.waitRun("run/a", { workspaceId: "ws 1" });
  await client.listRunEvents("run/a", { workspaceId: "ws 1" });
  await client.getRunDecision("run/a", { workspaceId: "ws 1" });
  await client.cancelRun("run/a", { workspaceId: "ws 1" });

  assert.equal(requests[0]?.url, "https://mercator.example/v1/runs?workspace_id=ws+1");
  assert.equal(requests[1]?.url, "https://mercator.example/v1/runs/run%2Fa:wait?workspace_id=ws+1");
  assert.equal(requests[2]?.url, "https://mercator.example/v1/runs/run%2Fa/events?workspace_id=ws+1");
  assert.equal(requests[3]?.url, "https://mercator.example/v1/runs/run%2Fa/decision?workspace_id=ws+1");
  assert.equal(requests[4]?.url, "https://mercator.example/v1/runs/run%2Fa:cancel?workspace_id=ws+1");
});

test("workloads, images, connections, offers, secrets, placements, and sinks use v1 routes", async () => {
  const { fetch, requests } = createMockFetch((request) => {
    if (request.url.endsWith("/v1/workloads")) return { status: 202, body: { workload_id: "wl_1" } };
    if (request.url.includes("/v1/workloads/wl_1/revisions?")) return { body: { revisions: [] } };
    if (request.url.endsWith("/v1/workloads/wl_1/revisions")) return { status: 202, body: { revision: { id: "rev_1", workspace_id: "ws_1", workload_id: "wl_1", digest: "sha256:abc", spec: {} } } };
    if (request.url.endsWith("/v1/workloads/wl_1/revisions/rev_1?workspace_id=ws_1")) return { body: { revision: { id: "rev_1", workspace_id: "ws_1", workload_id: "wl_1", digest: "sha256:abc", spec: {} } } };
    if (request.url.endsWith("/v1/images:resolve")) return { body: { image: { image: "repo/app:latest", digest: "sha256:abc", platform: "linux/amd64" } } };
    if (request.url.endsWith("/v1/connections?workspace_id=ws_1")) return { body: { connections: [{ id: "conn_1", workspace_id: "ws_1", adapter_type: "fake", authorized: true }] } };
    if (request.url.endsWith("/v1/offers?workspace_id=ws_1")) return { body: { offers: [] } };
    if (request.url.endsWith("/v1/secrets?workspace_id=ws_1")) return { body: { secrets: [{ secret_id: "db", version: 1 }] } };
    if (request.url.endsWith("/v1/secrets/db/versions")) return { status: 202, body: { secret_id: "db", version: 2 } };
    if (request.url.endsWith("/v1/secrets/db/grants")) return { status: 202, body: { grant: { id: "grant_1", secret_id: "db", version: 2, scope_type: "run", scope_id: "run_1", revoked: false } } };
    if (request.url.endsWith("/v1/placements:preview")) return { body: { decision: { id: "decision_1", workload_revision_digest: "sha256:x", evaluated_at: "2026-06-20T00:00:00Z", model_version: "latency-v1", policy: { objective: "balanced" }, collection_report: {}, candidates: [], selection_reason_codes: [] } } };
    if (request.url.endsWith("/v1/sinks/audit")) return { body: { sink_id: "audit", cursor: 1, has_cursor: true } };
    if (request.url.endsWith("/v1/sinks/audit:deliver")) return { status: 202, body: { sink_id: "audit", delivered: 1, last_position: 2 } };
    if (request.url.endsWith("/v1/sinks/audit:replay")) return { status: 202, body: { sink_id: "audit", delivered: 1, last_position: 2, replay_id: "replay_1" } };
    throw new Error(`Unhandled route: ${request.url}`);
  });
  const client = new MercatorClient({ baseUrl: "https://mercator.example", fetch });
  const revision = { workspace_id: "ws_1", workload_id: "wl_1", spec: { containers: [], resources: {}, network: {}, placement: {}, execution: {} } };

  await client.createWorkload({ workspace_id: "ws_1", workload_id: "wl_1", name: "api" }, { idempotencyKey: "idem-workload" });
  await client.createWorkloadRevision("wl_1", { workspaceId: "ws_1", revision }, { idempotencyKey: "idem-revision" });
  await client.listWorkloadRevisions("wl_1", { workspaceId: "ws_1" });
  await client.getWorkloadRevision("wl_1", "rev_1", { workspaceId: "ws_1" });
  await client.resolveImage({ image: "repo/app:latest", platform: "linux/amd64" });
  await client.listConnections({ workspaceId: "ws_1" });
  await client.listOffers({ workspaceId: "ws_1" });
  await client.listSecrets({ workspaceId: "ws_1" });
  await client.createSecretVersion("db", { workspace_id: "ws_1", value: "secret" }, { idempotencyKey: "idem-secret" });
  await client.grantSecret("db", { workspace_id: "ws_1", version: 2, scope_type: "run", scope_id: "run_1" });
  await client.previewPlacement({ workspace_id: "ws_1", workload: revision });
  await client.getSinkStatus("audit");
  await client.deliverSink("audit");
  await client.replaySink("audit", { from_exclusive: 1, limit: 10, replay_id: "replay_1" });

  assert.equal(headersOf(requests[0]!).get("idempotency-key"), "idem-workload");
  assert.equal(headersOf(requests[1]!).get("idempotency-key"), "idem-revision");
  assert.equal(headersOf(requests[8]!).get("idempotency-key"), "idem-secret");
  assert.deepEqual(requests.at(-1)?.body, { from_exclusive: 1, limit: 10, replay_id: "replay_1" });
});

test("throws MercatorAPIError with parsed error details on non-2xx responses", async () => {
  const { fetch } = createMockFetch([
    {
      status: 409,
      body: {
        code: "IDEMPOTENCY_CONFLICT",
        message: "Idempotency key was reused with a different request hash.",
        details: [{ code: "X", path: "run_id", message: "bad" }],
      },
    },
  ]);
  const client = new MercatorClient({ baseUrl: "https://mercator.example", fetch });

  await assert.rejects(
    () => client.createRun({ run_id: "run_1", workload: { spec: { containers: [], resources: {}, network: {}, placement: {}, execution: {} } } }, { idempotencyKey: "idem-1" }),
    (error: unknown) => {
      assert.ok(error instanceof MercatorAPIError);
      const apiError = error as MercatorAPIError;
      assert.equal(apiError.status, 409);
      assert.equal(apiError.code, "IDEMPOTENCY_CONFLICT");
      assert.equal(apiError.request.method, "POST");
      assert.equal(apiError.request.path, "/v1/runs");
      assert.deepEqual(apiError.details, [{ code: "X", path: "run_id", message: "bad" }]);
      return true;
    },
  );
});

test("wraps invalid JSON responses in MercatorRequestError", async () => {
  const fetch: FetchFunction = async () => new Response("{not-json", {
    headers: { "content-type": "application/json" },
    status: 502,
  });
  const client = new MercatorClient({ baseUrl: "https://mercator.example", fetch });

  await assert.rejects(
    () => client.listRuns({ workspaceId: "ws_1" }),
    (error: unknown) => {
      assert.ok(error instanceof MercatorRequestError);
      assert.equal((error as MercatorRequestError).request.path, "/v1/runs");
      return true;
    },
  );
});

test("supports request abort signals and per-call headers", async () => {
  const { fetch, requests } = createMockFetch([{ status: 204, body: undefined }]);
  const client = new MercatorClient({ baseUrl: "https://mercator.example", fetch });
  const controller = new AbortController();
  const options = { signal: controller.signal, headers: { "X-Trace-ID": "trace_1" } };

  const response = await client.request<void>("POST", "/v1/custom", options);

  assert.equal(response, undefined);
  assert.equal(requests[0]?.init.signal, controller.signal);
  assert.equal(headersOf(requests[0]!).get("x-trace-id"), "trace_1");
});
