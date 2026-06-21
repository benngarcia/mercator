# Mercator TypeScript SDK

Hand-written TypeScript client for the Mercator V1 HTTP API.

```ts
import { MercatorClient } from "@mercator/sdk";

const mercator = new MercatorClient({
  baseUrl: "http://127.0.0.1:8080",
  token: process.env.MERCATOR_API_TOKEN,
});

const runs = await mercator.listRuns({ workspaceId: "ws_1" });
```

## Install for local development

```sh
cd sdk/typescript
npm install
npm test
```

The package targets Node 20 or newer and uses the runtime `fetch`
implementation. Pass `fetch` in the constructor to use a custom transport in
tests or non-Node runtimes.

## Idempotency

Mercator requires `Idempotency-Key` on mutation routes that append commands.
Pass it in the method options:

```ts
await mercator.createRun(
  {
    run_id: "run_123",
    workload: {
      workspace_id: "ws_1",
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

- runs: create, list, get, wait, refresh, cancel, events, decision
- placement preview
- workloads and workload revisions
- image resolution
- connections and offers
- secrets, versions, and grants
- sink status, delivery, and replay

The nested workload, offer, placement, event, and run shapes are pragmatic DTO
types matching the current JSON contract. The server OpenAPI document does not
fully specify every nested object field, so deeply nested adapter-specific data
remains intentionally permissive.
