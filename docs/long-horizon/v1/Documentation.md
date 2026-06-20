# Mercator V1 Documentation

This file is the live status and audit log for the long-horizon Mercator V1 OCI run broker effort.

Structure follows the OpenAI long-horizon Codex pattern: `Prompt.md` freezes target/spec/done-when, `Plan.md` defines checkpointed milestones, `Implement.md` is the runbook, and this file records live status, decisions, verification, gaps, and handoff state.

Reference: https://developers.openai.com/blog/run-long-horizon-tasks-with-codex

## Current Branch Status

Initialized: 2026-06-20
Workspace: `/Users/beng/Work/mercator/.worktrees/v1-run-broker`
Branch: `beng/v1-run-broker`
Current HEAD before long-horizon docs: `f73665c9dead1cc9ba20ee7e200dcd4e206d54ee`
Reviewed baseline in agent verification: `76d05b1d5397fbc0af3e9b1c774d1925d3dc0c13`

Git status at initialization:

```text
## beng/v1-run-broker
?? docs/long-horizon/
```

## Source Inputs

Read before initializing this file:

- `docs/superpowers/status/2026-06-20-oci-run-broker-v1-agent-verification.md`
- `docs/superpowers/status/2026-06-20-oci-run-broker-v1.md`
- `docs/superpowers/specs/2026-06-20-oci-run-broker-v1-verification-brief.md`
- `docs/long-horizon/v1/README.md`
- OpenAI Developers blog: `https://developers.openai.com/blog/run-long-horizon-tasks-with-codex`

## Current Assessment

The current branch is a tested foundation scaffold, not a complete Mercator V1 OCI run broker.

Implemented foundation areas include SQLite event log basics, domain validation basics, deterministic scheduler basics, fake adapter basics, a narrow internal orchestrator happy path, minimal REST API/OpenAPI, and `cmd/mercator`.

The branch does not yet satisfy several load-bearing V1 invariants from the original prompt. Future agents must avoid treating current passing tests as proof of V1 completeness.

## Audit Findings Summary

High severity findings from the verification pass:

- Executable server cannot run the advertised broker fast path. `POST /v1/runs` only appends `RunRequested`; production wiring has no usable offers or launch adapter.
- `AdvanceRun` is not replay-safe after partial progress and can replay launch attempts.
- Recovery after durable `LaunchIntentRecorded` is not event-log authoritative.
- Launch timeout and indeterminate behavior collapse into `LaunchFailed` without required observe/ListOwned reconciliation.
- Adapter launch contract does not carry the full OCI workload or placement metadata.
- Public events and event APIs can expose literal environment values.
- Scheduler ignores accelerator/GPU requirements.
- Fake adapter idempotency is incomplete across different operation keys with the same launch key.
- Placement determinism depends on input offer order.
- Required V1 run endpoints are missing: cancel, get/list, wait, decision, refresh, links.
- API idempotency conflict behavior is not represented as a machine-readable conflict.

Medium severity findings from the verification pass:

- Cleanup retry/no-orphan behavior is not robust.
- Unknown or dishonest capability facts can pass feasibility checks.
- Price constraints and policy penalties are incomplete.
- Placement preview bypasses workload validation and can panic on empty containers.
- Decision audit contents are incomplete.
- OpenAPI is materially incomplete.
- Workspace isolation at the HTTP boundary is weak.
- Subscription offsets are written but not used by subscribe.
- OCI-only validation is not hardened enough.
- Known gaps under-report major missing V1 areas.

## Audit Finding Ownership Checklist

High and medium findings from `docs/superpowers/status/2026-06-20-oci-run-broker-v1-agent-verification.md` are locked here so later milestones have explicit repair owners.

| Status | Severity | Finding | Owner milestone |
| --- | --- | --- | --- |
| Fixed in M5 | High | Executable server cannot run the advertised broker fast path | M5 |
| Fixed in M4 | High | `AdvanceRun` is not replay-safe after partial progress | M4 |
| Fixed in M4 | High | Durable `LaunchIntentRecorded` recovery is not event-log authoritative | M4 |
| Fixed in M4 | High | Launch timeout and indeterminate behavior collapse into `LaunchFailed` | M4 |
| Fixed in M3 | High | Adapter launch contract does not carry the full OCI workload or placement metadata | M3 |
| Fixed in M1 | High | Public events and event APIs can expose literal environment values | M1 |
| Fixed in M2 | High | Scheduler ignores accelerator/GPU requirements | M2 |
| Fixed in M3 | High | Fake adapter idempotency is incomplete for `LaunchKey` reuse | M3 |
| Fixed in M2 | High | Placement determinism depends on input offer order | M2 |
| Fixed in M5 | High | Required V1 run endpoints are missing | M5 |
| Fixed in M5 | High | API idempotency conflicts are not machine-readable 409 responses | M5 |
| Fixed in M4 | Medium | Cleanup retry/no-orphan behavior is not robust | M4 |
| Fixed in M2 | Medium | Unknown or dishonest capability facts can pass feasibility checks | M2 |
| Fixed in M2 | Medium | Price constraints and scheduler policy penalties are incomplete | M2 |
| Fixed in M5 | Medium | Placement preview bypasses workload validation and can panic on empty containers | M5 |
| Fixed in M2 | Medium | Decision audit contents are incomplete | M2 |
| Fixed in M5 | Medium | OpenAPI is materially incomplete | M5 |
| Fixed in M5 | Medium | Workspace isolation at the HTTP boundary is weak | M5 |
| Open | Medium | Subscription offsets are written but not used by `Subscribe` | M10 |
| Fixed in M1 | Medium | OCI-only validation is not hardened enough | M1 |
| Open | Medium | Known gaps under-report required V1 service areas | M0/M5/M13 |

## Milestone 0 Regression Lock

These tests intentionally fail at Milestone 0 and must not be treated as unexpected regressions until their owner milestones repair the behavior.

