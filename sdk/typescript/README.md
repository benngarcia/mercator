# Mercator TypeScript SDK

Hand-written TypeScript client for the Mercator V1 HTTP API.

```ts
import { MercatorClient } from "@mercator/sdk";

// Scope the workspace once on the client; every call inherits it. A per-call
// workspaceId still overrides this default.
const mercator = new MercatorClient({
  baseUrl: "http://127.0.0.1:8080",
  token: process.env.MERCATOR_API_TOKEN,
  workspaceId: "ws_1",
});

const runs = await mercator.listRuns();
```

## Run an image, get the exit code

The minimal one-liner: hand `runImage` an image and (optionally) some args. You
do not supply a full workload spec (the server defaults everything). Then block
until the run closes and read the container exit code straight off the run
object.

```ts
import { MercatorClient } from "@mercator/sdk";

const mercator = new MercatorClient({
  baseUrl: "http://127.0.0.1:8080",
  token: process.env.MERCATOR_API_TOKEN,
  workspaceId: "ws_1",
});

const created = await mercator.runImage("busybox", {
  args: ["echo", "hi"],
});
const runId = created.run.id; // server-generated id

const result = await mercator.waitRunUntilTerminal(runId);
console.log(result.run.outcome, result.run.exit_code); // => succeeded 0
```

`runImage` generates a `runId` when you omit one and derives a stable
`Idempotency-Key` from it (`` `${runId}:create` ``). Pass `idempotencyKey` only
when you need to coordinate retries with an external caller. Pass
`{ env: { K: { value: "v" } } }` for environment.

## Create, wait, and read the exit code in one round trip

`createRun` returns the same envelope as `getRun`/`waitRun`/`cancelRun`
(`{ run_id, run: {...}, metadata?, links?, duplicate? }`). Read the run id from
the convenience top-level `response.run_id` or from `response.run.id` (they are
always equal). The run record also exposes `run.disposition` (`release` or
`terminate`) — the recorded cleanup intent. `waitRunUntilTerminal` honors the
server's long-poll semantics and re-issues the wait until the run closes, so the
container exit code is available directly on the run, no event-log parsing
required.

```ts
const created = await mercator.createRun(
  { run_id: "run_1", workload },
  { idempotencyKey: "run_1:create" },
);
const runId = created.run.id;
if (created.duplicate) {
  console.log("idempotent replay of an existing run");
}

const result = await mercator.waitRunUntilTerminal(runId);
console.log(result.run.outcome, result.run.exit_code); // e.g. "succeeded" 0
```

`result.run.closed` distinguishes a terminal run from a wait that hit its
deadline; `result.run.exit_code` is `undefined` until a terminal observation is
recorded and a present `0` is a real success exit.

## Install for local development

```sh
cd sdk/typescript
npm install
npm test
```

The package targets Node 20 or newer and uses the runtime `fetch`
implementation. Pass `fetch` in the constructor to use a custom transport in
tests or non-Node runtimes.

## Workspace scoping

Set `workspaceId` once in the constructor and it is applied to every call: as
the `workspace_id` query parameter on reads and as the `workspace_id` body field
on `createRun` (unless the body already supplies one). Override it for a single
call by passing `{ workspaceId }` in the per-call options (reads) or mutation
options (`createRun`). If neither is set, the parameter is omitted and the
server applies its own default only when configured with a single concrete
workspace.

## Idempotency

Mercator requires `Idempotency-Key` on mutation routes that append commands.
Pass it in the method options:

```ts
await mercator.createRun(
  {
    run_id: "run_123",
    workload: {
      spec: {
        containers: [
          {
            name: "main",
            image: "registry.example/app@sha256:...",
            platform: { os: "linux", architecture: "amd64" },
          },
        ],
        resources: {
          cpu: { min_millis: 500 },
          memory: { min_bytes: 268435456 },
          ephemeral_disk: { min_bytes: 1073741824 },
        },
        network: { inbound: "none" },
        placement: { objective: "balanced" },
        execution: { max_runtime_seconds: 60, max_pre_start_attempts: 1 },
      },
    },
  },
  { idempotencyKey: "run_123:create" },
);
```

Derive a stable key from `run_id` (for example `` `${runId}:create` ``). A
logical retry that reuses the same key, the same `run_id`, and the same logical
workload is a safe replay (`duplicate: true`) even if a cosmetic `workload.id`
is regenerated; only a substantively different payload under a reused key
returns `409 IDEMPOTENCY_CONFLICT`.

## Errors

Non-2xx responses throw `MercatorAPIError` with `status`, Mercator `code`,
`details`, parsed `responseBody`, and request metadata.

```ts
import { MercatorAPIError } from "@mercator/sdk";

try {
  await mercator.getRun("missing", { workspaceId: "ws_1" });
} catch (error) {
  if (error instanceof MercatorAPIError) {
    console.error(error.status, error.code, error.message);
  }
}
```

## API coverage

`MercatorClient` includes methods for:

- runs: create, list, get, wait, waitRunUntilTerminal (poll-until-terminal), refresh, cancel, events, decision
- placement preview
- workloads and workload revisions
- image resolution
- connections and offers
- sink status, delivery, and replay

The nested workload, offer, placement, event, and run shapes are pragmatic DTO
types matching the current JSON contract. The server OpenAPI document does not
fully specify every nested object field, so deeply nested adapter-specific data
remains intentionally permissive.
