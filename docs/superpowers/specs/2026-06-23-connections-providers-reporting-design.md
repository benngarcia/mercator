# Connections, Providers, Workload Reporting, and a Usable Sink

Date: 2026-06-23
Status: Approved (design phase)
Branch: `beng/v1-provider-connections`

## Problem

Mercator V1 ships two adapters (`fake`, `docker`), runs exactly **one** adapter
selected by `MERCATOR_ADAPTER`, exposes connections as **read-only** (and only
the loopback docker connection, derived from offers), has a **discard** sink,
and has **no real provider**. To become a usable broker we need: real
connections you can add/authorize, more than one connection at a time, a real
provider (RunPod), a usable event sink, and a way to learn a workload's true
exit status on providers whose control plane doesn't expose it.

## Goals

- Make **connections first-class**: add, authorize, and run across **multiple**
  connections, from the API and UI.
- Add **RunPod** as the first real provider adapter.
- Make sinks **usable** (webhook delivery), not a discard stub.
- Let workloads **report events back to Mercator** (incl. exit code) via the
  SDK or raw HTTP — the provider-agnostic answer to control planes that don't
  surface exit codes (RunPod).
- Stay consistent with **ADR 0001** (Mercator does not store secrets).

## Non-goals

- Mercator-owned encrypted secret storage / KMS (ADR 0001). Credentials are
  *referenced*, never stored.
- Multi-tenant runtime self-service of credentials (single-operator for now;
  the credential-source seam leaves room for `vault`/`aws-secrets` later).
- Connection delete/deauthorize endpoints (YAGNI for now).
- The `fake` adapter as a shippable provider (kept only as a test double).

## Decisions made during brainstorming

- **Credential model: env-referenced, behind a `credential_source` seam.** A
  connection record holds `{ source: "env", ref: "<ENV_VAR>" }` — a *reference*,
  never the secret. The event log therefore never contains a secret (a
  Mercator-specific hard constraint: the log is append-only, replayable, and
  sink-streamed). `EnvSource` is the only source now; `vault`/`aws-secrets` are
  future sources behind the same interface. Single-operator positioning.
- **Fake adapter:** drop as a runtime offer source (remove the
  `MERCATOR_FAKE_OFFER` path; do not register `fake` in the production factory).
  Keep `internal/adapter/fake` as the test double the httpapi suite depends on.
- **RunPod exit codes:** RunPod's API has no exit code. Rather than a bespoke
  sidecar, the workload **reports** its exit via the SDK/HTTP contract. If a pod
  exits with no report, the run is marked failed/indeterminate (success cannot
  be confirmed).
- **Reachability:** workload reporting requires Mercator to be reachable from
  the workload, i.e. a public URL (matches the intended "deploy to a URL"
  model). Live RunPod testing locally needs a tunnel; the connection plane and
  sink are fully local-verifiable.

## Build order (each independently shippable + verified)