| Test command | Owner milestone | Locked invariant | Current failure |
| --- | --- | --- | --- |
| `go test ./internal/orchestrator -run TestCreateRunPublicEventRedactsEnvironmentBindings -count=1` | M1 | Public `RunRequested` CloudEvents must not expose literal env values or secret reference metadata | Fails because public event data contains literal env values and secret reference name |
| `go test ./internal/orchestrator -run TestAdvanceRunDoesNotRelaunchAfterNonterminalObservation -count=1` | M4 | Replaying `AdvanceRun` after a nonterminal observation must observe existing launch state, not relaunch | Fails with `eventlog: idempotency conflict` on second advance |
| `go test ./internal/orchestrator -run TestAdvanceRunRecoversRecordedLaunchIntentWhenOffersChange -count=1` | M4 | Recorded launch intent must remain authoritative if current offers disappear | Fails with `orchestrator: no feasible offers` after offer cache changes |
| `go test ./internal/adapter/fake -run TestFakeAdapterLaunchIsIdempotentAndDetectsConflicts -count=1` | M3 | Fake adapter must reject different operation keys reusing the same launch key/ownership token with a different request hash | Fails because launch succeeds instead of returning `adapter.ErrIdempotencyConflict` |
| `go test ./internal/scheduler -run TestSchedulerDecisionStableAcrossOfferOrder -count=1` | M2 | Identical offer sets must produce stable candidate order and decision identity regardless of input order | Fails because candidate order and decision ID follow input order |

## Decision Log

Append decisions in reverse chronological order. Each entry must explain what changed, why, and what it rules out.

### 2026-06-20 - Initialize Long-Horizon Documentation

Decision: Treat the current branch as a foundation scaffold, not production V1.
Why: Verification found multiple violations of original V1 invariants despite passing tests.
Rules out: Continuing implementation under status language that claims V1 completeness.

### 2026-06-20 - Use Durable Long-Horizon Files

Decision: Use `Prompt.md`, `Plan.md`, `Implement.md`, and `Documentation.md` as durable project memory.
Why: Multi-hour or multi-day agent runs need stable target, checkpointed plan, runbook, and live audit log to prevent drift.
Rules out: Relying on chat-only context or stale status prose.

### Prior Foundation Decisions

- Implement load-bearing foundation first instead of every adapter/UI/sink feature in one diff.
- Use `modernc.org/sqlite` for cgo-free SQLite.
- Require digest-pinned images in this slice; tag resolution is a later ImageResolver milestone.
- Use deterministic fake adapter before Docker to verify event-first side-effect boundaries.

## Milestone Log

Use this format for every milestone. Keep entries append-only.

### M<N> - <Milestone Name>

Status: `not_started | in_progress | blocked | complete | superseded`
Owner/session: `<agent or human>`
Started: `<YYYY-MM-DD HH:MM TZ>`
Completed: `<YYYY-MM-DD HH:MM TZ or blank>`
Plan reference: `Plan.md#<anchor>`
Prompt requirements covered: `<requirement ids or short names>`

Scope:

- `<what this milestone is allowed to change>`
- `<what this milestone explicitly does not change>`

Acceptance criteria:

- `<observable outcome>`
- `<observable outcome>`

Implementation notes:

- `<important code paths changed>`
- `<architecture or API decisions made>`

Verification:

- `<command>` - `<pass/fail/not run>` - `<notes>`
- `<command>` - `<pass/fail/not run>` - `<notes>`

Handoff:

- `<what the next agent should inspect first>`
- `<known risks or incomplete edges>`

### M0 - Baseline And Audit Lock

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 00:47 PDT`
Completed: `2026-06-20 00:47 PDT`
Plan reference: `Plan.md#milestone-0-baseline-and-audit-lock`
Prompt requirements covered: `audit ownership, executable regression lock, no V1 expansion`

Scope:

- Added executable audit-lock tests for public event redaction, replay-safe `AdvanceRun`, launch-intent recovery, fake adapter launch-key conflicts, and scheduler order determinism.
- Updated audit ownership documentation for every high/medium finding from the 2026-06-20 verification report.
- Did not change production behavior, add V1 expansion work, or mark any audit finding fixed.

Acceptance criteria:

- Each high/medium audit finding has an owner milestone.
- Known failing regression tests are documented with exact commands.
- Build remains green despite expected failing tests.

Implementation notes:

- Changed tests only under `internal/adapter/fake`, `internal/orchestrator`, and `internal/scheduler`.
- Added `Milestone 0 Regression Lock` so later agents can distinguish expected audit failures from new regressions.

Verification:

- `go test ./internal/orchestrator -run TestCreateRunPublicEventRedactsEnvironmentBindings -count=1` - `failed as expected` - public `RunRequested` data contains literal env and secret reference metadata.
- `go test ./internal/orchestrator -run TestAdvanceRunDoesNotRelaunchAfterNonterminalObservation -count=1` - `failed as expected` - second advance returns `eventlog: idempotency conflict`.
- `go test ./internal/orchestrator -run TestAdvanceRunRecoversRecordedLaunchIntentWhenOffersChange -count=1` - `failed as expected` - offer loss after recorded intent returns `orchestrator: no feasible offers`.
- `go test ./internal/adapter/fake -run TestFakeAdapterLaunchIsIdempotentAndDetectsConflicts -count=1` - `failed as expected` - launch-key reuse with a different operation key succeeds.
- `go test ./internal/scheduler -run TestSchedulerDecisionStableAcrossOfferOrder -count=1` - `failed as expected` - input order changes candidate order and decision ID.
- `go test ./... || true` - `expected red` - fails only on audit-lock tests in adapter/fake, orchestrator, and scheduler.
- `go build ./...` - `passed` - executable build remains green.
- `git status --short --branch` - `observed` - test and documentation changes only.

Handoff:

- Start M1 by making public events/API redaction-safe and hardening OCI validation.
- Do not start Docker, secrets, sinks, or UI work before M5 passes.

### M1 - Public Event Redaction And Workload Boundary Hardening

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 00:47 PDT`
Completed: `2026-06-20 00:59 PDT`
Plan reference: `Plan.md#milestone-1-public-event-redaction-and-workload-boundary-hardening`
Prompt requirements covered: `secret non-observability, public event safety, OCI-only workload validation, workspace boundary`

Scope:

- Split run-request event payloads into public-safe CloudEvent data and private internal data for replay.
- Hardened workload validation for raw extensions, empty env bindings, invalid env names, oversized env literals, invalid ports, and workspace mismatch.
- Sanitized HTTP validation errors to return stable machine-readable codes without echoing literal env values.
- Did not change scheduler, adapter idempotency, or replay/reconciler behavior owned by M2-M4.

Acceptance criteria:

- Literal env values and secret reference names are absent from public run-request events and event-list API responses.
- V1 rejects unsupported raw workload extensions and malformed env/port inputs.
- Workspace mismatch is rejected before run creation.
- M2-M4 audit-lock tests remain expected red and are not claimed fixed.

Implementation notes:

