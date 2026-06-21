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

## Secret Non-Observability

Implemented protections:

- Secret versions are stored encrypted in private event data.
- Public event APIs skip private events and expose public CloudEvents only.
- Secret list APIs return metadata, not plaintext.
- Workload env secret references require exact secret versions and active
  run-scoped grants.
- Sink delivery skips private events.

Operator checks:

```sh
go run ./cmd/mercator run events --workspace-id ws_eval --run-id run_secret_1 \
  | jq '.events'

curl -fsS -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  "$MERCATOR_API_URL/v1/secrets?workspace_id=ws_eval" | jq .
```

## Idempotency And Side Effects

- Mutation routes require `Idempotency-Key` where implemented.
- Reusing a command key with a different request hash returns
  `IDEMPOTENCY_CONFLICT`.
- Launch and cleanup side effects are preceded by durable intent events.
- Docker launch uses deterministic container names and ownership labels.

## Current Risks

- No TLS termination is provided by Mercator; use a local-only bind or a trusted
  reverse proxy for any remote evaluation.
- There is one bearer token, not per-user auth.
- `MERCATOR_SECRET_KEY_HEX` is raw key material in process configuration; no KMS
  integration exists yet.
- Docker secret materialization is not implemented.
- Registry-backed image resolution and registry credential management are not
  implemented.
- External Kafka/Postgres sink auth/config is not wired through the executable.
