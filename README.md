# Mercator

An **event-sourced OCI run broker**.

> Run this immutable container workload against the best currently feasible offer.

Mercator is a single Go process with an embedded SQLite event log, in-process
provider adapters, and a deterministic scheduler. It places immutable container
workloads onto the best currently feasible offer across providers — with no
cluster object, SSH bootstrap, code synchronization, or managed runtime.

## Status

Early V1 foundation. The current implementation covers the load-bearing core:

1. **Event core** — SQLite `EventLog` (atomic multi-event append, optimistic
   concurrency, command idempotency), CloudEvents-shaped envelope, in-process
   subscriptions, and read-model projections.
2. **Domain** — Workload / Run / Attempt / Offer / Decision / Lease models and
   their state machines.
3. **Test adapter** — a deterministic fake provider with programmable queues,
   failures, and indeterminate launches.
4. **Scheduler** — pure capability filtering plus a money-equivalent cost/latency
   placement model that emits an audited decision.
5. **Run fast path** — event-first orchestration through placement, launch intent,
   fake-adapter launch, observation, cleanup confirmation, and run closure.
6. **HTTP API** — minimal REST surface for run creation, run events, placement
   preview, health, and OpenAPI discovery.

See the design notes for the full V1 ontology, invariants, and roadmap.

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
