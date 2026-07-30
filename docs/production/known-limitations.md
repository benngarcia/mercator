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
- A registry credential minted for one pull is the operator's standing account
  verbatim ([#238](https://github.com/benngarcia/mercator/issues/238)).
  `credential.Mint.RegistryPull` wraps the account from the operator's
  `config.json` in a scope naming one operation, one workspace, one digest and an
  expiry no longer than an hour, and that scope is enforced by Mercator's own
  agent on the machine. A password registry sees none of it: it sees a username
  and a password valid for everything that account can read, for as long as the
  account exists. An attacker who takes a rented host takes the whole account.
  Narrowing it needs a per-registry token exchange, which nothing in the tree
  performs. Contrast an Artifact read, where the scope is real on the far side
  too, because a presigned GET is a signature over one object path and one
  expiry. Operators renting machines from a provider whose physical security they
  do not control should give Mercator a registry account scoped to only the
  images it needs to pull.

## Capacity Reuse

- Shadeform is the only provider backend in the reusable lane. It rents a VM and
  hands it a script that installs and starts the pinned node agent, so the
  machine enrols itself and outlives the workloads run on it. Docker, RunPod and
  Vast each still create capacity for one workload and destroy it afterwards, so
  no machine those three allocate survives a Run.
- A Shadeform connection publishes no placement candidate, so no Run can be
  placed on it in production yet. A capacity connection is not asked for offers
  ([#200](https://github.com/benngarcia/mercator/issues/200)) and a launch is
  still addressed through the selected offer's native ref rather than the machine
  a provisioning built ([#207](https://github.com/benngarcia/mercator/issues/207)).
  Renting works; nothing production-side asks for a machine yet.
- Shadeform has had no live run since it became a capacity provider. The path is
  proven under its package's httptest fake only
  ([#235](https://github.com/benngarcia/mercator/issues/235)).
- A Shadeform connection needs an `agent_download_url` an operator hosts, because
  Mercator's release archives ship no `mercator-node` binary
  ([#234](https://github.com/benngarcia/mercator/issues/234)), and the control
  plane needs `MERCATOR_AGENT_VERSION` to say which build that URL serves. Neither
  has a default: a guessed URL is a paid machine fetching a 404, and a guessed
  version is a pin nobody chose. A connection or a deployment missing either
  verifies and lists capacity, and refuses to provision.
- A provision the provider classified as fatal, such as an authentication failure
  after a key rotation, is asked again on every advance for ever
  ([#236](https://github.com/benngarcia/mercator/issues/236)). The capacity build
  records no classified failure, and the enrolment deadline cannot bound it because
  it is only consulted once a provision has succeeded. Out of reach today, because
  a capacity connection publishes no placement candidate yet.
- A repeated provision against a provider honouring no idempotency key is
  resolved only by the adapter, and nothing in the Lab holds that
  ([#237](https://github.com/benngarcia/mercator/issues/237)). Shadeform creates
  the instance, then scans every instance wearing the lease's tag and destroys
  the losers, so a create whose answer was lost and then repeated really does
  rent a second billed machine carrying a second copy of the same single-use
  invitation, and a failed delete leaves both up. The Lab's provider answers
  every repeat under a lease with the machine that lease already has, so neither
  `capacityAlreadyHeld` nor the clause about one invitation reaching two machines
  is exercised by any world.
- A provisioned Rental is never handed back
  ([#206](https://github.com/benngarcia/mercator/issues/206)). Nothing ends the
  lease of a machine nobody is using, so a machine that is rented, bootstrapped
  and enrolled goes on being billed until an operator destroys it out of band.
  The pieces exist (`Leases.EndGeneration` retires the runtime bound to the
  generation, and `TerminateCapacity` gives the machine back), and nothing calls
  them on an idle machine. Out of reach for a Run today only because a capacity
  connection publishes no placement candidate; it is reachable right now through
  the capacity seam, and `mercator verify --mode capacity` is the one path that
  reliably gives a machine back, because the trial sweeps its own workspace.
- The capacity conformance suite cannot see a second machine hidden behind a
  lease that already holds one
  ([#239](https://github.com/benngarcia/mercator/issues/239)). When a lease
  already knows a machine and a later provision comes back indeterminate, the
  repeat that would surface a duplicate is skipped, because on a conforming
  provider that repeat answers with the machine that already exists and reporting
  it would cry wolf. Nothing in the contract distinguishes the two, so a machine
  really allocated by a lost answer can be billed and never named. Unreachable
  today because nothing in the tree puts two machines under one lease, and it
  becomes reachable the moment a lease grows a second generation.
- An ephemeral execution still commits a Booking against a single-use Rental
  identity. Placement makes that binding unqueueable and records the honest
  `launch_ephemeral` disposition, but the Booking record type is shared with
  reusable placements, so a reader of the schema alone cannot tell them apart.
- Reuse works end to end only on nodes an operator enrolled by hand. A machine
  Shadeform rents is bootstrapped with an agent, but the two issues above stop a
  Run from being placed on the result, so a provisioned machine is reachable
  today only through the capacity seam rather than through a Run.
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
carry. Restated at the phase 5 close-out on 2026-07-29, from the same amd64 Linux
workstation, now running Docker Engine 29.6.2 on Ubuntu 26.04 with an RTX 5090.
The phase 3 and phase 4 entries below were re-checked on that host.

Three of them are narrower than they were, and one is wrong as written:

- The accelerator probe this phase added ran against a real GPU through a real
  container, rather than against a recorded fixture, so the host-facts work is
  not in the unproven column at all.
- Playwright's Chromium does run here. Both browser-driven console cases execute
  against real headless Chromium on this host, which is how two races in the
  runs-navigation script were found and fixed at the phase 5 close-out. Issue
  [#197](https://github.com/benngarcia/mercator/issues/197) says the console can
  only be verified in CI, and that is true of the machine it was written on
  rather than of every workstation. The entry below is kept because the claim it
  makes about CI finding console defects still holds, and because nothing here
  guarantees another workstation can run the browser.
- Seven of the ten cases that skip under a bare `go test ./...` skip behind an
  environment flag rather than behind a missing capability, and all seven pass
  here when asked. A reader counting skips should ask which kind each one is
  before concluding anything about coverage.

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
- Shadeform sells capacity rather than one-shot execution, so the trial that
  applies to it is `mode: "capacity"`, which rents machines and gives them back.
  The bounded suite behind it runs on every build against the simulated provider
  and against Shadeform's own marketplace served over `httptest`, and no machine
  has ever been rented for real. The live run is
  [#235](https://github.com/benngarcia/mercator/issues/235); the command is in
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
- Provisioned reusable capacity has no live coverage at all. A provider does
  bootstrap a node agent now, and no machine has ever been rented to carry one:
  the bootstrap script is proven by running it under a real shell in a container
  on this host, and the provider half is proven against Shadeform's API served
  over `httptest`. Everything about a node that a real daemon proves here was
  proven on this workstation's own daemon. What only a rented machine can
  establish is whether a provider's image carries `systemd`, `curl` and a working
  Docker daemon, and whether the script runs as root once the instance is active
  ([#235](https://github.com/benngarcia/mercator/issues/235)).
- The whole of phase 5 was developed on a branch with no upstream, so nothing it
  built met CI until the close-out. Two defects survived every local slice and
  every adversarial review for exactly that reason: a hand-edited generated file
  that `go generate` plus `git diff --exit-code` catches immediately, and a
  browser case that skips unless `MERCATOR_BROWSER_TEST` asks for it. Work kept
  off CI for a long stretch should assume the same two classes are hiding in it.

## GA Documentation Gaps

- Deployment topology with TLS/reverse proxy.
- Key-management and rotation procedure.
- Registry digest-resolution procedure beyond pre-pinned workload images.
- External sink configuration and incident runbooks.
- SQLite migration, backup automation, and restore SLOs.
- Release/version compatibility and rollback procedure.