- `RunRequested` events now store public data with env binding kinds only, plus private data containing the internal workload revision for command replay.
- `decodeRunRequested` prefers private event data and falls back to public data for older scaffold events.
- HTTP create-run errors derive response codes from stable validation/workspace error prefixes.

Verification:

- `go test ./internal/domain -count=1` - `passed`.
- `go test ./internal/orchestrator -run 'Redaction|Redacts|Workload|Secret|Workspace' -count=1` - `passed`.
- `go test ./internal/httpapi -run 'Redaction|Workspace|Validation|Events' -count=1` - `passed`.
- `go test ./... || true` - `expected red` - remaining audit-lock tests fail in adapter/fake, orchestrator replay/recovery, and scheduler determinism.
- `go build ./...` - `passed`.
- `git status --short --branch` - `observed` - M1 source/test/docs changes only before commit.

Handoff:

- Start M2 by fixing scheduler determinism, accelerator requirements, conservative unknown facts, cost caps, penalties, and candidate audit contents.
- Do not treat full-suite red as new unless failures differ from the documented M2-M4 audit-lock tests.

### M2 - Deterministic Scheduler And Conservative Facts

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 00:59 PDT`
Completed: `2026-06-20 01:02 PDT`
Plan reference: `Plan.md#milestone-2-deterministic-scheduler-and-conservative-facts`
Prompt requirements covered: `deterministic placement, conservative unknown facts, accelerator enforcement, cost caps, candidate audit`

Scope:

- Sorted offer snapshots before evaluation so identical offer sets produce stable candidate order and decision IDs independent of input order.
- Enforced accelerator inventory, unavailable capacity, missing max-container facts, unknown image-cache facts, and max expected cost.
- Applied configured start-failure, interruption, and uncertainty penalties to candidate scores.
- Added deterministic collection report data and candidate audit fields for connection, adapter type, and native ref.

Acceptance criteria:

- Unknown facts fail hard requirements unless explicitly allowed by existing workload policy fields.
- Accelerator requirements, cost caps, capacity evidence, and risk penalties affect feasibility/scoring.
- Candidate ordering and decision identity are stable for identical logical inputs.
- M3/M4 audit-lock tests remain expected red and are not claimed fixed.

Implementation notes:

- Added `ReliabilityEvidence` to offer snapshots for scheduler risk penalties.
- Added candidate audit fields to `CandidateDecision`.
- Updated fake offer fixtures to provide known image-cache evidence after M2 made missing image-cache facts conservative.

Verification:

- `go test ./internal/scheduler -count=1` - `passed`.
- `go test ./internal/domain -count=1` - `passed`.
- `go test ./internal/httpapi -count=1` - `passed`.
- `go test ./internal/orchestrator -count=1 || true` - `expected red` - only M4 replay/recovery audit-lock tests fail.
- `go test ./... || true` - `expected red` - M3 fake adapter launch-key conflict and M4 replay/recovery remain.
- `go build ./...` - `passed`.
- `git status --short --branch` - `observed` - M2 source/test/docs changes only before commit.

Handoff:

- Start M3 by expanding the adapter launch contract and fixing fake adapter launch-key idempotency.
- Preserve M4 replay/recovery failures until the event-log authoritative orchestrator/reconciler milestone.

### M3 - Complete Adapter Contract And Fake Adapter Idempotency

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 01:02 PDT`
Completed: `2026-06-20 01:06 PDT`
Plan reference: `Plan.md#milestone-3-complete-adapter-contract-and-fake-adapter-idempotency`
Prompt requirements covered: `complete launch side-effect contract, deterministic launch key, cleanup locator, fake adapter idempotency`

Scope:

- Expanded `adapter.LaunchRequest` with workload identity, OCI platform/command/ports/resources, environment binding descriptors, selected offer context, ownership token, launch key, request hash, and cleanup locator.
- Expanded fake adapter receipts and owned-object records with cleanup locators and request hashes.
- Fixed fake adapter conflicts for different operation keys reusing an existing launch key/ownership token with a different request hash.
- Added explicit adapter error classes for timeout, indeterminate, not found, and retryable failures.

Acceptance criteria:

- Orchestrator passes full workload and selected-offer context to adapter `Launch`.
- Secret environment bindings are descriptor-only in the launch request; no secret value is passed.
- Fake adapter cannot overwrite an existing launch-key object with a conflicting request hash.
- M4 replay/recovery tests remain expected red and are not claimed fixed.

Implementation notes:

- Launch request hashing now covers the full side-effect-bearing request structure before the adapter call.
- Fake adapter treats launch key, ownership token, and request hash as the external object identity boundary.

Verification:

- `go test ./internal/adapter/... -count=1` - `passed`.
- `go test ./internal/orchestrator -run TestAdvanceRunPassesCompleteWorkloadAndPlacementToAdapter -count=1` - `passed`.
- `go test ./internal/orchestrator -run TestAdvanceRunPersistsLaunchIntentBeforeCallingAdapter -count=1` - `passed`.
- `go test ./... || true` - `expected red` - only M4 replay/recovery audit-lock tests fail.
- `go build ./...` - `passed`.
- `git status --short --branch` - `observed` - M3 source/test/docs changes only before commit.

Handoff:

- Start M4 by replacing `AdvanceRun`'s linear happy path with event-stream state recovery that observes existing launches and resumes cleanup from durable events.
- Keep side effects behind recorded durable intents.

### M4 - Event-Log Authoritative Orchestrator And Reconciler Core

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 01:06 PDT`
Completed: `2026-06-20 01:10 PDT`
Plan reference: `Plan.md#milestone-4-event-log-authoritative-orchestrator-and-reconciler-core`
Prompt requirements covered: `event-log authority, replay-safe lifecycle, launch-intent recovery, indeterminate launch reconciliation, cleanup-before-close`

Scope:

- Reworked `AdvanceRun` around an event-stream reducer so recorded launch intents and cleanup requests are authoritative.
- Added replay-safe observation recording with unique command keys.
- Added indeterminate launch recording/reconciliation before retry and prevented indeterminate results from becoming simple launch failures.
- Added cleanup retry behavior that resumes from durable cleanup request without relaunching.
- Added a small `internal/reconciler` entry point that delegates lifecycle advancement through the repaired orchestrator boundary.

Acceptance criteria:

- Replaying `AdvanceRun` after nonterminal observation does not perform a second launch.
- Existing `LaunchIntentRecorded` is recovered even if current offers disappear.
- Cleanup retry proceeds from cleanup state, not placement/launch.
- Indeterminate launch outcomes are reconciled by observation/list-owned behavior before retry.
- Runs still close only after cleanup confirmation.

