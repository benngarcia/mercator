# Migrate To A Locality-Aware Capacity Broker

This is a living execution plan. Update progress, decisions, evidence, and
unexpected findings in the same implementation-bearing pull request that changes
them. The tracking issue is
[#155](https://github.com/benngarcia/mercator/issues/155). The architecture
decision for the first slice is
[ADR 0005](../adr/0005-capacity-and-execution-are-separate-contracts.md), and
the executable specification it builds on is
[ADR 0004](../adr/0004-mercator-lab-deterministic-executable-specification.md).

## Purpose

Mercator should decide whether to reuse, queue on, resume, or provision
accelerated capacity by predicting candidate-specific time-to-ready,
time-to-completion, cost, locality, and risk, and record an explainable,
replayable reason for every decision.

Two kinds of capacity must stay impossible to confuse:

> Reusable capacity is a machine Mercator controls through an enrolled node
> runtime, which can execute successive workloads. Ephemeral capacity is a
> provider-native one-shot execution product, which holds nothing after its
> workload exits.

## Development rule

Every material capability lands in this order:

1. Add or update a target scenario.
2. Add the simulated external behavior and the invariants it must hold.
3. Implement the production behavior.
4. Make the scenario pass.
5. Promote it into the green regression corpus.
6. Add the appropriate higher-fidelity conformance test.

No scheduler, provider, node, cache, artifact, or reconciliation behavior is
complete because it works against a live provider.

## Approved decisions

- `T1`: the node agent reaches the control plane over a stdlib HTTP session
  stream. The agent opens a long-lived session and receives commands as
  newline-delimited JSON; events and command results post on separate paths.
  No new dependency, deterministic under the Lab, and no inbound listener or
  exposed Docker socket on the node.
- `O1`: Rental is the capacity lease; Node is the enrolled runtime bound to one
  Rental generation. Ephemeral executions get neither.
- `L1`: Shadeform and Vast become CapacityProviders because they rent real VMs;
  RunPod stays in the ephemeral lane until an agent is proven to bootstrap on a
  pod; Docker becomes a local NodeRuntime through agent enrollment. Shadeform is
  the phase 5 conformance provider.
- `S1`: ServiceClass replaces PlacementObjective outright, with an event
  migration. No shim, no derived objective.
- `C1`: `internal/capability` owns the three contracts; `internal/adapter` keeps
  the ephemeral lane's wire types. Relocating 458 references would bury the
  contract split in a rename.
- `V1`: the Lab keeps a published observation catalog separate from World Truth,
  which is what ADR 0004 requires. The catalog this replaced was written once at
  construction and refreshed by nothing outside a test, so every Lab placement
  priced a frozen world; answering from world state instead was tried and
  reverted, because it is the alternative the ADR names as rejected and it leaves
  staleness inexpressible. The provider republishes when virtual time advances
  and after each command it carries out itself, and an offer states the time of
  the publication rather than of the read. What a scenario can leave unpublished
  is what the world did behind the provider's back, which is how a fixture writes
  down capacity that was reclaimed after the snapshot Placement scored.
- `V2`: a pull takes the world's own transfer time and an execution starts when
  its bytes have landed, so a launch has three moments: accepted, started,
  completed. Predicted start latency is calibrated against `started - accepted`,
  and no Run can be adjudicated on a host that did not hold its image while it
  ran.

## Progress

- [x] 2026-07-24: Audit the conflated provider seam, the four production
  adapters, Broker aggregation and stamping, Placement disposition logic, the
  orchestrator commit path, and the Lab's simulated world.
- [x] 2026-07-24: Approve the lane ontology and the three contracts. ADR 0005.
- [x] 2026-07-24: Create tracking issue #155.
- [x] 2026-07-24: Make image locality a fact about content. An offer states what
  its host holds; the scheduler subtracts that from the image's manifest. The
  contract it replaced carried a missing-byte count that asserted an answer about
  an image the offer never named, which is why every offer in the tree claimed
  zero missing bytes and every candidate looked fully warm. Unknown locality is
  now uncertainty recorded on the estimate rather than a hard rejection, and an
  unresolved manifest leaves every candidate indistinguishable rather than
  favouring whoever reports most.
- [x] 2026-07-24: Route Placement to nodes. Enrolled nodes are aggregated as
  reusable-lane offers beside provider offers, priced from the shadow price the
  operator configured at invitation. The launch intent records the selected
  offer's lane, and the run lifecycle dispatches launch, observation, and
  release on that recorded value, so a Run that landed on a node still reaches
  that node after a restart. `POST /v1/nodes` and `GET /v1/nodes` are the
  operator surface; the agent protocol moved to `/v1/node-agent` so the two
  audiences share no URL space. The agent now watches its runtime on every
  heartbeat, which is how an exit reaches Mercator without the application
  saying anything.
- [x] 2026-07-24: Complete the phase 2 node runtime. `internal/node` owns node
  identity, leases, fencing, durable command records, and reconciliation, and
  implements `capability.NodeRuntime`. `internal/nodeapi` is the outbound
  transport, mounted beside the operator API so neither credential can stand in
  for the other. `internal/nodeagent` and `cmd/mercator-node` are the agent:
  local durable memory of applied operations, an event spool, a heartbeat, and
  a Docker runtime behind a narrow interface. `nodetest.RunStoreSuite` runs the
  same promises against the in-memory and SQLite stores.
- [x] 2026-07-24: Make running a workload what warms a host. Both simulated
  worlds wrote held layers only at construction and read them only when
  snapshotting offers, so the corpus proved a warm Rental wins and never proved
  that running makes a Rental warm. Launching now pulls what the host is missing
  and leaves it there once the bytes have arrived. This is also the first writer
  of `ImageInventory.ImageDigests`, which makes the whole-image fast path in
  the manifest subtraction (then `TransferBytes`, now `StartWork`) live rather
  than dead.
  `domain.OfferSnapshot.KeepsWhatItRuns` is the single answer to whether content
  survives here, read by both simulators and the Lab invariant so they cannot
  drift: a provisionable offer is a machine that does not exist yet, and an
  ephemeral-lane offer holds nothing once its workload exits.
- [x] 2026-07-24: Make a Run wait for its own image. The pull took simulated time
  for locality and no time for execution, so a Run was adjudicated succeeded on a
  host that provably did not hold its image while it ran, and its Booking
  Decision predicted 290 seconds of start latency against an actual of zero. An
  execution now starts when its bytes have landed, the ledger separates the pull
  a launch dispatched (`image.pull`) from the content a host kept
  (`image.retained`, written when it lands), releasing an execution cancels the
  transfer it was waiting on, and `safety.locality_provenance` reads retention so
  a host holding content nothing delivered is a violation. The observed offer
  catalog is restored beside World Truth, per ADR 0004.
- [x] 2026-07-24: Resolve an image's exact manifest from a registry, in both
  digest spaces. `internal/ociresolver.RegistryResolver` reads the platform
  manifest for a digest-pinned reference over registry v2 token auth and returns
  layer blob digests with their compressed sizes beside the config's
  uncompressed diff IDs. That bridge is what image locality needed to be usable
  at all: a Docker daemon can only name what it unpacked, a registry manifest
  only names what it served, and `domain.ImageInventory.LayerDigests` was a flat
  list that said which of the two it meant. `ImageLayer` now carries both names
  for the same bytes and an inventory states the space its runtime can
  enumerate. `internal/daemon/runtime.go` builds the orchestrator with the
  resolver, which is what stops image locality from being dead outside the Lab:
  it had no manifest source, so every manifest was unknown and every candidate
  in the real product scored identically on locality.
- [x] 2026-07-24: Make the two sides of image identity the same string, and stop
  three answers from being stated more confidently than they were known. Two
  reviews falsified parts of the commit above, and each defect was a place where
  a name or a confidence was asserted rather than established:
  - the resolver named an image by the platform manifest it selected, while a
    Docker host reports the digest it pulled by, which for a multi-platform image
    is the index above that manifest. The whole-image fast path in
    `TransferBytes` could therefore never fire in production. The manifest is now
    named by the digest the Run is pinned to, which is the one name both sides
    can say;
  - `broker.launchOnNode` stamped the whole reference into
    `LaunchWorkloadCommand.ManifestDigest`, so the agent built
    `repo@repo@sha256:...` to run and reported an image under a name no manifest
    could match. It carries the digest;
  - `nodeagent.layerDiffIDs` discarded every error from `docker image inspect`
    and still reported the image hot with no layers, which prices a fully warm
    host a full cold pull at full confidence. An image a daemon will not describe
    is now reported unknown rather than hot;
  - `capability.ImageLocality` defined `LayerDigests` as every layer the manifest
    names and carried a `MissingLayerDigests` subset nothing wrote or read, while
    the one consumer treated the list as content held. Every field now states
    what the machine holds, and the two unread fields are gone: what is missing
    is a subtraction only the control plane can make;
  - `pullEstimate` stamped confidence 1 on a duration whose speed was
    `DefaultRegistryDownloadMbps`, an explicitly unmeasured assumption that no
    production node overrides. Bytes and seconds are now separated: a host that
    holds everything is certainly zero seconds away, and a host that has to fetch
    carries `domain.AssumedLinkConfidence` until something measures its link;
  - an unresolved manifest recorded source `unknown` for every candidate, so a
    throttled registry was indistinguishable from a host that cannot enumerate
    itself. `ImageManifest.Unreadable` carries why, `ociresolver.Unreadable`
    classifies it, and the estimate's source names it;
  - resolution ran unbounded and uncached on the run-create path: nine requests
    and three token mints per placement, which reaches Docker Hub's anonymous
    limit in about thirty-three placements, and a registry that accepts a
    connection and answers nothing could outlive the request. A digest names one
    document forever, so successful resolutions are remembered, one token serves
    the three reads of one resolution, and Placement gives a registry a bounded
    budget;
  - the Blueprint launched an image the registry refused to describe and had the
    world retain it after a zero-byte pull, so the corpus asserted a host warming
    on content nothing could serve. `WorldSpec` no longer forbids an unreadable
    image from stating its layers, because what is running and what can be read
    about it are two facts: the fixture is now the case an operator actually has,
    a control plane without a registry credential placing onto machines that can
    still pull;
  - a fixture could declare a diff-ID-reporting host against layers naming no
    diff IDs, producing an inventory that is `Known` and empty. Both shapes a
    registry cannot serve are refused at load;
  - the diff-ID clause added to `CheckWarmingDoesNotShrinkInventory` was
    unreachable by any test. Every clause of that law is now shown failing on the
    transform it exists to catch.
- [x] 2026-07-24: Answer the second round of review on the same commit. Two more
  reviews falsified five things, and each was again a claim wider than what was
  established:
  - failing a node's whole facts report over one un-inspectable image does not
    cost one heartbeat, whatever the comment and this plan said. It ends the
    agent's session, and on an agent with no session yet it blocks enrollment
    entirely, so one image pruned between `docker images` and `docker image
    inspect` could keep a forty-image host out of the fleet. An image the daemon
    will not describe is now one image reported `LocalityUnknown`, which is
    priced a full pull and never mistaken for warmth. A read that failed because
    the agent is shutting down still fails the report, because that says nothing
    about the machine;
  - naming the resolved manifest by the digest the Run is pinned to made
    `ImageInventory.Holds` platform-blind. An index digest names one image per
    platform, so an amd64 host that pulled the arm64 build reported exactly the
    digest an amd64 Run is pinned to and was priced zero pull seconds at full
    confidence for content it does not have. `capability.ImageLocality.Platform`
    is now populated by the Docker runtime and read by the node's offer
    projection: an image counts as held whole only when the build the machine
    holds is the build it runs. Layers need no such test, being
    content-addressed;
  - the 429 judgment call was wrong, and its stated reason was false: the
    resolution error is discarded at `orchestrator.imageManifest`, so nothing
    downstream could recover a status code from it and a throttled registry read
    exactly like a hung one. `ociresolver.ErrThrottled` and
    `ociresolver.ErrUnreachable` are classified where the response is, and the
    Blueprint contract gained the two matching `RegistryAnswer` states so the
    corpus can state the difference;
  - `MeasuredRegistryDownloadMbps` decided an answer was measured from the mere
    existence of a registry p10 fact, ignoring the fact's own confidence and its
    expiry, while the only production publisher of that fact was a literal 100
    Mbps in the Docker adapter. The adapter publishes no throughput fact at all
    now, because nothing has measured that link, and `OfferSnapshot.
    RegistryDownload` returns a speed with what its publisher said it is worth,
    valid as of the moment the offer was observed;
  - the `Known` clause of `CheckWarmingDoesNotShrinkInventory` was still driven
    by nothing: the transform that was supposed to catch it supplied an empty
    inventory, which the layer clauses reject first. The transform now keeps
    every digest and only stops enumerating, so one clause catches it and
    deleting that clause fails the Lab;
  - `TestPlacementChargesNothingForAnImageTheNodeAlreadyHolds` claimed to hold
    the whole-image identity and does not: its host holds every layer too, so the
    layer subtraction reaches zero whatever the manifest is named. Verified by
    breaking the resolver's naming, which leaves that case green and fails
    `TestResolverStatesEveryLayerInBothDigestSpaces` and the Docker conformance
    case. The claim now says what the case actually holds and names the tests
    that hold the rest.
- [x] 2026-07-25: Ask each Docker image store the question it can answer, and
  correct the record of the change that shipped the day before. `internal/
  nodeagent/docker.go` hardcoded `Unpacked: true` and `State: hot` for every
  image `docker images --digests` returned, so a machine holding content it
  cannot start a container on was priced an instant start. The first fix read
  the storage chain out of `GraphDriver.Data`, on the stated premise that a
  content-store daemon reports no chain for content it has not unpacked. That
  premise is false and was refuted from moby's source: `daemon/containerd/
  image_inspect.go` returns `GraphDriver` with a driver name and no data at all,
  unconditionally, for every image on the containerd image store, which is the
  default store for Docker Engine 29 on Linux (`daemon/image_store_choice.go`).
  Three of the four graph drivers name no chain either: btrfs returns no
  metadata, vfs and zfs return one directory rather than a chain. So the rule
  reported every image on the majority configuration cold, and on overlay2 it
  could never report anything but hot, because a graph driver's layer store
  holds applied layers only.
  - The runtime now reads which image store it is talking to and takes evidence
    only from that store. A graph-driver daemon registers an image once its last
    layer is applied, so listing and describing it is the evidence a container
    can start on it. A content-store daemon is asked over the Engine API, which
    is the only place moby reports whether an image's content is present
    (`Manifests[].Available`) and whether its chain is unpacked
    (`ImageData.Size.Unpacked`, the usage of the snapshot named by the image's
    full chain ID, zero when that snapshot does not exist). The CLI exposes
    neither. A content store this agent cannot read fails the whole report
    rather than guessing: calling that machine cold sends its own work to a host
    that has to fetch the image, and calling it hot promises a start it may not
    be able to make.
  - `capability.ImageLocality` states content presence as its own fact rather
    than leaving it inferred from a state. `PulledImageDigests` is what makes
    the scheduler charge local assembly instead of a transfer, so only a node
    that says the bytes are here may put an image in it. It also removes the
    collision the previous change introduced, where a node reporting `cold` for
    an image became `partial` in the Booking Decision for the same machine.
  - `domain.ImageInventory` gained `PulledImageDigests`, and `TransferBytes`
    became `StartWork`, which answers with two kinds of work and a
    `domain.LocalityState`. Fetching and unpacking are different work over
    different resources: a host that fetched an image and never assembled it
    owes local assembly at `AssumedUnpackMBps` and no transfer, and charging it
    a pull would bill the network twice for bytes already on the disk while
    sending an operator after a problem that is not there.
  - Unknown locality is priced as the whole image rather than as zero seconds. A
    host that will not say what it holds is not a host with nothing to do: the
    image has to arrive from somewhere, and nothing says any of it is already
    there. Pricing silence at zero scored a machine nobody can describe exactly
    like one that is provably ready, and the only term that could have
    compensated, `Weights.UncertaintyPenaltyUSD`, is multiplied by zero in
    production. An unresolved manifest is still zero for everyone, because then
    no candidate can be told from another and the comparison is unaffected.
  - Both simulated worlds stopped answering for capacity Mercator does not
    control. Nothing of Mercator's runs on a machine it borrows a slot on, so
    nothing enumerates it, and every provider adapter in the tree publishes
    `Known: false` for exactly that reason. The worlds were reporting those
    machines as having enumerated and found nothing, which is a different fact
    and a more confident one. `WorldSpec.hosts` gained `cached_images` so a
    fixture can state what such a machine truly holds while no offer carries it,
    which is the position an operator's own Docker host is in.
  - `ImageInventory.ValidUntil` is deleted along with the `inventory_stale`
    reason. The node stood behind its enumeration exactly as long as the offer
    built from it, byte for byte the same instant, and Placement refuses an
    expired offer outright, so no evaluation time existed at which a candidate
    was selectable and its inventory stale. A green Blueprint asserted that
    combination and only reached it because both simulated worlds set the two
    bounds independently.
  - The decision records which of the four states each candidate was found in.
    Only the control plane can state it: the host says what it holds, the
    manifest says what the image is, and the answer is the subtraction.
    `capability.LocalityState` is gone, because two vocabularies for one answer
    is how they drift.
  - `node.Registry.NodeSupport` declared ArtifactReplicas, CacheMounts, Prewarm,
    and GarbageCollection true while the Docker runtime implemented none of
    them, which is the failure ADR 0005 exists to prevent one layer down: a
    negotiated capability set is a promise Placement routes work against. Each
    becomes true again in the slice that earns it.
- [x] 2026-07-25: Answer the review of the image-store commit. Two reviews
  falsified six things, and each was a name nothing could match or a silence
  resolved into a fact.
  - The content store names an image by the platform manifest it selected, in
    both the `ID` and the `Digest` column: `singlePlatformImage` builds
    `RepoDigests` from `rawImg.Target.Digest` over an `ImageManifest` whose
    `Target` `NewImageManifest` replaced with the platform descriptor (moby,
    `daemon/containerd/image_list.go`, `image_manifest.go`). A Run is pinned to
    the index above it. Every locality answer that arm produced was therefore
    filed where `ImageInventory.Holds` can never find it, and a host holding
    18GB whole was priced a full fetch. `imageStore` now answers what an image is
    called as well as what can be started on it: the content store reads the
    descriptor its own image record targets, the graph driver keeps reading the
    reference digest it records.
  - Pricing unknown locality as the whole image turned silence into
    infeasibility through `MaxP90StartSeconds`. Every provider offer in the tree
    publishes an unknown inventory, so a Run with a start SLO and a large image
    got `no feasible offers` from capacity that may well be holding it, and the
    Lab oracle applied the same gate so nothing could catch it. The bound now
    strikes out only a candidate known to start late. A measured start latency
    still binds, because that is a measurement rather than a guess.
  - `Available: false` was reported cold, which claims none of the image is
    here. moby's `Available` is all-or-nothing over every blob a manifest
    references, so an interrupted pull reports it false while holding 17 of 18
    layers. `Size.Content` separates the two: no bytes here is cold, some of them
    here with no way to name which is unknown.
  - A node's per-image unknown was erased at the offer boundary. Everything else
    in an enrolled node's inventory is enumerated, so an image filed in none of
    the lists read as absence and the decision stated at full confidence that a
    machine holds none of an image that may be assembled on it.
    `ImageInventory.UnknownImageDigests` carries it, `StartWork` prices it from
    the layers the host did prove and answers `unknown` all the same, and the
    estimate's source names whose silence it was. The platform test now runs
    after it: an image whose build nothing could read is not an image known to be
    another platform's.
  - Both simulated worlds suppressed the inventory of capacity Mercator does not
    control inside the function that also builds World Truth, so World Truth said
    a borrowed machine holds nothing, which is not true and is not what ADR 0004
    gives it to say, and `safety.locality_provenance` had an empty inventory to
    read whatever the world did. The suppression moved to publication, which is
    where the fact it models lives. That also fixes the half the previous change
    missed: a provisionable machine that does not exist yet claimed it had
    enumerated itself and found nothing.
  - Judgment calls. An image the content store does not account for at all is not
    reported, because this daemon has no name for it the control plane could
    match. Partial content is reported as uncertainty rather than as a byte
    count: `ImageInventory` names content, moby reports how many bytes of an
    interrupted pull are present and never which layers they belong to, and
    carrying the count is a capability with its own Blueprint rather than a line
    in a review response, tracked as
    [#166](https://github.com/benngarcia/mercator/issues/166). `RequestSpec`
    gained `max_start_latency`, because the corpus could not express a latency
    SLO at all, which is why nothing caught silence becoming infeasibility.
- [x] 2026-07-25: Make Artifacts a domain concept with object storage as their
  authority. ADR 0006. There was no Artifact type anywhere in `internal/domain`,
  `domain.WorkloadSpec` could not carry one, and `scenario.WorkloadForRun`
  dropped a Blueprint's `consumes_artifacts` and `produces_artifacts` on the way
  into the real orchestrator, so `safety.artifact_dependencies` was checking a
  fact Mercator had no representation of.
  - `domain.ArtifactVersion` is the catalog entry: version ID, workspace scope,
    content digest, size, object-store location, producing Run, publication
    time. `Durable()` is the only admissible answer to whether a consumer may
    run. `domain.ArtifactReplica` is one host's copy, carrying the digest it
    claims and when that claim was checked, and it reaches Mercator on
    `OfferSnapshot.Artifacts` through an inventory that states separately
    whether the holder enumerated at all, exactly as images do.
  - Admission blocks on durability rather than on presence. The Lab gated on
    `hasAnyReplica`, which is presence on some Rental: that predicate makes
    content available the moment it lands on one machine and unavailable when
    that machine goes away, which is the distributed-filesystem model the
    architecture forbids, encoded in the blocking condition. The gate now asks
    the object store, and asks it about the workload Mercator would record
    rather than about the arrival the world holds.
  - Publication takes the world's own transfer time, which is what makes a
    producer's local write and its durable publication two moments. Between them
    a copy exists and the Artifact does not, and that gap is the only place a
    control plane gated on presence and one gated on durability behave
    differently. `DriveToCompletion` now advances until the world owes nothing
    it started, including an upload, because a parked consumer waits on one.
  - `safety.artifact_replica_verified` is the new Lab invariant: no copy of
    content the catalog cannot name, no copy claiming a digest that version does
    not have, every copy traced back to the object store (fetched from a
    publication, or written by the Run that produces it on its way to becoming
    one), and no Run reading a copy nothing checked.
    `safety.artifact_dependencies` reads the workload out of Mercator's own
    public event log instead of `RunArrival.Request`, so it checks Mercator's
    admission decision rather than the world against itself.
    `safety.locality_provenance` gained the Artifact half of its rule: capacity
    that keeps nothing holds no Artifact copy either.
  - Judgment calls. `ArtifactSpec` did not gain a workspace field: a Blueprint
    has no workspace vocabulary and each backend names its own, so the only
    honest value a fixture could write is "mine". The catalog entry carries the
    scope from the world's workspace, and a corpus statement about cross
    workspace isolation waits for a Blueprint that can express two workspaces.
    The object-store location is derived from workspace and version ID rather
    than authored, because a version is immutable and there is exactly one place
    its bytes can be. Only the two Artifact notes in `SimBackend` are gone; the
    Cache Mount note stays, because advertising a mutable cache on an offer is
    the next slice and not this one.
- [x] 2026-07-25: Answer the review of the Artifact commit. Two reviews
  falsified four things, and three of them were one defect: the rule the slice
  claimed to add was in the test harness.
  - The durability gate is Mercator's. `Orchestrator.step` asks
    `inputsAreDurable` before every placement and answers from
    `orchestrator.ArtifactCatalog`, which is the object store's own contract: a
    workspace and a version ID in, what that version is and whether the bytes
    are there out. `internal/lab`'s `objectStore` implements it, as the object
    store in that world rather than as a stand-in for one, and the Lab's own
    `admissible` and its list of withheld arrivals are gone. Before this,
    deleting `controlPlane.admissible` was the only edit in the tree that broke
    the new conformance cases, while ADR 0006, this plan, and the generated
    public contract all stated the rule as Mercator's behaviour. A Mercator with
    no catalog configured now refuses a Run that reads an Artifact rather than
    launching it against content nothing confirmed exists.
  - A Run held by that rule is a Run Mercator has accepted. It is in the
    projection with no Booking Decision, and `liveness.admitted_run_progress`
    fails it past the bound. The harness gate withheld `CreateRun` instead, so a
    parked Run appeared in neither `observation.Runs` nor
    `observation.RunRequirements`: a publication that never landed produced a
    green execution in which a declared arrival silently never ran.
  - Publication settles on the world's clock. `storeRunOutputs` ran from
    `Observe` and stamped `world.now`, so `ArtifactVersion.PublishedAt` and
    `ArtifactReplica.VerifiedAt`, which are World Truth, moved with Mercator's
    polling cadence, and every consumer's admission moved with them. `setNow`
    now walks the deadlines between here and there in the order they happen and
    settles each at its own instant. That also fixes the case where a producer
    released or terminated before an `Observe` saw it succeed published nothing
    at all.
  - `safety.locality_provenance` gained the per-host Artifact clause it was
    missing. Its Artifact rule lived entirely inside
    `onlyKeptCapacityHoldsWhatItRan`, which returns early for every Rental, so
    once a version was durable any host could hold a copy of it with nothing
    explaining how the bytes got there, which is what Placement would price as
    warm. Images have required an `image.retained` effect since retention was
    introduced; copies now require a World Tape seed or an `artifact.replicated`
    effect against that same host.
  - `safety.artifact_dependencies` treats a launch with no recorded workload as
    a violation rather than as a Run that consumes nothing. The rule was just
    re-pointed at Mercator's own event log, and a missing fact reading as no
    constraint would have made every clause of it vacuous.
  - `scenario.adaptLegacyRentals` migrates a legacy named cache as an unchecked
    copy. A fixture that said only "this machine has the content this key names"
    was being translated into an assertion that those bytes were hashed and
    matched a catalog the old model had no concept of, which prices a copy at
    zero where the honest reading costs a fetch.
  - `capability.ArtifactLocality` is deleted. It carried `Verified bool` beside
    `State domain.LocalityState`, which is two vocabularies for one answer and
    the drift `capability.LocalityState` was deleted for a commit earlier. A node
    reports `domain.ArtifactReplica`, the record the control plane already keeps.
  - Judgment calls. The Lab world no longer tracks an "active Run" for offer
    reads: a provider has no Run identity to answer a capacity query with, and
    the field only existed to attribute an effect and to check that a fixture's
    image is defined, which is now refused when the arrival is prepared. Two
    findings were rejected and are recorded under the verification evidence
    below.
- [x] 2026-07-25: Answer the second review of the Artifact commit. Two reviewers
  found the same two defects, and both were the same shape: a rule that exists
  at one layer and is discarded at the layer that answers.
  - The refusal reaches the caller. `Intake` accepted the Run, appended
    `run_requested`, threw away the error `inputsAreDurable` raised, and
    answered 202 with phase `requested`, after which the minute reconcile sweep
    logged the same refusal to the daemon's stdout forever. No production
    daemon wires an `ArtifactCatalog`, so that was every Run that read an
    Artifact: accepted, unplaceable, and never terminal. A Run whose declared
    inputs this Mercator cannot ask about is now refused in `CreateRun`, before
    the Run is recorded, as `400 ARTIFACT_CATALOG_UNAVAILABLE` naming the
    version. The refusal belongs at the door because the answer can never
    change: it is a fact about the deployment, not a provider failure the Run's
    recorded state could represent, which is what `Intake`'s best-effort
    advancement is for.
  - Virtual time reaches the liveness bound. `liveness.admitted_run_progress`
    fails an admitted Run past 24 hours, and no execution could get there:
    `DriveToCompletion` stopped as soon as `executionHorizon` was zero, and a
    Run parked by admission is neither a running execution nor an upload in
    flight, so a publication that never landed still exported a fully green
    bundle with a declared arrival that never ran. The world is not the only
    thing an execution can be waiting on. The driver now ends an execution past
    the longest bound any standing rule is stated against whenever Mercator is
    still holding a Run open, and the registry decides whether ending it there
    was a violation.
  - Judgment calls. The failing case is a Blueprint in `internal/lab/testdata`
    rather than in the corpus: it is a claim about the Lab's own driver, and its
    correct outcome is a red execution, which no catalog classification can
    state. The bound the driver drives to is read from the invariant registry
    rather than written twice.
- [x] 2026-07-25: Make Artifact locality a placement fact. The catalog existed
  and nothing scored it: `OfferSnapshot.Artifacts` reached Placement and
  `scheduler.pullEstimate` never read it, so a Rental holding a checked copy of a
  40GB dataset and one holding none of it were priced identically and the winner
  came down to a 40MB image layer. `internal/scenario/runner.go` had been reading
  `artifact_evidence` out of raw decision JSON since before the field existed.
  - `domain.ArtifactFetchWork` is the subtraction, beside `ImageManifest.
    StartWork` and following the same three rules. A host holds a version when it
    has a copy that was checked and checked against THIS version's digest, which
    is what makes an unverified copy cost exactly what no copy costs. A host that
    cannot enumerate its copies is charged the whole read, because silence costs
    what absence costs. And there is no partial: an Artifact version is one
    immutable object, so a host has bytes that were checked against it or owes all
    of them.
  - The answer reaches the score through `CandidateEstimates.ArtifactSeconds` and
    the start estimate it feeds. This entry claimed it reached the score through
    no weighted term, which was the inverse of what the code did and is corrected
    in the entry below: the start estimate's only path into `ScoreUSD` was
    `Weights.StartLatencyUSDPerSecond`, so the whole capability was inert for
    three of the four objectives.
  - `CandidateDecision.ArtifactEvidence` is what each candidate was found holding,
    one entry per declared input, stated beside `ImageLocality` rather than folded
    into it. They are answers about different content: an image is what the
    runtime fetches to start a container, an Artifact is what the workload reads
    once it is running, and one host is routinely warm for one and cold for the
    other.
  - `SchedulingInput.Artifacts` carries what the catalog says each consumed
    version is, resolved once by `orchestrator.consumedArtifacts` and used for
    both questions Placement has: whether the version exists, which decides
    admission, and how big it is, which decides what a host holding no copy owes.
    Size is a property of the content, so it comes from the store: a host that
    does not have something cannot be asked how large it is.
  - `fake.World` gained the object store the placement corpus needed. Its catalog
    holds what a Blueprint declares, durable from the start for content no Run in
    the fixture produces, and `scenario.ArtifactSpec.Version` is now the one place
    a declaration becomes a catalog entry, shared with the Lab's own store.
    `fake.Machine.HeldCaches` is deleted: it was written empty by scenario setup
    and read by nothing, and the mutable cache it stood for is its own slice.
  - `safety.locality_is_never_infeasibility` is the new Lab invariant, and it
    encodes the architectural rule that unknown locality is uncertainty, which
    `feasibilityViolations` honoured only by omission. No candidate in any
    recorded Booking Decision is struck out for what it holds, and a host that
    could not say what it holds is never refused on a start latency nothing
    measured. A measured latency still binds, because that is a measurement about
    this offer whatever anyone could enumerate.
  - The demo's proof checkpoint 7 is tightened. `consumerUsesArtifactReplica`
    asked only whether the producer's output landed on the offer the consumer was
    selected on, which is green on a Blueprint with one standing Rental whatever
    Placement weighed, and was green through every execution before Artifact
    locality was scored anywhere. It now requires the consumer's own decision to
    cite a checked copy of every input on the candidate it selected.
  - Judgment calls. `DefaultObjectStoreDownloadMbps` is a second stated
    assumption rather than a reuse of the registry constant: an object store and a
    registry are different services over different links, and a measured registry
    throughput must never silently price an Artifact read. Neither is measured
    today, so both durations carry `AssumedLinkConfidence`. A Blueprint's
    `artifact_evidence` gained a third word, `unknown`, because the corpus could
    not otherwise state the case the goal is most explicit about. And the runner's
    raw-JSON reader is deleted: the domain type carries the field now, so reading
    around it would be the second vocabulary this plan keeps deleting.
- [x] 2026-07-25: Answer the review of the Artifact-locality commit. Two reviews
  falsified six things, and five of them were one shape: a rule the corpus had no
  way to construct a world for, so deleting the rule changed nothing.
  - The objective decides which candidate wins. `cheapest`, `fastest_start`, and
    `fastest_completion` are public API values, and until now they were words
    Mercator accepted and never read: every candidate was ranked on one blended
    dollar score whose only time term was `Weights.StartLatencyUSDPerSecond`,
    which nothing populates in production outside the balanced objective's 0.0005
    default. So the whole Artifact-locality slice was inert for three of the four
    objectives, and a Run that asked for the fastest start was placed on whichever
    machine was a fraction of a cent cheaper, or on whichever offer ID sorted
    first when prices tied. `PlacementPolicy.Prefers` is the ranking, stated in
    the domain beside the objective it reads: least dollars then earliest ready
    for cheapest and balanced, earliest ready then least dollars for the two
    speed objectives. It is a ranking rather than an exchange rate on purpose.
    Turning a second of waiting into dollars needs a number nobody measured, and
    inventing one per objective would be the unmeasured constant this plan keeps
    deleting; which quantity a Run asked for the least of orders candidates on its
    own. `SelectionReason` names the rule so the record says what it ranked on,
    because a Run that asked for the earliest start and got the costliest machine
    is explained by its objective and not by LOWEST_SCORE.
  - `ScoreWeights` stays as the seam calibration fills in phase 4, documented as
    something nothing populates. The 0.0005 literal it duplicated in the scheduler
    and the oracle is now `domain.BalancedWaitingUSDPerSecond`, said once.
  - A start bound strikes out only lateness somebody established. Widening the
    unknown-locality escape hatch to Artifact evidence disabled
    `MaxP90StartSeconds` outright for any Run reading an Artifact, including for
    queue and provisioning, which the offer states as facts: a Run refusing to
    wait three minutes could be placed on a machine fifteen minutes deep in its
    own queue because one input was unreadable.
    `CandidateEstimates.EstablishedStartSeconds` carries the part of the
    prediction that rests on stated facts, the bound is asked of that, and the
    silence is still priced into the full prediction. A measured latency is
    established too, so `startEstimate` returns the sample for both halves and the
    `SampleCount` special case is gone.
  - `safety.locality_is_never_infeasibility` is restated against that estimate and
    is no longer vacuous. Its clauses fired only on decisions hand-built in a unit
    test, because no Blueprint in the corpus combined `max_start_latency` with
    `consumes_artifacts`, so reverting the scheduler to let silence become
    infeasibility left every Lab execution green. The rule now reads the refusal
    against the bound the decision itself recorded, and
    `a-late-start-must-be-a-fact` is the conformance Blueprint it is a law about.
  - `node.Registry.offer` projects the copies the node reports.
    `capability.NodeFacts.Artifacts` has carried them since Artifacts existed and
    the offer projection dropped them, so on the only reusable lane there is,
    every candidate was recorded holding nothing anybody could describe and
    charged the whole read for content already on its disk. This commit derived
    `Known` from the heartbeat's timestamp the way the image inventory does, which
    the next entry reverses: nothing in this tree enumerates Artifact copies, so
    the derivation made every enrolled node assert a fact it never established.
  - A fixture can put a copy on capacity Mercator does not control.
    `HostSpec.artifact_replicas` is the Artifact half of `cached_images`, and
    without it the rule that borrowed capacity publishes no Artifact inventory was
    a rule about a world no fixture could build: silence and absence were the same
    state every time, and deleting the guard in either simulator changed nothing.
    `onlyKeptCapacityHoldsWhatItRan` admits a seeded copy on such a machine
    exactly as it admits seeded image content, and forbids one it accumulated.
    The forbidding half stayed unfalsifiable, which the next entry fixes.
  - A fixture can state a copy that claims the wrong content.
    `ArtifactReplicaSpec.content_digest` is the machine an operator restored an
    older snapshot onto: a checked copy filed under this version's name whose
    bytes are the previous version's. The digest half of `ArtifactInventory.Holds`
    was unreachable without it, and `safety.artifact_replica_verified` was part of
    why: it forbade the state outright. Its digest clause now applies to copies
    this world delivered, which do carry the catalog digest, and not to what the
    World Tape seeded, which is a fact about that machine's own bookkeeping and
    exactly what the subtraction exists to catch.
  - Judgment calls. `fastest_completion` ranks on start plus the runtime the Run
    expects, which is the same ordering `fastest_start` produces until something
    predicts per-candidate throughput. It is written as the sum it means rather
    than as the shortcut it currently equals. `established_start_seconds` is a
    required field on the public estimate set, following `artifact_seconds` from
    the commit before it rather than inventing a second convention.
- [x] 2026-07-25: Answer the review of the start-bound commit. Two reviewers
  falsified six things and two of them were the same one twice. Four are fixed
  here and two are rejected, with the evidence for rejecting them.
  - A node states whether it enumerated its Artifact copies.
    `capability.NodeFacts.Artifacts` is a `domain.ArtifactInventory` now, so the
    claim travels from the only authority that can make it. Deriving `Known` from
    the fact that a node answered about its host was the same manufactured fact
    the image inventory earns honestly and this one could not: `DockerRuntime`
    has no replica store to look in and `PrepareArtifact` returns
    `ErrCapabilityUnsupported`, so every enrolled node in production published "I
    hold no copy of anything" as an observation. Combined with the start bound in
    the commit before it, the only reusable lane there is refused a Run reading a
    40GB Artifact with `LATENCY_SLO_EXCEEDED` and `NO_FEASIBLE_OFFERS`, for
    content the machine never looked for and may well have been sitting on. Now
    that runtime says nothing, silence is priced, and
    `TestANodeThatCannotEnumerateCopiesOffersNoArtifactClaim` holds it at the
    daemon.
  - A start bound is asked of the quantile the provider published.
    `provisionSeconds` read `Provisioning.Expected` and threw away
    `Provisioning.P90`, and `startEstimate` invented a p90 by scaling the whole
    sum by 1.25. So an offer publishing ten minutes expected and eighteen in its
    tail was enforced against 76 seconds, and the Run was recorded
    `WITHIN_START_SLO` against a bound the provider had already said it would
    miss. Start quantiles now add rather than being scaled off the expectation:
    each part's tail belongs to whoever published it, and summing them is
    deliberately pessimistic about a joint distribution nothing here models.
    `a-late-start-must-be-a-fact` already carried `"p90": "18m"` as a fixture
    value nothing read, and now the refusal cites it.
  - A Rental Schedule projects its wait from where its Bookings are.
    `ExpectedWaitSeconds` summed what every caller declared, undecayed, so a
    machine one minute from finishing a Booking whose Run declared an hour
    reported an hour of waiting for the whole hour. Bound to the start bound that
    refused Runs outright: the only Rental in a fleet was struck out for 3600
    seconds of lateness when the honest projection was 60.
    `ScheduledBooking.StartedAt` is when a Booking took the Rental, and
    `RemainingExpectedSeconds` is the projection over it, which `startBounds` and
    `reproject` now read too. The Blueprint contract already modelled exactly
    this: `RentalScheduleSpec.runningExpectedRemaining` subtracts elapsed time,
    so the spec and the implementation disagreed. A running Booking recorded
    before Mercator kept this owes its whole declared runtime, because a schedule
    that cannot say how much has elapsed must not assume any of it has.
    `a-queue-drains-as-it-runs` is the conformance Blueprint, and reverting the
    projection turns it into `no feasible offers`.
  - `safety.locality_is_never_infeasibility` reads the refusal twice, from
    independent halves of the record. Asking only whether a
    `LATENCY_SLO_EXCEEDED` rejection agrees with the `EstablishedStartSeconds`
    recorded beside it is asking the scheduler to confirm its own arithmetic:
    `feasibilityViolations` derives the rejection from that same field, so the
    error where that field is the thing computed wrong is invisible. Deleting the
    unknown-locality guard in `pullEstimate` left every Lab execution green with
    the rule reporting nothing. `silenceWasTakenBackOut` recomputes what was
    discounted from the localities and the per-kind seconds the decision records:
    the seconds taken out to reach the established prediction must be at least the
    seconds charged for content nobody could describe. The Artifact half converts
    bytes to seconds through the unreadable share of the read itself, so the rule
    holds no opinion about the rate the scheduler used and cannot be satisfied by
    agreeing with it. Both mutations the reviewers described now fail
    `a-late-start-must-be-a-fact` through the invariant.
  - The Artifact clause of `onlyKeptCapacityHoldsWhatItRan` is falsifiable.
    `simulatedWorld.keepReplica` refuses to write a copy onto capacity that keeps
    nothing, so the only state a borrowed machine could reach was the World Tape
    seed the clause exempts, and deleting the whole loop left the tree green. The
    image half is falsifiable only because a hand-built observation states the
    forbidden world directly, so the Artifact half now has one too:
    `TestLocalityProvenanceRejectsBorrowedCapacityThatKeptACopy`, beside the
    admitted seed it must not catch.
  - Rejected: that seconds derived from `DefaultRegistryDownloadMbps` and
    `DefaultObjectStoreDownloadMbps` may not be established, so a start bound must
    never strike out a host that enumerated and holds nothing. The split
    `EstablishedStartSeconds` draws is bytes-known against bytes-unknown, and that
    is the right split. A host that enumerated and holds no copy is not a host
    with unknown locality: the manifest and the inventory both spoke, and what is
    left is Mercator's stated assumption about a link, applied identically to
    every candidate and carrying `AssumedLinkConfidence` on the record. Refusing
    it is what a bound is for. A caller that sets `max_p90_start_seconds` has
    asked to be refused rather than kept waiting, and placing a Run predicted to
    wait thirteen minutes under a three-minute bound because Mercator is only half
    confident in its own prediction breaks the API contract instead of honouring
    it. `TestNeitherModelTurnsSilenceIntoInfeasibility` has held both rows since
    the image-store commit: a Rental that enumerated and holds none of the image
    is infeasible, and a machine nothing could ask is not. The real defect the
    finding points at is that nothing populates `HostFacts.Network`, so no node
    can escape the assumption by measuring. That is phase 4 and 6 work, tracked
    rather than papered over by disabling the bound.
  - Rejected: that a queue projected from caller-declared runtimes is not a fact
    a bound may bind on. It is Mercator's own arithmetic over data Mercator holds,
    and after the reprojection above it is the honest answer to "how long until
    this machine is free". Waiving the bound for it would place a Run behind
    fifteen minutes of stated queue when it said three, which is the failure the
    established estimate exists to prevent read from the other end.
  - Judgment calls. `domain.LaunchSeconds` names the one second every launch costs,
    stated once rather than as a literal in the scheduler and the reference model.
    `provisionEstimate` restates the expectation for a quantile a provider left
    unstated, because an unstated p90 is not a promise of a short tail.
    `QueueSeconds` carries one number across all three quantiles: neither a Rental
    Schedule nor a provider publishes a spread on a queue, and inventing one would
    be this model's arithmetic wearing a provider's clothes.
- [x] 2026-07-25: Make a Cache Mount workspace-scoped, generation-aware, and
  actually attached. A Cache Mount is mutable application-owned state whose only
  identity is its workspace-scoped name, and nothing in the tree carried the
  workspace: `capability.CacheLocality` had no workspace field, its
  `CompatibilityKey` was declared and never set or compared by anything,
  `world.cacheMounts` was keyed offer to name with no workspace anywhere, and the
  Lab ran one workspace, so the hard-isolation claim was not merely unimplemented
  but unfalsifiable. `internal/nodeagent/docker.go` built `docker run` arguments
  with no volume flag and ignored `command.CacheMounts` entirely, so no cache was
  ever attached to anything.
  - `domain.CacheMountRequirement` is what a workload declares: a name, the
    generation of content it can use, and the room it expects to take. The
    workspace is never stated there, because a workload cannot choose which
    workspace it runs in. `domain.CacheIdentity` derives one string from all three
    parts, which is what makes cross-workspace sharing impossible by construction
    rather than by a comparison somebody has to remember to make, and
    `domain.CacheVolumeName` is that identity as a container runtime's own name
    for durable storage.
  - `capability.CacheLocality` is deleted. A node reports
    `domain.CacheInventory`, the record the control plane already keeps, exactly
    as it does for Artifact replicas and for the same reason: two vocabularies for
    one answer is how they drift.
  - The Docker runtime attaches and enumerates. `LaunchWorkload` opens a volume
    per cache identity and mounts it, `Facts` reads the caches back out of the
    labels the agent stamped, and `NodeSupport.CacheMounts` is true again, having
    been withdrawn in the image-store commit. A new compatibility key gets its own
    volume: Mercator compares the key the application stated, and mounting the
    previous generation across it anyway would be a comparison with no
    consequence.
  - The Lab runs more than one workspace, which is what makes a cross-workspace
    leak expressible at all. A Blueprint's arrival states the tenant it belongs
    to as a label, each backend maps labels to its own workspace identities, and
    the control plane creates, advances, projects, and reads the event log for
    every one of them. An Artifact belongs to the workspace that declared it, so
    a Run outside the default workspace naming one is refused when the arrival
    plan is validated rather than held forever by a gate that could never be
    satisfied.
  - `safety.cache_mount_workspace_isolation` is the new Lab invariant: no cache
    identity is ever observed under two workspaces, read over the ledger of what
    each launch touched and over what each host is holding. It is deliberately
    not stated as "the identity equals what its parts derive", because the world
    derives identities with the same function such a rule would check them
    against, so the one error that matters would agree with itself and pass.
  - `safety.locality_provenance` gained its cache clause, and
    `safety.cache_disk_accounting` counts caches by identity rather than by name.
    Capacity that keeps nothing keeps no cache either, for the same two reasons it
    keeps no image and no Artifact copy.
  - Judgment calls. A cache is recorded on the decision and never priced: what a
    warm cache saves is work inside the application, and nothing here has measured
    it, so seconds would be an exchange rate this model invented. `CacheMount`
    carries no size, because moby prices a volume only by walking every volume on
    the host (`GET /system/df?type=volume`, measured at 4.8 seconds for 342
    volumes on the development machine and unbounded on a real one), which is not
    a read a heartbeat may make, and a fabricated zero would be a machine claiming
    an empty cache it may be holding gigabytes in; the size a Run declares is a
    statement about what it expects to use, and prewarming's disk reservation is
    the slice that earns a measured one. `CacheMount.CreatedAt` is the freshness a
    container runtime can state, because a holder that makes new storage per
    generation can say when this one began and cannot say when anything last read
    it. The mount path inside the container is derived from the name rather than
    declared, because nothing in this tree needs a workload-chosen path yet. And
    the previous generation's volume is left behind: reclaiming it is garbage
    collection, which this runtime still declares unsupported.
- [x] 2026-07-25: Answer the review of the Cache Mount commit. Two reviewers
  falsified five things, and four of them were the same shape: a claim made
  wider than the act that established it.
  - Creating the container is what creates the cache. The agent opened the
    volume itself before dispatching the run, so every launch that died before
    the container existed, an image this machine cannot resolve, a full disk, a
    refused command, left a labelled volume behind that nothing reclaims, and
    the node's next heartbeat reported that empty directory as a cache it holds.
    The next Run declaring the same generation was then recorded `hot` on a
    machine that had never run the work, which is exactly the distinction
    `domain.CacheEvidence` exists to make. `docker run --mount type=volume,...,
    volume-label=...` makes the daemon create the volume with the same labels
    when the container asks for it and not before, which deletes `openCache` and
    one command per cache per launch with it. Verified against the daemon on this
    machine, both halves: a run pinned to a digest no registry can serve leaves
    no volume, and a second run of an existing cache is accepted with the labels
    already stamped.
  - The compatibility key is constrained where it enters. It is stamped into an
    option list the daemon parses on commas, so a key carrying one would be read
    as further options rather than as a generation. The name has been checked at
    the door since it existed and for the same reason, and this is the other half
    of that rule rather than an escape at the place that builds the flag.
  - A cache read never fails the node's report. One volume pruned between
    `docker volume ls` and `docker volume inspect` failed the whole enumeration,
    which ends the agent's session and, on an agent with no session yet, blocks
    its enrollment: an operator tidying volumes on a working machine would take
    it out of the fleet. That is the defect the image seam already recorded above
    and fixed thirty lines away, reintroduced for state that is best-effort by
    construction, and this commit made a prune the expected maintenance action by
    leaving a volume behind per generation. The daemon prints the volumes it could
    describe and exits non-zero for the rest, so `run` returns both and the read
    keeps what came back; a read that answered nothing at all leaves the node
    saying nothing rather than claiming it enumerated and found none.
  - `safety.cache_mount_workspace_isolation` reads the storage a read reached.
    Both identities in a read request are derived from the reading execution's
    own workspace, so the collision rule could only ever catch a derivation that
    drops the workspace: a resolution that wandered into the neighbour's slot
    agreed with itself and passed. The read's consequence now carries the
    identity the disk answered from, and the rule claims that slot for the tenant
    that read it.
  - The seam that carries a declared cache to a real runtime has a test.
    `broker.launchOnNode` is the only path from a Run to a container runtime for
    the reusable lane, the Lab never drives that lane, and deleting the line that
    carries the mounts left the whole tree green. This is the third defect at this
    same seam, after `node.Registry.offer` dropping `Artifacts` and
    `NodeFacts.Artifacts` manufacturing `Known`, so the case is the end-to-end one:
    a Run submitted over the public API with a cache in its workload spec, and the
    node's own runtime asked what it was told to attach and under whose workspace.
  - Running fills a cache in both simulated worlds. `fake.World` seeded caches at
    construction and never wrote one, so the L0 corpus could only prove that a
    seeded cache is found and never that running is what fills it, which is the
    same defect the image half was fixed for in "Make running a workload what
    warms a host". A launch now opens the caches it declared on the machine that
    keeps what it runs, and `CandidateExpectation.cache_evidence` is what lets a
    fixture state the answer at all: the corpus had no vocabulary for cache
    warmth, so the two worlds could disagree with nothing able to say so.
  - Judgment calls. The cache the node reports is storage a workload of that
    tenant and generation was actually attached to, and not a statement about
    what is inside it. A container that was created and then failed to start
    still leaves the volume, and a workload that ran and wrote nothing leaves an
    empty one: what an application put in its own cache is not something any
    runtime can report, which is why `CacheMount` carries no digest and no size.
    The L0 world opens a cache when the execution starts rather than when it
    finishes, because nothing in that world models a workload finishing; the Lab
    writes it on exit, and what both now agree on is the claim that matters.
- [x] 2026-07-25: Answer the second review of the Cache Mount commit. Two
  reviewers falsified five things. Four were the same shape again, a claim wider
  than the act that established it, and one was a rule that could not fail. The
  last two entries above are corrected by this one.
  - A cache is storage a workload of this node's has run against, and the node
    now establishes that rather than assuming it. Moving the volume's creation
    into `docker run` narrowed the defect and did not remove it: the daemon
    resolves the image, creates the container and its mount points, and only then
    asks the runtime for a process, so an entrypoint the image does not carry, a
    device this host lacks, or a memory limit the kernel refuses exits non-zero
    with the labelled volume already on the disk and no workload ever attached.
    Verified against the daemon on this machine, and the report is now the
    intersection of two facts it already holds: the volumes stamped as caches,
    and the volumes a container of this node's was mounted on while it held a
    process. Nothing is reclaimed, because removing a volume on a failed launch
    would delete a tenant's warm cache whenever the failing launch was the second
    one, and reclaiming storage is garbage collection, which this runtime still
    declares unsupported. The evidence is as durable as the container record, so
    an operator who prunes containers gets a machine reporting fewer caches than
    it holds, which is the safe direction of the same trade: a cache left out is
    work an application repeats, and a cache claimed without evidence is a Run
    placed on a machine that never did the work.
  - Attaching a cache is what creates it, in the Lab as well. `storeRunOutputs`
    was the only writer, so World Truth held a cache from the moment a workload
    exited: a Run cancelled or terminated mid-flight left none, and a decision
    made while the first workload was still running was recorded cold on a
    machine whose volume a real node has had since container creation. The Lab
    now attaches at `StartedAt`, which is where L0 already opened one and where
    the daemon creates one, so all three worlds say a machine holds a cache from
    when a workload of that tenant and generation started here.
    `OperationCacheMountRead` and `OperationCacheMountWrite` are one
    `OperationCacheMountAttach`, because opening the storage and reading what is
    in it are one act and the whole of what a container runtime can report.
  - The corpus states cache warmth in both worlds at a moment they agree on.
    `running-fills-a-cache` asserted a hit fifteen minutes into a one-hour Run,
    which was true at L0 and false at L1, and nothing could say so because the
    fixture is only ever driven through `SimBackend`.
    `cache-mounts-never-cross-a-workspace` is the L1 twin: a fifth Run arrives one
    minute after the generation it needs was first attached, while that workload
    is still running, and records it hot. Reverting the Lab to attach on exit
    turns that row cold.
  - `running-fills-a-cache` constrains the rule it is about. Its second placement
    was decided by the warm machine being busy, so a scheduler that priced cache
    warmth left the whole corpus green and the entry above claiming a cache is
    "recorded and never priced" was protected by nothing. Both machines now hold
    the image whole and both are idle when the second Run arrives, the warm one is
    a nickel an hour dearer, and the Run is short enough that the two differ by
    less than half a cent: pricing the cache in dollars or in seconds sends the
    Run to the machine holding it, and the fixture says so.
  - `safety.cache_mount_workspace_isolation` drops the reached-slot clause. Both
    writers file a cache under a key equal to its own identity, so the storage a
    read reaches is the string it asked for by construction and the clause could
    not disagree with the line above it; deleting the world's `reached_identity`
    left the whole tree green. That is not a Lab accident, it is what storage is:
    a volume is named by the workspace, the cache, and the generation together,
    here and on a container runtime alike, so a resolution cannot wander without
    the derivation wandering first. What a wandering derivation looks like is two
    tenants claiming one identity, which the rule already reads, and which a real
    Lab execution now fails on rather than a hand-built observation.
  - Judgment calls. A workload that ran and wrote nothing still leaves a cache
    this host holds, because being attached is the claim and no container runtime
    can say what an application put inside its own storage, which is why
    `CacheMount` carries no digest and no size. A container that never started is
    not that claim. `CacheMountState.Revision` counts attachments rather than
    writes, for the same reason.
- [x] 2026-07-25: Make disk a resource content is accounted and reserved
  against. Nothing owned disk. `capability.HostFacts.DiskTotalBytes` and
  `DiskFreeBytes` were declared and nothing wrote them, `internal/node/offers.go`
  maps `DiskFreeBytes` onto the resource a workload's disk minimum is compared
  against, and `domain.Normalize` gives a workload stating none a one gibibyte
  default. So every enrolled node advertised zero bytes and
  `internal/scheduler` struck it out for every Run in the product, on machines
  with terabytes free: the only reusable lane there is could not be selected by
  anything. That is a live bug rather than a gap, and it is the half of this
  slice an operator can see.
  - The Docker runtime measures its machine by running `df` inside a container of
    that daemon's own. It is measured through the daemon because every other host
    fact in the report is the daemon's answer about the machine it runs on: the
    CPU count and the memory come out of `docker info`, so reading this process's
    own filesystem would describe two machines at once and report a laptop's SSD
    as the room a workload in a VM has. A container's root filesystem is the
    storage driver's, which is where image layers, volumes, and writable layers
    all land. Measured on this workstation at 3934171283456 bytes total and
    3743076331520 free, agreeing with `statvfs` of the daemon's storage
    directory.
  - An offer states the room a machine has left rather than the disk it was built
    with. Both simulated worlds subtract what is resident and what is promised to
    content still moving, so an offer that could never say no became one that
    can, and the Lab states the whole account as World Truth: capacity, every
    resident item named by the content it is, and the bytes reserved for
    transfers in flight. A rule that could only read the remainder could never
    catch a world that lost track of the difference.
  - `domain.DiskDemand.Eviction` is the subtraction, beside
    `ImageManifest.StartWork` and `ArtifactFetchWork`. A candidate short of room
    makes it by deleting something, and content it gives up is content it fetches
    again, so the shortfall is charged back onto the transfer that caused it. The
    charge is capped at what Mercator credited the candidate for holding, so a
    host may be priced exactly like one holding none of this Run's content and
    never worse: anything else on that disk belongs to somebody else, and
    charging this Run to fetch it back would be inventing an eviction policy
    Mercator cannot observe. Each kind of content gives up the share its own
    residency represents, because a disk filling up does not tell an image layer
    from a dataset. The share is taken in floating point because byte counts at
    these sizes multiply past int64, which the first draft did, wrapping a warm
    machine's price into a negative number of bytes.
  - `pull_source` names the disk when the disk decided part of the answer. A
    reader who took the layer subtraction at its word could not arrive at fifty
    seconds for a machine holding 18 of 18.04 gigabytes, and that field is
    already where this record says whose evidence an answer rests on.
  - `safety.disk_reservation_respected` replaces `safety.cache_disk_accounting`,
    which accounted for no disk: it checked that a copy named a version and
    appeared once and never compared a byte of what a machine held against what
    it had room for. Deleting the name is the point, because a rule that promises
    accounting and performs none reads as a world somebody checked. The new rule
    is that a machine's account adds up: every resident item names content with a
    positive size, no item is counted twice, resident plus reserved never exceeds
    the disk, and the copies and caches World Truth says are on a machine are
    exactly the ones taking up room in its account. That last clause is what stops
    it being satisfied by a ledger that quietly forgot a kind of content.
  - Both worlds refuse to build a machine seeded with more content than it has
    disk, and the Lab refuses a launch it has nowhere to put.
    `a-machine-with-no-room-refuses-the-work` is the Lab's own failing case:
    deleting the refusal turns that execution red through the invariant with
    "machine cramped-rental holds and reserves 50000000000 bytes on a 20000000000
    byte disk", which is a state no Blueprint could otherwise reach, because a
    world that refuses to be built over-subscribed leaves a launch as the only
    way content lands on a disk that cannot hold it.
  - Judgment calls. A daemon that cannot be asked for its disk fails the node's
    whole facts report, unlike an image or a cache volume it will not describe,
    which cost one entry each: the fact is a number, a node advertising zero is
    refused every workload with a disk minimum, and a node advertising a guess
    sends work to a machine that may have nowhere to put it. The probe runs on
    every report rather than behind a cache, because free disk is the fastest
    moving fact in it. The refusal a full machine makes is retryable capacity
    unavailability, because the disk it is short of may be free again once
    something else there finishes. Cache Mounts are counted in the Lab's ledger
    and not in the L0 world's, because nothing in that world's vocabulary can
    size one: `domain.CacheMount` carries no size, the Lab states sizes where it
    states World Truth, and the L0 machine reports what a machine could. And the
    L0 world refuses only at construction, because it models one placement
    decision rather than an execution, which is what `CapabilityLabExecution`
    names.
  - What is left. The scheduler's demand covers the image and the Artifacts a Run
    reads and not the room a declared Cache Mount expects to take, while the
    world counts that room when it decides whether a launch fits, so Mercator can
    place a Run the machine then refuses. Turning a declared cache size into a
    reservation is the prewarming slice, which is where a reservation has a
    holder and a failure mode; charging one here would price content on a rule
    nothing yet holds anybody to.
  - A Run that finds no feasible offer records no Booking Decision at all, so
    there is nothing to read the refusal off at the daemon layer and
    `TestARunPlacesOnANodeWithRoomForItAndNotOnOneWithout` reads it from the
    daemon's own answer instead. That is its own gap in the explanation record
    and is not fixed here.
- [x] 2026-07-25: Answer the review of the disk commit. Two reviewers falsified
  eight things, and three of them were one cause: `DiskDemand.Eviction` priced a
  remedy that cannot work. Deleting a layer this Run needs frees exactly as many
  bytes as fetching it back consumes, so a machine whose content does not fit
  ends where it began however much of its own content it gives up. The entry
  above is corrected by this one.
  - Room is a fact about the machine now and not a price. A candidate whose
    content provably does not fit is refused with `RESOURCE_INSUFFICIENT` at
    `resources.ephemeral_disk`, `CandidateDecision.Disk` carries the demand so
    the refusal can be read, and nothing about the disk touches a second of any
    estimate. The three symptoms were the cap, which priced a machine holding
    none of this Run's content identically with twenty gigabytes free and with
    two terabytes; the proportional split, which charged a fraction of an
    immutable Artifact version and a fraction of a single image layer, states
    `domain/artifact.go` says outright cannot exist; and the charge reaching
    `EstablishedStartSeconds`, where a modelled eviction could strike a provably
    warm candidate out under a start bound the previous slice had deliberately
    restricted to measurements. The only content that would make room belongs to
    somebody else, which Mercator neither observes nor commands, because no
    runtime here implements garbage collection. When one does, what it reclaims
    is a fact this demand reads rather than a policy the scheduler assumes.
  - The refusal is asked of established bytes only, which is the architectural
    rule read from the disk end. A host that could not enumerate itself is
    charged the whole content in seconds and never turned away for it, because
    the bytes nobody could describe may already be on its disk.
  - The room a Run reserved and the room its content takes were spent twice
    against one offer. A 60GB machine satisfied a 50GB floor and separately had
    room for 50GB of image and dataset, so the Run was admitted on a floor of
    50GB and ran with 10. They are one question now, and the Lab world holds the
    floor back while the workload runs, because a floor two Runs were both
    promised is a floor neither of them has.
  - The caches a Run declares are counted. `contentFor` read the image and the
    Artifacts only while the world refuses a launch whose declared cache has
    nowhere to go, so a Run whose 40GB cache could not fit was priced perfectly
    warm and then refused. The previous entry disclosed this under "what is
    left", which was honest and did not stop the Run being placed on a machine
    that refuses it.
  - Content a Run produces is accounted. Nothing could price it at placement,
    because a Run declares which Artifacts it publishes and never how large they
    will be, so the room it gets is the room it reserved. A workload that writes
    past it fails with its disk full rather than publishing a smaller Artifact,
    which is what the Lab does now instead of creating forty gigabytes of content
    on a twenty gigabyte machine and surfacing it as a Mercator safety violation.
    `a-run-that-cannot-write-its-output-fails` is the Blueprint, and deleting the
    rule fails it through `safety.disk_reservation_respected`.
  - A node measures its disk with a kernel call and is allowed to say it could
    not. The probe ran `docker run --rm busybox df` on every facts report and
    failed the whole report when it did not answer, and Facts is the heartbeat:
    the three ways that read fails are a full storage-driver filesystem, a node
    with no egress to Docker Hub, and a routine `docker system prune -a`, all
    likeliest on the machines this fact exists to measure, and each of them took
    a healthy node out of the fleet with its workload still running and its exit
    code unreported. `docker info` already names DockerRootDir, statfs answers on
    a filesystem that is one hundred percent full, and `capability.DiskFacts`
    states the room beside whether anything established it. A node that
    established none advertises none and is struck out on the disk floor every
    workload carries, in the Booking Decision where an operator can read it,
    while its liveness and its exits keep reporting.
  - Every clause of `safety.disk_reservation_respected` has the world it is the
    one to catch. Replacing `everythingHeldTakesUpRoom` with a bare nil left the
    package green, and so did deleting `residentContentIsAccountable`, which is
    the defect `safety.cache_disk_accounting` was deleted for rebuilt under a
    better name. The clause about a machine with no disk holding content is
    deleted rather than driven: every resident item has a positive size, so the
    capacity clause catches that world first and there is none it would be the
    one to catch.
  - Blueprints. `a-machine-with-no-room-refuses-the-work` is restated as the
    silence half, because Placement now refuses what it can establish: its
    machine is capacity Mercator borrows a slot on, which is the only way a
    launch can still arrive somewhere it does not fit. The corpus blueprint
    states the placement refusal and a third Run whose floor fits alone and not
    beside its own dataset; reverting either rule fails it, once by un-refusing
    the cramped machine and handing it the Run on an offer-ID tie, and once by
    placing a Run on a machine that cannot keep the promise it was admitted on.
  - Judgment calls. The generator's Runs ask for half a machine's disk rather
    than all of it, because a Run reserving every byte a candidate has leaves
    nowhere for its own image and every generated world had nothing placeable in
    it. A Run that finds no feasible offer still records no Booking Decision, so
    the corpus states the double spend as a placement onto the machine with room
    rather than as a world with none; that gap is older than this slice.
- [x] 2026-07-25: Say in the fleet listing what is known about a node's disk,
  and answer the review of it. A node that established no room wins no placement
  that declares a floor, which every Run does, so the listing showed a ready
  machine that quietly never runs and an operator had nowhere to read why. Two
  reviewers falsified the first attempt at saying so, and every finding was the
  same shape one layer up from the disk facts themselves: an answer published
  more narrowly than the question it answers.
  - The report has three values, because the question does. A boolean carried
    "measured" and "not measured", so a node that answered and could not measure
    its disk read identically to an identity nobody has ever heard from. Those
    send an operator to different places, one to a daemon its agent cannot see
    and one to a machine to go and start an agent on, and every invited node was
    published as the first from the moment it was created, which states a fact
    about a host Mercator has never been told anything about. `node.Record.Disk`
    answers `never_reported`, `unmeasurable`, or `measured` from the node's own
    observation time and its own claim.
  - `disk_free_bytes` is always stated. It was optional, so a machine that
    measured its disk and found it full omitted the number entirely: zero free
    bytes is the machine an operator is looking for, and it was the one value at
    which the field disappeared and a reader could not tell a full disk from a
    server that said nothing. A generated client typed it `number | undefined`
    for exactly the case it exists to report.
  - The two halves of one report can no longer disagree. Nothing stopped a node
    from stating 400GiB beside "I did not measure this", and both readers in the
    tree, the offer projection and the listing, took the bytes without
    consulting the claim: an out-of-tree agent carrying a previous measurement
    forward would have had room nobody established advertised to Placement, a
    Run with a floor admitted onto it, and the contradiction stated back to the
    operator. `capability.NodeFacts.Established` keeps the half the machine
    stands behind, applied where a report crosses into the control plane, which
    is enrollment and the heartbeat.
  - Nothing held the negative direction of any of it. The listing had one
    assertion, in its affirmative direction, fed by one fixture that hardcoded a
    measured 400GiB, so wiring the field to a constant left the suite green. The
    scripted runtime now reports the disk a case asks for, and the three
    answers, the full machine, and the placement an unmeasurable node wins none
    of are each held. Verified by making each reversion in turn: dropping the
    normalization advertises 400GiB nobody measured, restoring `omitempty` loses
    the full machine's zero, and pinning the report to `measured` mislabels both
    the unmeasurable node and the invited one.
  - A Blueprint could not state a machine with no room. Both simulated worlds
    read a stated `"0GB"` as an unstated disk and handed it the 200GB default,
    so a fixture written to model the machine this whole slice is about would
    have gone green asserting the opposite. `ResourcesSpec.Disk` is a pointer,
    so a fixture that mentions disk and one that does not are different
    sentences, and the corpus now carries the cheapest machine in its world
    offering no room and refused on room for every Run. Verified by restoring
    the swallow: that machine takes two of the three Runs. Zero CPU and zero
    memory describe no machine any offer can carry and stay single numbers.
  - The two worlds derive a host from one function rather than two copies of the
    same fifteen lines with their own constants. One Blueprint meaning one
    machine in the placement corpus and another in the Lab is two fixtures
    wearing one name, and a corpus statement about either would have said
    nothing about the other.
  - The conformance case runs the production agent against the Docker daemon on
    the machine the suite is on and reads the answer back over the public API,
    compared against an independent statfs of the daemon's own root. Every other
    case scripts the machine it lists, so the path from a kernel measurement to
    the number an operator acts on could have been broken anywhere along it. Ran
    green on a Linux workstation against Docker Engine 29.6.2 on the containerd
    snapshotter, which is the first time any of this slice has run on amd64: the
    disk work was built and verified on arm64 macOS. The whole Go suite and the
    node agent's live Docker cases run green there too.
  - Judgment calls. No Lab invariant accompanies this. The fleet listing is an
    operator read model that no Lab execution observes, and the three-valued
    report is a fact about an enrolled node's agent, which the Blueprint
    vocabulary has no way to describe at all:
    `enrolled-node-survives-its-first-run` is still a target scenario. What the
    corpus can state is the placement consequence, a machine offering no room,
    and it now does. And the reachability probe's missing timeout, issue #165,
    is deliberately untouched: it does not reproduce on this host, it is a
    separate filed defect with its own regression test to write, and folding it
    into a locality slice would hide it.
- [x] 2026-07-25: Prepare the machine a queued Run is going to, and stop when it
  is not. The prepare command path was fully built and had no caller.
  `capability.PrepareImageCommand` reached `docker pull` through
  `node.CommandPrepareImage`, `Registry.PrepareImage`, operation-ID dedupe,
  fencing, a durable command record, and the agent, and the only caller in the
  tree was one test. `broker.Nodes` declared Offers, Ref, LaunchWorkload,
  ObserveWorkload, and StopWorkload, so the prepare half of
  `capability.NodeRuntime` was unreachable from the control plane's own
  abstraction. Every Run that queued behind a Booking therefore paid its whole
  pull when its turn came, on a machine that had been idle for that image the
  entire time it waited.
  - Preparation is desired state rather than a stream of orders.
    `adapter.PrepareRequest` carries the whole of what Mercator wants prepared
    for one workspace, and content absent from it is content Mercator has
    stopped asking for. That shape is what makes withdrawal expressible at all: a
    Run whose caller cancelled it must stop costing the machine it was queued on
    disk and bandwidth, and "stop" is an absence rather than a command.
    `orchestrator.Prewarm` reconciles the set on every advance and holds what it
    last asked for in memory only, because the desire is derived from the event
    log every time and a restarted Mercator recomputes and resends it.
  - Speculation never competes with admitted work, and the control plane is the
    only thing that can promise it: a node performs its commands in order, a link
    carries what it carries, and a machine asked for six transfers performs six.
    So the controller refuses to prepare a host where a Run it already admitted
    is still getting ready. Nothing below the control plane reports that an image
    is still landing, because a provider says running from the moment it accepts
    a launch, so the answer is Mercator's own prediction measured from when the
    launch was taken. Believing its own estimate is the honest reading: it is the
    number the placement was made on.
  - `safety.prewarm_yields_to_real_work` reads the ledger and finds no
    speculative transfer overlapping the fetch an admitted Run is waiting for on
    that machine, and no more of them in flight at once than the world allows. It
    is stated as an overlap rather than as an ordering, because "precedes" is not
    the failure: a prefetch that started first and is still running is exactly
    the one in the way. `liveness.prefetch_converges` is the bounded half, that a
    requested preparation reaches hot or cold rather than holding a machine's
    room for hours with nobody able to say whether it is still coming.
  - `node.prepare_image`, `node.prepare_artifact`, and `node.prepare_abandoned`
    join the Effect Ledger and `effectMutatesWorld`, so
    `safety.idempotent_external_commands` covers them. `image.retained` names why
    the content came, which is what makes a host warmed by preparation
    distinguishable from one warmed by execution: they are different facts about
    a machine and the ledger had one word for both.
  - Blueprint vocabulary gains `world.prewarm` bounds and
    `arrivals.cancellations`. The corpus could not state a Run that goes away
    before it runs, and that is the one world speculative preparation has to be
    right about.
  - `DockerRuntime.PrepareArtifact` stops refusing. It reads from the location
    the command names and nowhere else, which is what keeps object-store material
    off a machine an operator rents by the hour. The copy is hashed as it streams
    and recorded under the digest those bytes produced rather than the digest it
    was promised, and it lands under its final name only once complete. `Facts`
    enumerates those copies, so an enrolled node stops publishing silence about
    content it is holding. `NodeSupport.Prewarm` and `ArtifactReplicas` are true
    again, having been withdrawn in the image-store commit; garbage collection is
    the one still owed.
  - Judgment calls. The desired set is per workspace rather than per host,
    because what may be in flight at once is a fleet-wide bound and a per-host
    command could not express it. A preparation identity carries the machine and
    the content and never the Run, so two Runs wanting one image on one host want
    one transfer. The Lab never restarts a preparation it abandoned under the
    same identity: content Mercator stopped wanting and then wants again arrives
    with the launch that needs it, and reusing the identity would leave one
    operation ID with two consequences. The rate bound applies to additions only,
    because waiting out an interval before telling a machine to stop is the
    opposite of what the bound is for. The artifact root is stated by whoever
    starts the agent rather than defaulted, and a node keeping copies outside the
    daemon's storage advertises the smaller of the two filesystems.
    `DefaultPrewarmPolicy` ships on at the most restrained bound that does
    anything, because shipping it off would leave this capability exactly where
    the prepare path already was.
  - What is left. Withdrawal is not expressible on a node: its contract has one
    command per piece of content and no way to say stop, so an item a desired set
    no longer names is a pull that runs to completion there. The Lab world models
    it because a provider seam can, and the node's half is
    [#170](https://github.com/benngarcia/mercator/issues/170). A refused
    preparation is terminal: the operation store dedupes on the identity with no
    regard for its state, so a node whose pull failed answers Duplicate for that
    content from then on and the desired set is never restated either, which
    defeats the node agent's own intent to retry. Reproduced under "the rate bound
    under review" below and owed a slice, because reissuing a refusal changes what
    an operation identity promises and the Lab world cannot yet refuse a fetch.
    Nothing in production implements `orchestrator.ArtifactCatalog`, so no
    production Run declares an Artifact and the Artifact half of the desired set is
    exercised at L1 and against a real object store rather than end to end.
- [x] 2026-07-24: Give the corpus standing capacity in the ephemeral lane.
  `WorldSpec.hosts` declares a machine Mercator has not enrolled, which is what
  the local Docker daemon is in production, and `unenrolled-host-holds-nothing`
  makes the lane half of `KeepsWhatItRuns` load-bearing for the first time. Both
  simulators now take the kind and lane the caller states instead of overwriting
  them, and grant Rental identity only to capacity that keeps what it runs.
- [x] 2026-07-24: Complete phase 1. `internal/capability` declares
  CapacityProvider, NodeRuntime, and EphemeralExecutor with negotiated support
  sets. `Declare` derives a backend's lane from the contracts it satisfies and
  refuses capacity with nothing to execute on it. `domain.ExecutionLane` carries
  the answer onto every offer; the Broker stamps it and clears unearned Rental
  identity. Placement rejects an unstated lane, refuses to queue behind one-shot
  capacity, and records `launch_ephemeral`. All four backends declare ephemeral,
  which is what they do today.

## Phase status

| Phase | What it delivers | Status |
| --- | --- | --- |
| 1 | Contract split under simulation | done |
| 2 | Node protocol and Go agent | done for hand-enrolled nodes; provisioned capacity does not bootstrap an agent yet |
| 3 | Exact OCI and artifact locality; prefetch; producer affinity | image inventory, execution-driven warming, registry manifest resolution, and exact node-side reporting done at L1 and against a real daemon; Artifacts are a domain concept with the object store as their authority, admission gates on it, and Placement prices what each candidate would still have to read out of it, which the Run's stated objective now ranks candidates on; mutable caches are attached, enumerated, compared per generation, and isolated per workspace end to end; disk is a resource an enrolled node measures with a kernel call, an offer states what is left of, and a Run's reservation and its whole content are admitted against together; prefetching is a controller that gets a queued Run's host ready, bounded so it never competes with work already admitted there and withdrawn when the Run that wanted it goes away, and an enrolled node replicates an Artifact from a control-plane-minted read; a production object-store client and producer affinity remain |
| 4 | Candidate prediction, service classes, owned economics, replanning | not started, except that the four placement objectives now order candidates rather than being multiplied by weights nothing populates |
| 5 | One true VM provider with agent bootstrap and conformance | not started |
| 6 | Telemetry waterfall, calibration, explanation UI, counterfactuals | not started |

## Scenario and invariant coverage

Phase 1 added:

- `ephemeral-execution-is-never-a-rental` (green): a one-shot product is the
  cheapest and fastest candidate and still records `launch_ephemeral`, because
  nothing survives the workload's exit.
- `enrolled-node-survives-its-first-run` (target, missing `node_runtime` and
  `rental_schedule`): capacity provisioned for the first Run is still there when
  the second arrives, and the second reuses it rather than provisioning again.
- `safety.ephemeral_capacity_not_reused` (Lab invariant): no Run is ever queued
  behind one-shot capacity, and capacity held for a one-shot execution never
  accumulates a second Booking.

Phase 3 added:

- `running-warms-the-host` (green): two identical cold Rentals, and the one that
  ran the image is the only one at zero pull seconds afterwards. Shortening its
  advance to a second turns it red, because a second is not long enough for
  18GB to have arrived.
- `ephemeral-execution-holds-nothing` (green): a one-shot product runs the same
  image twice and pays the whole pull both times, beside a Rental that ran it
  once and is warm. Holding nothing is only a claim worth making beside capacity
  that holds something, which is why the fixture carries both.
- `execution-warms-a-rental` (conformance): the same claim at L1, driven through
  the real orchestrator, event log, and Run projection, asserted on the Booking
  Decisions the control plane recorded rather than on world state.
- `unenrolled-host-holds-nothing` (green): a Docker host Mercator has not
  enrolled runs the same image twice and pays the whole pull both times, beside
  the Rental that ran it once and is warm. It is the only fixture in either
  simulator that separates the lane from the kind, and deleting the lane term
  from `KeepsWhatItRuns` fails it.
- `borrowed-slot-holds-nothing` (conformance): the same claim at L1, through the
  real orchestrator, event log, and Run projection.
- `registry-manifest-bridges-digest-spaces` (green): the warm candidate is a
  Docker host, so it enumerates its layers as uncompressed diff IDs and can
  never pronounce the compressed blob digests the registry served. It is
  recognised as holding the image whole and beats a cheaper host that holds
  neither and pays the full 24.3GB transfer. The second step asks for an image
  the registry cannot resolve a manifest for, and every candidate records zero
  pull seconds with source `unknown` and no confidence, so silence stays
  uncertainty rather than becoming warmth. No Lab invariant: manifest resolution
  is a read that mints no external consequence, and the fact it produces is
  already policed by `safety.locality_provenance`.
- `registry-silence-has-a-name` (green): the two silences an operator cannot fix
  by fixing credentials. A registry rate limiting Mercator and a registry that
  answered nothing at all are priced identically, as zero seconds carrying no
  confidence, and recorded as `registry_throttled` and `registry_unreachable`,
  because one is waited out and the other is a network path to repair. Deleting
  either classification from `ociresolver.Unreadable` collapses both onto
  `registry_unreadable` and fails it.
- `unpacked-is-not-the-same-as-pulled` (green): four machines are offered at one
  price for one 18.04GB image, so nothing but what each holds can decide the
  placement. The one that assembled it wins at zero seconds; the one that
  fetched it and never unpacked it is recorded partial and priced the assembly
  it still owes, which is what stops a machine sitting on the image from being
  priced either an instant start or a fresh pull; the one holding nothing is
  priced the whole fetch; and the one that cannot say what it holds is priced
  that same whole fetch and recorded `unknown` with source `inventory_unknown`.
  Silence costs what absence costs, and the decision records which of the two it
  was. Every rate is equal on purpose: a fixture whose price gap decides the
  winner proves nothing about locality. No new Lab invariant: what a node says
  it holds is an observation, and the rule that matters is
  `safety.locality_provenance`, which now reads unassembled content too.
- `silence-is-not-infeasibility` (green): one Run that refuses to wait more than
  three minutes, and two machines at one price that would both take nearly five.
  The Rental that enumerated itself and holds none of the image is struck out,
  because that is a measured fact about a machine and a hard bound is what a Run
  gets to do with one. The borrowed host beside it is not, because nothing has
  established that it is slow. Making the bound locality-blind fails it with
  `no feasible offers`, which is the Run finding no capacity at all on machines
  that may already hold every byte. The decision records
  `START_SLO_UNVERIFIED` for that placement rather than `WITHIN_START_SLO`,
  which the scheduler used to append whenever a bound existed at all: admitting
  a candidate because nobody could describe it is not the same as promising it
  will start in time, and the fixture fails if the two are conflated.
- `borrowed-warmth-is-invisible` (conformance): a machine Mercator has not
  enrolled holding the whole image before the Run arrives. World Truth says it
  holds it, the offer carries no inventory, and the Run is priced the whole
  fetch. Publishing what such a machine holds fails it with `pull source
  "image_inventory" for a machine nothing of Mercator's runs on`.
- `artifact-must-be-durable-before-a-consumer-runs` (conformance): four claims
  about what makes an Artifact consumable, driven through the real orchestrator,
  event log, and Run projection. A producer writes its 10GB checkpoint onto the
  host it ran on and the object store takes it 160 seconds later, and Mercator
  holds its consumer unplaced across that whole gap while carrying it in the
  projection the entire time. A later Run consumes an Artifact whose only copy
  sat on a Rental whose idle lease has since elapsed, and runs anyway from the
  object store. That same Run reads a second Artifact whose copy is sitting on
  the host it landed on and fetches it anyway, because nobody ever checked those
  bytes against the catalog. Driven twice at cadences ten minutes apart, the two
  executions agree on when the checkpoint was written and when it became
  durable, because those are facts about the world rather than about the
  observer.
- `safety.artifact_replica_verified` (Lab invariant): no copy exists of content
  the catalog cannot name, no copy claims a digest that version does not have,
  every copy traces back to the object store, and no Run reads a copy nothing
  checked. "Traces back to the object store" has two shapes and the second is
  why the rule is not simply "the version is durable": a copy was fetched from a
  publication, or it is the output the producing Run wrote on its way to
  becoming one.
- `dataset-gravity-beats-image-cache` (green): four machines at one price for
  one Run that reads a 40GB dataset, so nothing but what each holds can decide
  the placement. The Rental holding a checked copy owes 40MB of image and beats
  the Rental holding the whole image and no copy, because 640 seconds of object
  store dwarfs 0.64 seconds of registry. The third Rental reports a checked copy
  under this version's name whose bytes are another version's, which is the
  machine an operator restored an older snapshot onto, and it owes the whole read:
  a copy of other content is worth exactly what no copy is worth. The fourth is a
  machine Mercator has not enrolled, and World Truth says it is sitting on the
  image and on a checked copy of the dataset. Nothing there can be asked, so it
  records `unknown` and is priced the whole read rather than the zero its silence
  used to buy, and never the zero its copy would have bought if an offer could
  carry it. Every rate is equal on purpose.
- `the-objective-decides-what-wins` (green): one world, two Runs, and the only
  difference between them is the objective each stated. The warm Rental is a
  second from ready at 4 USD an hour, the cold one nearly sixteen minutes at 2.
  A Run that asked for the cheapest capacity takes the cold machine; a Run that
  asked for the fastest start takes the warm one, pays double, and records
  `EARLIEST_START` rather than claiming it compared prices. Ranking on cost alone
  fails it with the hurried Run placed on the cold machine.
- `dataset-gravity-worth-waiting` (target, missing `rental_schedule`): the same
  gravity behind a running Booking. It now states one missing capability rather
  than three, because Artifacts and their per-candidate evidence exist.
- `a-late-start-must-be-a-fact` (conformance): one Run refusing to wait three
  minutes and reading a 40GB dataset, against a Rental a second from ready, a
  machine that does not exist yet stating ten minutes of provisioning, and a
  borrowed host nothing can be asked about. The provisioning is a fact about that
  offer, so the bound strikes it out; the borrowed host's twenty minutes is what
  its content would cost from nowhere, so the same bound leaves it alone. It is
  the only Blueprint that combines a start bound with an Artifact the Run reads,
  which is what gives `safety.locality_is_never_infeasibility` a recorded decision
  to be a law about.
- `a-queue-drains-as-it-runs` (conformance): the queue half of the same bound. The
  only Rental in the world is twenty-nine minutes into a Booking whose Run
  declared half an hour, and the arriving Run refuses to wait more than three
  minutes. What it waits for is the minute that is left, so it takes a queued
  Booking; a schedule that summed declared runtimes answered half an hour and
  reverting the projection turns the execution into `no feasible offers`.
- `a-host-that-cannot-hold-the-data-is-not-warm` (green): three machines and one
  Run at a time. Two hold the same 18GB layer of the same image at the same
  price, and one of those has nowhere to put the 40GB dataset the Run reads, so
  it is refused with RESOURCE_INSUFFICIENT naming its disk. Room is not a
  preference the scheduler weighs down, because nothing this machine could delete
  helps: every byte it gave up of the Run's content is a byte the Run needs back.
  Reverting the rule un-refuses it and hands it the Run on an offer-ID tie. The
  second Run states a disk floor of its own and the machine that cannot meet it
  is struck out; the third states a floor the roomy machine could meet on its own
  and reads a second dataset beside it, and reverting the reservation out of the
  demand places it on the machine that cannot keep the promise it was admitted
  on.
- `a-machine-with-no-room-refuses-the-work` (Lab testdata): capacity Mercator
  borrows a slot on, twenty gigabytes of disk, and one Run needing a ten gigabyte
  image and a forty gigabyte dataset. It says nothing about what it holds, so
  Placement cannot establish the shortfall and selects it, which is the only way
  a launch can still arrive somewhere it does not fit. The machine refuses the
  work rather than taking it and filling up partway through, and deleting the
  refusal fails the execution through `safety.disk_reservation_respected`.
- `a-run-that-cannot-write-its-output-fails` (Lab testdata): a producer on a
  twenty gigabyte machine computing a forty gigabyte checkpoint. Nothing could
  have priced it, because a Run declares which Artifacts it publishes and never
  how large they will be, so the room it gets is the room it reserved. The
  workload fails with its disk full and the checkpoint never becomes durable;
  deleting the rule creates forty gigabytes of content on a twenty gigabyte disk
  and fails the execution through `safety.disk_reservation_respected`.
- `safety.disk_reservation_respected` (Lab invariant): a machine's own account of
  its disk adds up. Every resident item names content with a positive size, no
  item is counted twice, resident plus reserved never exceeds the disk, and the
  copies and caches World Truth says are on a machine are exactly the ones taking
  up room in its account. It replaces `safety.cache_disk_accounting`, which
  accounted for no disk at all. Each clause has the one world it is the one to
  catch, verified by deleting each in turn.
- `safety.locality_is_never_infeasibility` (Lab invariant): no candidate in any
  recorded Booking Decision is refused for what it holds, and a candidate refused
  for a late start has to have established that lateness. It is the one rule in
  this registry stated entirely against Mercator's own decisions, because a
  candidate Mercator struck out leaves a trace nowhere else. Stating it against
  the established estimate rather than against "was anything unknown" is what
  keeps it from buying silence an exemption from a bound: a machine deep in its
  own stated queue is late whatever it could say about its disk. It reads the
  refusal twice over, because the recorded established estimate is what the
  scheduler derived the refusal from: the second reading recomputes what was
  discounted out of the localities and per-kind seconds recorded beside it, so a
  scheduler that counted a silence as established fails while agreeing with itself
  perfectly.
- `cache-mounts-never-cross-a-workspace` (conformance): five Runs, two tenants,
  and one shared machine beside a spare. Both tenants declare a cache called
  compiler-cache, so the machine holds two, and the recorded decisions say so: the
  neighbour's cache is never warmth, and neither is the generation the application
  replaced. The hot Run in the middle is what makes the cold answers mean
  anything, the fifth Run finds a generation whose workload is still running,
  and dropping the workspace from the cache identity fails the execution through
  `safety.cache_mount_workspace_isolation` rather than through an assertion.
- `running-fills-a-cache` (green): two Rentals holding the same image, one of them
  dearer and free, the other cheaper and busy for five more minutes. The first Run
  takes the free one and finds nothing under its cache's name; fifteen minutes
  later both are idle and a second Run declaring the same generation finds that
  cache on the machine that ran the first and none of it on the machine beside it.
  Running is what fills a cache, exactly as running is what warms a host, and
  every other cache fixture seeds one at construction. The second Run lands on the
  cheaper machine, because a cache is recorded and never priced: the two differ by
  less than half a cent over that Run and are equally warm on its image, so
  pricing the cache in dollars or in seconds flips the placement and fails the
  fixture.
- `safety.cache_mount_workspace_isolation` (Lab invariant): no cache identity is
  ever observed under two workspaces, read over the ledger of what each launch
  attached and over what each host is holding. Stating it as a collision rather
  than as an identity derivation is what keeps it independent of the code it
  polices: the world derives identities with the same function, so a rule asking
  whether an identity equals what its parts derive would agree with a derivation
  that dropped the workspace. It says nothing about which slot a read resolved to,
  because storage is reached by the identity itself, here and on a container
  runtime alike: a volume is named by the workspace, the cache, and the generation
  together, so a resolution cannot wander without the derivation wandering first.
- `prewarming-never-starves-real-work` (conformance): one machine and four Runs.
  The first is admitted and spends ten minutes fetching the forty gigabytes it
  runs, the second is queued behind it, and Mercator gets the machine ready for
  it: the image and the twenty gigabyte dataset it reads, one at a time and no
  sooner than a minute apart. Nothing starts until the admitted Run's own fetch
  has landed, because a node performs its commands in order and both cross one
  link. A third Run arrives once the machine holds both and its decision prices
  that host at zero pull seconds and a checked copy, on a machine that has
  executed neither. The fourth wants sixty gigabytes nothing else needs and its
  caller withdraws it eight minutes in: the preparation stops and the room goes
  back. Deleting the yielding guard, the concurrency bound, the withdrawal, or
  the whole controller each fails it, three of them through an invariant. It
  states nothing failable about the rate bound: its Runs arrive on minute
  boundaries and the harness advances a minute at a time, so the gaps between its
  preparations are the cadence rather than `PrewarmPolicy.MinInterval`.
- `prewarming-holds-its-own-rate-bound` (conformance): one machine already holding
  the image, and three Runs that want two versions of one corpus. The first
  occupies the machine, the second queues and Mercator asks for
  `artifact:corpus:v70` a minute in, and the third arrives ninety seconds later
  wanting `artifact:corpus:v7`. The second speculative fetch waits until five
  minutes after the first one started. Every part of the fixture exists to be
  failable: the wanted names prefix-collide, so a control plane comparing a new
  desire against the joined text of the last one reads `v7` as content it has
  already asked for and skips a bound it applies to additions only; the gap is
  longer than the cadence, so the harness cannot produce it; and the third Run
  arrives between two ticks, so the moment the bound is tested is not a moment the
  driver chose. Deleting the rate bound, or restoring the substring comparison it
  replaced, each fails it through `safety.prewarm_rate_within_bound`.
- `safety.prewarm_rate_within_bound` (Lab invariant): no two moments at which
  Mercator began preparing are closer together than the world's `min_interval`.
  It is stated over the moments preparation started rather than over transfers,
  because one desired set crosses the boundary at once and may open as many
  transfers as the depth bound allows: how many may move together is the other
  rule's question. A world stating no interval states no opinion.
- `safety.prewarm_yields_to_real_work` (Lab invariant): no speculative transfer
  is moving onto a machine at the same time as content a Run admitted there is
  waiting for, and no more of them are in flight at once than the world stated.
  It is an overlap rather than an ordering, because a prefetch that started first
  and is still running is exactly the one in the way. It reads the ledger rather
  than world state, because a transfer that has finished leaves nothing in world
  state and the question is about the moment it was happening.
- `liveness.prefetch_converges` (Lab invariant): a requested preparation reaches
  an answer within two hours of virtual time, the content landing or Mercator
  withdrawing it. What may never happen is the third state, a transfer holding a
  machine's room and its link with nobody able to say whether it is still coming.
  Assumptions: virtual time advances, and the registry and object store keep
  answering.
- `safety.locality_provenance` (Lab invariant): every digest a host holds is
  either seeded by the World Tape or recorded as retained there by an
  `image.retained` effect, every Artifact copy a host holds is either seeded or
  recorded landing there by an `artifact.replicated` effect, and only capacity
  Mercator keeps holds anything beyond its seed. Retention is written when the bytes land, so a host that holds
  content nothing has delivered fails the rule. It says nothing about a host
  holding less than before: locality decays, and a machine that lost what it held
  is a fact the World Tape must be able to state.

The corpus is 23 regression Blueprints: 15 green and 8 target, beside one demo,
one minimized case, and seven conformance Blueprints.

## What phase 2 does not yet do

Placement now routes Runs to enrolled nodes, and one node runs successive
workloads. What is still missing is how a node comes to exist on capacity
Mercator rents: a provisioned machine arrives with no agent, so only a node an
operator enrolled by hand is reusable. That is phase 5, and it is why
`enrolled-node-survives-its-first-run` stays a target scenario alongside the
Rental Schedule work.

Enrolling the local Docker host is a manual two-step: invite a node through
`POST /v1/nodes`, then run `mercator-node` with the returned bootstrap. There is
no CLI command or quickstart step for it yet.

A node's price is whatever the operator configured at invitation. The rest of
the owned-capacity economics the goal asks for, committed billing intervals,
idle-tail expectation, and warm-capacity opportunity cost, is phase 4.

## Known residual conflation

An ephemeral execution still commits a Booking against a single-use Rental
identity. The lane makes that binding unqueueable and the audit trail names it
honestly, but the record type is shared with reusable placements. Phase 2
introduces the Node and separates the two bindings.

## What phase 3 warming does not yet do

Warming stops at capacity Mercator already holds. A provisionable offer that
becomes a Rental cannot be modelled, because provisioned capacity does not
bootstrap a node agent yet, which is phase 5. Both simulators keep such an offer
cold, and the reason is the honest one: the offer is a template for a machine
that does not exist, so nothing an execution fetches there is anywhere a later
Run can see it. `enrolled-node-survives-its-first-run` declares
`execution_warms_capacity` alongside `node_bootstrap` and `rental_schedule`,
which is the corpus stating what its second step was always waiting on.

A host Mercator has not enrolled still reports `Images.Known: true` holding
nothing. In production that machine cannot enumerate its own content at all and
reports `Images.Known: false`, which is uncertainty rather than emptiness. The
two are priced identically today because nothing in the scheduler treats an
unknown inventory differently from an empty one; phase 4 is where that becomes a
difference worth modelling.

Neither simulator models a pull that fails, is throttled, or half-completes. A
transfer moves whole or not at all, which is why cancelling one on release leaves
the host exactly as cold as it was. What a Blueprint can now state is a registry
that refuses to describe an image, in all five of the ways a real one does; what
it still cannot state is a machine whose pull is refused after Placement priced
it.

A Blueprint cannot state per-platform builds of one image. `WorldSpec` images
have one layer set, both simulated resolvers ignore the platform they are asked
for, and every simulated offer is `linux/amd64`, so the corpus cannot express a
host holding another platform's build of the digest a Run is pinned to. That
case is held at L1 instead, by
`TestPlacementChargesTheWholePullForAnotherPlatformsBuild` in `internal/daemon`
and by the node's own projection test, and it is the reason the platform fix
lands without a Blueprint of its own.

No fixture yet leaves a provider observation unpublished. The mechanism exists
(`setOfferAvailable` changes the world without republishing, and
`TestPlacementCanReadAnOfferTheWorldHasAlreadyReclaimed` drives it), but no
Blueprint places a Run against capacity that vanished between the snapshot and
the launch.

## Verification evidence

### Phase 3 controlled prewarming

On 2026-07-25, `prewarming-never-starves-real-work` was written against the
world, driven as a target Blueprint, and promoted in the same change once green.
Each claim is held by a deliberate break that fails it:

- deleting the guard that refuses to prepare a host still getting ready for work
  Mercator admitted there fails the Blueprint through the invariant: `Lab
  invariant "safety.prewarm_yields_to_real_work" failed: machine "builder" was
  fetching "sha256:7a1c..." speculatively for Run "run-patient" while Run
  "run-long" was waiting on "trainer@sha256:5d7e..." to start there`. It also
  fails `TestNothingIsPreparedOnAMachineStillGettingReadyForItsOwnRun` against
  the production daemon, which is the same rule one layer up: `the machine was
  asked to prepare [sha256:8888...] while the Run it was just given is still
  fetching its own image`;
- deleting the truncation to the world's concurrency bound fails it through the
  same rule from the other side: `2 speculative fetches were in flight at
  2030-01-01T00:11:00Z, and this world allows 1`;
- never abandoning what a desired set no longer names fails
  `TestPreparationStopsWhenTheRunThatWantedItGoesAway` with `the ledger records 0
  withdrawals, want the one Run whose caller withdrew it`;
- turning the controller off entirely, which is the state the tree shipped in,
  fails `TestAPreparedHostIsWarmForARunThatNeverExecutedThere` with `the prepared
  machine was priced 320.50 pull seconds and recorded "cold", want zero on a host
  holding the image whole`. That is what a queued Run cost on a machine that had
  been idle for its image the whole time it waited;
- dropping the filter that stops Mercator asking for content a host already holds
  fails the Blueprint twice, with `the ledger records 1 preparations, want the
  image, the dataset, and the withdrawn one` and `the prepared machine was priced
  320.00 seconds of Artifact read`: the desire never shrinks, so the second piece
  of content never enters the concurrency window;
- giving each dispatch a fresh operation identity, which is what a control plane
  that treated preparation as an order rather than as desired state produces,
  fails `TestARedeliveredPreparationPullsOnce` in `internal/nodeagent` with the
  redelivery accepted as new work;
- filing a fetched copy under the digest it was asked for rather than the digest
  its bytes produced fails
  `TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth` against MinIO on this
  machine with `a copy whose bytes are another version's is reported "verified"`.

The Artifact fetch conformance ran live. `docker info` answers on this host, so
MinIO was started in a container of its own daemon, an object was written and
read over presigned URLs, and the node reported the digest those bytes actually
hashed to. The operator command that reproduces it by hand is recorded in
`internal/nodeagent/artifact_live_test.go`. No issue was filed for that gap
because there is none.

The demo Blueprint's normalized bundle hash does not move. It states no
`prewarm` bounds, so its control plane prepares nothing and no preparation effect
enters its ledger.

Two limits are worth stating rather than hiding.

Withdrawal is not expressible on a node. `capability.NodeRuntime` has one command
per piece of content and no way to say stop, so `broker.Prepare` can start a pull
and cannot end one: an item the next desired set no longer names is a transfer
that runs to completion on the machine, holding room a real launch may need. The
Lab world models the withdrawal because a provider seam can, and the node's half
is [#170](https://github.com/benngarcia/mercator/issues/170).

Nothing in production implements `orchestrator.ArtifactCatalog`, so no production
Run declares an Artifact and the Artifact half of a desired set never fires end to
end. What the node does with one is held against a real object store, and what
Mercator does with one is held at L1.

### Phase 3 prewarming, the rate bound under review

On 2026-07-25, two reviewers refuted the commit that replaced the desired-set
memory's substring comparison with a set. Both were right about the same thing
twice over, and the record is corrected here because a wrong record is what a
later reader decides with.

The commit stated the consequence backwards. It said a false "already asked for"
answer would have held back a fetch nobody requested. `tooSoon` returns false
whenever `adds` is false, so a wrong `adds=false` can only ever bypass the bound:
the defect was under-throttling. Driven on this host, the pre-commit code starts a
second speculative transfer at `00:02:30` under a five minute bound whose first
fetch began at `00:01:00`, and the committed code starts it at `00:06:00`. The
commit also named the wrong content as the collision. `PrepareItem.Content()` for
an image is `domain.ReferenceDigest`, always `sha256:` and sixty four hex
characters, and no fixed-length string is a strict prefix of another, so only an
Artifact ID can collide. An Artifact ID is a name a workload declares, which is
exactly why it can.

The commit also landed with nothing that could fail on it. The whole of it reverts
and `go test ./...` stays green in every package, because every Artifact ID in the
corpus is mutually non-prefix and the one Lab assertion about `MinInterval` ran on
a fixture whose gaps the harness produced. Editing that fixture's `min_interval`
from `1m` to `0s`, which turns the bound off, left `internal/lab` and
`internal/scenario` green. `prewarming-holds-its-own-rate-bound` and
`safety.prewarm_rate_within_bound` are the repair: the fixture fails with the
substring comparison restored and fails again with the `tooSoon` call deleted
outright, both reported as `speculative preparation started at
2030-01-01T00:01:00Z and again 1m30s later at 2030-01-01T00:02:30Z, and this world
allows one no sooner than 5m0s`. The unfailable gap assertion in
`TestOnePieceOfContentIsPreparedAtATime` is deleted rather than left to look like
evidence.

One further refutation is accepted and deliberately not repaired here. The
preparation identity carries the machine and the content, and the operation store
dedupes on it with no regard for the state it is in, so a refusal is terminal: a
node whose pull failed answers `Duplicate` to every later request for that content
and the control plane never asks again. Driving `internal/node` directly, a
`PrepareImage` settled with `Applied:false` and a failure of `pull failed: registry
unreachable` is followed by an identical `PrepareImage` that reports
`Duplicate=true`, delivers nothing on the session, and leaves
`AppliedOperationIDs=[]`, so the operation is neither applied nor reissuable.
`internal/nodeagent` deliberately does not remember a failed pull so that a retry
can happen, and the layer above defeats it. The orchestrator compounds it: the
failed content is still absent from the offer's inventory, so the desired set is
recomputed identically and `unchanged` keeps it from being restated at all. This
predates the commit under review and it is a change to what an operation identity
promises, so it is owed a slice of its own: the Lab world has no way to refuse a
fetch, and a retry rule with no world that can fail it would be the same mistake
this section is correcting.

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/domain ./internal/scheduler ./internal/lab \
  ./internal/scenario ./internal/adapter/fake ./internal/orchestrator \
  ./internal/node ./internal/nodeagent ./internal/broker ./internal/httpapi \
  ./internal/daemon ./cmd/mercator -count=1
```

Run on a Linux workstation against Docker Engine 29.6.2 on the containerd
snapshotter, which is amd64 and not the arm64 macOS the earlier phase 3 slices
were built on. Nothing in this slice behaved differently there.

### Phase 3 Artifact locality at placement

On 2026-07-25, `dataset-gravity-beats-image-cache` was rewritten against the
world, run as a target Blueprint, and promoted in the same change once green.
Each claim is held by a deliberate break that fails it:

- deleting the Artifact term from the start estimate fails the Blueprint with
  `expected "rental-dataset" to win, but the decision placed on
  "rental-imagecache"`. That is the state the tree shipped in: the offer carried
  the copies and nothing read them, so a 40MB image layer decided a placement a
  40GB dataset should have;
- pricing a host that cannot enumerate its copies at zero fails it on two
  assertions, `candidate "borrowed-host": artifact_seconds: want at least 639,
  got 0` and `Artifact "artifact:imagenet:v2.41": expected "unknown", recorded
  "hot"`, and fails
  `TestAHostThatCannotEnumerateItsCopiesRecordsUnknownAndNotZero` and
  `TestNeitherModelTurnsArtifactSilenceIntoInfeasibility`. Silence costs what
  absence costs, and the decision records which of the two it was;
- reading a copy's presence rather than its verification fails
  `TestNeitherModelPricesAnUncheckedCopyAsWarmth/a_copy_nobody_checked` with
  `production priced a copy nobody checked at 0 seconds`, and fails the
  conformance case with `Artifact "artifact:stale-set:v1" was recorded
  {... Locality:hot FetchBytes:0} on the host holding only an unchecked copy of
  it` and `the decision priced 80 seconds of Artifact fetch, and 7GB crosses a
  500 Mbps link in 112s`. That is the distributed-filesystem answer arriving
  through the back door of an estimate;
- dropping the Artifact term from the Lab's reference model fails
  `TestTheReferenceModelPricesArtifactLocalityTheSameWayProductionDoes` with
  `reference priced 0 seconds of Artifact fetch, production priced 640`, and
  takes the unchecked-copy case with it. The oracle has to learn the term or it
  disagrees with the scheduler about every dataset-bearing host for a reason
  belonging to neither model;
- deleting the refusal clause of `safety.locality_is_never_infeasibility` fails
  `TestEveryDefaultInvariantHasADeliberatelyFailingCase/safety.locality_is_never
  _infeasibility`, whose synthetic decision refuses a machine with
  `IMAGE_NOT_CACHED` at `images`. Deleting the silence clause fails
  `TestSilenceIsPricedAndAMeasurementBinds` with `a candidate refused on a start
  latency predicted over content nobody could describe raised nothing`, while its
  second row holds the other direction: a measured latency for this offer still
  binds;
- recording no Artifact evidence on a candidate fails the demo Blueprint with
  `vertical proof checkpoint 7 (warmth_observed) failed`. The rule that replaced
  asked only whether the producer's output landed on the offer the consumer was
  selected on, which was green on every execution of this Blueprint before
  Artifact locality was scored anywhere.

The demo Blueprint's normalized bundle hash moved from
`sha256:d8766ff9fe41cb65c27f2ec502256dc70dd6ba2b663504e936491b6985d99ee4` to
`sha256:0b3e8a2e6388ed362e473ab3610f6fbedc12b4a44ab7ba590fcea51b320078f4`, which
is the two new decision fields entering the record, and checkpoint 14 still
reconstructs the bundle to the same hash byte for byte. Answering the review of
that commit moved it again, to
`sha256:bf75c96873f2bd51363f83357c270f5d4d6b8724eaeb174be3631bc62ddc6598`, which
is `established_start_seconds` entering the record. Answering the review of the
start-bound commit moved it to
`sha256:193d9726fcca9d51071cb6028fad5006dd06395096aafacef6765ce69015f15b`, which
is start quantiles adding rather than being scaled off the expectation.

Three limits are worth stating rather than hiding.

Nothing in production implements `orchestrator.ArtifactCatalog`, so a production
Mercator still refuses a Run that reads an Artifact at the door. What changed is
that the two simulated worlds now both answer as object stores, so the scoring
this slice adds is exercised at L0, in the placement corpus, and at L1 through
the real control plane, and none of it is exercised against a real store.

Nothing yet fetches an Artifact onto a host before a Run needs it. The estimate
prices what a candidate would have to read; controlled prewarming and
producer-consumer soft affinity are later slices, and the affinity one is what
turns this evidence into a stated preference rather than a consequence of
arithmetic.

The transfer rate is `DefaultObjectStoreDownloadMbps`, a stated assumption that
nothing measures and no offer can override. `OfferSnapshot.RegistryDownload`
reads a published p10 fact for the registry link and there is no object-store
equivalent, because no adapter, node, or provider publishes one. Every Artifact
duration therefore carries `AssumedLinkConfidence`, and phase 4 is where a
measured link replaces the constant.

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/domain ./internal/scheduler ./internal/lab \
  ./internal/scenario ./internal/adapter/fake ./internal/orchestrator \
  ./internal/httpapi ./cmd/mercator -count=1
cd web/app && bun run typecheck && bun run test && bun run build
```

### Phase 3 the objective, the start bound, and the seams review found

On 2026-07-25, the review of the commit above was answered. Each claim is held by
a deliberate break that fails it:

- ranking candidates on cost alone, which is what the tree shipped for every
  objective but balanced, fails `the-objective-decides-what-wins` with `run
  "in-a-hurry": expected "rental-warm" to win, but the decision placed on
  "rental-cold"`, and fails
  `TestTheObjectiveDecidesWhichCandidateWins/fastest_start` and
  `/fastest_completion`. That is the state the capability shipped in: three of the
  four public objectives were words nothing read;
- deleting the second ranking term, so equal prices fall back to the offer ID,
  fails `TestEqualPricesAreDecidedByWhatEachCandidateHolds`. Two machines at one
  price is the case Artifact locality exists to decide, and it is the shape of
  every fixture in this slice;
- asking the start bound of the whole prediction rather than of
  `EstablishedStartSeconds` fails `a-late-start-must-be-a-fact` through the
  invariant rather than through an assertion: `Lab invariant
  "safety.locality_is_never_infeasibility" failed: Run "run-impatient": candidate
  "silent-host" was refused for a p90 start of 1162.67s against a bound of
  180.00s, and only 1.25s of that was established`. Before that Blueprint existed,
  the same break left every Lab execution green;
- waiving the bound whenever any locality was unknown, which is what the reviewed
  commit did, fails the same Blueprint from the other side with `ten minutes of
  stated provisioning did not bust a three-minute bound: []`;
- dropping `Artifacts` from `node.Registry.offer` fails every enumerating case of
  `TestANodeOffersTheCopiesItHolds` with `the offer says enumerated = false, want
  true`. That is the state the tree shipped in: the node reported its copies and
  the offer discarded them;
- deleting the digest clause from `ArtifactInventory.Holds` fails
  `dataset-gravity-beats-image-cache` with `expected "rental-dataset" to win, but
  the decision placed on "rental-restored-snapshot"` and `Artifact
  "artifact:imagenet:v2.41": expected "cold", recorded "hot"`, and fails
  `TestANodeOffersTheCopiesItHolds/a_checked_copy_of_other_content_under_this_name
  _is_worth_nothing`. Nothing in the tree could reach that clause before, because
  no fixture could state a copy whose claim disagrees with the catalog;
- deleting the Artifact guard from `fake.Machine.publishedArtifacts` fails
  `dataset-gravity-beats-image-cache` with `candidate "borrowed-host":
  artifact_seconds: want at least 639, got 0`, and deleting the matching line from
  `lab.simulatedWorld.publishedOffers` fails
  `TestWhatABorrowedMachineHoldsIsNotSomethingMercatorKnows` with `the decision
  recorded [{... Locality:hot FetchBytes:0}] for a copy nothing of Mercator's can
  be asked about`. Both guards were unfalsifiable until a fixture could put a copy
  on capacity Mercator does not control.

Two limits are worth stating rather than hiding.

`ScoreWeights` still has four terms nothing populates, and the objective is what
decides a placement instead. That is deliberate: a reliability or uncertainty
penalty in dollars needs an exchange rate nobody has measured, and the terms are
kept as the seam calibration fills rather than deleted, because phase 4 is where a
measurement replaces the assumption. Nothing in the tree reads them today, so a
reader should treat a weighted score as arithmetic over one live term.

`safety.artifact_replica_verified` no longer checks the digest of a copy the World
Tape seeded, only of copies this world delivered. A seeded claim that disagrees
with the catalog is a fact about that machine's own bookkeeping, and it has to be
expressible for the subtraction that catches it to be reachable. What a Lab
Blueprint therefore cannot yet state is a copy whose claim went stale after the
world began.

### Phase 3 mutable caches

On 2026-07-25, `cache-mounts-never-cross-a-workspace` was written against the
world, driven as a target Blueprint, and promoted in the same change once green.
Each claim is held by a deliberate break that fails it:

- dropping the workspace from `domain.CacheIdentity`, which is the identity the
  world keys caches by, fails the execution through the invariant:
  `safety.cache_mount_workspace_isolation failed: cache
  "compiler-cache/cuda-12.4" on "shared-builder" is used by workspaces
  "ws_lab_alpha" and "ws_lab_beta", and a cache belongs to one workspace`. Under
  the rule stated as an identity derivation rather than as a collision, that same
  break left every Lab execution green, because the rule and the world derived
  identities with one function;
- dropping the workspace from `domain.CacheVolumeName` fails
  `TestTwoWorkspacesGetTwoVolumesForOneCacheName` against the local Docker daemon
  with `two workspaces naming one cache were attached to the same volume
  "mercator-cache-compiler-cache-ab651da8"`, read back with `docker inspect` on
  the running containers rather than off the arguments this code built;
- ignoring the compatibility key in `CacheInventory.Holds` fails the Blueprint
  with `the decision recorded "hot" for compiler-cache: the application declared a
  new generation, so what is under the name is not usable`, and fails
  `TestCacheWarmthAnswersPerWorkspaceAndGeneration`;
- dropping `CacheMounts` from the launch request the orchestrator builds, which is
  the state the tree shipped in, fails the Blueprint with `the decision recorded
  "cold" for compiler-cache: the tenant that filled its own cache finds it on the
  machine that holds it` and `the ledger records 0 cache accesses`. Nothing
  carried a declared cache to a host, so nothing was ever attached or written;
- dropping `Caches` from `node.Registry.offer` fails
  `TestANodeOffersTheCachesItHoldsUnderTheWorkspaceThatOwnsThem` with `the offer
  carries {Known:false ...}, and this node reported two tenants' caches`;
- deleting the cache clause of `onlyKeptCapacityHoldsWhatItRan` fails
  `TestLocalityProvenanceRejectsBorrowedCapacityHoldingACache`. No execution can
  reach that state, because the world refuses to write a cache onto capacity that
  keeps nothing, so the forbidden world is stated directly.

The demo Blueprint's normalized bundle hash moved from
`sha256:193d9726fcca9d51071cb6028fad5006dd06395096aafacef6765ce69015f15b` to
`sha256:4ba5346629a82ae964897806814673a6f005106fe22c0e7d20e2184c8fd29203`, which
is cache evidence entering the decision record and the demo's own Cache Mount
gaining a generation and a declared size.

Two limits are worth stating rather than hiding.

No production Mercator can declare a cache without going through the workload
spec: `caches` is on the public `WorkloadSpec`, so the API carries it, and there
is no CLI or quickstart step that writes one. What a real operator gets today is
a node that will attach whatever the control plane asks for.

Nothing reclaims a superseded generation. A new compatibility key leaves the
previous volume on the disk, and `NodeSupport.GarbageCollection` is still false,
so the machine accumulates one volume per generation until an operator prunes it.
That is the slice that earns garbage collection, and it is tracked rather than
papered over by mounting incompatible content.

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/domain ./internal/scheduler ./internal/lab \
  ./internal/scenario ./internal/adapter/fake ./internal/orchestrator \
  ./internal/node ./internal/nodeagent ./internal/broker ./internal/httpapi \
  ./internal/daemon ./cmd/mercator -count=1
cd web/app && bun run typecheck && bun run test && bun run build
```

### Phase 3 the review of the cache commit

On 2026-07-25, the review of the commit above was answered. Each claim is held
by a deliberate break that fails it:

- opening the cache volume before dispatching the run, which is what the reviewed
  commit did, fails `TestALaunchThatNeverRunsLeavesNoCacheBehind` against the
  daemon on this machine with `the failed launch left volume
  "mercator-cache-ws_alpha-never-run-cache-ecdfda61" on this machine`. That is
  the state the tree shipped in: a launch that died at image resolution left a
  cache the node then advertised holding;
- failing the whole facts report over one un-inspectable volume, which is what the
  tree shipped, fails `TestOneUnreadableCacheVolumeDoesNotCostTheNodeItsReport`
  with `one pruned volume cost this node its whole facts report: ... no such
  volume`. The daemon had already printed the other cache on stdout;
- reporting a cache read this node could not make as an empty enumeration fails
  `TestANodeThatCannotReadItsCachesSaysNothing` with `a node that could not read
  its caches claims it enumerated them: {Known:true ... Mounts:[]}`;
- dropping `CacheMounts` from `broker.launchOnNode`, which nothing in the tree
  could catch before, fails `TestANodeIsAskedToAttachTheCachesTheWorkloadDeclared`
  with `the node was asked to attach [], and the workload declared
  {Name:compiler-cache CompatibilityKey:cuda-12.4 SizeBytes:8589934592}`;
- resolving a cache read to any storage of the same name on the host, which is a
  literal cross-workspace read of mutable state, fails
  `cache-mounts-never-cross-a-workspace` through the invariant:
  `safety.cache_mount_workspace_isolation failed: cache
  "ws_lab_alpha/compiler-cache/cuda-12.4" on "shared-builder" is used by
  workspaces "ws_lab_alpha" and "ws_lab_beta"`. Under the rule as the reviewed
  commit wrote it, reading only what each execution asked for, that same break
  left every Lab execution green, which is the whole reason the consequence now
  carries the slot;
- opening no cache when a workload runs at L0, which is the state the reviewed
  commit shipped, fails `running-fills-a-cache` with `run "second": candidate
  "builder-a": cache "build-cache": expected "hot", recorded "cold"`.

The demo Blueprint's normalized bundle hash does not move: `mercator lab run
--blueprint internal/scenario/scenarios/demos/artifact-warmth-restart.json`
answers `sha256:02af6ff3438a3e7a4cabd9c24ad88b3192bc653cccc714e6c004fecc63e1aa3f`
both before and after this change, because a cache read mutates nothing and the
normalized bundle carries only the effects that do. That command answers
`sha256:ead5057209fb7930d0d76568ebdd57575169b6001553b5364f28063c74c0321a` on the
commit before the cache slice, so the pair recorded above is not what this tool
produces and should be read as a claim that the hash moved rather than as either
value.

Two claims in this section were falsified by the review answered below, and the
entry there is what stands: the reached-slot clause could not fail and is gone,
and a container that was created and then failed to start is no longer reported
as a cache this machine holds.

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/domain ./internal/scheduler ./internal/lab \
  ./internal/scenario ./internal/adapter/fake ./internal/orchestrator \
  ./internal/node ./internal/nodeagent ./internal/broker ./internal/httpapi \
  ./internal/daemon ./cmd/mercator -count=1
cd web/app && bun run typecheck && bun run test && bun run build
```

### Phase 3 what a reported cache establishes

On 2026-07-25, the second review of the cache commit was answered. Each claim is
held by a deliberate break that fails it:

- reporting every labelled volume, which is what the reviewed commit shipped,
  fails `TestAContainerThatNeverStartsIsNotACacheThisNodeHolds` against the daemon
  on this machine with `the node reports a cache whose container never started:
  [{WorkspaceID:ws_alpha Name:never-started-cache ...}]`, and fails
  `TestANodeReportsOnlyTheCachesAWorkloadRanAgainst` on a scripted daemon holding
  one volume of each kind. The case is real on Docker 29.4.0: `docker run
  --detach --entrypoint /definitely-not-here --mount type=volume,...,
  volume-label=mercator.cache.name=build-cache busybox` exits 127, leaves the
  container in state `created`, and leaves the volume with all three labels;
- attaching a cache when the workload exits rather than when its container is
  created, which is what the Lab did, fails
  `cache-mounts-never-cross-a-workspace` through
  `TestACacheIsWarmOnlyForTheWorkspaceAndGenerationThatOwnsIt` with `the decision
  recorded "cold" for compiler-cache: a workload of this tenant and generation is
  attached to that cache right now`;
- pricing a hot cache at half a cent, or at thirty seconds off the start estimate,
  fails `running-fills-a-cache` with `run "second": expected "builder-b" to win,
  but the decision placed on "builder-a"`. Under the fixture as the reviewed
  commit wrote it, the second placement was decided by the warm machine being busy
  and both mutations left the whole corpus green;
- opening no cache when a workload runs at L0 still fails `running-fills-a-cache`
  with `run "second": candidate "builder-a": cache "build-cache": expected "hot",
  recorded "cold"`;
- dropping the workspace from `domain.CacheIdentity`, which is the derivation
  error the deleted reached-slot clause claimed to catch, fails
  `cache-mounts-never-cross-a-workspace` through the invariant:
  `safety.cache_mount_workspace_isolation failed: cache "compiler-cache/cuda-12.4"
  on "shared-builder" is used by workspaces "ws_lab_alpha" and "ws_lab_beta"`.
  Deleting `reached_identity` from the world, by contrast, left the entire tree
  green, which is why that clause is gone rather than restated.

The demo Blueprint's normalized bundle hash moved from
`sha256:02af6ff3438a3e7a4cabd9c24ad88b3192bc653cccc714e6c004fecc63e1aa3f` to
`sha256:8e266fa97355678213d3e29105f76a77444aee285b7beb6883b7282a3027cdf3`, both
read from `mercator lab run --blueprint
internal/scenario/scenarios/demos/artifact-warmth-restart.json --bundle <path>`.
Attaching a cache mutates the world and happens at a different moment than
writing one did, and the normalized bundle carries the effects that mutate.

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/domain ./internal/scheduler ./internal/lab \
  ./internal/scenario ./internal/adapter/fake ./internal/orchestrator \
  ./internal/node ./internal/nodeagent ./internal/broker ./internal/httpapi \
  ./internal/daemon ./cmd/mercator -count=1
cd web/app && bun run typecheck && bun run test && bun run build
```

### Phase 3 what the start bound may strike out

On 2026-07-25, the review of the start-bound commit was answered. Each claim is
held by a deliberate break that fails it:

- deriving an Artifact inventory's `Known` from the node's heartbeat, which is
  what the reviewed commit shipped, fails
  `TestANodeThatCannotEnumerateCopiesOffersNoArtifactClaim` with `the offer claims
  this node enumerated its Artifact copies` against the production daemon, and
  fails `TestANodeOffersTheCopiesItHolds/a_node_whose_runtime_does_not_enumerate
  _copies_says_nothing`. No runtime in this tree enumerates a copy, so that claim
  refused the only reusable lane there is for content it never looked for;
- reading `Provisioning.Expected` for the p90, which is what the tree shipped,
  fails `TestAStartBoundIsAskedOfThePublishedProvisioningTail` with `the decision
  recorded a provisioning p90 of 60, and the provider published 600`, and fails
  `a-late-start-must-be-a-fact` with `the decision recorded a provisioning p90 of
  600.00s, and this offer published 1080`;
- summing declared runtimes for a Rental Schedule's wait, which is what the tree
  shipped, fails `TestAQueueThatIsNearlyDoneIsAShortWait` with `the decision
  projected 3600 seconds of waiting for a Booking a minute from its own expected
  finish`, and fails `a-queue-drains-as-it-runs` at L1 with `advance Lab Run
  "impatient": orchestrator: no feasible offers`. The only Rental in that fleet is
  a minute from free;
- counting an image silence as established, by deleting the unknown-locality guard
  in `pullEstimate`, fails `a-late-start-must-be-a-fact` through the invariant:
  `safety.locality_is_never_infeasibility failed: Run "run-impatient": candidate
  "silent-host" was refused against a 180.00s bound having been charged 929.14s
  for content nobody could describe, of which only 640.00s was left out of the
  established start`. Under the rule as the reviewed commit wrote it, that same
  break left every Lab execution green;
- counting an Artifact silence as established, by deleting the locality filter in
  `establishedFetchBytes`, fails the same Blueprint through the same rule with
  `only 289.14s was left out of the established start`;
- deleting the Artifact clause of `onlyKeptCapacityHoldsWhatItRan` fails
  `TestLocalityProvenanceRejectsBorrowedCapacityThatKeptACopy`. Before that
  observation existed the whole loop could be replaced with `_ = seededCopies` and
  `go test ./...` stayed green, because `keepReplica` will not write a copy onto
  capacity that keeps nothing and the seed is exempt.

One limit is worth stating rather than hiding. `HostFacts.Network` is populated by
nothing in `internal/nodeagent`, so every node offer prices its transfers over
`DefaultRegistryDownloadMbps` and every Artifact read over
`DefaultObjectStoreDownloadMbps`. A host that enumerated and holds no copy is
therefore struck out of a tight start bound on an assumed rate, which is correct
as a contract with the caller and wrong as a prediction about a fast link. What
fixes it is a measurement, which is phase 4 and phase 6, and not waiving the bound.

### Phase 3 Artifact durability

On 2026-07-25, `artifact-must-be-durable-before-a-consumer-runs` was written
against the world and driven at L1 by four cases in `internal/lab`. Each claim
is held by a deliberate break that fails it:

- deleting the durability gate from `Orchestrator.step` fails ten Lab cases at
  once, starting with `safety.artifact_dependencies: Run
  "run-checkpoint-consumer" launched at effect 26 before Artifact
  "artifact:checkpoint:v1" was durable`, and taking the demo Blueprint's bundle,
  restart, and lost-response cases with it. That is the whole rule: the gate
  lives in Mercator, and removing it is visible from the Lab;
- gating admission on presence on some machine instead of on the object store
  fails the same cases the same way. That is the predicate the Lab shipped with,
  and the invariant catches it before any assertion in a test runs;
- storing a producer's output from `Observe` at `world.now`, which is the state
  the reviewed commit shipped in, fails
  `TestWhenAnArtifactBecameDurableDoesNotDependOnPolling` with `the checkpoint
  became durable at 2030-01-01 00:17:40 when Mercator looked every minute and at
  0001-01-01 00:00:00 when it looked every ten`. Two executions of one Blueprint
  are driven at cadences ten minutes apart and both World Truth stamps agree;
- publishing a producer's output the instant it is written fails
  `TestAConsumerWaitsForDurabilityAndNotForACopy` with `the checkpoint was
  written locally at 2030-01-01 00:15:00 and durable 0s later, and 10GB crosses
  a 500 Mbps link in 160s`. The gap is asserted exactly rather than as a lower
  bound, because settling on the world's clock makes it exact;
- withholding the Run instead of holding it, which is what the harness gate did,
  fails `TestARunHeldByAdmissionIsVisible` with `Run "run-checkpoint-consumer"
  is in none of Mercator's records`;
- ending an execution as soon as the world owes nothing fails
  `TestAPublicationThatNeverLandsIsNotAGreenExecution` with `driving a Blueprint
  whose publication never lands gave <nil>, and its consumer never ran`. That
  Blueprint rejects the producer's only launch, so nothing publishes and nothing
  is left in flight; `DriveToCompletion` reaches the liveness bound anyway and
  comes back with `liveness.admitted_run_progress`, which is what makes a
  publication that never lands unable to produce a green execution;
- deleting the per-host Artifact clause from `safety.locality_provenance` fails
  `TestLocalityProvenanceCoversArtifactCopiesToo` with `a host reported holding
  a copy of an Artifact nothing delivered to it and nothing objected`;
- letting `safety.artifact_dependencies` read an unrecorded workload as a Run
  that consumes nothing fails
  `TestArtifactDependenciesRefusesALaunchWithNoRecordedWorkload`;
- migrating a legacy named cache as a verified copy fails
  `TestLegacyPresenceMigratesAsAnUncheckedCopy` with `the migrated copy is
  "verified", and the fixture only ever said the machine has it`;
- letting a retired Rental keep its Artifact copies fails
  `TestAConsumerRunsWhenTheOnlyCopyIsGone` with `the Rental holding the only
  copy is still here`. Losing every copy is what the second claim is about, and
  without the retirement there is nothing to lose;
- letting a Run read an unverified copy fails every case in the Blueprint
  through `safety.artifact_replica_verified: Run "run-reference-consumer" read
  Artifact "artifact:stale-set:v1" from a "unverified" copy on offer
  "producer-rental", which nothing checked against the catalog`. The fixture
  seeds that unchecked copy on the host the Run lands on, which is what gives
  the rule something to catch;
- dropping the Artifact declarations from `scenario.WorkloadForRun` fails
  `TestARunsRecordedWorkloadCarriesItsArtifacts` with `the producer's recorded
  workload publishes []`, and fails the durability case with `the consumer
  entered Mercator at 2030-01-01 00:00:00, and its input became durable at
  00:18:00`. That is the state the tree shipped in: the declarations never
  reached the control plane, so nothing could wait for anything;
- letting capacity that keeps nothing keep an Artifact copy fails
  `safety.locality_provenance` on the generated Blueprint with `offer
  "market-generated-001" is a machine that does not exist yet, and holds a copy
  of Artifact "artifact:generated:001:v1"`.

Five limits are worth stating rather than hiding.

Nothing in production implements `orchestrator.ArtifactCatalog`. There is no
object-store client, no artifact controller, and no node-side fetch, so a
production Mercator refuses a Run that reads an Artifact rather than guessing.
The refusal is answered where the Run is submitted, as `400
ARTIFACT_CATALOG_UNAVAILABLE` naming the version nothing can establish, because
there is no later moment at which this deployment could answer differently.
That refusal is the honest state: no production path publishes a version either,
so no production Run has an Artifact it could legitimately read.

That paragraph also said Placement scores nothing from `OfferSnapshot.Artifacts`,
and that the two `dataset-gravity` Blueprints report
`ARTIFACT_CATALOG_UNAVAILABLE` at the step that submits the Run. Both were true
when it was written and are superseded by the placement slice above: the fake
world is an object store now, and the evidence reaches the score through the
transfer estimate.

Blueprints still cannot express two workspaces, so `ArtifactSpec` states no
workspace and the corpus cannot state that one workspace's Artifact is
invisible to another. The catalog entry carries the scope, and
`ArtifactVersion` answers nothing for a workspace that is not the Lab's, which
is a unit-level fact rather than a corpus one.

Nothing walks the conformance Blueprints through `mercator lab run`, and three
of the four are red from the CLI while their L1 cases pass. `DriveToCompletion`
begins by quiescing the World Tape, which jumps virtual time to the last arrival
before Mercator observes anything, so a Run that finished at 15m is half an hour
stale the first time it is looked at and `liveness.stale_lease_expiry` fires.
That predates this slice and reproduces on the commit before it. Advancing to
the nearest deadline the world owes rather than to the last event changes the
drive record of every exported bundle, so it is its own slice, tracked as
[#169](https://github.com/benngarcia/mercator/issues/169).

Two review findings were rejected rather than fixed, and both are recorded as
issues rather than as silence.

A reviewer held that two clauses of `safety.artifact_replica_verified` are
unreachable, because the simulated world cannot produce a copy of content the
catalog cannot name or a copy claiming a divergent digest: `keepReplica` refuses
an unknown Artifact and `replicaOf` stamps the catalog's own digest. That is
true and it is how every safety rule in this registry works. A standing rule is
a law about states the system must never reach, and the tree's mechanism for
proving one can fail is
`TestEveryDefaultInvariantHasADeliberatelyFailingCase`, which drives a
synthetic observation; `safety.lease_fencing`'s "active launch has no ownership
token" clause is unreachable by fixture for the same reason. Making the world
able to produce a corrupt or truncated copy is a fault vocabulary, tracked as
[#168](https://github.com/benngarcia/mercator/issues/168) along with the related
gap that no Blueprint can state an object-store read that fails, is partial, or
is slow.

A reviewer held that `domain.ArtifactReplicaState` should carry `unknown`,
because `capability.ArtifactLocality` did. The answer went the other way: a
replica record asserts that these bytes are on this machine, and a node that
could not establish what it holds has nothing to assert, which is what
`ArtifactInventory.Known` is for. What was wrong was the node contract carrying
`Verified bool` beside `State domain.LocalityState`, and that type is deleted.
`ArtifactInventory.Known` is still written `true` unconditionally by both
simulated worlds, exactly as `ImageInventory.Known` is, and no Blueprint field
can yet state a machine that holds copies and cannot enumerate them. That is a
world-model capability rather than a fixture, tracked with the image half in
[#167](https://github.com/benngarcia/mercator/issues/167).

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/domain ./internal/lab ./internal/scenario \
  ./internal/adapter/fake ./internal/orchestrator ./internal/httpapi \
  ./internal/scheduler ./cmd/mercator -count=1
cd web/app && bun run typecheck && bun run test && bun run build
```

### Phase 3 exact node reporting

On 2026-07-25, `unpacked-is-not-the-same-as-pulled` was written against the
world and promoted in the same change once green. Each claim it makes is held by
a deliberate break that fails it:

- reporting a content-store image as runnable on the strength of its content
  being present fails `TestDockerRuntimeSeparatesWhatItUnpackedFromWhatItPulled`
  with `an image whose bytes are here and whose chain is not built was reported
  ... ContentPresent:true State:hot, want partial with no mountable layer`;
- reading a graph driver's images as anything but assembled fails
  `TestEveryImageAGraphDriverDaemonListsIsRunnable` and, against the daemon on
  this machine, `TestEveryImageThisDaemonHoldsIsAssembled` on every image it
  holds: `was reported "cold" ... and this daemon can run every image it lists`;
- filing an image as pulled on a state rather than on stated content presence
  fails `TestANodeOffersTheContentItActuallyHolds` with `pulled and not
  assembled = true, want false` for the host missing part of an image, which is
  the case that would have been charged local assembly for bytes nobody fetched;
- projecting a whole-image identity without requiring assembly, and dropping
  the pulled projection, fails `TestANodeOffersTheContentItActuallyHolds` on both
  new cases with `pulled and not assembled = false, want true`, and fails the
  daemon case with `the node never reported the image it fetched and never
  assembled`;
- pricing content a host pulled and never assembled at nothing fails the
  Blueprint on four assertions at once, starting with `expected "ready-host" to
  win, but the decision placed on "pulled-host"` and including `image_locality:
  want "partial", got "hot"`, and fails
  `TestWhatANodeHoldsDecidesWhatItStillHasToDo` on the two assembly cases;
- pricing a host that cannot enumerate itself at zero seconds fails three green
  Blueprints, starting with `candidate "silent-host": pull_seconds: want at
  least 285, got 0` and `pull_confidence: want 0.5, got 0`, and taking
  `unenrolled-host-holds-nothing` and `ephemeral-execution-is-never-a-rental` with
  it, because borrowed capacity is exactly the capacity nobody can enumerate. It
  also fails `TestWhatANodeHoldsDecidesWhatItStillHasToDo` on the silent host;
- letting a simulated world answer for capacity Mercator does not control fails
  the Blueprint with `pull_source: want "inventory_unknown", got
  "image_inventory"` and `image_locality: want "unknown", got "hot"`, which is
  the world lending Placement knowledge no deployment has;
- restoring any of the four unearned declarations fails
  `TestANodeDeclaresOnlyWhatItsRuntimePerforms` with `the node declares
  artifact_replicas, and nothing on the machine performs it`;
- dropping the assembly term from the Lab's reference model fails
  `TestTheReferenceModelPricesAssemblyTheSameWayProductionDoes` with `reference
  priced 0.5 seconds of image work, production priced 72.82`. That case exists
  because the generated oracle worlds hold no unassembled content, so without it
  the reference model's new term was driven by nothing;
- dropping unassembled content from `heldDigests` fails
  `TestLocalityProvenanceCoversContentAHostFetchedAndNeverAssembled` with `a host
  reported holding unassembled content nothing delivered and nothing objected`.

Against the Docker 29.4.0 daemon on this machine, over the OrbStack VM's
overlay2 store:

- `TestDockerRuntimeReportsTheLayersItUnpacked` asserts the reported diff IDs
  match `docker image inspect --format '{{json .RootFS.Layers}}'` exactly, the
  reported platform matches the build the daemon holds, and the image comes back
  hot with its content present;
- `TestEveryImageThisDaemonHoldsIsAssembled` is the rule's other half. Every
  image this daemon lists is one it can run, so every one must come back hot: a
  readiness test that called a working host partial would price local assembly
  nobody owes, and one that called it cold would send its own work to a machine
  that has to fetch what this one is sitting on.

Three limits are worth stating rather than hiding.

The content-store arm has no live daemon behind it here, and now has one
elsewhere. On 2026-07-25 the whole suite ran on an amd64 Linux workstation whose
Docker Engine 29.6.2 keeps its images in the containerd content store
(`driver-type: io.containerd.snapshotter.v1`), which is the arm this paragraph
says nothing had exercised: `openImageStore` selects `contentStore` there, and
`TestEveryImageThisDaemonHoldsIsAssembled` and
`TestDockerRuntimeReportsTheLayersItUnpacked` pass against it. The limit below
describes the machine the arm was written on, which runs the
overlay2 graph driver, where standing up a daemon on the containerd image store
needs a privileged container that environment refuses, so that arm is held by
moby's source and by a scripted daemon whose API answers the shapes moby
documents (`api/types/image/manifest.go`). What was verified live is the graph
driver arm, on every image this machine holds, plus the two facts the
content-store arm turns on: `docker info --format '{{json .DriverStatus}}'`
reports the graph driver's own status here and reports `driver-type` as
`io.containerd.snapshotter.v1` under the content store, and `docker image
inspect --format '{{json .Manifests}}'` returns `null` on this daemon because
the CLI never requests manifests, which is why the agent asks the API instead.
The naming defect that arm shipped with is the cost of that limit: no test and
no live daemon covered it, and it took a reading of `singlePlatformImage` to
find that this store prints a name no Run is ever pinned to.

The agent takes no assembly evidence from the daemon's storage root. A node
agent may run beside a daemon whose filesystem it cannot see, which is true of
every Docker Desktop and OrbStack install, so statting storage paths would
report every image on such a host as unassembled. The disk fact does statfs that
root, and it says nothing about images: a machine whose room the agent cannot
measure reports no disk fact at all, and its inventory is unaffected.

Neither simulated world models the time assembly takes. A fixture's `unpacked`
flag says what a host reports, and the world's own transfer model is unchanged,
so a Run placed on a half-assembled host in the Lab would start as soon as its
bytes were there. `unpacked-is-not-the-same-as-pulled` is a placement fixture
for that reason: it asserts the Booking Decision and does not run the workload.

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/domain ./internal/capability ./internal/scheduler \
  ./internal/lab ./internal/scenario ./internal/adapter/fake ./internal/node \
  ./internal/nodeagent ./internal/daemon ./internal/ociresolver \
  ./internal/httpapi -count=1
cd web/app && bun run typecheck && bun run test && bun run build
```

### Phase 3 registry manifest resolution

On 2026-07-24, `registry-manifest-bridges-digest-spaces` was written against the
world, run as a target Blueprint, and promoted in the same change once green.
Each claim it makes is held by a deliberate break that fails it:

- deleting the diff-ID clause from `domain.ImageInventory.HoldsLayer` fails it
  with `run "resolved": candidate "docker-host": pull_seconds: want exactly 0,
  got 389.3` and the Docker host losing to the cheaper cold one. This is the
  bridge itself: nothing else in the tree can tell that a compressed blob digest
  and an uncompressed diff ID name the same bytes;
- making `pullEstimate` claim confidence in an unknown answer fails its second
  step, `pull_source: want "registry_unauthorized", got "unknown"` for both
  candidates, so the fixture really is what keeps silence from becoming warmth;
- letting `pullEstimate` claim full confidence in a duration derived from the
  unmeasured 500 Mbps assumption fails it on the cold candidate, and fails
  `TestPlacementPricesAWarmNodeFromTheResolvedManifest` with `pull confidence =
  1, want 0.5: the bytes are counted and the link they cross is assumed`;
- removing `orchestrator.WithImageManifests` from `internal/daemon/runtime.go`
  fails `TestPlacementPricesAWarmNodeFromTheResolvedManifest`. That is exactly
  the state the production daemon shipped in: no manifest source, so every
  candidate looked alike on locality however warm it was;
- dropping the diff-ID projection from `internal/node/offers.go` fails the same
  test with `the node never reported the layers it unpacked`;
- returning no diff IDs from the resolver fails the network-gated conformance
  case with `layer 0 diff ID = , the daemon reports sha256:66cb17ea...`;
- naming the resolved manifest after the platform manifest instead of the pinned
  digest fails the conformance case with `resolved digest =
  sha256:e0e8b3cb..., the daemon holds this image under sha256:fd8d9aa6...: no
  host could ever be recognised as holding it whole`, and fails
  `TestResolverStatesEveryLayerInBothDigestSpaces`;
- restoring the whole reference into `LaunchWorkloadCommand.ManifestDigest`
  fails all three node-placement warmth cases in `internal/daemon`, starting with
  `the node never reported holding the image it ran`;
- reporting an image the daemon would not describe as hot with no layers fails
  `TestDockerRuntimeSaysWhichImageItCannotDescribe`, and failing the whole report
  over it fails the same case with `read node facts: read image sha256:gone`;
- deleting any clause of `CheckWarmingDoesNotShrinkInventory` fails
  `TestSchedulingMetamorphisms/warming_that_loses_content`. Deleting the `Known`
  clause fails it with `a host that stopped enumerating its content was reported
  as lawfully warmed`, which it did not do until the transform stopped emptying
  the inventory it was supposed to leave intact.

The second review round is held by these breaks:

- deleting the `registry_throttled` and `registry_unreachable` arms from
  `ociresolver.Unreadable` fails `registry-silence-has-a-name` on all four
  candidates with `pull_source: want "registry_throttled", got
  "registry_unreadable"`. The sentinels themselves are held against real
  responses by `TestRegistryRefusalsStayDistinguishable/a_registry_rate_limiting
  _this_client`, `TestARegistryNothingCanReachIsItsOwnAnswer`, and
  `TestARegistryThatAnswersNothingInTimeIsUnreachable`;
- deleting the platform term from `node.imageInventory` fails
  `TestPlacementChargesTheWholePullForAnotherPlatformsBuild` with `the node
  reports holding an image whole on the strength of a name it shares with another
  platform's build`, and fails the node's own projection case with `holds the
  whole image = true, want false`. Not populating `ImageLocality.Platform` in the
  Docker runtime fails the conformance case with `reported platform = , the
  daemon holds the linux/arm64 build`;
- reading a throughput fact's existence as a measurement fails
  `TestARegistryLinkIsWorthWhatItsPublisherSaid` with `registry link = {Mbps:250
  Confidence:1}, want {Mbps:250 Confidence:0.9}`, and ignoring its expiry adds
  `{Mbps:250 Confidence:1}, want {Mbps:500 Confidence:0.5}`. Restoring the
  Docker adapter's literal 100 Mbps fact fails
  `TestStandingOfferPublishesNoThroughputNothingMeasured`;
- naming the resolved manifest after anything but the pinned digest leaves
  `TestPlacementChargesNothingForAnImageTheNodeAlreadyHolds` green, which is why
  that case no longer claims to hold digest identity. It fails
  `TestResolverStatesEveryLayerInBothDigestSpaces` and the Docker conformance
  case; the machine half is held by
  `TestANodeHoldsTheImageItRan`, which fails when
  `broker.launchOnNode` stamps the whole reference again.

Three conformance cases run against registries and daemons that are real rather
than simulated, and skip rather than fail when the machine has no network or no
Docker daemon:

- `TestRegistryResolverAgreesWithDockerAboutAPublicImage` resolves
  `docker.io/library/busybox` through anonymous registry token auth. It asserts
  the resolved digest is the one the daemon holds the image under, which is the
  index digest and not the platform manifest below it, and that the layer blob
  digests and compressed sizes match `docker manifest inspect` of the platform
  child read back through the index, and the diff IDs match `docker image
  inspect --format '{{json .RootFS.Layers}}'`;
- `TestRegistryResolverAuthenticatesAgainstAPrivateRegistry` starts `registry:2`
  behind the committed htpasswd fixture through the local Docker daemon, pushes
  an image into it, and resolves it with credentials. The same read without
  credentials must fail `ErrUnauthorized`, because otherwise the credentials
  could have been doing nothing;
- `TestDockerRuntimeReportsTheLayersItUnpacked` reads node facts from the real
  Docker daemon and asserts the agent reports exactly the diff IDs the daemon
  holds, under the digest `docker images --digests` names it by, and for the
  build `docker image inspect` says it holds. Together with the case above, that
  is the whole identity agreement: the resolver and the agent are each checked
  against Docker rather than against each other.

The production path is driven end to end in `internal/daemon`, against a
registry the test serves over the real registry v2 protocol on loopback and a
node speaking the real node protocol:

- `TestPlacementPricesAWarmNodeFromTheResolvedManifest` runs one version of a
  multi-platform image and then places the next version, which shares the 18GB
  base layer and nothing else. The host holds no version of the new image, so
  the whole-image shortcut cannot fire and the only way to see the warmth is to
  recognise the manifest's compressed blob digests as the diff IDs the daemon
  unpacked. It asserts only the rebuilt layer is charged, at
  `AssumedLinkConfidence`;
- `TestPlacementChargesNothingForAnImageTheNodeAlreadyHolds` is the other half:
  a node that ran an image is charged zero seconds at full confidence to run it
  again. It is the end-to-end statement and not the proof of digest identity,
  because that host holds every layer as well as the image, so the layer
  subtraction reaches zero too. Identity is held by
  `TestResolverStatesEveryLayerInBothDigestSpaces` on the registry side and
  `TestANodeHoldsTheImageItRan` on the machine side, each against Docker in the
  conformance cases;
- `TestPlacementChargesAssemblyForAnImageTheNodeHasNotUnpacked` is the machine
  half of "unpacked is not the same as pulled" through the production daemon:
  every byte of the image is on the node, no container can start on it, and the
  decision records partial and charges the assembly rather than an instant start
  or a pull of content already on the disk;
- `TestPlacementChargesTheWholePullForAnotherPlatformsBuild` is where holding an
  image whole stops being a question about a name: an operator pulled the arm64
  build by hand, the machine reports exactly the digest the amd64 Run is pinned
  to, and the decision charges the whole 18.04GB rather than nothing;
- `TestPlacementBoundsWhatItWaitsForAManifest` and
  `TestPlacementRecordsWhyAManifestCouldNotBeRead` pin the two halves of a
  registry that will not answer: Placement never hands one an unbounded context,
  and each refusal reaches the decision under its own name.

Five limits are worth stating rather than hiding.

Every registry refusal is still priced as the same uncertainty, because a host
may hold an image nothing can describe. What differs now is the record: the
estimate's source names which of the five refusals it was, and the corpus asserts
that name. A refusal the resolver never classified stays `registry_unreadable`,
which is the honest answer for a status nothing has taught it to read.

Nothing in production publishes a measured registry throughput, so every transfer
duration today carries `AssumedLinkConfidence`. The measured branch is exercised
only by the Lab, where a Blueprint's declared path is a world fact. That is the
state phase 4 changes, and it is better than the literal it replaced: an adapter
was stamping full confidence on a number nobody measured.

`selectPlatform` matches an index entry on OS and architecture alone, because
`domain.Platform` has no variant, so an image published for `arm/v5` and
`arm/v7` resolves to whichever the index lists first.

`domain.AssumedLinkConfidence` is 0.5, which is a stated placeholder rather than
a measurement. Nothing measures a host's registry throughput yet, so the only
honest properties available today are that a transfer of no bytes is certain and
a transfer over an unmeasured link is not. Phase 4 replaces the number with a
distribution and is where the `P90 = 1.5 × P50` spread stops being arbitrary too.

The simulated registries answer with the digest a reference pins, exactly as the
real one does, and both simulated worlds record held images under the same name.
Neither models a pull that fails, is throttled, or half-completes, so a Blueprint
still cannot express a node whose pull is refused after Placement priced it.

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/ociresolver ./internal/domain ./internal/scheduler \
  ./internal/lab ./internal/scenario ./internal/adapter/fake \
  ./internal/adapter/docker ./internal/node ./internal/nodeagent \
  ./internal/daemon ./internal/orchestrator ./internal/broker \
  ./internal/capability -count=1
cd web/app && bun run typecheck && bun run test && bun run build
```

Run with Docker and the network available, so no conformance case skipped.

### Phase 3 warming

On 2026-07-24, both new Blueprints were red before the world changed, each on
exactly one assertion (`pull_seconds: want exactly 0, got 289.14`), and green
after, at which point `TestPlacementScenarios` failed them for passing and they
were promoted in the same commit.

Two independent reviews then refuted parts of that commit, and two more refuted
parts of the correction. What survived is stated below with the deliberate break
that holds it.

- changing `running-warms-the-host`'s advance from `15m` to `1s` fails it with
  `pull_seconds: want exactly 0, got 289.14`. The host holds the image when its
  bytes have arrived, not when the container was dispatched;
- deleting the `KeepsWhatItRuns` guard from the fake world's pull fails
  `TestWorldCapacityItDoesNotKeepHoldsNothingItRan` with `a host Mercator has not
  enrolled held ... LayerDigests:[layer-base layer-top] an hour after its
  workload`, and the same guard in the Lab world fails
  `safety.locality_provenance` in `TestABorrowedSlotIsPricedTheWholePullEveryTime`
  with `offer "local-docker" holds nothing once its workload exits, and holds
  sha256:5d7e... beyond what the World Tape seeded`. Neither break is visible in
  an offer: capacity Mercator runs nothing on publishes no inventory at all, so
  retention is asserted where it happens, on the machine and on World Truth.
  Deleting only the predicate's lane term fails `unenrolled-host-holds-nothing`
  with `borrowed-second: pull_seconds: want at least 200, got 0`,
  `TestABorrowedSlotIsPricedTheWholePullEveryTime` with `run-borrowed-second was
  priced 0.00s of pull on capacity Mercator keeps nothing on`, and
  `TestWorldGrantsRentalIdentityOnlyToCapacityItKeeps`; deleting only its kind
  term fails `TestOneShotCapacityKeepsNothingItPulled`. Both halves are
  load-bearing;
- keeping content at dispatch rather than when it lands fails
  `safety.locality_provenance` in ten Lab tests with `offer "rental-warm" holds
  producer@sha256:aaaa... with no World Tape seed and no content retained against
  that host`;
- completing an execution a sampled runtime after acceptance, ignoring the pull,
  fails `TestWorldActualRuntimeComesFromTheTape` and the generated-blueprint
  execution; dropping the pull cancellation fails
  `TestAnAbandonedPullLeavesNothingBehind`;
- answering `ListOffers` from world state rather than from the published
  observation fails `TestPlacementCanReadAnOfferTheWorldHasAlreadyReclaimed`;
- deleting the image loop from `imageInventory` in `internal/node/offers.go`
  fails `TestANodeHoldsTheImageItRan`, which drives a real agent over the real
  node protocol and reads the offer catalog over HTTP. The scripted runtime
  starts holding nothing and reports what it was asked to run, so a node that
  cannot become warm by running a workload is now visible above the unit level;
- disabling warming in the Lab world fails
  `TestExecutionWarmsARentalUnderTheRealControlPlane` with
  `the Rental that ran the image is still priced 289.14s of pull`, asserted on
  the Booking Decisions the real control plane recorded;
- each clause of `safety.locality_provenance` fails its own test.

### Phase 3 image stores and silence

On 2026-07-25, each fix answering the second round of review was measured by
breaking it and reading what failed.

- making `exceedsStartSLO` locality-blind fails `silence-is-not-infeasibility`
  with `step 1: submit "impatient": orchestrator: no feasible offers` and
  `TestNeitherModelTurnsSilenceIntoInfeasibility` with `production called a
  machine nothing of Mercator's runs on feasible=false, want true`. Applying the
  same gate in `internal/lab/oracle.go` alone fails the reference half of that
  test, so the two models are held to one rule;
- returning the CLI's digest from `contentStore.pinnedDigest` fails
  `TestAContentStoreImageIsNamedByTheDigestARunIsPinnedTo`: the report names
  every image `sha256:1111...` rather than the index a Run is pinned to, and
  three images the store never accounted for come back as confident answers;
- deleting the `Size.Content > 0` arm fails
  `TestDockerRuntimeSeparatesWhatItUnpackedFromWhatItPulled` with `an image this
  store holds 17GB of and cannot name a layer of was reported ... State:cold,
  want unknown`;
- filing a per-image unknown nowhere in `node.recordImage` fails
  `TestANodeOffersTheContentItActuallyHolds` and
  `TestPlacementRecordsWhatANodeCouldNotSayAsSilence` with `the node never
  reported the image it could not account for`; dropping the `Undescribed` case
  from `StartWork` fails the same conformance case with `image locality =
  "cold", want unknown`;
- deleting the publication guard in the Lab world fails
  `TestWhatABorrowedMachineHoldsIsNotSomethingMercatorKnows`; deleting it in the
  fake world fails `TestWorldMarketplaceOfferOwesFullImagePull`,
  `silence-is-not-infeasibility`, and `unpacked-is-not-the-same-as-pulled` with
  `silent-host: pull_seconds: want at least 285, got 0`. That last failure is
  what `hosts[].cached_images` buys: without the seed the same break is caught
  only as `pull_source: want "inventory_unknown", got "image_inventory"`, and the
  fixture's claim that this machine holds every byte and cannot say so would be
  decoration.

What the corpus still cannot state: no Blueprint drives
`nodeagent.DockerRuntime` or `node.imageInventory`. Both simulated worlds write
`domain.ImageInventory` directly, so a fixture states what a machine reports and
never how a container runtime came to report it. The node's own reporting is
held one level up instead, in `internal/daemon`, which drives a real agent over
the real node protocol against a scripted runtime, and in `internal/nodeagent`,
which drives scripted daemons whose shapes come from moby's types and a live
daemon where one is reachable. Closing that gap means a Blueprint that can
declare a container runtime and its image store, which is a world-model
capability rather than a fixture, tracked as
[#167](https://github.com/benngarcia/mercator/issues/167).

Removed rather than fixed: the invariant no longer requires a host's inventory
to be monotone between world snapshots. That law is true of one warming
transform, where `internal/lab/oracle.go` still holds it, and false between
arbitrary snapshots, where it would have made eviction a control-plane safety
violation and the dominant real-world locality failure mode impossible to write
down.

No test pins that deletion, and an earlier version of this section claimed one
did. `InvariantObservation.PreviousWorld` was the only input the clause read and
it was deleted with the clause, so no test in the package can express the rule to
fail it. What is pinned is the rule that replaced it:
`TestLocalityProvenanceAllowsAHostToLoseWhatItHeld` presents a host still holding
one seeded digest and one it retained while a third has gone missing, so both
halves of the invariant inspect real content and neither objects.

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/lab ./internal/scenario ./internal/adapter/fake ./internal/daemon -count=1
```

### Phase 2 placement

On 2026-07-24, three cases in `internal/daemon` drove one production daemon, one
real agent over the real node protocol, and a runtime that records what it was
asked to run:

- one enrolled node runs two workloads in sequence, and the second Run records
  that it reused the Rental rather than creating one;
- a container that exits non-zero closes its Run failed on the node's authority
  alone, with nothing reported by the application;
- a node that stops heartbeating stops being offered before its lease elapses,
  so Placement never chooses a machine Mercator has stopped hearing from.

### Phase 2

On 2026-07-24, the reviewed worktree passed:

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/node/... ./internal/nodeagent ./internal/nodeapi -count=1
go test ./internal/nodeagent -run 'Redelivered|Restarts' -count=3
```

The two idempotency cases inject real faults through the real transport: a lost
command result, and a machine reboot before the control plane learned the
outcome. Both were vacuous until the faults were real, because the registry
deduplicates before the wire.

### Phase 1

On 2026-07-24, the reviewed worktree passed:

```text
go build ./...
go vet ./...
go test ./...
go test -race ./internal/capability ./internal/scheduler ./internal/lab ./internal/scenario -count=1
cd web/app && bun run typecheck && bun run test && bun run build
```

`internal/scenario` reports the target scenario as pending rather than passing,
which is the corpus stating that the reusable-node path is specified and not yet
built.
