import assert from "node:assert/strict";
import { test } from "node:test";

import { createReporter, Reporter } from "../src/index.js";

type FetchFunction = (input: string | URL, init?: RequestInit) => Promise<Response>;

type CapturedRequest = {
  url: string;
  init: RequestInit;
  body: unknown;
};

function makeStubFetch(status: number, responseBody: unknown = {}): {
  fetch: FetchFunction;
  requests: CapturedRequest[];
} {
  const requests: CapturedRequest[] = [];
  const fetch: FetchFunction = async (input: string | URL, init: RequestInit = {}) => {
    const body = typeof init.body === "string" ? JSON.parse(init.body) : undefined;
    requests.push({ url: String(input), init, body });
    const data = JSON.stringify(responseBody);
    return new Response(data, {
      status,
      headers: { "Content-Type": "application/json" },
    });
  };
  return { fetch, requests };
}

const TEST_ENV = {
  MERCATOR_REPORT_URL: "https://pub.example",
  MERCATOR_RUN_ID: "run_abc123",
  MERCATOR_WORKSPACE_ID: "ws_xyz",
  MERCATOR_RUN_TOKEN: "tok_secret",
};

test("createReporter returns null and warns when env vars are absent", () => {
  const warns: string[] = [];
  const originalWarn = console.warn;
  console.warn = (msg: string) => warns.push(msg);
  try {
    const reporter = createReporter({ env: {} });
    assert.equal(reporter, null);
    assert.equal(warns.length, 1);
    assert.match(warns[0]!, /MERCATOR_REPORT_URL/);
  } finally {
    console.warn = originalWarn;
  }
});

for (const missingVar of [
  "MERCATOR_REPORT_URL",
  "MERCATOR_RUN_ID",
  "MERCATOR_WORKSPACE_ID",
  "MERCATOR_RUN_TOKEN",
] as const) {
  test(`createReporter throws when only ${missingVar} is missing`, () => {
    // A partially populated environment is a misconfiguration: every report
    // would fail server-side (e.g. 400 WORKSPACE_REQUIRED without a
    // workspace id), so construction fails fast instead.
    const env: Record<string, string | undefined> = { ...TEST_ENV };
    delete env[missingVar];

    assert.throws(
      () => createReporter({ env }),
      (err: unknown) => {
        assert.ok(err instanceof Error);
        assert.match(err.message, new RegExp(missingVar));
        return true;
      },
    );
  });
}

test("createReporter treats an empty MERCATOR_WORKSPACE_ID as missing", () => {
  const env = { ...TEST_ENV, MERCATOR_WORKSPACE_ID: "" };

  assert.throws(
    () => createReporter({ env }),
    (err: unknown) => {
      assert.ok(err instanceof Error);
      assert.match(err.message, /MERCATOR_WORKSPACE_ID/);
      return true;
    },
  );
});

test("createReporter returns a Reporter when all env vars are present", () => {
  const { fetch } = makeStubFetch(202);
  const reporter = createReporter({ env: TEST_ENV, fetch });
  assert.ok(reporter instanceof Reporter);
});

test("report() POSTs to the correct URL with Bearer auth and JSON body", async () => {
  const { fetch, requests } = makeStubFetch(202);
  const reporter = createReporter({ env: TEST_ENV, fetch });
  assert.ok(reporter);

  await reporter.report({ type: "progress", data: { pct: 50 } });

  assert.equal(requests.length, 1);
  const req = requests[0]!;
  // URL: <base>/v1/runs/<run_id>:report?workspace_id=<ws>
  assert.equal(
    req.url,
    "https://pub.example/v1/runs/run_abc123:report?workspace_id=ws_xyz",
  );
  assert.equal(req.init.method, "POST");
  const headers = new Headers(req.init.headers);
  assert.equal(headers.get("authorization"), "Bearer tok_secret");
  assert.equal(headers.get("content-type"), "application/json");
  assert.deepEqual(req.body, { type: "progress", data: { pct: 50 } });
});

test("report() omits data field when not provided", async () => {
  const { fetch, requests } = makeStubFetch(202);
  const reporter = createReporter({ env: TEST_ENV, fetch });
  assert.ok(reporter);

  await reporter.report({ type: "started" });

  const req = requests[0]!;
  assert.deepEqual(req.body, { type: "started" });
  assert.equal("data" in (req.body as Record<string, unknown>), false);
});

test("reportExit() posts { type: 'exit', exit_code: code }", async () => {
  const { fetch, requests } = makeStubFetch(202);
  const reporter = createReporter({ env: TEST_ENV, fetch });
  assert.ok(reporter);

  await reporter.reportExit(0);

  const req = requests[0]!;
  assert.equal(
    req.url,
    "https://pub.example/v1/runs/run_abc123:report?workspace_id=ws_xyz",
  );
  assert.deepEqual(req.body, { type: "exit", exit_code: 0 });
});

test("reportExit() encodes non-zero exit codes", async () => {
  const { fetch, requests } = makeStubFetch(202);
  const reporter = createReporter({ env: TEST_ENV, fetch });
  assert.ok(reporter);

  await reporter.reportExit(1);

  assert.deepEqual(requests[0]!.body, { type: "exit", exit_code: 1 });
});

test("report() rejects when the server returns non-202", async () => {
  const { fetch } = makeStubFetch(500, { code: "SERVER_ERROR", message: "boom" });
  const reporter = createReporter({ env: TEST_ENV, fetch });
  assert.ok(reporter);

  await assert.rejects(
    () => reporter.report({ type: "progress" }),
    (err: unknown) => {
      assert.ok(err instanceof Error);
      assert.match(err.message, /202/);
      assert.match(err.message, /500/);
      return true;
    },
  );
});

test("run_id and workspace_id with special characters are URL-encoded", async () => {
  const { fetch, requests } = makeStubFetch(202);
  const specialEnv = {
    ...TEST_ENV,
    MERCATOR_RUN_ID: "run/with spaces",
    MERCATOR_WORKSPACE_ID: "ws/special&chars",
  };
  const reporter = createReporter({ env: specialEnv, fetch });
  assert.ok(reporter);

  await reporter.report({ type: "test" });

  assert.equal(
    requests[0]!.url,
    "https://pub.example/v1/runs/run%2Fwith%20spaces:report?workspace_id=ws%2Fspecial%26chars",
  );
});