Implementation notes:

- `reduceRun` reconstructs run lifecycle state from stored events.
- `LaunchIntentRecorded` stores the full adapter launch request, so replay can continue without re-querying offers.
- Reconciler package is intentionally thin at this checkpoint; the behavioral reducer lives with the orchestrator and is covered by orchestrator tests.

Verification:

- `go test ./internal/orchestrator -count=1` - `passed`.
- `go test ./internal/reconciler -count=1` - `passed`.
- `go test ./...` - `passed`.
- `go build ./...` - `passed`.
- `git status --short --branch` - `observed` - M4 source/test/docs changes only before commit.

Handoff:

- Start M5 by wiring the runnable HTTP fake-adapter fast path and adding required run endpoints, projections, explicit workspace scoping, idempotency conflicts, and OpenAPI schemas.
- M0-M4 audit-lock failures are now repaired; full suite should stay green unless M5 intentionally adds new red tests first.

### M5 - Runnable Fast Path, API Repair, And OpenAPI Completeness

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 01:10 PDT`
Completed: `2026-06-20 01:14 PDT`
Plan reference: `Plan.md#milestone-5-runnable-fast-path-api-repair-and-openapi-completeness`
Prompt requirements covered: `HTTP fake fast path, run read/list/wait/events/decision/refresh links, explicit workspace scoping, idempotency conflicts, OpenAPI truthfulness`

Scope:

- `POST /v1/runs` now drives create plus `AdvanceRun`, so fake-adapter runs can progress through placement, launch intent, launch, observation, cleanup, and closure.
- Added event-derived run get/list/wait/decision/refresh endpoints and link responses.
- Made run read/event endpoints require explicit `workspace_id`.
- Returned machine-readable HTTP 409 for idempotency-key reuse with a different request hash.
- Expanded OpenAPI paths, request body, response schemas, error schema, event list shape, decision shape, and conflict response.
- Validated placement preview workloads before scheduling to prevent validation bypass/panic.

Acceptance criteria:

- Advertised fake-adapter broker fast path is runnable through HTTP.
- Required V1 run endpoints exist and are covered by HTTP tests.
- OpenAPI describes the implemented run request/response/error shapes.
- Workspace isolation is explicit for run read/list/event/decision/refresh boundaries.
- High and medium audit findings from the repair set are now fixed or have later owner milestones.

Implementation notes:

- Event-derived projections currently live on the orchestrator; disposable projection tables are deferred to later projection-runner work.
- Cancel endpoint exists as a read-oriented placeholder at this checkpoint; full cancellation semantics remain future lifecycle work.

Verification:

- `go test ./internal/httpapi -count=1` - `passed`.
- `go test ./internal/orchestrator -count=1` - `passed`.
- `go test ./...` - `passed`.
- `go build ./...` - `passed`.
- `git status --short --branch` - `observed` - M5 source/test/docs changes only before commit.

Handoff:

- Start M6 by adding event-backed workload revisions and an OCI image resolver.
- Do not start Docker, secrets, sinks, or UI work before the post-M5 repair state remains green.

### M6 - Workload Service And OCI Image Resolver

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 01:14 PDT`
Completed: `2026-06-20 01:18 PDT`
Plan reference: `Plan.md#milestone-6-workload-service-and-oci-image-resolver`
Prompt requirements covered: `event-backed workload revisions, immutable digest-pinned revisions, image tag resolver boundary, run-by-revision create path`

Scope:

- Added `internal/workload` event-backed workload and immutable revision service.
- Added `internal/ociresolver` static resolver for digest no-op and configured tag-to-digest resolution by platform.
- Added HTTP workload create, revision create/list/get, and image resolve endpoints.
- Added create-run path that loads a stored workload revision by `workload_id` and `workload_revision_id`.

Acceptance criteria:

- Workload revisions are validated and event-backed.
- Mutable tags are rejected at revision creation unless resolved before revision storage.
- Resolver can return digest-pinned metadata for tags and no-op digest references.
- HTTP create-run can reference exact stored revisions.

Implementation notes:

- The resolver is a deterministic static implementation suitable for tests and future registry-backed replacement.
- Workload revision immutability is enforced by rejecting duplicate revision IDs in the workload stream.

Verification:

- `go test ./internal/workload/... -count=1` - `passed`.
- `go test ./internal/ociresolver/... -count=1` - `passed`.
- `go test ./internal/httpapi -run 'Workload|Image|StoredWorkloadRevision' -count=1` - `passed`.
- `go test ./...` - `passed`.
- `go build ./...` - `passed`.
- `git status --short --branch` - `observed` - M6 source/test/docs changes only before commit.

Handoff:

- Start M7 by adding connection, offer, authorization, and latency service boundaries, then wire scheduler input from offer snapshots.

### M7 - Connections, Offers, Authorization, And Latency Estimation

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 01:18 PDT`
Completed: `2026-06-20 01:23 PDT`
Plan reference: `Plan.md#milestone-7-connections-offers-authorization-and-latency-estimation`
Prompt requirements covered: `connection records, offer cache from events, workspace authorization boundary, latency estimates feeding scheduler`

Scope:

- Added event-backed `internal/connection` service with create/get/list/update-authorization behavior and adapter authorization schema metadata.
- Added event-backed `internal/offers` service that ingests offer snapshots and rebuilds a disposable cache from event history.
- Added `internal/authz` workspace-scoped authorizer covering run, workload, connection, and secret resources.
- Added `internal/latency` in-process estimator and scheduler latency-estimate input path.

Acceptance criteria:

- Offer cache can be rebuilt from event-log history and filters expired offers.
- Authorization rejects cross-workspace operations.
- Scheduler decisions can use latency estimates supplied by the estimator path.
- Candidate collection provenance from M2 remains available.

Implementation notes:

- HTTP wiring for connection/offer endpoints is deferred; M7 establishes tested service boundaries and scheduler input.
- Offer service remains event-log authoritative; no table-backed read model is introduced.

Verification:

- `go test ./internal/connection/... -count=1` - `passed`.
- `go test ./internal/offers/... -count=1` - `passed`.
- `go test ./internal/authz/... -count=1` - `passed`.
- `go test ./internal/latency/... -count=1` - `passed`.
- `go test ./internal/scheduler -count=1` - `passed`.
- `go test ./...` - `passed`.
- `go build ./...` - `passed`.
- `git status --short --branch` - `observed` - M7 source/test/docs changes only before commit.

Handoff:

