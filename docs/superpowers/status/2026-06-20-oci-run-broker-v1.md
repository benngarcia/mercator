# OCI Run Broker V1 Status

## Current Milestone

Task 2: Domain Contracts.

## Completed

- Created isolated worktree at `/Users/beng/Work/mercator/.worktrees/v1-run-broker` on branch `beng/v1-run-broker`.
- Trusted the worktree `mise.toml`.
- Confirmed initial baseline has no Go packages to test yet.
- Created durable long-horizon artifacts: spec, plan, runbook, status.
- Implemented Task 1 event core with SQLite append/read/idempotency/concurrency/subscription behavior.

## Decisions

- Implement the load-bearing V1 foundation first, not every downstream adapter/UI/sink feature in one unreviewable diff.
- Use `modernc.org/sqlite` for cgo-free SQLite.
- Require digest-pinned images in this slice. Tag resolution will be a later ImageResolver milestone.
- Use a deterministic fake adapter before Docker to verify event-first side-effect boundaries.

## Verification Log

- `go mod download`: no module dependencies to download.
- `go test ./...`: exited 1 because there are no packages yet (`"./..." matched no packages`).
- Task 1 red test: `go test ./internal/eventlog -run TestSQLiteEventLog -count=1` failed on missing eventlog API/types.
- Task 1 focused green: `go test ./internal/eventlog -run TestSQLiteEventLog -count=1` passed.
- Task 1 full green: `go test ./...` passed for `internal/eventlog`.

## Known Gaps

- Docker host adapter is not implemented in this slice.
- Secret encryption and service credentials are not implemented in this slice.
- Webhook/Kafka/Postgres sinks are not implemented in this slice.
- Embedded UI is not implemented in this slice.
