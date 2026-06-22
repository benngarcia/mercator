# OCI Run Broker V1 Verification Brief

This file exists to keep verification aligned with the original project prompt.
The implementation under review is a first foundation slice on branch
`beng/v1-run-broker`, not a complete production V1.

## Original Target Summary

Build an event-sourced OCI run broker, not a reduced cluster manager.

Primary abstraction:

> Run this immutable container workload against the best currently feasible offer.

Fast path:

1. Append `RunRequested`.
2. Evaluate cached offer snapshots.
3. Append `PlacementDecided` and `LaunchIntentRecorded`.
4. Make one idempotent adapter `Launch` call.
5. Observe until terminal.
6. Confirm cleanup.

Non-goals include cluster objects, remote controllers, SSH bootstrap, Ray,
setup phases, code synchronization, file mounts, workdirs, arbitrary Docker
flags, host networking, interactive shells, and post-start recovery.

## Load-Bearing Requirements To Verify

- One Go process, embedded SQLite event log, in-process adapters, REST/OpenAPI API.
- SQLite is the only internal source-of-truth event backend.
- Event log supports expected stream version, atomic multi-event append,
  command idempotency, canonical request hash, stream/global reads,
  subscriptions, and CloudEvents-shaped events.
- Public state is derived from the event log. External observations become
  authoritative only after recorded events.
- Workload boundary is OCI-only: image, concrete platform, entrypoint, args,
  env, and ports. No mounts, workdirs, setup commands, agents, stdin, or TTY.
- V1 validates exactly one Linux container named `main`.
- Runs reference immutable workload revisions and digest-pinned images.
- Unknown facts fail hard requirements unless the workload explicitly allows
  unknowns.
- Scheduler is pure and deterministic for identical snapshots, policy, model
  version, and evaluation time.
- Placement decisions are audited before launch and include candidate rejections.
- Every external side effect has deterministic launch key, ownership token, and
  cleanup locator.
- No run closes before cleanup is confirmed.
- Commands and lifecycle operations are idempotent.
- Secret values never appear in public events, logs, read models, placement
  records, errors, or API responses.
- Sink failures must not block placement or lifecycle progress.
- Read models, offer caches, and scheduler statistics are disposable.
- Launch timeouts and indeterminate launches require observation/reconciliation
  before new attempts.

## Major V1 Feature Areas From The Prompt

- Workload service and immutable revisions.
- Run service with create, cancel, get/list/wait/events/decision/refresh.
- Connection service with adapter-defined authorization schemas.
- Secret vault with encrypted event-backed secret versions and grants.
- OCI image resolver for tag-to-digest and artifact metadata.
- Offer service and cached offer snapshots.
- Latency estimator.
- Scheduler.
- Adapter registry and runtime adapters.
- Reconciler and lease janitor.
- Projection runner.
- Event sinks: webhooks, Kafka, Postgres, replay, durable cursors.
- Authorization service.
- Docker host adapter.
- Embedded static UI.
- CLI with JSON-first agent-native behavior.

## Verification Output Requested

Reviewers should report findings first, ordered by severity, with local file and
line references when possible. Distinguish:

- Implemented and reasonably tested.
- Partially implemented but violating an invariant.
- Documented as known gap.
- Missing but required by the original prompt.
- Overclaim in README/status/API relative to code.
