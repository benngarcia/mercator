# Mercator

An **event-sourced OCI run broker**.

> Run this immutable container workload against the best currently feasible offer.

Mercator is a single Go process with an embedded SQLite event log, in-process
provider adapters, and a deterministic scheduler. It places immutable container
workloads onto the best currently feasible offer across providers — with no
cluster object, SSH bootstrap, code synchronization, or managed runtime.

## Status

M13-verified V1 slice. The current implementation covers the load-bearing run
broker path and operator surfaces:

1. **Event core** — SQLite `EventLog` (atomic multi-event append, optimistic
   concurrency, command idempotency), CloudEvents-shaped envelope, in-process
   subscriptions, durable cursors, and read-model projections.
2. **Domain** — Workload / Run / Attempt / Offer / Decision / connection /
   secret / sink models and their state transitions.
3. **Adapters** — deterministic fake provider and Docker host adapter, including
   a guarded live Docker integration test.
4. **Scheduler** — pure capability filtering plus a money-equivalent cost/latency
   placement model that emits an audited decision.
5. **Run lifecycle** — event-first orchestration through placement, launch intent,
   launch, observation, cancel, cleanup confirmation, and run closure.
6. **Surfaces** — REST/OpenAPI, JSON-first CLI, embedded static UI, secret vault,
   workload revisions, offer reads, connection reads, sink replay, and health.

Remaining limitations are tracked in `docs/long-horizon/v1/Documentation.md`;
notably production key management, registry-backed tag resolution, and concrete
Kafka/Postgres client wiring are still basic.

## Build

```sh
go build ./...
go test ./...
```

The project uses a pure-Go SQLite driver (`modernc.org/sqlite`) so the binary
builds without cgo.

## Run

```sh
MERCATOR_SQLITE_DSN='file:/tmp/mercator.db' go run ./cmd/mercator
```

The server listens on `:8080` by default. Set `MERCATOR_ADDR` to override it.
For local fake-adapter smoke testing, set `MERCATOR_FAKE_OFFER=1`.
