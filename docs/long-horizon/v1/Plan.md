# Mercator OCI Run Broker V1 Plan

This plan implements the actual Mercator V1 target, not the current foundation slice. The first checkpoints repair the 2026-06-20 audit findings before any expansion to Docker, secrets, sinks, or UI.

## Operating Rules

- Source of truth: `docs/long-horizon/v1/Prompt.md`.
- Runbook: `docs/long-horizon/v1/Implement.md`.
- Live audit/status: `docs/long-horizon/v1/Documentation.md`.
- Do not revert unrelated edits. Inspect `git status --short` before and after each milestone.
- Every milestone uses small TDD loops: write focused failing test, run focused test, implement, rerun focused test, run package tests, run full validation.
- Stop immediately on failed validation, unexpected dirty files, public secret exposure, non-deterministic scheduler output, or side effects without durable intent events.
- SQLite remains the only internal source-of-truth event backend.
- No Docker adapter, secret vault, event sinks, or UI work may begin until Milestone 5 passes.

## Global Validation

Run after every milestone unless the milestone says otherwise:

```sh
go test ./...
go build ./...
git status --short
```

Update `docs/long-horizon/v1/Documentation.md` after each milestone with completed scope, validation output, known gaps, and any deferred decisions.

## Milestone 0: Baseline And Audit Lock

**Dependency:** none.

**Purpose:** Freeze the current scaffold state and make the audit findings executable.

**File areas:**
- `docs/long-horizon/v1/Documentation.md`
- Existing tests under `internal/...`

**Loops:**
1. Add an audit checklist section mapping every high/medium finding from `docs/superpowers/status/2026-06-20-oci-run-broker-v1-agent-verification.md` to a milestone below.
2. Add or identify regression tests that currently fail for the highest-risk invariants: public env redaction, replay-safe `AdvanceRun`, launch-intent recovery, fake adapter launch-key conflict, deterministic scheduling across offer order.
3. Do not change production behavior in this milestone.

**Acceptance criteria:**
- Each audit finding has an owner milestone.
- Known failing tests are documented with exact commands.
- No V1 expansion work is started.

**Validation:**
```sh
go test ./... || true
go build ./...
git status --short
```

**Stop/fix gate:** If baseline does not build, fix build breakage before adding any new behavior.

## Milestone 1: Public Event Redaction And Workload Boundary Hardening

**Dependency:** Milestone 0.

**Purpose:** Repair public data leakage and harden OCI-only validation before touching orchestration or adapters.

**File areas:**
- `internal/domain/types.go`
- `internal/domain/validation.go`
- `internal/domain/domain_test.go`
- `internal/orchestrator/orchestrator.go`
- `internal/orchestrator/orchestrator_test.go`
- `internal/httpapi/server.go`
- `internal/httpapi/server_test.go`

**Loops:**
1. Add failing tests proving `RunRequested` public CloudEvents do not contain literal env values or secret references with provider-sensitive metadata.
2. Split public event data from private/internal event data for workload revisions.
3. Add failing validation tests for non-empty `WorkloadSpec.Raw`, empty env bindings, invalid env names, invalid ports, oversized env data, and workspace mismatch.
4. Implement exact validation errors with stable machine-readable codes.
5. Add HTTP tests proving API error responses never echo literal env values.

**Acceptance criteria:**
- Secret values and literal env values never appear in public events, logs, errors, read models, or API responses.
- V1 still accepts exactly one Linux container named `main`.
- Unsupported OCI extensions fail closed.

**Validation:**
```sh
go test ./internal/domain -count=1
go test ./internal/orchestrator -run 'Redaction|Workload|Secret' -count=1
go test ./internal/httpapi -run 'Redaction|Workspace|Validation' -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** If any test can find a literal env value in public output, stop and repair before continuing.

## Milestone 2: Deterministic Scheduler And Conservative Facts

**Dependency:** Milestone 1.

**Purpose:** Repair scheduler correctness before more runtime paths depend on placement.

**File areas:**
- `internal/scheduler/scheduler.go`
- `internal/scheduler/scheduler_test.go`
- `internal/domain/types.go`
- `internal/domain/domain_test.go`

**Loops:**
1. Add failing tests for accelerator/GPU requirements, `MaxContainers == 0`, unavailable capacity evidence, unknown image-cache facts, and `MaxExpectedCostUSD`.
2. Add order-independence tests proving identical offer sets produce identical candidate ordering, decision hash, and selected offer.
3. Add tests for start-failure, interruption, and uncertainty penalties.
4. Implement stable offer sorting before evaluation and include connection, adapter type, and native ref in candidate audit data.
5. Populate `CollectionReport` deterministically.

**Acceptance criteria:**
- Unknown facts fail hard requirements unless explicitly allowed.
- Candidate order and decision ID are stable for identical input sets regardless of input order.
- Accelerator requirements are enforced.
- Cost caps and configured penalties affect feasibility/scoring.

**Validation:**
```sh
go test ./internal/scheduler -count=1
go test ./internal/domain -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** If scheduler output changes when input offers are shuffled, stop and fix determinism.

