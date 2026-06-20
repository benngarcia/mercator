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
| Open | High | Executable server cannot run the advertised broker fast path | M5 |
| Open | High | `AdvanceRun` is not replay-safe after partial progress | M4 |
| Open | High | Durable `LaunchIntentRecorded` recovery is not event-log authoritative | M4 |
| Open | High | Launch timeout and indeterminate behavior collapse into `LaunchFailed` | M4 |
| Open | High | Adapter launch contract does not carry the full OCI workload or placement metadata | M3 |
| Fixed in M1 | High | Public events and event APIs can expose literal environment values | M1 |
| Open | High | Scheduler ignores accelerator/GPU requirements | M2 |
| Open | High | Fake adapter idempotency is incomplete for `LaunchKey` reuse | M3 |
| Open | High | Placement determinism depends on input offer order | M2 |
| Open | High | Required V1 run endpoints are missing | M5 |
| Open | High | API idempotency conflicts are not machine-readable 409 responses | M5 |
| Open | Medium | Cleanup retry/no-orphan behavior is not robust | M4 |
| Open | Medium | Unknown or dishonest capability facts can pass feasibility checks | M2 |
| Open | Medium | Price constraints and scheduler policy penalties are incomplete | M2 |
| Open | Medium | Placement preview bypasses workload validation and can panic on empty containers | M5 |
| Open | Medium | Decision audit contents are incomplete | M2 |
| Open | Medium | OpenAPI is materially incomplete | M5 |
| Open | Medium | Workspace isolation at the HTTP boundary is weak | M5 |
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

- Event-log authority and replay safety are incomplete.
- Launch intent recovery is not authoritative after durable intent.
- Indeterminate launch reconciliation is missing.
- Public event redaction is unsafe for literal env values.
- Adapter launch does not carry full OCI workload/placement contract.
- Scheduler hard-requirement handling is incomplete for accelerators and unknown facts.
- Placement determinism is sensitive to offer input order.
- Required run APIs are missing.
- OpenAPI does not represent the real V1 contract.
- API idempotency conflicts need proper conflict semantics.

Known missing feature areas from original V1:

- Workload service and immutable revisions.
- Run service complete endpoint set.
- Connection service and adapter-defined authorization schemas.
- Secret vault with encrypted event-backed secret versions and grants.
- OCI image resolver for tag-to-digest and artifact metadata.
- Offer service and cached offer snapshots.
- Latency estimator.
- Adapter registry and runtime adapters.
- Docker host adapter.
- Reconciler and lease janitor.
- Projection runner.
- Event sinks with replay and durable cursors.
- Authorization service.
- Embedded static UI.
- JSON-first CLI behavior.

Known scaffold gaps:

- Docker host adapter is not implemented.
- Secret encryption and service credentials are not implemented.
- Webhook/Kafka/Postgres sinks are not implemented.
- Embedded UI is not implemented.
- Registry tag resolution is not implemented; workloads must already be digest-pinned.

## Next Action

Start `Plan.md` Milestone 2: deterministic scheduler and conservative facts.

The remaining M0 audit-lock tests are expected to fail until their owner milestones fix them. Do not edit production code outside the active milestone, and do not start Docker, secrets, sinks, or UI work before M5 passes.
