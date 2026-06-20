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

See the design notes for the full V1 ontology, invariants, and roadmap.

## Build

```sh
go build ./...
go test ./...
```

The project uses a pure-Go SQLite driver (`modernc.org/sqlite`) so the binary
builds without cgo.
