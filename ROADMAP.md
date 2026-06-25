# Mercator Roadmap

This roadmap is launch-facing. It summarizes what would make Mercator easier to
try, trust, and contribute to as an open source project. Detailed V1 production
hardening lives in [docs/production/known-limitations.md](docs/production/known-limitations.md).

## Current: V1 Evaluation

- Event-sourced run lifecycle over SQLite.
- Fake adapter for deterministic local evaluation.
- Docker host adapter with guarded live integration test.
- RunPod-oriented adapter and examples.
- Operator console embedded in the Go binary.
- TypeScript, Python, and Ruby SDKs.
- Production evaluation runbooks.
- OSS project scaffolding: license, security policy, contribution guide, issue
  templates, CI workflow, screenshots, and launch scorecard.
- Short fake-adapter console demo committed under `docs/assets/`.

## Next: Open Source Launch Polish

- Add a first tagged release with downloadable binaries and checksums.
- Publish SDK packages or document exact local install paths for each language.
- Add public CI badges after the first GitHub Actions run succeeds.
- Write a short compatibility policy for the V1 HTTP API and SDKs.
- Add one external user story or case-study style narrative once there is a
  maintainer-approved public reference.

## Next: Production Hardening

- Finish registry-backed image resolution and credential handling.
- Decide external sink configuration for Kafka/Postgres or keep the current
  audit sink boundary explicit.
- Expand RunPod setup docs with a complete credential and quota checklist.
- Exercise backup/restore and no-orphan cleanup drills in a real environment.
- Clarify release, migration, and rollback mechanics for SQLite-backed
  deployments.

## Later

- Additional provider adapters with the same auditable run contract.
- Package-manager distribution for the CLI/server.
- More console affordances for run comparison and placement diagnostics.
- Compatibility tests across SDKs and the OpenAPI document.

## Non-Goals

- Replacing Kubernetes, Slurm, or a managed batch platform.
- Becoming a secret manager for workload-owned secrets.
- Hiding provider-specific constraints behind an opaque scheduler.
- Optimizing for multi-tenant SaaS operation before the single-process operator
  model is trustworthy.
