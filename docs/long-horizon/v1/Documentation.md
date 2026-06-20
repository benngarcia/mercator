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

## Audit Finding Ownership

| Finding | Owner milestone |
| --- | --- |
| Public env/secret values can leak through public events/API | M1 |
| OCI boundary validation can be bypassed or is incomplete | M1 |
| Scheduler ignores GPU/accelerators | M2 |
| Unknown and dishonest facts pass feasibility | M2 |
| Placement determinism depends on input order | M2 |
| Price caps and penalties are incomplete | M2 |
| Adapter launch does not carry OCI workload | M3 |
| Fake adapter launch-key idempotency is incomplete | M3 |
| `AdvanceRun` is not replay-safe | M4 |
| Launch-intent recovery is not event-log authoritative | M4 |
| Indeterminate launch handling is absent | M4 |
| Cleanup retry/no-orphan behavior is weak | M4 |
| HTTP fast path is not runnable | M5 |
| Required run endpoints are missing | M5 |
| API idempotency conflicts are not 409 machine-readable responses | M5 |
| OpenAPI is incomplete | M5 |
| Workspace scoping is weak | M5 |
| Workload service and immutable revision store are missing | M6 |
| OCI tag-to-digest resolver is missing | M6 |
| Connection, offer, authz, latency services are missing | M7 |
| Docker host adapter is missing | M8 |
| Secret vault and grants are missing | M9 |
| Projection runner and CLI are missing | M10 |
| Event sinks and durable cursor/replay are missing | M11 |
| Embedded UI is missing | M12 |

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

## Verification Log

Use this format for every command or manual check. Do not summarize failures without preserving the command and result.

| Time | Scope | Command/check | Result | Notes |
| --- | --- | --- | --- | --- |
| 2026-06-20 | branch state | `git status --short --branch` | observed | `## beng/v1-run-broker`; `?? docs/long-horizon/` |
| 2026-06-20 | branch state | `git rev-parse HEAD` | observed | `f73665c9dead1cc9ba20ee7e200dcd4e206d54ee` |

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

Start at `Plan.md` Milestone 0. Do not edit production code until the baseline/audit lock is complete and current failing regression tests are documented.

Recommended first implementation milestone after M0: reconcile documentation and claims so README/status/API language clearly says the current branch is a foundation scaffold, not complete V1. After that, begin the first correctness milestone around event-log authority, replay-safe lifecycle behavior, and public event redaction.