## Milestone 3: Complete Adapter Contract And Fake Adapter Idempotency

**Dependency:** Milestone 2.

**Purpose:** Repair adapter launch semantics before orchestrator replay and Docker work.

**File areas:**
- `internal/adapter/adapter.go`
- `internal/adapter/fake/fake.go`
- `internal/adapter/fake/fake_test.go`
- `internal/orchestrator/orchestrator.go`
- `internal/orchestrator/orchestrator_test.go`

**Loops:**
1. Add failing tests proving `LaunchRequest` carries full OCI workload: image digest, platform, entrypoint, args, ports, resources, selected offer/native ref, launch key, ownership token, and secret binding descriptors without secret values.
2. Add failing fake-adapter test proving a different operation key cannot overwrite an existing object with the same launch key and ownership token but different request hash.
3. Add explicit adapter errors for accepted, conflict, timeout, indeterminate, not found, and retryable failure classes.
4. Update orchestrator launch-intent payload to use the complete adapter contract.
5. Verify adapter receipts expose cleanup locators and ownership tokens.

**Acceptance criteria:**
- Every external side effect has deterministic launch key, ownership token, operation key, request hash, and cleanup locator.
- Fake adapter cannot overwrite existing launch-key objects.
- Adapter contract is sufficient for Docker without hidden workload lookups.

**Validation:**
```sh
go test ./internal/adapter/... -count=1
go test ./internal/orchestrator -run 'Launch|Adapter|Intent' -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** If launch request hashing excludes any side-effect-bearing field, stop and fix the contract.

## Milestone 4: Event-Log Authoritative Orchestrator And Reconciler Core

**Dependency:** Milestone 3.

**Purpose:** Repair replay, recovery, cleanup, and ambiguous launch handling.

**File areas:**
- `internal/orchestrator/orchestrator.go`
- `internal/orchestrator/orchestrator_test.go`
- `internal/reconciler/reconciler.go`
- `internal/reconciler/reconciler_test.go`
- `internal/domain/types.go`

**Loops:**
1. Add failing tests for replaying `AdvanceRun` after nonterminal observation: it must observe existing launch, not relaunch.
2. Add failing tests for durable `LaunchIntentRecorded` recovery when current offers have changed or disappeared.
3. Add failing tests for cleanup retry after `CleanupRequested` without returning to placement/launch.
4. Add failing tests for launch timeout and indeterminate outcomes requiring `Observe` or `ListOwned` reconciliation before any retry.
5. Implement a run state reducer from event streams.
6. Implement `Reconciler.AdvanceRun` over reducer state, with command handlers for observe, cancel, release, and retry eligibility.
7. Ensure no run closes before cleanup is confirmed.

**Acceptance criteria:**
- Public state is derived only from the event log.
- Existing launch intent is authoritative over later offer snapshots.
- Ambiguous launch outcomes never cause blind duplicate launches.
- Cleanup is retried from cleanup state, not from launch state.

**Validation:**
```sh
go test ./internal/orchestrator -count=1
go test ./internal/reconciler -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** If any replay path produces duplicate fixed event IDs or a second launch side effect, stop and repair before continuing.

## Milestone 5: Runnable Fast Path, API Repair, And OpenAPI Completeness

**Dependency:** Milestone 4.

**Purpose:** Finish audit repair before expanding to Docker, secrets, sinks, or UI.

**File areas:**
- `cmd/mercator/main.go`
- `internal/httpapi/server.go`
- `internal/httpapi/openapi.go`
- `internal/httpapi/server_test.go`
- `internal/projection/...`
- `internal/orchestrator/...`

**Loops:**
1. Add failing integration test proving `POST /v1/runs` performs the advertised fast path through placement, launch intent, adapter launch, observation, and cleanup when configured with fake adapter/offers.
2. Add missing run endpoints: cancel, get, list, wait, events, decision, refresh, and links.
3. Replace default `ws_1` behavior with explicit workspace scoping.
4. Return machine-readable `409` idempotency conflicts for key reuse with different request hashes.
5. Expand OpenAPI with request bodies, response schemas, error schema, event schemas, and conflict responses.
6. Add disposable read projections for run list/get/decision.
7. Wire production server to a configured adapter registry, not an unlaunchable static adapter.

