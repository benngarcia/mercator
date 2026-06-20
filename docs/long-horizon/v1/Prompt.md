# Mercator OCI Run Broker V1 Prompt

## Purpose

Build Mercator V1 as an event-sourced OCI run broker, not a reduced cluster manager.

Primary abstraction:

> Run this immutable container workload against the best currently feasible offer.

Mercator V1 is a single Go process with an embedded SQLite event log, in-process runtime adapters, a deterministic scheduler, a REST/OpenAPI API, an agent-native CLI, and an embedded static UI. It places immutable OCI container workloads onto provider offers while preserving event-log authority, auditable placement, idempotent side effects, cleanup guarantees, and secret non-observability.

## Branch State Warning

The current `beng/v1-run-broker` branch state is a scaffold, not V1.

It contains useful foundation pieces: event log basics, domain validation basics, scheduler basics, a fake adapter, a narrow internal orchestrator happy path, and a minimal HTTP surface. It does not satisfy this Prompt. Future agents must treat existing implementation and earlier status language as partial scaffolding only, and must use this file as the frozen V1 target.

## Long-Horizon Structure

This document is the canonical frozen prompt for `docs/long-horizon/v1/Prompt.md`.

The remaining long-horizon files follow this structure:

- `Plan.md`: checkpointed milestones with acceptance criteria.
- `Implement.md`: operational runbook for agents executing the plan.
- `Documentation.md`: live status, audit log, verification evidence, decisions, and known gaps.

## Goals

Mercator V1 must provide:

1. An event-sourced run broker for immutable OCI workloads.
2. A SQLite-backed event log as the only internal source of truth.
3. Public state derived from recorded events, never from unrecorded observations.
4. Deterministic placement against cached offer snapshots.
5. Audited placement decisions recorded before launch.
6. Exactly one idempotent adapter launch call per launch intent.
7. Observation and reconciliation before terminal state.
8. Cleanup confirmation before any run is closed.
9. Secret redaction across events, logs, read models, errors, and APIs.
10. Disposable read models, offer caches, and scheduler statistics.
11. REST/OpenAPI, CLI, and embedded UI surfaces that expose the same broker semantics.

## Non-Goals

V1 must not become a cluster manager or remote execution platform.

Out of scope:

- Cluster objects.
- Remote controllers.
- SSH bootstrap.
- Ray or managed runtime orchestration.
- Setup phases.
- Code synchronization.
- File mounts.
- Working directories.
- Arbitrary Docker flags.
- Host networking.
- Interactive shells.
- stdin or TTY.
- Post-start recovery by mutating a running workload.
- Provider-specific lifecycle state as an internal source of truth.
- Treating adapters, read models, or caches as authoritative.

## Core Fast Path

The required fast path is:

1. Append `RunRequested`.
2. Evaluate cached offer snapshots.
3. Append `PlacementDecided` and `LaunchIntentRecorded`.
4. Make one idempotent adapter `Launch` call.
5. Observe until terminal or reconcile indeterminate state.
6. Request cleanup.
7. Confirm cleanup.
8. Close the run only after cleanup is confirmed.

No external side effect may happen before its corresponding durable event is recorded.

## Workload Contract

V1 accepts only immutable OCI workload revisions.

A workload revision contains:

- Workspace ID.
- Workload ID.
- Revision ID.
- Exactly one Linux container named `main`.
- Digest-pinned OCI image reference.
- Concrete platform, including OS and architecture.
- Entrypoint.
- Args.
- Environment bindings.
- Declared public ports.
- Resource requirements.
- Accelerator requirements, if any.
- Network requirements.
- Policy controlling whether unknown offer facts may satisfy requirements.

V1 workload restrictions:

- Exactly one container.
- Container name must be `main`.
- OS must be Linux.
- Image must be digest-pinned before run creation.
- Tags may be accepted only through the image resolver, which records the resolved digest and metadata.
- No mounts.
- No workdir.
- No setup commands.
- No sidecar agents.
- No stdin.
- No TTY.
- No host networking.
- No arbitrary raw Docker flags.
- No arbitrary extension payloads that bypass validation.

Environment variables:

- Literal values are allowed only when safe to expose.
- Secret-backed values must reference secret grants, not secret material.
- Public events must never contain secret values.
- API responses must never contain secret values.
- Logs and errors must never contain secret values.
- Empty bindings, invalid names, oversized values, and ambiguous literal/secret bindings must be rejected.

## Event Log Requirements

SQLite is the only internal source-of-truth event backend.

The event log must support:

- Expected stream version.
- Atomic multi-event append.
- Command idempotency.
- Canonical request hash.
- Idempotency conflict detection.
- Stream reads.
- Global reads.
- Durable global ordering.
- In-process subscriptions.
- CloudEvents-shaped stored events.
- Durable sink cursors.
- Replay from global positions.

