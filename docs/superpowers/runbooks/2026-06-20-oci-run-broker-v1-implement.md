# OCI Run Broker V1 Runbook

Use `docs/superpowers/plans/2026-06-20-oci-run-broker-v1.md` as the execution source of truth.

## Operating Rules

- Keep diffs scoped to the current task.
- Use TDD: write a failing test, run it, implement, rerun the focused test, then run `go test ./...`.
- If validation fails, repair before moving to the next task.
- Update `docs/superpowers/status/2026-06-20-oci-run-broker-v1.md` after each milestone.
- Do not implement out-of-scope adapters, sinks, secrets, or UI in this slice.
- Do not add alternate event buses.

## Verification Commands

```sh
go test ./...
go build ./...
```

## Done When

- Event log, domain, scheduler, fake adapter, orchestrator, and HTTP API packages compile and pass tests.
- The HTTP API can accept a run with an idempotency key and return run events.
- Placement preview returns candidate evaluations and structured rejections.
- Status log records completed milestones and known V1 gaps.
