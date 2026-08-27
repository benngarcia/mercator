# Mercator V1 Production Evaluation Guide

Mercator V1 is ready for human evaluation and production-hardening work, not
unqualified GA operation. Treat this directory as the operator entry point for
running, testing, and auditing the current single-process OCI run broker.

Mercator runs as one Go process with an embedded SQLite event log, REST/OpenAPI
API, JSON-first CLI, embedded UI, a Docker host adapter (with RunPod and
Shadeform added via connections), env-based workload
configuration and sink cursor/replay support. SQLite events are the source
of truth; derived read models and sink cursors can be
rebuilt from the event log.

## Start Here

1. Install and configure the process: [install-configuration.md](install-configuration.md).
2. Lock down API access: [authentication.md](authentication.md).
3. Exercise the local Docker adapter: [docker-adapter-operation.md](docker-adapter-operation.md).
4. Launch a bounded provider trial: [provider-conformance.md](provider-conformance.md).
5. Evaluate workload semantics: [workload-run-lifecycle.md](workload-run-lifecycle.md).
6. Check the production-hardening gaps before relying on it:
   [known-limitations.md](known-limitations.md).

## Operator Runbooks

- [install-configuration.md](install-configuration.md): build, server start,
  environment variables, health checks, OpenAPI, and UI.
- [authentication.md](authentication.md): authenticated operator and workload
  identities for one deployment execution scope.
- [docker-adapter-operation.md](docker-adapter-operation.md): Docker host adapter
  setup, labels, lifecycle, and cleanup checks.
- [provider-conformance.md](provider-conformance.md): real provider launch,
  budget gate, signed probe evidence, and cleanup verification.
- [../reference/openapi.md](../reference/openapi.md): OpenAPI discovery,
  route families, auth boundary, and first HTTP integration path.
- [workload-run-lifecycle.md](workload-run-lifecycle.md): workload JSON, run
  create/list/get/wait/events/decision/refresh/cancel commands.
- [environment-configuration.md](environment-configuration.md): workload env,
  create-run overrides, and the no-secret-vault boundary.
- [sinks-replay.md](sinks-replay.md): sink status, delivery, replay, and cursor
  behavior.
- [backup-recovery.md](backup-recovery.md): SQLite WAL backups and restore
  checks.
- [observability-audit.md](observability-audit.md): health, event audit,
  decision audit, sink cursors, and logs.
- [security-model.md](security-model.md): trust boundaries and verified
  non-observability claims.
- [node-agent.md](node-agent.md): what credentials a node agent holds, how long
  each is good for, how a session is renewed, and what to do about a machine that
  is locked out.
- [human-eval-checklist.md](human-eval-checklist.md): concrete acceptance
  checklist for V1 evaluation.
- [known-limitations.md](known-limitations.md): residual risks and GA docs gaps.

## Baseline Command Set

```sh
go build ./...
go test ./...

export MERCATOR_ADDR=127.0.0.1:8080
export MERCATOR_SQLITE_DSN='file:/tmp/mercator.db'
export MERCATOR_API_TOKEN="$(openssl rand -hex 32)"
export MERCATOR_SECRET_KEY="$(openssl rand -hex 32)"

go run ./cmd/mercator serve
```

In another shell:

```sh
export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_API_TOKEN='<same token>'

curl -fsS "$MERCATOR_API_URL/health/live"
curl -fsS "$MERCATOR_API_URL/openapi.json" >/tmp/mercator-openapi.json
go run ./cmd/mercator run list
```

## Production-Hardening Stance

Use the current branch to answer whether the V1 broker model is usable,
auditable, and operationally understandable. Do not use it yet as evidence that
Mercator has production-grade key management, registry-backed tag resolution,
external Kafka/Postgres sink configuration, or a complete multi-tenant
authorization system.