1. Connection plane (factory + Broker + connection management + env-ref).
2. Webhook sink.
3. Workload reporting (run tokens + env injection + `:report` ingest + SDKs).
4. RunPod adapter (consumes #3 for exit codes).

---

## 1. Connection plane

### Credential model

`internal/connection` `Record` gains:
- `config map[string]string` — non-secret provider config (endpoint, region…).
- `credential struct { Source string; Ref string }` — e.g. `{env, MERCATOR_CONN_RUNPOD_MAIN_KEY}`.

A new `CredentialResolver` interface resolves `{source, ref}` → secret at use
time:
```go
type CredentialResolver interface { Resolve(ctx, src Credential) (string, error) }
```
Only `EnvSource` (reads `os.Getenv(ref)`) now. The resolved secret is passed to
the adapter factory; it is never persisted or logged.

### Adapter factory + Broker (core change)

Today the server holds one `adapter.Adapter`. Introduce:

- **`AdapterFactory`**: `adapter_type → func(config map[string]string, secret string) (adapter.Adapter, error)`. Registered: `docker`, `runpod`. (`fake` only in tests.)
- **`Broker`** (implements `adapter.Adapter` by routing): for a workspace it
  lists **authorized** connections, lazily builds + caches each connection's
  adapter via the factory + resolver, and:
  - `ListOffers` → **aggregates** offers across connections; each offer is
    stamped with its `connection_id` (adapters already set this, or the Broker
    enforces it).
  - `Launch / Observe / Cancel / Release / Terminate / ListOwned` → **routes**
    to the adapter of the run's connection. The run records the selected offer,
    which carries `connection_id` + `adapter_type`; the Broker dispatches on it.
- The orchestrator's single `adapter.Adapter` becomes the Broker. The
  orchestrator keeps calling the interface unchanged.

### Bootstrap connection (no regression)

At startup the env-configured docker adapter is registered as a bootstrap
connection (e.g. `conn_docker_loopback`, authorized, `adapter_type=docker`,
config from the existing `MERCATOR_DOCKER_*` env). This **replaces** the
"derive connections from offers" shim (commit 218d43c) with a real registry;
the Connections page now lists registry connections.

### Adapter interface addition

Add `Verify(ctx) error` — a cheap credential/reachability check used by the
authorize flow. docker/fake implement trivially (docker: `docker info` via the
endpoint; fake: nil).

### Connection management API

- `POST /v1/connections` — create `{workspace_id, connection_id, adapter_type, config, credential: {source, ref}}` (extends `connection.Service.Create`). Idempotency-Key required (mutation).
- `POST /v1/connections/{id}:authorize` — resolve the credential, build the
  adapter, call `Verify`; on success record `authorized=true`
  (`connection.Service.UpdateAuthorization`); on failure stay unauthorized and
  surface the error.
- `GET /v1/connections` — exists; now returns registry records.

### UI

- Connections page: **"Add connection"** dialog — adapter_type `Select` →
  dynamic config fields + credential env-var name input; **Authorize** action
  per row with validation feedback (pending / authorized / error). Offers and
  runs now span multiple connections.

### Testing

- `Broker`: unit tests with two fake connections → aggregated offers; lifecycle
  routes to the correct connection's adapter.
- `CredentialResolver`/`EnvSource`: unit tests.
- httpapi: create → authorize (with a fake `Verify`) → list reflects authorized.

---

## 2. Webhook sink

Make the existing `sinks.WebhookSink` usable:
- Configured via env: `MERCATOR_SINK_WEBHOOK_URL` (+ optional
  `MERCATOR_SINK_WEBHOOK_AUTH_HEADER`). When set, it replaces the `audit`
  `DiscardSink`.
- A background **delivery loop** advances the sink cursor and POSTs each public
  event to the URL (at-least-once; cursor/replay machinery already exists).
  Failures stop at the failed position for retry/replay (existing semantics).
- UI Sinks page shows the configured sink + cursor advancing; manual
  deliver/replay already wired.

### Testing

- Unit test `WebhookSink.Deliver` against an `httptest.Server` (payload shape,
  auth header, error propagation).
- Delivery-loop test: append events → loop delivers in order, cursor advances,
  a 5xx halts at the right position.

---

## 3. Workload reporting

Let a workload report its own lifecycle/exit back to Mercator.

### Run-scoped identity (injected at launch)

When the orchestrator launches a run, Mercator injects literal env into the
container (ADR 0001-consistent):
- `MERCATOR_RUN_ID`
- `MERCATOR_REPORT_URL` (Mercator's public base URL)
- `MERCATOR_RUN_TOKEN` — a run-scoped bearer token authorizing **only**
  reporting for that run, minted per-run (short-lived; HMAC of run id + secret,
  or a stored opaque token keyed to the run).

### Ingest endpoint

- `POST /v1/runs/{id}:report` — authed by the run token (not the operator
  token). Body: `{ type, data?, exit_code? }`.
  - Generic events → `compute.run.reported.v1` on the run stream (visible in
    the Events timeline).
  - `reportExit(code)` → authoritative outcome (`outcome_recorded` with the
    exit code), which closes the success/failure ambiguity for providers like
    RunPod.
- Plain JSON contract — any workload can `curl` it.

### SDK reporters

- `sdk/typescript` + `sdk/python` gain a small reporter that reads the injected
  env and exposes `report(event)` / `reportExit(code)` (and a context-manager /
  `try-finally` helper that reports exit automatically). Thin wrapper over the
  HTTP contract.

### Outcome policy

`Observe` still polls the provider for liveness. The authoritative exit comes
from the workload's report. If the provider shows the workload exited but no
report arrived, the run is marked failed/indeterminate.

### Testing

- httpapi: a run token reports an event → appears on the run stream; reports
  exit → outcome recorded; an operator token cannot report; a run token cannot
  act on a different run.
- Token mint/verify unit tests.

---

## 4. RunPod adapter (`internal/adapter/runpod`)

Implements `adapter.Adapter` over RunPod (REST `rest.runpod.io/v1` + GraphQL for
pricing), bearer auth from the resolved credential. HTTP client is injectable
for tests.

- `ListOffers` → GraphQL `gpuTypes` → `OfferSnapshot`s: GPU accelerators
  (vendor/model/count/mem), `$/hr` pricing (secure/community + spot), platform
  `linux/amd64`, `kind = provisionable`.
- `Launch` → `POST /v1/pods` (`imageName` digest, `env` incl. the injected
  reporting vars, `dockerEntrypoint`/`dockerStartCmd`, `gpuTypeIds`,
  `gpuCount`). Owner marker encoded in pod `name` = `mercator-<ws>-<runid>`.
  Private images: pre-register via `POST /v1/containerregistryauth`.
- `Observe` → `GET /v1/pods/{id}`; map `desiredStatus` (RUNNING→running,
  EXITED→terminal, TERMINATED→released). Exit code comes from the workload
  report, not RunPod.
- `Terminate` → `DELETE /v1/pods/{id}`. RunPod pods are provisioned and owned,
  so runs on RunPod record `disposition=terminate` and cleanup destroys the pod.
  `Release` (remove only our job from a pool we don't own) does not apply to
  RunPod; it returns the standing-pool error.
- `ListOwned` → `GET /v1/pods?name=mercator-<ws>-` + client-side filter.
- `Verify` → cheap authenticated GET.

### Testing

- Unit tests against a faked HTTP client with recorded RunPod responses
  (offers mapping, launch body, observe status mapping, list-owned filter).
- A live integration test gated on `RUNPOD_API_KEY` (mirrors the guarded docker
  integration test). End-to-end live runs also need a tunnel for reporting.

---

## Risks / honesty

- **RunPod live verification needs a real `RUNPOD_API_KEY` + a few cents + a
  tunnel** (for the workload to reach Mercator's report URL). Build + unit
  tests are complete without it; the connection plane and sink are 100%
  locally verifiable; reporting is verifiable with docker.
- **Broker is the largest change** — it moves the server from single-adapter to
  multi-connection routing. Mitigated by keeping the `adapter.Adapter` interface
  and bootstrapping the docker connection so existing behavior is preserved.
- Lands as **4+ commits** (plane → sink → reporting → RunPod), each verified.
