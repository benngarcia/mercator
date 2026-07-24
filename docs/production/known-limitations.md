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

- One bearer token principal plus audited OIDC identities, with no roles or
  per-user workspace authorization.
- No built-in TLS.
- No Mercator-managed secret vault, grant API, KMS integration, or key rotation
  flow exists. Workloads/runtimes own their secret-management backend.
- Health, OpenAPI, and UI shell are public on the listen interface.

## Capacity Reuse

- Every current backend is in the ephemeral lane. Docker, RunPod, Shadeform, and
  Vast each create capacity for one workload and destroy it afterwards, so no
  machine survives a Run and nothing is warm for the next one. Reusable capacity
  needs the Mercator node runtime, which is not implemented
  ([#155](https://github.com/benngarcia/mercator/issues/155) phase 2).
- An ephemeral execution still commits a Booking against a single-use Rental
  identity. Placement makes that binding unqueueable and records the honest
  `launch_ephemeral` disposition, but the Booking record type is shared with
  reusable placements, so a reader of the schema alone cannot tell them apart.
- Rental Schedules exist in the domain model and the console, but nothing
  populates them across Runs yet: queueing behind a running Booking is a target
  scenario, not shipped behavior.

## Adapters And Workloads

- Docker adapter is local-host oriented and intentionally narrow.
- Docker receives literal env bindings only.
- Tag resolution reads the broker host's Docker daemon, so a tag must name an
  image that host already holds. Registry-backed resolution, which would let a
  tag resolve without a local pull, is not implemented.
- Resolution uses the broker host's Docker endpoint even when the run lands on
  a remote Docker connection, so a remote host holding a different image set
  can disagree with the recorded digest.
- Docker supports one static registry credential per connection. Token
  exchange, multiple registries on one connection, and automatic rotation are
  outside the current contract.
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
- Deeper connection, offer, and sink management workflows are not built into the
  UI.

## GA Documentation Gaps

- Deployment topology with TLS/reverse proxy.
- Key-management and rotation procedure.
- Registry digest-resolution procedure beyond pre-pinned workload images.
- External sink configuration and incident runbooks.
- SQLite migration, backup automation, and restore SLOs.
- Release/version compatibility and rollback procedure.