- Start M8 by adding the Docker host adapter and adapter registry wiring.

### M8 - Docker Host Adapter

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 01:23 PDT`
Completed: `2026-06-20 01:25 PDT`
Plan reference: `Plan.md#milestone-8-docker-host-adapter`
Prompt requirements covered: `Docker adapter contract, deterministic labels/names, list-owned reconciliation, adapter registry`

Scope:

- Added `internal/adapter/docker` adapter over a narrow Docker client interface with fake-client unit coverage.
- Added launch, observe, release, list-owned, deterministic container names, labels, environment mapping, ports, platform, entrypoint, args, resources, ownership token, launch key, and cleanup locator behavior.
- Added idempotent Docker launch behavior through deterministic container names and inspect-on-existing.
- Added `internal/adapter/registry` for adapter type lookup.

Acceptance criteria:

- Docker adapter implements the common adapter contract.
- `ListOwned` can discover owned resources by workspace label.
- Launch labels include workspace, run, attempt, launch key, ownership token, cleanup locator, request hash, workload ID, and revision ID.
- Unsupported Docker flags do not enter the workload contract.

Implementation notes:

- The current Docker adapter is unit-tested against a fake client interface; no live Docker integration test has been added yet.
- `docker version` was available locally, so a future guarded live integration can be added without changing the unit boundary.

Verification:

- `go test ./internal/adapter/docker -count=1` - `passed`.
- `go test ./internal/adapter/... -count=1` - `passed`.
- `docker version` - `passed` - Docker Engine reachable via Orbstack.
- `MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run Integration -count=1` - `not run` - no live integration test exists yet.
- `go test ./...` - `passed`.
- `go build ./...` - `passed`.
- `git status --short --branch` - `observed` - M8 source/test/docs changes only before commit.

Handoff:

- Start M9 by adding encrypted event-backed secret versions and scoped grants, then ensure adapter contracts carry descriptors without plaintext.

### M9 - Secret Vault And Grants

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 01:25 PDT`
Completed: `2026-06-20 01:27 PDT`
Plan reference: `Plan.md#milestone-9-secret-vault-and-grants`
Prompt requirements covered: `encrypted event-backed secret versions, metadata-only reads, scoped grants, revocation, plaintext non-observability`

Scope:

- Added `internal/secrets` AES-GCM encrypted secret version events, metadata reads, grant creation, grant revocation, and grant listing.
- Added HTTP secret version, metadata list, and grant endpoints that never return plaintext.
- Verified secret plaintext does not appear in non-test implementation files.

Acceptance criteria:

- Secret versions are encrypted in private event data and public event data contains only metadata.
- Public API responses expose metadata and grants only, not plaintext.
- Grants can be revoked and replayed from event history.

Implementation notes:

- Key management is currently a configured byte slice for the in-process vault; production key management remains a future hardening area.
- Adapter contracts already carry descriptors without plaintext; secret materialization at launch remains deliberately narrow.

Verification:

- `go test ./internal/secrets/... -count=1` - `passed`.
- `go test ./internal/httpapi -run Secret -count=1` - `passed`.
- `go test ./internal/adapter/... -run Secret -count=1` - `passed`.
- `rg -n "super-secret-value|plaintext-secret|plaintext-secret" internal --glob '!**/*_test.go' || true` - `passed` - no implementation/plain output hits.
- `go test ./...` - `passed`.
- `go build ./...` - `passed`.
- `git status --short --branch` - `observed` - M9 source/test/docs changes only before commit.

Handoff:

- Start M10 by adding projection runner replay/rebuild behavior and JSON-first CLI commands.

### M10 - Projection Runner And CLI

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 01:28 PDT`
Completed: `2026-06-20 01:32 PDT`
Plan reference: `Plan.md#milestone-10-projection-runner-and-cli`
Prompt requirements covered: `durable projection offsets, disposable projection rebuilds, Subscribe offset resume, JSON-first run CLI`

Scope:

- Fixed SQLite event-log subscriptions to resume from stored subscription offsets when they are ahead of the requested position.
- Added `internal/projection` runner support for durable resume from global position and disposable rebuild from the beginning.
- Added `internal/cli` JSON-first run commands for create, cancel, get, list, wait, events, decision, refresh, and machine-readable conflict reporting.
- Refactored `cmd/mercator` so no-arg or `serve` keeps the HTTP server path, while CLI subcommands delegate to `internal/cli`.

Acceptance criteria:

- Stored subscription offsets are used by `Subscribe`.
- Durable projections resume from the last acknowledged global position.
- Disposable projections can rebuild from global position zero without overwriting durable offsets.
- CLI stdout and stderr payloads are parseable JSON for success and conflict paths.

Implementation notes:

- Projection offsets reuse the event log's `subscription_offsets` table through `Offset` and `Ack`.
- The CLI is intentionally HTTP-backed so command behavior stays aligned with the REST API.
- `run wait` currently uses the HTTP wait/read endpoint; deeper streaming wait semantics remain tied to future API lifecycle improvements.

Verification:

- `go test ./internal/eventlog -run TestSQLiteSubscribeResumesFromStoredOffset -count=1` - `passed`.
- `go test ./internal/projection/... -count=1` - `passed`.
- `go test ./internal/cli/... -count=1` - `passed`.
- `go test ./cmd/mercator/... -count=1` - `passed`.
- `go test ./...` - `passed`.
- `go build ./...` - `passed`.
- `git status --short --branch` - `observed` - M10 source/test/docs changes only before commit.

Handoff:

- Start M11 by adding event sinks, replay APIs, and durable sink cursors.

### M11 - Event Sinks, Replay, And Durable Cursors

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 01:33 PDT`
Completed: `2026-06-20 01:35 PDT`
Plan reference: `Plan.md#milestone-11-event-sinks-replay-and-durable-cursors`
Prompt requirements covered: `webhook sink, Kafka sink boundary, Postgres sink boundary, durable sink cursors, bounded replay, failure isolation`

Scope:

- Added `internal/sinks` manager with durable per-sink cursors, retry-after-failure behavior, bounded replay, and status reads.
- Added webhook sink delivery over CloudEvents JSON.
- Added Kafka and Postgres sink boundaries behind configured producer/writer interfaces.
- Added HTTP sink status, deliver, replay, and OpenAPI endpoint descriptions plus JSON CLI sink replay support.
- Added failure-isolation coverage proving sink failure does not block run creation/lifecycle progress.

Acceptance criteria:

