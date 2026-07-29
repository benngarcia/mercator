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
- Mercator terminates TLS itself and manages no certificates. It reads
  `MERCATOR_TLS_CERT_FILE` and `MERCATOR_TLS_KEY_FILE` once, at startup, so a
  renewed certificate is served only after a restart. There is no ACME client,
  no automatic issuance, no OCSP stapling configuration, no client-certificate
  (mTLS) mode, no HSTS header, and no plain-HTTP listener that redirects to
  HTTPS: a deployment that wants port 80 to redirect needs something else on
  port 80. An operator renewing on a short-lived certificate should expect a
  process restart per renewal
  ([#213](https://github.com/benngarcia/mercator/issues/213)).
- The non-loopback TLS rule is `mercator serve`'s alone. It is enforced in the
  process entrypoint against `MERCATOR_ADDR`, and `mercator verify` builds the
  same server on a listener of its own: a remote provider trial binds the fixed
  routable `MERCATOR_CONFORMANCE_LISTEN_ADDR` it requires, reads no TLS
  variable, and serves the full `/v1` API and its generated operator token in
  cleartext until the trial ends. The documented topology is a TLS terminator in
  front of it. Moving the rule into the server so it covers both callers needs
  the trial to be able to carry its own certificate
  ([#216](https://github.com/benngarcia/mercator/issues/216)).
- A workload's run-report token is minted once at dispatch and never expires, so
  a master-key rotation performed while a run is executing answers every later
  report from that container `401 INVALID_RUN_TOKEN` for the rest of the run,
  including its terminal verdict. Let in-flight runs finish before rotating;
  there is no drain command
  ([#215](https://github.com/benngarcia/mercator/issues/215)).
- The non-loopback TLS rule reads the bind address and nothing else, which makes
  it conservative in one real case: a container that binds `0.0.0.0` so that a
  loopback-only published port can reach it is required to carry a certificate
  even though nothing off the host can reach the listener. The alternative,
  inferring exposure from anything other than the address this process binds,
  would be guessing.
- No Mercator-managed secret vault, grant API, or KMS integration exists.
  Workloads and runtimes own their secret-management backend. Master-key
  rotation does exist: `mercator rekey` re-seals every stored connection
  credential from `MERCATOR_SECRET_KEY_PREVIOUS` to `MERCATOR_SECRET_KEY` in one
  transaction. It is offline. There is no online rotation, no rotation of a
  session-cookie key, and no scheduled or automatic rotation, so the retired key
  is in the environment for as long as the operator leaves it there.
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
  terminates anything
  ([#44](https://github.com/benngarcia/mercator/issues/44)).
- The local Docker adapter publishes `RatePerSecondUSD: 0` with the price marked
  known, which is the one production publisher of free capacity left. It makes a
  local machine unconditionally the cheapest candidate. Both honest repairs, a
  configured shadow price on the connection or an unpriced offer a Run must opt
  into, change where every local Run lands
  ([#188](https://github.com/benngarcia/mercator/issues/188)).
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
- The Workspace canvas draws at most two days ahead, 289 columns. A Booking whose
  projected start is further out than that is listed in its machine's queue and
  has no block on the timeline. The bound exists because the axis is built from
  the difference between a projected start and the moment the workspace last said
  something: asked for a start years away it built 723,040 elements and held the
  browser's main thread for seventy seconds, so an unbounded axis is a hazard
  whatever produced the timestamp.
- The Lab can record a different Booking Decision for one Blueprint depending on
  how a caller drove the execution: successive advances and a drive to completion
  disagree about whether a consumer's candidate was queued or free
  ([#182](https://github.com/benngarcia/mercator/issues/182)). Nothing in
  production reads a Run Bundle, so this costs an operator nothing today. It costs
  the executable specification its central promise, and it means a claim proven
  under one drive shape is not established under the other.

## What A Developer Workstation Cannot Prove

These are gaps in the evidence rather than in the product, recorded so a reader
knows which promises rest on CI or on a credential this repository does not
carry. Stated as of the phase 4 close-out on 2026-07-27, from an amd64 Linux
workstation running Docker Engine 29.6.2. The phase 3 entries below were restated
on that host and still hold.

- The prediction key's recurrence is unproven against any live marketplace, which
  is phase 4's largest untested assumption
  ([#184](https://github.com/benngarcia/mercator/issues/184)). The hierarchical
  estimator's narrowest rung keys on a provider's own identifier for a machine
  shape, and nothing here has ever seen a provider list the same shape twice.
  Docker-backed conformance is not blocked on this host and was used instead
  wherever a container could stand in, including a real MinIO object store and a
  real `registry:2`, but a marketplace read needs an account rather than a
  container.

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
- The browser-driven console checkpoints cannot run on this workstation at all.
  Playwright's Chromium needs system libraries that are not installed and there
  is no sudo to add them, so the console half of the Lab acceptance flow is
  proven by CI and never locally. That gap is not theoretical: four defects in
  the phase 4 close-out were found only when CI ran that flow, including one
  where the console rendered nothing at all because the offer catalog frame
  failed to decode. Two of them now have non-browser regression tests
  (`feed.contract.test.ts` decodes the captured feed,
  `TestVerticalProofHoldsInTheOrderTheConsoleDrivesIt` drives the console's
  order without a browser), so the same class of break fails locally next time.
  Anyone changing an event payload, a schema, or a placement weight should
  assume the console is affected and cannot confirm it here.
- Provisioned reusable capacity has no live coverage at all, because no provider
  bootstraps a node agent yet. Everything about a node that a real daemon proves
  here was proven on this workstation's own daemon.

## GA Documentation Gaps

- Deployment topology with a reverse proxy in front of a loopback bind.
- Key-management procedure beyond the rotation command
  ([security-model.md](security-model.md#master-key-and-rotation)): where the
  master key lives, who can read it, and how a leak is detected.
- Registry digest-resolution procedure beyond pre-pinned workload images.
- External sink configuration and incident runbooks.
- SQLite migration, backup automation, and restore SLOs.
- Release/version compatibility and rollback procedure.
