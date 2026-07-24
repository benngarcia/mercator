# Mercator Roadmap

Mercator is a compute broker and fleet manager: push a container workload and
it starts fast on the warmest machine in your fleet, rents new GPU capacity
when none fits, and records what it decided and why. This roadmap says what is
shipped, what is being built now, and what comes later. Operational gaps live
in [docs/production/known-limitations.md](docs/production/known-limitations.md).

## Now: shipped

- Event-sourced run lifecycle over SQLite, in one Go process with a JSON HTTP
  API, a CLI, and an embedded operator console.
- Placement with recorded booking decisions: the winning offer, every rejected
  candidate, and reason codes, queryable per run.
- One-command push-to-run: `mercator run create <image> -- <cmd>` resolves the
  digest and platform from the image and defaults workspace, connection, and
  run ids by uniqueness.
- Docker host adapter for real local launches; RunPod, Shadeform, and Vast.ai
  adapters behind the same run contract, each with a runbook and a bounded
  conformance trial that produces a sanitized evidence bundle.
- Rentals with persisted schedules: placement can queue a booking behind the
  run a rental is executing, weighing queued expected runtime against the
  cost and latency of provisioning fresh capacity.
- The Workspace canvas as the console home: each rental with its running and
  queued bookings, streamed live over SSE.
- A placement scenario corpus (`internal/scenario`) that states the decisions
  placement must make. Scenarios marked `target` describe behavior that is not
  built yet and fail CI the moment they start passing, so the corpus always
  says exactly where the program stands.

## Building: an auditable capacity broker

Mercator is migrating to a control plane that decides whether to reuse, queue
on, resume, or provision capacity by predicting candidate-specific
time-to-ready, cost, locality, and risk, and records a replayable reason for
every decision. The tracking issue is
[#155](https://github.com/benngarcia/mercator/issues/155) and the living plan is
[the migration plan](docs/project/capacity-broker-migration.md).

The first slice shipped: capacity allocation and workload execution are now
separate contracts. A backend lands in the reusable lane only by implementing
both a capacity provider and a node runtime, so nothing can advertise reuse it
cannot perform. Every current backend is ephemeral, which is what each of them
actually does today.

What comes next, in order: the node protocol and Go agent so one machine runs
successive workloads; exact image and artifact locality; candidate-specific
prediction with service classes and owned-capacity economics; one provider
bootstrapping the agent end to end; then the launch waterfall, calibration, and
placement explanation UI.

## Building: warm placement

The goal of the current program: the next run starts faster because the fleet
is warm.

- Schedule advancement: dispatch the next queued booking the moment the
  running one reaches a terminal state, re-evaluating only the tail of the
  schedule.
- Image warmth: score rentals by the Docker layers they already hold, so
  workloads with similar images pack onto the same machine and pull less.
- Cache mounts: workload-declared named mounts whose contents persist on a
  rental across runs. Placement prefers machines whose caches are already
  populated; what is inside the cache stays the application's business.
- Host facts: rentals report the layers and caches they hold, so warmth
  scoring works from evidence instead of assumptions.

## Later

- Registry-backed tag resolution and credential handling
  ([#125](https://github.com/benngarcia/mercator/issues/125)), so a tag
  resolves without a local pull and remote Docker connections work.
- Refreshed launch collateral: the material in `docs/launch/` predates the
  repositioning and needs a rewrite before any public launch push.
- Package-manager distribution for the CLI and server.
- Additional provider adapters behind the same auditable run contract.
- External sink hardening for Kafka/Postgres delivery beyond the current
  audit sink boundary.
- Multi-node operation: failover, TLS, per-user authorization.

## Non-Goals

- Replacing Kubernetes, Slurm, or a managed batch platform.
- Building or syncing your code into images: Mercator takes an OCI image.
- Becoming a secret manager for workload-owned secrets.
- Hiding provider-specific constraints behind opaque placement.
- Optimizing for multi-tenant SaaS operation before the single-process
  operator model is trustworthy.