Rules:

- Public state is derived from the event log.
- External observations become authoritative only after recorded events.
- Read models are disposable.
- Offer caches are disposable.
- Scheduler statistics are disposable.
- Rebuilding projections from the event log must recover public broker state.
- Command retries must be safe after partial progress.

## Required Invariants

### Event Authority

- Every state transition must be represented by an event.
- No adapter observation is authoritative until recorded.
- No run closes before cleanup confirmation.
- Recorded launch intent must be recovered before any new placement decision is attempted.
- Durable cleanup request must be recovered before any relaunch path is considered.

### Idempotency

- Commands are idempotent by operation key and canonical request hash.
- Reusing an idempotency key with a different request hash returns a machine-readable conflict.
- Adapter launch is idempotent by deterministic launch key and ownership token.
- Adapter cleanup is idempotent by cleanup locator and ownership token.
- Retrying after process crash must resume from recorded events.

### Placement

- Scheduler is pure and deterministic for identical snapshots, policy, model version, and evaluation time.
- Candidate ordering must be deterministic independent of input order.
- Unknown facts fail hard requirements unless explicitly allowed by workload policy.
- Accelerator, network, capacity, price, image cache, and platform requirements must be enforced.
- Candidate rejections must be audited.
- Placement decisions must be recorded before launch.
- Decision records must include enough context to explain why the winner was selected and why rejected candidates failed.

### Side Effects

Every external side effect must have:

- Deterministic operation key.
- Deterministic launch or cleanup key.
- Ownership token.
- Cleanup locator.
- Recorded intent event before execution.
- Recorded result, failure, or indeterminate observation after execution.

Launch timeout or indeterminate launch result must not become a simple launch failure. It must trigger observe/ListOwned reconciliation before retry or replacement.

### Cleanup

- Cleanup must be requested for every launched workload.
- Cleanup retry must resume from recorded cleanup events.
- Run closure requires cleanup confirmation.
- Orphan detection must use ownership tokens and adapter `ListOwned`.

### Sinks

- Sink failures must not block placement or lifecycle progress.
- Sinks use durable cursors.
- Sinks are replayable.
- Webhook, Kafka, and Postgres sinks must preserve event ordering per configured stream or cursor contract.

## API Surface

V1 must expose a REST API with an OpenAPI document that includes request bodies, response schemas, event schemas, error schemas, and idempotency conflict responses.

Required API areas:

### Workloads

- Create workload.
- Create immutable workload revision.
- Get workload.
- List workloads.
- Get workload revision.
- List workload revisions.
- Resolve image tag to digest through the image resolver.

### Runs

- Create run.
- Cancel run.
- Get run.
- List runs.
- Wait for run.
- Stream or poll run events.
- Get placement decision.
- Refresh/reconcile run.
- Return links to related workload, revision, events, decision, and cleanup state.

### Connections

- Create connection.
- Get connection.
- List connections.
- Update connection authorization.
- Validate adapter-defined authorization schema.

### Secrets

- Create secret.
- Create encrypted secret version.
- Grant secret to workload/run scope.
- Revoke grant.
- List secret metadata without exposing values.

### Offers

- Refresh offers.
- List cached offers.
- Get offer snapshot.
- Explain offer freshness and source.

### Sinks

- Configure sink.
- Pause/resume sink.
- Replay sink from cursor.
- Inspect sink cursor and delivery status.

### Health And Metadata

- Health check.
- Readiness check.
- OpenAPI discovery.
- Version/build metadata.

## Services

V1 must include these services:

### Workload Service

Owns workload definitions, immutable revisions, OCI validation, digest-pinned image requirements, and public workload metadata.

### Run Service

Owns run commands, run state derivation, create/cancel/get/list/wait/events/decision/refresh behavior, and run-level idempotency.

### Connection Service

Owns provider connection records and adapter-defined authorization schemas.

### Secret Vault

Owns encrypted event-backed secret versions and grants. Secret values are never emitted into public events or read models.

### OCI Image Resolver

Resolves tags to digests and records artifact metadata. Runs reference immutable workload revisions and digest-pinned images.

### Offer Service

Collects and caches offer snapshots from adapters. Cached offers are disposable and must be reconstructable or refreshable without corrupting broker state.

### Latency Estimator

Estimates start latency from recorded observations and offer metadata. Its statistics are disposable and must not be required for correctness.

### Scheduler

Pure deterministic placement engine over workload revision, policy, model version, evaluation time, and offer snapshots.

### Adapter Registry

Maps adapter types to runtime adapter implementations, authorization schemas, offer collection, launch, observe, cancel, release, and ListOwned behavior.

### Runtime Adapters