- Sink failures are isolated to sink operations and do not fail placement or run lifecycle commands.
- Sink cursors are durable per sink using event-log offsets.
- Replay is bounded by request limit, observable in the result, and does not move the durable cursor.
- Webhook/Kafka/Postgres sink boundaries preserve event payloads behind the common sink contract.

Implementation notes:

- Sink cursor IDs use `sink:<sink_id>` in the event log's `subscription_offsets` table.
- Sink delivery skips private events while advancing the cursor, preserving secret non-observability.
- Kafka and Postgres implementations are deliberately interface-backed in this milestone; production client configuration remains future wiring.

Verification:

- `go test ./internal/sinks/... -count=1` - `passed`.
- `go test ./internal/httpapi -run Sink -count=1` - `passed`.
- `go test ./internal/cli -run Sink -count=1` - `passed`.
- `go test ./internal/eventlog -run 'Subscribe|Ack|Cursor' -count=1` - `passed`.
- `go test ./...` - `passed`.
- `go build ./...` - `passed`.
- `git diff --check` - `passed`.
- `git status --short --branch` - `observed` - M11 source/test/docs changes only before commit.

Handoff:

- Start M12 by adding the embedded static UI and browser-verifying run/events/decision/sink views.

### M12 - Embedded Static UI

Status: `complete`
Owner/session: `Codex`
Started: `2026-06-20 01:36 PDT`
Completed: `2026-06-20 01:43 PDT`
Plan reference: `Plan.md#milestone-12-embedded-static-ui`
Prompt requirements covered: `embedded single-process UI, public API backed views, no secret values, read-oriented operations UI`

Scope:

- Added top-level `web` package with embedded static assets for a compact Mercator operations UI.
- Added UI views for run list, selected run detail, public event timeline, placement decision, connections, offers, and sink status.
- Added public read endpoints for connections and offers, plus OpenAPI descriptions for those endpoints.
- Added an opt-in `MERCATOR_FAKE_OFFER=1` executable seed path for local fake-adapter smoke testing.
- Kept the UI read-oriented except refresh and cancel controls.

Acceptance criteria:

- UI is embedded and served by the single Go process at `/` with assets under `/ui/`.
- UI reads only public REST APIs and does not use private event data or secret values.
- Empty state, populated fake-adapter run state, event/decision/offer panels, sink error state, and mobile layout render without overlap.

Implementation notes:

- The UI intentionally shows sink configuration errors as an operator-visible panel state.
- `GET /v1/offers` falls back to adapter offers when the disposable offer cache is empty, so the local fake-adapter server can expose the active offer.
- Browser plugin was unavailable for QA (`Browser is not available: iab`); Playwright fallback used system Chrome because the bundled Playwright Chromium binary was not installed.

Verification:

- `go test ./internal/httpapi -run UI -count=1` - `passed`.
- `go test ./cmd/mercator -count=1` - `passed`.
- `go test ./...` - `passed`.
- `go build ./...` - `passed`.
- Browser smoke via Playwright system Chrome at `http://127.0.0.1:18082/` - `passed` - empty state, populated `run_browser`, event timeline, decision panel, `offer_local_fake`, sink error state, and mobile layout verified.
- Screenshots inspected with `view_image`: `/tmp/mercator-ui-empty.png`, `/tmp/mercator-ui-populated.png`, `/tmp/mercator-ui-mobile.png`.
- Console health - `expected warning` - only expected 501 sink-status fetches for the intentional unconfigured sink error state.
- `git status --short --branch` - `observed` - M12 source/test/docs changes only before commit.

Handoff:

- Start M13 final release gate with end-to-end CLI/API/UI checks, race subset, Docker status, docs audit, and final known-gap tightening.

## Verification Log

Use this format for every command or manual check. Do not summarize failures without preserving the command and result.

