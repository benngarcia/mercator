# OCI Run Broker V1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build Mercator's event-sourced OCI run broker foundation.

**Architecture:** Implement the load-bearing core as focused Go packages over a single SQLite event log. Side effects are driven only after durable intent events, and scheduling remains a pure deterministic function.

**Tech Stack:** Go 1.25, `modernc.org/sqlite`, standard-library HTTP, table-driven Go tests.

## Global Constraints

- One Go process and one deployable image.
- SQLite is the single internal event backend.
- No internal Kafka/NATS/Postgres event-bus abstraction.
- OCI-only workload boundary: image, platform, entrypoint, args, environment, ports.
- V1 validates exactly one Linux container named `main`.
- Tags may be accepted later, but this slice requires digest-pinned images before placement.
- No mounts, workdirs, setup commands, agents, stdin, TTY, Ray, SSH, or persistent clusters.
- Every mutation command has an idempotency key.
- No provider side effect before a durable intent event.

---

### Task 1: Event Core

**Files:**
- Create: `internal/eventlog/eventlog.go`
- Create: `internal/eventlog/sqlite.go`
- Create: `internal/eventlog/sqlite_test.go`

**Interfaces:**
- Produces: `EventLog`, `AppendRequest`, `AppendResult`, `StoredEvent`, `StreamKey`, `GlobalPosition`.
- Produces: `OpenSQLite(ctx context.Context, dsn string) (*SQLiteEventLog, error)`.

- [ ] Write failing tests for atomic append/read, optimistic concurrency, command idempotency conflict, global reads, and subscription wakeup.
- [ ] Run `go test ./internal/eventlog -run TestSQLiteEventLog -count=1` and confirm failure from missing package/types.
- [ ] Implement the SQLite schema and event log.
- [ ] Run `go test ./internal/eventlog -count=1`.
- [ ] Run `go test ./...`.
- [ ] Commit `feat: add sqlite event log`.

### Task 2: Domain Contracts

**Files:**
- Create: `internal/domain/types.go`
- Create: `internal/domain/validation.go`
- Create: `internal/domain/hash.go`
- Create: `internal/domain/domain_test.go`

**Interfaces:**
- Produces: `WorkloadSpec`, `WorkloadRevision`, `OfferSnapshot`, `PlacementDecision`, `RunRecord`, `AttemptRecord`.
- Produces: `ValidateWorkloadRevision(rev WorkloadRevision) []Violation`.
- Produces: `CanonicalHash(v any) (string, error)`.

- [ ] Write failing tests for exactly-one-container validation, digest image requirement, Linux platform requirement, duplicate env rejection, public port capability requirement, and canonical hash stability.
- [ ] Run `go test ./internal/domain -count=1` and confirm failure from missing behavior.
- [ ] Implement domain types and validators.
- [ ] Run `go test ./internal/domain -count=1`.
- [ ] Run `go test ./...`.
- [ ] Commit `feat: add oci workload domain`.

### Task 3: Scheduler

**Files:**
- Create: `internal/scheduler/scheduler.go`
- Create: `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: `domain.WorkloadRevision`, `domain.OfferSnapshot`.
- Produces: `Scheduler.Evaluate(ctx context.Context, input SchedulingInput) (domain.PlacementDecision, error)`.

- [ ] Write failing tests for deterministic lowest-score selection, hard capability rejection, stale offer rejection, unknown network fact rejection unless allowed, and no-feasible-offer decision.
- [ ] Run `go test ./internal/scheduler -count=1` and confirm failure from missing package/types.
- [ ] Implement feasibility filtering and money-equivalent scoring.
- [ ] Run `go test ./internal/scheduler -count=1`.
- [ ] Run `go test ./...`.
- [ ] Commit `feat: add deterministic scheduler`.

### Task 4: Fake Adapter

**Files:**
- Create: `internal/adapter/adapter.go`
- Create: `internal/adapter/fake/fake.go`
- Create: `internal/adapter/fake/fake_test.go`

**Interfaces:**
- Consumes: `domain.OfferSnapshot`.
- Produces: `Adapter` interface and fake implementation with idempotent `Launch`, `Observe`, `Cancel`, `Release`, and `ListOwned`.

- [ ] Write failing tests for idempotent launch, idempotency conflict on key reuse with different hash, deterministic ownership listing, terminal observation, and release success when absent.
- [ ] Run `go test ./internal/adapter/... -count=1` and confirm failure from missing package/types.
- [ ] Implement adapter contracts and fake adapter.
- [ ] Run `go test ./internal/adapter/... -count=1`.
- [ ] Run `go test ./...`.
- [ ] Commit `feat: add deterministic fake adapter`.

### Task 5: Orchestrator Fast Path

**Files:**
- Create: `internal/orchestrator/orchestrator.go`
- Create: `internal/orchestrator/orchestrator_test.go`

**Interfaces:**
- Consumes: `eventlog.EventLog`, `scheduler.Scheduler`, `adapter.Adapter`.
- Produces: `Orchestrator.CreateRun`, `Orchestrator.AdvanceRun`, `Orchestrator.GetRunEvents`.

- [ ] Write failing tests proving `LaunchIntentRecorded` is appended before adapter launch, repeated create command returns the existing run, launch conflict is visible as an event, and a successful fake run reaches cleanup confirmed and closed.
- [ ] Run `go test ./internal/orchestrator -count=1` and confirm failure from missing package/types.
- [ ] Implement the command/event flow for the fake-adapter fast path.
- [ ] Run `go test ./internal/orchestrator -count=1`.
- [ ] Run `go test ./...`.
- [ ] Commit `feat: add run orchestrator fast path`.

### Task 6: HTTP API And OpenAPI

**Files:**
- Create: `internal/httpapi/server.go`
- Create: `internal/httpapi/openapi.go`
- Create: `internal/httpapi/server_test.go`
- Create: `cmd/mercator/main.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `orchestrator.Orchestrator`.
- Produces: `POST /v1/runs`, `GET /v1/runs/{run_id}/events`, `POST /v1/placements:preview`, `GET /openapi.json`, `GET /health/live`, `GET /health/ready`.

- [ ] Write failing HTTP tests for required `Idempotency-Key`, accepted run response, event listing, placement preview, and OpenAPI availability.
- [ ] Run `go test ./internal/httpapi -count=1` and confirm failure from missing package/types.
- [ ] Implement minimal HTTP server and CLI entrypoint.
- [ ] Run `go test ./internal/httpapi -count=1`.
- [ ] Run `go test ./...`.
- [ ] Run `go build ./...`.
- [ ] Commit `feat: expose broker api`.

### Task 7: Final Verification

**Files:**
- Modify: `docs/superpowers/status/2026-06-20-oci-run-broker-v1.md`

- [ ] Run `go test ./...`.
- [ ] Run `go build ./...`.
- [ ] Run `git status --short`.
- [ ] Update status log with completed milestones, verification output, known gaps, and next work.
- [ ] Commit `docs: update v1 implementation status`.
