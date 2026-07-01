# Security Model

This document describes the current V1 security boundary as implemented for
evaluation. It is not a GA security assurance statement.

## Trust Boundaries

- Mercator is a single trusted process.
- SQLite is the internal source of truth.
- `/v1/*` API routes are protected by a configured bearer token.
- Workspace authorization is an allow-list on that bearer principal.
- Health, OpenAPI, and the UI shell are public on the listen interface.
- Runtime adapters are trusted in-process code.
- Docker runs on the local host and should be treated as part of the trusted
  evaluation environment.

## Environment Non-Observability

Implemented protections:

- Public event APIs skip private events and expose public CloudEvents only.
- Workload env literal values are redacted from public run events. The
  redaction covers env **values** only: image references and container args are
  recorded verbatim in public events, so do not put secrets in them.
- `secret_ref` env bindings are rejected; Mercator does not own secret storage,
  grants, KMS integration, or runtime secret materialization.
- Sink delivery skips private events.

Operator checks:

```sh
go run ./cmd/mercator run events --workspace-id ws_eval --run-id run_secret_1 \
  | jq '.events'
```

## Idempotency And Side Effects

- Mutation routes require `Idempotency-Key` where implemented.
- Reusing a command key with a different request hash returns
  `IDEMPOTENCY_CONFLICT`.
- Launch and cleanup side effects are preceded by durable intent events.
- Docker launch uses deterministic container names and ownership labels.

## Current Risks

- No TLS termination is provided by Mercator; binding `MERCATOR_ADDR` to a
  non-loopback address serves plaintext HTTP (the server logs a warning). Use a
  local-only bind or a trusted TLS-terminating reverse proxy for any remote
  evaluation.
- When `MERCATOR_API_TOKEN` is unset, `serve` logs a generated token to stdout;
  operators shipping logs should set the variable explicitly.
- There is one bearer token, not per-user auth.
- Secret management is delegated to the workload/runtime. Mercator has no
  secret vault, grant API, or KMS adapter surface.
- Registry-backed image resolution and registry credential management are not
  implemented.
- External Kafka/Postgres sink auth/config is not wired through the executable.