| Time | Scope | Command/check | Result | Notes |
| --- | --- | --- | --- | --- |
| 2026-06-20 | branch state | `git status --short --branch` | observed | `## beng/v1-run-broker`; `?? docs/long-horizon/` |
| 2026-06-20 | branch state | `git rev-parse HEAD` | observed | `f73665c9dead1cc9ba20ee7e200dcd4e206d54ee` |
| 2026-06-20 00:47 PDT | baseline | `git status --short --branch` | observed | `## beng/v1-run-broker` |
| 2026-06-20 00:47 PDT | baseline | `go test ./... || true` | passed | All existing tests passed before M0 audit-lock regressions were added |
| 2026-06-20 00:47 PDT | baseline | `go build ./...` | passed | Build green before M0 edits |
| 2026-06-20 00:47 PDT | M0 red | `go test ./internal/orchestrator -run TestCreateRunPublicEventRedactsEnvironmentBindings -count=1` | failed as expected | Public event exposed literal env value and secret reference name |
| 2026-06-20 00:47 PDT | M0 red | `go test ./internal/orchestrator -run TestAdvanceRunDoesNotRelaunchAfterNonterminalObservation -count=1` | failed as expected | Second advance returned `eventlog: idempotency conflict` |
| 2026-06-20 00:47 PDT | M0 red | `go test ./internal/orchestrator -run TestAdvanceRunRecoversRecordedLaunchIntentWhenOffersChange -count=1` | failed as expected | Second advance returned `orchestrator: no feasible offers` after offers disappeared |
| 2026-06-20 00:47 PDT | M0 red | `go test ./internal/adapter/fake -run TestFakeAdapterLaunchIsIdempotentAndDetectsConflicts -count=1` | failed as expected | Different operation key reused launch key/ownership token without conflict |
| 2026-06-20 00:47 PDT | M0 red | `go test ./internal/scheduler -run TestSchedulerDecisionStableAcrossOfferOrder -count=1` | failed as expected | Reversed offer order changed candidate order and decision ID |
| 2026-06-20 00:47 PDT | M0 validation | `go test ./... || true` | expected red | Audit-lock tests fail in adapter/fake, orchestrator, and scheduler |
| 2026-06-20 00:47 PDT | M0 validation | `go build ./...` | passed | Build green after M0 audit-lock tests |
| 2026-06-20 00:47 PDT | M0 validation | `git status --short --branch` | observed | Test and documentation changes only |
| 2026-06-20 00:59 PDT | M1 focused | `go test ./internal/domain -count=1` | passed | Workload validation hardening tests green |
| 2026-06-20 00:59 PDT | M1 focused | `go test ./internal/orchestrator -run 'Redaction\|Redacts\|Workload\|Secret\|Workspace' -count=1` | passed | Public event redaction and workspace mismatch tests green |
| 2026-06-20 00:59 PDT | M1 focused | `go test ./internal/httpapi -run 'Redaction\|Workspace\|Validation\|Events' -count=1` | passed | API event/error redaction and workspace tests green |
| 2026-06-20 00:59 PDT | M1 validation | `go test ./... || true` | expected red | Remaining documented audit-lock failures are M2 scheduler order, M3 fake launch-key conflict, and M4 replay/recovery |
| 2026-06-20 00:59 PDT | M1 validation | `go build ./...` | passed | Build green after M1 |
| 2026-06-20 00:59 PDT | M1 validation | `git status --short --branch` | observed | M1 source/test/docs changes only |
| 2026-06-20 01:02 PDT | M2 focused | `go test ./internal/scheduler -count=1` | passed | Determinism, accelerator, conservative facts, cost cap, penalties, and audit tests green |
| 2026-06-20 01:02 PDT | M2 focused | `go test ./internal/domain -count=1` | passed | Domain model changes compile with validation tests |
| 2026-06-20 01:02 PDT | M2 focused | `go test ./internal/httpapi -count=1` | passed | HTTP fixtures updated with known image-cache evidence |
| 2026-06-20 01:02 PDT | M2 focused | `go test ./internal/orchestrator -count=1 || true` | expected red | Only M4 replay/recovery audit-lock tests fail |
| 2026-06-20 01:02 PDT | M2 validation | `go test ./... || true` | expected red | Remaining documented failures are M3 fake launch-key conflict and M4 replay/recovery |
| 2026-06-20 01:02 PDT | M2 validation | `go build ./...` | passed | Build green after M2 |
| 2026-06-20 01:02 PDT | M2 validation | `git status --short --branch` | observed | M2 source/test/docs changes only |
| 2026-06-20 01:06 PDT | M3 focused | `go test ./internal/adapter/... -count=1` | passed | Fake adapter launch-key conflict and receipt identity tests green |
| 2026-06-20 01:06 PDT | M3 focused | `go test ./internal/orchestrator -run TestAdvanceRunPassesCompleteWorkloadAndPlacementToAdapter -count=1` | passed | Launch request carries full workload and placement context |
| 2026-06-20 01:06 PDT | M3 focused | `go test ./internal/orchestrator -run TestAdvanceRunPersistsLaunchIntentBeforeCallingAdapter -count=1` | passed | Durable intent still precedes launch side effect |
| 2026-06-20 01:06 PDT | M3 validation | `go test ./... || true` | expected red | Only M4 replay/recovery audit-lock tests fail |
| 2026-06-20 01:06 PDT | M3 validation | `go build ./...` | passed | Build green after M3 |
| 2026-06-20 01:06 PDT | M3 validation | `git status --short --branch` | observed | M3 source/test/docs changes only |
| 2026-06-20 01:10 PDT | M4 focused | `go test ./internal/orchestrator -count=1` | passed | Replay, launch-intent recovery, indeterminate launch, cleanup retry tests green |
| 2026-06-20 01:10 PDT | M4 focused | `go test ./internal/reconciler -count=1` | passed | Reconciler entry point tests green |
| 2026-06-20 01:10 PDT | M4 validation | `go test ./...` | passed | Full suite green after M4 |
| 2026-06-20 01:10 PDT | M4 validation | `go build ./...` | passed | Build green after M4 |
| 2026-06-20 01:10 PDT | M4 validation | `git status --short --branch` | observed | M4 source/test/docs changes only |
| 2026-06-20 01:14 PDT | M5 focused | `go test ./internal/httpapi -count=1` | passed | Fast path, run endpoints, workspace scoping, 409 conflict, OpenAPI, and preview validation tests green |
| 2026-06-20 01:14 PDT | M5 focused | `go test ./internal/orchestrator -count=1` | passed | Event-derived projection helpers still green |
| 2026-06-20 01:14 PDT | M5 validation | `go test ./...` | passed | Full suite green after M5 |
| 2026-06-20 01:14 PDT | M5 validation | `go build ./...` | passed | Build green after M5 |
| 2026-06-20 01:14 PDT | M5 validation | `git status --short --branch` | observed | M5 source/test/docs changes only |
| 2026-06-20 01:18 PDT | M6 focused | `go test ./internal/workload/... -count=1` | passed | Event-backed workload revision tests green |
| 2026-06-20 01:18 PDT | M6 focused | `go test ./internal/ociresolver/... -count=1` | passed | Digest no-op, tag resolution, and missing tag tests green |
| 2026-06-20 01:18 PDT | M6 focused | `go test ./internal/httpapi -run 'Workload\|Image\|StoredWorkloadRevision' -count=1` | passed | Workload/revision/image endpoints and run-by-revision path green |
| 2026-06-20 01:18 PDT | M6 validation | `go test ./...` | passed | Full suite green after M6 |
| 2026-06-20 01:18 PDT | M6 validation | `go build ./...` | passed | Build green after M6 |
| 2026-06-20 01:18 PDT | M6 validation | `git status --short --branch` | observed | M6 source/test/docs changes only |
| 2026-06-20 01:23 PDT | M7 focused | `go test ./internal/connection/... -count=1` | passed | Connection CRUD and authorization-schema metadata tests green |
| 2026-06-20 01:23 PDT | M7 focused | `go test ./internal/offers/... -count=1` | passed | Offer ingest/cache/rebuild/expiry tests green |
| 2026-06-20 01:23 PDT | M7 focused | `go test ./internal/authz/... -count=1` | passed | Workspace authz tests green |
| 2026-06-20 01:23 PDT | M7 focused | `go test ./internal/latency/... -count=1` | passed | Latency estimator tests green |
| 2026-06-20 01:23 PDT | M7 focused | `go test ./internal/scheduler -count=1` | passed | Latency estimate input path tests green |
| 2026-06-20 01:23 PDT | M7 validation | `go test ./...` | passed | Full suite green after M7 |
| 2026-06-20 01:23 PDT | M7 validation | `go build ./...` | passed | Build green after M7 |
| 2026-06-20 01:23 PDT | M7 validation | `git status --short --branch` | observed | M7 source/test/docs changes only |
| 2026-06-20 01:25 PDT | M8 focused | `go test ./internal/adapter/docker -count=1` | passed | Fake-client Docker adapter tests green |
| 2026-06-20 01:25 PDT | M8 focused | `go test ./internal/adapter/registry -count=1` | passed | Registry tests green |
| 2026-06-20 01:25 PDT | M8 validation | `go test ./internal/adapter/... -count=1` | passed | Adapter package tests green |
| 2026-06-20 01:25 PDT | M8 validation | `docker version` | passed | Docker Engine reachable via Orbstack |
| 2026-06-20 01:25 PDT | M8 validation | `go test ./...` | passed | Full suite green after M8 |
| 2026-06-20 01:25 PDT | M8 validation | `go build ./...` | passed | Build green after M8 |
| 2026-06-20 01:25 PDT | M8 validation | `git status --short --branch` | observed | M8 source/test/docs changes only |
| 2026-06-20 01:27 PDT | M9 focused | `go test ./internal/secrets/... -count=1` | passed | Secret encryption, metadata, grant, revoke tests green |
| 2026-06-20 01:27 PDT | M9 focused | `go test ./internal/httpapi -run Secret -count=1` | passed | Secret metadata/grant API tests green |
| 2026-06-20 01:27 PDT | M9 focused | `go test ./internal/adapter/... -run Secret -count=1` | passed | Adapter packages have no secret-specific failures |
| 2026-06-20 01:27 PDT | M9 check | `rg -n "super-secret-value\|plaintext-secret\|plaintext-secret" internal --glob '!**/*_test.go' || true` | passed | No non-test plaintext hits |
| 2026-06-20 01:27 PDT | M9 validation | `go test ./...` | passed | Full suite green after M9 |
| 2026-06-20 01:27 PDT | M9 validation | `go build ./...` | passed | Build green after M9 |
| 2026-06-20 01:27 PDT | M9 validation | `git status --short --branch` | observed | M9 source/test/docs changes only |
| 2026-06-20 01:30 PDT | M10 focused | `go test ./internal/eventlog -run TestSQLiteSubscribeResumesFromStoredOffset -count=1` | passed | Subscribe resumes from stored offset |
| 2026-06-20 01:30 PDT | M10 focused | `go test ./internal/projection/... -count=1` | passed | Projection durable resume and disposable rebuild tests green |
| 2026-06-20 01:30 PDT | M10 focused | `go test ./internal/cli/... -count=1` | passed | Run create/cancel/get/list/wait/events/decision/refresh JSON tests green |
| 2026-06-20 01:31 PDT | M10 focused | `go test ./cmd/mercator/... -count=1` | passed | Command package delegates JSON CLI subcommands |
| 2026-06-20 01:32 PDT | M10 validation | `go test ./...` | passed | Full suite green after M10 |
| 2026-06-20 01:32 PDT | M10 validation | `go build ./...` | passed | Build green after M10 |
| 2026-06-20 01:32 PDT | M10 validation | `git status --short --branch` | observed | M10 source/test/docs changes only |
| 2026-06-20 01:34 PDT | M11 focused | `go test ./internal/sinks/... -count=1` | passed | Cursor delivery, retry, restart, replay, webhook, Kafka, and Postgres boundary tests green |
| 2026-06-20 01:34 PDT | M11 focused | `go test ./internal/httpapi -run Sink -count=1` | passed | Sink replay API and failure isolation tests green |
| 2026-06-20 01:34 PDT | M11 focused | `go test ./internal/cli -run Sink -count=1` | passed | Sink replay CLI emits parseable JSON |
| 2026-06-20 01:35 PDT | M11 validation | `go test ./internal/eventlog -run 'Subscribe\|Ack\|Cursor' -count=1` | passed | Event-log cursor tests green |
| 2026-06-20 01:35 PDT | M11 validation | `go test ./...` | passed | Full suite green after M11 |
| 2026-06-20 01:35 PDT | M11 validation | `go build ./...` | passed | Build green after M11 |
| 2026-06-20 01:35 PDT | M11 validation | `git diff --check` | passed | No whitespace errors |
| 2026-06-20 01:35 PDT | M11 validation | `git status --short --branch` | observed | M11 source/test/docs changes only |
| 2026-06-20 01:39 PDT | M12 focused | `go test ./internal/httpapi -run UI -count=1` | passed | Embedded UI assets and UI-backed read APIs green |
| 2026-06-20 01:40 PDT | M12 focused | `go test ./cmd/mercator -count=1` | passed | Opt-in fake-offer executable seed tests green |
| 2026-06-20 01:40 PDT | M12 validation | `go test ./...` | passed | Full suite green after M12 code |
| 2026-06-20 01:40 PDT | M12 validation | `go build ./...` | passed | Build green after M12 code |
| 2026-06-20 01:41 PDT | M12 browser | Browser plugin bootstrap | blocked | In-app browser reported `Browser is not available: iab` |
| 2026-06-20 01:42 PDT | M12 browser | Playwright bundled Chromium launch | blocked | Bundled Chromium binary was not installed |
| 2026-06-20 01:43 PDT | M12 browser | Playwright system Chrome smoke at `http://127.0.0.1:18082/` | passed | Empty, populated run, events, decision, offers, sink error, and mobile states verified |
| 2026-06-20 01:43 PDT | M12 visual | `view_image` on `/tmp/mercator-ui-empty.png`, `/tmp/mercator-ui-populated.png`, `/tmp/mercator-ui-mobile.png` | passed | Desktop and mobile layouts inspected; no overlap/clipping observed |