**Acceptance criteria:**
- Advertised broker fast path is runnable through HTTP using the fake adapter.
- Required V1 run endpoints exist and are covered by tests.
- OpenAPI describes real request/response/error shapes.
- Workspace isolation is explicit at every API boundary.
- All high and medium audit findings are either fixed or explicitly deferred in `Documentation.md` with a V1-blocking label.

**Validation:**
```sh
go test ./internal/httpapi -count=1
go test ./internal/projection/... -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** Do not start Docker, secrets, sinks, or UI until this milestone is green and the audit checklist marks repair complete.

## Milestone 6: Workload Service And OCI Image Resolver

**Dependency:** Milestone 5.

**Purpose:** Add immutable workload revisions and tag-to-digest resolution.

**File areas:**
- `internal/workload/...`
- `internal/ociresolver/...`
- `internal/httpapi/...`
- `internal/domain/...`

**Loops:**
1. Add workload create/revise/get/list tests.
2. Add resolver tests for digest-pinned no-op, tag resolution to digest, platform metadata, and resolver failure.
3. Append immutable workload revision events before runs reference them.
4. Ensure runs reference revision IDs and digests, not mutable inline specs.

**Acceptance criteria:**
- Workload revisions are immutable and event-backed.
- Runs reference exact revisions.
- Tag inputs are resolved before placement; placement only sees digest-pinned images.

**Validation:**
```sh
go test ./internal/workload/... -count=1
go test ./internal/ociresolver/... -count=1
go test ./internal/httpapi -run 'Workload|Image' -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** If any placement path can use a mutable tag, stop and repair.

## Milestone 7: Connections, Offers, Authorization, And Latency Estimation

**Dependency:** Milestone 6.

**Purpose:** Replace ad hoc offers with event-backed connection and offer services.

**File areas:**
- `internal/connection/...`
- `internal/offers/...`
- `internal/authz/...`
- `internal/latency/...`
- `internal/scheduler/...`
- `internal/httpapi/...`

**Loops:**
1. Add connection CRUD tests with adapter-defined authorization schemas.
2. Add offer snapshot ingest/cache tests with expiry and disposable rebuild.
3. Add authorization tests for workspace-scoped run, workload, connection, and secret operations.
4. Add latency estimator tests feeding scheduler estimates.
5. Wire scheduler input from offer snapshots and latency estimates.

**Acceptance criteria:**
- Offer cache is disposable and rebuilt from events/adapter observations.
- Authorization is enforced before mutations and reads.
- Scheduler decisions include offer collection provenance.

**Validation:**
```sh
go test ./internal/connection/... -count=1
go test ./internal/offers/... -count=1
go test ./internal/authz/... -count=1
go test ./internal/latency/... -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** If a read model becomes source of truth, stop and move authority back to events.

## Milestone 8: Docker Host Adapter

**Dependency:** Milestone 7.

**Purpose:** Add the first real runtime adapter after core invariants are repaired.

**File areas:**
- `internal/adapter/docker/...`
- `internal/adapter/registry/...`
- `internal/httpapi/...`
- `cmd/mercator/...`

**Loops:**
1. Add unit tests with a fake Docker client for launch, observe, cancel, release, list-owned, labels, ports, env, entrypoint, args, platform, resources, and cleanup locator.
2. Add integration tests behind an explicit Docker guard.
3. Map Docker labels from workspace, run, attempt, launch key, and ownership token.
4. Ensure Docker launch is idempotent using deterministic names/labels.
5. Wire adapter registry config.

**Acceptance criteria:**
- Docker adapter implements the complete adapter contract.
- `ListOwned` can reconcile orphaned/ambiguous launches.
- No unsupported Docker flags leak into the OCI workload boundary.

**Validation:**
```sh
go test ./internal/adapter/docker -count=1
go test ./internal/adapter/... -count=1
docker version
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run Integration -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** If Docker is unavailable, skip only the guarded integration command and record the skip. Unit tests and full build must still pass.

## Milestone 9: Secret Vault And Grants

**Dependency:** Milestone 8.

**Purpose:** Add encrypted event-backed secret versions and scoped runtime grants.

**File areas:**
- `internal/secrets/...`
- `internal/domain/...`
- `internal/adapter/...`
- `internal/adapter/docker/...`
- `internal/httpapi/...`

**Loops:**
1. Add tests for encrypted secret version events and grant events.
2. Add tests proving secret plaintext never appears in public events, read models, logs, errors, OpenAPI examples, or adapter receipts.
3. Implement secret resolution only at launch boundary.
4. Pass secret binding descriptors through placement and adapter contracts without plaintext.
5. Implement grant revocation and cleanup behavior.

