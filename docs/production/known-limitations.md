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

- An ephemeral execution still commits a Booking against a single-use Rental
  identity. Placement makes that binding unqueueable and records the honest
  `launch_ephemeral` disposition, but the Booking record type is shared with
  reusable placements, so a reader of the schema alone cannot tell them apart.
- Reuse works only on nodes an operator enrolled by hand. Provisioned capacity
  arrives with no agent on it, so renting a machine still produces one-shot
  execution. Bootstrapping the agent through a provider is
  [#155](https://github.com/benngarcia/mercator/issues/155) phase 5.
- Enrolling a node is a manual two-step: `POST /v1/nodes` for the bootstrap,
  then run `mercator-node` with it. There is no CLI command and no quickstart
  step.
- A node runs one workload at a time. A second Run arriving while a node is busy
  queues behind its Booking rather than provisioning elsewhere, which is the
  Rental Schedule working, but the node still executes them one after another.
- Nothing enforces `max_runtime_seconds`. The node agent passes it to Docker as
  `--stop-timeout`, which governs how long a stop waits before killing, not how
  long a container may run. A workload that never exits runs until something
  else stops it, and it holds its machine's Rental Schedule slot while it does.
  Every overrun rule in the scheduler exists for that world and none of them
  terminates anything.
- The local Docker adapter publishes `RatePerSecondUSD: 0` with the price marked
  known, which is the one production publisher of free capacity left. It makes a
  local machine unconditionally the cheapest candidate. Both honest repairs, a
  configured shadow price on the connection or an unpriced offer a Run must opt
  into, change where every local Run lands.
- A Run placed on a node that then goes quiet stays open indefinitely. The node
  stops being offered for new work, but the Run already on it is never
  adjudicated: nothing re-places it and nothing fails it. Adjudicating a lost
  node's Bookings needs a declared grace window and restart policy, which is
  replanning work ([#163](https://github.com/benngarcia/mercator/issues/163)).

## Prediction And Scheduling

- The prediction key has never been tested against a live marketplace. The whole
  hierarchical estimator rests on a provider's own identifier for a machine shape
  recurring across listings, and that claim is held by unit cases against recorded
  Vast and Shadeform response shapes and by the Lab, not by a real account. This
  host holds no Vast, Shadeform, or RunPod credential. If a provider mints a fresh
  identifier per listing, every candidate falls to the region rung or the global
  prior and the exact-candidate level is dead weight. Check the recorded fallback
  level on real decisions before trusting a p50.
- Nothing production-side has ever populated launch history for the three
  transfer stages, and by design nothing will: `image_fetch`, `unpack`, and
  `artifact_fetch` are priced from missing bytes over a measured path rather than
  answered from history, because a duration keyed on the candidate cannot know
  what bytes that candidate is missing. A transfer answered from history is a
  Lab invariant violation. The consequence for operators is that transfer
  estimates are only as good as the published path throughput, and an unmeasured
  path falls back to a stated assumption.
- Predicted-versus-actual is recorded but nothing calibrates on it. No feedback
  loop adjusts a prior that is consistently wrong, so a systematically optimistic
  global prior stays optimistic until somebody reads the Run Bundles. Calibration
  is phase 6.
- Soft and hard affinity are not implemented. The phase scoped a thin dependency
  model and delivered artifact dependencies, blocked-until-ready, group
  parallelism, queueing, interruptibility, deadline, and service class. Affinity
  has no field, no scheduler term, and no scenario, so a Run cannot ask to land
  near or away from anything.
- Three capacity-economics terms are deliberately unpriced, so a Booking
  Decision's cost is an underestimate in known directions. Stopped-state storage
  is not charged, so a machine kept stopped looks free. Preemption risk is not
  priced into the score; it is expressed only as a hard refusal when a class
  forbids interruption on capacity the provider may reclaim. Warm-capacity
  opportunity cost is folded into the shadow price rather than modelled
  separately.
- The idle tail is charged whole to the placement that forced Mercator to buy the
  billing increment, and a later Run that uses part of that remainder is charged
  nothing. A short Run that triggers a purchase therefore looks expensive and its
  successors look cheap. The error is deliberate and in the safe direction.
- The availability window is decided once, at placement, against the runtime
  Mercator enforces. A Booking queued behind another one is projected from where
  that predecessor sits at that moment, so a predecessor that overruns can push a
  queued Booking's end past a window that was clear when it was admitted. Nothing
  reconciles that afterwards.
- A Run that no machine in the fleet can ever hold stays queued until its
  deadline refuses it, rather than being refused when it exceeds its class's
  maximum queue delay. What admission should do at that bound is an unmade
  refusal-policy decision.
- The operator API silently drops an unknown `objective` field. A caller who kept
  sending the retired `fastest_start` after the ServiceClass migration is scored
  as `standard` with nothing in the record saying their request was reinterpreted.
  The Blueprint loader refuses the retired vocabulary by name; the HTTP door does
  not.
- Two rules of the orphan-adoption policy are held only by package tests, not by
  the scenario corpus: that the janitor records its decision before acting on it,
  and that it releases the slot of capacity whose provider cannot destroy it. A
  refactor that undoes either leaves the whole corpus green.

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
