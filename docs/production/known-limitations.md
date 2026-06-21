# Known Limitations

The current V1 branch is suitable for human evaluation and production-hardening
work. It should not be described as production GA without addressing these
limits.

## Runtime And Deployment

- Single-process only; no multi-process leader election, failover, or replicated
  event log.
- SQLite backup/restore is manual.
- No schema migration runbook exists yet.
- Health checks are shallow process/API checks.

## Security

- One bearer token principal with workspace allow-list, not per-user auth.
- No built-in TLS.
- `MERCATOR_SECRET_KEY_HEX` is raw process configuration; no KMS or key rotation
  flow is implemented.
- Health, OpenAPI, and UI shell are public on the listen interface.

## Adapters And Workloads

- Docker adapter is local-host oriented and intentionally narrow.
- Docker secret environment materialization is not configured.
- Workloads must be digest-pinned before run creation.
- Registry-backed tag resolution is not implemented; current resolver is
  deterministic/static.
- No mounts, workdir, setup commands, stdin, TTY, host networking, sidecars, or
  arbitrary Docker flags.

## Sinks And Integrations

- Executable server wires only the `audit` discard sink.
- Webhook, Kafka, and Postgres sink implementations are interface-backed code
  boundaries but not production-configurable through `cmd/mercator`.
- External sink authentication, retries, dead-letter handling, and deployment
  topology need future docs after wiring exists.

## UI And Operator Workflows

- Embedded UI is compact and read-oriented.
- Deeper connection, offer, secret, and sink management workflows are not built
  into the UI.

## GA Documentation Gaps

- Deployment topology with TLS/reverse proxy.
- Key-management and rotation procedure.
- Registry credential and digest-resolution procedure.
- External sink configuration and incident runbooks.
- SQLite migration, backup automation, and restore SLOs.
- Release/version compatibility and rollback procedure.
