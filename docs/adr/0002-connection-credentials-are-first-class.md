# ADR 0002: Connection Credentials Are First-Class

Status: Accepted
Date: 2026-06-23

## Context

ADR 0001 removed a Mercator-owned secret vault. That decision is about
*workload* secrets — material the user's container needs (e.g. S3 credentials),
which the workload fetches from its own backend.

Connection/adapter credentials are different: they are the credentials Mercator
*itself* needs to call a provider's control plane (e.g. a RunPod API key).
Mercator cannot broker without them. The blanket "no secrets" reading of ADR
0001 does not fit this case.

## Decision

ADR 0001 governs *workload* secrets only. *Connection* credentials are
first-class and handled by a `credential_source` seam:

- `env` — the connection references an env var holding the secret.
- `mercator` — the secret is stored encrypted (AES-256-GCM under a process
  master key `MERCATOR_SECRET_KEY`) in a dedicated table, addressed by
  connection id. The append-only, sink-streamed event log stores only the
  reference `{source, ref}`, never the secret.
- `vault` / external sources may be added later behind the same seam.

Mercator does not store *workload* secrets, KMS, rotation policies, or secret
versions. The connection secret store is intentionally small: low cardinality,
infrastructure-only, never materialized into arbitrary containers.

## Consequences

Connections can be added and authorized from the API/UI without an operator env
change (the `mercator` source). The event log remains secret-free. One process
master key is required to enable the `mercator` source.
