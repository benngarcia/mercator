# OCI Run Broker V1 Design

## Goal

Build Mercator as an event-sourced OCI run broker: run an immutable container workload against the best currently feasible offer.

## Reference Operating Model

This work follows the long-horizon Codex pattern from the OpenAI Developers blog: durable spec, checkpointed plan, runbook, continuous verification, and a status/audit log. The repo artifacts are:

- `docs/superpowers/specs/2026-06-20-oci-run-broker-v1-design.md`
- `docs/superpowers/plans/2026-06-20-oci-run-broker-v1.md`
- `docs/superpowers/runbooks/2026-06-20-oci-run-broker-v1-implement.md`
- `docs/superpowers/status/2026-06-20-oci-run-broker-v1.md`

## V1 Scope For This Implementation Slice

The V1 prompt is broad enough to become several production milestones. This first implementation establishes the load-bearing core that the rest of V1 depends on:

- SQLite event log with atomic multi-event append, optimistic stream concurrency, command idempotency, stream reads, global reads, and in-process subscriptions.
- CloudEvents-shaped stored event envelope.
- Workload, container, resource, network, run, attempt, offer, and placement decision domain types.
- Workload validation for OCI-only, Linux-only, exactly-one-container V1 semantics.
- Deterministic scheduler that filters hard constraints, reports structured violations, estimates cost/start latency, and records an audited decision.
- Deterministic fake adapter for tests and orchestrator development.
- Minimal run orchestrator fast path for fake-adapter launches: request run, resolve image if already digest-pinned, decide placement, record launch intent, launch idempotently, observe terminal outcome, and request/confirm cleanup.
- Minimal HTTP API and OpenAPI document for core run and placement operations.

Out of this slice:

- Real Docker host adapter.
- External provisionable adapter.
- Secret encryption and grants.
- Webhook/Kafka/Postgres sink delivery.
- Embedded UI.
- Generated SDKs.

Those are intentionally downstream of the event/domain/scheduler/orchestrator contracts.

## Architecture

One Go module exposes focused internal packages:

- `internal/eventlog`: SQLite-backed event store and subscription bus.
- `internal/domain`: domain types, validation, state helpers, IDs, and canonical JSON hashing.
- `internal/scheduler`: pure scheduler implementation over resolved workload revisions and offer snapshots.
- `internal/adapter/fake`: deterministic adapter used for orchestration and crash-boundary tests.
- `internal/orchestrator`: command handlers that append events before side effects and drive the fake launch lifecycle.
- `internal/httpapi`: REST handlers and embedded OpenAPI JSON.

SQLite is the only source-of-truth event backend. Read models are disposable and will be kept minimal in this slice.

## Invariants Implemented In This Slice

- OCI-only workload boundary.
- Exactly one container in V1.
- Immutable run input references an exact workload revision and digest-pinned image.
- Event-first side effects for launch and cleanup.
- Event-log authority for state transitions.
- Capability honesty with structured violations.
- Deterministic placement with auditable candidates and rejections.
- No untracked fake-adapter side effects.
- Idempotent API command acceptance and adapter launches.
- Secret non-observability at the domain boundary; actual secret vault arrives later.

## Testing Strategy

Use TDD for each production behavior:

- `internal/eventlog`: append/read/idempotency/concurrency/subscription tests against shared in-memory SQLite.
- `internal/domain`: workload validation and canonical hashing tests.
- `internal/scheduler`: deterministic selection and capability rejection tests.
- `internal/adapter/fake`: idempotent launch, conflict, observe, cancel, release tests.
- `internal/orchestrator`: verifies event-before-side-effect ordering and run closure path.
- `internal/httpapi`: mutation idempotency and API shape smoke tests.

Every milestone ends with `go test ./...`.