Implement provider-specific behavior behind the common adapter contract.

Required V1 adapters:

- Deterministic fake adapter for tests and reconciliation development.
- Docker host adapter for local OCI execution.

### Reconciler

Resumes incomplete runs from the event log, observes ambiguous launches, repairs lifecycle progress, and prevents duplicate side effects.

### Lease Janitor

Finds owned external resources through adapter ownership tokens and cleanup locators, then records and drives cleanup.

### Projection Runner

Builds disposable read models from the event log.

### Event Sinks

Delivers events to configured webhook, Kafka, and Postgres sinks using durable cursors and replay support.

### Authorization Service

Enforces workspace isolation, command authorization, connection access, secret grants, and API permissions.

### Embedded Static UI

Provides an operator UI for workloads, runs, events, decisions, offers, sinks, and reconciliation state.

### CLI

Provides JSON-first, agent-native commands for workloads, runs, events, decisions, offers, connections, secrets, sinks, and health.

## Adapter Contract

Adapters must expose:

- Authorization schema.
- Offer collection.
- Launch.
- Observe.
- Cancel.
- Release/cleanup.
- ListOwned.

`Launch` input must include:

- Workload revision identity.
- OCI image digest.
- Platform.
- Entrypoint.
- Args.
- Environment binding metadata, without secret values.
- Ports.
- Resource requirements.
- Accelerator requirements.
- Selected offer identity.
- Selected adapter/native ref.
- Deterministic launch key.
- Ownership token.
- Cleanup locator or enough information to derive one.

Adapters must not receive secret values unless explicitly required at execution time through a controlled secret materialization path that does not log or persist them in public state.

## Security

V1 security requirements:

- Workspace isolation at every API boundary.
- Authorization checks for every command and read.
- Connection credentials stored through secret-backed mechanisms.
- Event payloads are public-safe by default.
- Secret values never appear in public events, read models, logs, errors, placement records, OpenAPI examples, CLI output, or UI responses.
- Secret grant records expose metadata only.
- Idempotency conflicts are machine-readable and do not leak request secrets.
- Adapter errors are sanitized before public recording.
- Audit records preserve who requested commands and which workspace they affected.
- Durable event history must be safe to inspect without privileged secret access.

## Deliverables

V1 is delivered when the repository contains:

1. Production implementation for all services listed above.
2. SQLite event log satisfying the required event semantics.
3. Event schemas for all public and internal lifecycle events.
4. Full REST API and OpenAPI document.
5. JSON-first CLI.
6. Embedded static UI.
7. Fake adapter.
8. Docker host adapter.
9. Reconciler and lease janitor.
10. Secret vault with encrypted event-backed secret versions and grants.
11. Offer service and cached offer snapshots.
12. Deterministic scheduler with audited decisions.
13. Webhook, Kafka, and Postgres sinks with replay and durable cursors.
14. Projection runner and disposable read models.
15. Authorization service and workspace isolation.
16. Tests proving correctness of event authority, idempotency, placement, reconciliation, cleanup, redaction, API behavior, and sink replay.
17. Documentation for operators and agent developers.

## Done When

V1 is done only when all of the following are true:

- A fresh run follows the required fast path from `RunRequested` through cleanup confirmation.
- Crash/retry after every durable event resumes without duplicate side effects.
- Re-running create/cancel/refresh commands is idempotent.
- Reusing an idempotency key with a different request hash returns a conflict.
- Placement is deterministic for identical inputs, regardless of input offer ordering.
- Candidate rejections and winner selection are recorded and explainable.
- Accelerator, capacity, network, platform, image, price, and unknown-fact requirements are enforced.
- Launch intent is recorded before adapter launch.
- Existing launch intent is recovered before any new placement decision.
- Ambiguous launch outcomes trigger observation/ListOwned reconciliation before retry.
- Cleanup is confirmed before run closure.
- Orphaned owned resources are discoverable and cleanable.
- Secret values do not appear in events, logs, read models, errors, API responses, CLI output, UI responses, or placement records.
- Public state can be rebuilt from the SQLite event log.
- Read models, offer caches, and scheduler statistics can be discarded without losing broker truth.
- Sink delivery failures do not block run lifecycle progress.
- Sink cursors support replay.
- REST API, OpenAPI, CLI, and UI expose consistent semantics.
- Docker host adapter can execute a valid digest-pinned OCI workload.
- Fake adapter supports deterministic lifecycle and failure testing.
- `go test ./...` passes.
- `go build ./...` passes.
- Focused tests cover event log, domain validation, scheduler, adapters, orchestrator/reconciler, API, CLI, secrets, sinks, and UI.
- Documentation states remaining limitations honestly.
- No README, status file, API doc, or UI copy claims the scaffold is complete V1 before these conditions are met.
