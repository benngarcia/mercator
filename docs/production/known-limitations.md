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

- Every provider backend is in the ephemeral lane. Docker, RunPod, Shadeform,
  and Vast each create capacity for one workload and destroy it afterwards, so
  no machine those backends allocate survives a Run and nothing is warm for the
  next one. The reusable lane is reached only through the Mercator node runtime,
  which today means a Docker host an operator enrolled by hand.
- An ephemeral execution still commits a Booking against a single-use Rental
  identity. Placement makes that binding unqueueable and records the honest
  `launch_ephemeral` disposition, but the Booking record type is shared with
  reusable placements, so a reader of the schema alone cannot tell them apart.
- Rental Schedules exist in the domain model and the console, but nothing
  populates them across Runs yet: queueing behind a running Booking is a target
  scenario, not shipped behavior.
- Reuse works only on nodes an operator enrolled by hand. Provisioned capacity
  arrives with no agent on it, so renting a machine still produces one-shot
  execution. Bootstrapping the agent through a provider is
  [#155](https://github.com/benngarcia/mercator/issues/155) phase 5.
- Enrolling a node is a manual two-step: `POST /v1/nodes` for the bootstrap,
  then run `mercator-node` with it. There is no CLI command and no quickstart
  step.
- A node's price is the shadow price configured at invitation, and nothing else.
  Committed billing intervals, idle-tail expectation, and warm-capacity
  opportunity cost are not modelled, so a node's cost in a Booking Decision is
  a flat rate rather than an economic estimate.
- A node runs one workload at a time and Rental Schedules are not populated, so
  a second Run arriving while a node is busy provisions elsewhere instead of
  queueing behind it.
- Image locality is exact only where a node reports content. Mercator resolves a
  digest-pinned image's manifest from the registry and subtracts what an enrolled
  node says it holds, so an enrolled node's candidacy is priced on real content.
  A Docker offer from the ephemeral lane reports a silent inventory, so every
  such candidate is priced a full pull at assumed link speed and they stay
  indistinguishable from each other on locality.
- Manifest resolution uses one static credential per registry connection
  ([#125](https://github.com/benngarcia/mercator/issues/125)). An image whose
  registry answers nothing, throttles, or refuses the credential is recorded
  unreadable with the reason, and every candidate is then priced identically,
  which understates absolute start latency without disturbing the comparison.
- A Run placed on a node that then goes quiet stays open indefinitely. The node
  stops being offered for new work, but the Run already on it is never
  adjudicated: nothing re-places it and nothing fails it. Adjudicating a lost
  node's Bookings needs a declared grace window and restart policy, which is
  replanning work ([#163](https://github.com/benngarcia/mercator/issues/163)).

## Locality, Artifacts, And Caches

- No production deployment can run a workload that declares an Artifact input.
  `cmd/mercator` builds the orchestrator with no `ArtifactCatalog`, because no
  production object-store client exists, so a Run that reads an Artifact is
  refused at intake with `ARTIFACT_CATALOG_UNAVAILABLE`. Everything Mercator
  knows about Artifact durability, Artifact locality, and the read a candidate
  still owes is therefore exercised in the Lab and against a MinIO container in
  conformance, and reaches no operator until that client lands.
- A verified Artifact copy on a node's disk is not readable from inside the
  container a Run executes in
  ([#171](https://github.com/benngarcia/mercator/issues/171)). Nothing attaches
  a replica to a launch and nothing tells a workload which of its inputs are
  local, so the zero seconds Placement prices a host holding a checked copy is a
  statement about the decision rather than a saving the workload collects. An
  operator reading a Booking Decision should read artifact locality as the
  reason a host was chosen and not as time the Run will save.
- A workload's own output leaves the machine no copy. A Run writes its output
  inside its own container, and nothing hashes or files those bytes, so the
  producing host reports no replica of what it just wrote and the next Run reads
  the object store even when it lands on that same machine.
- A refused preparation is terminal for that content on that node. The node
  operation store dedupes on operation identity with no regard for state, so a
  node whose prefetch failed answers `Duplicate` for that image or Artifact from
  then on, and the desired set is never restated either. The Run still runs,
  fetching at launch, and the prefetch never retries. Clearing it needs operator
  intervention in the node's operation store.
- A withdrawn prefetch keeps running on the node
  ([#170](https://github.com/benngarcia/mercator/issues/170)). The node protocol
  has one command per piece of content and no way to say stop, so cancelling a
  queued Run stops Mercator asking and leaves the transfer already in flight
  holding that node's link and disk until it completes.
- Preparation for a Run whose machine is momentarily stale waits for the sweep.
  A node offer is selectable for a third of the node lease, so a machine that has
  not reported inside that window is one Mercator states no desire for, and the
  Booking that arrived during it was the only wake-up that Run gets. The Run still
  runs and still fetches at launch. What it loses is the head start, by up to the
  reconcile cadence, which is a minute. Operators who care about that should keep
  the node heartbeat comfortably inside a third of the lease.
- A control-plane restart forgets which content it has already asked for. The
  moment preparation last began is durable, and the desired sets are in process,
  so a restarted Mercator restates a desire it may already have sent and delays
  a withdrawal it discovers by up to `PrewarmPolicy.MinInterval`.
- A node's disk report states free bytes and never total. An operator can tell a
  full machine from an unmeasurable one through `disk_report` and
  `disk_free_bytes` on `GET /v1/nodes`, and cannot see capacity or a utilisation
  ratio.
- A Run that finds no feasible offer records no Booking Decision, so a refused
  placement leaves no rejection an operator can read. The refusal is visible on
  the Run and in the daemon's answer alone.
- Cache Mounts are isolated per workspace and per generation, and nothing prices
  them. A warm cache is recorded on a candidate and never scored, so two
  otherwise equal machines are chosen between on cost and start latency even
  when one holds the cache the Run declared.
- A Run whose image is a tag is refused at intake with `IMAGE_NOT_PINNED`. Every
  answer Mercator gives about an image is a digest comparison, so a Run is
  admitted only against `repository@sha256:<64 hex>`. A deployment configured
  with an image resolver pins a submitted tag before admission and an operator
  sees nothing; a deployment with no resolver, or one whose registry is
  unreachable when the Run is created, refuses the Run and the operator must
  supply the digest. A stored workload revision may still carry a tag, because a
  revision is a template.

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

## What A Developer Workstation Cannot Prove

These are gaps in the evidence rather than in the product, recorded so a reader
knows which promises rest on CI or on a credential this repository does not
carry. Stated as of the phase 3 close-out on 2026-07-25, from an amd64 Linux
workstation running Docker Engine 29.6.2.

- The public-image half of registry conformance did not run. Docker Hub answers
  an anonymous manifest read from this address with 429, so
  `TestRegistryResolverAgreesWithDockerAboutAPublicImage` skips and the claim it
  holds, that the resolver names a multi-platform public image the way the daemon
  running it does, rests on runs from other addresses. Its authenticated half
  runs here in full against a `registry:2` container. A Docker Hub credential in
  the environment would close this.
- No provider conformance trial ran against RunPod, Shadeform, or Vast. Nothing
  in this environment carries their credentials, so the ephemeral lane's wire
  behaviour rests on the recorded fixtures in each adapter's tests and on
  `mercator verify` being run by an operator who holds a key. See
  `docs/production/provider-conformance.md`.
- The browser-driven console checkpoints skip without
  `MERCATOR_BROWSER_TEST=1` and a Playwright Chromium install, so the console
  half of the Lab acceptance flow was proven by CI rather than locally.
- Provisioned reusable capacity has no live coverage at all, because no provider
  bootstraps a node agent yet. Everything about a node that a real daemon proves
  here was proven on this workstation's own daemon.

## GA Documentation Gaps

- Deployment topology with TLS/reverse proxy.
- Key-management and rotation procedure.
- Registry digest-resolution procedure beyond pre-pinned workload images.
- External sink configuration and incident runbooks.
- SQLite migration, backup automation, and restore SLOs.
- Release/version compatibility and rollback procedure.