**Acceptance criteria:**
- Secret versions are encrypted in event private data.
- Public surfaces expose only stable references.
- Runtime adapters receive secrets only through explicit launch-time resolution.

**Validation:**
```sh
go test ./internal/secrets/... -count=1
go test ./internal/httpapi -run Secret -count=1
go test ./internal/adapter/... -run Secret -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** If a grep or test finds plaintext secret material in public output, stop and repair before continuing.

## Milestone 10: Projection Runner And CLI

**Dependency:** Milestone 9.

**Purpose:** Add durable projection operation and JSON-first CLI behavior.

**File areas:**
- `internal/projection/...`
- `internal/cli/...`
- `cmd/mercator/...`
- `internal/httpapi/...`

**Loops:**
1. Add projection runner tests for replay from global position and disposable rebuild.
2. Add CLI tests for create run, cancel, get/list, wait, events, decision, refresh, and JSON output.
3. Add wait behavior over event subscription/read-all fallback.
4. Add command idempotency flags and conflict reporting.

**Acceptance criteria:**
- CLI is agent-native and JSON-first.
- Projection offsets are durable where needed and rebuildable where disposable.
- `Subscribe` uses stored offsets correctly.

**Validation:**
```sh
go test ./internal/projection/... -count=1
go test ./internal/cli/... -count=1
go test ./cmd/mercator/... -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** If CLI output is not parseable JSON for machine workflows, stop and repair.

## Milestone 11: Event Sinks, Replay, And Durable Cursors

**Dependency:** Milestone 10.

**Purpose:** Add webhooks, Kafka, Postgres, replay, and sink failure isolation.

**File areas:**
- `internal/sinks/...`
- `internal/eventlog/...`
- `internal/httpapi/...`

**Loops:**
1. Add sink cursor tests for delivery, ack, retry, replay, and restart.
2. Add sink failure tests proving placement and lifecycle continue when sinks fail.
3. Implement webhook sink.
4. Implement Kafka sink behind configuration.
5. Implement Postgres sink behind configuration.
6. Add replay API/CLI for sinks.

**Acceptance criteria:**
- Sink failures never block placement or lifecycle progress.
- Cursors are durable per sink.
- Replay is bounded, observable, and idempotent.

**Validation:**
```sh
go test ./internal/sinks/... -count=1
go test ./internal/eventlog -run 'Subscribe|Ack|Cursor' -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** If a sink failure can fail a run lifecycle command, stop and isolate sink execution.

## Milestone 12: Embedded Static UI

**Dependency:** Milestone 11.

**Purpose:** Add a small operational UI over existing APIs.

**File areas:**
- `web/...`
- `internal/httpapi/...`
- `cmd/mercator/...`

**Loops:**
1. Add API-backed UI views for run list, run detail, events, placement decision, connections, offers, and sink status.
2. Add build/embed tests.
3. Add browser smoke test for empty state, populated run, and error state.
4. Keep UI read-oriented except explicit cancel/refresh controls.

**Acceptance criteria:**
- UI is embedded in the single Go process/image.
- UI depends on public APIs, not private state.
- UI exposes no secret values.

**Validation:**
```sh
go test ./internal/httpapi -run UI -count=1
go test ./...
go build ./...
```

**Stop/fix gate:** If UI needs private event data or secret values, stop and redesign the API/view model.

## Milestone 13: V1 End-To-End Release Gate

**Dependency:** Milestones 0-12.

**Purpose:** Prove actual V1 completeness against the frozen prompt.

**Loops:**
1. Run the complete test/build suite.
2. Run an end-to-end fake-adapter run through HTTP and CLI.
3. Run an end-to-end Docker-adapter run when Docker is available.
4. Run secret redaction checks across events, read models, API responses, logs, and UI.
5. Run sink failure/replay tests.
6. Review `Prompt.md` done-when line by line and update `Documentation.md` with evidence.

**Acceptance criteria:**
- All prompt load-bearing requirements are implemented or explicitly marked out of V1 with user-approved scope change.
- No known high/medium audit findings remain open.
- README/status/API language matches real behavior.
- One Go process, embedded SQLite event log, in-process adapters, REST/OpenAPI API, CLI, sinks, Docker adapter, secrets, and UI are all verified.

**Validation:**
```sh
go test ./...
go build ./...
go test -race ./internal/eventlog ./internal/orchestrator ./internal/reconciler ./internal/sinks/...
MERCATOR_E2E_FAKE=1 go test ./... -run E2E -count=1
docker version
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run Integration -count=1
git status --short
```

**Stop/fix gate:** Do not call V1 complete while any required invariant lacks passing validation evidence in `Documentation.md`.
