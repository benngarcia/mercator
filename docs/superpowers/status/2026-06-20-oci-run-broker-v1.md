# OCI Run Broker V1 Status

## Current Milestone

Task 7: Final Verification.

## Completed

- Created isolated worktree at `/Users/beng/Work/mercator/.worktrees/v1-run-broker` on branch `beng/v1-run-broker`.
- Trusted the worktree `mise.toml`.
- Confirmed initial baseline has no Go packages to test yet.
- Created durable long-horizon artifacts: spec, plan, runbook, status.
- Implemented Task 1 event core with SQLite append/read/idempotency/concurrency/subscription behavior.
- Implemented Task 2 domain contracts and workload validation for the V1 OCI boundary.
- Implemented Task 3 pure deterministic scheduler with structured hard rejections and score-based selection.
- Implemented Task 4 adapter contract and deterministic fake adapter with idempotent lifecycle methods.
- Implemented Task 5 run orchestrator fast path with event-first placement/launch, fake-adapter observation, cleanup confirmation, and run closure.
- Implemented Task 6 minimal REST API, OpenAPI document, and `cmd/mercator` entrypoint.

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
- Task 2 red test: `go test ./internal/domain -count=1` failed on missing domain API/types.
- Task 2 focused green: `go test ./internal/domain -count=1` passed.
- Task 2 full green: `go test ./...` passed for `internal/domain` and `internal/eventlog`.
- Task 3 red test: `go test ./internal/scheduler -count=1` failed on missing scheduler API/types.
- Task 3 focused green: `go test ./internal/scheduler -count=1` passed.
- Task 3 full green: `go test ./...` passed for `internal/domain`, `internal/eventlog`, and `internal/scheduler`.
- Task 4 red test: `go test ./internal/adapter/... -count=1` failed on missing adapter package.
- Task 4 focused green: `go test ./internal/adapter/... -count=1` passed.
- Task 4 full green: `go test ./...` passed for adapter, domain, eventlog, and scheduler packages.
- Task 5 red test: `go test ./internal/orchestrator -count=1` failed on missing orchestrator API/events.
- Task 5 focused green: `go test ./internal/orchestrator -count=1` passed.
- Task 5 full green: `go test ./...` passed for adapter, domain, eventlog, orchestrator, and scheduler packages.
- Task 6 red test: `go test ./internal/httpapi -count=1` failed on missing HTTP API types/server.
- Task 6 focused green: `go test ./internal/httpapi -count=1` passed.
- Task 6 full green: `go test ./...` passed for all packages.
- Task 6 build green: `go build ./...` passed.

## Known Gaps

- Docker host adapter is not implemented in this slice.
- Secret encryption and service credentials are not implemented in this slice.
- Webhook/Kafka/Postgres sinks are not implemented in this slice.
- Embedded UI is not implemented in this slice.
