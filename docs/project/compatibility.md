# Compatibility Policy

Mercator is pre-1.0. The project still optimizes for a trustworthy V1 run
broker over long-term compatibility, but public users should be able to tell
which surfaces are intended to be stable and how breaking changes will be
handled.

## Versioning

- Tags use semantic-version-shaped names: `v0.MINOR.PATCH` before 1.0.
- Patch releases should be bug fixes, security fixes, docs fixes, or
  compatibility-preserving behavior clarifications.
- Minor releases may change API details before 1.0, but breaking changes must
  be called out in release notes with migration guidance.
- After `v1.0.0`, public HTTP, CLI, and SDK compatibility should follow SemVer.

## Stable V1 Surfaces

These surfaces should not break casually:

- HTTP routes under `/v1` documented by `/openapi.json`.
- Run response envelope: top-level `run_id`, `run`, optional `metadata`,
  optional `links`, optional `duplicate`.
- Machine-readable error shape: `code`, `message`, optional `details`.
- Idempotent mutation behavior using `Idempotency-Key`.
- CLI JSON output for run create/list/get/wait/events/decision/refresh/cancel
  and sink status/deliver/replay.
- SDK happy paths:
  - `run_image` / `runImage`
  - `create_run` / `createRun`
  - `wait_run_until_terminal` / `waitRunUntilTerminal`

## Explicitly Experimental

These may change during pre-1.0 work:

- Deeply nested adapter-specific fields on offers, placement candidates, and
  event payload `data`.
- Console layout, route structure, and visual components.
- Internal Go packages under `internal/`.
- Provider-specific configuration keys that are not documented in
  `docs/production/`.
- Package names for unreleased SDKs.

## Breaking Change Rules

A PR with a breaking public change must include:

- A test that proves the new behavior.
- Documentation updates in the root README, relevant SDK README, OpenAPI text,
  or production docs.
- A migration note in release notes or `docs/project/release-process.md`.
- A reason why compatibility could not be preserved with a new optional field,
  route, flag, or SDK method.

## Deprecation Preference

Prefer adding a new field/route/method and documenting old behavior as
deprecated before removing it. For pre-1.0 security or correctness issues,
maintainers may remove unsafe behavior immediately, but the release notes should
name the risk and the migration path.
