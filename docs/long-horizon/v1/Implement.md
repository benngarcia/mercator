# Mercator V1 Implementation Runbook

This file is the operating loop for future agents implementing the real Mercator V1 OCI run broker.

The current branch contains a foundation scaffold, not complete V1. Do not treat passing tests or earlier status text as proof that V1 is done.

## Source Of Truth

Use the long-horizon V1 documents in this order:

1. `docs/long-horizon/v1/Prompt.md`
   - Frozen target, constraints, non-goals, deliverables, and done-when.
   - If code, comments, README text, or old plans disagree with this file, `Prompt.md` wins.
2. `docs/long-horizon/v1/Plan.md`
   - Checkpointed milestone plan.
   - Execute only the current milestone unless the plan explicitly says to continue.
3. `docs/long-horizon/v1/Implement.md`
   - This runbook.
   - Defines the operating loop, verification, checkpointing, dispatch, and stop rules.
4. `docs/long-horizon/v1/Documentation.md`
   - Live status, audit log, decisions, verification evidence, known gaps, and claims.
   - Update it continuously. Never leave status stale after code changes.

Historical context only:

- `docs/superpowers/specs/2026-06-20-oci-run-broker-v1-verification-brief.md`
- `docs/superpowers/status/2026-06-20-oci-run-broker-v1-agent-verification.md`
- `docs/superpowers/runbooks/2026-06-20-oci-run-broker-v1-implement.md`
- `docs/superpowers/plans/2026-06-20-oci-run-broker-v1.md`
- `docs/superpowers/status/2026-06-20-oci-run-broker-v1.md`

Those files describe the scaffold and the verification failure that found prior overclaims. They are not permission to narrow V1.

## Start Of Session Checklist

Before editing any production file:

- [ ] Confirm you are in `/Users/beng/Work/mercator/.worktrees/v1-run-broker`.
- [ ] Run `git status --short --branch`.
- [ ] Identify unrelated dirty files and preserve them.
- [ ] Read `Prompt.md`, `Plan.md`, this `Implement.md`, and `Documentation.md`.
- [ ] Read the verification brief and agent verification report listed above.
- [ ] State the exact milestone and task you are taking from `Plan.md`.
- [ ] Check whether the task has a clear test target and stop if it does not.

Do not revert edits you did not make. If another agent changed files relevant to your task, read the changes and work with them. Ask only if the changes make the task impossible to complete safely.

## Operating Loop

For each task in `Plan.md`:

1. Scope
   - Restate the task in one or two sentences.
   - List the exact files expected to change.
   - Confirm the task advances a V1 invariant from `Prompt.md`.
2. Red
   - Write the smallest failing test that proves the missing behavior.
   - Prefer package-level tests that exercise public/internal contracts rather than private helper details.
   - Run the focused command and record the failure in `Documentation.md`.
3. Green
   - Implement the minimal production change.
   - Keep event-log authority, idempotency, redaction, and deterministic scheduling invariants explicit in code and tests.
   - Run the focused command until it passes.
4. Broaden
   - Run the package test.
   - Run the repo verification commands listed below.
   - Fix regressions before moving on.
5. Document
   - Update `Documentation.md` with task name, files changed, tests added, focused red result, focused green result, full verification result, known gaps still open, and any decisions made.
6. Commit
   - Commit only your scoped changes for the task or coherent subtask.
   - Do not include unrelated dirty files.
   - Use factual commit messages, for example `test: capture launch replay invariant`, `feat: resume launch from recorded intent`, `fix: redact public run events`, or `docs: update v1 audit log`.

## TDD Rules

Every behavior change starts with a failing test unless the task is purely documentation. If a test cannot be written first, stop and record why in `Documentation.md` before implementing.

Required red/green evidence format:

```text
Task: <Plan.md task name>
Red: <command> -> failed because <specific missing behavior>
Green: <command> -> passed
Full: <command> -> passed
Commit: <sha or pending>
```

Tests must cover the invariant, not just the implementation shape. For V1, prefer tests that prove:

- durable event before external side effect
- expected stream version and idempotency behavior
- command replay after partial progress
- recovery from recorded intent
- no launch retry after ambiguous launch without observation/reconciliation
- cleanup confirmed before run closure
- public events/API/logs never expose secret values
- deterministic placement for identical logical inputs
- candidate rejection audit is complete and stable
- unknown facts fail hard requirements unless explicitly allowed

## Verification Commands

Run focused tests first, then broader verification.

Common focused commands:

```sh
go test ./internal/eventlog -count=1
go test ./internal/domain -count=1
go test ./internal/scheduler -count=1
go test ./internal/adapter/... -count=1
go test ./internal/orchestrator -count=1
go test ./internal/httpapi -count=1
```

Required before each task commit:

```sh
go test ./...
```

Required before milestone completion:

```sh
go test ./...
go build ./...
git status --short --branch
```

Use this stricter check before claiming a milestone is complete:

```sh
GOCACHE=$(mktemp -d) go test -mod=readonly ./...
go build ./...
```

If a command fails, do not summarize the milestone as complete. Fix the failure or record the blocker with the exact command, exit status, and relevant output.

## Checkpointing

A checkpoint is complete only when all of these are true:

