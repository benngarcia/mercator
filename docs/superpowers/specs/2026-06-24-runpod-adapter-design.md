# RunPod Adapter Design

Date: 2026-06-24
Status: Approved (design phase)
Branch: `beng/v1-runpod-adapter` (to be created)

## Problem

Mercator now has real connections, a credential-resolving broker, workload
reporting (run tokens + `:report` ingest + SDKs), and a named cloudflare tunnel
verified end-to-end. The remaining gap to being a usable broker is a **real
cloud provider**. RunPod is the chosen first provider: container-native,
Bearer-auth REST API, GPU pricing.

RunPod's control plane **never exposes a container exit code** (confirmed across
both its REST and GraphQL surfaces — only a pod-level `desiredStatus` of
`RUNNING`/`EXITED`/`TERMINATED`). This is exactly the case workload reporting was
built for: the workload self-reports its exit code, and Mercator treats that
report as authoritative.

## Goals

- Add `runpod` as a real provider adapter implementing `adapter.Adapter`.
- Wire **reported-exit → authoritative outcome** in the orchestrator (the
  provider can't tell success from failure; the report can).
- Validate end-to-end with **two live runs** on the user's RunPod account:
  1. a `busybox` image reporting via **raw HTTP** (no SDK), and
  2. a `python` script reporting **custom events** via the Mercator Python SDK.

## Non-goals

- A GPU-aware scheduler beyond the existing accelerator-feasibility check.
- Serverless endpoints, network volumes, spot/interruptible pods, multi-GPU.
- Caching of `gpuTypes`/offers beyond what the offer service already does.
- Pod `stop`/`start`/resume — only create / observe / delete (terminate).

## Decisions made during brainstorming

- **Offer breadth:** query GraphQL `gpuTypes` for live price/availability, but
  advertise only a **configurable allow-list** of GPU types (default
  `["NVIDIA RTX A2000","NVIDIA RTX A4000"]`). Keeps the offer list small and the
  live test cheap.
- **busybox test:** proves the **raw-HTTP** `:report` contract (a shell
  one-liner curling the injected endpoint), distinct from the Python SDK test.
- **Cost guardrail:** cheapest community GPU, short runs, **auto-terminate on the
  exit report**; < $0.01 each.
- **Targeting RunPod for the tests:** the scheduler picks the lowest-cost
  *feasible* offer, and the docker loopback offer is priced at 0, so it would
  always win. The test workloads therefore declare a **GPU accelerator
  requirement**; docker advertises no accelerators (infeasible), leaving the
  RunPod offer as the only feasible candidate.

## RunPod API facts (grounding)

Verified against current RunPod docs (mid-2026):

- **REST** `https://rest.runpod.io/v1` is the preferred surface for pod
  lifecycle. **GraphQL** `https://api.runpod.io/graphql` is the **only** place
  for GPU catalog/pricing (`gpuTypes`). A real integration needs both.
- Auth: `Authorization: Bearer <RUNPOD_API_KEY>` for both, plus
  `Content-Type: application/json`.
- `POST /pods` body (object, not array, for `env`): `name` (free text, max 191
  chars, non-unique), `imageName`, `gpuTypeIds` (array of human-readable id
  strings), `gpuCount` (default 1), `containerDiskInGb` (default 50), `env`
  (`{KEY:value}` map), `ports` (array of `"<port>/<http|tcp>"`),
  `dockerEntrypoint` (array), `dockerStartCmd` (array), `cloudType`
  (`SECURE`|`COMMUNITY`). Returns a `Pod`.
- `GET /pods/{id}` and `GET /pods` (array) return `Pod` objects with `id`,
  `name`, `image`, `desiredStatus` (`RUNNING`|`EXITED`|`TERMINATED`),
  `publicIp`, `portMappings`, `costPerHr`, `env`, `lastStatusChange` (free
  text), etc. **No exit-code field anywhere.** `GET /pods` supports a `name`
  query filter and a `desiredStatus` filter.
- There is **no `provisioning` status enum**; readiness is inferred from
  `desiredStatus==RUNNING` **and** populated `publicIp`/`portMappings`.
- `DELETE /pods/{id}` → 204.
- **No arbitrary tags/labels** on pods. Ownership is encoded in the `name` field
  (and redundantly in `env`, which is returned).
- Cheapest GPU for smoke tests: `"NVIDIA RTX A2000"` (~$0.12/hr community),
  `"NVIDIA RTX A4000"` (~$0.17/hr) as a scheduling fallback. Live price +
  `lowestPrice.stockStatus` come from GraphQL `gpuTypes`.

## Architecture

### Package structure

```
internal/adapter/runpod/
  runpod.go         # Adapter: implements adapter.Adapter (Verify/ListOffers/Launch/Observe/Cancel/Release/Terminate/ListOwned)
  rest_client.go    # REST client: createPod/getPod/listPods/deletePod
  graphql_client.go # GraphQL client: gpuTypes() only
  offers.go         # gpuTypes -> []domain.OfferSnapshot (allow-list, pricing, accelerator inventory)
  runpod_test.go    # unit tests against a fake http.RoundTripper
examples/runpod/
  busybox-report/README.md   # raw-HTTP reporting workload (documented start command)
  python-sdk/run.py          # custom-event workload using the Mercator Python SDK
  python-sdk/README.md
docs/production/runpod.md     # operator runbook
```

Each file has one responsibility: REST transport, GraphQL transport, offer
mapping, and the adapter that composes them. Both clients accept an injectable
`http.RoundTripper` (default `http.DefaultTransport`) so unit tests run without
network access.

### Clients

Both clients are constructed with the resolved Bearer secret and a
`*http.Client`. Base URLs are overridable via connection config (for tests /
future regions) but default to the public RunPod endpoints.

- `restClient`:
  - `createPod(ctx, podCreate) (pod, error)`
  - `getPod(ctx, id) (pod, error)` (404 → `ErrNotFound`)
  - `listPodsByName(ctx, namePrefix) ([]pod, error)` (uses `?name=` filter, then
    a defensive client-side prefix check since `name` is non-unique and the
    filter is a contains/exact match per RunPod)
  - `deletePod(ctx, id) error` (204 or 404 → success/idempotent)
- `graphqlClient`:
  - `gpuTypes(ctx) ([]gpuType, error)` returning `{id, displayName, memoryInGb,
    communityPrice, securePrice, lowestPrice{stockStatus}}`

`pod` is an internal struct mapping the RunPod JSON fields we use.

### Offer mapping (`offers.go`, `ListOffers`)

1. Call `gpuTypes()`.
2. Keep ids in the allow-list (config `gpu_types`, comma-separated; default
   `NVIDIA RTX A2000,NVIDIA RTX A4000`) with non-empty stock
   (`lowestPrice.stockStatus` not "unavailable").
3. Map each to a `domain.OfferSnapshot`:
   - `Kind: OfferKindProvisionable` (⇒ disposition `terminate`).
   - `NativeRef: <gpuTypeId>`.
   - `Platform: {OS:"linux", Architecture:"amd64"}`.
   - `Pricing: {Currency:"USD", RatePerSecondUSD: communityPrice/3600,
     GranularitySeconds:1, Known:true}`.
   - `Resources.Accelerators`: one NVIDIA accelerator
     (`AcceleratorInventory{Vendor:"NVIDIA", Model:displayName, Count:1,
     MemoryBytes: memoryInGb*GiB}`) plus reasonable `CPUMillis`/`MemoryBytes`
     defaults so a GPU-requiring workload is feasible. Also set
     `Capabilities.Resources.GPUVendors:["NVIDIA"]`. (The matching requirement a
     workload declares is `ResourceRequirements.Accelerators` —
     `AcceleratorRequirement{Vendor:"NVIDIA", Count:1}`.)
   - `Capabilities.Container`: `SupportsDigestRefs:true`,
     `MaxEnvironmentBytes` sized for the reporting vars.
   - `Capacity: {Available:true}`, `ObservedAt/ExpiresAt` (short TTL).
4. The broker stamps `ConnectionID`/`AdapterType` on each offer (existing
   behavior).

If `gpuTypes()` fails, `ListOffers` returns the error; the broker already
isolates a broken connection from the aggregate list.

### Launch / ownership / observe / cleanup

**Ownership.** RunPod pods carry no tags, so:
- pod `name = "mercator-" + req.LaunchKey` (LaunchKey is unique per launch and
  well under 191 chars).
- pod `env` additionally carries `MERCATOR_OWNERSHIP_TOKEN`,
  `MERCATOR_REQUEST_HASH`, `MERCATOR_WORKSPACE_ID`, `MERCATOR_RUN_ID`,
  `MERCATOR_ATTEMPT_ID`, `MERCATOR_LAUNCH_KEY` — the same material docker stores
  in labels. Observe/Release/Terminate verify `MERCATOR_OWNERSHIP_TOKEN` +
  `MERCATOR_REQUEST_HASH` match before acting; mismatch → `ErrIdempotencyConflict`.

**Implementation note:** the shipped adapter verifies the ownership token only (not `MERCATOR_REQUEST_HASH`); it conflicts only on a positive token mismatch (token present and different), never on an absent token. The unique `mercator-<launchKey>` pod name is the primary ownership signal, and requiring both fields would risk false conflicts that orphan paid pods — the worse failure mode.

**Launch.** `createPod` with:
- `name`, `imageName: req.Image`, `gpuTypeIds: [req.SelectedOfferNativeRef,
  ...remaining allow-list]` (fallbacks improve community-stock scheduling odds),
- `cloudType: "COMMUNITY"` (config-overridable), `gpuCount:1`,
  `containerDiskInGb` (config default 20),
- `env`: the run's environment bindings + the Mercator reporting vars already
  injected by the orchestrator (`MERCATOR_RUN_ID`, `MERCATOR_REPORT_URL`,
  `MERCATOR_RUN_TOKEN`, `MERCATOR_WORKSPACE_ID`) + ownership material,
- `dockerEntrypoint: req.Entrypoint` (if set), `dockerStartCmd: req.Args`.

Returns `LaunchReceipt{ExternalID: pod.id, LaunchKey, OwnershipToken,
CleanupLocator, Phase: phaseFromPod(pod), AcceptedAt}`. A create that collides
on an existing `mercator-<launchKey>` pod is treated as a duplicate (idempotent),
verifying ownership env before returning `Duplicate:true`.

**Observe.** `listPodsByName("mercator-"+launchKey)`; if none → phase
`released` (pod gone). Verify ownership env. Map `desiredStatus`:
- `RUNNING` + populated `publicIp`/`portMappings` → `running`
- `RUNNING` without ip yet → `queued`
- `EXITED` → terminal (`succeeded` placeholder; real outcome comes from the
  report — see Outcome integration)
- `TERMINATED` → `released`/terminal
`ExitCode` is always `nil` (provider exposes none).

**Release / Terminate.** Resolve pod by name, verify ownership, `deletePod`.
`Release` and `Terminate` share the resolve+delete path; for `provisionable`
RunPod offers the orchestrator routes `Terminate`. (Both implemented; RunPod
does not return `ErrTerminateUnsupported`.)

**Cancel.** `CancelReceipt{Cancelled:true}` (best-effort; a queued pod that has
not started is deleted via the same resolve+delete path).

**ListOwned.** `GET /pods` filtered by name prefix `mercator-`; map each owned
pod's `env` back into `OwnedExternalObject` fields.

**Verify.** A cheap authenticated `GET /pods` (lists pods; 401 on a bad key).
Used by the connection authorize flow.

### Outcome integration (orchestrator change)

This is the one change outside the adapter package. It implements the spec's
outcome policy: *Observe polls for liveness; the authoritative exit comes from
the report; provider-exited-without-report → failed.*

Today `recordObservation` records `outcome_recorded` from the observed phase
when terminal. Change:

1. **Exit report drives outcome + cleanup.** When a `compute.run.reported.v1`
   event with a non-nil `exit_code` is ingested for a run that is **not yet
   outcome-recorded**, the orchestrator appends `outcome_recorded`
   (`exit_code==0` → `succeeded`, else → `failed`) **and** `cleanup_requested`
   (carrying the run's `launch_key`), then runs the existing release/close path.
   This terminates the RunPod pod promptly on the report rather than waiting for
   the next Observe poll. Implemented by extending `RecordReport` to project run
   state and, for exit reports, append the same terminal event sequence
   `recordObservation` uses (guarded by `!outcomeRecorded`, optimistic-concurrency
   retry-once as today).
2. **Observe remains the backstop.** When Observe sees a terminal phase and the
   run is not yet outcome-recorded: if a reported exit code exists in run state,
   use it for the outcome; otherwise (provider exited, no report) record
   `failed` (indeterminate). The existing terminal-observe branch is updated to
   consult run state's reported exit code before falling back to
   `outcomeForPhase`.

Both paths are idempotent on `outcomeRecorded`, so a report and an Observe that
race produce exactly one outcome.

### Factory + connection wiring (`cmd/mercator/main.go`)

- Register the adapter type:
  ```go
  factory.Register("runpod", func(config map[string]string, secret string) (adapter.Adapter, error) {
      return runpod.New(secret, config)
  })
  ```
- **No bootstrap connection.** The operator adds the RunPod connection through
  the existing Add Connection UI / `POST /v1/connections` with
  `adapter_type:"runpod"` and `credential:{source:"env", ref:"RUNPOD_API_KEY"}`
  (or a `mercator` stored secret), then **Authorize** (runs `Verify`).
- `runpod.New(secret, config)` reads optional config keys: `gpu_types`,
  `cloud_type`, `container_disk_gb`, `rest_base_url`, `graphql_base_url`.

### Live test workloads (`examples/runpod/`)

Both declare a GPU accelerator requirement so only the RunPod offer is feasible.

1. **busybox-report** — image `busybox`, start command a single
   `sh -c 'wget/curl ... :report ...'` (busybox ships `wget`; we use a small
   `wget --post-data` or an installed `curl`, documented in the README). Emits a
   `progress` event then `{type:"exit", exit_code:0}` against
   `${MERCATOR_REPORT_URL}/v1/runs/${MERCATOR_RUN_ID}:report?workspace_id=${MERCATOR_WORKSPACE_ID}`
   with `Authorization: Bearer ${MERCATOR_RUN_TOKEN}`.
2. **python-sdk** — image `python:3-slim`, `run.py` uses the Mercator Python SDK
   (`from mercator import run_reporter`) to emit two custom event types
   (e.g. `model.loaded`, `progress`) and report exit via the context manager.

The runbook documents launching each (operator adds the connection, authorizes,
creates the run with the accelerator requirement, watches the Events tab,
confirms the pod auto-terminates).

## Error handling

- HTTP non-2xx from RunPod → wrapped error including status + body snippet
  (never the Bearer key). 401 from `Verify` → authorize fails with a clear
  message; 404 on get/delete → treated as not-found/idempotent success.
- `gpuTypes()` failure → `ListOffers` error (broker isolates it).
- Ownership mismatch on Observe/Release/Terminate → `ErrIdempotencyConflict`.
- Provider exited with no exit report → run outcome `failed` (indeterminate),
  surfaced as a reason code.

## Testing

**Unit (no network), `runpod_test.go`:**
- Fake `http.RoundTripper` returns canned RunPod JSON.
- Offer mapping: `gpuTypes` → offers filtered by allow-list + stock; pricing and
  accelerator inventory correct; unavailable stock excluded.
- Launch: request body shape (name, gpuTypeIds incl. fallbacks, env map incl.
  reporting + ownership vars, dockerStartCmd); duplicate-name idempotency.
- Observe: `desiredStatus`/`publicIp` → phase mapping; ownership mismatch →
  conflict; missing pod → released.
- Terminate/Release: resolve-by-name + delete; 404 idempotent.
- Verify: 200 → nil, 401 → error.

**Orchestrator (`orchestrator_test.go`):**
- Exit report on a live run → `outcome_recorded` (succeeded/failed by code) +
  `cleanup_requested`; pod terminate routed.
- Terminal Observe with a prior reported exit code → outcome from the code.
- Terminal Observe with no report → `failed` (indeterminate).
- Report and Observe race → exactly one outcome.

**Live e2e (manual, documented in the runbook):**
- Real account; `RUNPOD_API_KEY` via env only (never committed/logged); through
  the `bucket.bot` tunnel.
- busybox run → progress + exit events land, outcome `succeeded`, pod
  auto-terminated.
- python-sdk run → custom events + exit land, outcome `succeeded`, pod
  auto-terminated.

## Secrets handling

The RunPod API key is only ever passed via the `RUNPOD_API_KEY` env var
(resolved through the existing credential resolver). It is never written to the
repo, the event log, read APIs, or logs. Error messages include status codes and
truncated bodies but never the Authorization header. After live testing the user
rotates the key.

## Rollout / order of work

1. RunPod REST + GraphQL clients (unit-tested against a fake transport).
2. Offer mapping.
3. Adapter lifecycle (launch/observe/ownership/cleanup/verify).
4. Orchestrator outcome integration (report-driven + Observe backstop).
5. Factory registration + runbook.
6. Example workloads.
7. Live e2e on the user's account.