Required verification cadence:

- Run focused tests for the package being changed before broad tests.
- Run `go test ./...` before marking a milestone complete.
- Run `go build ./...` before claiming executable readiness.
- If API behavior changes, run or add HTTP/OpenAPI tests.
- If event semantics change, add replay/idempotency/recovery tests.
- If secrets or public events change, verify redaction with explicit negative tests.
- If a command fails, fix or log the blocker before moving to the next milestone.

## Known Issues

Blocking V1 correctness issues:

- Docker adapter live integration remains missing; unit fake-client contract is implemented.
- Cancellation is endpoint-present but not yet a full adapter-backed lifecycle command.
- OpenAPI is expanded for repaired run/workload paths but is not yet complete for all future V1 service areas.

Known missing feature areas from original V1:

- Live Docker integration test.
- Reconciler and lease janitor.
- Authorization service.

Known scaffold gaps:

- Production key management and service credential storage remain basic.
- Kafka/Postgres sinks use configured backend interfaces; production client wiring remains future work.
- Embedded UI is intentionally compact and operational; deeper connection/offer management workflows remain future work.
- Registry-backed tag resolution is not implemented; the M6 resolver is deterministic/static.

## Next Action

Start `Plan.md` Milestone 13: V1 end-to-end release gate.

Do not edit production code outside the active milestone. Docker, secrets, sinks, and UI remain gated behind their owner milestones.