- The task or milestone has passing focused tests.
- `go test ./...` passes.
- Required build checks pass when applicable.
- `Documentation.md` records the exact evidence.
- Known gaps are listed as gaps, not hidden by broad wording.
- A scoped commit exists or the user explicitly asked not to commit.

Milestone completion requires an additional review pass against `Prompt.md`:

- [ ] Which V1 requirements are implemented?
- [ ] Which requirements are partially implemented?
- [ ] Which requirements are still missing?
- [ ] Are any README/API/status claims broader than the code?
- [ ] Do tests prove the load-bearing invariants?
- [ ] Are public events and responses redaction-safe?
- [ ] Can replay/recovery proceed from durable events without repeating unsafe side effects?

## Agent Dispatch And Review

Use subagents for independent tasks when possible. Recommended pattern:

1. Primary agent reads the long-horizon docs and selects one task.
2. Dispatch one implementation agent for that task only.
3. Give the implementation agent:
   - task name from `Plan.md`
   - relevant `Prompt.md` invariants
   - exact files likely involved
   - required focused test command
   - documentation update requirement
   - instruction not to edit unrelated files
4. After the implementation agent returns, primary agent reviews the diff.
5. Primary agent runs verification commands independently.
6. Primary agent updates `Documentation.md` and commits if the task is valid.

Do not let subagents independently redefine V1 scope. They execute a task; they do not decide that missing prompt requirements are out of scope.

Review every agent result for:

- event-log authority
- replay safety
- idempotency conflicts
- side-effect ordering
- secret redaction
- deterministic scheduler output
- complete candidate rejection audit
- cleanup-before-close semantics
- API and OpenAPI truthfulness
- tests that would fail on the previous bug

## Commit Cadence

Commit after each coherent task or subtask once verification passes. Prefer small commits that can be reverted independently.

Before each commit:

```sh
git status --short
git diff --stat
go test ./...
```

Use path-limited staging:

```sh
git add <exact files>
git diff --cached --stat
git commit -m "<type>: <specific change>"
```

Never stage unrelated files to make the tree look clean. If unrelated dirty state exists, leave it alone and mention it in the handoff.

## Documentation Updates

`Documentation.md` must stay current with the code. Update it after every task and before every stop.

Each update should include:

```markdown
## <YYYY-MM-DD HH:MM PT> - <Task or Milestone>

**Scope:** <what changed>

**Files Changed:**
- `<path>`

**Verification:**
- `<command>`: <passed/failed and short evidence>

**Decisions:**
- <decision and reason, or "None">

**Known Gaps:**
- <gap, or "None newly introduced">

**Claim Check:**
- <what can now be claimed>
- <what must not yet be claimed>
```

When updating README, OpenAPI, status, CLI help, or comments, use narrow claims. Say "foundation slice", "fake adapter path", or "implemented for X" when that is what the code actually supports. Do not say "V1 complete", "production-ready", "fully event-sourced", or "adapter-ready" unless the prompt requirements and tests prove it.

## Avoiding The Prior Overclaim Mistake

The previous scaffold passed tests but still failed V1 verification because status and README language implied more than the code did.

Before any completion claim, answer these directly:

- Can the executable server run the advertised broker fast path?
- Does `POST /v1/runs` drive placement, durable launch intent, launch, observation, cleanup, and closure as claimed?
- Can replay after partial progress resume from the event log without changing placement or duplicating side effects?
- Are launch timeout and indeterminate outcomes reconciled before retry?
- Does the adapter launch contract carry the full OCI workload and selected offer context?
- Are literal env values and secrets absent from public events, logs, errors, API responses, placement records, and read models?
- Does the scheduler enforce accelerator, cost, stale offer, unknown fact, and policy constraints deterministically?
- Are required V1 endpoints, OpenAPI schemas, CLI behavior, sinks, secrets, authorization, connection service, offer service, reconciler, and projection runner either implemented or explicitly documented as missing?

If any answer is "no", the completion language must say exactly what remains missing.

## Stop Conditions

Stop immediately and update `Documentation.md` if:

- `Prompt.md` and `Plan.md` conflict.
- The next task lacks a testable acceptance condition.
- You discover a security/redaction issue in public events, logs, or API responses.
- A change would require reverting another agent's unrelated edits.
- Verification fails and the root cause is not understood.
- The implementation needs a scope decision not already made in `Prompt.md` or `Plan.md`.
- You cannot preserve event-log authority or idempotency with the current design.
- You are about to claim V1 completion without direct evidence for every done-when item.

Record the stop as:

```markdown
## Blocked - <YYYY-MM-DD HH:MM PT>

**Task:** <task>
**Reason:** <specific blocker>
**Evidence:** <commands, files, or test output>
**Decision Needed:** <the smallest decision needed to continue>
```

## End Of Session Handoff

Before ending a work session:

- [ ] Run `git status --short --branch`.
- [ ] Record completed work in `Documentation.md`.
- [ ] Record verification commands and results.
- [ ] Record open gaps and next recommended task.
- [ ] Commit scoped completed work, unless instructed otherwise.
- [ ] Leave unrelated dirty files untouched.
- [ ] State whether the next agent should continue, review, or stop for a product/design decision.

Final handoff format:

```text
Completed:
- <task or commit>

Verification:
- <command>: <result>

Documentation:
- Updated <path> with <summary>

Still Open:
- <specific remaining gap>

Next:
- <next Plan.md task or required decision>
```
