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
    and is not fixed here. It is fixed by the appended-decision entry below, and
    that daemon case reads the recorded refusal now.
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
    rather than as a world with none; that gap is older than this slice, and the
    appended-decision entry below closes it.
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
  - Judgment calls. The desired set is stated per workspace and reconciled for
    every workspace in one pass, because both bounds are the fleet's: what may be
    in flight at once and how often preparation may begin are about a machine's
    link and this process's egress, which every tenant shares. A per-host command
    could not express either, and a pass per workspace expressed neither until
    2026-07-25. A preparation identity carries the machine and
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
    preparation was terminal in two places and one of them is repaired. The
    operation store deduped on the identity with no regard for its state, so a
    node whose pull failed answered Duplicate for that content from then on and
    defeated the node agent's own intent to retry; that half is repaired under
    "replanning by explicit policy" below, and a second ask now reaches the
    runtime when one is issued. The control plane's half is not repaired. It
    reissues on `PrepareReceipt.Refused`, and no production prepare lane fills
    that field: `broker.Prepare` answers Started or Duplicate, a node settles a
    refusal asynchronously over the node protocol, and nothing in the prewarm
    controller subscribes to that, so what triggers a second ask on the only
    production lane there is remains a change to the desired set. Only the Lab
    world produces the synchronous refusal the controller reads.
    Nothing in production implements `orchestrator.ArtifactCatalog`, so no
    production Run declares an Artifact and the Artifact half of the desired set is
    exercised at L1 and against a real object store rather than end to end.
- [x] 2026-07-26: Make the class of work a Run is the thing that prices waiting,
  and turn on the score terms that were multiplied by zero. `ServiceClass`
  replaces `PlacementObjective` outright, with no shim and no derived objective,
  and it carries the exchange rates the score is finally computed over.
  - An objective named a quantity to minimise and never what a second of it was
    worth, which is why the ranking had to honour it separately: `ScoreWeights`
    was a struct nothing populated, so `StartLatencyUSDPerSecond`,
    `CompletionLatencyUSDPerSecond` and `UncertaintyPenaltyUSD` were multiplied by
    zero in production and only the balanced objective's one default ever fired.
    A class states the rate, so cost and waiting become one comparable quantity
    and there is one ranking rule for every class: least dollars, then earliest
    ready, then the offer ID. `domain.ServiceClass.Weights` is the declaration and
    `domain.ScoreWeights.ScoreUSD` is the whole arithmetic, said once and read by
    the scheduler, the Lab's reference model, and the rule below.
  - Every class states a multiple of one number. `domain.WaitingUSDPerSecond` is
    what a second of waiting costs the machine doing the waiting, 1.80 USD an
    hour, and interactive prices it at twenty times that to the start, standard at
    exactly that to the start, experimental at twice it to the finish, batch at a
    fifth of it to the finish, and opportunistic at nothing, which is what makes
    opportunistic the one class ranked on price alone. One rate to argue with
    rather than five, and each class measures waiting to the moment that matters
    to it.
  - The two uncertainty definitions are collapsed before either fires.
    `scheduler.uncertaintyPenalty` counted the capacity and reliability
    confidences a candidate was given; `lab.referenceUncertainty` counted those
    plus a full point for an unenumerated image inventory and another for unknown
    pricing. They agreed on every decision only because both were multiplied by
    zero, and the first Run scored at a nonzero rate would have made them disagree
    about every machine Mercator borrows a slot on. The extra points are deleted
    as double counting: unknown inventory is already priced twice, once as the
    whole content that might have to arrive and once as the cap on the confidence
    of the seconds that takes. What is left is the shortfall of the answers the
    candidate itself was scored on, stated the same way in both models.
  - The score is reproducible from the record. `BookingDecision.Weights` is the
    rate every candidate was scored at, and `CandidateDecision.Confidences` is
    every answer the uncertainty term counted, so a reader re-derives ScoreUSD
    instead of trusting it. `safety.score_is_reproducible_from_the_record` is the
    Lab invariant, and what it forbids is a term whose input is nowhere in the
    record: that is exactly how the two definitions drifted unnoticed, one of them
    reading facts off an offer no decision carried.
  - The refusal is loud. `POST /v1/runs` with a class Mercator cannot price is
    refused `400 SERVICE_CLASS_UNKNOWN` at the door, and Placement refuses to rank
    such a Run at all, because both used to fall through a `default:` that ranked
    on price and recorded a reason naming a class nothing declared: a caller would
    have learned their word was ignored from the bill.
  - History is migrated rather than shimmed. The sqlite migration renames
    `objective` to `service_class` in the requested workload, public and private,
    and in the policy each Booking Decision was taken under, mapping cheapest to
    batch, balanced to standard, fastest_start to interactive, and
    fastest_completion to experimental. Nothing maps to opportunistic, because no
    objective could say waiting is free. It refuses to run while any stream
    carrying the old field is still open, and refuses an objective it has no class
    for rather than guessing one. It also writes onto each old decision the weights
    it was actually scored at, which were nearly all zero, so the whole log is
    reproducible and not merely the part written since a class carried a rate.
  - While the six wire surfaces were open, `launch_ephemeral` went into the
    `CandidateDecision.disposition` enum in `openapi.json` and into the
    hand-written Effect `Schema.Literals` in `contracts.ts`. It had been missing
    from both since the lane split, so the console could not decode a Booking
    Decision on the whole ephemeral lane, and two exhaustive switches over the
    disposition rendered nothing for it.
  - Judgment calls. The class declares the exchange rates and nothing else yet.
    Priority, aging, group parallelism, backfill eligibility, interruption
    permission, and the queue-on-warm preference have no reader anywhere in this
    tree, and a declared field nothing consumes is the defect this plan has
    deleted five times: each becomes a field in the slice that prices it. Maximum
    queue delay and maximum cost are deliberately not restated on the class, because
    `PlacementPolicy.MaxP90StartSeconds` and `MaxExpectedCostUSD` are those bounds
    and are enforced today; a second copy would be two authorities for one refusal.
    The two reliability terms are left unpriced and deleted from `ScoreWeights`
    rather than kept at zero: they come back as expected redo cost, which is
    probability times a predicted start, so a flat penalty invented for them now
    would be the unmeasured constant this plan keeps deleting. The uncertainty
    penalty is derived from each class's own waiting rate over
    `domain.UncertaintySeconds` rather than being a second invented number per
    class. And every fixture that equalises price so locality decides now states
    the class whose rate preserves that intent, because `running-fills-a-cache`
    turns on less than half a cent.
- [x] 2026-07-26: Seed the Bookings a placement world starts with, and let five
  queue fixtures state the answer. A Blueprint that said a Rental was busy stated
  it twice and only one of the two reached Mercator: the world knew the machine
  was occupied, and the Broker's Rental Schedule store was empty, so Placement
  had nothing to queue behind and struck every busy Rental out as
  CAPACITY_UNAVAILABLE. The fast placement harness now seeds each declared
  schedule through `domain.RentalSchedule.Reserve`, so a fixture can only state a
  schedule Mercator could have reached, and completes the seeded Booking through
  `Complete` at the moment the world frees the machine, because the Bookings a
  world starts with belong to Runs no event log ever saw and nothing else would
  ever report them ending. Two more gaps sat behind that one and neither was
  fixture construction. Every Booking Decision recorded the wait and never what
  the wait was read from, so each candidate on a Rental with Bookings now carries
  the schedule as the decision read it, version and all: a schedule moves, and
  the seconds alone are unretraceable afterwards. And fixtures name Bookings
  while Mercator hashes them, so the runner resolves a fixture's names to the
  identities Mercator minted rather than asking production to accept a name from
  a world. `busy-rental-worth-waiting`, `queued-run-makes-fresh-capacity-win`,
  `multiple-runs-schedule-in-order`, `full-schedule-forces-fresh-capacity`, and
  `dataset-gravity-worth-waiting` are green.
  - Judgment calls. No Lab invariant: seeding a schedule is fixture construction,
    and the laws that police queueing already exist and now read seeded schedules
    at L0 too. `busy-rental-not-worth-waiting` and `running-fills-a-cache` were
    green while asserting the absence of this capability, with their busy Rental
    refused rather than priced; both now assert the wait, which is the same
    placement for the honest reason. `full-schedule-forces-fresh-capacity` asked
    for SCHEDULE_FULL at `rental_schedule.queued`, a field a Rental Schedule does
    not have, and now asserts the QUEUE_CAPACITY_EXCEEDED that Mercator emits at
    the path it emits it on. Two fixtures priced the warm Rental a nickel an hour
    above capacity that provisions in five minutes, which the balanced objective
    decides on the price gap and not on the wait, so both now price their
    machines identically and let the queue decide, which is what each is about.
  - What is left. `queued-booking-deadline-expiry` keeps exactly one declaration,
    `schedule_advancement`. Its Booking is queued with a latest start six minutes
    out while the Booking ahead of it runs for thirty, and at seven minutes
    nothing expires it. The survey's suspicion that the declaration was stale
    because `dispatchQueuedBooking` exists is wrong: dispatch is what happens when
    a Booking's turn arrives, and expiry is what happens when its turn does not.
    A fixture can seed only the running Booking's end, because when a waiting one
    finishes depends on when it starts and the world models one busy window per
    machine; a queue that drains further than one Booking is held at L1 by
    `a-queue-drains-as-it-runs`.
- [x] 2026-07-26: Answer the review of the service-class commit. Two reviewers
  falsified seven claims on it, five of them distinct once the duplicates are
  folded together, and all five reproduced.
  - The class was refused at the door that stores a revision. `CreateRevision`
    validated the raw body while run intake normalised first, so one body got two
    answers: `POST /v1/runs` filled an omitted class with standard and returned
    202, and `POST /v1/workloads/{id}/revisions` refused the same omission 400
    `SERVICE_CLASS_UNKNOWN`, which the parent commit had accepted and which
    openapi's own PlacementPolicy says defaults to standard. The door also stored
    what it validated, which is why a revision could be recorded with no class at
    all and served back as `service_class:""`. It normalises before validating now,
    which is the order `NormalizeWorkloadRevision` documents, and the validator
    says why its class check is deliberately not read as an effective value the
    way the runtime bound above it is.
  - The migration missed the objective site that repriced work.
    `compute.workload.revision_created.v1` stores the whole revision at
    `$.revision.spec.placement.objective` on the workload stream, and nothing
    decodes `objective` any more, so an unmigrated revision read back with no class
    and the next Run created from it was normalised to standard: a caller who
    stored `fastest_completion` was scored at a tenth of experimental's rate with
    nothing in the record saying so, and the two doors disagreed about one history.
    Every site now names the stream it lives on, because the open-Run refusal is
    about Runs in flight: a workload stream never closes, so listing the new site
    without that distinction would have refused every database that has a workload.
    The completeness assertion scanned the same three paths the migration writes,
    which is a tautology; it scans the whole payload for the word now, and the case
    reads the migrated revision back through `workload.GetRevision`.
  - A fact its own publisher disowned was the cheapest answer in the fleet. A
    published `NetworkFact` with confidence zero had its speed used to predict a
    duration and its zero dropped from the record, because zero means nobody said,
    so a host publishing 5 Gbps it disowned was charged 3.7 seconds for a 2GB image
    and no doubt at all, outranking the host that published 750 Mbps and stood
    behind it, while the host that published nothing was charged the most of the
    three. `domain.NetworkFact.Answers` is the one rule both readers of a fact ask,
    and expiry moved into it because it was the same question asked twice. The hard
    half mattered more: a Run's `MinP10Mbps` floor with `AllowUnknown` false was
    cleared by any fact naming a big enough number, its publisher's confidence
    unread. `PathSpec` carries a stated confidence so a Blueprint can state the
    machine that disowns its own number, and the fast harness implements paths at
    all: it dropped them silently, so a fixture could declare a measured throughput
    and be scored against the standing assumption.
  - A machine nobody priced was scored as free. Both models predicted zero dollars
    for an offer with `Pricing.Known` false, so a Run with `allow_unknown_pricing`
    took the unquoted machine over a quoted one every time, sixty seconds slower to
    start, and `internal/node/offers.go` is written to publish exactly that offer
    for a node with no configured shadow price. The absence is stated as the source
    of the cost estimate, `domain.CostUnpriced`, and `CandidateDecision.Priced`
    reads it off the record; `Preferred` asks it before it compares dollars, so an
    unpriced candidate ranks behind every priced one and is taken when the
    alternative is not running, which is what the policy asked for. A budget is the
    same absence read as a bound: `max_expected_cost_usd` was cleared by a
    candidate reporting zero dollars, and is refused `COST_LIMIT_EXCEEDED` with
    "unpriced" as what was offered. No cost confidence is invented, because a
    provider quotes a rate and publishes no confidence in it, and the reference
    model's deleted point for unknown pricing is not restored: a full point of
    interactive doubt is 0.60 USD against a real price of 0.72, so it never fixed
    the mispricing it was charged for. The answer is that there is no number.
  - Three marketplace adapters asserted full confidence in a capacity claim their
    provider never put a number on. A catalog listing says a machine type is in
    stock and says nothing about how sure of that it is, so the offers state
    availability and no confidence, which is what an absent entry has always meant.
    No score changes: an unstated confidence and a stated certainty are both worth
    zero points of doubt. The enrolled node and the probed local daemon keep full
    confidence and say why, because that capacity is Mercator's own observation.
  - What was rejected. The reviewers wanted a doubt constant for a marketplace
    capacity claim so the uncertainty term's capacity dimension could fire. A flat
    0.7 for a listing is the unmeasured number this plan keeps deleting, and the
    real answer is a measurement: how often provisioning a listing actually
    succeeds is the slice that prices it. Until then the live sources of doubt are
    the transfer confidences, which is the honest state of the term. The claim that
    the `rental-doubted` fixture rests on a value nothing emits is also wrong for
    the corpus: `internal/adapter/fake` publishes what the Blueprint's
    `capacity_confidence` states, which is how a simulated external behaviour is
    supposed to reach Mercator.
  - Three live cases could not be evaluated on the amd64 Linux host this review ran
    on, and two reported it as a defect in Mercator. Docker Hub is rate limiting
    anonymous reads from it, and `requireDockerHubReachable` asked whether `/v2/`
    answers, which it does: the token is issued and every manifest is then answered
    429. The guard asks whether the registry will serve this machine now. The
    private-registry case needed nothing off this host and reuses a copy already on
    disk, which is what content addressing means. The live Docker adapter case
    hardcoded `linux/arm64`, the laptop it was written on, so on this host the
    daemon reported every local image missing and went to the registry for a build
    it would never run. And the node disk fact was compared to a fresh read for
    exact equality, which fails whenever anything else writes to the same
    filesystem; it is held to the filesystem now rather than to the instant.
- [x] 2026-07-26: Answer the second review of the service-class commit. Two
  reviewers falsified five things, four of them distinct, and all four
  reproduced. Three were the same shape once more: a rule stated in one place and
  contradicted at the place that answers.
  - A disowned fact bought its publisher less than silence in the half that
    decides feasibility. `downloadRequirementSatisfied` consulted `AllowUnknown`
    only when an offer published no download facts at all, so an offer whose fact
    its own publisher disowned, or whose fact had expired, was struck out
    `NETWORK_FACT_UNSATISFIED` while an offer publishing nothing was feasible and
    selected. `len(facts) == 0` is the wrong test for "nobody answered":
    `NetworkFact.Answers` skips a disowned fact inside the loop that the empty
    check has already decided instead of. There are two ways to miss a floor and
    the record now keeps them apart. A candidate whose fact answers and falls
    below the floor was measured too slow and the decision states the speed it
    published; a candidate nobody answered for measured nothing, and AllowUnknown
    is what decides. `NetworkFacts.DownloadP10` is the one rule for which
    published fact answers a question about a link, so the bound and the transfer
    prediction read one measurement rather than two facts that happen to travel
    together.
  - No Blueprint could state a download floor, which is why nothing caught it.
    `RequestSpec.download` is the vocabulary, following `max_start_latency`, and
    `WorkloadForRun` carries it so both simulators read one translation.
    `a-floor-refuses-a-measurement-and-not-a-silence` is the L0 fixture and
    `a-link-nobody-measured-is-not-a-slow-link` is the same claim at L1. The
    reference model still refuses a world with a network requirement, which is
    honest rather than convenient: teaching it this rule by calling the same
    domain method production calls would make the two models agree by
    construction, which is the "asking the scheduler to confirm its own
    arithmetic" this plan has rejected twice.
  - The record priced an absent price at zero and nothing said which rule ranked
    it. `ScoreUSD` sums the cost estimate, which is zero dollars with source
    `unpriced`, so the unquoted machine scored 0.0005 against the selected
    machine's 0.3338, `CandidateTable.tsx` sorted feasible candidates by that
    number ascending, and an operator reading the Run saw the machine the
    placement refused ranked first as the cheapest in the fleet. The decision now
    records `PRICED_BEFORE_UNPRICED` when a priced candidate was taken over a
    feasible unquoted one and `UNPRICED_LAST_RESORT` when the Run landed on a
    machine nobody quoted, which is the standard `ServiceClass.SelectionReason`
    set. `web/app/src/lib/placement.ts` is the domain's own ranking in the view of
    it, asked before any dollars are compared, and a score with no price in it
    reads as one rather than as a total.
  - The revision door published secrets. `workload.CreateRevision` marshalled the
    whole revision into a public event with no private copy, so an environment
    value, which is where a caller puts a token, reached every console reader of
    the workspace over the SSE feed, while `POST /v1/runs` has reduced the same
    value to `{"kind":"literal"}` for as long as it has had a private payload.
    `domain.WorkloadRevision.Public` is now the single redaction both doors write
    through, so a field added to a workload spec cannot reach one public payload
    and not the other, and the event's private payload is the whole revision,
    which is the copy a Run is created from.
    `migrateStoredRevisionSecrets` rewrites the history through that same
    function, because history is what a console reader reads.
  - Both Lab world statements the previous review's Blueprints rest on were
    unfalsifiable. Stamping certainty on a fixture's path measurement again, or
    publishing the default priced offer for a Rental a fixture says nobody quoted,
    each left the whole tree green: only `internal/scenario/sim.go` was held to
    either rule, by two L0 fixtures. `a-link-nobody-measured-is-not-a-slow-link`
    and `an-unquoted-machine-is-the-last-resort` are the two executions those
    statements are now falsifiable through, which is development rule step 6 for
    two capabilities that shipped without it.
  - Judgment calls. An unpriced candidate's score keeps its number rather than
    becoming absent. The convention this plan chose for a missing price is the
    number beside a source that says nobody quoted it, and a second convention for
    the same absence is the drift `capability.LocalityState` and
    `capability.ArtifactLocality` were deleted for; what was wrong was that
    nothing said the number omits a price, and the two reason codes and the
    console ordering say it. The disowned publisher under `AllowUnknown` false is
    refused `UNKNOWN_FACT` with "unknown" offered rather than
    `NETWORK_FACT_UNSATISFIED`, because that machine measured nothing rather than
    measuring too slow.
- [x] 2026-07-26: Let a decision state the risk it was taken under, and hold the
  two models to every candidate. Phase 4 prices reliability, and there was
  nothing to price it from: `domain.ReliabilityEvidence` had exactly one writer
  in the tree, `internal/adapter/vast`, no Blueprint could state a machine that
  refuses to start, and no decision recorded either rate. So the whole risk half
  of the goal rested on a fact no fixture could construct and no record could
  show.
  - `RentalSpec.reliability` is the Blueprint vocabulary, in the terms the one
    production publisher states: how often this machine refuses to start, how
    often it drops what it is running, and how much its publisher stands behind
    the history. Both simulated worlds publish it, so the fact travels the way it
    travels in production, from the provider onto the offer.
  - `CandidateDecision.Reliability` is the record. Recording it is not deferred
    along with pricing it, because the doubt about that history already reaches
    the score: `Confidences` has carried a `reliability` entry since the two
    uncertainty definitions were collapsed, so a candidate could be charged a
    tenth of a point with no sign anywhere of which answer the doubt was about,
    and a score is only re-derivable from the record if every answer it doubts is
    in it.
  - Neither rate is priced. Expected redo cost is a probability times a predicted
    start, nothing here predicts either yet, and a flat penalty invented for them
    now would be the unmeasured constant this plan keeps deleting, which is why
    the two reliability weights were deleted from `ScoreWeights` rather than kept
    at zero. `a-machine-that-fails-to-start-says-so` states that gap rather than
    hiding it: two machines whose only difference is that one refuses two starts
    in five score to the same dollar, so the placement falls through to the offer
    ID and the flaky machine takes the Run. Price a refusal and the fixture fails,
    which is the point of writing it down.
  - The oracle law is per candidate now.
    `TestSmallWorldReferenceSolverAgreesWithProductionOnEveryCandidate` compares
    every stage of both models' predictions, their quantiles and confidences,
    the dollars, the doubt, and the risk each recorded, where it used to compare
    feasible sets and winners. Every drift this corpus has found was too small to
    move a winner when it landed: two definitions of uncertainty agreed on every
    placement for a phase because both were multiplied by zero. Restoring the
    reference model's extra point for an unenumerated inventory fails it twice
    per candidate, on the doubt and on the dollars; having the reference model
    throw away either quantile the provider published fails it on
    `provision_seconds` and on both starts derived from it; dropping the risk from
    the production record fails it on what the two models recorded. The small
    world's provisionable offer now states its own p50 and p90, because with only
    an expectation stated there neither model could be caught inventing a spread,
    which is a defect this plan has already had to fix once in the scheduler.
  - Judgment calls. The rates go on `RentalSpec` and not on
    `MarketplaceOfferSpec`, because a fixture that states a history no candidate
    in the corpus is scored against is a declared field nothing consumes; a
    marketplace offer publishes one the same way in the slice that needs it. A
    clean measured record and no measurement at all stay two worlds: silence
    states no rate to read and no confidence to doubt, so the corpus asserts the
    steady machine's zeros as a published fact rather than deriving them from an
    absence. The Blueprint's expectations for the two rates are exact rather than
    bounded, because a published fact that arrives changed arrived from somewhere
    else.

- [x] 2026-07-26: Make a start a moment somebody observed, and let the world spend
  acquisition, boot, and agent enrollment. Decision V2 says predicted start
  latency is calibrated against `started - accepted`, and nothing in the tree
  could perform that subtraction: `adapter.ExternalObservation` carried no start
  timestamp, `capability.WorkloadObservation.StartedAt` was written by the
  contract and read by nobody, and the Lab computed `execution.StartedAt` and
  reported it through no seam. Nothing can learn a stage duration until the stage
  has an actual.
  - `ExternalObservation.StartedAt` is what the thing holding the workload says
    about when its process began. It is not `ObservedAt`, which is when Mercator
    looked, and it is not the accepted moment, which is when the machine started
    getting ready. The node's Docker runtime reads `State.StartedAt`, which is a
    second read because `docker ps` carries it in no format, asked once for the
    whole list; the Docker adapter reads it off inspect; RunPod reports
    `lastStartedAt` and Vast reports `start_date`. Shadeform reports none and says
    why: `created_at` is when the instance was asked for, so publishing it as a
    workload's start would fold a whole acquisition and boot into the runtime.
  - `compute.run.execution_started.v1` is the moment on the run stream, written
    once per attempt from the first observation that carries one and cleared with
    the launch when a new attempt begins. It is its own event because it is a
    different fact from a phase: every provider in this tree reports running from
    the moment it accepts, so a phase says only that Mercator asked.
    `domain.RunRecord.StartedAt` is the read model and the public contract, absent
    until something observed one.
  - A Booking's runtime is measured from the container's own start where the
    holder reported one. It used the moment Mercator polled, which is the same
    defect one layer over: both runtimes a Booking declares are bounds on a
    container. Where nobody reported a start it still falls back to the
    observation, because a schedule needs a clock to project from and the last
    thing Mercator can prove is the honest choice there; nothing derives the Run's
    recorded start that way.
  - Three of the eight launch stages cost zero time, because a provisionable
    offer's provisioning was a published claim the world never spent: Launch put
    an execution straight into running. `ProvisioningSpec` states each stage and
    both simulated worlds spend them, so a pull and an Artifact read begin when
    there is a machine to land on. The stages are stated separately from
    `expected` and `p90` rather than derived from them, because a world that spent
    its provider's own expectation would make that expectation right by
    construction, and each is a pointer because a stage a fixture did not mention
    and one it says costs nothing are different sentences.
  - The Run Bundle holds two predicted-versus-actual records per Run.
    `start_latency_seconds` is read entirely out of Mercator's own event log:
    predicted from the selected candidate's start estimate, actual from the
    accepted moment on the launch receipt to the start moment on the run stream. A
    Run whose holder reported no start gets the row with the prediction and no
    actual, sourced `start_not_observed`, because a zero there would teach a
    calibration that every start is instant.
  - `safety.start_is_observed_not_inferred` is the Lab invariant. Every start
    moment the run stream records must be one an observation of that Run reported,
    must not be later than the look that carried it, and must not be dropped when
    a holder did report one. The three clauses read independent halves of the
    record, so none is satisfied by Mercator agreeing with itself: deriving the
    record from the accepted moment leaves the observation saying otherwise, and
    fails six Lab executions.
  - `a-start-is-a-moment-somebody-observed` is the L0 fixture and
    `conformance/a-node-reports-when-the-container-really-started` is the same
    claim at L1. `ExpectSpec.start_latency_seconds` is the vocabulary that makes
    either statable, with `no_start_observed` as the other sentence. Shortening
    the world's boot to zero fails the L0 fixture with "want at least 588, got
    348.64"; deriving the moment from acceptance fails it with "got 0".
  - `TestTheNodeReportsWhenTheContainerStarted` is the live half, run against
    Docker Engine 29.6.2 on amd64 Linux with the containerd snapshotter: a
    container launched through the production runtime, its start moment read back
    and compared against an independent `docker inspect
    {{.State.StartedAt}}`. `TestANodeReportsTheMomentItsContainerReallyStarted` is
    the end-to-end seam over the public API, against a scripted runtime rather than
    a live daemon, and it is the fourth defect found at
    `broker.observeOnNode` after Artifacts, `NodeFacts.Artifacts`, and cache
    mounts: deleting the one line that carries the moment leaves the Run with no
    start and every other test green.
  - The console reads the moment instead of inventing it twice. `runningAt` was
    stamped from the Booking Decision's own event time, which for a provisioned
    machine is before that machine existed, and again from every observation, so a
    reconnecting console reported a long-running workload as newly started.
  - Judgment calls. A Run with no start moment is not an invariant violation:
    acquisition and boot have no production observation until phase 5 bootstraps
    an agent on provisioned capacity, and the record states the stage absent
    rather than estimated. The existing fixtures state stages summing to the
    expectation their provider published, because a machine that took as long as
    it was said to take is a legitimate world and each of those fixtures is about
    something else. And the whole slice was built beside a concurrent session in
    the same worktree; the reliability entry above landed from that session and
    the two changes are separate commits.
- [x] 2026-07-26: Answer the review of the risk-history commit. Two reviewers
  refuted parts of it, and the central one reproduced immediately: the term that
  commit relied on to justify recording a published history ran backwards.
  - The score doubts only the answers the score reads. `Confidences` carried a
    `reliability` entry, `Uncertainty` charges `1 - value` for a confidence strictly
    between zero and one, and nothing anywhere prices either rate. So the only thing
    a published history could do to a placement was penalise the provider that
    published one: a machine measured and never seen to fail carried a tenth of a
    point at 0.03 USD and lost the Run to an identical machine nobody had measured,
    while a machine whose provider was certain it refuses every start carried no
    doubt at all and won. Adding a third Rental with no history to
    `a-machine-that-fails-to-start-says-so` demonstrated it, at 0.333833 USD against
    the two measured machines' 0.336833. The entry above defended recording the rates
    with "the doubt about this history already reaches the score"; what reached the
    score was a charge for having answered, which is the inverse of modelling the
    unknown as uncertainty. Both models drop the entry, `Uncertainty` states the rule
    that produced the inversion, and the history is recorded, unpriced, and undoubted,
    for the reason the cache warmth beside it is recorded: it is the account of what
    was known when the placement was taken, and a fact no record carries is one the
    slice that prices it cannot be held to.
  - The machine nobody measured is now in both fixtures, at L0 and at L1, which is
    what makes the rule falsifiable rather than merely stated. All three candidates
    score to the same dollar and carry no doubt, so restoring the reliability
    confidence to either model separates the two measured machines from the silent one
    by 0.003 USD and fails the Blueprint on the doubt, the score, and the winner, and
    fails `TestARefusalToStartIsRecordedAndNotPricedAtL1` on the doubt.
  - A rate nobody measured is absent rather than zero. `ReliabilityEvidence` held two
    rates under one confidence, so the only production publisher of it recorded
    `start_failure_rate: 0` at `confidence: 1` for every Vast candidate in the fleet:
    Vast measures an uptime score and nothing about refused starts, and the record
    said its publisher had measured this machine and never seen it refuse one. That
    is the claim `internal/scenario/schema.go` already refuses to let a fixture make.
    Each rate is now a `domain.StatedRate` that stands on its own measurement, and the
    confidence beside it is what says the measurement happened, which is the reading a
    disowned network measurement already gets in this tree. `RentalSpec.reliability`
    states each rate or omits it, a history that states no rate at all is refused at
    load by `reliability-history-without-a-rate.json`, and the offer field is
    `omitzero` so an unmeasured machine publishes no history on the wire rather than an
    empty one.
  - Vast's `reliability2` is a pointer, for the reason `dph_total` beside it is. The
    field was a bare float64, so an ask that omitted or nulled it decoded as an uptime
    score of zero and Mercator published "drops every run, certain" on the publisher's
    behalf: the worst answer in the catalog, invented out of a missing field.
    `buildOffers` had no test over its reliability output at all; two now cover the
    measured ask and the silent one, and mutating `interruptionHistory` to state a
    start failure rate or to read silence as zero fails both.
- [x] 2026-07-26: Answer the review of the ephemeral start-moment commit. Two
  reviewers refuted parts of it, and what sat under the three new adapter cases was
  a production rule missing rather than three fixtures merely being wrong.
  - Mercator files only a start moment it can defend, and
    `adapter.ExternalObservation.ObservedStart` is where the two things that
    disqualify one are stated, so the rule holds for the reusable lane and all three
    ephemeral adapters at once instead of three times in three adapters. A moment
    later than the read that carried it is a clock Mercator does not share: a host an
    hour ahead published a start an hour in the future, `execution_started` recorded
    it, the Run Bundle filed a start latency an hour too large as a measurement, and
    `safety.start_is_observed_not_inferred` then failed the execution for a moment
    Mercator only passed through. A moment carried by a phase saying the work has not
    begun is the claim every provider makes from the moment it accepts: RunPod
    publishes `lastStartedAt` while the image is still landing, which is exactly why
    an address is what makes a pod running here, and leaving the moment outside that
    distrust bought the phase gate nothing. The observation still carries what the
    holder said either way; only what Mercator adopts is refused.
  - The Lab rule reads observations through that same law, which makes the clause
    about a moment ahead of its read reachable and the refusal not a violation. The
    clause was dead: deleting it left the whole tree green, because neither simulated
    world can publish a start that has not arrived.
    `TestEveryClauseOfTheStartRuleCanFail` drives all three clauses,
    `TestAStartClaimMercatorRefusedIsNotAViolation` is the world the second loop would
    otherwise blame Mercator for, and the fake provider gained the one knob that can
    state either: it publishes the moment it gave a container a process, dated on its
    own clock, and publishes nothing by default.
  - The local Docker lane measured every start latency as a negative number. The
    receipt's accepted moment was Mercator's clock after `docker start` returned,
    which is later than the start the same daemon reports, and on a launch resolved as
    a duplicate it was later by the whole retry gap: the container was made and given
    a process by the first attempt, and only the acceptance was re-dated. It is now
    the moment the daemon made the container, on the daemon's own clock, so both
    moments in the subtraction come from one clock. A start earlier than its own
    acceptance is not subtracted at all: the Run Bundle row names the pair it could
    not measure with `start_before_launch_accepted` rather than filing a negative
    wait.
  - `docker inspect` moments are parsed loudly. Both were read with the error
    dropped, so a daemon that renamed the field or stated the moment in another form
    reported the epoch, the whole lane published no start, and every start-latency row
    degraded to unobserved with nothing failing. The mapping is
    `containerFromInspect` over one payload, its cases read a capture this host's
    Engine 29.6.2 actually printed, and the live integration case compares the
    observed start and the accepted moment against an independent `docker inspect`.
    Deleting the parse fails a case offline and fails the live case.
  - Both caught fixtures were wrong about the record they pinned. The Vast case dated
    an instance's start half a day after the read that carried it and passed, because
    the adapter under test read the wall clock. The RunPod case pinned a start on a
    pod with no address, which this adapter reports as queued, so it canonized an
    observation saying a workload had begun and had not begun at once. Both pin the
    read moment now and assert the order, and the queued pod with a start already
    published is its own case.
  - Judgment calls. The refused claim is dropped silently rather than raised: the
    observation event still records what the provider said, so nothing is hidden, and
    failing a Run because its host keeps a skewed clock would take capacity out of
    service over a stage nobody can measure. No World Tape vocabulary states provider
    clock skew, so the production rule is held by the seam cases and the Lab rule by
    hand-built records rather than by a Blueprint; a fixture that states a host's
    clock offset belongs with the slice that gives the simulated worlds one. And this
    pass ran beside a concurrent session in the same worktree: its Lab half landed
    inside that session's commit `6858429` rather than in a commit of its own.
- [x] 2026-07-26: Make a launch a waterfall of eight predicted stages, each with
  an actual of its own. The record carried four quantities, and one of them,
  `domain.LaunchSeconds`, stood for agent enrollment, container creation, and
  application readiness together. A single number covering three stages cannot be
  calibrated: an actual for it is the sum of three durations with three causes, and
  measuring any one of them could not replace it. Five of the eight stages the phase
  goal names had no prediction, and three of them cost no time in either simulated
  world, so a prediction of any of them was measured against nothing.
  - `domain.LaunchStage` names the eight in the order a launch goes through them and
    `domain.LaunchStageEstimates` carries one distribution each, read through one
    ordered list so a stage cannot reach the record without reaching the bundle, the
    invariant, the reference model, and the console with it.
  - A published provisioning claim is read as a claim about boot, because that is
    what its only publisher in this tree calls it: Shadeform states a min and max
    `boot_in_sec` for an instance type and nothing else about getting one.
    Acquisition and agent enrollment are published by nobody, so they are predicted
    as nothing and the record says whose silence that was, `unpublished` for capacity
    that has to be allocated and `machine_exists` for capacity already running
    Mercator's runtime. A share of the published claim would attribute a provider's
    boot window to stages the provider never mentioned, and a prior of Mercator's
    would be a number invented for every listing in every catalog. The consequence is
    a machine prediction short of the truth by whatever acquisition and enrollment
    really take, which the per-stage record now shows: the L1 fixture predicts zero
    acquisition against an actual of two minutes.
  - Application readiness is predicted from the workload's own declaration and from
    nothing else. Readiness is the application's semantics, so no machine fact and no
    provider claim predicts it, and a Run that declares none is predicted none rather
    than charged a prior. It is deliberately not part of `StartSeconds`, because the
    actual a start is calibrated against is the container's own start moment and
    readiness is a later one.
  - The image answer becomes two stages over two resources, a fetch across a link and
    an assembly of bytes already on the disk. Confidence is stated per stage, so a
    host with nothing to fetch is certain about the fetch and doubtful about the
    assembly it still owes, where one answer used to carry the lower of the two.
  - `WorldSpec.launch` is what a launch costs after its content arrives: `unpack`,
    `container_start`, and `application_ready`, each a stated duration rather than
    arithmetic over a rate, because a world computing what the predictor computes
    would make every prediction right by construction. Both simulated worlds spend
    them, and the launch effect's consequence carries what each of the eight stages
    really took. That ledger is the only source of six of those actuals: Mercator can
    observe a container starting and an application reporting ready, and nothing in
    production tells it when a machine finished booting.
  - Application readiness is a typed report. `compute.run.reported.v1` with type
    `ready` requires `data.ready_at` and reduces into `domain.RunRecord.ReadyAt`. The
    moment is the application's own rather than the moment Mercator appended the
    event, which is the defect the observed start moment was fixed for one stage over.
    The conformance probe was already sending a `ready` report, with a scenario name
    and no moment, and nothing in the tree read it: that is the untyped callback this
    slice is about. Both simulated worlds deliver readiness as an inbound call,
    because routing it through the provider seam would make a running process and a
    serving one the same fact again.
  - `predictions.jsonl` carries a row per stage beside the runtime and start-latency
    aggregates, and `safety.prediction_is_recorded_against_its_actual` is the Lab
    invariant: for every launch the Effect Ledger accepted, every stage the world
    spent has both halves in the record, and no stage the world spent is one the
    record cannot name. The two halves are read from independent places, which is what
    stops the rule being satisfied by the predictor agreeing with itself. It is
    deliberately not stated as accuracy: how close a prediction lands is a calibration
    metric, and a rule of that shape would fail on a fixture whose world is simply
    slow, which several of these fixtures are on purpose.
  - A Blueprint states stages by name in one map, replacing `provision_seconds`,
    `pull_seconds`, `pull_source`, `pull_confidence`, and `artifact_seconds`. A stage
    cannot be added to the record without a way to state it, and a fixture asserts
    seconds, source, and confidence together because zero seconds means two opposite
    things. `a-launch-is-eight-stages` is the placement fixture and
    `conformance/every-stage-of-a-launch-has-an-actual` the same claim at L1.
  - Judgment calls. The world spends unpack on any launch that had bytes to fetch or
    content it holds unassembled, while the scheduler charges assembly only for
    content a host already fetched: the two are meant to differ, and a fetch whose
    assembly the model folds into the transfer rate is a gap the per-stage record now
    shows rather than hides. An Artifact stage for a Run that reads nothing names that
    silence, `workload_reads_nothing`, because an empty source is what the new rule
    reads as a stage nobody predicted. And the slice's own statement that five stages
    take zero time was written before the observed-start slice landed: three did, and
    the three are the ones this commit makes cost time.

- [x] 2026-07-26: Answer the second review of the start-moment commit. Two reviewers
  refuted parts of it, and the blocking one was a law with two readers where only one
  of them asked.
  - `bookingStartedAt` adopted any moment that was not nil, in the same append forty
    lines below the one that refuses a moment Mercator cannot defend. So an ephemeral
    host an hour ahead had its start correctly kept off the run stream and stamped
    straight onto the Booking's runtime clock: `RemainingMaxSeconds` stayed positive
    for an extra hour, `OverrunSeconds` read zero for the whole real runtime,
    `Expired` never fired, and the schedule reported the machine busy while the
    container burned paid capacity. `ExternalObservation.EstablishedStart` replaces
    `ObservedStart` and returns the moment with the answer, so there is nothing left
    for a caller to adopt on its own.
  - The clause about a moment ahead of its read was structurally unreachable on the
    reusable lane, because the node supplied both moments it compares: its runtime
    stamps ObservedAt from its own wall clock and reads State.StartedAt off the same
    daemon, and the Broker copied both through. Two moments from one foreign clock
    agree with each other whatever that clock reads.
    `capability.WorkloadObservation.ReceivedAt` is when the control plane accepted the
    report, stamped where a node's report enters the control plane and never by the
    node, and the Broker reads it as the observation's read moment. It is at or after
    the node's own look in real time, so a start dated ahead of it is a start ahead of
    Mercator, which is the only comparison available without a shared clock. A stored
    report with no receipt moment is refused loudly.
  - Neither ephemeral provider could establish the moment it was publishing. RunPod
    stamps `lastStartedAt` when it places a pod, minutes before the image has landed,
    and the value does not move when the process begins: a pod placed at 11:00:05 and
    running at 11:04:10 filed five seconds as a measured start latency. Vast's
    `start_date` is when it started the instance's contract. The phase gate postpones
    adopting a stale moment rather than correcting it, and a failed pull reaches EXITED
    still carrying it, which `Exited()` accepts. Both adapters publish no start moment
    now, which is what Shadeform already did and said why. The Vast half is a judgment
    call from the same argument rather than from a captured transcript: the cost of
    being wrong that way is one stage recorded as unobserved, and the cost the other
    way is every start latency on that lane filed as a measurement of nothing.
  - The reusable lane still read `State.StartedAt` with the parse error dropped, which
    is the silent degradation the Docker adapter had just been corrected for, in the
    one lane that will measure a start latency after phase 2. A runtime whose compat
    inspect prints Go's default time form reported every container with no start.
    `parseStartMoment` now answers three things rather than two: a line that names no
    container is a container pruned between the two calls and is skipped, the epoch is
    a container that never ran, and any other form fails the read. The Docker adapter's
    own guard on `Created` had no case that could break it, which matters more than the
    half that did, because `Launch` returns that moment as the launch's acceptance and
    `invalidLaunchReceipt` wedges every reduce of the stream without one.
  - The Lab rule stopped delegating. It had been changed to call the production
    predicate it exists to constrain, so deleting the clause about a moment ahead of
    its read left the rule agreeing with the mutation, which is the shape
    `safety.locality_is_never_infeasibility` was corrected for two entries earlier.
    The rule states the three clauses itself, and it reads the Rental Schedule now: a
    Booking's clock must be a start one of that Run's observations established or the
    read that carried one of them. That clause is the one no rule in the corpus had,
    which is how the blocking defect stayed green.
  - The corpus can state the world. `RentalSpec.clock_ahead` makes a host read the
    moment it states off its own clock while both simulated worlds keep the truth on
    Mercator's, which no fixture could say before, so the law was held only by
    hand-built records. `a-clock-nobody-shares-is-not-a-start` is the placement fixture
    and `conformance/a-clock-nobody-shares-measures-nothing` drives the same world
    through the real orchestrator, event log, schedule, and Run Bundle, where the
    start-latency row reads `start_not_observed`. Publishing the truth instead of the
    machine's own reading fails the conformance case at 20 seconds.
  - `TestANodeWithASkewedClockDoesNotSetMercatorsOwn` is the production half, against
    the real daemon, the real node protocol, and a machine whose clock runs an hour
    ahead. Its workload declares a one second bound and does not exit: copying the
    node's own read moment through records a start an hour in Mercator's future, and
    adopting any non-nil moment for the Booking leaves 3509 seconds of enforced runtime
    on a container already past its bound, so the daemon queues an arriving Run behind
    work that will never finish. Every scripted runtime in that file dated both moments
    from the control plane's clock, which is why nothing there could see either defect.
  - The record of the previous pass was wrong about what it could prove. At 588b66f
    the test trees of internal/lab, internal/orchestrator, internal/scheduler, and
    internal/daemon did not compile and the Blueprint corpus did not load, because
    6858429 replaced the `CandidateEstimates` stage fields without updating four test
    packages and 22 fixtures. `go build ./...` passed and `go vet ./...` did not, so
    every case that entry named as holding was runnable only in a concurrent session's
    uncommitted working tree. b8bca95 was the last commit that vetted clean, and the
    tree of record was green again at 33707f6. The entry recorded the pass as complete
    while naming the concurrent session, which is the part to not repeat: a commit that
    cannot run its own evidence is not evidence, whoever else is in the worktree.
  - Judgment calls. `EstablishedStart` still accepts a terminal phase, because a
    container observed only after it exited has a real start moment from a runtime that
    owns its lifecycle, and refusing exited phases would throw away every fast
    workload's measurement to compensate for a provider publishing the wrong field.
    The fix for that belongs where the wrong field is read. A node whose clock runs
    behind still hands over a start earlier than the truth; nothing here can detect it,
    and the start-latency row already names the pair it could not subtract. And the
    Booking clock's fallback reaches the Lab world only as the positive case, because
    that provider says running from the moment it accepts, so the first observation
    carries no start at all: the lane where a start arrives with the first running
    observation is the reusable one, and that is where the fleet case drives it.
- [x] 2026-07-26: Answer the review of the launch-waterfall commit. Two reviewers
  refuted parts of it. Six of the seven findings were real and are fixed at the
  source; the seventh is rejected below with the reason.
  - The readiness moment had no law. It was adopted from the workload verbatim: no
    bound against Mercator's clock, no relation to the container it is about, and no
    rule about which of two reports stands, so a report could file a readiness three
    and a half years in the future for a Run whose container nothing had observed
    starting, and a second report moved it backwards. That is the same foreign-clock
    defect `EstablishedStart` was written for one stage earlier, and it was not
    carried over. `runReportedData.establishedReady` refuses a moment later than the
    read that carried it and a moment before the recorded start, the ordering is
    checked at both events because the two moments come from two authorities and the
    workload's arrives first as often as not, and the first defensible moment stands.
    The report stays in the log whatever the rule answers.
  - Nothing could state the world where it fires, in three separate places. Both
    simulated worlds stated readiness on Mercator's clock even for the one machine
    whose clock is not Mercator's, so no fixture could produce an indefensible
    readiness; the corpus asserted readiness by reading the report rather than the
    record, so `no_ready_reported` meant no workload spoke rather than Mercator
    refused; and no invariant read readiness at all.
    `safety.readiness_is_reported_not_inferred` is the start rule over the last stage,
    four clauses written out rather than delegated, and `Session.RunRecord` makes the
    corpus assert what Mercator adopted. `a-clock-nobody-shares-is-not-a-start` now
    states that the readiness off the same wrong clock is refused too.
  - `ImageManifest.StartWork` charged a transfer and no assembly for bytes a host
    does not hold, so `imageStage` took its zero-bytes path and recorded the assembly
    as zero seconds at confidence 1.0. A Rental about to pull 18GB stated at full
    confidence that it owed no assembly, while the host beside it holding those same
    bytes unassembled was charged the 72 seconds, and both worlds contradict it: each
    spends its unpack whenever a launch fetched anything. A layer that has to arrive
    has to be applied, so a layer nothing holds owes both. Two consequences beyond the
    record were the reason to fix it at the source: a fetching candidate now carries
    half a point of doubt for the link and half for the unpack rate, which is the same
    point an enrolled machine that answered and holds nothing carries, and
    `max_p90_start_seconds` is enforced against a number that includes the assembly
    rather than structurally omitting it. `unpacked-is-not-the-same-as-pulled` states
    the whole rule in one world.
  - The per-stage fixture vocabulary was unvalidated. `LaunchStageEstimates` answers
    about an unknown stage with a zero `Estimate` from no source, so a misspelled key
    asserted zero seconds against a stage that does not exist and passed green, taking
    the assertion the fixture was written for with it: replacing this corpus's own
    600-second boot claim with the record's JSON key `boot_seconds` left the tree
    green. The key is `domain.LaunchStage` now and the same validation that checks
    artifact and cache vocabulary checks it against the eight.
  - `safety.prediction_is_recorded_against_its_actual` read the set of accepted
    launches off the stage durations those launches reported, so a launch that
    reported none was not a launch as far as the law was concerned. Deleting the
    world's stage accounting left the canonical execution green with all its
    invariants passing. The waterfall records which Runs had a launch accepted
    separately from which reported a duration, and the Bundle names the difference:
    `launch_reported_no_actual` rather than a measured zero.
  - Both worlds modelled only the happy path of readiness. Every started execution
    reported ready, and a fixture that declared no readiness spend got a report at the
    same instant its container started, so thirty Blueprints asserted by default that
    a running process is a serving one and the failure mode the stage exists to expose
    was unstatable. `application_never_ready` states it, the ledger says which stages
    a launch reached rather than leaving it to be read off a missing number, and
    `a-running-process-is-not-a-serving-one` plus
    `conformance/a-workload-that-never-becomes-ready` hold it at both levels.
  - Rejected: that an omitted `application_ready` should mean the world says nothing
    about readiness. Omission already means something in that block. Every other stage
    reads a missing duration as a world that spends nothing on it, which is how a
    Rental spends no acquisition and no boot, and a readiness of zero is a legitimate
    world where a process serves the instant it exists. Reading one field's silence as
    absence and its siblings' silence as zero would make a fixture's own sentence
    undecidable, and it would leave eight-stage completeness unstatable. What was
    missing was a way to say the opposite thing, which is what
    `application_never_ready` is.
  - The live-evidence list for the previous pass named
    `TestANodeReportsTheMomentItsContainerReallyStarted`, which runs against a
    scripted runtime with `PATH` emptied so no Docker is reachable, and would pass with
    the daemon uninstalled. The correction is in the phase 4 verification section
    beside the claim.
  - Judgment calls. The refused readiness is dropped rather than raised, for the same
    reason the refused start is: the report still records what the workload said, and
    failing a Run because its host keeps a skewed clock would take capacity out of
    service over a stage nobody can measure. Readiness gets no event of its own like
    `compute.run.execution_started.v1`, because the report is already an event and the
    adopted moment is a projection of it, so the invariant reads the projection. And
    this pass again ran beside a concurrent session in the same worktree, which had the
    tree non-compiling for stretches of it; every command below was run against a
    `git archive` of the commit under test with only this pass's files overlaid, so the
    evidence is the tree of record rather than a shared working directory.

- [x] 2026-07-26: Price a transfer from the bytes that are missing and the
  throughput of the path they cross. Every Artifact read in the fleet cost the same
  seconds on every machine, because both halves of the arithmetic read one constant
  per scope: reading forty gigabytes was 640 seconds beside the object store and 640
  seconds across the country from it, and no fact anybody published could change
  either number, because `domain.NetworkScope` had no object-store scope for a host
  to speak about that path with.
  - `NetworkScopeObjectStore` is the scope, and `OfferSnapshot.DownloadRate` answers
    per path where `RegistryDownload` answered for one. `LinkSpeed` carries where its
    number came from, exactly one of a measurement or a named assumption, so a reader
    can tell a machine Mercator measured from one it guessed about. The three
    assumptions are named constants, so a record says `assumed_object_store_rate`
    rather than 500.
  - `CandidateDecision.TransferRates` records the rate every stage that had bytes to
    move was priced at, with the bytes beside it. The seconds are bytes over a rate,
    the bytes were already explained by the locality evidence, and the rate was the
    half no reader could retrace. A stage with nothing to move records nothing,
    because there was no transfer to have priced.
  - Both simulated worlds spend the Blueprint's own declared path rate instead of a
    constant, and read it from the declaration rather than from the fact the offer
    publishes. That distinction is the point: how fast a path is and how much its
    publisher stands behind having measured it are different statements, so a fixture
    can state a path a host disowned and the world still crosses it at the speed the
    fixture said. `PathSpecs.LinkMbps` and `PathSpecs.PublishedFacts` are the one
    place each answer is derived, so one declaration cannot mean two transfer models.
  - An enrolled node measures the path it just crossed. `capability.HostFacts.Network`
    was declared in phase 2 and written by nothing, so the field the offer projection
    already carried and Placement already read had no producer. `PrepareArtifact`
    times its own copy and publishes the slowest reading it has seen as a p10, which
    is the pessimistic quantile every reader here asks for. Only the object-store path
    is measured: the daemon pulls images and reports neither the bytes nor the
    duration, so a registry rate derived from anything available would be an inference
    dressed as a measurement.
  - `safety.transfer_rate_is_attributed` is the Lab rule, and it is
    `safety.locality_provenance` for the other half of the arithmetic. Every transfer
    a decision recorded names the measurement or the assumption it was priced from,
    exactly one of the two, and a rate presented as measured has to be a number some
    host or path fact actually reported at that scope, one its own publisher still
    stands behind. It is stated over what the decision recorded rather than over the
    arithmetic: a rule that recomputed the seconds would be a second implementation of
    the predictor agreeing with the first.
  - Judgment calls. `AssumedUnpackMBps` became `AssumedUnpackMbps`, the same 250 MB/s
    restated as 2000 Mbps, so one unit and one arithmetic price every stage and one
    record holds every rate a candidate was priced at; assembly stays a rate of its
    own, because a machine on a slow link with fast disks is a real machine. Each
    simulated world keeps its own unmeasured constant, deliberately the same figure as
    the scheduler's assumption, because an unmeasured path is the one case where both
    halves are guessing about one thing; what separates prediction from actual is a
    fixture declaring a path. `MeasuredLinkConfidence` is a stated 0.9 rather than a
    function of the sample count, which would be an estimator this slice has not
    measured. And the two halves are two Blueprints rather than one, because a
    Blueprint carries exactly one of a placement fixture or an arrival plan and the
    Lab compiles only the second: `a-fast-machine-far-from-the-data-loses` at L0 and
    `conformance/a-path-somebody-measured-prices-the-read` at L1, over one world.

- [x] 2026-07-26: Answer the review of the measured transfer path. Two reviewers
  refuted parts of it. Three of the four findings were real and are fixed at the
  source; the fourth is half real, fixed as far as it goes, and rejected below where
  it does not.
  - A node's slowest reading could never retire. `pathMeasurements` kept a running
    floor and re-dated it on every later transfer, so the slowest reading a machine
    ever took was republished as a current p10 forever, stamped with the moment some
    other transfer finished. A node that read once at 100 Mbps while a container
    shared its link, and then read at a gigabit every half hour for twelve hours,
    published 100 Mbps observed at midnight, and the only exits were a full hour of
    moving nothing or restarting the agent. The commit message claimed the opposite
    property. Readings are kept one by one now and the fact is the slowest of the ones
    still standing, which is what retires the slow one: each reading serves its own
    hour and leaves the window whatever the node does afterwards, so a floor nothing
    can date is no longer what gets published. The date this entry then gave the fact
    was wrong, and the entry below corrects it.
  - `safety.transfer_rate_is_attributed` judged a historical decision against the
    current fleet. Its second clause read World Truth's offer list, so a machine
    legitimately gone by check time turned a correct placement into a reported safety
    violation, in the exact words the rule exists to say about a prediction that
    invented a measurement. The world writes down what it publishes now,
    `publishOfferFacts` keeps it after the machine is retired, and the rule asks it as
    of the decision's own `EvaluatedAt`. The second instance of the same defect was in
    the declaration: a fixture's path was stamped valid for a day from the world's
    start while offers are observed now, so any execution driven past twenty four
    hours watched every declared path go silent and flipped every measured rate
    already in its log into the same false violation. A declared path is a standing
    statement of the world, and a fixture that wants silence states a confidence of
    zero.
  - Nothing in the corpus held the two halves of the transfer model apart. The Lab
    world reads a Blueprint's declaration rather than the fact the machine published,
    and every fixture that declared a path declared one its host stands behind, so
    replacing the world's reading with a read of Mercator's own input left the whole
    suite green. `conformance/a-path-a-host-disowned-is-still-the-path` is the case
    that can tell them apart: the machine states no confidence in its own 200 Mbps
    path, Mercator prices the read from its fleet-wide assumption at 640 seconds, and
    this world spends the sixteen hundred the path really costs. That closed the Lab
    world, and this entry read as though it closed both. The fake adapter was still
    substitutable, and the entry below closes it.
  - What a node publishes is not the link, and now says so. The reading is timed over
    `io.Copy`, so it is the bytes crossing the path, landing on the disk, and being
    hashed on the way past; it is published as `node_artifact_copy` and
    `NetworkScopeObjectStore` states that a p10 over it is delivery. What is rejected
    is the reading of that as a false refusal. A machine on a ten gigabit path whose
    Artifact disk delivers four cannot serve a Run that states a floor of eight, so
    refusing it is the right answer and admitting it was the unmeasured guess this
    slice replaced. That semantics was settled here and constrained by nothing, since
    every download floor in the tree was over a registry; the entry below gives it a
    case at both fidelities. The rest of the finding stands: both readers of the fact
    ask how fast content reaches this host, and landing is part of reaching it. The
    expressibility half goes with it: a fixture's `p10_mbps` is that delivery rate,
    which is why one declaration is enough, and a host that publishes something other
    than what it delivers is stated through the confidence it puts on its own number.

- [x] 2026-07-26: Close the transfer-path slice on the two halves it left open: what
  the corpus says about a machine nobody measured, and what an admitted assumption is
  allowed to be worth. The measured half was held at L0 and L1 and the fallback was
  held by unit tests over the domain rate and by one conformance world about a host
  that disowned its own reading, so no Blueprint said that silence about a path is
  priced rather than refused. And `safety.transfer_rate_is_attributed` was a rule about
  provenance only, which left the shortest route to the outcome it exists to stop
  wide open.
  - `rental-nobody-measured-the-path-of` is the third machine in
    `a-fast-machine-far-from-the-data-loses`. It declares no path at all, which is the
    silence itself rather than a rate stated at zero confidence, and it is charged the
    stated prior over forty gigabytes at `assumed_object_store_rate` with the estimate
    capped at what a guess is worth. What it pins is two constants,
    `DefaultObjectStoreDownloadMbps` and `AssumedLinkConfidence`. This entry also
    claimed it made the determinism claim falsifiable at L0, and the entry below
    corrects that: it contributes nothing under that break.
  - The rule's third clause is that an unmeasured transfer is worth at most
    `domain.AssumedLinkConfidence`, asked of the rate and of the stage estimate it
    produced. Naming the assumption truthfully and then stating the duration at full
    confidence satisfied both existing clauses and bought exactly the ranking a
    fabricated measurement buys, by the route a prediction slice is likelier to take:
    raising a confidence reads as tuning a constant rather than as inventing a source.
    Both halves are asked because they are two mistakes. The rate is what the next
    caller divides by; the estimate is what this decision's own uncertainty term
    already charged doubt from.
  - Deferred, and the reason is the runtime rather than the plan. A node still cannot
    measure its own unpack rate. `PrepareImage` is `docker pull`, which interleaves
    downloading and extracting per layer and reports neither the compressed bytes nor
    a phase duration, so any rate derived from it would be a measurement of fetch plus
    unpack published under the name of a measurement of unpack. That is the same
    inference this tree already refuses for the registry path, and `AssumedUnpackMbps`
    stays the stated assumption until something can separate the two. Adding a
    per-offer unpack fact with no producer was rejected for the reason the last review
    gave: `capability.HostFacts.Network` sat declared and unwritten since phase 2, and
    a reader with no writer is the defect, not the fix.

- [x] 2026-07-26: Answer the second review of the measured transfer path. Two
  reviewers refuted four things about the entry above. All four were real, three in
  the production code and one in what this document claimed.
  - Dating a node's published p10 by its slowest reading made the same fact fail the
    freshness bound it exists to serve. A node that read at 100 Mbps at noon and at a
    gigabit every minute until one o'clock published a measurement dated noon with
    sixty samples under it, so a Run stating `max_measurement_age_seconds` of ten
    minutes read the machine as having published nothing about its link and struck it
    out on a floor its last fifty-nine reads cleared twenty times over. One fact is a
    quantile over a window and never one transfer, which is what `SampleCount` has
    always said, so what dates it is the moment its evidence ends. The value is still
    the slowest reading standing, the slow reading still retires because each reading
    serves its own hour, and the fact now expires with the newest reading, which is
    the moment the window would empty. Expiry and collection are one statement
    instead of a constant chosen twice.
  - One published fact was read at two moments. `OfferSnapshot.DownloadRate` priced
    the transfer as of the offer's observation and `NetworkDownloadRequirement.Answer`
    decided the Run's floor as of the decision, so a fact its publisher stopped
    standing behind in between was both things at once: the record refused the
    candidate because nobody had published a download p10 for its link and priced its
    pull at 750 Mbps measured by that same publisher. `safety.transfer_rate_is_attributed`
    then reported a decision taken by the scheduler's own documented rule as a
    fabricated measurement. The moment is the caller's now and every caller passes the
    decision's, which is the only one the Booking Decision carries and the honest
    question anyway. A Run's `max_measurement_age` stays with the floor alone, because
    it is that Run's policy about what it will be placed on rather than a statement
    about the fact, and a reading one Run refuses is still the best evidence Mercator
    holds about how long the transfer takes.
  - The fake adapter's transfer model was still Mercator's own input. Replacing
    `simLinkMbps` with a read of the facts the offer publishes left all thirty-six
    regression Blueprints green, verified on this host, and the two fixtures that
    declare a disowned path moved their image bytes at the 500 Mbps fleet assumption
    rather than the 5000 they state. `a-world-crosses-the-path-its-host-disowned` is
    the fixture that notices: one Rental holding nothing, an 18GB image, and a four
    gigabit registry path the machine disowns, so Mercator prices the pull at 288
    seconds and this world spends the thirty-six the path really costs. The start it
    asserts is the world's own moment, and under the substitution the Run starts 288
    seconds in.
  - The delivery semantics the last entry settled had no executable case. Every
    download floor in the tree was over a registry, so what a Run asking about its own
    dataset receives was decided by a rule no fixture could reach, and the corpus
    could not state `max_measurement_age` at all.
    `a-floor-on-reading-the-data-is-a-floor-on-delivery` states both: three Rentals
    holding one image at one price, one delivering ten gigabits, one delivering four,
    and one delivering ten that measured a week ago. A Run needing eight refuses the
    four and names the four, refuses the week-old reading and says nobody answered,
    and takes the third. `PathSpec.measured_ago` is what lets one world hold a machine
    that measured lately beside one that did not; it states no expiry and adds none,
    because how old a reading a Run will act on is the Run's policy rather than the
    path's. `TestAFloorOnReadingTheDataIsAskedOfWhatThisNodeDelivers` is the same
    claim against a real node and a real object store: two Artifacts replicated out of
    MinIO on this host's daemon, a p10 over the two transfers, and a Run refused the
    floor above what the machine delivered and served the floor below it. The node
    measured 12426.40 Mbps of delivery, and dropping the floor comparison admits it to
    a Run asking for twice that.

- [x] 2026-07-26: Answer a launch stage from what earlier launches of the same
  candidate really spent, at a declared level of a hierarchy. `internal/prediction`
  files every measured launch under `domain.CandidateIdentity` and answers a stage
  from the narrowest level holding samples: this exact candidate, this provider in
  this region, this provider, then the prior the rest of the tree already computed.
  Every stage estimate records the level, the sample count, and the key the samples
  were read under, so an answer that cannot be audited cannot be written.
  `SchedulingInput.LatencyEstimates` is deleted rather than kept beside it: it was
  keyed by offer snapshot ID, nothing ever wrote it, and a start latency replayed onto
  a machine whose locality has changed is the wrong number for a stated reason.
  - Vast's `machine_id` reaches the offer. It was decoded and read by nothing, so a
    catalog of other people's hardware keyed a region full of identical 4090s as one
    candidate and served each host's launches back as evidence about the others.
  - One stage has an actual today. Readiness is bounded by two moments Mercator
    observes from independent authorities; the other seven happen inside one observed
    interval and stay predicted from published claims and stated constants, named on
    the record as the prior they are.
  - A Blueprint can now state the machine behind a marketplace listing and the
    readiness of one machine rather than of the whole world, which is what lets a
    fixture publish one machine under two ask IDs and make the levels answer
    different seconds.
- [x] 2026-07-26: Answer the third review of the transfer path. Two reviewers refuted
  four things about the entry two above. Three were real and one was two claims, one
  of which is refuted below.
  - The slice's central claim was false in the production code its fixture certified.
    A candidate's established start counted every second an inventory could account
    bytes for, whatever divided them, so a machine that enumerated its copies
    perfectly and has never measured its path to the object store was charged the
    fleet-wide prior over forty gigabytes and struck out `LATENCY_SLO_EXCEEDED`. The
    fixture could not notice because it declares no start bound, and
    `safety.locality_is_never_infeasibility` could not either, because it measured
    silence purely in unknown-locality bytes and returned zero here. Established now
    means both halves of a duration are somebody's: content an inventory answered
    about, crossing a path some machine published a reading of. Nothing to move is
    still nothing to wait for at any rate, and a machine that measured its own link
    and is slow on it is still refusable, which is what a bound is for.
    `a-start-bound-refuses-only-what-it-can-prove` states it at L0 and
    `TestAStartBoundRefusesOnlyThePathThisNodeMeasured` states it against a real node
    reading real content out of MinIO on this host's daemon.
    `silence-is-not-infeasibility` now publishes a registry reading for the Rental it
    strikes out, at exactly the rate Mercator would have assumed about it, so the
    seconds are unchanged and what the fixture turns on is that a machine said them.
  - The Lab rule reads the transfer rates a decision recorded as well as its
    localities, and charges a stage priced from an assumption its whole price. A stage
    that suffered both silences is counted once at the larger share.
  - The third clause of `safety.transfer_rate_is_attributed` capped the stage
    estimate's confidence and not `CandidateDecision.Confidences`, which is what
    `Uncertainty` reads and what the score charges doubt from. The two are built
    separately, so stating an assumed read as certain in the list while leaving the
    estimate honest passed the whole `internal/lab` package. The clause now asks all
    three readings, and `domain.LaunchStage.ConfidenceAnswer` replaces the three
    strings that spelled the mapping out independently in the scheduler, the reference
    model, and the rule.
  - The claim that the third machine made the determinism claim falsifiable at L0 was
    false and the same commit said so two paragraphs later. Verified on this host:
    with `DownloadRate` stripped of its fact read, the fixture reports eleven failures
    and none of them is `rental-nobody-measured-the-path-of`, and the placement fell to
    `rental-far-from-the-data` before that machine existed. The entry above is
    corrected in place.
  - Refuted. A measured path and an unmeasured one were said to be compared as if they
    were the same statistic, so a host that publishes an honest slow reading is gated
    out where an identical silent host is admitted. The gating half is the rule
    directly above and is deliberate: a bound refuses what is known and never what is
    guessed, and a machine that measured 200 Mbps is known. The statistic half is
    consistent as stated: `DownloadRate` is documented as the pessimistic quantile and
    the standing prior answers that same question, so both reach `LinkSpeed.Mbps` as a
    p10 and nothing in the tree treats them as different statistics. What is open is
    calibration, not correctness: nothing has yet measured whether 500 Mbps is a
    pessimistic prior or an optimistic one, and a host whose true p10 is under it is
    ranked worse for having published it. That is a fleet-wide repricing to be made
    against measurements the calibration slice will hold, and the counterweight to it
    is already charging. `scheduler.Evaluate` sets `Weights` from the Run's class and
    `ServiceClass.Weights` prices doubt at sixty seconds of that class's own waiting
    rate, so a standard Run pays 0.03 USD for every point of confidence shortfall a
    candidate carries, and an unmeasured link is half a point of it on every stage it
    priced. Whether 0.03 is the right price is the same open calibration question as
    the 500.

- [x] 2026-07-26: Answer the fourth review of the transfer path. Two findings, both
  real, and both in this document rather than in the code it describes.
  - The entry above rested twice on the uncertainty term being dead, and this same
    document records the slice that turned it on. `scheduler.Evaluate` has set
    `Weights` from the Run's class since the service class landed, and
    `ServiceClass.Weights` prices doubt at sixty seconds of the class's own waiting
    rate. Verified here by zeroing `UncertaintyPenaltyUSD` alone: four green
    Blueprints fail on their recorded scores, `uncertainty-is-priced-once`,
    `the-service-class-decides-what-wins`, `a-published-risk-history-ranks-nothing`
    and `a-disowned-fact-is-not-an-answer`, and so does the Lab law that reproduces a
    score from the record. Both sentences are corrected in place. The three earlier
    mentions of terms multiplied by zero are histories of what the class replaced and
    are left alone.
  - `silence-is-not-infeasibility` claimed both halves of the struck-out Rental's five
    minutes were somebody's measurement. A fifth of them is assembly, priced over
    `AssumedUnpackMbps`, which `UnpackRate` stamps as an assumption because nothing in
    the fleet measures a host's storage, so `establishedOverAMeasuredPath` takes every
    unpack second out of what a bound may refuse. Measured here: the Rental is charged
    289.14 seconds of fetch over its published 500 Mbps and 72.16 of assembly over the
    constant, and its established p90 start is 434.71 against a whole prediction of
    542.95. The fixture passed at three minutes on the fetch half alone.
  - That is the rule working rather than the bug the entry above reports fixing, so
    the code is unchanged. Refusing a Run's only capacity for seconds derived from a
    constant of Mercator's own is exactly what the slice stopped doing for registry
    links, and assembly is that same guess made about every machine at once. The
    candidate still pays those seconds in the score, so it never outranks a machine
    that will really be faster, and the decision records `START_SLO_UNVERIFIED` rather
    than a promise. `stagePredictor` already puts a measured launch into both halves,
    so the stage becomes refusable the moment anything has watched it.
  - What was missing is that nothing said so. The Blueprint gained a second Run at
    seven and a half minutes, where the measured fetch is inside the bound and the
    whole prediction is not, asserting the Rental feasible, the placement
    `START_SLO_UNVERIFIED`, the fetch rate as a measurement and the unpack rate as
    `assumed_unpack_rate`. Two breaks fail it, each of which strikes the Rental out on
    a constant of Mercator's own: stating the unpack rate as a measurement, and making
    `establishedOverAMeasuredPath` return what it was given, which is what the tree
    did before the slice above.
  - Open, and named rather than fixed. No hard start bound can act on assembly for a
    candidate the fleet has never watched, whatever the image size, so a Run whose
    start budget is mostly unpacking is admitted unverified rather than refused. The
    answer is a measured unpack rate, which is a node slice and a calibration
    question, not a constant promoted to a measurement here.
- [x] 2026-07-26: Stop answering a transfer from a launch history. Two reviewers
  refuted the transfer-path repair above at its own trigger. Four of five findings
  were real, and the central one is that the repair had fixed the wrong half.
  - The estimator was the lying half. `stages.Answered` replaced a transfer's
    prediction with what measured launches of the candidate spent, with no regard to
    what this launch has to move, so a machine holding a verified copy of the whole
    forty gigabyte dataset was charged 920 seconds and refused `LATENCY_SLO_EXCEEDED`
    at `Offered 937.25` against a bound of 180, with `Locality:hot FetchBytes:0` in the
    same record. The image side is the same defect: a host reporting every layer was
    charged the pull it had already performed. A transfer is a byte count over a
    throughput and the byte count belongs to the launch, so `image_fetch`, `unpack` and
    `artifact_fetch` are filed under no key at all and the rate the entry above deleted
    comes back. Nothing measured is lost, because what recurs about a transfer is the
    throughput, and an enrolled node already publishes that as a fact with a validity
    window.
  - `safety.locality_is_never_infeasibility` was failing a lawful refusal at the same
    trigger, because `pricedSilenceSeconds` multiplies a share of bytes by seconds
    those bytes did not produce. It needs no clause of its own once no transfer's
    seconds arrive from anywhere else, and the Lab refuses the answer outright instead,
    in `safety.prediction_states_its_provenance`, so no other law has to ask first
    whether the seconds it reads are a transfer's seconds.
  - The rate law was silent by omission about the transfers a decision recorded no rate
    for at all, which is a cheaper way out than inventing a measurement.
    `everyTransferNamesItsRate` reads the seconds instead of a byte count, and fails on
    each of the three stages on its own.
  - The corpus can catch the change that brings it back.
    `history-answers-for-the-machine-it-was-measured-on` asks its two measured machines
    what they will spend pulling, and the answer is the prior with the throughput it was
    divided by. It cannot fail on this tree, because nothing in a Lab world produces a
    timed transfer; it fails the day `Launch.Observations` emits one and the estimator
    files it, which is exactly the regression review named.
  - The disk conformance case released half a gigabyte inside its own measured window,
    by removing an interrupted run's leftover container after taking the reading it
    compares against. Reproduced here as `fell by 0 bytes` with the node correct both
    times.
  - `Registry.draining` and the `OpenSession` refusal it gates were asked for by
    nothing, and both are the difference between a fifteen second shutdown and a clean
    one. Two cases at the registry now fail without them.
  - Open, and named rather than fixed. A transfer becomes learnable again when the key
    names what a transfer is, the bytes this launch is missing and the path they cross,
    which is the slice where a node reports the stages it performs. Until then a timed
    fetch is filed nowhere rather than filed wrong.
- [x] 2026-07-26: Answer the second review of the risk-history commit. Two reviewers
  refuted seven claims on it, five of which the first review of that commit had
  already fixed and which reproduce nowhere in this tree. The two that survive are
  both about the same thing: a rule the code obeys and nothing enforces, and a rate
  the corpus can state and no world can produce.
  - What a score may doubt is a law now. The reliability entry was removed from both
    models' confidences, and the rule that removal rests on was left stated in three
    comments and pinned by two fixtures, which is the exact shape the defect survived
    a phase in. `domain.ScoredAnswers` declares the questions the score reads an
    answer to, which is the capacity claim and the eight stages of a launch, and
    `safety.doubt_only_the_answers_the_score_reads` holds every recorded decision to
    it. What it forbids can only ever run one way: a stated confidence is charged and
    a silence is not, so doubt about an answer the score never reads penalises the
    publisher that measured its machine, leaves alone the publisher that said nothing,
    leaves alone the publisher certain its machine refuses every start, and ranks the
    machine nobody measured above all three. `safety.score_is_reproducible_from_the_record`
    could never see it, because both models charged the same doubt and the score
    reproduced from the record exactly. Restoring the entry to the production scheduler
    fails the L1 execution through the new rule, naming the candidate, the tenth of a
    point, and the answer it was about.
  - The corpus can produce a refused start now. `RequestSpec.max_pre_start_attempts`
    is the missing vocabulary: the API has carried the bound since launches could
    fail, no Blueprint could state it, so every Run in this corpus was normalised to
    one attempt and closed the moment any machine turned it away. A Run placed again
    on other capacity is the whole consequence a refusal has, and the term that will
    price a published refusal rate is a probability times the start of exactly that
    redo, so the successor slice had no world to be falsified in.
  - `a-published-rate-is-not-what-a-machine-does` is that world at L1. Two listings
    at one price and one warmth. The Run takes the machine whose provider measured it
    and never saw it refuse a start, because the histories rank nothing and the offer
    ID decides, and the world refuses that launch through the fault the corpus has
    always had for a provider that rejects a command. Mercator strikes the machine out
    with `PREVIOUS_ATTEMPT_CAPACITY_UNAVAILABLE` and places the Run on the listing
    whose provider published the worse record, where it runs and succeeds. Both
    decisions record both histories unchanged, which is the second claim: a rate is
    what a provider measured and published, Mercator measures nothing about machines
    on its providers' behalf, and a refusal that really happened moves neither number.
    Three breaks fail it: removing the fault, removing the attempt bound, and stopping
    the Lab world publishing the history at all.
  - Rejected, with evidence. Five findings describe the tree as it was at that commit
    and not as it is. The reliability confidence left `scheduler.confidences` and
    `lab.referenceConfidences` in "Doubt only the answers the score reads"; the
    vocabulary moved from `RentalSpec` to `MarketplaceOfferSpec`, where the one
    production publisher actually attaches it, in "Make a launch eight predicted
    stages"; `ReliabilityEvidence` became two independently stated rates and
    `vast.interruptionHistory` stopped inventing a start failure rate at full
    confidence, with `reliability2` a pointer so a silent ask publishes no history,
    in "State a rate somebody measured". A sixth is refuted outright: the Lab world
    has been able to refuse a launch since the fault vocabulary existed, through
    `provider.launch` and `reject_command`, and `provider-rejection-single-run` is a
    Blueprint in the corpus that does it.
  - Open, and named rather than fixed. No world can interrupt work it is already
    running. `ExternalPhaseFailed` is terminal in the orchestrator, so an interrupted
    Run would close failed rather than be placed again, and there is no interruption
    policy for a fixture to be about: what a published interruption rate costs cannot
    be stated until Mercator decides what it does when a machine drops a running
    workload. Simulating one now would state a world the control plane has no answer
    for.
- [x] 2026-07-26: Stop the advance loop the queue left spinning. Admission turned a
  Run no candidate would take from an error into a deferral, and `stepAdmit`
  reported that placement had moved the Run whatever placement did. A deferral the
  record already carries appends nothing, so `AdvanceRun` re-derived the same state
  and deferred again without end: one submission to a control plane with no capacity
  burned a core inside its own HTTP request, held that Run's lock for as long as the
  caller waited, and never answered. It reproduced on this workstation as
  `TestANodeThatCannotMeasureItsDiskWinsNoPlacement` timing out at ten minutes with
  `internal/daemon` failing the whole suite, and it is a production hang rather than
  a test one: the stack is `CreateRun` through `Intake` to the same loop.
  - `stepPlace` reports whether the Run moved. A placement that selected nothing has
    not moved it: admission has queued it and the next tick asks again. Retry
    exhaustion still reports progress, because closing a Run is a transition the loop
    has to reduce.
  - `TestARunNothingCanTakeWaitsInsteadOfSpinning` holds it, and the bound is what
    states the claim. The fixed loop answers in milliseconds; the broken one only
    stops when the deadline kills the query it is in the middle of, which is what
    makes the case a failure rather than a hang. Reverting the fix fails it with
    `advance a Run nothing can take: orchestrator: read the launch history: context
    deadline exceeded`.
  - Two daemon cases asserted the contract admission replaced. `refuseToPlace`
    expected 502 from a refresh, and a Run nothing can take is now queued with the
    reason on the Run itself, so `queueForWantOfCapacity` reads the phase and
    `NO_FEASIBLE_OFFER` off the daemon's own answer. That is a stronger assertion
    than the status code was: 502 said only that something had failed.
  - Judgment call. The loop and the two stale cases belong to the admission slice,
    which has no entry in this log and left `internal/scenario` changes uncommitted in
    the worktree. They are recorded here because they were found while verifying the
    launch waterfall, and a branch whose suite cannot finish cannot verify anything.
    The uncommitted scenario work was left exactly as it was found.
  - Refuted, and repaired in the entry below. Two reviewers showed that the claim held
    only for a workspace with one Run in it, that the queue it ratified stalls a fleet
    with room for other work, and that the corpus could state none of it.

- [x] 2026-07-26: Make a wait admission records answerable and passable. Two reviewers
  refuted the entry above. Each defect is one the record could have been read for and
  nothing read it, and each is fixed at the stage that produces it.
  - The command a deferral was appended under was named after the reason alone, so it
    was spent on the first Run that reason applied to. The second time admission said
    the same thing about a changed queue, the append replayed that key with a different
    request hash, the event log refused it as an idempotency conflict, and `AdvanceRun`
    returned that error to every caller for as long as the state held: the refresh
    answered 502, the reconcile sweep logged it every tick, `stepAdmit` never reached
    `stepPlace`, and the Run's own record stayed frozen at the stale answer. Recording
    the nth admission decision about a Run is a distinct command from recording the
    (n-1)th, so the key now carries the number the event ID already did.
  - The queue ordered each Run against every wait worth more than its own and never
    asked whether the work in front of it was waiting for anything that can arrive, so
    one Run asking for more room than any machine in the fleet has emptied the fleet.
    A Run refused by every candidate is now recorded in one of two waits.
    `NO_FEASIBLE_OFFER` is a wait for capacity to come free, and the record points at
    what holds it. `NO_CAPACITY_FITS` is a wait for capacity to be added, where every
    machine the fleet published was weighed against this Run and none of them holds a
    queue it could be waiting on. Work behind the first waits for the same machine.
    Work behind the second is only stopped by it, and it is admitted past it.
  - A fleet that published nothing at all is the first wait and not the second. Nothing
    was weighed, so nothing has been established about what would hold the Run, and the
    order the classes declare is what should decide the first machine to arrive.
  - The two waits are separate reasons rather than a flag beside one reason. It is what
    an operator acts on, "add capacity" against "wait", it is what the queue reads, and
    the existing suppression already appends a fact when the reason changes. The flip
    is also what makes the rule self-correcting: a machine that is both occupied and
    too small holds the queue only while the record can project when it comes free.
  - `BEHIND_HIGHER_CLASS` is now `BEHIND_HIGHER_PRIORITY`. The ordering was always on
    effective priority, so a Run held behind older work of its own class was told it
    was behind a higher class, which is not what the record said.
  - The deadline is now asked of every wait rather than only of the wait Placement
    causes. A Run held behind work that outranks it was told to wait again every tick
    for ever, past the moment its own class says the answer stopped being worth having,
    and the queue in front of it was the one thing that could keep it there. An
    interactive Run behind an experimental one is where the aging curves cross, and
    that is the case the new test states it against.
  - The executable specification now constrains all of it, which is what the reviewers
    found missing. `internal/scenario` can state a `defer` and a `refuse` outcome with
    the reason, the whole set of work the Run waits behind, its effective priority, and
    how long it has waited. `an-impossible-ask-holds-no-queue` and
    `a-queue-restates-what-it-waits-behind` are green regression Blueprints, and
    `conformance/an-impossible-ask-empties-no-fleet` drives the same world through the
    real control plane in the Lab.
  - `TestAnImpossibleAskLeavesThisFleetRunning` is the same claim through the public
    API against the daemon's own fleet, and the order is what makes it: the impossible
    Run is submitted first, so it is the older wait and outranks every later arrival of
    its own class. The existing case submits them the other way round and never had a
    wait for a later Run to be ordered behind.
  - `safety.nothing_waits_behind_an_impossible_ask` is the law: Mercator never tells a
    Run it waits behind a Run the record already said no machine in the fleet can take.
    It is replayed out of the public log, it has its deliberate failing case in the
    registry, and reverting the queue fix fails the conformance execution on it.
  - `safety.service_class_admission_order` had to be amended rather than left alone,
    which is the Lab refusing the fix until the law was stated properly. It forbade
    admitting the Run that fits while the impossible Run outranked it, and
    `liveness.aging_prevents_starvation` forbids leaving that machine idle, so the two
    laws contradicted each other on exactly this world. The ordering is over Runs
    waiting for capacity, and a Run waiting for capacity to be added is not in that
    queue.
  - Judgment call. The corpus entry lands with the repair rather than before it. This
    is a refutation response to a committed slice, and a Blueprint committed first
    would have been a red commit stating a law the branch did not hold yet. Every claim
    here has a case that fails without its fix, verified one at a time: the changed
    wait as an idempotency conflict, the impossible ask as an idle machine beside a Run
    that fits it, the deadline as a Run still queued ten minutes past it, and the
    suppression as two identical facts over two advances.
  - Still open, and deliberately not smuggled in here. A Run nothing in the fleet can
    hold stays queued past its class's maximum queue delay until its deadline refuses
    it, and `liveness.aging_prevents_starvation` calls that starvation on any execution
    that observes it inside that window. What admission should do at that bound is a
    decision about refusal policy rather than about the queue's order, and it needs its
    own slice.
  - Refuted, and repaired in the entry below. Three of the claims here held only for a
    fleet of one machine, which is the only shape anything recorded in this entry ever
    built. Both waits were classified by asking whether any candidate carried a Rental
    Schedule, so the exemption switched off for the whole workspace as soon as anything
    was running anywhere in it; the "self-correcting" bullet has the defect backwards, a
    machine that is both occupied and too small holds the queue for exactly as long as
    it is occupied; and the law could fail only on the ordering step, because it decided
    which Runs the ordering exempts with the same predicate the production code did.

- [x] 2026-07-26: Classify a wait from what the fleet refused. Two reviewers refuted the
  entry above. The queue read the two waits off a proxy for what each machine is doing,
  and every fixture in the slice weighed the fitting Run against an idle fleet, which is
  the one state in which that proxy agrees with what the machines actually said.
  - `NO_CAPACITY_FITS` was assigned when no candidate carried a Rental Schedule, and
    `scheduler.scheduleEvidence` attaches one to every candidate whose Rental holds a
    Booking whether that candidate is feasible or not. So one machine busy anywhere in
    the workspace recorded every unplaceable Run as a wait for capacity to come free,
    which keeps the queue, and a Run that fitted an untouched machine was ordered behind
    an ask nothing in the fleet can satisfy until that ask's four hour deadline cleared
    it. Because the classification is re-derived every tick, a continuously busy machine
    kept the exemption switched off for the whole fleet.
  - A refusal now states where it is made whether waiting ends it. Capacity the offer
    says is spent, a Rental Schedule with no open Booking position, and an offer an
    earlier launch attempt found unavailable are capacity somebody is spending and it
    comes back. Everything else refused for what the machine is or for what nobody
    published. `domain.CandidateDecision.CouldHoldOnceFree` is the question the queue
    asks of one machine, and the two waits are the answer summed over the fleet.
  - Room is deliberately a refusal waiting does not end, which is a judgment call.
    Nothing in this tree collects garbage, Mercator observes no other tenant's content
    and commands no removal of it, so a machine short of room is short of room until
    somebody adds a disk. It is also the safe direction: a refusal wrongly said to end
    on its own makes later arrivals wait behind a Run the machine may never take, and a
    refusal wrongly said to be permanent only stops that Run from holding the queue,
    where its class bound and its deadline still govern how long it waits. When a
    runtime reclaims space, what it reclaims will be a fact `Violation.EndedByWaiting`
    can be set from.
  - Everything a wait says is now answered over the machines that could hold the Run
    once free. What it waits behind, because naming work on a machine that will refuse
    it again tells an operator to wait for nothing. And the projected wait, which is
    what the deadline rule reads: a Run needing 900GB of a 200GB machine inherited the
    six hours somebody else declared as a max runtime on it, and was closed
    `DEADLINE_UNREACHABLE` at its first pass with zero seconds queued, while the
    identical submission against the identical idle machine was queued and kept for four
    hours. `a-wait-nobody-can-end-is-not-a-missed-deadline` is that case.
  - Only Placement may set what a Run is waiting for. A Run held behind work that
    outranks it weighed no machine at all, so that deferral leaves what the fleet last
    said standing. Otherwise the exemption undid itself: an impossible ask holds no
    queue, so it is itself ordered behind the first Run that outranks it, and reading
    that ordering as an answer about capacity put the ask back in front of the fleet it
    can never use. `a-wait-the-queue-caused-says-nothing-about-capacity` states it.
  - A deferral records the fleet it was measured against: how many machines were weighed
    and how many of them could have held the Run. The reason alone cannot be checked
    against anything, and a Blueprint can now state both.
  - `safety.nothing_waits_behind_an_impossible_ask` is stated over that evidence rather
    than over the reason, which is what the second reviewer's mutation showed it had to
    be. Deleting the `NO_CAPACITY_FITS` branch reinstated the fleet-emptying defect in
    full and every one of the thirty-six laws passed, because the law computed
    impossibility with the same predicate the production code decided the queue with.
    It now fails by name on exactly that mutation. `safety.service_class_admission_order`
    reads the same evidence, so the two laws cannot disagree about which Runs are in the
    queue.
  - The corpus states the busy fleet at every level.
    `a-busy-fleet-holds-no-impossible-ask` is the reviewer's own world: two machines,
    one working for another 45 minutes, and the 50GB Run takes the idle one.
    `conformance/an-impossible-ask-empties-no-fleet` is two machines with one of them
    five hours into work of its own, and it now asserts that the Run that fits was never
    told to wait at all. `TestAnImpossibleAskLeavesABusyFleetRunning` is the same claim
    through the public API against a node that is already running something.
  - Every claim has a case that fails without its fix, verified one at a time by
    mutating the production code and running the fixture: the classification as three
    green Blueprints and the daemon case, the deadline as a Run refused at its first
    pass, the ordering clause as a record naming the impossible ask, and the law as a
    named invariant failure at L1.
  - Still open, unchanged by this repair. A Run nothing in the fleet can hold stays
    queued past its class's maximum queue delay until its deadline refuses it, and
    `liveness.aging_prevents_starvation` calls that starvation on any execution that
    observes it inside that window.

- [x] 2026-07-25: Answer the second review of the prewarming commit. Two
  reviewers refuted four things and every one of them held: an image's
  preparation content was the empty string whenever nobody pinned the image, the
  policy was enforced per workspace while the invariant and this plan claimed a
  fleet-wide bound, the rate clock was lost with the process that kept it, and no
  production deployment could reach the bound at all because the only caller was
  a sweep slower than any interval an operator would state. A Run whose image
  names no content is refused at intake; preparation is one pass over the fleet
  with one budget and one clock; the clock is a durable row; and preparation runs
  when a Booking, a launch, a withdrawal, or a closure changes what Mercator
  wants prepared, with the sweep left as the only timer because a desire also
  changes when a predicted start elapses and nothing is recorded then. The
  two-tenant Blueprint also caught the Lab world reading one tenant's desired set
  as the whole fleet's, which had made the concurrency bound unfailable in the
  only world where it matters. Evidence and the deliberate breaks are under
  "Phase 3 prewarming, the second review".
- [x] 2026-07-25: Withdraw producer affinity, and stop the Lab keeping a copy of
  a Run's own output. Two reviewers refuted the affinity slice and both were
  right, so it is gone rather than argued for. The discount fired only where a
  machine had said nothing about its Artifact copies, and no machine Mercator runs
  says nothing: `cmd/mercator-node` always builds the runtime with an artifact
  root, so `nodeagent.artifacts` reports an enumerated inventory, and
  `internal/node` is the only source of reusable-lane offers. The producing host
  therefore took the enumerated-and-holds-none branch and was charged the full
  read exactly like a machine that never saw the bytes. A preference that provably
  changes no placement is the dead-weights failure in another costume, and this
  plan had recorded it as delivered.
  - The precedence was not the bug, so reordering it would have been worse. One
    host's replica store is the only place a copy a Run may read can be. A
    workload writes its output inside its own container, nothing files that
    content as a replica, and bytes no verification ever touched are bytes no
    consumer may be sent to read. So a host that enumerated and found no copy has
    answered about every copy anybody could use. When a publication does file a
    producing host's own output as a checked copy, that machine will be warm on
    its own inventory and will need no record to be preferred.
  - Deleted: `ProducedOnRentalID`, `ProducedOn`, `ArtifactEvidence.ProducedHere`
    and its public field, the `produced_here` estimate source and its zero
    confidence, the Blueprint `produced_on` and `produced_here` expectations with
    their validation, the `enumerates_artifacts` knob in both simulators, and the
    three fixtures and two tests that existed to make the discount fire.
    `ArtifactFetchWork` takes an inventory again, because that is all it reads.
  - The simulated world was keeping a verified replica of every Run's output on
    the host that computed it, which is what let the affinity conformance report a
    saving at all, and which a real node contradicts: this slice's own live test
    on this workstation asserts a node reports zero replicas after a real
    container wrote its output. A workload's output is now `artifact.written`, the
    fourth Artifact operation beside read, replicated, and published, and it is
    what the durability gap is measured from. Only a fetch Mercator issued leaves
    a replica, so `safety.artifact_replica_verified` drops its second shape and
    reads simply: a copy of a version nothing published is content from nowhere.
  - The corpus keeps both halves of what a copy is worth, on copies that can
    exist. `artifact-must-be-durable-before-a-consumer-runs` gains a fourth Run
    that reads what the previous Run's fetch left and checked, and the vertical
    demo's consumer now reads two inputs, a dataset a fetch put there before the
    world started and the checkpoint its own producer wrote, so
    `warmth_observed` asserts no read for the first and the whole read for the
    second. A checkpoint that asked only for warmth would pass on a decision that
    had invented some.
  - What survives from the withdrawn slice is what was true.
    `OfferSnapshot.UncertaintyPenalty` is still one quantity said once, because
    the oracle and the scheduler had drifted on two of its four terms and agreed
    only through a weight that is zero everywhere. The live test survives as the
    fact the withdrawal rests on. `ScoreWeights` still gains no field, for the
    reason it gained none before.
  - What this leaves owed, and it is the one gap under everything above: a
    verified replica in a node's replica store is not reachable from inside the
    container a Run executes in.
    `LaunchWorkloadCommand.ArtifactMounts` declared the attachment and was
    populated by nothing and read by nothing, so it is deleted rather than left as
    a promise. The zero seconds Placement prices for a host holding a checked copy
    is therefore a specification ahead of its implementation, exercised at L1 and
    not collectible by any production workload, and
    [#171](https://github.com/benngarcia/mercator/issues/171) owes the attachment,
    the way a workload learns which of its inputs are local, and a live
    conformance test on a real daemon. No entry here may call artifact locality a
    saving a Run collects until that lands.
- [x] 2026-07-25: Read a host-local copy only when it is the version the catalog
  names. The simulated world decided a copy was worth reading from the copy's
  own state alone, in what a launch reads and in what a launch still needs room
  for. Every predicate in the control plane already compares the copy's digest
  against the catalog, so a machine reporting a checked copy of the version
  before this one was priced the whole 640 second read by placement and then
  handed the workload those bytes for nothing, in the same execution, with every
  invariant green. `simulatedWorld.readableReplica` is the one predicate now,
  this world's half of `domain.ArtifactInventory.Holds`.
  - `a-restored-snapshot-is-not-a-copy` (conformance) is the world that produces
    it: the estimate charges 40GB and the execution reads 40GB out of the object
    store. It has to be a conformance Blueprint, because the Booking Decision
    was already right and only an execution can say which bytes the workload was
    handed. Deliberate break, since removed: trusting the copy's own state fails
    `safety.artifact_replica_verified` with `Run "run-consumer" read Artifact
    "artifact:imagenet:v2.41" from the replica on offer
    "rental-restored-snapshot" and was handed digest sha256:2b2b..., and the
    catalog says sha256:1a1a...`.
  - The disk half is stated in the Lab's own failing case for disk rather than
    in a case of its own. `a-machine-with-no-room-refuses-the-work` already
    describes the one machine that can reach the world's refusal at all,
    capacity whose inventory is silent, because silence is priced and never
    refused: it now holds a checked copy of another version under this version's
    name, so forty of its sixty gigabytes are spent on bytes this launch cannot
    use, and World Truth refuses the work Placement selected it for. Deliberate
    break, since removed: trusting the copy's state leaves eleven gigabytes of
    work on twenty gigabytes of free disk, the launch is accepted, and the read
    fails `safety.artifact_replica_verified` on `offer "cramped-host"`.
  - Two claims this entry made when it was written were wrong, and the
    corrections are in the entry below: preparation was said to answer ready
    with zero fetched bytes on that machine, which no fixture reached and which
    no node does, and the silence branch of `orchestrator.alreadyHeld` was said
    to be reachable by nothing, which was false in two ways.
- [x] 2026-07-25: Stop the Lab keeping copies nothing keeps, and stop preparing
  on machines Mercator cannot see. Two reviewers refuted parts of the entry
  above and of the entry before it; what follows is what was actually true and
  what changed.
  - A launch read left a verified copy behind. `readRunArtifacts` filed one for
    every input read out of the object store, so a machine was warm afterwards
    for content a workload had downloaded into its own container. Nothing in
    production performs that: `PrepareArtifact` is the only fetch in the tree,
    its only issuer is this controller over queued placements, and the launch
    command has carried no Artifact mount since it was deleted for promising an
    attachment nothing performs. `locality_provenance` now reads the source on
    `artifact.replicated`, so a copy explained only by an execution is no copy,
    and re-adding the launch side fails it with `offer "producer-rental" holds a
    copy of Artifact "artifact:checkpoint:v1" with no World Tape seed and no
    preparation recorded landing one there`. The fourth Run of
    `artifact-must-be-durable-before-a-consumer-runs` was the entry above's
    "reads what that fetch left behind"; it lands on that same machine and is
    priced the whole 2GB again, which is what a real node charges it, and the
    unchecked copy it read past is still unchecked. The saving a checked copy
    buys is asserted where such a copy can exist: on the prepared host of
    `prewarming-never-starves-real-work`, at both ends now, the decision pricing
    zero and both Runs reading the local disk.
  - The `artifact.read` effect recorded the digest and state of whatever copy
    the machine had under that name, including on a read out of the object
    store, so a green execution's own ledger said the workload was handed
    another version's bytes and the law skipped every read whose source was not
    a replica. A read now records what it served, and
    `artifact_replica_verified` holds over every accepted read.
  - `prefetchArtifact` answered ready with zero fetched bytes for a copy the
    machine already held. Deleted rather than covered:
    `nodeagent.PrepareArtifact` streams the source it was given and rewrites the
    record from the stream with no test for a copy already on the disk, so
    answering ready credited a node with a decision no node makes. What keeps
    the same bytes from crossing the link twice is the operation identity, on
    this seam and on a real one alike.
  - The silence branch of `alreadyHeld` was reachable, twice. `catalog[id]`
    returns a zero `OfferSnapshot` when a queued placement's machine is absent
    from the current listing, and a node that keeps no replica store reports
    `ArtifactInventory{}` on every heartbeat. The first was not silence about an
    inventory at all, it was a machine Mercator could say nothing about, and in
    the reusable lane it was a defect rather than a wasted command: a node
    leaves the catalog through the same `record.Alive` predicate that makes
    `Registry.Ref` refuse, so the desire became a command `Broker.Prepare`
    errors on and `Prewarm` ended the whole fleet's pass, leaving every other
    tenant's desire unstated, withdrawals included, on every trigger for as long
    as the Booking stayed queued there. A queued placement whose machine is not
    on offer now states nothing, and the silence that remains is the one the
    branch always claimed: a machine on offer whose runtime cannot enumerate
    what it holds. Stated at the orchestrator seam both ways, because no
    Blueprint can take capacity out of the catalog while a Booking is queued on
    it.
  - `PrepareReceipt.Unsupported` is still written by both `Broker.Prepare` and
    the Lab world and read by nobody, so a machine that refused the whole desire
    is still indistinguishable from one that took it. That is the half of the
    earlier note that stands, and it belongs to the slice that records what a
    machine said it could not prepare.
  - The live half ran on this workstation, amd64 Linux with a native daemon:
    MinIO in a container of its own, a presigned GET the node reads with no
    credential of any kind, a copy of another version's bytes reported
    unverified under the digest it actually produced, and a real container's own
    output reported as no copy at all. That last case is the production fact
    both halves of the withdrawal rest on: this node's enumeration answers about
    its replica store, so bytes a container wrote or downloaded for itself are
    bytes Mercator is not told about.
- [x] 2026-07-25: Close phase 3, and make its live half actually run. The three
  classes of locality are exact: OCI image content named in both digest spaces and
  subtracted from what a node reports, immutable Artifacts with the object store as
  their authority, and mutable caches keyed per workspace and generation. Disk is a
  resource all of it is accounted against, preparation is bounded so it cannot
  starve admitted work, and producer affinity was built and withdrawn because no
  shipped node can be in the state its discount fired in. Closing the phase turned
  up two things about the evidence rather than about the product, and both were
  assumptions from the machine the earlier slices were written on:
  - the entire L4 half of this phase was skipping on this workstation, and none of
    it skipped for a reason about this machine. `internal/nodeagent`'s gate pulled
    the image a case needs and treated any failed pull as an environment that
    proves nothing, so an address that has spent Docker Hub's anonymous quota
    skipped image assembly, layer reporting, disk measurement, cache isolation and
    Artifact replication while holding busybox, `registry:2` and `minio/minio`
    unpacked on the daemon the whole time. What those cases check is the agent
    against what this daemon itself reports, so a copy already here is the same
    evidence as one fetched a second ago. The pull is now a refresh, and only a
    reference this machine can neither pull nor already hold stops a case. Ten live
    cases run as a result, including every live case slices 4 through 9 rest on.
    The one case that genuinely needs Docker Hub, the public-image comparison,
    proves it can read that exact manifest anonymously before it starts and skips
    on 429 for the same reason it skips when offline. `requireDockerHubReachable`
    is deleted: it proved a registry answers, which an address being throttled does
    not contradict, so it was passing a gate meant to prove the read was possible;
  - `TestIntegrationDockerAdapterLaunchObserveRelease` named `linux/arm64`
    outright, which is the laptop the ephemeral lane was written on. On this amd64
    host it asked Docker for a foreign build, so it could not use the image the
    daemon holds and failed trying to fetch one. It is opt-in behind
    `MERCATOR_DOCKER_INTEGRATION`, so no suite has ever run it here and nothing
    caught it. It now asks the daemon what it is through `CLIClient.Info` and
    `OCIArch`, the same reader production places standing offers from.
  - Both are test-harness defects and neither changes a production answer. They are
    recorded here because they are the difference between a phase whose live half
    ran and a phase that reported skips, and because the first one had been silently
    true since slice 4.
  - What phase 3 does not leave finished is one thing above all others, and it is
    not a locality question. No production deployment can run a workload that
    declares an Artifact input at all: `cmd/mercator` builds the orchestrator with
    no `ArtifactCatalog`, so such a Run is refused at intake with
    `ARTIFACT_CATALOG_UNAVAILABLE`. Artifact durability, Artifact locality, the read
    a candidate still owes, and the whole Artifact half of preparation are therefore
    exercised in the Lab and against a MinIO container, and reach no operator until
    a production object-store client lands. That client, the attachment in
    [#171](https://github.com/benngarcia/mercator/issues/171), withdrawal on a node
    in [#170](https://github.com/benngarcia/mercator/issues/170), and a preparation
    that can be refused and retried are the four things phase 3 hands forward.
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
- [x] 2026-07-26: Append a decision rather than replacing one, and record the
  decision a Run that found nothing was never given. A Booking Decision now names
  what it supersedes and why, its identity is derived from its own recorded
  content, and Placement weighing the whole fleet and placing the Run nowhere is a
  fact in the record instead of a number thrown away.
  - The audit hole this closes is one the plan disclosed twice and left open both
    times, under the disk slice and again under its corpus judgment calls: a Run
    that found no feasible offer recorded no Booking Decision at all. Its whole
    account of itself was a reason code and two counts, so no candidate, no
    rejection and no schedule the wait was projected from survived anywhere, and
    every rule the rest of this phase needs reads decisions that were written.
    `safety.locality_is_never_infeasibility` had nothing to read on the one kind of
    Run whose refusal is the point.
  - The refusal is recorded with the deferral it caused, in one commit, and is
    suppressed with it. A Run waiting an hour against an unchanged fleet would
    otherwise write sixty decisions nobody asked a different question of, and what
    an operator needs is the evidence from the moment the answer last changed. A Run
    held behind work that outranks it records none, because nothing weighed a
    machine on its behalf and the queue is the whole of what happened to it.
  - A re-decision names its predecessor and gives a reason a reader can check
    against the Run's own stream: `PREVIOUS_LAUNCH_FAILED` where the machine the
    last decision chose refused to start the work, and
    `PREVIOUS_DECISION_SELECTED_NOTHING` where the last decision placed the Run
    nowhere and the fleet was asked again. Those are the two ways Mercator decides
    twice about one Run today, and both are facts already in the log, which is why the
    reason is read off the state rather than passed down by the caller. The vocabulary
    holds a third, `PREVIOUS_CAPACITY_RECLAIMED`, which nothing produces until an
    agent bootstraps on provisioned capacity in phase 5: it is capacity Mercator took,
    gave back, and asked the fleet again about, and the fact a reader checks it
    against is the confirmed cleanup on the machine the previous decision named.
  - Supersession is an input to the evaluation and part of the identity hash rather
    than a field stamped on afterwards. Two answers about one unchanged fleet at one
    instant are different decisions exactly because the second replaces the first,
    and an identity that ignored that would give them one ID and one event ID, so
    the second append would collide with the first inside a stream.
  - `domain.BookingDecision.Identity` derives the ID from the record: the Run, the
    revision, the moment, the model, the candidates, what was chosen, and what this
    answer replaces. The Booking is deliberately outside it, because a Booking's own
    identity is derived from the decision ID and a dispatched Booking carries a
    state its decision never claimed.
  - `safety.decisions_are_never_rewritten` and `safety.decision_is_reproducible` are
    the two new laws, and they are the pair. One ID means one decision, and every
    answer after the first names the record immediately before it and gives a
    reason; and re-deriving an ID from the content the record carries yields the ID
    the record carries. Without the second the first is defeatable by editing a
    decision and its ID together, which is a chain of consistent-looking records
    assembled after the fact.
  - `GetBookingDecision` becomes `GetBookingDecisions`, the API answers with the
    whole chain oldest first, and callers that want the current answer take its end
    where a reader can see them doing it. The console keeps the chain in its own
    projection and lists it under the decision that stands: holding one decision per
    Run meant a re-placement erased the answer it replaced, and the refusal a queued
    Run is waiting on vanished the moment anything else was decided. Conformance
    evidence carries the chain for the same reason.
  - The Run projection asks whether anything was chosen rather than whether anything
    was decided. A queued Run has a decision now, and reading the presence of one as
    placement reported every queued Run as requested again.
  - `a-changed-decision-names-the-one-it-replaces` (green) is the corpus half: a
    Rental whose schedule is full refuses the only Run in the world, the refusal is
    recorded, six minutes later the running Booking finishes and a position opens,
    and the answer that replaces the refusal names it. The re-decision is caused by
    the fleet changing rather than by a machine refusing a launch, because a refused
    launch is a fault and a placement fixture has none.
  - The launch-failure half is stated at L1 by
    `TestAReplacementNamesTheDecisionItReplaces` over
    `a-published-rate-is-not-what-a-machine-does`, where the fleet does not change at
    all and the machine refuses the start. Both answers survive with distinct
    identities, and the one that no longer stands is the only record that the Run was
    sent to the machine with the clean history first.
  - `TestARunPlacesOnANodeWithRoomForItAndNotOnOneWithout` reads the refusal off the
    decision route now, through the real daemon and a real enrolled node, rather than
    off the daemon's own answer with the struck-out machine asserted one layer down.
    That is the disclosure above closed at the layer it was disclosed at.
  - Every claim has a case that fails without its fix, verified one at a time.
    Dropping the supersession fails `safety.decisions_are_never_rewritten` by name on
    the real launch-failure re-placement and fails the corpus Blueprint on both the
    predecessor and the reason. Editing a recorded decision in place fails
    `safety.decision_is_reproducible` on the canonical execution. Naming the wrong
    reason for a refused launch fails the L1 case. Dropping the recorded refusal
    answers `404 DECISION_NOT_FOUND` from the decision route in the daemon case and
    leaves `an-impossible-ask-empties-no-fleet` with a Run nothing could place and no
    decision to be explained from.
  - Judgment calls. The corpus names a superseded decision by position and the runner
    resolves the ID off the record there, because Mercator hashes decision IDs and a
    fixture predicting one would be asserting the hash rather than the chain; the
    predecessor is then checked by identity, since naming a decision is the claim. The
    chain is required to be linear, each answer naming the record immediately before
    it, because a chain that skips a link is one a reader cannot walk and they are
    back to taking the last entry.

- [x] 2026-07-26: Answer the second review of the appended-decision commit. Two
  reviewers refuted five things in it, one blocking. All five were real, and the
  blocking one is a workspace-emptying stall the exemption two entries above this one
  was supposed to have removed.
  - A fleet that publishes nothing an ask matches was recorded as
    `NO_FEASIBLE_OFFER`, a wait for capacity to come free, which is the one wait the
    queue makes every other Run respect. So the strongest impossible ask there is was
    the only one nothing exempted. An offer query is a search on the shape asked for,
    which `internal/adapter/vast` builds out of `req.Resources`, so a Run asking for
    eight GPUs nobody sells gets zero offers, zero candidates, and a wait that keeps
    the queue, ages past standard work in about eleven minutes and past interactive in
    thirty, and holds every other Run in the workspace for as long as its own class
    allows. Twenty four hours for batch. The fleet meanwhile goes on selling exactly
    what the work behind it asked for. The state never self-corrects, because the
    reason and the empty `Behind` list are unchanged on every later tick and the
    deferral is therefore recorded once.
  - The root cause is the shape of the record rather than either reading of it. The
    deferral carried the fleet's answer as two loose integers, and their zero value
    meant two opposite things: a fleet that published nothing an ask matches weighed
    no machines, and so did a wait the queue caused on its own account. Neither side
    could tell them apart and each guessed differently. Production guessed from the
    reason code. The Lab guessed from the counts, keeping whatever was previously
    established whenever nothing was weighed, so it could not see the zero-offer ask
    at all and the corpus could never go red on the stall; and on a zero-weighed
    answer following a `NO_CAPACITY_FITS` one the two guesses contradicted each other
    in the other direction, with the Lab failing a history production wrote on purpose.
  - So the answer is typed where it is created. `domain.AdmissionDeferral.Fleet` is a
    `*FleetAnswer` that is absent when the fleet was never asked,
    `AdmissionDeferral.HoldsNoQueue` is the one rule both sides read off it, and the
    reason code is derived from that rule rather than decided beside it. A fleet that
    published nothing an ask matches is `NO_CAPACITY_FITS`, which is what it always
    meant. A Blueprint states the answer as `"fleet": {"weighed": n, "could_hold": m}`
    or as `"fleet": {"absent": true}`, and asking for two zeroes is no longer a way to
    say either.
  - `safety.nothing_waits_behind_an_impossible_ask` is a law about the ordering now,
    which is what its name says, and it reads the same rule production orders the
    queue on. Its previous claim to be independent of that rule was worth less than it
    looked: what it was independent of was the reason code, and the price was a second
    reading of the evidence that disagreed in both directions at once.
  - `an-ask-nothing-matches-holds-no-queue` is the new claim, red before and green
    after: the patient ask waits, twelve minutes promote it past the class that arrives
    next, and the Run that arrives is not ordered behind an ask nothing in the fleet
    even matches.
  - `a-queue-restates-what-it-waits-behind` used an empty fleet as a convenient way to
    get three Runs queued, so every ordering it asserted was one that should not have
    existed. It has a machine with every Booking position taken now, so the work behind
    really is waiting for the same machine, and the two orchestrator cases that made
    the same assumption get a machine whose own capacity evidence says it is busy. That
    is a better fixture for each of their subjects.
  - The deferral suppression claimed to fire on an unchanged fleet and compared two
    labels. A Run waiting on `NO_CAPACITY_FITS` has an empty `Behind` list by
    construction and a reason that does not move, so exactly one decision is ever
    recorded for such a Run however the fleet changes underneath it. That is an audit
    hole and a blind spot: every law about Placement is stated over recorded decisions,
    so a machine that arrives while a Run waits and is struck out for unknown locality
    is named in a decision nothing ever appended, and
    `safety.locality_is_never_infeasibility` reads recorded decisions. Suppression now
    compares `domain.BookingDecision.FleetVerdict`, the machines weighed and what each
    was refused for. The numbers beside them are left out deliberately: a projected
    start a minute nearer than it was is the same answer about the same fleet, and
    comparing a decision whole would record one on every tick of the sweep.
    `a-fleet-that-changed-is-recorded-again` states it through an idle lease that runs
    out under a waiting Run.
  - The chain claim was unconstrained at every production read path. Three one-line
    reversions each left the tree green: collapsing `GetBookingDecisions` to its last
    entry, truncating the decision route's response, and having the console reducer
    keep only the newest decision. The Lab corpus and the launch-failure case both read
    `booking_decided` events straight out of the log, and the only conformance
    assertion on the chain required exactly one entry.
    `TestTheChainAReaderGetsHoldsEveryAnswer` reads the chain of three a Run gets when
    two machines refuse it in turn, and `TestTheDecisionRouteAnswersWithTheWholeChain`
    reads the same thing over HTTP through the real daemon.
  - The console had a second defect under the same claim. Its live event schema dropped
    `supersedes` and `supersedes_reason` on decode, and `DecisionPanel` reads both, so
    a supersession only ever rendered for a page that had refetched the chain over
    REST. A console watching a Run be re-placed showed two answers with nothing saying
    that either replaced anything.
  - The conformance evidence keeps the whole chain and no longer claims a length it
    cannot produce. A trial asks for one launch on one machine and states
    `MaxPreStartAttempts` of 1, so its chain has one entry by construction. A claim
    stated where it cannot be reached is the defect, and it is held at the two layers
    above instead.
  - `safety.decisions_are_never_rewritten` had four clauses and one deliberate failing
    case, which drives the clause that returns first.
    `TestEveryClauseOfTheSupersessionRuleCanFail` shows each of the four failing on the
    one record it exists to catch, including the linearity the entry above states by
    name: nothing in the tree produced a chain longer than two, and two is the length
    at which a chain that skips a link and a chain that does not are the same chain.
    This is the treatment `TestEveryClauseOfTheCandidateIdentityRuleCanFail` already
    exists for, and the multi-clause law was added without it.
  - Every claim has a case that fails without its fix, each verified by mutating the
    production code and running the case: the classification as a red Blueprint on the
    zero-offer ask, the suppression as a red Blueprint on a fleet one machine smaller,
    the chain at both read paths as the reviewers' own reversions, and each of the three
    supersession clauses as the reviewer's own mutation.
  - Judgment calls. Exempting a zero-weighed ask costs the one thing the old reading
    bought: while a fleet publishes nothing to anybody, class ordering is not enforced,
    so the first machine to arrive can go to whichever Run the sweep reaches first
    rather than to the one worth most. That is worth paying. Telling a Run it waits
    behind another when no machine exists is a queue an operator cannot act on, the
    ordering is restored the moment anything is published that either Run could use,
    and the alternative is a workspace held for a day by one ask for a shape nobody
    sells. `FleetVerdict` leaving out the estimates is the same kind of call in the
    other direction: it still suppresses a candidate whose refusals are unchanged and
    whose established start moved, which is a far narrower hole than sixty decisions an
    hour, and every change the laws read is a change to a refusal.

- [x] 2026-07-26: Answer the third review of the appended-decision commit. Two
  reviewers refuted eight things in it, one blocking. Seven were real. All seven
  were the same mistake in different places: a record claiming more than the
  evidence under it could support.
  - The blocking one is what an empty offer answer means. The whole classification
    rests on the premise that a search returning nothing has said the fleet sells no
    machine of that shape, and neither marketplace adapter in the tree published an
    answer that could carry it. `internal/adapter/vast` filtered on machines nobody
    is on, so every sold-out moment answered exactly as a shape Vast does not sell,
    and on a marketplace a popular card is rented most of the time: a Run against a
    sold-out market lost its place in the queue, and the first machine to come free
    went to whichever Run the sweep reached first. Shadeform filtered on
    availability twice over, in the query and per region, on the phase 5 conformance
    provider. Both search for the machines now and publish the ones somebody else is
    on as capacity that is not available, which is how every other occupied machine
    in this tree is published and which the scheduler already refuses as a wait
    rather than as an impossibility. Vast orders the free ones first so the limit
    still spends itself on capacity a Run can have today.
  - Vast also asked for exactly the accelerator count, and it sells asks against
    power-of-two partitions of a machine, so a Run wanting three cards matched
    nothing in a market abundantly selling two, four and eight. It asks for at least
    what the Run needs. The ask publishes its true card count and its true price, so
    a larger partition is ranked on what it costs rather than excluded from the
    fleet's answer, which is the same fix as the one above: the search may not decide
    on the fleet's behalf that a machine is not for sale.
  - A node that could not measure its disk published the zero its failed
    measurement left behind. `capability.DiskFacts` carries `Known` precisely so a
    machine that could not look is distinguishable from a machine with no room, and
    the offer threw that half away. Every Run carries a disk floor, so every Run in
    that workspace was refused, every refusal read as a machine that can never hold
    the work, and the whole workspace was recorded as work no capacity can ever take
    and lost its queue ordering until a heartbeat happened to succeed. The offer
    states both halves now, and a machine that answered nothing is a third answer
    in the fleet's own account of a wait: `FleetAnswer.Unstated`, reason
    `CAPACITY_UNSTATED`, which holds the queue because the machine may be able to
    take the Run the moment it speaks. Placement still refuses it, because landing
    content on a disk nobody measured is a launch nobody can promise.
  - The queue exemption was a standing claim. `HoldsNoQueue` kept whatever was last
    established whenever a deferral carried no fleet answer, and a deferral the queue
    caused carries none by construction, so a Run outranked by a steady stream of
    arrivals never asked the fleet again and its exemption outlived every machine
    that arrived afterwards. Work of its own class that arrived later overtook it.
    Only the latest answer may make the claim now. Losing it on a queue-caused wait
    costs nothing that was not already lost: a Run only fails to renew it while
    something outranks it, and whatever it would have held up is held up by that work
    anyway.
  - The suppression rewrite did not close the blind spot it was written for. Its
    justification was that every change the laws read is a change to a refusal, and
    the law it named reads no refusal at all: what a machine holds is priced rather
    than refused, on purpose, so a candidate whose image locality went from known to
    a silence produced a byte-identical list of refusals against the same bound on
    the same path, the decision was suppressed, and
    `safety.locality_is_never_infeasibility` reads recorded decisions. `FleetVerdict`
    is now what each machine was struck out for, what it was found holding, and what
    every answer it published was scored at. The numbers are still left out, because
    they move on their own.
  - A present `FleetAnswer` asserted the fleet was asked and the record could not
    support it. `CollectionReport.ConnectionsQueried` was derived from the offers, so
    a connection that answered with nothing and a connection nobody contacted
    produced the same empty list, and `ExcludedConnections` existed for exactly that
    distinction and was written nowhere. The console renders all three lists, so the
    fabrication was operator-facing. The census travels with the offers now, because
    it cannot be derived from them: `broker.fanOut` names every connection it
    skipped, and the orchestrator reads placement capacity through `CollectOffers`.
  - `an-ask-nothing-matches-holds-no-queue` could not model the failure it claimed,
    because neither simulated world read the requested shape. Both answer it now for
    marketplace listings, and only for those: a listing is a search result and a real
    search filters it, while capacity Mercator holds is listed whole and refused in
    the record, which is the difference between a catalog and a fleet. The Blueprint
    publishes a 200GB machine, answers a 900GB ask with nothing, and its second Run
    is placed, which is the stall the case exists for.
    `a-machine-that-could-not-look-is-not-a-machine-with-no-room` is the disk silence
    at the same level, which no fixture could state before.
  - `safety.a_silence_is_not_an_answer_about_capacity` recounts every recorded wait
    off the decision it was read off, the way `silenceWasTakenBackOut` recomputes what
    a candidate was charged. A scheduler that miscounts its own evidence agrees with
    itself perfectly, and only a reading taken from the record's other half catches
    it.
  - Partly upheld: the console. The defect the last entry recorded was not
    producible. `DecisionPanel` is only ever fed by `useRunDecisions`, which resolves
    the chain over REST through a contract that already carried the supersession, so
    no page could show two live answers. The projection the fix was aimed at,
    `Workspace.runs[].decisions`, was written by the reducer and read by no
    component, so it is deleted: it was a second store of facts the Run's own page
    already reads. The reviewer's replacement claim does not hold.
    `resourceKey.runDecision` is invalidated on `booking_decided` in
    `Workspace.invalidateMessage` and has been since the canvas became the console
    entry point.
  - Rejected: that `a-fleet-that-changed-is-recorded-again` is unreliable on this
    host. It was reported failing once at `20d3bb0` on amd64 Linux and could not be
    reproduced in more than five hundred further executions here, single and heavily
    parallel, on the case alone, across the whole corpus, and across the four packages
    together. The harness is
    single threaded on a scripted clock, the event log is read in global-position
    order rather than by timestamp, and the only way the reported failure can occur is
    for the second evaluation to have seen two machines, which requires the world
    clock not to have advanced past the idle lease. Nothing on that path reads a real
    clock. The candidates the reviewer named are both ruled out: the in-memory SQLite
    log is opened under a fresh name per session, and the `occurred_at` fallback
    cannot reorder a scan that orders by position. Left open with the evidence rather
    than papered over with a retry.
  - Judgment calls. `domain.ResourceInventory` states whether a disk was measured
    rather than leaving it to be inferred from the bytes, so a publisher that sold a
    disk says so and a publisher that forgets is refused loudly. A double that states
    its own offers states its own census, because Go resolves an embedded method
    against the embedded value and a census inherited from the fake adapter answers
    about offers the double does not publish; that bit immediately in ten
    orchestrator cases and a green Blueprint, which is the right way for it to bite.
    A Vast launch is refused by name when its ask has been taken since the decision,
    rather than being attempted and failing at the provider, because the search no
    longer excludes those asks and a create Vast refuses would reach the record as a
    provider failure instead of as somebody getting there first.
  - Every claim has a case that fails without its fix, each verified by mutating the
    production code and running the case: the locality flip as a domain case on
    `FleetVerdict`, the disk silence as a red Blueprint four ways over, the shape
    filter and the zero-offer exemption as the two halves of one red Blueprint, the
    exemption's freshness as the ordering in
    `a-wait-the-queue-caused-says-nothing-about-capacity`, and the recount as the
    deliberate failing case beside every other invariant.
- [x] 2026-07-26: Hold a Run to the bounds it declared, and rebuild the read model
  the class rename changed underneath. The class a Run states is the exchange rate
  every candidate is scored at, so a class can always be talked into a costlier or a
  later machine; the bounds are what say how far, and neither of them was fully
  held.
  - The class deadline bounded waiting and not starting. It was asked exclusively of
    a Run being told to wait, so a Run whose capacity came free after the moment its
    class says the answer stops being worth having was placed by the very pass that
    should have refused it: it spent the money to produce an answer nobody was
    waiting for, and the overshoot was however long the sweep interval is.
    `stepAdmit` asks it on both ways out now. A Run being deferred is asked by
    `deferOrRefuse`, which keeps recording what was holding it, and a Run nothing is
    holding is asked before Placement and refused `DEADLINE_UNREACHABLE` with no
    fleet answer beside it, because nothing weighed a machine for it and that
    refusal is about the clock. `domain.Admission.DeadlinePassed` is the elapsed half
    of the rule said once, and `DeadlineUnreachable` is stated over it.
  - No Blueprint could state a bound on cost, which is why nothing here could catch
    the other half. `COST_LIMIT_EXCEEDED` has been enforced in production since the
    unpriced-machine pass and was reachable by no fixture, so everything this corpus
    could say about money was which machine won on price. `request.max_cost_usd` is
    the vocabulary, translated once in `WorkloadForRun` so both simulators read one
    statement, and a budget of zero dollars is refused at load: a bound that refuses
    every quoted machine in the world is a fixture to write on purpose.
  - `safety.class_bounds_honoured` is the law. No Run was placed on a machine
    costing more than its caller allowed, on a machine nobody quoted under a bound
    on dollars, or past the moment its class states, measured from the deferral that
    started its wait. The two are one law because they are one failure, and both
    halves are read off the decision and the public log rather than off the
    scheduler's own arithmetic. The maximum queue delay is deliberately not restated
    in it: that promise is what `liveness.aging_prevents_starvation` is stated over,
    and two laws over one bound let a repair satisfy one of them and be believed.
  - The migration left the read model behind. The service class rename rewrites the
    vocabulary inside the event log, and the Run projection is stored rather than
    recomputed, so every Run recorded before a Run stated its class read back with
    no class at all through `GET /v1/runs`, for the life of the installation: a
    rebuild happens when the projection's schema version is not the current one, and
    a database that predates the rename already carries the current version. The
    migration reports whether it rewrote anything, and a rewritten log marks the
    projection stale, which is the question the daemon already asks before it
    replays each Workspace. It is the one migration in this tree with that problem:
    the legacy run event migration predates the projection table, and the stored
    revision migration rewrites workload streams, which no Run record is reduced
    from.
  - The live placement harness had been stating a superseded vocabulary. Every case
    in `internal/daemon/node_placement_test.go` submitted
    `placement.objective: balanced`, a word nothing has decoded since the class
    replaced it, so each of them was normalised to standard and none of them
    asserted anything about the class it thought it set. They state a service class
    now.
  - Judgment calls. The deadline fixture is L0 only, and that is a consequence
    rather than a preference: every class's maximum queue delay is shorter than its
    deadline, so a Run that reaches its deadline has already starved and
    `liveness.aging_prevents_starvation` calls a Lab execution of that world a
    violation. That is the same open question this plan has disclosed twice, and it
    is a refusal-policy slice rather than something to smuggle in beside a law. The
    deadline is measured from the first deferral in both the production queue and
    the law, because a Run told to wait for a second reason has not started waiting
    again. An unpriced candidate fails a stated bound rather than passing it, which
    is the rule `Preferred` already ranks on, said as a limit. The corpus runner now
    names the machine a Run a fixture expected to be waiting was actually placed on:
    being placed appends no admission fact, so the diagnostic read the wait the Run
    was in beforehand and reported the deadline regression as a Run still queued for
    want of capacity when the Run had run.
  - What is left. The public API still drops `objective` silently, the way any HTTP
    surface drops a field its schema does not carry, so a caller who kept sending
    `fastest_start` after the upgrade is scored as standard with nothing in the
    record saying so. The corpus loader refuses the superseded word by name and the
    door does not. Refusing it there means holding a list of retired vocabulary at
    the door, or making the whole operator API strict about unknown fields, and
    which of those is right is a decision about the wire rather than about the class.

- [x] 2026-07-27: Make waiting a phase that ends, and prove aging is what ends it in
  the Run's favour. This closes the one thing this plan has disclosed under three
  separate entries and deferred each time: a Run kept waiting longer than its class
  allows went on waiting.
  - The maximum queue delay was the one number on a class that nothing acted on. The
    ordering derives its aging rate from it, `liveness.aging_prevents_starvation` holds
    every execution to it, and admission itself only ever ended a wait at the class
    deadline. So standard work waited four hours past a thirty minute promise, and
    opportunistic work, which declares no deadline at all, waited for ever. A caller
    learned nothing while Mercator spent the whole interval deciding it could not help.
    `domain.RefusedQueueDelayExceeded` is the answer, and `deferOrRefuse` is the one
    door it is asked at.
  - It is asked only where waiting continues, which is a judgment call. A Run whose
    capacity came free a moment after the bound has stopped waiting, and refusing it
    there would spend the whole wait and then throw away the answer it was for. The
    deadline is asked at both doors because it is a different question: whether the
    answer is still worth producing at all.
  - It is measured off elapsed time rather than off a projection, exactly as
    `DeadlinePassed` is. A bound that has gone by is a fact about the clock, and the
    tree already has the case for refusing on a projected wait nobody measured:
    `a-wait-nobody-can-end-is-not-a-missed-deadline` is a Run closed at its first pass
    on somebody else's runtime.
  - `liveness.aging_prevents_starvation` had to be strengthened rather than left
    alone, and this is the finding that mattered most in the slice. The law said no Run
    sits queued past its class bound, which a refusal at that bound satisfies by
    construction: once admission ends every wait it cannot honour, refusing everything
    is a passing execution and the law has no teeth left. Its second half now reads the
    refusals. A wait ended at the bound is starvation unless the record says the wait
    could not have ended, and the whole of that exemption is the fleet answer saying no
    machine it published could ever hold the Run.
  - The second half is deliberately stated over waits and never over effective
    priority. Production orders the queue on `Admission.EffectivePriority`, and a law
    that read the same function would be checking the aging term against itself:
    deleting the term makes the ordering wrong and every reading of it agree, which is
    the mutation this slice is required to be red for. What the law reads instead is the
    derivation the rate is built from, that a Run outranks anything arriving once it has
    waited half its own bound, and the only thing it takes from the class table is that
    bound. Work admitted past such a Run must itself have waited half of its own.
  - That reading is also what keeps a fleet too small from being called starvation. A
    machine serving less interactive work than arrives refuses the excess, and every Run
    admitted ahead of one of those had waited longer than it had, so the law is silent.
    A Run stepped over by arrivals that had waited nothing is the opposite record, and
    that is the one it fails on.
  - `a-batch-run-eventually-runs` is the proof the phase goal asks for, and it is a
    green conformance Blueprint rather than a repair: aging has existed since the class
    replaced the objective, and nothing in the corpus asserted it. Every queue fixture
    before it states one moment of an ordering, and starvation is a claim about what an
    hour of arrivals does to a Run. One machine, forty one interactive Runs arriving
    over an hour, and one batch Run at a base priority of twenty. At thirty minutes and
    thirty seconds the batch Run is worth a hundred and one, the next arrival is the
    first told it waits behind it, and it takes the position that comes free.
  - It is driven in thirty second advances rather than to completion, which is a
    judgment call about the driver. `DriveToCompletion` jumps to whatever the world
    still owes, so the sweeps between the last arrival and the next completion happen
    inside one advance with nothing reasoning in the middle of them, and those sweeps
    are the entire fixture: a freed Booking position is given to whatever outranks the
    rest of the queue on the sweep that notices it.
    `an-impossible-ask-empties-no-fleet` is driven to the world's horizon instead, so it
    reaches five hours in one advance with both bounds behind it, and the bound its
    refusal names is the earlier one. This entry first recorded that Run as refused
    `DEADLINE_UNREACHABLE` and presented it as a consequence of the driver, which review
    refuted. The review bullet below is where that is corrected.
  - `a-class-with-no-deadline-still-stops-waiting` is the refusal at L0 and
    `conformance/a-queue-delay-bound-is-refused-loudly` is the same claim at L1. Both
    are the opportunistic case on purpose, because it is the one where nothing else can
    end the wait, and the second is what the starvation law's exemption is falsifiable
    through. It landed as a target Blueprint declaring `refused_queue_delay` and was
    promoted with the production change, so the corpus was red for the behaviour before
    the behaviour existed.
  - The public contract had never described the queue at all. `phase` was an
    undocumented string, and `service_class`, `queued_since`, and `admission` were
    emitted over `GET /v1/runs` and absent from `openapi.json`, so the state a queued
    Run is in was readable in practice and unstated in the contract. That is a gap the
    queued-phase and class-rename slices left, and this is where the new refusal reason
    an operator reads had to go, so all of it is described now: the phase enum, the six
    admission reasons, the fleet answer a wait rests on, and the work named ahead.
  - The invariant names the slice asked for, `liveness.no_run_starves` and
    `safety.class_ordering_respected`, are the two laws this tree already holds under
    `liveness.aging_prevents_starvation` and `safety.service_class_admission_order`.
    They are the same rules over the same records, the second is already stated as an
    overlap in time, and renaming them would have been thirty documentation lines and
    two test files of churn for no change in what is checked.
  - The bound is unchanged at `longestClassQueueDelay()`, which is two hours, and that
    is chosen with `longestBound()` in mind: `liveness.admitted_run_progress` already
    holds every execution to twenty four, so this rule lengthens nothing. A bound past
    that one would have made every fixture in the tree longer to state a rule about the
    queue.
  - Every claim has a case that fails without its fix, verified one at a time. Deleting
    the aging term from `Admission.EffectivePriority` fails
    `a-batch-run-eventually-runs` on `liveness.aging_prevents_starvation` by name,
    naming the arrival that overtook the batch Run and the wait it had accumulated.
    Deleting the queue-delay branch from `deferOrRefuse` fails
    `conformance/a-queue-delay-bound-is-refused-loudly` on the same law's first half.
    The strengthened clause has its own cases in `internal/lab`. Younger work admitted
    past a Run later refused is a violation, a wait past its bound refused under
    another name is a violation, a Run the fleet could hold when its wait began and not
    when it ended is a violation, and a Run Mercator itself placed a machine for is a
    violation; while a fleet that held nothing from the first deferral to the refusal,
    older work admitted ahead, another tenant's admission, and a Run placed again after
    a failed launch are all silent.
  - Two reviewers refuted parts of this slice on 2026-07-27, five of the six findings
    were real, and all five are repaired. Every repair has a case that is red against
    the reading it replaces, driven one at a time.
    - Admission named the later of two broken bounds. `stepAdmit` asked only the class
      deadline on the way to Placement, so a Run past both bounds was closed
      `DEADLINE_UNREACHABLE` with its queue delay unmentioned:
      `an-impossible-ask-empties-no-fleet` refused `run-impossible` after 17940 seconds
      against the 1800 its class allows and named the four hour deadline.
      `Admission.BoundAlreadyBroken` is the one place both doors take the word from now,
      and it names the promise Mercator broke first. Naming the elapsed deadline is
      unreachable for every class in the table as a result, which is a fact about a
      table where every queue delay is shorter than its own deadline rather than a hole:
      `DEADLINE_UNREACHABLE` is what a projected miss is called, which is the case only
      `deferOrRefuse` can see. `a-machine-that-came-free-too-late-is-not-a-start` changes
      reason with it and proves exactly what it did before, that the Run is refused
      rather than started on a machine that came free too late.
    - The starvation law read the reason code, so the record above went unjudged by both
      of its halves at once: the first skips a Run that is closed, and the second
      filtered refusals on `QUEUE_DELAY_EXCEEDED` and never saw it. It reads the wait
      against the class bound now, which is what this registry says about reason codes
      everywhere else.
    - Both admission laws replayed every tenant in one flat pass while Mercator orders
      each workspace's queue on its own, so an admission in `ws_beta` convicted a
      refusal in `ws_alpha` of starvation for an ordering neither queue can express.
      They are scoped to the workspace now, and `WorkspaceID` was on every event the
      whole time.
    - Both laws also restarted a Run's wait at each placement, while
      `runState.queuedSince` is set at the first deferral and never cleared. A
      replacement after a launch that failed read back as an arrival that had waited
      nothing, and convicted the queue it was in fact the oldest member of. Membership
      of the queue still ends at a placement, and the moment a wait began no longer does.
    - The exemption was read off the refusal's own fleet answer, and the priority door
      records none, so a Run nothing in the fleet could ever hold was judged as if the
      fleet had room for it. It is the fleet's last measurement during the wait now,
      which is what this law's stated assumption always claimed and what production's own
      reading deliberately does not do: production asks whether other work must be held
      behind this wait now, where an unrenewed exemption is a claim about a fleet nobody
      has asked since.
    - Two reviewers refuted parts of that pass in turn, and three of these five repairs
      were incomplete. What each of them left is in the entry below.
    - Rejected, with the evidence. The sixth finding is that feeding `run.queued` into
      `Starved` refuses a Run for time it spent holding a Booking rather than queueing.
      `queued` is elapsed time since the first deferral, which is what the class deadline
      has always been measured over and what `AdmissionDeferral.QueuedSeconds` documents
      itself as, so subtracting the launch attempts would make the two bounds measure
      different things while the record states one number for both. A Run that has not
      started an hour after a thirty minute promise has broken that promise whether it
      spent the hour queued or spent it failing to launch. Max pre-start attempts bound
      how many machines a Run may be tried on, the class bounds how long its caller
      waits, and whichever bites first is the answer.
  - The review of that review, on 2026-07-27. Four findings were real and one is
    rejected with its evidence. Every repair is red against the reading it replaces.
    - "Both laws" is three laws. `internal/lab/invariant_admission.go` holds three that
      measure a wait, and `noPlacementPastItsDeadline` was left restarting the clock at
      each placement, contradicting its own doc comment. So
      `safety.class_bounds_honoured` measured a shorter wait than `stepAdmit` refuses on,
      and could not fail for any Run placed past its deadline that had been placed once
      before, which is exactly the shape a failed launch produces. There is one reading
      now, `waitsBegan`, and all three laws take the moment from it.
    - The widened exemption survived a placement Mercator itself made. A Run measured
      unholdable once, given a machine, and sent back through admission carried that
      first answer for the rest of its life, because every later deferral through the
      priority door records no fleet at all, so a Run the fleet demonstrably held was
      exempt from the starvation law outright. The exemption is every answer during the
      wait now, and a placement is the strongest answer there is that the fleet could
      hold the Run. The same reading fixes the other direction: a Run the fleet could
      hold when its wait began, overtaken for an hour, and refused after the machine
      left the fleet was exempt on the strength of the last answer alone.
    - Production held both of the readings the Lab was corrected for.
      `runState.queuedSince` never moves, and `applyToQueue` dropped the moment a wait
      began at every placement, so a Run deferred, placed, and told to wait again was
      ranked at its whole wait by its own door and as an arrival by every other Run in
      its tenant: it aged toward a queue delay measured from a moment nobody else could
      see while fresh work of a higher class was admitted past it. The queue keeps both
      facts now, membership and the moment. The finding named a launch failure as the
      path that reaches it, and that half is refuted: a replacement that finds no
      machine closes the Run `RETRY_EXHAUSTED` rather than returning it to the queue,
      and the only other path, expiring a Booking past its latest start and re-placing
      its Run, is the schedule advancement this corpus still carries as a target with
      `schedule_advancement` declared missing. The disagreement was latent rather than
      live, which is why no Blueprint states it: the record is stated in an orchestrator
      test over the real event log, and the Blueprint belongs to the slice that builds
      schedule advancement.
    - Moving `a-machine-that-came-free-too-late-is-not-a-start` to
      `QUEUE_DELAY_EXCEEDED` was right, and it left `DEADLINE_UNREACHABLE` asserted by
      nothing in the whole executable specification. The elapsed branch of
      `Admission.BoundAlreadyBroken` cannot fire for any class in the table, as the
      entry above concedes, so the projected miss in `deferOrRefuse` is the only thing
      left that can produce the word at all, and deleting those four lines left every
      one of the 36 packages green. `a-start-nobody-can-reach-is-refused-at-the-door`
      states the world that branch exists for: a machine whose Booking queue is full
      for twenty five minutes, an interactive Run with no wait behind it and both its
      bounds still ahead of it, and an answer that has already stopped being worth
      producing.
    - Rejected, with the evidence. One finding is that the plan's own
      `safety.class_bounds_honoured` entries claim the opposite of what the law did, and
      asks for the entries to be corrected. They say the deadline is measured from the
      deferral that started the wait, which is what the law was written to do and now
      does: the code was wrong and the prose was right, so the repair is in the code and
      there is nothing to fix in the entries. The finding is filed twice, once against
      `invariant_admission.go` and once against this document, and the code half is the
      first repair above.
- [x] 2026-07-27: Make a run group a bound admission holds, and let a world take a
  machine back. Two vocabularies the corpus could write down and nothing could act on.
  - A group was a string the arrival plan wrote onto a Run and nothing read. It reached
    the World Tape and stopped there, because `WorkloadForRun` had nowhere to put it and
    `domain.WorkloadSpec` had no field for it. It moves onto the request the class
    already travels on rather than staying beside the arrival, so it enters production
    the way every other statement a caller makes does, and the per-arrival and
    per-family strings are deleted rather than joined by a second way to say it. A
    family is a name and a width together, every member declares the width, and half a
    declaration is refused where the Run enters: a name with no bound would be held to a
    width of nothing and never placed.
  - There is no Group aggregate, and that is the judgment call. A group is a label the
    work carries, so there is nothing to register before submitting and nothing to
    reconcile afterwards. The price is that members have to agree about their own width,
    which `safety.group_parallelism_respected` reports rather than resolves: holding a
    family to a bound half of it never asked for would be worse than saying the record
    is contradictory.
  - Admission asks the family first, ahead of the ordering and ahead of any machine
    being weighed, because it is the only one of the three questions no ordering and no
    capacity can answer differently. Asking it later recorded the Run as waiting behind
    work that outranked it while the thing holding it was its own declaration, and let
    it hold the queue against unrelated work. The wait it produces therefore holds no
    queue, which is the same exemption an impossible ask carries and for the same
    reason.
  - The count is over placements rather than over executions. A member given a queued
    Booking behind somebody else's work is not running yet and admission will never ask
    about it again, so counting what runs would let a family of three commit six
    machines and then run six of them.
  - `reclaimable` is the term capacity was sold on, stated by the backend that sold it.
    It is deliberately not derived from the interruption rate offers already carry: a
    rate is how often a machine has been seen to fail, and refusing work that may not be
    interrupted has to rest on what the capacity is. Silence means no provider said it
    sells this capacity that way, which is safe on this fact alone, because what a
    provider does not sell as reclaimable it does not reclaim, and the Lab enforces
    exactly that by refusing to preempt a Rental no fixture declared reclaimable.
  - Interruption permission is decided before the work starts and never after. Nothing
    Mercator holds survives a machine being reclaimed, so there is no policy about which
    execution to give up: by the time the provider says so the choice has been made for
    it. It joins the class table beside the priority, because the two classes that price
    waiting at a fifth of the rent or at nothing are exactly the two that would rather
    be cheap than certain.
  - `world.capacity.preempted.v1` is the first World Tape event that is neither a
    caller's doing nor Mercator's. The world removes the capacity and the executions on
    it, and Mercator learns the way it would in production, by looking and finding the
    launch missing: a provider that has taken its machine back answers no differently
    from one whose machine finished the work.
  - An interrupted Run closes failed. Re-placing work whose process already ran is
    replanning, which is the remaining phase 4 slice, so this one states the permission
    and the loss rather than pretending to survive it.
- [ ] Not yet done, and disclosed rather than implied. No production adapter publishes
  `reclaimable`: the field is on the wire, the class states the permission, the
  scheduler refuses on it, and the two simulated worlds write it, but the backends that
  sell interruptible capacity are the phase 5 provider's business. Soft and hard
  affinity and a blocked-until-ready edge wider than the single Artifact dependency are
  also still open, and both want a scenario and an invariant of their own rather than a
  field added quietly beside this one.
- [x] 2026-07-27: The group bound under review. Two reviewers refuted parts of the
  slice above. Three findings were real, the fourth was real about its evidence and
  wrong about the repair it asked for, and every repair is red against the reading it
  replaces.
  - Admission is one decision per workspace at a time. The width a family declared was
    read over the whole workspace event log and written to one Run's stream, and nothing
    anywhere refused the second writer: a Run's own stream version guards every other
    transition it makes, and this bound has no such guard. Intake advances a Run inside
    its own HTTP request, so a caller launching a sweep is exactly the burst that
    defeats it, and two members of a family declared one wide each took a machine, five
    times out of five. It is a lock in this process because the log is this process's own
    SQLite file, so a workspace's admissions are all decided here or not at all; a
    second control plane over one log would need the log itself to arbitrate, which is a
    different design rather than a wider mutex. The price is that admissions in one
    workspace no longer overlap, including the provider call a provisioning decision
    makes, and admission already replayed the whole workspace log on every pass, so this
    stage was never the parallel one.
  - No Blueprint states that and none can. The Lab drives admission one Run at a time by
    construction, which is what makes it a deterministic specification, and the daemon
    fleet harness cannot force the interleaving either: six members of a width-one family
    submitted together over the public API left the defect green twenty times out of
    twenty, because the work each request does before admission is long enough that no
    two admission windows line up. The claim is stated where the interleaving can be
    forced, in `internal/orchestrator`, and the gap is disclosed here rather than papered
    over with a case that passes against the defect.
  - A wait a caller's own declaration is holding is charged the caller's own bound and
    never Mercator's. A member held by its family's declared width was refused
    `QUEUE_DELAY_EXCEEDED`, whose own record reads "a Run Mercator has already kept
    waiting longer than its class allows", while a machine that could have taken it stood
    idle: any family that takes longer to drain than its class's patience lost its later
    members as failed Runs. The maximum queue delay is Mercator's promise about waiting
    for capacity and a caller cannot break it; the deadline asks whether the answer is
    still worth producing and still ends the wait.
    `domain.AdmissionDeferral.SelfImposed` is the one place that difference is stated.
  - The Lab held both readings at once. `liveness.aging_prevents_starvation` exempted
    such a wait from its half about refusals and demanded through its half about live
    waits that no accepted Run be left waiting past its class bound, which only refusing
    the member could satisfy. Both halves carry the exemption now, and the deliberate
    failure beside it is unchanged: the identical record waiting on `NO_FEASIBLE_OFFER`
    is still reported.
  - The corpus can state a queued Booking inside a family, and now does. Both group
    Blueprints run on idle machines, where a placement and an execution are the same
    instant, so no member of any family in the tree ever held a queued Booking and
    counting executions instead of placements left every Blueprint and every law green.
    That was a Blueprint nobody had written rather than a limitation of the Lab, and it
    is written now.
  - What takes a member out of that count is the capacity going back rather than
    admission being asked about it again. The count left on a deferral, and its
    correctness rested on the prose claim that admission is only ever asked of a Run that
    still needs a machine, which holds today only because the one path that re-admits a
    Run is restricted to capacity failures with no side effect whose Booking was
    completed first. A Booking is given back in the same commit as the launch failure
    that ended it, so the log says when the capacity went. An indeterminate launch
    records a different fact and keeps its Booking, so such a Run keeps its family's
    place, which is right for the reason the distinction exists: nobody knows whether the
    container is running.
  - Disclosed rather than implied. The Lab has no sweep of its own. Its driver advances
    to the next thing the world does, which for a queued member is always the moment its
    family makes room, so no Blueprint can ask a held Run a question in the middle of a
    wait. That is why the queue-delay defect above was invisible at L1 and why the two
    fixtures that state it drive the clock themselves, one through a `reconcile` step and
    one through an explicit advance in the test. A periodic reconcile in the Lab would
    make a class of bounds falsifiable that currently is not, and it is a change to the
    execution model rather than a fixture, so it wants a slice of its own.
- [x] 2026-07-27: The divided wait. Two reviewers refuted the repair above. The
  exemption it added was keyed on the reason of the moment while the whole wait went on
  accumulating against the bound, so the difference it exists to state was laundered in
  both directions. Four findings were real, two were real about their evidence and wrong
  about the repair they asked for, and every repair is red against the reading it
  replaces.
  - A wait is two numbers now, `domain.Wait`, and each bound is asked of the part of it
    that bound is about: the maximum queue delay of the part Mercator caused, the class
    deadline of the whole of it. The part the caller's own declaration held is summed
    over intervals, because a deferral is the answer for the interval it opens and
    nothing else. The exemption's own claim is what the reviewers falsified. A member
    held seventy minutes by its own siblings, asked on the first pass after its family
    made room and finding no machine free that instant, was refused
    `QUEUE_DELAY_EXCEEDED` and closed failed, for a wait Mercator had kept it in for
    zero seconds. The reverse ran the other way: a Run the fleet had starved for fifty
    minutes became exempt from the bound and from the starvation law both, for the rest
    of its life, because a sibling took its family's place.
  - The claim above that "`domain.AdmissionDeferral.SelfImposed` is the one place that
    difference is stated" was false, and that is the second finding. One of the two doors
    that can refuse a Run never read it: a held member past its class deadline was named
    `QUEUE_DELAY_EXCEEDED` on the way to a machine and `DEADLINE_UNREACHABLE` on the way
    into the queue, so which broken promise the caller was told about depended on whether
    a machine happened to be free at that second, which is the sweep-cadence dependence
    the bound naming exists to forbid. `Admission.BoundAlreadyBroken` now answers for
    both doors off the wait, and `DeadlineOnlyAlreadyBroken` is deleted rather than
    kept beside it. `SelfImposed` remains what it always described, one interval.
  - The record carries the division, `self_imposed_seconds` on the admission fact and in
    the API contract. Two numbers beside each other cannot be read without it: a deferral
    an hour past a bound of an hour is a contradiction on the face of the record until it
    says which part of that hour Mercator caused, and a refusal naming the bound is only
    checkable against the part it was measured on.
  - The corpus could not see any of it, and now states both directions.
    `a-wait-the-fleet-caused-is-not-excused-by-a-sibling` is the laundering the plan Lab
    can drive, and `only-the-part-of-a-wait-mercator-caused-is-charged` is the handoff
    instant itself, which needs a world event because nothing in the plan Lab can give
    capacity back: a provider takes the family's one machine away an hour and a minute
    in. The two fixtures the previous pass added miss the defect for a reason worth
    writing down. One never lets the family make room, and the other gives the family two
    idle machines, so placement always succeeds at the handoff.
  - The claim above that "the deadline asks whether the answer is still worth producing
    and still ends the wait" is not true of every class, and the entry did not disclose
    it. Opportunistic states no deadline, so a member of an opportunistic family whose
    sibling never gives the place back is bounded by neither of the two bounds its class
    states. That is the class doing what it says, and refusing it is the repair this pass
    declined: it would name a moment the caller expressly declined to state, in the words
    of a promise about capacity that nothing there broke, which is the lie the pass above
    removed. What bounds it is that Mercator is still holding the Run.
    `liveness.admitted_run_progress` reads the phase not at all, so a Run of a declared
    arrival still open a day into an execution is reported whatever it is waiting for,
    and `TestAFamilyHeldMemberIsStillHeldToProgress` states that over the held
    opportunistic record. The reviewers' stronger claim, that nothing anywhere could
    report such a member, is refuted by that law.
  - `a-member-that-gave-its-capacity-back-leaves-room` described an execution that does
    not happen. Its summary said the first member "waits behind its own sibling and runs
    after it" and that "the order the two members ran in is the whole claim"; only one
    member ever runs, and its own Lab test asserts the other never started. It also
    cannot constrain what it was promoted for: dropping the `EventLaunchFailed` departure
    from the admission queue leaves that Blueprint, the whole placement corpus and every
    Lab law green, and only the orchestrator test over a log that stops between the
    failure and the replacement goes red. Both are said plainly now, in the fixture, in
    the test, and in the coverage list, which stated the order claim flatly.
  - Ordering still ages on the whole wait, and that is a decision rather than an
    oversight. The bounds are promises about what Mercator does, so they are charged by
    cause; the ordering is about which work has gone longest without an answer, which is
    the same question whoever caused the delay. A Run its family holds holds no queue
    anyway, so the only thing this decides is what such a Run is worth when it competes
    again.
- [x] 2026-07-27: No capacity is free. A candidate's price was a rate times one Run's
  seconds, plus a setup fee charged to every machine whether or not Mercator had to buy
  one, and both mistakes point the direction that spends money: they make capacity
  Mercator already holds look cheaper than it is. A price is now four terms and the
  record carries all of them.
  - Rent for seconds inside an interval Mercator has already committed to is charged to
    whoever spends those seconds. The invoice arrives either way, so the money is not
    what the decision changes; the seconds are, because nothing else can have them
    afterwards. That is what an owned machine's shadow price states, and it is why an
    idle owned machine is not free.
  - Rent beyond that interval is what the placement itself commits Mercator to, bought
    in whatever increment the publisher sells, and the part of that increment nothing
    will use is the idle tail. An hourly machine asked for twenty minutes costs the
    hour; billing the twenty minutes reported two thirds of the bill to nobody.
    `PriceModel.GranularitySeconds` is what the increment is read from. All four
    adapters have written it since they were authored and nothing had ever read it, so
    no fixture could state a world where the increment mattered.
  - The setup fee and the minimum charge are asked only of capacity Mercator has to
    acquire. Charging them to a standing machine priced a machine already running as
    though it were being bought again.
  - The seconds a Run spends of a commitment are counted from the Run's own start rather
    than from the decision's moment. Two Runs queued on one machine occupy different
    seconds of one interval, and charging each of them everything still outstanding
    would count the same money twice and report a fleet costing more than the invoices
    it will get. `safety.committed_cost_is_not_double_counted` states that over the
    placements Mercator took, and `TestTwoRunsMaySpendOneCommittedHourBetweenThem` is
    the lawful half, so the law is not a ban on committed rent.
  - Two terms of a sale are refusals rather than prices, because there is nothing to
    trade off. Capacity held for particular service classes refuses every other class
    outright, which is how reserved capacity is stated, and capacity that stops being
    Mercator's at a declared moment refuses work that could still be holding it then.
    The window is judged against the runtime Mercator enforces rather than the one the
    caller guessed, because admitting on the guess puts work on a machine that goes away
    underneath it whenever the guess is short.
  - An operator states the rest of the sale at invitation, `node.Purchase`: the block
    the machine is bought in, the classes they hold it for, and the moment it stops
    being Mercator's. Every part is optional and every absence is an answer rather than
    a default. No increment is a machine bought in no blocks at all, which is an
    operator's own hardware: Mercator holds it continuously, so no second of it is a
    fresh commitment and there is no tail to charge, and that is the same silence
    `GranularitySeconds` already meant. Where the current block ends is derived from
    enrolment rather than configured, because that is the moment Mercator started paying
    for this generation of this machine.
  - Three of the terms this slice was scoped to carry are not priced, and the reasons are
    in the section below rather than left as silence: stopped-state storage, preemption
    risk, and warm-capacity opportunity cost as a term of its own.

- [x] 2026-07-27: Replanning by explicit policy, and a refusal that is not terminal.
  Reconciliation's mechanics were already real and its policy was implied, and the
  operation store's dedupe was state-blind, so a machine that refused a pull was
  answered Duplicate for that content forever.
  - An operation identity is reissuable exactly when a refusal can have left nothing
    behind, which is the line `CommandKind.MayLeaveEffectOnFailure` already drew for
    the node agent's own retry rule: a failed pull left nothing, a failed launch may
    have made the container. So a refused preparation is asked again and a refused
    launch keeps its identity spent, and `nodetest.RunStoreSuite` carries both
    promises so the in-memory and SQLite stores cannot drift apart on them. The
    refused row is rewritten in place rather than appended beside itself, because
    the sequence is where the node was first told about this content and a second
    row would redeliver in the wrong order after a reconnection.
  - `PrepareReceipt.Refused` is the third answer the seam was missing, and the
    orchestrator's memory now remembers what the far side took on rather than what
    Mercator asked for. Remembering a refusal as asked-for is what made it permanent
    at the control plane: the desire is recomputed identically on the next sweep and
    an unchanged desire is not resent. Nothing in production fills that field yet,
    so this is the seam being right rather than a production lane changing
    behaviour: `broker.Prepare` answers Started or Duplicate and a node settles a
    refusal asynchronously, so what triggers a second ask on a node is still a
    change to the desired set. What this slice makes true there is that the second
    ask reaches the runtime when it comes.
  - A refusal is answered by the machine that refused. `PrepareItem.Identity` is
    what one item of a desired set is called, the machine and the content
    together, and the receipt, the controller's memory, and the key that memory is
    held under all name items by it. Matching a refusal on content alone let one
    host's refusal erase the memory of the same content another host had taken on,
    which collapsed the memory to the empty key: the next desire computed after
    those Runs were withdrawn read as unchanged and the withdrawal for the transfer
    that was really running was never sent.
    `a-refusal-on-one-machine-is-not-a-withdrawal-on-another` is the world that
    fails on it.
  - Wanting nothing is a desire with a key of its own. It used to be the empty
    key, which is also what a control plane holds for every workspace before it
    has asked for anything, and those two are opposite instructions: one leaves a
    fleet alone and the other stops everything speculative on it. So a Mercator
    that restarted after the Runs waiting on a transfer were withdrawn computed a
    desire naming nothing, read it as unchanged, and let a hundred gigabytes land
    for Runs that no longer existed. The memory is in process on purpose and that
    is still right, because the desire is derived from the log every time; what was
    wrong was a memory that could not tell its own absence from a state it had
    reached. A control plane with no queued work now sends one desire of nothing at
    startup, which costs a machine nothing, and the rate bound does not count it,
    because that bound is on how often Mercator may begin preparing and a
    withdrawal begins nothing.
    `a-restart-still-withdraws-what-nobody-waits-for` is the world that fails on it.
    It stops at the Lab: withdrawal has no node command yet, so nothing at higher
    fidelity can observe one.
  - The Lab world can refuse a fetch, under a `reject_command` fault on
    `node.prepare_image` or `node.prepare_artifact`, and holds the same rule the
    store now does: a refused fetch is not remembered as work it took on.
    `a-refused-prepare-can-be-asked-again` is the fixture. It is a conformance entry
    rather than the top-level path the slice named, because `LoadCorpus` refuses an
    arrival-driven Blueprint there: every top-level entry is a placement fixture.
  - `janitor.OrphanPolicy` is the replanning half, stated once and recorded with
    every decision. Capacity whose recorded launch says the machine outlives its
    workload is adopted, its slot released and the machine kept; everything else
    stops existing. The behaviour change is that capacity Mercator cannot account
    for is destroyed rather than half-reclaimed: releasing only its slot left a
    machine billing that no Run could ever be placed on. A Run that closed with no
    cleanup ever asked for is converged too, which is the hole a sweep keyed on the
    cleanup request alone could only skip.
  - The launch that took the capacity is what decides, whenever the record holds
    one. Reading the cleanup request first destroyed the whole machine under a Run
    that reached a launch on a pool Mercator does not own and then ended without
    anybody asking for its capacity back, which is the ordinary end of a launch
    whose attempts ran out.
    `closed_without_a_cleanup_request` is what is left for a Run that recorded no
    launch at all, and
    `an-orphan-is-adopted-or-destroyed-by-policy` states that combination.
  - A Run holds one recorded launch per attempt, so reading its last one was
    reading whichever attempt happened to be last. A Run replaced from a machine
    Mercator provisioned onto a slot Mercator only borrows records terminate and
    then release, and the machine the first attempt took was then handed back as a
    slot and left billing with no Run that could ever be placed on it; the reverse
    mix routed a terminate at a pool Mercator has no right to destroy. The launch is
    now found by the identities the capacity itself carries, which is what every
    adapter reads back off its own labels, tags, or environment. Capacity carrying
    none of them is still decided by the record when every launch of the Run handed
    capacity back the same way, and when they disagree nothing in the record can say
    which machine this is, so it stops existing rather than being kept on a guess.
    `a-machine-two-launches-disagree-about-is-not-adopted` states it.
  - The decision is recorded before the provider is asked to act on it, and a
    sweep that finds capacity already decided about carries out that decision
    rather than judging it again. Acting first left the one failure the rule calls
    a violation and cannot be recovered from: a terminated machine is never listed
    again, so nothing remains for a later sweep to explain. `internal/janitor`'s own
    tests are the whole of what holds this, and no rule in the corpus can see it:
    the two orderings differ only when a reclaim fails, the Lab world's provider
    cannot be made to refuse one, and a sweep that returned an error would fail the
    Lab control plane's tick rather than leave a state a rule could read. Making the
    corpus hold it is owed.
  - A provider that answers `ErrTerminateUnsupported` is saying there is no
    machine of Mercator's to destroy, so the slot is given back and that is the
    whole of the capacity ceasing to exist. Local Docker answers exactly that, and
    stopping at the refusal returned before every later object in the same
    listing, so one container nothing could account for stopped every sweep of
    that workspace from then on. This one is a provider's vocabulary rather than a
    world's, and `internal/janitor` and the Docker adapter are what hold it: every
    machine in the Lab can be destroyed, so no Blueprint can state a provider that
    refuses to.
  - `safety.orphan_policy_is_explicit` is stated beside `liveness.orphan_convergence`
    rather than in place of it. The two read different facts: one asks that no
    execution Mercator launched outlives the Run that owns it, and the other asks
    what became of capacity the world was already holding that Mercator never
    launched. Dropping the first left every projection defect that strands a
    running execution invisible to the whole corpus.
  - `world.orphans` is the Blueprint vocabulary, and it is deliberately not an
    execution. Every rule about the fleet reads an execution as work Mercator is
    accountable for, so folding the two together would make capacity nobody
    recognises look like a launch with half its identity missing, which is what
    `safety.owned_external_resources` exists to refuse. The Lab control plane runs
    the janitor after the Runs settle, for the same reason production does.
- [x] 2026-07-27: Close phase 4. The whole verification was run on the amd64 Linux
  workstation rather than the arm64 macOS laptop the phase 3 slices were built on,
  and no test failed for a platform reason. What follows is the set of judgment
  calls the phase made that a reader would otherwise have to reconstruct from
  twelve slices of commit messages.
  - The score weights are alive, and the class is what populates them. This was
    the phase's stated debt: `SchedulingInput.Weights` reached production with
    only `StartLatencyUSDPerSecond` set, to 0.0005 for the balanced objective, so
    the reliability, uncertainty, and completion-latency terms were multiplied by
    zero and phase 3 slice 9 deliberately declined to route a new answer through
    them. The exchange rates are now stated by the ServiceClass, and the decision
    records the weights it was scored at, so the score can be recomputed from the
    record rather than trusted. `safety.score_is_reproducible_from_the_record` is
    the rule, and the oracle derives the same score independently rather than by
    calling the scheduler.
  - A transfer's seconds may never come from launch history. This is the phase's
    least obvious rule and the one most likely to be undone by a well-meaning
    change. A duration is a byte count over a throughput, and the byte count
    belongs to the launch rather than to the candidate: `CandidateIdentity` names
    the machine and the image, and can name neither the bytes already resident nor
    the Run's inputs. So `levelKeys` files and answers nothing for `image_fetch`,
    `unpack`, or `artifact_fetch`, and `safety.prediction_states_its_provenance`
    refuses a transfer answered from launches. Without that rule a host holding
    every byte of a 40GB dataset was charged the full cold fetch out of history
    and struck out on the latency bound.
  - A wait is charged to whoever caused it. The maximum queue delay is a promise
    about what Mercator does, so it is asked of the part Mercator caused; the
    deadline is about when the answer stops being worth having, so it is asked of
    the whole wait. Ordering still ages on the whole wait, because ordering is
    about which work has gone longest without an answer rather than about who is
    to blame. `domain.Wait` carries both numbers and the record states the
    division.
  - The idle tail is deliberately conservative. The unused remainder of a billing
    increment is charged whole to the placement that forced Mercator to buy it,
    and a later Run that uses part of that remainder is charged nothing. Splitting
    it needs a model of what arrives next, and every substitute tried made a
    longer Run cheaper than a shorter one. The error is in the safe direction, and
    the alternative was charging the remainder to nobody.
  - A declared field nothing reads is a defect this plan has deleted repeatedly,
    so the ServiceClass declares only what has a reader. Priority, aging, maximum
    queue delay, deadline, and backfill eligibility are on `domain.Admission` and
    read by the queue. Maximum cost stays on `PlacementPolicy`, because a budget
    is per Run rather than per class and a second copy would be two authorities
    for one refusal. Group parallelism, interruption permission, and the
    queue-on-warm preference each become a field in the slice that prices them.
  - Three economics terms named in the phase goal are deliberately unpriced, each
    for a stated reason rather than for want of time. Stopped-state storage needs a
    next-arrival model. Preemption-risk pricing needs a hazard over the length of a
    Run, and what the probability multiplies is the placement the work would move
    to rather than this one. Warm-capacity opportunity cost would double count,
    because an owned machine's shadow price already says its seconds are worth
    something to somebody else. Reserved capacity is delivered as eligible service
    classes rather than as a concept of its own, and is not deferred.
- [x] 2026-07-27: Give the corpus the words for capacity, and the Effect Ledger the
  operations. Nothing acts on either yet, and that is the order: the rules that
  read a provision are already registered, so the day a provider really emits one
  nothing else has to change for them to see it.
  - A marketplace listing states what its provider negotiated over the machine,
    carried as `capability.CapacitySupport` itself rather than as a Blueprint copy
    of it. A parallel struct would be a translation of nothing and would drift, and
    the whole point of the field is that a fixture states the set a real provider
    answers with. It is a pointer, because a listing that negotiated nothing and a
    listing whose provider can do nothing are different sentences, and every
    listing in this corpus is the first.
  - The set is a set of separate promises rather than a list of the capability
    names a provider ticked. A provider that suspends a machine and cannot bring
    the same one back exists, and a name list cannot tell a resume nobody offers
    from a resume nobody mentioned.
  - `CapacitySupport.Validate` is the contract's own coherence rule, and `Declare`
    calls it, so an incoherent set is refused where the connection is built rather
    than found later by whichever caller reads two of its fields together. A resume
    without a stop, a persistent disk without a stop, an unknown idempotency
    mechanism, and `none` with no owned listing are each refused. The last is the
    one that matters most: a provider that deduplicates nothing and lists nothing
    leaks every machine a lost response allocated, and there is no later moment at
    which Mercator could find those machines to account for them. The Blueprint
    door calls the same function, so a fixture cannot state a provider Mercator
    would refuse to build.
  - `capacity` and `bootstrap` belong to the reusable lane and are refused on an
    `ephemeral` listing, because `Declare` stamps every connection that provides
    capacity reusable. Capacity
    implies the lane rather than accompanying a choice of one, and a one-shot
    execution product holds nothing after its workload exits, so it has no machine
    to stop, no machine to bring back, and no agent to enrol. Without the rule the
    corpus could describe a provider-native one-shot execution that suspends a
    machine and keeps its disk between Runs, which is the conflation ADR 0005 and
    `safety.ephemeral_capacity_not_reused` exist to prevent.
  - A listing that states any of it must name its `machine`. Every promise in the
    set is about one machine keeping its identity through a stop, a resume, a
    repeated provision, or a terminate, and a listing ID is numbered afresh on every
    search.
  - `bootstrap.never_enrolls` is stated rather than derived from a missing
    `agent_ready` stage. Silence there already means a stage that costs nothing, and
    a bootstrap that costs nothing is a machine ready the instant it booted, so
    folding the two together would make the failure a provider bills for
    indistinguishable from the fastest possible success. A listing that says its
    agent never enrols therefore states no `agent_ready` at all, which is the one
    provisioning stage a listing may leave out: a stage that never completes has no
    seconds to state. It must name a deadline, because a machine nobody gives up on
    bills for ever. The entry dated 2026-07-28 below deletes the `reclaim_after` this
    originally offered as the alternative: no world performed it.
  - The ledger gains six capacity operations and one enrolment.
    `capacity.provision`, `capacity.stop`, `capacity.resume`, and
    `capacity.terminate` change what a provider holds for Mercator and are counted
    by `effectMutatesWorld`; `capacity.observe` and `capacity.list_owned` are reads
    and deliberately are not, because two reads answering differently is a machine
    whose state moved and counting them would make every reconciliation sweep a
    violation. `node.enrolled` is not counted either, for the reason
    `capacity.preempted` is not: both are the world acting on its own account rather
    than a command Mercator issued under a key a provider honours. They are separate
    from `provider.launch` and `provider.release` on purpose: those are an execution,
    and ADR 0005 is the reason a stop that suspends a machine may never be filed
    under a release that ends a workload. `capacity.resume` is named for the promise
    the capability set negotiates rather than for `StartCapacity`, the method that
    performs it.
  - `Compile` refuses an arrival-driven Blueprint whose listing states a bootstrap,
    for the reason it already refuses a seeded Rental Schedule. Provisioned capacity
    bootstraps no agent in the Lab, so a listing saying its agent never arrives would
    compile into a world that enrols nothing either way, and a fixture about a
    stranded machine would read green beside a fixture about one that enrols
    perfectly while both described the same world. A world statement that reaches
    nothing is an error rather than a silence. The two new Blueprints are placement
    fixtures, so they do not go through that path; a conformance fixture that wants
    this world arrives with the provider.
  - No Lab invariant, and the reason is the registry's own rule.
    `TestEveryDefaultInvariantHasADeliberatelyFailingCase` refuses an invariant no
    world can make fail, and no world emits a capacity operation yet. What lands
    instead is the classification and two tests over a hand-written ledger, one for
    each direction: an operation left out of `effectMutatesWorld` is one
    `safety.idempotent_external_commands` walks straight past, and an operation
    wrongly counted fails on a machine seen starting and then active. Both tests read
    the whole registry rather than the one rule they are about, because a ledger entry
    that trips a rule with no business reading it is the same defect as one the
    intended rule misses.
- [x] 2026-07-27: Take the stale claim off
  `enrolled-node-survives-its-first-run`. It declared `rental_schedule` beside
  `node_bootstrap` and `execution_warms_capacity`, and the Rental Schedule store,
  its versioning, and its reservation are wired end to end with four green fixtures
  exercising them. A target naming a capability the tree already has cannot be read
  as evidence of anything. Its listing now names the machine it becomes and the set
  its provider negotiated, and the new target
  `provisioned-capacity-enrolls-or-is-reclaimed` states the other half of the same
  transition: acquisition and boot succeed, the agent never opens a session, and
  the record has to say the start was never observed and the application never
  spoke. The corpus moves from 59 Blueprints, 56 green and 3 target, to 60, 56
  green and 4 target.
- [x] 2026-07-27: Fix what two reviewers refuted in the slice above. Every finding
  held, two of the six being one defect stated twice, and all are fixed at the
  source; the entries above are corrected where they stated something the tree no
  longer does.
  - `prefetchSettlements` decoded the request projection of every accepted effect
    before deciding which operation the effect was, so an accepted
    `capacity.provision`, whose answer lives in the consequence and which projects no
    request at all, was a malformed effect rather than an operation the rule ignores.
    The whole registry over the ledger the new test builds reported three violations,
    not one: the intended `safety.idempotent_external_commands` plus
    `safety.prewarm_yields_to_real_work` and `liveness.prefetch_converges`, both with
    a JSON decode error. The slice's central claim, that the registered rules can read
    a provision with nothing else changing, was false. Which operation an effect is
    now decides whether the rule reads it at all, as its two sibling readers in the
    same file already did, and both new tests read the whole registry.
  - `node.enrolled` is no longer counted by `effectMutatesWorld`. Counting it
    asserted that one enrolment identity yields one byte-identical consequence, and
    the node registry is built the other way: `Reinvite` mints a fresh token for an
    identity that already exists, so an agent that restarted or let its lease lapse
    enrols again under the same node and generation, and every successful `Enroll`
    closes the open session, mints a new one, and returns the next fencing token. A
    node that came back from a reboot was a violation. A replayed token is refused
    with `ErrEnrollmentSpent` rather than answered as a duplicate, so an enrolment
    stays redeemable once whatever the ledger does: that guard is the registry's.
  - The simulated world now honours `bootstrap.never_enrolls` instead of noting it
    and enrolling anyway. Nothing can create a container on a machine Mercator has no
    session to, so such a machine records no start moment and reports no readiness.
    Refusing the statement in `Compile` alone was a guard in the wrong place: no
    placement fixture uses that path, so a green Blueprint could state `never_enrolls`
    and read green while its world booted the machine, started the workload, and
    reported it ready at eight minutes twelve. It also settles what the exemption from
    stating `agent_ready` was worth: provisioning does not complete on such a machine,
    so it is never the listing that provisions fastest, and the published `expected`
    and `p90` the scheduler predicts from are stated as they are for every other
    listing.
  - `provisioned-capacity-enrolls-or-is-reclaimed` asserts the reclaim half its name
    promises. Stating only the outcome, the offer, and the two absent stages described
    an indefinite wait: a control plane that provisions the silent machine and then
    does nothing at all satisfied every expectation, so once the world honoured the
    statement the fixture read as passing and would have been promoted to green as
    evidence of a reclamation nobody built. It now carries a second, dearer listing
    whose agent does arrive, and its last step expects the work to move there under an
    appended decision that names the first.
  - `capacity` and `bootstrap` are refused on an `ephemeral` listing, recorded with
    the rule itself in the entry above.
- [x] 2026-07-27: Fix what two reviewers refuted in the fix above. Both findings held
  and both were the same class the entry above claims to have fixed: an expectation
  that could not fail.
  - The world's claim that a stranded machine holds none of the image was a property
    of the fixture rather than of the guard. Content is recorded only for capacity
    that keeps what it runs, so no provisionable machine in that world holds anything
    whatever its bootstrap does, and the assertion passed with the guard deleted. What
    is true is stronger and belongs at the world's door: Mercator keeps a machine
    through the agent enrolled on it, so `AddMachine` refuses capacity it keeps
    alongside an agent that never enrols, and there is now no world state in which one
    can ask whether a stranded machine got warm. The readiness half was vacuous too,
    because the world under test never said its applications come up at all, so no
    launch in it recorded readiness; the world now says they do, and the enrolled
    machine beside the stranded one is the control the silence is read against.
  - `provisioned-capacity-enrolls-or-is-reclaimed` asserted nothing that depended on
    time, so shortening its `advance` from twelve minutes to thirty seconds produced a
    byte-identical failure list: it could not tell reclamation after the stated
    deadline from giving up on any machine whose start has not been observed yet,
    which in that fixture's own world would abandon the healthy machine too. It now
    reconciles on both sides of the ten minutes, and the earlier look expects the
    first answer still standing.
  - The target pinned `PREVIOUS_LAUNCH_FAILED`, which in this tree means the launch
    call failed with a side effect of `none`: nothing was created, which is the
    opposite of a machine a provider allocated and is billing for.
    `SupersededCapacityReclaimed` is the reason it states now, and it is checkable
    against the Run's own record rather than taken on trust, because the step also
    asserts the confirmed terminate on that machine before the work moved. The
    expectation vocabulary gained `reclaimed` for that, read out of the cleanup and
    the launch intent that names the machine the cleanup ended. Nothing produces the
    new reason yet, which is the point of a target; the public contract is unchanged
    until the behaviour that emits it lands with `node_bootstrap`.
  - Neither half of the corpus could exercise `reclaimed` from both sides, because a
    Run's cleanup is the last thing that happens to it and no world in the corpus
    hands capacity back and then decides again. It is read from both sides through
    `Run` against a replayed stream instead: handed back before the answer changed,
    handed back after it, and released rather than terminated.
- [x] 2026-07-28: Make a placement that chose to provision an act, and give a
  machine nobody came for an end. `provisioned-capacity-enrolls-or-is-reclaimed`
  is green with its `missing_capabilities` emptied, and the corpus moves from 56
  green and 4 target to 57 and 3.
  - The control plane gained two seams. `orchestrator.Capacity` is the lease,
    satisfied by the Broker, and `orchestrator.Inviter` is the node registry.
    They are separate from `orchestrator.Adapter` because Adapter is an
    execution: ADR 0005 is why a terminate that destroys a machine may never be
    filed under a release that ends a workload.
  - What a Run placed on capacity to provision now does, in order: reserves its
    Booking on the Rental the decision minted, sweeps the connection's owned
    capacity for a machine already allocated against that Rental, invites the
    node the machine will be with the listing's own rate on it, hands the
    provider the bootstrap verbatim, and watches. Acquisition and boot are read
    from the provider; whether an agent opened a session is read from the
    registry, because nothing else can answer that. Each stage is recorded when
    it completes with the seconds it really took, measured from the stage before
    it.
  - The owned-capacity sweep runs before every provision rather than only after
    a failure. A command whose response was lost and a command never sent leave
    Mercator's own record saying the same thing, so there is no state in which
    the cheaper check would be correct.
  - The enrollment token is never written down, in the public payload or the
    private one. It is minted at the moment it is handed to a provider, and what
    the record carries is the node identity it was minted for.
    `TestTheProviderIsHandedTheBootstrapVerbatim` reads every event this Run
    records back and refuses any that carries it.
  - Reclamation is its own event and not a cleanup, because a cleanup ends a Run
    and this is a Run that never started. The terminate happens before the work
    moves and in the same commit that releases the Booking, and a provider that
    refuses to destroy the machine leaves the Run exactly where it was:
    `TestTheWorkDoesNotMoveUntilTheBillEnds` is that clause, and without it a
    recorded reclamation would read the same in a world where the machine is
    still billing.
  - Offer exclusions are typed. A bare list of IDs could only say "an earlier
    attempt refused this", which reads a machine somebody else was using and a
    machine Mercator allocated and destroyed as one fact.
    `PREVIOUS_ATTEMPT_CAPACITY_RECLAIMED` is the second code.
  - Mercator's enrolment patience is fifteen minutes and a listing may state its
    own. There is deliberately no value meaning "wait for ever".
  - `liveness.provisioned_capacity_enrolls_or_is_reclaimed` is registered with a
    thirty-minute bound and a deliberate failing world: an accepted
    `capacity.provision` an hour old with no `node.enrolled` and no
    `capacity.terminate` against its Rental. It is stated over the ledger alone,
    because Mercator's own record can say a Run moved on and only the ledger says
    whether the machine it moved off is still allocated.
  - `safety.capacity_lifecycle_is_negotiated` is NOT registered, and the reason
    is the registry's own rule. Nothing in the tree stops or resumes capacity:
    this slice performs provision, observe, terminate, and list_owned, and
    `CapacitySupport.Claims` returns true for the first three unconditionally.
    The production half of that promise is already made where the provider is,
    in `Broker.providerFor`, which refuses an operation a connection never
    claimed before any request is sent. A rule policing an act no path performs,
    against a set no ledger entry yet carries, would be a rule that could only
    fail against a hand-written observation. It lands with the slice that stops
    or resumes a machine.
  - Two green Blueprints changed, and it is the honest consequence rather than a
    fixture bent to fit. `a-launch-is-eight-stages` and
    `a-start-is-a-moment-somebody-observed` both measured a start latency from
    the moment the launch was accepted, and that moment used to be before the
    machine existed, so the figure they asserted was provisioning plus the image.
    The three provisioning stages are now spent under the lease with three
    actuals of their own, so what is left between the launch being accepted and
    the container starting is the content the machine still owed: 58s and 288s.
    Both also need one more look, because a control plane that never looks never
    launches.
  - `capability.CapacityProvider.Provision` is now `ProvisionCapacity`. Every
    sibling act was already named for the lease, and a provider that also
    launches workloads has two things it could be asked to provision.
  - `Compile` no longer refuses a listing that states a bootstrap. That refusal
    was correct while the Lab built no machine from a listing; it now allocates
    one, honours `never_enrolls`, and carries the stated patience onto the offer,
    so the statement is performed rather than turned away. The test that guarded
    it is replaced by two conformance cases over the Effect Ledger:
    `provisioned-capacity-becomes-a-machine-mercator-holds` asserts an accepted
    `capacity.provision`, a `node.enrolled` naming the same Rental, and a
    `provider.launch` after both, in that order, with nothing terminated; and the
    same Blueprint with `never_enrolls` set asserts the absence, which is a
    machine allocated, no session opened, and nothing launched.

- [x] 2026-07-28: Fix what two reviewers refuted in the entry below, which was the
  fix for the entry below that. Three defects held, and both of the entry's own new
  assertions were among them: one compared a value with itself, and the other could
  not tell a measurement from a polling interval.
  - `safety.enrolment_names_the_generation_it_was_invited_for` compared the
    provision's generation against itself. The Lab stored `command.Generation` on the
    lease and wrote that one field into both the provision facts and the enrolment
    facts, so the generation the machine's bootstrap was actually minted under never
    reached the ledger. The lease now holds the bootstrap it was handed, entire, and
    the enrolment is recorded under what the agent redeems. Verified against the
    control plane rather than against the recorder: `allocateCapacity` sending
    `Generation: requested.Generation + 1` fails 15 Lab tests mid-drive, and at
    3b2c3e4 the same defect drove a fully green `go test ./...`.
  - `simulatedWorld.Enrolled` ignored the generation on the `NodeRef` while
    `node.Registry` refuses a mismatch, so the Lab could not model the failure the
    rule is about at all. It now refuses with the sentence the real registry writes.
    The mirror-image defect, inviting under generation 2 while provisioning under 1,
    now fails the Lab the way it fails production instead of reading as a machine
    ready to launch on.
  - Provisioning stages were recorded at `now.Sub(since)` with both ends at reconcile
    moments, so the seconds were the world's spend rounded up to the polling grid.
    The fixture's 30s, 4m and 45s were all exact multiples of the fifteen second
    cadence, which is the only reason the assertion held. Each stage is now dated by
    the authority that owns it: `CapacityObservation.StateSince` is the provider's own
    account of when the machine entered the state it reports, and `Inviter.EnrolledAt`
    replaces `Enrolled` so the registry answers with the moment the agent opened its
    session. Where an authority will not date a transition the record carries
    `bounded`, which is the honest reading and what keeps a calibration from training
    on a polling interval. The fixture leaves the grid: 37s, 4m7s, 51s.
  - The judgment call is to model the undated case rather than to require dating.
    A provider that reports a state without a since is a real product, and refusing
    to record anything for it would lose the one fact Mercator does have. What is
    refused is the pretence: the seconds are published as a bound and marked as one.
  - No Blueprint states either defect, and none can. A scenario describes a world,
    and both are Mercator's own acts, so the deliberate failing cases reach the
    world's two contracts directly and the control-plane evidence is the injected
    defect recorded under Verification evidence.

- [x] 2026-07-28: Fix what two reviewers refuted in the entry above. Four findings
  held, and all four were the same class the entry claims to have fixed: a statement
  that reaches nothing, or an assertion that could not fail.
  - The listing's stated patience reached the offer and nothing measured it. The
    fixture stated fifteen minutes, which is `enrolmentPatience` exactly, so even a
    case that measured the reclaim moment could not tell the listing's patience from
    Mercator's default, and replacing `state.offer.Bootstrap = ...` with a discard
    left the whole tree green. The fixture states eight minutes now, and
    `TestALabMachineWhoseAgentNeverArrivesIsHandedBackAtTheStatedPatience` reads the
    machine handed back one reconcile inside it. With the assignment discarded, the
    ledger holds no `capacity.terminate` at all inside the twelve minutes it drives.
  - `bootstrap.reclaim_after` is deleted, along with `CapacityBootstrap.ReclaimAfterSeconds`.
    It had one writer and no reader anywhere: no world performed it, so two
    Blueprints stating a five minute and a forty five minute provider backstop
    compiled into byte-identical worlds and neither ever destroyed anything. It was
    stated in two fixtures decoratively, and one of them was the new conformance
    case, which is exactly the dishonesty the deleted `Compile` guard existed to
    prevent. The judgment call is to delete rather than to perform it: a machine
    ending on its own account while Mercator still holds a Booking on it is a state
    the control plane has no answer for, and building the answer is a slice rather
    than a review fix. The vocabulary comes back with the act, which is the same rule
    `safety.capacity_lifecycle_is_negotiated` is held to above. A `never_enrolls`
    listing must now name a `deadline`, because Mercator's patience is the only thing
    that ends such a machine and the fixture has to name the bound it is judged
    against.
  - `Compile` is narrowed rather than left open. It refuses a listing stating more
    patience than `provisionedCapacityBound`, because
    `liveness.provisioned_capacity_enrolls_or_is_reclaimed` accuses any machine still
    waiting after thirty minutes: a Blueprint saying Mercator waits forty produced
    that accusation against a control plane obeying the fixture, and before the guard
    was deleted it was refused at compile time. That the harness bound is longer than
    any patience the corpus states was true by coincidence and is now enforced.
  - The generation binding is asserted for the first time.
    `safety.enrolment_names_the_generation_it_was_invited_for` is registered with its
    own deliberate failing world, and the conformance case compares the whole lease
    rather than the Rental alone. Recording `lease.Generation + 7` in the Lab's
    enrolment now fails the corpus mid-drive; before, it was green. Refuted by the
    review round below: that evidence mutates the recorder rather than the control
    plane, and the rule as registered here compared the provision's generation with
    itself.
  - The conformance cases reconcile every fifteen seconds rather than every three
    minutes, and `TestEveryProvisioningStageIsRecordedAtWhatTheWorldSpent` holds each
    stage to the seconds this world really spends. At the old cadence the record
    carried the poll interval, including an `agent_ready` of zero for a stage that
    took forty five seconds. The zero itself is not a defect: two stages found
    complete in one look share a moment, and splitting an interval nothing observed
    would be the control plane inventing a boundary. What was wrong was a green
    fixture whose record read that way and asserted nothing about it. Refuted by the
    review round below: a finer cadence narrows the error and does not make the
    assertion capable of detecting it, and every stage in the fixture was an exact
    multiple of the new interval.
  - Not fixed here, and filed instead: a Run whose machine was provisioned under the
    capacity lease has the listing's whole provisioning attributed to its launch, so
    the Run Bundle reads `start_latency_seconds` predicted against an actual measured
    from launch acceptance rather than from admission. It predates this work, it is
    true of `every-stage-of-a-launch-has-an-actual` too, and it needs a decision about
    which of the two accounts of the same stages is authoritative. Filed as #205.

- [x] 2026-07-27: Make the capacity contract reachable from the control plane. Every
  one of `CapacityProvider`'s nine methods was called by nothing, `Backend.Capacity`
  had no caller, and `capability.Declare` refused capacity without a `NodeRuntime` on
  the same Go value, which is a shape no provider adapter can have. A connection can
  now sell capacity without selling one-shot execution, and the machine lifecycle is
  a call the control plane can make.
  - A `CapacityProvider` declares the reusable lane, because a machine a provider
    allocates and holds is capacity that outlives the workload run on it. There is no
    second condition, and the first two attempts at one both failed: a `NodeRuntime`
    on the same Go value is a shape no provider adapter has, and a `NodeRuntime`
    anywhere in the deployment is a shape every deployment has, because `daemon.New`
    always builds a node registry and `Registry.NodeSupport` is a static literal that
    consults no enrolment. That check refused nothing in any real deployment while
    licensing a Rental identity, a prewarm claim and an artifact replica claim about
    machines Mercator had not allocated.
  - Whether a workload can run on one machine is that machine's own fact, established
    by the agent enrolled on it, so the rule is made where offers are published. A
    capacity connection publishes no placement candidate. What `ListCapacity` returns
    is capacity to acquire, and acting on that selection means provisioning a Rental
    and bootstrapping an agent onto it, which is
    [#200](https://github.com/benngarcia/mercator/issues/200).
  - Publishing it before then had no correct outcome, and both halves were reproduced
    against the real daemon. Stated as completely as a provider honestly can, the offer
    is struck out of every placement with `UNKNOWN_FACT container.max_containers`,
    `CAPACITY_UNAVAILABLE capacity.available` and
    `CAPABILITY_MISMATCH container.supports_digest_refs`, and every decision record in
    the workspace carries a candidate that can never be feasible. Stated with those
    container facts filled in, which is a provider asserting a container runtime it
    does not own on a host that may have no agent, Placement selects it, the record
    says `disposition:run_now_existing_rental` and `REUSE_EXISTING_RENTAL` for a machine
    nobody rented, and the launch fails because the offer's `NativeRef` resolves to no
    enrolled node. The Run is now told `NO_FEASIBLE_OFFERS` and
    `NO_CAPACITY_FITS` instead, which is the truth about a fleet with nothing on it.
  - A machine that does have an agent on it is published once, by the node registry,
    from the enrolment: the Rental the invitation named and the container runtime,
    idempotent launch, free capacity, image inventory and disk the agent reported.
    Publishing the provider's own listing of that same machine beside it counted one
    host twice under two Rental identities, gave one machine with
    `MaxConcurrentWorkloads` of 1 two independent Rental Schedules, and split
    per-machine learning between the node ID and the provider's instance ref.
  - A Rental identity is Mercator's to mint. `StampLane` now clears whatever
    `rental_id` an adapter stated, in every lane rather than only where the lane
    cannot become a Rental, and aggregation mints none: it used to mint one for a
    standing offer in the reusable lane, which is `OfferKind` answering a question it
    does not answer. Kind says who owns the host, so a Vast-style listing of somebody
    else's idle machine is standing, and a Booking bound to it accumulated Warmth and
    a queue against capacity nobody had allocated.
  - A connection that implements `CapacityProvider` and `EphemeralExecutor` at once is
    refused. One lane is stamped on every offer a connection publishes, so a backend
    answering both `ListCapacity` and `ListOffers` would publish machines and one-shot
    executions under one word and nothing downstream could say which an offer came
    from. A provider that sells both is two connections, and promoting one is
    deliberate rather than a precedence rule inside the Broker.
  - The ownership sweep converges workloads, and a capacity connection is running
    none. It holds machines, and a machine carries no Run because a Rental outlives
    the Run placed on it, so filing one as an `adapter.OwnedExternalObject` made
    `janitor.decide` read it as capacity nobody could account for. The sweep then
    wrote a durable `compute.capacity.orphan_converged.v1` with
    `outcome:terminated reason:unattributed` about a live machine, failed to carry it
    out because `Broker.Terminate` resolves an `EphemeralExecutor` a capacity
    connection does not have, and aborted before reaching the one-shot executions that
    were genuinely billing. The recorded decision was sticky, so every later sweep
    read it back and failed identically. A capacity connection now reports no workload,
    and machines are swept against Rental records by #199 in the Rental's own
    vocabulary.
  - What a capacity connection is asked for is settled on `Backend` rather than at the
    call site, which is what fixed the sweep of the whole workspace: asking such a
    connection for one-shot executions used to fail every reconcile of every workspace
    that held one.
  - The five capacity commands are one method each on the Broker, resolving the
    connection from the command's own `CapacityRef` so a reconciler can act after a
    restart with nothing in memory to look the machine up in. `CapacitySupport.Claims`
    answers which negotiated promise each command needs, and a command a provider
    never promised is refused with `ErrCapabilityUnsupported` naming the provider and
    the operation before any request leaves Mercator. Provisioning, observing, and
    destroying are the floor of the contract and have no field to negotiate; stop,
    resume, and the owned listing do.
  - No Blueprint and no Lab invariant, and the reason is the Lab's shape rather than a
    gap. The Lab injects its simulated world directly as the orchestrator's `Adapter`
    and constructs no `Broker`, so no world can reach this seam and an invariant here
    would be one `TestEveryDefaultInvariantHasADeliberatelyFailingCase` refuses. It is
    held at L1 by `internal/broker` and `internal/capability`, each new rule shown
    failing with the production behaviour broken, and the Blueprints that exercise
    what it makes possible arrive with the provider that emits a capacity command.
  - The higher-fidelity half is two cases in `internal/daemon` against real SQLite,
    the real connection registry, the real HTTP API and the production wiring.
    `TestACapacityConnectionIsHeldByTheProductionControlPlane` authorizes a capacity
    connection over the API, reads `/v1/offers`, submits a Run, and holds the recorded
    Booking Decision to weighing no machine nothing can execute on while still naming
    the connection in `connections_queried`.
    `TestReconcilingAWorkspaceHoldingCapacityConvergesTheExecutionsThatLeaked` drives
    `ReconcileWorkspace`, which is what the one-minute reconcile loop calls, over a
    workspace holding a capacity connection and a one-shot connection that leaked an
    execution, and holds the sweep to reclaiming the execution.
- [x] 2026-07-28: A Rental is a domain aggregate with generations, and a generation
  ending retires its node. The lease Mercator provisions had no type at all:
  `node.StateRetired` was read in three places and written in none, `node.Store` had
  no `Retire`, and nothing in the tree could say which machine a lease was on or what
  it had been through. This is the vocabulary step the rest of phase 5 slice 3 is
  written in, and nothing provisions yet.
  - `domain.Rental` is the lease and `domain.RentalGeneration` is one lifecycle cycle
    of the machine under it. A lease is not a machine: capacity that stops and resumes
    comes back as a different machine with a different runtime on it, which is why a
    Node is bound to a generation rather than to the Rental. Generations are kept in
    order and never rewritten, so a lease says what it has been through rather than
    only what it is now.
  - An ending says which of three things happened, because they license different
    next steps. A stop suspends capacity that is still Mercator's and a later
    generation resumes it. A termination is capacity Mercator destroyed and a
    reclamation is capacity the provider took back, and both release the lease,
    because a lease over a machine that no longer exists is not capacity anything may
    be placed on.
  - The identity is minted before the provider is asked, so `Acquire` is a separate
    act: until the provider answers, the lease says what was asked for and cannot say
    what was got. Answering twice with the same machine changes nothing, which is what
    makes a retried provision safe, and answering with a second machine is refused
    rather than quietly replacing the first, which would leave the first billing with
    nothing able to name it.
  - `rental.Store` has a memory and a SQLite implementation held to one conformance
    suite in `internal/rental/rentaltest`, exactly as `node.Store` is. A write states
    the version it replaces, so two controllers ending one generation is a conflict
    rather than a last-writer-wins, and a lease Mercator could not have reached is
    refused before it is written.
  - Ending a generation retires the node bound to it, which is the first write of
    `node.StateRetired` in the tree. It is one act across two authorities, so it lives
    on `rental.Leases`: the lease records that the machine stopped being Mercator's,
    and only the registry can stop the runtime being offered and answered as capacity.
  - The runtime is retired before the lease is written, and the two failures are not
    symmetric. Retiring first and failing to write leaves a machine nothing offers
    under a generation the record still calls open, and the next attempt ends it,
    because retirement is idempotent. Writing first and failing to retire leaves a
    runtime publishing itself as capacity for a machine the record says Mercator gave
    up, and the Run that wins it starts by discovering there is nobody there.
  - Retirement being terminal for `dispatch` is not something this ordering decides.
    A successful end retires the runtime exactly as a failed write does, so after
    either one no further command reaches that identity. Anything Mercator means to
    stop on the machine is stopped before the generation's end is recorded, because
    the ending is the record of a decision already carried out and not the request to
    carry it out. The machine still gets its last word in either case, because
    reporting is the half retirement leaves open.
  - Retirement withdraws standing and withdraws nothing else. What it refuses is
    every door that would give the identity capacity or work again: `Heartbeat` set
    `StateReady` unconditionally in both stores, which is the one state the registry
    publishes as capacity, so an agent on a machine being torn down would have put
    itself back in the fleet with its next report and retirement would have been a
    no-op against any live agent. Retiring also ends the session the node is holding
    open, and `OpenSession` refuses the credential afterwards, so the agent's
    immediate reconnect is answered with `ErrRetired` rather than with a fresh
    session preloaded with every command the last one never acknowledged, and that
    refusal is also how such an agent learns it is retired. `dispatch` refuses a
    retired identity for the same reason from the other side: a node reference
    resolved before the generation ended would otherwise append a durable command
    that outlives the decision that issued it.
  - What retirement must never refuse is the machine reporting what it already did.
    A generation ends while a container is running every time a provider reclaims a
    machine, and the agent is alive inside the interruption window when that
    container exits. The node owns container lifecycle and exit codes and there is
    no second authority on them, so `RecordEvents` and `RecordResult` stay open to a
    retired identity: refusing them would leave a finished Run reading as unobserved
    forever and an operation the machine really applied stranded as pending, with
    dispatch refused so nothing could ever correct it. The authority model says
    application callbacks must never be the only way Mercator learns a process
    exited, and a retired node that could not report would make them exactly that.
    A retired node's heartbeat is therefore kept as the event it is and renews no
    lease, which is the whole of what the state costs it. The check lives at each
    door rather than in `authenticate`, because `authenticate` answers both kinds and
    a check there takes the second with the first.
  - Ending a generation names the generation. The decision and the write it lands
    are separated by a network, so an attempt whose answer was lost comes back to a
    lease that may already have stopped and resumed onto a fresh machine, and ending
    whichever generation is current then would retire a live runtime mid-Run on the
    authority of a decision about a machine that is already gone. Ending a generation
    the same way twice changes nothing and writes nothing, which is what makes that
    retry safe; ending it a second way is refused.
  - No Blueprint and no Lab invariant. For the lease itself that is not a gap: a
    Rental that nothing provisions decides nothing, so a Blueprint asserting on it
    would be a fixture about a struct, and an invariant over a store with no world
    behind it is one `TestEveryDefaultInvariantHasADeliberatelyFailingCase` refuses.
    The two target Blueprints stay red on purpose.
  - For the two retirement rules in the node registry it is a real gap, stated
    plainly rather than argued away. Delete the `StateRetired` check in
    `OpenSession` and the one in `dispatch` together and `go test ./internal/lab/...
    ./internal/scenario/...` is still green: only the hand-written cases in
    `internal/node` catch either, so promoting a scenario into the corpus proves
    nothing about retirement. The reason is that the Lab has no node registry in its
    loop at all. It imports nothing from `internal/node`, writes an enrolment as one
    `OperationNodeEnrolled` entry in the ledger, and models retirement in
    `simulatedWorld.retireCapacity` as the machine ceasing to exist, so there is no
    identity left to open a session, report an exit, or be dispatched to. Closing
    this needs a node command lane in the simulated world: a machine that outlives
    its generation, an agent that keeps reporting on it, and effects for the doors it
    comes to. That is a slice, not a check, and it is where the deliberate failing
    case for "retirement is terminal" belongs, and it is filed as #204. Until it
    exists, the rules are held only at L1 and this plan says so.
  - `safety.reusable_capacity_has_an_enrolled_runtime` was reviewed adversarially
    before anything was built on it, because the run that landed it died with both
    reviewers in flight. Three findings were fixed at the root and one was rejected
    with the evidence that refutes it; the corpus is still red without the world's
    enrolment, on the same 48 cases and the same three clauses.

- [x] 2026-07-28: Let a Run end without taking its machine with it. The cleanup
  disposition was derived from the offer's `Kind` alone, and `Kind` says only that
  the machine did not exist before Mercator asked for it. That is equally true of a
  one-shot execution product and of the fresh machine a reusable Rental is built
  on, so every provisioned placement recorded `terminate` and the reusable lane
  destroyed its own host the first time a Run finished on it. The next Run then
  allocated and booted a second machine, and an operator paid twice for the four
  minutes of getting ready that the lease exists to pay once.
  - The rule now reads both facts an offer states about itself.
    `domain.OfferSnapshot.CleanupDisposition` replaces
    `domain.DispositionForOfferKind`, and the only capacity a Run's own ending
    destroys is a provider-native one-shot product Mercator allocated: nothing
    survives it, so there is no host left to hand back. Everything else releases,
    including the fresh machine, because a lease decides when that machine goes and
    a workload finishing is not a lease ending.
  - The judgment call is that the rule reads both terms rather than collapsing to
    the lane. A slot in a pool Mercator does not own and a machine it holds a lease
    on release for two different reasons, and a standing one-shot offer is a real
    shape a provider can sell. Stating the kind keeps the reason an operator reads
    true of the capacity in front of them.
  - Ten green fixtures and the target moved with it, which is the corpus stating the
    change rather than being repaired around it. `enrolled-node-survives-its-first-run`
    now expects the first Run to release, which is the disposition half of that
    target and the half that had to land before the second Run could ever find a
    machine. Its own second-Run disposition is deliberately no longer evidence of
    reuse and the fixture says so: both lanes of that Run release now, so what
    carries the reuse claim is the selection reason, the candidate disposition, and
    the two stages costing nothing.
  - `a-machine-two-launches-disagree-about-is-not-adopted` needed its listing
    declared ephemeral. The whole fixture is two launches that recorded opposite
    dispositions, and after this change a provisioned reusable listing and a
    borrowed slot agree, so the disagreement it is named for could only be stated by
    the lane that really disagrees: a one-shot product against a standing host.
  - A defect the change surfaced at the seam. `offerFromDecision`, which rebuilds
    the offer a queued Booking was placed on when it is finally dispatched, stated
    no execution lane at all. `broker.Launch` dispatches on that lane, so a Run that
    queued on an enrolled node was launched down the ephemeral seam, looking for a
    provider connection under the node registry's own connection id. Only reusable
    capacity can carry a Booking, so the lane is now stated from the candidate's own
    disposition. Deleting it again fails one orchestrator case and nine Lab cases
    loudly, which is what an empty lane could never do before cleanup read it.
  - What this leaves standing, said plainly. A machine provisioned for a reusable
    Rental now outlives its Run and nothing yet ends the lease when the work stops:
    the Rental aggregate and its stores exist and have no production caller, so an
    idle provisioned machine is held until an operator intervenes. It is filed as
    #206, blocking for phase 5, rather than softened here. The path is not reachable end to end in
    production either, because `broker.launchOnNode` resolves the node from the
    selected offer's native ref and a marketplace listing's ref is not a node id, so
    a provisioned Run cannot launch at all until the launch is re-addressed to the
    machine that was built, which is #207. Both are named in the slice list below.

- [x] 2026-07-28: Fix what two reviewers refuted in the disposition entry above.
  Three findings held and two were rejected with the evidence that refutes them.
  The one that mattered is that the rule was made to read a fact the Blueprint was
  allowed to leave unsaid.
  - A Blueprint's `lane` is stated or the Blueprint is refused. It carried a silent
    default of `reusable`, and that default was inert only for as long as cleanup
    read the offer kind alone. The entry above made cleanup read the lane, so
    twenty-five fixtures were deciding whether a machine gets destroyed on a key
    none of them had written. ADR 0005 says of the production path that an offer
    stating no lane is infeasible with `UNKNOWN_FACT` and that there is no default,
    "because a silent default is exactly how the previous conflation survived"; the
    Blueprint contract now says the same thing, `MarketplaceOfferSpec.ExecutionLane`
    is deleted, and every listing in the corpus says what it sells.
  - Each of those twenty-five was written as `reusable`, which is a transcription
    rather than a decision: it is the lane they were already running under, it is
    what their expectations were computed from, and it is what they mean, because a
    listing whose candidate disposition is `provision_fresh_rental` is a fresh
    Rental by construction. What changed is that a reader can see it.
    `listing-that-states-no-lane` is the deliberate failing case, and it is a
    Blueprint refused at load rather than a rule refuted at L1, because a fixture
    that got past that door would be asserting the money decision instead of
    describing it.
  - `TestAReusableProvisionedRunReleasesItsWorkloadAndLeavesItsHost` swept the whole
    forty-five virtual minutes for a `capacity.terminate` and failed if it ever found
    one, which required the machine to still be billing ten minutes after its only
    Run ended. That is the #206 leak written down as a requirement, and the #206 fix
    would have had to delete the case to land. It is now bounded at the moment the
    Run's cleanup is confirmed, which is the only moment this case is about, and a
    lease that ends its own generation afterwards passes it.
  - `janitor.byRecordedDisposition` maps `release` to adoption, and the janitor's
    own comment said the opposite, so the comment is fixed. It was then rewritten
    into a second wrong claim, that an operator's sweep now keeps a provisioned
    machine it used to destroy, and the entry below has the corrected reading: no
    production sweep sees a machine at all, and adoption acts by calling
    `adapter.Release`, which every VM adapter implements as an instance delete.
  - Rejected, with the evidence. First, that no green fixture would catch a
    regression flipping a one-shot placement to `release`:
    `ephemeral-execution-is-never-a-rental` and `ephemeral-execution-holds-nothing`
    are both green, both sell a provisionable ephemeral listing, and both assert
    `terminate` on it, and `TestAOneShotExecutionStillTakesItsHostWithIt` reads the
    same answer out of the Effect Ledger in the green conformance world
    `an-owned-hour-is-charged-to-somebody`. Collapsing the rule that way was shown
    failing on all three in the evidence section. Second, that the sweep
    lost its coverage of a provisioned machine. The janitor reads the recorded
    disposition and cannot see a lane, so a leased machine and a borrowed slot are
    the same input to it, and a second janitor case stating a lane cannot be
    written. That much holds. The conclusion drawn from it, that nothing is left
    uncovered, does not, and the entry below names the axis that is: what an
    adoption leaves standing.
  - The plan's claim that the Lab suite carries a live half was false and is
    corrected where it was made. `internal/lab` holds no Docker and no object-store
    case; the live cases in this tree are `internal/nodeagent`'s and
    `internal/adapter/docker`'s, and this slice touched neither.

- [x] 2026-07-28: Fix what a fourth review refuted in the entry above. Both
  findings held, and both are one mistake told twice: the entry described an
  operator sweeping a provisioned machine, and no such sweep exists or can happen
  today. Nothing in `internal/janitor` changed except what it says about itself.
  - No production sweep sees a machine. `Backend.ListOwned` answers no workloads
    for a capacity connection by design, a reusable launch is placed on a node and
    a node is not a connection `Broker.ListOwned` fans out to, and
    `TestTodaysBackendsAreAllOneShot` pins docker, runpod, shadeform and vast to
    the ephemeral lane. Every `adapter.OwnedExternalObject` the janitor can reach
    was therefore launched by a one-shot executor, where `CleanupDisposition`
    answers standing with release and provisionable with terminate: the same
    kind-only mapping as before the disposition slice, value for value. The one
    branch that slice changed, provisionable and reusable, is reachable only from
    `internal/lab`'s simulated provider, and
    `TestASweepOfAWorkspaceHoldingCapacityConvergesTheWorkloadsItLeaked` is the
    case that keeps it that way. So the claim that a sweep now keeps a machine it
    used to destroy was operator-visible behaviour this system cannot produce in
    either direction. It is struck from the plan and from the janitor's comment,
    which now says what bounds the rule instead of what an operator would see.
  - Adoption is not the keeping its name promises, and the plan asserted it was as
    settled fact. `janitor.reclaim` carries an adoption by calling
    `adapter.Release`, and `shadeform`, `vast` and `runpod` implement `Release` as
    the identical `deleteOwned` their `Terminate` performs. Only
    `internal/adapter/docker` distinguishes them, by removing the container and
    leaving a host that was never Mercator's to destroy. `internal/adapter/fake`
    deletes on `Release` too, so
    `TestJanitorAdoptsCapacityItsOwnRecordSaysSurvives` reports the machine adopted
    and the object is gone from `ListOwned` afterwards, which is reproduced in the
    evidence below. This costs nothing today, because adoption fires only on a
    standing slot and giving a slot back is exactly what those adapters do. It
    costs a leased machine the first time a provider joins the reusable lane, which
    is the purpose of this branch, so it is filed as #208 and blocking that
    promotion rather than softened here.
  - The coverage the third review rejected is therefore genuinely missing, and not
    as a second janitor case. The uncovered axis is what `Release` leaves standing,
    and no adapter in the tree can express an adoption that keeps a machine, so the
    case has to arrive with the fix rather than before it.
  - The judgment call is that no guard was added to `internal/janitor`. A rule that
    cannot be handed the wrong input cannot be defended by refusing it, and the
    distinction such a guard would need, a leased machine against a borrowed slot,
    is in nothing the sweep reads. Widening the sweep to machines is #199, and the
    action seam is answered there or in #208 rather than by a check that no caller
    can currently reach.

- [x] 2026-07-28: Make a bootstrapped machine keep its session. An agent whose
  session credential lapsed re-enrolled by replaying the invitation it joined
  with, which the store had already spent and the signer refuses on its own, so a
  real node stopped being able to authenticate about thirty minutes after
  bootstrapping while its containers went on running. No test noticed, because
  every one of them finished inside the window.
  - The registry gained `RenewSession`, over `POST
    /v1/node-agent/{node}/session/renew`, authenticated by the credential being
    replaced. It writes nothing: a session credential is signed rather than
    stored, the fencing token does not move, and no invitation is spent, which is
    what makes renewing a different act from enrolling rather than a gentler one.
    A retired machine renews nothing, and that is the whole of what bounds a
    leaked bearer credential.
  - The agent asks for a credential at each use rather than holding one for the
    life of a connection, and renews when less than two heartbeat intervals of
    its life remain. Two heartbeats is enough for one failed attempt to be retried
    before anything it sends could outlive the credential carrying it, and it
    scales with the cadence the agent was configured for instead of a constant
    that would be wasteful or too late depending on it.
  - A defect this uncovered: the registry told a node a session expiry its
    credential did not have. A token carries whole seconds, so an expiry with a
    fraction in it was truncated at signing and the node renewed against the
    moment it was told rather than the moment the credential died. `Signer.Expiry`
    is now what every window is computed through. It is invisible at thirty
    minutes and it is exactly what made the first conformance run fail.
  - Enrollment's two doors are told apart. `ErrEnrollmentSpent` is the store's
    record that this invitation was redeemed; `ErrEnrollmentInvalid` is the
    signer refusing material that is not this node's or whose window has closed.
    Inside the window only the first fires, so collapsing them would leave a
    replayed bootstrap accepted for the rest of its window. The HTTP answer stays
    opaque on purpose.
  - Evidence. `a-machine-keeps-working-past-its-first-session` is a green
    conformance Blueprint whose Run outlives one session lifetime, and the ledger
    holds one `node.enrolled` and a `node.session_renewed` recorded while the work
    was still running. At L2, a real `nodeagent.Agent` against the daemon's own
    server with a two second session renews twice on the wire, then places and
    completes a Run, and redeemed exactly one invitation. Removing the renewal
    from the agent turns that into a loop of `ENROLLMENT_REFUSED`, which is
    `ErrEnrollmentSpent` behind the deliberately opaque answer.
  - Found and not fixed here: `Reinvite` has no operator surface. The only caller
    is the orchestrator's own provisioning path, so an operator whose machine lost
    its state has no supported way to reinvite that identity. Filed as #211 and
    recorded in docs/production/node-agent.md rather than papered over.

- [x] 2026-07-29: Rent a real machine and bootstrap an agent onto it. Shadeform
  implements `CapacityProvider` and is the first backend in the reusable lane. Its
  create carries a script launch configuration that installs the pinned node agent
  and starts it under systemd, so the machine enrols itself outbound and Mercator
  opens nothing on it.
  - The promotion deleted the one-shot half. `capability.Declare` refuses a backend
    that both provides capacity and executes one-shot work, because one lane is
    stamped on every offer a connection publishes, so `Launch`, `Observe`,
    `Release`, `Terminate`, `ListOwned`, `EphemeralSupport` and the docker launch
    configuration are gone rather than kept beside the new contract.
  - `CapacitySupport` states only what the four endpoints do: no stop, no resume,
    no disk that survives one, provision idempotency by client-side tag
    reconciliation because create honours no operation key, owned capacity
    listable, and a destroyed instance observable while it is deleting.
  - The agent source is required connection configuration with no default, because
    the release archives ship no node agent binary and a guessed URL is a paid
    machine fetching a 404. Shipping it, so the key can have a default that is a
    pin, is #234.
  - The manifest now declares every key `New` reads. `base_url` and `os` were read
    and undeclared, which the conformance validator rejects while production
    accepts the same connection silently.
  - Blocked and not faked: no live Shadeform run. This host holds no credential and
    no `op`, which is #235. Full detail, evidence, and the six deliberate breaks
    that proved each rule can fail are in the verification section below.

- [x] 2026-07-29: What two reviewers refuted about the slice above, repaired. Six
  findings were real and are fixed at the root; one is real and is its own slice.
  - The slice landed with no Blueprint and no Lab invariant, and the Lab could not
    have held it: its only `CapacityProvider` deduplicated by Rental
    unconditionally, so a Blueprint declaring `idempotent_provision: "none"` still
    compiled into a world with server-side idempotency. The world can now lose a
    provision answer, and
    `a-lost-provision-answer-costs-one-repeat-and-not-the-machine` drives that
    world through the real orchestrator: one machine, adopted under the lease, its
    agent enrolled on it, the workload launched.
  - The defect that Blueprint exists for. `Reinvite` minted a fresh token every
    time and the store wrote it over the old one, and `Enroll` matches the token
    the record names exactly. `allocateCapacity` asks for the bootstrap before
    every provision, because it cannot know whether an earlier attempt landed a
    machine until the provider answers, so a create whose answer was lost and
    which the next attempt adopts hands the Run a paid machine holding material
    the control plane invalidated a moment earlier. It enrols nowhere and the Run
    burns its whole enrolment patience. An invitation still outstanding is now
    handed back, rebuilt from the record's own digest.
    `safety.a_machine_holds_material_the_control_plane_will_still_accept` is the
    rule, and it fails through the real orchestrator when the Lab's registry is
    put back the way it was.
  - `agent_download_url` required `{version}` because a URL naming no version is
    not a pin, and the value substituted into it was the literal `"dev"` in every
    production deployment: `node.Registry` defaulted to it and nothing ever set
    `daemon.Config.AgentVersion`. The default is gone rather than corrected, and
    `MERCATOR_AGENT_VERSION` is where an operator states which build their URL
    serves. A deployment that states none provisions nothing.
  - `agent_download_url` is the one bootstrap value an operator writes, and it
    lands inside a single-quoted word in the `curl` line. The bar on unattended
    values permitted the single quote, so a URL carrying one closed that word and
    ran the rest as root on the rented machine. Proved on the build host against a
    real shell in `busybox:1.37` before the fix, refused after it.
  - Four paths read the same account listing for the same lease and disagreed
    about ownership. The reconciler verified only the machine it kept and then
    destroyed every other instance wearing the Rental tag, which the teardown
    beside it refuses to touch on exactly that mismatch; and the observation had
    been narrowed to the machine it was about to report, on the strength of a
    second generation this provider cannot have, which hid a second machine the
    account was being billed for. Both now ask about every match.
  - `capability.ErrCapacityIndeterminate` is a provision whose outcome nobody
    knows, which was prose in Shadeform and a bare error everywhere else. It is
    typed now, because callers act on the difference: a failure allocated nothing
    and can be asked again, and asking this again is how one lost answer becomes
    two machines.
  - Not fixed here and filed as #236: a provision the provider classified as fatal
    is retried for ever. `allocateCapacity` unwraps no `*adapter.ProviderFailure`,
    records no classified failure, and `EnrolmentDeadlineAt` cannot bound it
    because it is only consulted after a provision succeeds. That is a new failure
    path with its own policy decisions and its own Blueprint to write, rather than
    a defect in code that already exists.

## Phase status

| Phase | What it delivers | Status |
| --- | --- | --- |
| 1 | Contract split under simulation | done |
| 2 | Node protocol and Go agent | done for hand-enrolled nodes; provisioned capacity does not bootstrap an agent yet |
| 3 | Exact OCI and artifact locality; prefetch | done for capacity Mercator already holds, and unreachable in production for Artifacts until an object-store client exists: image inventory, execution-driven warming, registry manifest resolution, and exact node-side reporting done at L1 and against a real daemon; Artifacts are a domain concept with the object store as their authority, admission gates on it, and Placement prices what each candidate would still have to read out of it, which the Run's stated objective now ranks candidates on; mutable caches are attached, enumerated, compared per generation, and isolated per workspace end to end; disk is a resource an enrolled node measures with a kernel call, an offer states what is left of, and a Run's reservation and its whole content are admitted against together; prefetching is a controller that gets a queued Run's host ready, bounded so it never competes with work already admitted there and withdrawn when the Run that wanted it goes away, and an enrolled node replicates an Artifact from a control-plane-minted read; producer affinity was built and withdrawn, because no shipped node can be in the state its discount fired in; a production object-store client remains, and so does the attachment that would let a workload read the verified copy its host holds, which is what makes the zero-second read a specification rather than a saving |
| 4 | Candidate prediction, service classes, owned economics, replanning | ServiceClass replaces PlacementObjective outright and carries the exchange rates the score is computed over, so the start, completion, and uncertainty terms fire for the first time and the decision records the weights it was scored at; a decision states the risk history it was taken under; a launch is eight stages rather than four quantities, each predicted on its own, each spent by both simulated worlds, and each recorded in the Run Bundle beside its own actual, with application readiness a typed report the workload owns; a transfer is priced from the bytes that are missing and the throughput of the specific path they cross, which an enrolled node measures on its own reads and publishes, and the decision records the rate it divided by and who stands behind it; a Booking Decision is appended and never rewritten, so a re-decision names the answer it replaces and why, a Run that Placement weighed the fleet for and placed nowhere records the decision that placed it nowhere, and the API and console read the chain rather than its last entry; a Run is held to the bounds its caller and its class declared, so a machine costing more than the caller allowed and a machine that came free after the moment the class states are both refused rather than started, and a Blueprint can state a budget for the first time; waiting is a phase that ends, so a Run kept waiting longer than its class allows is refused rather than held and the class that declares no deadline stops waiting for the first time, and aging lifting a batch Run past an hour of interactive arrivals is a claim the corpus makes rather than one the policy implies; a run group is a bound admission holds rather than a word the arrival plan wrote, so a family of eight declared three wide runs three at a time on four idle machines and the members waiting say so in the record, and a wait is charged to whoever caused it, so the queue delay is asked of the part Mercator caused and the deadline of the whole of it, with the division summed over intervals and recorded beside the bound; a class that forbids interruption is refused capacity its provider may take back while a world that takes one back interrupts only the work whose class permitted it; a machine's price is the terms it was sold on rather than one rate, so rent already committed to is charged to the Run that spends those seconds, rent beyond the commitment is bought in the increment its publisher sells with the unused tail of that increment charged to the placement that bought it, a setup fee is asked only of capacity Mercator has to acquire, and an operator states what their machine is bought in, who they hold it for, and when it stops being Mercator's; capacity Mercator does not recognise is adopted or terminated by a stated policy the record names, decided by the launch that took the capacity rather than by the Run's last one, and content a machine refused is asked for again rather than answered out of the record of the pull that failed; every stage is answered by a hierarchical estimator that declares which rung answered and records p50, p90, sample count and confidence beside the actual, keyed on identity that recurs rather than on offer IDs that do not; done, with soft and hard affinity, stopped-state storage, preemption-risk pricing, a production publisher for reclaimable capacity, and a live marketplace trial of key recurrence left to their own issues |
| 5 | One true VM provider with agent bootstrap and conformance | in progress; the corpus has the words for capacity and the Effect Ledger has the operations, and the capacity contract is reachable from the control plane for the first time: a connection can sell capacity without selling one-shot execution and declares the reusable lane for doing so, the machine lifecycle is five calls the control plane can make with a command the provider's negotiated set does not promise refused at the seam, and a workspace holding such a connection reconciles instead of failing every sweep, and no machine accumulates an image, a cache, an Artifact copy, or a second Booking unless an enrolment for that machine is in the record, which is the safety net the acquisition path lands under. Its listings are not placement candidates yet, because a machine no agent has enrolled on can execute nothing and acquiring one needs the Rental lifecycle and agent bootstrap in #200. A Rental is now a domain aggregate with generations, held in a memory and a SQLite store under one conformance suite, and ending a generation retires the runtime bound to it, which is the first write of `node.StateRetired` in the tree and the first thing that stops a machine Mercator gave up being published as capacity. A Run that ends on a machine provisioned to hold a Rental now releases its workload and leaves the host standing, because the cleanup disposition reads the execution lane as well as the offer kind: only a one-shot product Mercator allocated is destroyed by the end of its own Run. A bootstrapped machine now keeps its session for as long as it works: the registry renews a credential over the node protocol, the agent renews ahead of each lapse, and the invitation it joined with is redeemed exactly once and appears in no event, ledger entry, or Run Bundle. Before it, a real node stopped being able to authenticate about thirty minutes after bootstrapping while its containers went on running, and no test could see it. A machine now states what it has to state to be placed on at all: the agent reports the cards under it and the driver over them, an offer carries both, a workload declares the accelerator stack its image was built against, and Placement refuses a host that said no with `CAPABILITY_MISMATCH` and a host that said nothing with `UNKNOWN_FACT` rather than installing a stack onto somebody's host or finding out at launch. Before it, every enrolled GPU machine advertised zero accelerators and was struck out of every GPU placement it was perfect for. Shadeform is now a CapacityProvider and the first backend in the reusable lane: it rents a VM, hands it a script launch configuration that installs the pinned node agent and starts it under systemd, states a negotiated set that promises no stop, no resume and no persistent disk, and reconciles a repeated provision by scanning the account for the lease's own tag because the API honours no operation key. Its one-shot half went with the promotion, because one connection cannot sell both. No live provider run has happened: this host holds no Shadeform credential, so the whole path is proven under the package's httptest fake and the live half is blocked and filed rather than claimed. The launch is still not addressed to the machine a provisioning built, a capacity connection still publishes no placement candidate, and nothing yet ends the lease of a machine nobody is using |
| 6 | Telemetry waterfall, calibration, explanation UI, counterfactuals | not started |

## Scenario and invariant coverage

Phase 1 added:

- `ephemeral-execution-is-never-a-rental` (green): a one-shot product is the
  cheapest and fastest candidate and still records `launch_ephemeral`, because
  nothing survives the workload's exit.
- `enrolled-node-survives-its-first-run` (target, missing `node_bootstrap` and
  `execution_warms_capacity`): capacity provisioned for the first Run is still
  there when the second arrives, and the second reuses it rather than provisioning
  again. It carried `rental_schedule` as a third pending reason until phase 5
  slice 1, and that debt is paid: the Rental Schedule store, its versioning, and
  its reservation are wired end to end and four green fixtures exercise them, so
  what the second step waits on is the provisioned-to-enrolled transition twice
  over. Its first step no longer waits on anything: the first Run releases its
  workload rather than terminating its host, which is the disposition half of this
  target and the half that had to be true before there could be a machine for the
  second Run to find at all.
- `safety.ephemeral_capacity_not_reused` (Lab invariant): no Run is ever queued
  behind one-shot capacity, and capacity held for a one-shot execution never
  accumulates a second Booking.
- `safety.a_rental_identity_is_capacity_mercator_holds` (Lab invariant): an offer
  carrying a Rental identity is capacity Mercator holds, standing and in the
  reusable lane. A Rental Schedule is keyed by Rental identity, so a template for a
  machine that does not exist yet publishing one gives Placement somewhere to put a
  Booking and the next Run somewhere to wait, behind a machine nothing will free.
  Added in the second review round of phase 5 slice 2.
- `safety.reusable_capacity_has_an_enrolled_runtime` (Lab invariant): no machine
  accumulates an image, a cache, an Artifact copy, or a second Booking unless an
  enrolment for that machine is in the ledger. It is the counterpart to
  `safety.ephemeral_capacity_not_reused` from the other side: that rule holds that
  a one-shot product accumulates nothing, this holds that the capacity which does
  accumulate is capacity Mercator can reach, because all four are the agent's own
  work. Added in phase 5 slice 3, together with the Lab world recording
  `node.enrolled` for the Rentals it holds, which is the first writer that operation
  has ever had.
- `a-machine-keeps-working-past-its-first-session` (conformance, green): one
  provisioned machine, an agent that enrols four minutes in, and a forty five
  minute Run, so the credential the agent joined with lapses while the container
  is still running. The ledger holds one `node.enrolled` and a
  `node.session_renewed` recorded while the work was still going. Nothing in the
  corpus reached that state before, because every fixture finished inside one
  session lifetime. Added in phase 5 slice 4.
- `safety.bootstrap_credential_is_short_lived_and_single_use` (Lab invariant):
  one machine holds a given enrollment token, it is redeemed once, and it appears
  nowhere in Mercator's own record. Each clause is read off World Truth, because
  the world is the only thing that knows what it handed each machine. A double
  redemption is deliberately not producible through the Lab's own registry, which
  refuses it exactly as production does, and that refusal is asserted so the
  clause is not left checking a state nothing can reach. Added in phase 5 slice 4.
- `safety.secrets_absent` (Lab invariant, strengthened in phase 5 slices 4 and 5):
  it matched field names, so material in a field called credential, password, or
  secret was caught and the same material in a field called enrollment_token was
  not, which is the field a bootstrap would honestly be filed under. It now also
  refuses a signed URL wherever it appears, because a presigned read is a bearer
  credential written as a location, and any credential the world minted whatever
  field it is filed under. Slice 5 extended that last clause from bootstrap
  tokens to the material a machine was handed for one fetch, which the name
  clause and the signed-URL markers between them do not reach: a registry
  password filed under a name nobody thought of passed both.
- `safety.content_credentials_are_scoped_and_expiring` (Lab invariant): every
  credential Mercator hands a machine so it can fetch content names one
  operation, one workspace, one piece of content and an expiry; is checked
  against the command it arrived on rather than against itself; has not lapsed
  when it is handed over; and states a window no longer than an hour. The four
  clauses fail separately on four hand-written worlds, and the world can really
  produce the state they are about: a private image asked for with nothing minted
  is refused by the registry, the same image with material minted for that
  operation is served, and the account handed over with no scope at all is
  recorded and caught. Added in phase 5 slice 5.

  Read what that last clause says and not more. It reads the bound Mercator
  attached, never whether the far side can enforce it. Against an object store it
  is both, because a presigned read is a signature over one object and one
  window and the store refuses anything else on its own. Against a registry that
  only knows how to check a password it is neither: the material handed over is
  the operator's standing account verbatim, `credential.Mint` performs no token
  exchange for any registry today, and the registry never sees a
  `ContentCredentialScope`. What holds the machine to the scope there is
  `authorisedPull` on the node, which is Mercator's own code on a host an
  operator rents by the hour, so an attacker holding that host has the account
  and it keeps working for every private image in that registry. Narrowing that
  needs a registry token exchange, which is its own slice and is not built. The
  Lab models the split rather than papering over it: the machine's check and the
  far side's check are separate refusals in `internal/lab/content_credential.go`,
  because a world where the registry honoured Mercator's scope is a world that
  proves the control plane agrees with itself.

  The clause that is deliberately not there is material uniqueness, which was the
  obvious fourth and fails on correct behaviour. SigV4 presigns one object for
  one expiry, so two operations minted for one version in one instant produce the
  same URL; a registry that only knows how to check a password has one password
  behind every image it serves. Neither is a defect. The material simply cannot
  always be narrower than the far side is able to enforce, and what narrows it to
  the operation is the scope, which the node checks before presenting anything.
  What can always hold is that the window is bounded, so a read good for a day is
  caught while a generous window for a large image over a slow link is not. The
  bound is the Lab's own hour rather than `credential.DefaultMintWindow`, because
  a rule that read the number production mints with would agree with production
  by construction and could never fail.
- `a-private-pull-uses-a-credential-that-expires` (conformance, green): one
  machine, an occupant holding it for forty minutes, and a queued Run that needs
  an image at a registry serving nothing to an anonymous reader and a corpus in
  an object store answering only a signed read. Both fetches carry material
  minted for that one operation and expiring inside the execution. The occupant's
  own image is public and is fetched with nothing at all, which is the other half
  of the claim: minting for content any anonymous reader can have would put an
  account on a machine that never needed one. `ImageSpec.private` is the schema
  this needed, and it is a fact about a registry rather than about the control
  plane: a world where every image is anonymous can never catch a Mercator that
  mints nothing, because nothing on any machine would notice. Added in phase 5
  slice 5.
- `safety.enrolment_names_the_generation_it_was_invited_for` (Lab invariant): an
  agent that opened a session on a machine a provider allocated enrolled under the
  generation that machine was invited for. A generation is what fences a lease, so
  a session filed under another one is a node Mercator would address every later
  act about, the fencing token included, to a machine that does not exist. A lease
  with no provision behind it is exempt, because standing capacity a world seeded
  has no invitation to be right or wrong about. Added in the review round of phase
  5 slice 4, and made able to fail in the round after it: the enrolment is recorded
  under the generation the agent's own bootstrap names, so the rule compares two
  facts of Mercator's making rather than one field with itself. A control plane that
  provisions under one generation and mints the token under another fails 15 Lab
  tests mid-drive; before, it was fully green.
- `safety.a_machine_holds_material_the_control_plane_will_still_accept` (Lab
  invariant): an invitation that reached a machine is not replaced before that
  machine redeems it. A rented host is handed its invitation once, in the bootstrap
  written when it is created, and Mercator opens no connection to it afterwards, so
  an invitation superseded while the host still has to enrol is that host locked
  out of the fleet it was paid for. It presents material the registry no longer
  names, is refused, and has nothing else to try, and nothing about the machine
  looks wrong from the provider's side. A credential nobody has redeemed yet is not
  the violation, because that is every machine still booting; the violation is one
  that reached a machine, was never redeemed, and has been superseded. Added in the
  review round of phase 5 slice 7, with
  `a-lost-provision-answer-costs-one-repeat-and-not-the-machine` as the world it
  fires in: put the Lab's registry back to minting a fresh token on every repeat
  and the rule reports the invitation mid-drive.
- `bad-host-facts-rejected-loudly` (green as of phase 5 slice 6): four listings
  for one training image that declares the accelerator stack it was built
  against. The cheapest states outright that it has no working NVIDIA driver and
  is refused `CAPABILITY_MISMATCH facts.nvidia_driver`; one states a driver four
  years older than the image needs and is refused
  `CAPABILITY_MISMATCH host.driver_version`; one is silent about the SSH access
  the Run's operator will not do without and is refused
  `UNKNOWN_FACT facts.ssh`; the well-attested host wins at twice the cheapest
  price. It is the first time the Blueprint's facts map reaches an offer field,
  and the fourth listing is new, because a map of booleans alone cannot state a
  driver that is present and too old. It waited on `host_facts` since phase 1.
- `safety.host_supports_the_image_it_was_given` (Lab invariant): no launch in the
  world's ledger landed on a machine whose published facts cannot carry the
  accelerator stack Mercator's own event log says the workload declared, and no
  Booking Decision called such a machine feasible. The second clause is there
  because a candidate ranked feasible is one busy machine away from being that
  launch. Three sources meet in it and none is checking itself: what the machine
  said is the World Tape's, what the workload declared is Mercator's public log,
  and whether a launch happened is the world's ledger. Its deliberate failing
  world is a host stating a CUDA 12 driver accepting a launch whose image
  declares CUDA 13. Added in phase 5 slice 6.

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
  The Rental that enumerated itself, holds none of the image, and published what
  its link to the registry delivers is struck out on the fetch half of its five
  minutes, which is 433.71 p90 seconds over a link it measured and over the bound
  on its own. It publishes the same number Mercator would have assumed about it,
  so the seconds are the seconds either way and what the fixture turns on is that
  a machine said them. The borrowed host beside it is not struck out, because
  nothing has established that it is slow. Making the bound locality-blind fails
  it with `no feasible offers`, which is the Run finding no capacity at all on
  machines that may already hold every byte. The decision records
  `START_SLO_UNVERIFIED` for that placement rather than `WITHIN_START_SLO`,
  which the scheduler used to append whenever a bound existed at all: admitting
  a candidate because nobody could describe it is not the same as promising it
  will start in time, and the fixture fails if the two are conflated.
  The second Run states the other half of the same rule, and is why the first
  says fetch rather than five minutes. The Rental's remaining 108.24 p90 seconds
  are assembly, priced over `AssumedUnpackMbps`, and `UnpackRate` stamps that as
  an assumption because nothing in the fleet measures a host's storage. No bound
  may act on it, so at seven and a half minutes the Rental is admitted
  `START_SLO_UNVERIFIED` with its whole prediction at 542.95 seconds and its
  established start at 434.71. That is every machine in the fleet until a
  measured launch answers the stage, which `stagePredictor` puts into both halves
  when one exists. Two breaks fail it: stating the unpack rate as a measurement,
  and making `establishedOverAMeasuredPath` return what it was given, each of
  which strikes the Rental out on a constant of Mercator's own.
- `borrowed-warmth-is-invisible` (conformance): a machine Mercator has not
  enrolled holding the whole image before the Run arrives. World Truth says it
  holds it, the offer carries no inventory, and the Run is priced the whole
  fetch. Publishing what such a machine holds fails it with `pull source
  "image_inventory" for a machine nothing of Mercator's runs on`.
- `artifact-must-be-durable-before-a-consumer-runs` (conformance): five claims
  about what makes an Artifact consumable and what a local copy is worth, driven
  through the real orchestrator, event log, and Run projection. A producer writes
  its 10GB checkpoint on the host it ran on and the object store takes it 160
  seconds later, and Mercator holds its consumer unplaced across that whole gap
  while carrying it in the projection the entire time. That consumer then lands on
  the very machine the checkpoint was written on and reads the object store
  anyway, priced the full 160 seconds, because nothing of Mercator's fetched,
  hashed, or filed what a workload wrote inside its own container. Which machine
  that is comes out of the ledger rather than out of the fixture: the case reads
  the producing host off the `artifact.written` effect and requires the consumer's
  selected offer to be that same host, because an assertion naming
  `producer-rental` would hold just as well in a world where the producer ran
  somewhere else. A later Run consumes an Artifact whose only copy sat on a Rental
  whose idle lease has since elapsed, and runs anyway from the object store. That
  same Run reads a second
  Artifact whose copy is sitting on the host it landed on and fetches it anyway,
  because nobody ever checked those bytes against the catalog. The Run behind it
  reads what that fetch left and something checked, and is priced no read at all,
  which is the one thing a local copy buys. Driven twice at cadences ten minutes
  apart, the two executions agree on when the checkpoint was written and when it
  became durable, because those are facts about the world rather than about the
  observer.
- `safety.artifact_replica_verified` (Lab invariant): no copy exists of content
  the catalog cannot name, no copy claims a digest that version does not have,
  every copy traces back to the object store, and no Run reads a copy nothing
  checked. "Traces back to the object store" is exactly the version being durable,
  with no second shape: only a fetch Mercator issued leaves a copy, because a copy
  is worth what checking it against the catalog says it is worth. Content a
  workload wrote for itself is `artifact.written` in the ledger and never a copy in
  an inventory, and restoring the world that filed one fails the rule with `offer
  "producer-rental" holds a copy of Artifact "artifact:checkpoint:v1", which
  nothing published`.
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
- `a-restored-snapshot-is-not-a-copy` (conformance): the execution half of that
  third Rental, on the one machine in the world, so it is the machine the Run
  goes to. The decision prices the whole 40GB read for a checked copy of another
  version filed under this one's name, and the Run then reads all 40GB out of the
  object store rather than the bytes the machine happened to be holding. The
  restored snapshot is still there afterwards: a workload reads its inputs into
  its own container, so nothing repaired this machine and the next Run sent here
  owes the same 640 seconds. A placement corpus cannot reach this: the Booking
  Decision was right either way, and only an execution can say which bytes the
  workload was handed.
- `the-service-class-decides-what-wins` (green, replacing
  `the-objective-decides-what-wins`): one world, two Runs, and the only difference
  between them is the class of work each says it is. The warm Rental is a second
  from ready at 4 USD an hour, the cold one fifteen minutes at 2. The batch Run
  takes the cold machine, because batch work values a second of waiting at a fifth
  of the machine's own rent and the price gap is larger than that. The interactive
  Run takes the warm one, pays double, and the decision records
  `SERVICE_CLASS_INTERACTIVE` and the weights it was scored at. Both candidates
  pin their score in dollars, so the exchange rate is asserted rather than
  inferred from which machine won: zeroing the class's weights places the hurried
  Run on the cold machine.
- `uncertainty-is-priced-once` (green): three machines at one price for one 18.04GB
  image. One enumerated itself and holds the image, so it owes nothing and is
  certain of that. One is a machine Mercator has not enrolled that World Truth says
  is sitting on the whole image: it is charged the whole fetch and half a point of
  doubt, which is what a duration over an unmeasured link is worth, and that half
  point is the whole uncertainty term for it. One holds the image and publishes a
  capacity claim it is 70 percent sure of, which is three tenths of a point and the
  only thing separating it from the first. Restoring either extra point the
  reference model used to add, for an unenumerated inventory or for unknown
  pricing, fails it on the recorded uncertainty and on the score.
- `dataset-gravity-worth-waiting` (green): the same gravity behind a running
  Booking. The Rental holding the dataset is busy for eight more minutes and the
  Rental with the perfect image cache is idle, and the Run queues for the dataset,
  because 640 seconds of object store dwarfs eight minutes of waiting.
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
  borrows a slot on, sixty gigabytes of disk with forty already spent on a checked
  copy of the version before the one it reads, and one Run needing a ten gigabyte
  image and the forty gigabyte dataset the catalog names. It says nothing about
  what it holds, so Placement cannot establish the shortfall and selects it, which
  is the only way a launch can still arrive somewhere it does not fit. The machine
  refuses the work rather than taking it and filling up partway through, and
  deleting the refusal fails the execution through
  `safety.disk_reservation_respected`. The copy is what makes it the disk half of
  what a copy is worth as well: counting another version's bytes as this version's
  leaves the work fitting, and the read that follows fails
  `safety.artifact_replica_verified`.
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
- `a-start-is-a-moment-somebody-observed` (green): one Run of opportunistic work,
  a Rental holding the whole image a second from ready at 20 USD an hour, and a
  machine that does not exist yet at a dollar an hour whose provider publishes
  five minutes of provisioning that the world really spends: thirty seconds
  acquiring it, four minutes booting it, thirty seconds enrolling the runtime.
  Waiting is free to this class so it takes the cheap machine, and the fixture
  states both moments. At the decision nothing has observed a container and the
  record says so; ten minutes later the recorded start is 588.64 seconds after the
  launch was accepted. The provisionable candidate's `provision_seconds` of 300
  sits beside it, so the published claim and the spent actual are both written
  down and are different numbers. Shortening the world's boot to zero collapses
  the two moments and fails it at 348.64; deriving the record from the accepted
  launch fails it at zero.
- `a-node-reports-when-the-container-really-started` (conformance): the same claim
  through the real orchestrator, event log, and Run projection, and the only place
  the Run Bundle can be read. Two predicted-versus-actual records for one Run
  rather than one, the start actual sourced `run_stream.execution_started`, the
  predicted value equal to what the winning candidate's own decision recorded, and
  the two differing: a calibration set whose columns came from one piece of code
  teaches nothing. It carries ten records per Run now, one per launch stage beside
  the two aggregates.
- `a-launch-is-eight-stages` (green): the waterfall. A machine that does not exist
  yet publishes ten minutes of provisioning and this world spends them over three
  stages, then moves 500MB of image, applies it, and asks a container runtime for a
  process; the Rental beside it holds the image assembled and idle, so its only
  remaining stages are the container starting and the application coming up, which
  is what lets the fixture say which stages belong to the machine and which to the
  workload. Eleven minutes in the container has been running for a moment and the
  application has said nothing; four minutes later it reports ready, three minutes
  after its own process began, against the two it declared. Returning nothing from
  any of the world's three new spends fails it, each on its own number.
- `every-stage-of-a-launch-has-an-actual` (conformance): the same waterfall at L1,
  and the only place the per-stage record can be read. Every one of the eight rows
  has a prediction sourced from the Booking Decision and an actual sourced from the
  Effect Ledger, boot is predicted at what the provider published against an actual
  the world spent, acquisition is predicted at nothing because nobody publishes one,
  and readiness is predicted at what the workload declared against an actual only the
  workload could state.
- `safety.prediction_is_recorded_against_its_actual` (Lab invariant): for every
  launch the Effect Ledger accepted, the record carries a prediction and an actual
  for each stage the world simulated, and names no stage it cannot carry. The two
  halves are read from independent places, so the rule cannot be satisfied by the
  predictor agreeing with itself. It is deliberately not accuracy: that is a
  calibration metric, and a rule of that shape would fail on a fixture whose world is
  simply slow.
- `a-clock-nobody-shares-is-not-a-start` (green): one Run, one Rental holding the
  image assembled and idle, and a host whose wall clock runs an hour ahead. Its
  runtime hands back a process twenty seconds after the launch is taken and then
  states that moment on the clock it has, an hour in Mercator's future, beside its own
  read of now on the same clock. The record says the stage is unobserved, which is the
  only honest answer: nothing observed this container start on a clock Mercator
  shares. It is the first fixture in the corpus whose machine does not keep the
  control plane's clock.
- `a-clock-nobody-shares-measures-nothing` (conformance): the same world at L1,
  through the real orchestrator, event log, Rental Schedule, and Run Bundle. The
  start-latency row reads `start_not_observed` rather than filing an hour as a
  measurement, and every standing law still passes, which is the second half of the
  claim: refusing a moment is not a violation of the rule that a start be observed.
  Publishing the world's own truth instead of the machine's reading fails it at 20
  seconds.
- `safety.start_is_observed_not_inferred` (Lab invariant): no adjudicated Run
  carries a start moment Mercator derived, and no Rental Schedule measures a
  Booking's runtime from one. Every moment the run stream records is one an
  observation of that Run reported, no moment is later than the look that carried it,
  a moment a holder did report is never dropped, and a Booking's clock is either a
  start an observation established or the read that carried one. The clauses are
  stated in the rule's own terms rather than delegated to the production predicate
  they exist to constrain, and they read independent halves of the record, so none can
  be satisfied by Mercator agreeing with itself. A Run with no start moment at all is
  not a violation: acquisition and boot have no production observation until an agent
  bootstraps on provisioned capacity, and what the record must then say is that the
  stage is unobserved rather than that it took no time.
- `safety.score_is_reproducible_from_the_record` (Lab invariant): for every
  candidate of every Booking Decision Mercator recorded, ScoreUSD is the arithmetic
  over the terms that decision itself carries, at the weights it says it used. What
  it forbids is a scoring term whose input is nowhere in the record: such a term
  moves placements a reader with the whole decision in front of them cannot
  explain, and no rule can police a number nobody wrote down. That is not
  hypothetical, and it is why the rule arrives with the class rather than after it:
  two definitions of uncertainty ran side by side for a phase, one counting the
  confidences a candidate's answers carried and the other counting facts read
  straight off the offer, and they agreed on every decision only because both were
  multiplied by zero.
- `safety.doubt_only_the_answers_the_score_reads` (Lab invariant): every answer a
  candidate recorded a confidence for is one `domain.ScoredAnswers` names, which is
  the capacity claim and the eight stages of a launch. What it forbids is doubt
  about a question the score reads no answer to. Such a term can only run one way,
  because a stated confidence is charged and a silence is not: it penalises the
  publisher that measured its machine, leaves alone the publisher that said
  nothing, and leaves alone the publisher certain its machine refuses every start,
  so the machine nobody measured outranks both. A published reliability history sat
  in that list for a phase, and the arithmetic rule above could not see it, because
  both models charged the same doubt and the score reproduced from the record
  exactly.
- `a-class-mercator-does-not-know-is-refused` (conformance): one idle Rental
  holding the image, and one Run that says it is urgent. There is no urgent class,
  so the Run has stated no exchange rate at all, and Mercator refuses it where it
  enters rather than ranking every candidate on price alone. The world would have
  placed it, which is what makes the refusal a choice rather than a consequence,
  and no event is written for a Run turned away at the door.
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
  wanting `artifact:corpus:v7`. The control plane restarts the moment that third
  Run is recorded, and the second speculative fetch still waits until five minutes
  after the first one started. Every part of the fixture exists to be failable:
  the wanted names prefix-collide, so a control plane comparing a new desire
  against the joined text of the last one reads `v7` as content it has already
  asked for and skips a bound it applies to additions only; the gap is longer than
  the cadence, so the harness cannot produce it; the third Run arrives between two
  ticks, so the moment the bound is tested is not a moment the driver chose; and
  the restart means a clock living only in the process cannot satisfy it. Deleting
  the rate bound, restoring the substring comparison it replaced, or keeping the
  clock in process each fails it through `safety.prewarm_rate_within_bound`.
- `prewarming-bounds-the-whole-fleet` (conformance): two tenants, two machines,
  and both bounds. Each machine is occupied and holds what its own tenant runs, so
  each queued Run wants twenty gigabytes on its own host. The first tenant's Run
  arrives at five minutes and is prepared for; the second tenant's arrives ninety
  seconds later and gets nothing, and its transfer starts once the first has
  landed. Restoring per-workspace bookkeeping fails it through
  `safety.prewarm_rate_within_bound` with `speculative preparation started at
  2030-01-01T00:05:00Z and again 1m30s later at 2030-01-01T00:06:30Z`.
- `prewarming-spends-one-budget-across-tenants` (conformance): the same two
  tenants in a world that states a depth bound and no interval, both Runs arriving
  in the same minute. One slot exists, the Run that starts soonest gets it, and the
  other tenant waits for it to land. Truncating the desire per workspace fails it
  through `safety.prewarm_yields_to_real_work` with `2 speculative fetches were in
  flight at 2030-01-01T00:05:00Z, and this world allows 1`.
- `a-refused-prepare-can-be-asked-again` (conformance): one machine already holding
  the image, one Run occupying it, and one queued Run that reads a twenty gigabyte
  corpus. The machine turns the fetch away, and a minute later, which is the
  soonest the rate bound allows, Mercator asks the same machine for the same
  corpus again. The bytes land on the second ask. A world that remembered a refused
  fetch as work it had taken on answers the second ask Duplicate and moves nothing;
  a control plane that remembered refused content as content it had asked for
  computes an unchanged desire and never asks twice.
- `a-refusal-on-one-machine-is-not-a-withdrawal-on-another` (conformance): two busy
  machines with a queued Run behind each, both reading the same hundred gigabyte
  corpus. One machine is asked first and starts reading; the other joins the same
  desired set five minutes later and turns its fetch away. Then both Runs are
  withdrawn, and the transfer still running has to be stopped. A control plane that
  hears a refusal as being about content rather than about one machine's copy of it
  forgets what the other machine took on, computes an empty desire it believes it
  never departed from, and sends no withdrawal at all.
- `a-restart-still-withdraws-what-nobody-waits-for` (conformance): one busy machine
  and two queued Runs reading the same hundred gigabyte corpus, which is asked for
  once. Both callers withdraw at the same moment and Mercator restarts on the first
  of them, so the restarted control plane's first reconciliation is the one that
  wants nothing, and nothing is also what it would want if it had never asked for
  anything. A control plane that cannot tell those apart sends no withdrawal and
  the replica lands nineteen virtual minutes after the last Run that wanted it went
  away.
- `an-orphan-is-adopted-or-destroyed-by-policy` (conformance): four machines and
  two Runs. Three of them are holding something the control plane never launched,
  and the control plane restarts into that state. The one carrying a Run identity
  Mercator can account for is adopted, its slot released and its machine kept. The
  one carrying a Run whose start the provider refused until its attempts ran out is
  adopted too, because its launch recorded the same release even though nobody ever
  asked for its capacity back, and reading the cleanup request first destroys that
  machine. The one carrying nothing anybody can account for is terminated and its
  machine stops existing. The fleet afterwards is the claim, because an adoption
  that quietly destroyed the machine and a termination that quietly kept it read
  the same in a count of things reclaimed.
- `a-machine-two-launches-disagree-about-is-not-adopted` (conformance): one Run
  launched twice, first on a machine Mercator provisions for it and whose provider
  refuses the start, then on a slot Mercator borrows, so its record holds terminate
  and release at once. A third machine holds capacity carrying that Run and neither
  of its launch identities. It is destroyed, because no recorded launch accounts for
  it. Deciding it by the Run's last launch adopts it on `recorded_disposition_release`
  and leaves it standing and billing, which is what the corpus could not say before:
  every other world holding an orphan holds a Run with exactly one launch, so the
  rule that reads the launch that took the capacity and the rule that reads the last
  one agreed everywhere.
- `safety.orphan_policy_is_explicit` (Lab invariant): capacity a world began
  holding that Mercator never launched is either still standing or converged by a
  decision the record holds, and every such decision names a policy, a reason, and
  one of the two outcomes an operator can act on. It reads what a world began with
  rather than the fleet as it stands, because the interesting case is the machine
  that is no longer here. It stands beside `liveness.orphan_convergence` rather
  than in place of it: that rule reads executions Mercator launched against the
  Runs it projects, which is the fact this one says nothing about.
- `safety.prewarm_rate_within_bound` (Lab invariant): no two moments at which
  Mercator began preparing are closer together than the world's `min_interval`,
  whichever tenant wanted the content and whether or not the control plane
  restarted in between. It is stated over the moments preparation started rather
  than over transfers, because one desired set crosses the boundary at once and may
  open as many transfers as the depth bound allows: how many may move together is
  the other rule's question. A world stating no interval states no opinion.
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

Phase 4 added:

- `busy-rental-worth-waiting` (green): the warm Rental's Booking is expected to
  finish in four minutes and fresh capacity pays five of boot plus an 18GB pull,
  so the Run takes a queued Booking behind it. Dropping the seeded schedule out
  of the world fails it with the Run placed on `fresh-4090` as a running Booking
  on a Rental identity that did not exist a moment earlier.
- `multiple-runs-schedule-in-order` (green): two Runs and a Rental one minute
  from free, against capacity that takes thirty to provision. Both queue, in
  submission order, and the second's Booking follows the first's rather than the
  running one, which is the claim: a schedule is ordered, and Mercator reads its
  own last answer before giving the next.
- `queued-run-makes-fresh-capacity-win` (green): the same world with the price
  gap removed, so the queue is the only thing that can decide it. The first Run
  queues behind five minutes; the second reads a schedule that now holds the
  first Run's twenty minutes as well, and twenty-five minutes of waiting loses to
  ten of provisioning and pulling. A queue Mercator itself created is what makes
  the second answer differ from the first.
- `full-schedule-forces-fresh-capacity` (green): the cap rather than the score.
  The warm Rental holds the maximum four waiting Bookings and is refused with
  QUEUE_CAPACITY_EXCEEDED, and CAPACITY_UNAVAILABLE beside it, because with no
  open position there is nothing left to make a busy machine available. Its
  schedule is at version nine holding five Bookings, which is what makes the
  seeded version load-bearing: a version counts transitions rather than
  occupants, and deriving it from the Bookings still there fails the fixture with
  version five.
- `dataset-gravity-worth-waiting` (green): gravity behind a running Booking. The
  Rental holding a checked copy of the 40GB dataset is busy for eight more
  minutes and the Rental with the perfect image cache is idle, and the Run queues
  for the dataset, because 640 seconds of object store dwarfs eight minutes of
  waiting.
- `queued-booking-deadline-expiry` (target, missing `schedule_advancement`): the
  Run takes a queued Booking with a latest start six minutes out, and the Booking
  ahead of it runs for thirty. At seven minutes nothing expires it. Its first two
  steps are green, which is what makes the one declaration it keeps precise. That
  diagnosis was a step short until the overrun rule landed: at seven minutes the
  Booking ahead is a minute past the runtime Mercator enforces, so a re-decision
  read a wait of nothing and would have chosen the same machine, and expiry alone
  would have produced a placement, an expiry, and the same placement again.
- `a-queue-drains-as-it-runs` (conformance) gained the record beside the seconds.
  The Booking ahead is twenty-nine minutes into a declared half hour, so this is
  the one execution in the tree where what a Booking has left and what its caller
  asked for are different numbers, and the recorded evidence has to say the
  first: restating the declared runtimes fails it with "the record says the
  Booking ahead has 1800.00s left".
- `an-overrun-booking-is-not-an-empty-queue` (green): the Rental's Booking declared
  forty-five minutes, fifty have passed, and the machine is occupied for another
  forty. Both remaining runtimes bottom out at zero, which is what an idle Rental
  reports, so the Rental was a feasible candidate priced at no waiting at all and
  the Run took a Booking whose latest acceptable start was the moment it was minted.
  It is refused on the machine's own capacity evidence now, and the record carries
  the overrun beside the two remainders that ran out. Reverting the queueing rule
  fails it through `RentalSchedule.Reserve` refusing to promise a start behind a
  Booking it cannot project, rather than through the assertion, which is the two
  layers saying the same thing.
- An enrolled node publishes the room it has. Every node offer stated
  `capacity.available` true at full confidence for as long as its lease held,
  including a machine that had just reported the container it was running, and
  `feasibilityViolations` only asks `queueable()` of an offer that says it is
  occupied. So on the only lane that reaches production every queueing rule was
  unreachable, including the exhausted clause above: a node mid workload was scored
  idle this instant and won on price and warmth, and the refusal the fixture states
  could not happen. Worse than a mispriced wait, a machine holding a container past
  its Booking's bound was selected and `RentalSchedule.Reserve` then refused the
  reservation, so `stepPlace` returned an error before appending anything: `502
  REFRESH_RUN_FAILED`, no Booking Decision, no fallback to the idle machine beside
  it, and the same answer on every reconcile forever. The offer now states how many
  workloads the machine says it is executing against how many it runs at once, which
  is the node's own authority over container lifecycle. An orphan therefore blocks
  placement rather than being placed behind, and a Booking Mercator still holds for
  a container that has exited blocks nothing.
- `TestANodeStillRunningPastItsBoundIsNotQueuedBehind` (`internal/daemon`) is the
  same rule where it is not a declaration. Nothing terminates a workload at its
  enforced maximum, so a container that does not exit holds its node with the
  Booking's remaining runtime at zero, and this drives exactly that through the
  public API, the production storage, and the node protocol. The fleet has two
  machines, and the assertions are the sentence the rule is: the busy machine is
  refused on its own capacity evidence, the record carries the overrun beside the
  remainder that ran out, and the Run is placed on the expensive cold machine
  instead. An earlier version asserted `502` and that nothing was launched, which
  every orchestrator error satisfies identically, so it held neither layer: it was
  green with the Placement rule reverted, through the internal failure above.
  Reverting either half of Placement fails it now. The domain refusal is held by
  `TestAScheduleWhoseBookingIsPastItsBoundPromisesNothing`, and with Placement
  correct it changes no answer here, which the commit message that claimed both were
  required overstated.
- The runtime Mercator enforces is measured against the container it bounds. A
  Booking recorded taking its Rental at the moment its decision was evaluated, and
  both runtimes it declares bound a container, so provisioning and image pull were
  spent against a runtime nothing was enforcing yet: a Run declaring twenty minutes
  that waits fifteen for an 18GB fetch had its Booking read as past its own bound
  one minute into the workload. That refused its machine to every arriving Run,
  blocked anything from queueing behind it for the rest of the Run, and published an
  overrun in the auditable record as a measured fact about a machine running
  normally. `ScheduledBooking.StartedAt` is the moment the machine that owns the
  container's lifecycle said it was running, recorded by the orchestrator on the
  first observation whose phase is running and committed with that observation. A
  Booking that has not launched owes its whole declared runtime, which is what the
  zero start already meant for a queued one; it understates occupancy while a
  machine is fetching, and nothing here bounds preparation, so the wait projected for
  a Booking that has not started is a floor rather than a promise. Manufacturing an
  overrun out of that gap was the worse of the two answers.
  `TestABookingStillStartingIsNotPastItsBound` is the case the old clock could not
  pass. The corpus seeds a running Booking through the same transition, because a
  fixture saying a Rental is running work is saying its workload is running.
- There is no Lab Blueprint for the overrun, and the reason recorded before was a
  non sequitur about the code as it then stood. The rule does not need a workload to
  outlive its declared runtime, only a Booking to outlive its bound, and with the
  bound charged from the placement decision a cold Rental with a long pull stated
  exactly that: an arrival declaring twenty minutes against a validly modelled ten
  would have been exhausted while its container was still running. That Blueprint
  would have been a fixture asserting an overrun for a machine running normally,
  which is the defect above rather than the rule. With the clock on the container,
  blueprint validation refusing a runtime model beyond `max_runtime` is the whole
  reason: no Lab world can state a container that outlives what its Run declared,
  and the only world this rule is about is a container that does not exit. Lifting
  that validation would state a world with nothing to enforce it, which belongs with
  the enforcement work.
- `safety.promised_start_is_still_ahead` (Lab invariant): no Booking Mercator
  commits promises a start no later than the moment the decision that minted it
  was evaluated. It is stated over the decision record, because a promise can only
  be judged against the moment it was made, and it says nothing about a deadline
  that passes afterwards: waiting longer than promised is what expiry exists to
  answer. Its deliberate failure is a decision whose queued Booking carries a
  latest start equal to its own evaluation time.
- `warm-idle-rental-wins` and `no-rentals-provisions-fresh` (green) gained
  `no_rental_schedule`, which is what turns "a Rental nothing is assigned to
  records nothing" from an unbreakable guard into a rule. Deleting the guard
  publishes a schedule at version zero with a wait of nothing for every candidate,
  including machines that do not exist yet and have no Rental at all, and both
  fixtures now say so.
- A seeded schedule states a version at least as large as the number of Bookings it
  holds, checked in the Blueprint validator and again in the store. A version
  counts transitions and each Booking took one, so a lower version is a history
  Mercator cannot have had, and the arriving Run's Booking would be minted at a
  version one of the seeded Bookings already consumed.

- `a-disowned-fact-is-not-an-answer` (green): three Rentals at one price with the
  same 2GB image to fetch. One publishes 750 Mbps to the registry and states nine
  tenths of a point of confidence in it, one publishes 5 Gbps and states zero, one
  publishes nothing. The disowned publisher and the silent one are identical, which
  is the whole claim: a number nobody stands behind buys its publisher exactly what
  saying nothing buys. Honouring the disowned fact again flips the placement onto
  it with "pull_seconds: want at least 32.4, got 3.7"; dropping the harness's path
  facts fails it from the other side, charging the measured machine the assumption
  it published past.
- `a-machine-nobody-priced-is-a-last-resort` (green): two Rentals holding the same
  image, one quoted at 2.00 USD an hour and one nobody quoted, and a Run that
  allows unknown pricing. The winner has the higher score, which is the point:
  dollars order the candidates that have dollars, and an unpriced candidate is
  ranked behind every priced one rather than being the cheapest thing in the fleet.
  Dropping the priced-first rule places the Run on the unquoted machine. The
  decision also records `PRICED_BEFORE_UNPRICED`, because the winner has the higher
  score and nothing else in the record says why.
- `a-floor-refuses-a-measurement-and-not-a-silence` (green): the same rule in the
  half that decides feasibility. A Run states a 500 Mbps floor and allows a link
  nobody measured, against four Rentals at one price. The machine that published
  750 Mbps and stands behind it wins, the machine that published 100 and stands
  behind it is refused with the number it published, and the disowned publisher
  sits in every column beside the machine that published nothing. Restoring the
  empty-list test refuses the disowned publisher while the silent machine is
  selected.
- `a-link-nobody-measured-is-not-a-slow-link` (conformance): the same world at L1,
  and the execution the Lab's own path confidence is falsifiable through. Stamping
  certainty on a fixture's measurement flips the placement onto the machine that
  disowned 5 Gbps; dropping the world's paths flips it there too, because then
  nothing separates the four machines.
- `an-unquoted-machine-is-the-last-resort` (conformance): an unpriced Rental at L1,
  and the execution the Lab's own unpriced offer is falsifiable through. Publishing
  the default priced offer for it places the Run on the machine nobody quoted at a
  rate of zero.
- `a-machine-that-fails-to-start-says-so` (green): three Rentals at one price and one
  warmth. Two stand behind a reliability history their provider measured, one having
  refused two starts in five and been interrupted a quarter of the time and the other
  never having done either, and nobody has ever measured the third. The decision
  records each published history, prices none of them, and doubts none of them, so all
  three score to the same dollar and the placement falls through to the offer ID, which
  is how the machine known to refuse starts takes the Run. That is the honest state of
  this model, written where a fixture will fail when somebody closes the gap: what a
  refusal is worth is a probability times a predicted start, and a flat penalty
  invented for it now would be the unmeasured constant this plan keeps deleting.
  Deleting the L0 world's reliability projection fails it on both rates. Charging the
  published confidence as doubt again, which is what this fixture was first written
  against, fails it on the doubt, the score, and the winner, because the machine nobody
  measured becomes 0.003 USD cheaper than the two that were.
- `a-refusal-to-start-is-recorded-and-not-priced` (conformance): the same world at
  L1, and the execution the Lab world's own reliability projection is falsifiable
  through. It asserts the record, the absence of any doubt about it, and deliberately
  not the placement.

- `a-candidate-is-what-recurs` (green): what a launch history may be filed under,
  stated as the candidates a fleet has to tell apart and put together. Two asks a
  marketplace numbered differently for one product in one place key as one candidate,
  a third ask there whose cards hold half the memory keys as another, a fourth whose
  probe reported its eight cards as two entries of four keys as the same product as
  the ask that reported them whole, a fifth holding four of that product keys as
  another, a sixth that the same provider sells as a one-shot execution rather than as
  a machine keys as another again, an enrolled machine keys as the machine its backend
  named and never as the lease or the listing this fixture named it by, and a one-shot
  pool publishing nothing that outlives its listing has no key at all. The fixture
  states each whole key, so a world that stopped publishing a region fails it with the
  region gone rather than silently dropping a rung of the ladder, and it states the
  enrolled machine's content key as well as its machine key, because what a pull costs
  is a property of the content and what a boot costs is not.
- `registry-silence-has-a-name` (green): also states, since the second review, that
  neither Run has a content key at all. A registry that will not answer leaves
  Mercator unable to name what it is about to run, and a content key naming no content
  filed a 900MB image and a 40GB one under one name per machine.
- `a-candidate-recurs-through-the-control-plane` (conformance): the same candidates at
  L1, on the keys the real orchestrator recorded in its Booking Decision. It exists
  because the placement corpus and the Lab are two different simulated worlds, and a
  key that agreed in only one of them would be a key about the harness.
- `safety.candidate_identity_recurs` (Lab invariant): no two capacities the world says
  are different share one candidate key, two Runs that asked one machine for different
  content do not share one content key, a key names the machine its backend published
  and never the lease or the listing that search found, and capacity the world
  publishes nothing recurring about has no key. It is stated against World Truth
  rather than against the derivation: the collision counts accelerators where the key
  groups them, the content clause reads the image out of Mercator's own workload
  record, and the honesty clauses read the offer. Each clause has a case that fails
  it, one at a time, in `TestEveryClauseOfTheCandidateIdentityRuleCanFail`, and the
  registry's permanent deliberate failure drives the collision. Through the whole
  control plane on the conformance Blueprint: restoring the inventory bug where two
  entries naming one product were deduplicated fails it with an eight-card machine and
  a four-card machine under one name, dropping the lane fails it with a rental and a
  one-shot execution of one product under one name, naming the machine from the Rental
  fails it on every Blueprint in the corpus, and letting any provider recur fails it
  on the one-shot pool.
- `history-answers-for-the-machine-it-was-measured-on` (green): six listings of five
  marketplace machines, two of which this fleet has measured, and one of the measured
  machines published twice under two ask IDs. A third Run asks all six what they will
  spend on the stage this fleet has actuals for, and the fixture asserts every answer
  by level, by sample count, and by seconds: both listings of the measured machine at
  the exact candidate with its own thirty seconds, the second measured machine with
  its own hundred and fifty, an unmeasured machine in the same region at the two
  samples of that provider and place, an unmeasured machine of that provider elsewhere
  at the provider, and a machine of a provider nobody has measured at the prior, which
  is what the Run declared about itself. It is the first fixture that states the
  machine behind a marketplace listing and the readiness of one machine rather than of
  the whole world, and the second of those is what makes the levels answer different
  seconds. A fourth Run asks the same six about a second image nothing in this world has
  ever launched, and every rung has to be silent about it: readiness is the workload's
  own semantics, so the measured machine, its neighbour in that region, and its provider
  elsewhere all fall to what that Run declared about itself. A world holding one image
  is what left the three answers above unfalsifiable against a ladder that carried the
  content at its narrowest rung alone.
- `history-answers-through-the-control-plane` (conformance): the same claim at L1,
  where both halves of the answer are Mercator reading Mercator. The identity is the
  one its own Booking Decision recorded and the seconds are the difference between two
  moments its own Run stream adopted, one stated by the machine holding the container
  and one stated by the application inside it. All three keyed rungs answer here: the
  machine that ran the first Run at the exact candidate, an unmeasured machine in its
  region at the provider and place, and an unmeasured machine in another region at the
  provider, for the same seconds at less confidence. A third Run then asks the same
  three machines about a second image, and all three fall to its own declaration, so
  each coarse rung is in this world twice: once where it answers and once where it has
  to be silent. What the region here holds is the half of its production path that runs
  from the offer to the recorded identity; that a backend states one at all is held in
  the adapters, in the lane where they do.
- `safety.prediction_states_its_provenance` (Lab invariant): every stage of every
  recorded candidate names the level its answer came from and how many measured
  launches stand behind it; a keyed level names a key and a positive count, a prior
  names neither, and the key an answer was read under is the key this candidate has at
  the level that answered. Two clauses are load-bearing. A key that names the listing
  the offer arrived under is refused, because a marketplace mints an ask ID per search
  and a history filed under it reports a key that cannot grow as candidate-specific
  evidence. And a coarse rung answering a stage that is a property of the content under
  a key naming no content is refused, because a rung generalizes over machines and
  never over what those machines were asked to run. Each clause fails on the one record
  it exists to catch in `TestEveryClauseOfThePredictionProvenanceRuleCanFail` and
  `TestACoarseRungAnsweringContentItDoesNotNameIsAViolation`, the counterpart holds
  that an answered stage and a prior are both honest provenance, and the registry's
  permanent deliberate failure drives the listing clause. Through the whole control
  plane on the conformance Blueprint: keying the history on the offer snapshot ID, on
  both the writing and the reading side, fails it with the key naming the listing, and
  building the coarse keys without the image fails it on the artifact conformance
  fixtures with a readiness answered at `provider` for content that key does not name.

- `a-fast-machine-far-from-the-data-loses` (green): three Rentals equally warm on the
  image at one price, one on a measured 4 Gbps path to the object store, one on 200
  Mbps, and one that has never measured that path at all, and one Run that reads a
  40GB dataset. The slow machine publishes the faster path to the registry, which buys
  it nothing, because it holds the image already and there is nothing to fetch over
  that link. The unmeasured machine is the fallback half of the same claim: it is
  priced rather than refused, charged the stated prior, capped at what a duration over
  an unmeasured rate is worth, and its record names `assumed_object_store_rate` rather
  than presenting 500 Mbps as something a machine reported. It is the first Blueprint
  to declare a path to an object store, and under one constant per scope it is red
  three ways: the two measured candidates price the read at 640 seconds against the 80
  and 1600 stated, both record the assumption where a measurement is asserted, and the
  placement lands on `rental-far-from-the-data` because nothing separates the three
  and the tie broke on the offer ID.
- `a-start-bound-refuses-only-what-it-can-prove` (green): one Run that refuses to wait
  a quarter of an hour and two machines holding the whole image that both have to read
  the same 40GB. The one that measured its path at 200 Mbps is struck out, because a
  Run gets to refuse a machine that is known to be late. The one nobody has measured is
  taken, priced at the prior over the byte count and recorded `START_SLO_UNVERIFIED`,
  because a duration over a rate nothing on that machine answered for is a guess and
  refusing capacity on a guess is silence about a path becoming infeasibility.
  Restoring the assumed seconds to the established start fails it with `no feasible
  offers`, which is the Run finding no capacity at all on two machines that may both be
  a minute from ready. It is a second world beside
  `a-fast-machine-far-from-the-data-loses` rather than a bound added to it: no single
  bound can leave a measured slow path feasible for the ranking claim and refuse it for
  this one.
- `a-path-somebody-measured-prices-the-read` (conformance): the same world at L1,
  where the bytes really move. The decision prices each read off the machine's own
  published path, and the world then spends eighty seconds reading forty gigabytes
  onto the near one. Both halves come from the one declaration, so the prediction and
  the actual agree for a reason rather than by construction; dropping the world's
  reading of paths leaves it spending 640 seconds whatever the fixture declared.
- `a-path-a-host-disowned-is-still-the-path` (conformance): the one world where the
  prediction and the actual cannot be the same number. A single Rental reads a 40GB
  dataset over a path this world crosses at 200 Mbps and states no confidence in, so
  Mercator prices the read from its own assumption at 640 seconds and the world spends
  1600. It is what holds the two halves apart in the Lab world: with that world reading
  published facts instead of its own declaration, the read costs 640 and the actual is
  derived from Mercator's own input. `a-world-crosses-the-path-its-host-disowned` is
  the same claim about the fake adapter.
- `a-floor-on-reading-the-data-is-a-floor-on-delivery` (green): the two ways a
  candidate can miss a Run's floor on reading its dataset, which no fixture could
  state because every download floor in the tree was over a registry. Three Rentals
  hold one image at one price. The machine that delivers four gigabits is refused a
  floor of eight and the record states the four it published; the machine that
  delivers ten and last measured a week ago is refused as unmeasured, because this Run
  acts on nothing older than ten minutes; the third is taken. Dropping either rule
  fails it on that machine alone. It is the fixture that says an object store p10 is
  delivery and a floor may know it, and the one that makes a node's dating of its own
  measurements reachable from the corpus.
- `a-world-crosses-the-path-its-host-disowned` (green): the fake adapter's transfer
  model, held apart from Mercator's own input. One Rental holding nothing, an 18GB
  image, and a four gigabit registry path the machine states no confidence in, so the
  pull is priced from the fleet assumption at 288 seconds and this world spends the
  thirty-six the path costs. The start it asserts is the world's moment: deriving the
  world's rates from the published facts leaves the same Run starting 288 seconds in,
  which is what running it that way reports. Before it, that substitution left the
  whole suite green.
- `safety.transfer_rate_is_attributed` (Lab invariant): every transfer a Booking
  Decision recorded names either the measurement or the assumption it was priced
  from, and never both, and a rate the record presents as measured is a number some
  host or path fact reported at that scope, one its publisher still stands behind. A
  disowned or expired fact is silence for every other reader here and may not become
  a measurement by being divided by. A transfer priced from an assumption is worth at
  most `domain.AssumedLinkConfidence`, asked of the rate and of the stage estimate it
  produced, which is the clause that keeps the first two from being bookkeeping: an
  honestly named assumption charged no doubt ranks an unmeasured machine exactly where
  a fabricated measurement would. It is the rate half of
  `safety.locality_provenance`, which explains the bytes: seconds are the product of
  the two, and either one can be invented.
- `a-changed-decision-names-the-one-it-replaces` (green): a Rental whose schedule
  already holds a running Booking and the maximum four waiting refuses the only Run in
  the world, which is recorded as a decision rather than thrown away. Six minutes
  later the running Booking finishes, a position opens, the Run is placed, and the
  answer that replaces the refusal names it by ID and gives
  `PREVIOUS_DECISION_SELECTED_NOTHING`. Both survive in the record. The re-decision is
  caused by the fleet changing rather than by a machine refusing a launch, because a
  refused launch is a fault and a placement fixture has none; the launch-failure half
  is held at L1 by `TestAReplacementNamesTheDecisionItReplaces` over
  `a-published-rate-is-not-what-a-machine-does`, where the fleet does not move and the
  machine refuses the start.
- `safety.decisions_are_never_rewritten` (Lab invariant): one decision ID means one
  decision, and every answer after the first names the record immediately before it
  and gives a reason. Two records under one ID that disagree are a decision edited
  after the fact, and every account built on that ID, the predictions filed against it
  and the audit of why a Run went where it did, is then an account of something that
  never happened. The predecessor is checked as the immediate one rather than as any
  earlier record, because a chain that skips a link is one a reader cannot walk.
- `safety.a_silence_is_not_an_answer_about_capacity` (Lab invariant): every recorded
  wait recounted off the decision it was read off, so a fleet that says nothing it
  published can ever hold this Run has to have weighed a machine that said so. A node
  whose disk probe failed reports no room and has not said it is full. It recounts
  rather than trusting the answer beside the reason, for the reason
  `silenceWasTakenBackOut` recomputes what a candidate was charged: a scheduler that
  miscounts its own evidence agrees with itself perfectly, and only a reading taken
  from the record's other half catches it.
- `safety.decision_is_reproducible` (Lab invariant): re-deriving a decision's ID from
  the content the record carries yields the ID the record carries. It is what makes
  the rule above enforceable rather than defeatable by editing a decision and its ID
  together, which is a chain of consistent-looking records assembled after the fact.
  It reads every decision and not only the newest, because a superseded decision is
  the part of the chain nobody is looking at any more, which is exactly where an edit
  would go.
- `an-ask-nothing-matches-holds-no-queue` (green): a marketplace publishing a 200GB
  machine and answering a 900GB ask with nothing at all, which is the strongest thing
  a fleet can say and was the one wait nothing exempted. The patient ask waits, twelve
  minutes of waiting promote it past the class that arrives next, and the Run that
  arrives fits what this fleet sells and is placed. That last part is the case:
  recording the ask's wait as one for capacity to come free is what let one submission
  for a shape nobody sells hold a whole workspace for a day while the machine behind
  it went unsold. Both simulated worlds answer the shape they were asked about for
  this reason, because a world that returned its whole inventory whatever anybody
  asked could state a fleet nobody can use and never a fleet that answers one ask with
  nothing while answering another with a machine, which is the shape production
  reaches through a shape-filtered search rather than through an empty fleet.
- `a-machine-that-could-not-look-is-not-a-machine-with-no-room` (green): one machine,
  holding the image, whose disk probe failed. Placement refuses it, because landing
  content on a disk nobody measured is a launch nobody can promise, and the refusal
  names the silence rather than a shortfall: the fleet's answer counts it as a machine
  that said too little to tell. Every Run carries a disk floor, so reading that as a
  full disk made every Run in the workspace an ask no capacity can ever hold and took
  the ordering away from all of them at once. The second Run is the assertion. It
  arrives later, it is worth less than the Run already waiting, and it is told it waits
  behind it.
- `a-fleet-that-changed-is-recorded-again` (green): two machines too small for the ask
  and one of them on an idle lease that runs out. The wait does not change, and a Run
  in that wait has an empty list of work ahead of it by construction, so suppression
  stated over the reason and that list threw away the decision naming the fleet as it
  now is. It is stated over the fleet's own verdict now, the machines weighed and what
  each was refused for, which is what every law about Placement reads.

- `a-bound-on-cost-outranks-the-class-that-would-pay` (green): the world
  `the-service-class-decides-what-wins` states, with a bound on what the Run may
  cost. Interactive work prices a second of waiting at twenty times the machine's
  own rent, so its class buys the warm machine on the five minutes of pulling the
  cold one owes, and twenty minutes there is 1.33 USD against the one dollar its
  caller allowed. The warm machine is refused `COST_LIMIT_EXCEEDED` and the Run
  runs on the cheap slow one. It is the first fixture in this corpus that states a
  budget at all: the refusal was enforced in production and reachable by no
  Blueprint, so everything the corpus could say about money was which machine won
  on price. Removing the bound from the scheduler places the Run on the machine its
  class prefers and fails it three ways over.
- `a-machine-that-came-free-too-late-is-not-a-start` (green): one busy machine and
  an interactive Run that will not wait a minute to start, so the only candidate is
  struck out as too slow and nothing projects when the wait ends. The machine comes
  free at ten minutes and a quarter, the Run is asked again half a minute later,
  and its class says it must have started within ten minutes of being told to wait.
  It is refused `QUEUE_DELAY_EXCEEDED`, which is the earlier of the two bounds that
  wait broke. Asking the deadline only of a Run being told to wait places it instead,
  which is what the fixture fails on.
- `a-start-nobody-can-reach-is-refused-at-the-door` (green): one machine with every
  Booking position taken and an interactive Run arriving into it with no wait behind
  it. Both its bounds are still ahead of it and the record already says the machine
  comes free in twenty five minutes, so the answer has stopped being worth producing
  before anything was broken, and admission refuses it `DEADLINE_UNREACHABLE` at its
  first pass. It is the only fixture that pins that reason, and the only branch that
  can still produce it: `Admission.BoundAlreadyBroken` names the deadline only for a
  wait that has already reached it, and every class states a queue delay shorter than
  its own deadline, so a projected miss is the whole of it. Deleting the projected
  miss from `deferOrRefuse` leaves the Run waiting on `NO_FEASIBLE_OFFER`, which is
  what the fixture fails on, and left the rest of the tree green before it existed.
- `a-cost-bound-refuses-the-machine-the-class-would-buy` (conformance): the same
  world under the real control plane, and the execution
  `safety.class_bounds_honoured` is falsifiable through. It asserts the refused
  machine scored better than the selected one, because a world where the cheap
  machine also wins on score says nothing about a limit.
- `safety.class_bounds_honoured` (Lab invariant): no Run was placed on a machine
  costing more than its caller allowed, on a machine nobody quoted under a bound on
  dollars, or past the moment its own class says the answer stops being worth
  having, measured from the deferral that started its wait. Both bounds are read
  off the record rather than off the scheduler's arithmetic, and they are one law
  because they are one failure: a class is a declaration Mercator scores every
  candidate on, so it can always be talked into a costlier or a later machine, and
  the bounds say how far. The maximum queue delay is deliberately not restated in
  it, because that promise is what `liveness.aging_prevents_starvation` is stated
  over and two laws over one bound let a repair satisfy one of them and be
  believed. The deadline half is exercised at L0 and in the rule's own clause tests
  and nowhere at L1, and it cannot be reached by driving a Run to its deadline at all:
  every class's maximum queue delay is shorter than its deadline, so a wait that
  reaches the later bound broke the earlier one first and
  `Admission.BoundAlreadyBroken` names the earlier one at both doors. What this law
  catches is a placement past the moment, which is a decision rather than a refusal,
  and its own deliberate failure is a Run placed 15000 seconds into a wait its class
  bounds at 14400 with a failed launch in the middle of it.
- `a-class-with-no-deadline-still-stops-waiting` (green): one machine with 200GB and
  an opportunistic Run asking for 900GB. Two hours and a minute later it is refused
  `QUEUE_DELAY_EXCEEDED`, which is the only bound that can end this wait: its class
  states that its value does not expire, so it declares no deadline, and before the
  maximum queue delay was a refusal this Run waited for ever.
- `a-queue-delay-bound-is-refused-loudly` (conformance): the same claim at L1, and the
  execution the starvation law's exemption is falsifiable through. Deleting the
  queue-delay branch from `deferOrRefuse` fails it on
  `liveness.aging_prevents_starvation` naming a Run two hours and five minutes into a
  wait its class caps at two hours.
- `a-batch-run-eventually-runs` (conformance): one machine, forty one interactive
  Runs arriving over an hour, and one batch Run that base priority alone leaves behind
  every one of them. At thirty minutes and thirty seconds of its own wait it is worth
  a hundred and one, the next arrival is the first told it waits behind it, and it
  takes the position that comes free and runs. It is the only fixture in either
  simulator that states starvation, which is a claim about what a stream of arrivals
  does to a Run over half an hour rather than about one moment of an ordering.
  Deleting the aging term from `Admission.EffectivePriority` fails it by name.
- `liveness.aging_prevents_starvation` (Lab invariant, strengthened): no Run sits
  queued past the longest wait its own class allows, and no wait admission ended at
  that bound was one the record says could have ended. The second half is what keeps
  the first from being satisfied by refusing everything, and it is stated over waits
  rather than over effective priority: production orders the queue on
  `Admission.EffectivePriority`, so a law reading the same function would agree with a
  deleted aging term. What it reads is the derivation the rate is built from, that
  half a class bound of waiting outranks anything arriving, so work admitted past such
  a Run must itself have waited half of its own bound. A fleet that published no
  machine which could ever hold the Run is the whole of the exemption, which is what
  separates a fleet too small from a queue that wronged somebody.
- `a-group-never-runs-wider-than-it-declared` (green): eight Runs in one family whose
  caller said three of them may run at once, four idle machines warm for the image,
  and the fourth Run told to wait. The record names `GROUP_AT_PARALLELISM`, the three
  siblings holding capacity, and no fleet answer at all, because no machine was
  weighed on the fourth's behalf. The ninth Run belongs to no family and takes the
  fourth machine, which is what makes the fixture about a declared width rather than
  about a fleet that ran out. Dropping the family from `WorkloadForRun` runs all eight
  at once and gives the ninth Run a queued Booking on a machine that was busy.
- `a-group-of-eight-runs-three-at-a-time` (conformance): the same family driven to the
  end under the real control plane. Three run, five wait, and as each member finishes
  the next is admitted, so the peak in the launch ledger is three and every member
  ends succeeded. A family held to its width by never running is starved rather than
  bounded, and only an execution can tell those apart.
- `safety.group_parallelism_respected` (Lab invariant): stated in two halves. The
  family every member declared reached the workload Mercator recorded, and no family
  ever held capacity wider than that, counted over distinct Runs in the launch ledger
  rather than over anything the control plane keeps. The first half is what makes the
  second falsifiable: reading the family off the recorded workload is right for the
  reason `safety.artifact_dependencies` reads a Run's inputs off it, and on its own it
  goes silent on exactly the defect this slice repaired, a declaration that never
  reached the record. Members that disagree about their own width are a violation of
  their own, which is the price of a group being a label the work carries rather than
  an object Mercator registers.
- `only-work-that-may-be-interrupted-runs-on-reclaimable-capacity` (green): two idle
  machines, and the cheap one is sold on terms that let its provider take it back. The
  standard Run goes first, while both are free, and `INTERRUPTION_NOT_PERMITTED`
  strikes out the machine its class would have bought; the batch Run behind it takes
  the reclaimable machine. Letting the standard class permit interruption puts the Run
  with most to lose on the machine that can disappear.
- `a-preemptible-run-is-the-one-interrupted` (conformance): the same fleet with the
  provider taking both reclaimable machines back five minutes in, which is the first
  thing in this corpus that happens to Mercator rather than because of it. The batch
  Run is the one interrupted, and the standard Run runs to the end on capacity nothing
  could reclaim.
- `safety.interruption_was_permitted` (Lab invariant): no execution the world took
  away belonged to a class that forbids interruption. It crosses the world's own
  reclamation with the workload Mercator recorded, because neither half says it alone:
  an execution whose machine went away and one that failed on its own are the same
  fact on the Run's record. It reads the permission off the class table, as the
  neighbouring laws read the queue bounds off it, so changing what a class permits is
  a change to the contract rather than a break of it. What it is red for is Mercator
  ignoring the permission, which dropping the feasibility refusal produces.
- `liveness.aging_prevents_starvation` (Lab invariant, restated over a divided wait):
  both halves measure the part of each wait Mercator caused, which is the whole of it
  less the intervals the Run's own family held it. The width counts members rather than
  machines, so no ordering could have ended those intervals, and a caller whose width
  outlasts its class's own patience has contradicted itself rather than been starved.
  The division is summed over intervals and never read off the latest answer: a
  deferral says who held the Run over the interval it opens, and exempting the whole
  record excused every hour the fleet had already starved a Run of as soon as a sibling
  took the family's place. `TestAWaitItsOwnFamilyHoldsIsNoStarvation` states a wait its
  family held all of, `TestAFleetStarvedWaitIsNotExcusedByASibling` is the failing case
  for the other direction, and the deliberate failure beside them is the identical
  record waiting on `NO_FEASIBLE_OFFER`.
- `a-family-place-is-taken-by-a-member-that-waits-its-turn` (green): the world
  `busy-rental-worth-waiting` states, with a family of two one wide in it. The first
  member is given a queued Booking behind somebody else's running work, which is
  capacity Mercator committed and not an execution, and the second member is held on
  `GROUP_AT_PARALLELISM` anyway. It is the only fixture in the tree that tells the two
  readings of the count apart: counting executions instead reports `run "sweep-2":
  expected outcome "defer", and admission recorded nothing at all about this Run
  waiting`, and leaves both group Blueprints, both group executions and every law green.
- `a-family-holds-its-own-members-past-the-queue-bound` (green): a family of two one
  wide, half an hour a member, two idle machines. An hour and a minute in, a minute past
  the whole queue delay this class states, the held member is reconciled and waits,
  because the wait is the caller's own declaration and not a promise Mercator made. A
  day and a minute in the same member is refused `DEADLINE_UNREACHABLE`, which is the
  caller's own bound ending it. Reconciling explicitly is how a fixture asks a held Run a
  question at a moment nothing else is happening.
- `a-family-narrower-than-its-class-patience-still-drains` (conformance): the same claim
  driven to the end, three members one wide at forty minutes each, so the family takes
  two hours to drain against the hour its class states. The test advances the clock into
  the middle of the second member's run, which is the minute sweep production has and the
  Lab does not, and reads the third member still queued and not closed; then all three
  succeed and the second machine is never taken. Against the reading it replaces the
  third member is closed failed after 4200 seconds.
- `a-member-that-gave-its-capacity-back-leaves-room` (conformance): the first launch
  failure inside a family anywhere in the corpus. One warm machine refuses the first
  member's launch, the Booking goes back in the same commit, and the second member is
  admitted onto the machine its sibling gave back and runs; the first member ends failed,
  because Mercator will not offer it the snapshot that just refused it. That a family
  which lost a member to a launch failure still drains is the claim, with one member
  holding capacity at a time throughout. Only one member ever runs, so no order is being
  claimed, and which recorded fact took the first member out of the count is not what
  this tells apart: the Booking going back and the Run closing land in the same pass, so
  either reading admits the sibling here. Its own summary said the opposite of all three
  until the review that follows.
- `a-wait-the-fleet-caused-is-not-excused-by-a-sibling` (green): a member whose ask no
  machine in the fleet can hold waits two hours and a minute on `NO_CAPACITY_FITS`, and
  then its sibling takes the family's one place. It is refused `QUEUE_DELAY_EXCEEDED`
  for the two hours the fleet kept it waiting rather than exempted from that moment on.
  Against the reading it replaces the fixture reports the `GROUP_AT_PARALLELISM`
  deferral it got instead.
- `only-the-part-of-a-wait-mercator-caused-is-charged` (conformance): the other
  direction, and the handoff instant itself. One reclaimable machine, a family of two
  one wide, and the provider taking the machine back an hour and a minute in, which
  interrupts the first member and gives the family's place back at a moment nothing
  else happens. The held member is deferred `NO_CAPACITY_FITS` with 3660 of 3660
  seconds charged to its caller, and refused `QUEUE_DELAY_EXCEEDED` an hour later with
  3660 of 7320 charged to Mercator. Against the reading it replaces the member is
  closed failed at the handoff, for a wait Mercator had kept it in for no time at all.
  It needs a world event because the plan-driven Lab has no way to give capacity back.
- `an-idle-machine-is-not-free` (green): an enrolled node at 1 USD an hour bought by the
  hour, ten minutes into the hour Mercator has already paid for, against a one-shot sold
  by the minute at 1.60 with a 5 cent fee to hand it over. Four steps. The half-hour Run
  is 1.17 USD on the node against 0.85 on the one-shot and flips away from the node,
  which is the whole slice: under a rate times one Run's seconds it is 0.50 and the node
  wins. The sweep behind it is refused the node `CLASS_NOT_ELIGIBLE` rather than priced
  there. The long Run wins the node back, because fifty-five minutes uses the hour the
  node costs either way. The last Run is refused the node `AVAILABILITY_WINDOW_CLOSES`
  while the long one is still on it, because the window that machine is Mercator's for
  closes before this Run's turn could end. It is the first fixture in the corpus to
  state a billing increment, a commitment, a reservation, or a window, and the first to
  assert a price term by term.
- `an-owned-hour-is-charged-to-somebody` (conformance): the flip and the reservation
  through the real control plane, with every term of the node's price asserted rather
  than the total, and both economics laws read off the recorded decisions. Against the
  reading it replaces the Run lands on the node and the test fails on its first line.
- `safety.no_capacity_is_free` (Lab invariant): every candidate somebody quoted carries
  positive dollars accounted for by the terms recorded beside it. An owned machine is
  the case it exists for: nothing new is billed for an hour already paid for, so a model
  asking what this decision adds to the bill reaches zero honestly and is wrong, because
  the seconds are the scarce thing and a candidate priced at nothing wins every
  placement it is weighed in. A machine nobody quoted is exempt and carries no terms,
  because pricing the absence of a price is the fabrication the law is against.
- `safety.committed_cost_is_not_double_counted` (Lab invariant): one second of one
  committed interval belongs to one Run. It is stated over the placements Mercator took
  rather than the candidates it weighed, because candidates are alternatives and neither
  has spent anything, and it also refuses committed rent charged past the end of the
  interval, which is the keep-alive term wearing the committed term's discount.

No Lab invariant reads a seeded schedule, and none can. Invariants are evaluated
only over the Lab's `InvariantObservation`, the placement harness at L0 evaluates
none at all, and `internal/scenario` imports nothing from `internal/lab`. Every
queue these laws have seen was committed by the Broker for a Run the Lab itself
created, which is what `a-queue-drains-as-it-runs` drives. An earlier version of
this section claimed `safety.exclusive_booking_capacity` and
`safety.ephemeral_capacity_not_reused` had begun reading seeded schedules at L0.
That was false in both halves, and it was offered as the reason the seeding work
needed no invariant of its own.

Seeding at L1 is a gap rather than a decision, and the Lab refuses the Blueprint
now instead of dropping the statement: `Compile` fails on a world stating
`rental_schedules`, because nothing loads them into the control plane's own
storage and the world would otherwise be built with every Rental idle while the
fixture said a machine was occupied for the next forty-five minutes. Two things
have to arrive before the Lab can hold a seeded queue. The Broker's storage needs
a seam a fixture may write through, and `liveness.superseded_booking_release`
refuses any Booking whose Run has no record, which is true of every seeded Booking
by construction.

The corpus is 62 regression Blueprints: 61 green and 1 target, beside two demo
documents, one minimized case, and forty three conformance Blueprints, all of
them green. The count is read off the
tree rather than remembered: `internal/scenario/scenarios/*.json` is the
regression corpus, `conformance/` is driven through the Lab, and the two
subdirectories beside them hold the demo and the one minimized case.
`internal/scenario/blueprint_test.go` asserts the three regression figures, so a
Blueprint added without a classification fails the build rather than drifting the
number quoted here.

One target is left, and it is the capability no simulated world performs yet.
`queued-booking-deadline-expiry` needs
`schedule_advancement`, which is a Booking expiring past its latest start and its
Run being placed again, and it is [#190](https://github.com/benngarcia/mercator/issues/190).
The other two are paid off. `enrolled-node-survives-its-first-run` went green on
2026-07-28, when the placement world learned to provision, enrol, and publish the
machine a listing became. `bad-host-facts-rejected-loudly` went green on
2026-07-29: an offer now carries the promises a machine made about the substrate
under a workload, a Run declares what its image needs of that substrate, and
Placement refuses a stated no and a silence under different codes.

The Lab registry holds fifty two invariants, forty four safety and eight
liveness. The figure is counted off `DefaultInvariantRegistry` rather than
remembered; an earlier revision of this section said forty five and was already
two behind, and a later one said forty eight. Every one carries a deliberate failing case, which
`TestEveryDefaultInvariantHasADeliberatelyFailingCase` requires of the registry
itself: an invariant nothing can make fail is not evidence, so one cannot be
registered without the world that breaks it.

The conformance Blueprints are driven from `internal/lab` rather than from the
placement corpus, because each one asserts something only an execution can say:
what bytes a workload was handed, when a transfer was moving, what a host held
afterwards, and what survived a control-plane restart.

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
`execution_warms_capacity` alongside `node_bootstrap`, which is the corpus stating
what its second step was always waiting on.

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

### Phase 5 the machine Shadeform rents and the agent it starts

Shadeform is a `CapacityProvider` and is the first backend in the reusable lane.
It rents a VM and hands it a script launch configuration that installs the pinned
node agent and starts it under systemd, so the machine enrols itself over an
outbound session and Mercator never opens anything on it.

The promotion took the one-shot half away rather than adding to it, and that is
`capability.Declare`'s rule rather than a choice made here: a backend that both
provides capacity and executes one-shot work is refused, because one lane is
stamped on every offer a connection publishes and nothing could then say which of
the two an offer came from. `Launch`, `Observe`, `Release`, `Terminate`,
`ListOwned`, `EphemeralSupport` and the docker launch configuration are gone, and
so is the argv shell-joining that existed only to fill
`docker_configuration.args`. `TestTheCatalogRecordsWhichBackendsHaveBeenPromoted`
is the deliberate act: `shadeform: reusable`, with docker, runpod and vast
unchanged.

What the negotiated set says, and why each half of it is what the four endpoints
can really do. There is no stop and no resume, because `/instances/{id}/delete`
destroys a machine and nothing suspends one, which also settles the persistent
disk: a disk that survives a stop is a claim about a provider that can stop.
Create honours no idempotency key, so `IdempotentProvision` is `none` and the
account listing is what a repeated provision is reconciled against, which is
exactly the pair `CapacitySupport.Validate` refuses a provider for breaking. A
destroyed instance stays in the listing while it is deleting and then disappears,
so `ObserveAfterTerminate` holds for that window.

The idempotency is the same convergence the deleted launch path used, rekeyed
from the launch key to the Rental: scan for a live instance tagged with this
lease before creating, scan again after, keep the oldest and destroy the rest,
and adopt what an indeterminate create landed instead of asking again. The tags
are exactly the fields `capability.OwnedCapacity` carries, because the listing is
the only place a reconciler can read them back from, and the filter is the Rental
tag rather than the Mercator prefix: a machine carrying no lease is not capacity
Mercator holds, and naming one there would have the reconciler adopt a lease
nothing ever took out.

The agent source is required connection configuration with no default, and that
is the judgment call this slice is most exposed on. Mercator publishes no node
agent binary today: `scripts/build-release-archives.sh` ships `cmd/mercator`
only. A download URL guessed in the adapter would be a paid machine fetching a
404 and never enrolling, and the Run would wait out its whole enrolment patience
to find out. So `agent_download_url` is declared, must be https, and must contain
`{version}`, which is replaced with the build the bootstrap pinned; a URL naming
no version installs whatever is behind it on the day the machine boots, which is
not a pin. The refusal is at provision rather than at construction, because
`Factory.Declarations` builds every backend with empty configuration and a
constructor that demanded it would take the whole catalog down. Shipping the
agent in the release archives, so this key can have a default that is a pin, is
[#234](https://github.com/benngarcia/mercator/issues/234).

What the machine is told is the invitation and nothing else. It goes into a 0600
environment file the unit loads rather than onto the agent's command line, where
every user on the machine could read it out of the process table, and what stays
on the disk after enrolment opens no door: the token is single-use and spent on
redemption. Values an unattended script cannot carry are refused before an
instance is created, and the refusal never quotes the value, because one of them
is a credential. The one credential a create body now carries is that script, so
it is redacted as a whole when a provider echoes the request back.

Two things the promotion corrected on the way past. A capacity listing no longer
states container capabilities, an idempotent launch or a concurrency limit: those
are the enrolled agent's facts, established from the machine, and they arrive on
that node's own offer, while what this provider promises about the lease it sells
is negotiated in `CapacitySupport`. And the manifest now declares every key `New`
reads: `base_url` and `os` were read and undeclared, which is a real
disagreement rather than a tidiness point, because the conformance validator
rejects a trial setting them while production accepts the same connection
silently. `TestATrialMaySetEveryKeyProductionReads` is the guard.

Evidence. `go build ./...`, `go vet ./...` and `go test ./...` are green, and
`go test -race -count=1 ./internal/adapter/shadeform ./internal/broker
./internal/providers ./internal/conformance` is green on amd64 Linux. Every rule
was proven able to fail by breaking the production behaviour it covers: a create
with no launch configuration fails the bootstrap case at `launch configuration =
<nil>`; an indeterminate create retried instead of reconciled fails at `got 2
create calls`; a stop that quietly succeeds fails at `stop = <nil>, want
ErrCapabilityUnsupported`; an owned listing that stops excluding deleting
machines returns two leases where one is live; a manifest that stops declaring
`base_url` fails the trial at `config key "base_url" is not public`; a bootstrap
that stops refusing unsafe material accepts a token carrying a heredoc
terminator; and a create body echoed back unredacted surfaces the whole base64
bootstrap in the operator's diagnostic.

The live half ran, on this host's own Docker daemon, and it found a defect no
string comparison could. `TestTheBootstrapScriptRunsOnARealMachine` renders the
script Mercator would hand a machine and runs it under a real shell on a real
filesystem in a `busybox:1.37` container, with `curl` and `systemctl` stubbed
because there is no agent binary to fetch and a container is not a booted host.
The script assumed `/usr/local/bin` and `/etc/systemd/system` already existed: on
a userland where they do not, it fetched the agent and then failed at `install:
can't create '/usr/local/bin/mercator-node'`, leaving a paid machine that had
downloaded an agent it never installed. It now installs every directory it writes
into. Removing the `systemctl enable --now` afterwards fails the case at the ask
rather than at the effect, which is what the stub is for.

The one break also found a defect in the test it was breaking. Redaction
re-marshals the decoded response body, which orders keys alphabetically, and the
fixture's 8KB padding field was named `detail`: it sorted before `message` and
pushed the echoed material past the size bound, so the assertion had been passing
on a body that no longer contained what it was looking for. The padding is
renamed to sort last, and only then does removing the redaction fail the test.

What this slice does not do, said plainly.

- No live Shadeform run. The bootstrap runs on a real machine here, and the
  provider does not: this host holds no `SHADEFORM_API_KEY` and no `op`, so
  everything that needs the marketplace itself is blocked rather than done, and
  nothing here claims otherwise. What only a live run can establish is whether a
  `shade_os` image carries `systemd`, `curl` and a working Docker daemon, and
  whether Shadeform runs the script as root once the instance is active.
  The exact command an operator runs is in `docs/production/shadeform.md` and the
  blocked run is [#235](https://github.com/benngarcia/mercator/issues/235).
- A Shadeform connection publishes no placement candidate, so no Run can be
  placed on it in production. `broker.Backend.ListOffers` answers a capacity
  connection with `NotAsked`, which is mercator#200, and `broker.launchOnNode`
  still resolves a node from the selected offer's native ref, which is
  mercator#207. Renting a machine works and nothing production-side asks for one
  yet.
- The bounded provider conformance suite is not here. The promises every
  `CapacityProvider` keeps are its own slice, and until it lands a `shadeform`
  conformance trial validates and then finds no offers, because the runner
  launches through the ephemeral lane.
- mercator#208 said the adoption defect had to land before any provider joined
  the reusable lane. It did not, and the hazard it names is nonetheless out of
  reach: `janitor.reclaim` acts on `adapter.OwnedExternalObject`s, a capacity
  connection answers `Backend.ListOwned` with nothing, and this adapter no longer
  implements `Release` at all. The issue stays open for the lane rather than for
  this backend.
- The broker's provider-failure test lost its production adapter, because the
  only backend that classifies provider failures has left the ephemeral lane. It
  is rewritten against a one-shot executor written for the case. Which status and
  code one marketplace calls out of stock is that adapter's own classification,
  held by `internal/adapter/shadeform`'s own tests; what the rewritten test
  watches is what the control plane does with a failure that is already
  classified.

### Phase 5 the driver a host provides and the stack an image brings

The defect had two halves and one cause: nothing in the tree ever wrote down
what accelerator hardware a machine had, so nothing could decide anything about
it.

`capability.HostFacts` declared `DriverVersion`, `DriverCapability` and
`Accelerators` in phase 2, and no agent ever populated them. `node.Registry`
publishes the accelerator inventory straight onto every node offer, so an
enrolled machine holding eight A100s advertised zero cards and was struck out of
every accelerator placement with `RESOURCE_INSUFFICIENT`. Read as a decision,
that says the fleet can never run the work; what happened is that nobody looked.
It blocked the whole phase, because the machines this phase exists to rent are
GPU machines.

The other half is compatibility. The host provides the driver and the image
provides the CUDA runtime that talks to it, and an image built against a newer
driver than the machine runs cannot start there. Nothing stated either number, so
the mismatch was something a launch discovered: minutes into a machine Mercator
had already paid to acquire, boot, and enrol, with a stack trace out of somebody
else's runtime and nothing in the record naming a driver.

What the agent reports, and the three states it can be in.
`internal/nodeagent/accelerator.go` asks `nvidia-smi` twice: `--version` for the
driver and the highest CUDA version that driver supports, and `--query-gpu` for
the cards. The container runtime's own answer is not a substitute: `docker info`
naming the nvidia runtime says a container can be handed the cards and says
nothing about how many there are or which driver is under them, and a toolkit
installed on a machine whose driver never loaded reports exactly what a working
workstation reports.

`capability.AcceleratorFacts` is tri-state, like the disk report beside it, and
the reason is sharper here. Every field is empty on a CPU box and empty on a GPU
box whose agent never looked, so a reader holding only the values cannot tell
them apart. `Established` is the agent saying it looked, whatever it found, and
`NodeFacts.Established` erases what an unestablished report happened to carry, so
no reader downstream has two answers to choose between.

What separates stated-false from silence is the kernel, and not the vendor
tool's exit status. What the tool establishes is what it printed: a machine whose
`nvidia-smi` named a driver has one, and the cards it went on to list are the
inventory. Every other outcome is this agent failing to reach an answer.

Reading a non-zero exit as the stated-false case was the third thing this slice
got wrong about the same question, and the review round that caught it
reproduced the harm on this workstation, whose driver works:

    unshare -rm sh -c 'mount -t tmpfs none /dev && nvidia-smi --version'
    -> NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA
       driver.  (exit 9)

A hardened unit with `PrivateDevices=yes`, and an agent in a container with only
the docker socket mounted, are both in that execution context, and both give the
same words and the same status a machine with no driver gives. Filed as the
established negative, an 8xH100 box refuses every GPU Run with
`CAPABILITY_MISMATCH facts.nvidia_driver` and tells its operator to buy a
different machine, which is exactly the harm the round before it fixed for
`exec.ErrNotFound`.

So the kernel is asked. A loaded NVIDIA driver publishes its version under
`/proc/driver/nvidia/version`, so a tool that failed beside a kernel holding the
module is an agent that could not see what is there, and a tool that failed
beside a kernel with no module is a machine that has established it runs no
NVIDIA driver. A kernel this agent cannot read establishes nothing, which is what
a host with no procfs is. `nodeagent.WithKernelReports` points the read at a
stand-in, because hardware is the one thing a case cannot arrange and the kernel
under it is the second: a case reading this host's own `/proc` would state one
machine on the workstation it was written on and another on the build box. It
also deletes a state: the tool's exit status no longer decides anything, so the
three-way `smiAnswer` collapses into whether this agent got to ask.

`exec.ErrNotFound` is still not a machine with no cards. It is the tool off this
unit's `PATH`, or unreadable to this user, or wedged. The slice hit that inside
its own daemon fleet and answered it in the test rather than in the product.
`-nvidia-smi` and `MERCATOR_NODE_NVIDIA_SMI` on `cmd/mercator-node` are the
operator's half of that answer, matching `-docker` beside them.

The two calls also fail independently, and the driver is stated as soon as
`--version` yields it. A card that has fallen off the bus answers `--version`
with a working driver and fails `--query-gpu` on the handle; discarding the
driver there published one report saying both that the machine established it has
no driver and that it never stated one, in a single Booking Decision, which is
exactly the distinction `domain.HostFacts` exists to keep. The cards it could not
count are an empty inventory, which is the true answer for a card that is gone.

Both calls are bounded at three seconds, because `Facts` runs on the heartbeat
select loop. That is the goroutine command work was deliberately moved off so
nothing long-running could stop the heartbeats and have the control plane declare
a healthy machine lost in the middle of the work it asked for, and this slice put
a new external command directly onto it.

The bound is this agent's own, and the earlier claim that `Cmd.WaitDelay` made it
real was wrong. `os/exec`'s `Cmd.Wait` runs `c.Process.Wait()` as its first
statement and only afterwards reads the watch result carrying the `WaitDelay`
timer, so nothing in `os/exec` returns while the process is still there. An Xid
79 puts `nvidia-smi` into an uninterruptible ioctl on `/dev/nvidiactl`, where the
SIGKILL behind the deadline is a signal left pending on a process that never
exits, and the heartbeat stops with seven good cards still running work. The call
now runs on its own goroutine and the heartbeat waits on the deadline, so the
report comes back whether or not the process ever does. `WaitDelay` stays for
what it really does, which is keeping the abandoned call from holding a goroutine
and a stdout pipe for the six hundred seconds the tool asked for.

The regression case was defanged for the same reason and is now honest about what
it arranges. Its stand-in trapped `TERM` and `INT`, which `SIGKILL` ignores, so
the trap was inert; what held the call open was an orphaned grandchild on the
stdout pipe, which is [go.dev/issue/23019](https://go.dev/issue/23019) and not the
unkillable process the text described. The fixture spawns that orphan on purpose
now, and the assertion is that the report comes back at the deadline rather than
at the deadline plus the reap delay, so deleting the goroutine bound goes red. A
process in `TASK_UNINTERRUPTIBLE` is not arrangeable from a test, which is why the
bound is written so that no case has to arrange one.

The memory a card states goes through `gpunorm.CardMemoryBytes`, for the reason
its model name goes through `gpunorm.Canonical`. A marketplace lists the capacity
a card is sold with and `--query-gpu=memory.total` measures the framebuffer left
after the driver and ECC have held their own regions back: this workstation's RTX
5090 is sold as 32GB and measures 32607MiB. Published raw, a `memory_min_bytes` a
caller copied out of a listing admitted the Shadeform 5090 and refused the
enrolled 5090, which is the silent strike-out this slice exists to remove, on the
lane phase 5 is about.

Rounding up to the whole gibibyte was not the conversion. It covers a driver
reserve of a few hundred mebibytes and leaves every part that holds ECC out of
band a whole gibibyte short: a T4 is sold as 16GB and measures 15360MiB, which is
exactly 15 GiB, and an L4 and an A10G sold as 24GB measure 23 GiB. All three are
in `gpunorm`'s own alias table, and the pinned fixtures were an RTX 5090, an RTX
4090, two H100s and two A100s, every one of which has a sub-gibibyte gap, so
nothing could go red where the rule failed. The conversion is now to the capacity
a part is sold with, from the list of capacities parts are sold in, and it is
bounded: a measurement with no sold capacity within an eighth of it is published
at the whole gibibyte, because a part `gpunorm` has never heard of is better
stated a little low than restated as the next size up.

A partition is not a card and is left as measured. A MIG `1g.10gb` instance
measures about 9856MiB, and publishing it as 10 GiB admits a Run that asked for
10 GiB onto less than it asked for, which is the same silent wrong answer in the
other direction. `nvidia-smi` names a MIG instance after the profile it was cut
to, so the model travels beside the measurement and the name is where the
distinction is read. The conversion is still not a tolerance in the comparison,
which would loosen every floor including the ones written against a real
measurement. `internal/adapter/docker`'s GPU probe shares it. The remaining unit
assumption is on the Shadeform adapter's side, where a listing's own number is
read as binary, and it is
[#234](https://github.com/benngarcia/mercator/issues/234).

What an offer carries. `domain.HostFacts` is its own field on `OfferSnapshot`
rather than more `ResourceInventory`, because these are promises and not
quantities, and because the answer that matters most is the one nobody gave.
`Attested` is a map rather than a set of the true ones: false is an answer.
Placement refuses a stated no with `CAPABILITY_MISMATCH` and a silence with
`UNKNOWN_FACT`, and an operator reading the decision then knows whether to buy a
different machine or to go and find out about this one. `HostFact` is a closed
set, checked where a Blueprint states one and where a Run declares one, because a
misspelling on either side is a promise nothing matches and a refusal that reads
as a fleet too small.

`domain.HostFacts.Violations` and `CompareDottedVersions` live in the domain
because two readers ask them: Placement deciding, and the Lab judging what
Placement decided. Two implementations of one rule is how the judge comes to
agree with the accused. The comparison answers separately whether two versions
could be ordered at all, because an unparseable component read as zero refuses
machines that are fine and read as large admits machines that are not. A
difference settled before the unreadable component is still settled, so a
distribution's patched `550.54.15-ubuntu3` clears a floor of 535 and is unknown
against 550.54.16.

A node states nothing about SSH, and that is the true answer rather than an
omission. Mercator reaches an enrolled machine over the agent's own outbound
session and holds no login on it, so a Run that wants a shell on its host is
refused `UNKNOWN_FACT` there. SSH is a provider's promise about a machine nobody
has allocated yet, which is where the corpus states it.

Evidence, in order of fidelity. At L0, `internal/domain/host_test.go` holds the
four arms apart: a fact nobody stated refused as a silence, a fact stated false
refused for what it is, a CUDA 12 driver refused under a CUDA 13 image, and a
driver nobody can order recorded as unknown; plus the admission refusal, so a
caller's typo is caught where the Run enters rather than in every Booking
Decision it would spoil. Reading a missing component as zero turns the fourth
into `CAPABILITY_MISMATCH` and fails it.

At L1, `bad-host-facts-rejected-loudly` is promoted green with its
`missing_capabilities` emptied. It gained a fourth listing, because the boolean
facts map alone cannot state the arm this slice is really about: `driverless-host`
is refused `CAPABILITY_MISMATCH facts.nvidia_driver`, `old-driver-host` is refused
`CAPABILITY_MISMATCH host.driver_version` on a 470 driver under a 535 floor,
`unattested-host` is refused `UNKNOWN_FACT facts.ssh`, and `good-host` wins at
twice the cheapest price. Deleting the one line that asks the offer's host facts
makes all four candidates feasible and places the Run on the machine with no
driver.

`safety.host_supports_the_image_it_was_given` asks two things over three sources
that are not checking themselves: no launch in the world's ledger landed on a
machine whose published facts cannot carry the workload Mercator's own event log
says it declared, and no Booking Decision called such a machine feasible. The
second clause is there because a candidate ranked feasible is one busy machine
away from being that launch. Its deliberate failing world is a host stating a
CUDA 12 driver accepting a launch whose image declares CUDA 13.
`WorldTruthSnapshot.PublishedHostFacts` keeps what a machine said after the
machine is gone, for the reason the published paths beside it are kept: a
placement is decided at one moment and judged at a later one.

What that rule does not yet have is a conformance Blueprint of its own. The
Blueprint vocabulary reaches both simulated worlds, `RentalSpec` states a driver
and `lab/world.go` publishes it, and no conformance Blueprint declares a host
requirement, so the rule passes vacuously on all forty three of them and its only
failing world is one a test constructs.
[#227](https://github.com/benngarcia/mercator/issues/227) is the world that
drives it: two rentals, one on a driver the image outgrew, and a Run that lands
on the other.

At L2, `TestANodeReportsTheCardsAndTheDriverUnderThem`,
`TestAMachineWithNoDriverEstablishesThatItHasNone` and
`TestAnAgentThatCannotSeeTheDriverEstablishesNothing` drive the real agent
against a scripted vendor tool and a stand-in `/proc`, because hardware is the
one thing a case cannot arrange and the kernel under it is the second.

At L3, on this workstation, the live half ran. `TestThisMachineReportsItsOwnCardsAndDriver`
checks the real agent against `nvidia-smi` asked directly, and
`TestThisMachinesCardsReachAPlacementAndAnOutgrownDriverDoesNot` runs the real
`nodeagent.DockerRuntime` against the real Docker daemon under the real node
registry and the real Placement. The machine reports one NVIDIA GeForce RTX 5090
with 32607 MiB on driver 595.71.05 supporting CUDA 13.2, the daemon has its
nvidia runtime, the enrolled node offers that card and that driver, a Run needing
an accelerator is placed on it, and a Run whose image declares a driver floor of
596 is refused `CAPABILITY_MISMATCH` in the Booking Decision and never handed to
the node. Removing the agent's accelerator read makes both fail with a driver of
`""` against nvidia-smi's 595.71.05; removing the offer projection's host facts
fails the fleet case the same way.

That live case resolves `nvidia-smi` to an absolute path and passes it through
`nodeagent.WithAcceleratorTool`, because the daemon fleet clears `PATH` so no
local Docker connection is seeded. Since the review round it is the same option
production uses, and an agent inside that fleet without it now reports a machine
nobody asked rather than a workstation with no driver. The live case also asserts
that every card the node offers states a whole number of gibibytes, which is the
unit a marketplace floor is written in; asserting a number instead would go stale
the next time this host's card changes.

`Established` reaching only the facts half of the offer was the blocking defect
of the round after that, and it is the one that mattered most. A node whose agent
could not run the vendor tool published `Resources.Accelerators` as an empty list
with no flag beside it, and `Resources` is where the count, the model, and the
memory floor are read: a Run declaring `resources.accelerators [{count: 8,
model_any_of: ["nvidia-a100"]}]` was refused `RESOURCE_INSUFFICIENT` on the
machine holding the cards, and only a Run that separately declared
`facts: ["nvidia_driver"]` ever reached the silence. No GPU Run is written that
way. `ResourceInventory.AcceleratorsKnown` now travels beside the list the way
`EphemeralDiskKnown` travels beside the room, every publisher states it, and the
scheduler asks it the way it asks the disk: `UNKNOWN_FACT` against a machine
nobody counted, `RESOURCE_INSUFFICIENT` against a machine that counted and came
up short.

The Docker lane publishes host facts now, and the claim that it was a catalog
with nothing to say was wrong. Its GPU probe enumerates the cards by running a
container with `--gpus all`, which is the same act a Run's own container
performs and which no endpoint without a loaded NVIDIA driver can complete, so a
probe that listed the cards has established the driver behind them in the pass
that built the offer. It asks for `driver_version` in the same query and returns
the same `capability.AcceleratorFacts` an enrolled node reports, so one
vocabulary states what a machine established about its cards on both lanes. A
daemon that answered and registered no NVIDIA runtime cannot hand a container a
card, which is an inventory of none somebody took; a daemon nobody could reach
establishes nothing.

That probe had also never worked. The NVIDIA container runtime injects the
host's own `nvidia-smi` and driver libraries into the container it starts, and
those are linked against glibc, so running them inside `busybox:1.37` dies with
`error while loading shared libraries: libdl.so.2` on every endpoint there has
ever been. Every Docker endpoint therefore advertised no cards whatever it was
holding, and no unit case could see it, because they all parse the output a
working probe would have printed. The GPU probe runs in `debian:12-slim` now; the
disk probe keeps busybox, which is the right image for `df`.

What is still missing on the provider lane. Nothing in `internal/adapter` outside
`docker` writes `OfferSnapshot.Host`, so a Run declaring `min_driver_version` or
`facts: ["ssh"]` is refused `UNKNOWN_FACT` on every Shadeform, RunPod, and Vast
offer. That refusal is the true answer for a catalog that publishes no driver
version, and `ssh` is deliberately a provider's promise about a machine nobody
has allocated yet, which is where the corpus states it. It is a gap in coverage
rather than a defect in the rule, and it is
[#230](https://github.com/benngarcia/mercator/issues/230).

What is still missing on the node lane, which the round before this one wrongly
implied was the covered one. Nothing in `internal/lab` or `internal/scenario`
imports `internal/nodeagent`, and `node.NewRegistry` is wired only in
`internal/daemon/runtime.go`, so neither simulated world runs the agent or
`node.Registry.offer`: `lab/world.go` builds its offers itself and stands in for
enrolment with its own `Invite`. Deleting the `Host` block from
`internal/node/offers.go`, or `gpunorm.CardMemoryBytes` from `smiDevices`, leaves
the whole Blueprint corpus green, and only the Go cases and the live daemon case
catch it. The accelerator-knownness rule this round added is stated in the corpus
because both worlds read `scenario.HostInventory`, which is where the flag is
set; the writers above it are not. That is
[#233](https://github.com/benngarcia/mercator/issues/233).

`go build ./... && go vet ./... && go test ./...` is green. The corpus gains
`a-machine-nobody-counted-is-not-a-machine-with-no-cards` as a green Blueprint
and its arrival-driven copy under `internal/lab/testdata/blueprints`, so the Lab
grades the refusal against
`safety.a_silence_is_not_an_answer_about_capacity` rather than against a
fixture's expectation. Reverting the scheduler clause turns the Blueprint red
with `NO_CAPACITY_FITS` where it expects `CAPACITY_UNSTATED`, which is its
deliberate failing case. The Lab found one more thing on its first run:
`safety.decision_is_reproducible` failed because `CanonicalHash` hashed the Go
value rather than the document it becomes, so a refusal carrying a struct in
`Required` hashed one way where it was built and another where it was read back.
It hashes the document now.

On this host, the live half ran again and further: the real Docker daemon, the
real RTX 5090, and the real driver, through both the node agent and the Docker
adapter's own probe. `TestIntegrationThisEndpointCountsItsOwnCardsAndNamesTheirDriver`
reports one card at 32 GiB on driver 595.71.05 and a Run needing an NVIDIA driver
not refused there.

### Phase 5 the session a machine keeps

The defect was that a node stopped being able to authenticate about thirty
minutes after it bootstrapped. `nodeagent.Agent.session` treated an expired
credential as no credential and enrolled again, presenting the invitation the
machine was bootstrapped with. `memoryStore.Enroll` clears `EnrollmentTokenID`
when it redeems it, so the replay answered `ErrEnrollmentSpent`, and past thirty
minutes the signer refused the same material on its own. The machine went on
running containers that nothing could reach and nothing could stop.

Why nothing caught it. Every case in the tree finishes inside `DefaultSession`.
The agent suite runs for milliseconds, the daemon fleet for seconds, and the Lab
had no notion of a session at all. A defect whose earliest possible symptom is
thirty minutes in is invisible to a suite whose longest case is thirty seconds,
which is why the shortened window is configuration rather than a test double:
`daemon.Config.NodeSession` and `node.WithSession` make the lapse a thing a case
states.

Renewal is its own act, in the ledger and in the code. `node.session_renewed` is
a separate operation from `node.enrolled` because the two have different material
behind them and different consequences: enrolling redeems a single-use invitation
and moves the fencing token, renewing spends nothing and moves nothing. Filed
under one name, a machine working for a day would read as a machine that joined
the fleet forty eight times, and the question the ledger exists to answer, which
invitation made this machine executable, would have no answer.

The truncation defect the conformance run found. The first L2 run failed with the
daemon refusing every renewal, and the cause was not renewal at all: `Signer.mint`
formats the expiry as whole Unix seconds, and the registry was telling the node a
moment with a fraction in it. At a two second session the credential died up to a
second before the moment the node was renewing against. At thirty minutes it is
invisible, and it would have stayed invisible until a clock skew or a slow round
trip made the difference matter on a real machine. `Signer.Expiry` is now what
every credential window is computed through, so a node is told the moment its
credential really has.

What the record is held to. `safety.bootstrap_credential_is_short_lived_and_single_use`
reads three things off World Truth: how many accepted allocations carried each
credential, how many times each was redeemed, and whether the bytes turn up in
Mercator's event log or Effect Ledger. The Lab world keeps every token it ever
minted, keyed by the token, because the interesting one is the credential a
reinvitation superseded: an invitation nobody is offering any more is exactly the
one that must not be redeemable a second time. The credentials travel on the
invariant observation and never on `WorldTruthSnapshot`, which the Lab's own HTTP
surface serves.

`safety.secrets_absent` was a rule about vocabulary. It searched for the field
names credential, password, and secret, so a token filed under `enrollment_token`
passed it, which is the name a bootstrap would honestly be filed under. It now
scans values as well: any credential this world minted wherever it is filed, and
any signed URL, because a presigned read is a bearer credential written as a
location and recording one would put a working read of the object store into
every exported bundle.

Evidence, in order of fidelity. At L1,
`a-machine-keeps-working-past-its-first-session` drives forty minutes of virtual
time and the ledger holds one enrolment and a renewal sequenced after the launch
and before the Run ends; deleting the renewal from the world's sweep fails it with
"the machine outlived its first session credential and the ledger holds no
renewal". A byte scan of the exported Run Bundle finds no credential material. At
L2, `TestAMachineGoesOnWorkingAfterItsFirstSessionCredentialLapses` runs a real
`nodeagent.Agent` against the daemon's own HTTP server with a two second session,
counts two renewals on the wire, then places and completes a Run, and asserts
exactly one invitation was redeemed; restoring the old behaviour turns it into a
loop of `ENROLLMENT_REFUSED` and fails on the renewal count.
`go test -race ./internal/daemon/ ./internal/lab/` is green, at 31s and 253s.

### Phase 5 the machine a listing becomes

`enrolled-node-survives-its-first-run` has been a target since phase 1 and is
green. What it was waiting on was never the bootstrap alone: the placement world
allocated a machine, handed it a bootstrap, and let its agent enrol against the
real node registry, and then had nowhere to publish the machine from, so the
second Run saw the listing it had already bought and bought it again.

The decision that unblocked it. Both the harness's own registry and the world
itself could publish an enrolled machine, and the world does. Publishing from the
registry is production's own shape, and reaching it means a Broker in the
placement harness, node facts carrying the machine's inventory, and a launch
addressed at a node runtime the placement world does not have; that is the Lab's
fidelity level and the placement corpus is about decisions. The world is already
the provider, so it publishes the machine the same way it publishes every other:
`fake.World.publishEnrolledMachine` adds a standing reusable offer at the moment
the agent's session opens, which is the same moment `AddMachine` already refuses
to call a machine reusable without.

What the machine is called. The offer ID is the provider's own handle for the
machine, which is the `machine` a Blueprint's listing declares:
`simcloud-4090-0f31` rather than `reusable-4090`. Naming the machine after the
listing would have made reuse an arithmetic identity rather than a decision, and
would have filed a launch history under a product. `WorldSpec.candidateIDs` now
admits a listing's declared machine so a fixture can name the winner.

A listing that declares no machine is a product rather than a host, and the
machine it yields is one nothing has named yet, so `fake.machineHandle` mints one
from the lease that bought it. The first version of this reached for the
listing's own ID, which replaced the product: the catalog entry stopped being
sold, the provisioning stages it published vanished, and two green fixtures were
doing it silently.

One machine is sold once, which is the rule the naming above rests on. A listing
that names a machine is a listing of that machine, so `ProvisionCapacity` refuses
a second purchase by name, and every listing of a machine under a live lease
answers `Capacity.Available: false`. Two listings of one machine are refused the
same way, which is a shape this corpus already contains on purpose:
`history-answers-for-the-machine-it-was-measured-on` publishes `machine-77` under
two ask IDs, because an ask ID is a fresh integer for every search. The first
version of this sold the machine as many times as the listing was bought, and
each publication overwrote the last: the second lease inherited the first
machine's identity and lost its layers, its busy window and its Rental.

A sold listing is refused rather than withdrawn, for the same reason
`TerminateCapacity` leaves the listing alone: a decision has to see an offer to
record having refused it, and the product is on sale again the moment the lease
ends. `ListCapacity` is the other question and answers only capacity to acquire,
which is what its own doc comment already claimed and what mercator#200 will
read.

`TerminateCapacity` withdraws the machine, which is the inverse the publication
owed. Without it a provider went on advertising available standing reusable
capacity it had destroyed, while `ListOwnedCapacity` in the same world reported
nothing owned, so a later Run could be placed on, and recorded as having
successfully executed on, a host nobody is billed for.

Where a workload runs. A Run placed on a listing is launched at the listing, and
`fake.World.executionHost` sends it to the machine that listing was allocated
into for that very attempt. The ownership token is what correlates them: the same
token stamps the provision command and the launch, and it is already what the
ownership sweep attributes a machine by. Without it the first Run's eighteen
gigabytes would land on a product nobody can run anything on, and the second Run
would find a cold machine.

The first Run has to end, and could not. Nothing in the placement world ever
finished a workload, so every Booking it created was immortal and every machine
it published carried one. `World.DefineRuntime` reads the Blueprint's existing
`runtime_models`, which is what the Lab already samples for the same question,
and a launch whose runtime a fixture stated exits when its work is done. A launch
nobody timed behaves exactly as before, still running for as long as the scenario
lasts, so no fixture that says nothing about runtimes changed.

One production defect fell out of it, and it is not the world's. A standing
reusable offer whose Rental Schedule is exhausted was feasible whenever the offer
said its capacity was available, and `domain.RentalSchedule.Reserve` then refused
the reservation, which failed the whole placement rather than striking out one
candidate. The two answers come from different authorities and can disagree,
which is why the schedule is now asked in its own right:
`RENTAL_SCHEDULE_EXHAUSTED` in `feasibility`, beside the availability check
rather than behind it. It does not end by waiting, which the first version of it
got wrong. The refusal's own message says the schedule cannot promise a start,
and that is not the claim that the capacity comes back when the work spending it
finishes: every projection off an exhausted schedule reads zero, so a refusal
counted as a wait deferred a Run behind a Booking that had already overrun, named
that Booking's Run as work ahead of it, and dated the wait at nothing. That is
the head-of-line block `domain.Violation` names as the reason the flag is false
by default. `an-exhausted-schedule-is-not-a-queue-to-wait-in` states the reason,
the work ahead and the fleet's own count, and fails on all three with the flag
put back. `an-overrun-booking-is-not-an-empty-queue` states both
codes, and it fails with the check removed.
`registry-manifest-bridges-digest-spaces` advanced thirty minutes past a twenty
minute bound and asserted the overrun Rental was still a candidate, which was the
hole written down; its advance is now ten minutes, which is what it meant.

The higher-fidelity half, and the live run.
`TestAMachineItsProviderBootstrappedIsWarmCapacityForTheNextRun` in
`internal/daemon` is the same claim against this workstation's own Docker daemon.
A real capacity connection holding a real `capability.CapacityProvider` is
authorized over the API, the real node registry mints a bootstrap, the provision
command carries it to that provider, and the production `nodeagent.Agent` is
started from the provider's own copy of it and no other input, so an identity the
provider was never handed could not enrol. The machine then appears as placeable
standing capacity, a Run lands on it and really fetches the image over the
registry protocol, and a second Run lands on the same machine charged zero boot
and zero image fetch from `image_inventory`. The image is taken off this host
before the fleet starts: a case that placed content the workstation already held
would charge the first Run nothing either, and would pass against a control plane
that learned nothing at all from the execution. Publishing an empty image
inventory from `Registry.offer` fails it, on the machine never reporting what it
ran.

The image the case places is built on the host and served from a registry the
case runs there. It used to be `alpine:3.20` pulled from Docker Hub and deleted
again on every run, and a refusal turned into a skip: once this address had spent
its anonymous quota, Docker Hub answered 429, the case reported SKIP in under a
second, and `go test ./...` reported the package green with the only live
statement about provider bootstrap never having executed. The case now commits an
image onto a base this host keeps, whose top layer is four megabytes of
randomness made for the run, pushes it to a registry container, and takes it back
off the daemon. The fetch is real, and the content is cold because it did not
exist until the run made it. What still comes from a public registry is the two
tags that scaffolding is built out of, `busybox:1.37` and `registry:2`, read at
most once per machine and never deleted. A host holding neither and unable to
fetch them now fails the case rather than skipping it, which is the rest of this
tree's answer to the same question. The reason to differ is what a skip costs
here: the live cases in `internal/nodeagent` and `internal/ociresolver` check a
reader against what this daemon itself reports and have in-process siblings
making the same claim, while this case is the only live statement Mercator has
about provider bootstrap and is the evidence cited for it above, so a skip
retires that claim with the tree still green. It did exactly that on this
workstation once. Reviewers found the skip surviving in this case's own gate
after the paragraph above was written, which is why it says this now instead of
saying nothing asks Docker Hub for anything.

The live cases hold this machine's Docker daemon while they use it, through
`internal/dockertest.Exclusive`. Three packages drive the one daemon this host
has, `go test ./...` runs them at once in separate processes, and run together
they measure each other rather than Mercator: that is mercator#212, and the file
lock is the option it asked for. It is also what makes a budget stated as "what
this host's Docker really takes" true, because a wait taken while four other
suites work the same machine is a measurement of them.

What that case deliberately does not do is let Placement choose to provision. A
capacity connection publishes no candidate, which is mercator#200, and the launch
that follows one cannot be addressed at the machine that was built, which is
mercator#207. The one act a placement would perform is performed by the case;
everything after it is production's.

### Phase 5 a Run that ends without taking its machine

Everything below ran on the amd64 Linux workstation with Go 1.25.11. `go build`,
`go vet`, and `go test ./...` are green, and so is `go test -race -count=1` over
`internal/domain`, `internal/orchestrator`, `internal/scenario`, `internal/httpapi`,
`internal/broker`, and `internal/lab`. Nothing in `web/app` was touched. No live
half ran for this rule, and there was none to run: `internal/lab` drives the
tape-driven simulated world end to end and holds no Docker and no object-store
case, and the live cases in the tree are `internal/nodeagent`'s and
`internal/adapter/docker`'s, which this slice did not touch. Cleanup disposition
is a control-plane decision, so what stands behind it is the corpus and the two
Lab cases below rather than a real daemon.

The behaviour was shown failing from both sides, which is what this rule needs: a
disposition has two wrong answers and each of them is invisible to the test that
catches the other.

Restoring the old rule, `case offer.Kind == OfferKindProvisionable`, so every
provisioned placement terminates again:

```text
domain       a fresh machine allocated to hold a Rental is cleaned up by
               "terminate", want "release"
orchestrator TestAProvisionedRentalRecordsReleaseAndLeavesItsHostStanding
               recorded disposition = "terminate", want "release"
lab          TestAReusableProvisionedRunReleasesItsWorkloadAndLeavesItsHost
               the Run recorded disposition "terminate", and a machine held under
               a lease is not a Run's to destroy
scenario     nine green Blueprints regress at once
```

Collapsing it the other way, so nothing ever terminates:

```text
domain       a one-shot execution Mercator allocated is cleaned up by "release",
               want "terminate"
orchestrator TestOneShotOfferRecordsTerminateDispositionAndInvokesTerminate
               recorded disposition = "release", want "terminate"
lab          TestAOneShotExecutionStillTakesItsHostWithIt
               the one-shot execution was never terminated
lab          TestCapacityNoRecordedLaunchAccountsForIsDestroyed
               the capacity two launches disagree about was adopted on
               recorded_disposition_release
scenario     ephemeral-execution-holds-nothing and
               ephemeral-execution-is-never-a-rental regress
```

The two new Lab cases are the higher-fidelity half and they read the Effect
Ledger, because Mercator's own record cannot answer this. The record says a
cleanup was confirmed under a recorded disposition; only the ledger says which
command the world received and what became of the machine.
`TestAReusableProvisionedRunReleasesItsWorkloadAndLeavesItsHost` drives
`provisioned-capacity-becomes-a-machine-mercator-holds` until that Run's cleanup
is confirmed, past the end of the twenty-minute Run the other cases in that world
stop short of, and holds four things together at that moment: the Run succeeded,
the workload was released, no `provider.terminate` and no `capacity.terminate`
had been carried out, and the lease is still held with its agent's session open.
The bound is the cleanup rather than the end of virtual time, and that is the
whole difference between a case about the end of a Run and a case that pins the
#206 leak in place: a Rental that ends its own generation when the last Booking
on it completes destroys this machine later, correctly, and would fail an
assertion swept over the whole ledger.
`TestAOneShotExecutionStillTakesItsHostWithIt` asks the same
question of `an-owned-hour-is-charged-to-somebody`, where the Run wins an
ephemeral listing, and requires the terminate that world's machine has coming.

Neither invariant went vacuous and neither needed changing.
`safety.ephemeral_capacity_not_reused` and
`safety.reusable_capacity_has_an_enrolled_runtime` still hold across every
execution, and `TestEveryDefaultInvariantHasADeliberatelyFailingCase` still
refutes both with the worlds it always did. What the change did to their coverage
is add to it: `a-machine-two-launches-disagree-about-is-not-adopted` now declares
its listing ephemeral, so one more L1 execution runs a one-shot product through
placement, launch, refusal, and the orphan sweep.

One honest gap in that coverage, which this slice did not close.
`safety.reusable_capacity_has_an_enrolled_runtime` reads the fleet as published,
and no world publishes the machine a provider allocated: the Lab's enrolment
writes a ledger entry and no offer, so in the provisioned worlds the rule is
carried by its queue clause alone and asks nothing about the machine that was
built. That is the same missing publication the second Run of
`enrolled-node-survives-its-first-run` waits on, and the rule gets its full
reading over provisioned capacity when that lands rather than by being rewritten.

### Phase 5 the disposition, under the third review

Everything below ran on the amd64 Linux workstation with Go 1.25.11. `go build`,
`go vet`, and `go test -count=1 ./...` are green, and so is `go test -race
-count=1` over `internal/domain`, `internal/scenario`, `internal/lab`,
`internal/janitor`, and `internal/orchestrator`. Nothing in `web/app` was touched,
and no live half ran, for the same reason as above: this is a control-plane
decision and `internal/lab` holds no Docker and no object-store case to run it
against.

Requiring the lane has its own deliberate failing case, and it is a Blueprint
rather than a rule. Deleting the refusal in `validateWorld` and loading
`testdata/blueprints/invalid/listing-that-states-no-lane.json`:

```text
scenario     TestLoadBlueprintRefusesACapacityAccountNoProviderCouldKeep
               loading listing-that-states-no-lane.json gave <nil>, want a
               refusal naming "the end of a Run destroys one lane and hands the
               other back"
```

The bounded Lab case still refuses both wrong cleanups, which is the thing the
bound had to keep. Restoring the kind-only rule so a provisioned placement
terminates again, the case fails on the recorded disposition; with that assertion
suppressed it fails because nothing released the workload; with that one
suppressed too it fails on the ledger itself:

```text
lab          TestAReusableProvisionedRunReleasesItsWorkloadAndLeavesItsHost
               the Run recorded disposition "terminate", and a machine held
               under a lease is not a Run's to destroy
               nothing released the workload, so nothing took Mercator's
               container off a machine it means to keep
               effect_0a880782f1d37c272c55b4f2 destroyed the machine by the time
               this Run was cleaned up, and the Run ending is not the lease ending
```

Collapsing the rule the other way, so nothing ever terminates, still fails on
all three of the green one-shot worlds the reviewers said were not there:

```text
lab          TestAOneShotExecutionStillTakesItsHostWithIt
               the Run recorded disposition "release" on a one-shot execution,
               which holds nothing once its workload exits
scenario     ephemeral-execution-holds-nothing
             ephemeral-execution-is-never-a-rental
             an-idle-machine-is-not-free
```

### Phase 5 the disposition, under the fourth review

Everything below ran on the amd64 Linux workstation with Go 1.25.11. `go build`,
`go vet`, and `go test -count=1 ./...` are green, and so is `go test -race
-count=1` over `internal/janitor`, `internal/domain`, `internal/broker`,
`internal/providers`, and `internal/lab`. Nothing in `web/app` was touched. The
only files that changed are this plan and one comment in
`internal/janitor/janitor.go`, because both findings were about what the plan
claimed rather than about what the code does.

What a sweep can actually be handed, read out of the code rather than asserted.
`Backend.ListOwned` returns `nil, nil` for a capacity connection, and
`TestASweepOfAWorkspaceHoldingCapacityConvergesTheWorkloadsItLeaked` fails if it
ever asks one for its machines. `Broker.Launch` sends a reusable launch to
`launchOnNode`, and `Broker.ListOwned` fans out over connection records, which a
node is not. `TestTodaysBackendsAreAllOneShot` pins docker, runpod, shadeform and
vast to `domain.LaneEphemeral`. So the only offer shapes a production sweep can
be deciding about are ephemeral standing and ephemeral provisionable, which
`OfferSnapshot.CleanupDisposition` answers with release and terminate, before the
disposition slice and after it. The branch that slice changed, provisionable and
reusable, has no production producer.

What an adoption leaves standing, reproduced. Running the arrangement of
`TestJanitorAdoptsCapacityItsOwnRecordSaysSurvives` with one assertion added, on
what the adapter still reports owning after the sweep:

```text
janitor      sweep={Found:1 Adopted:1 Terminated:0}, owned after adoption=0
```

`fake.Adapter.Release` deletes the object exactly as its `Terminate` does, and so
do `shadeform`, `vast` and `runpod`. That is #208. The probe was deleted rather
than kept, because a case asserting `owned after adoption=0` would write today's
defect down as the requirement.

### Phase 5 the generation binding and the measured stage, under the second review

Everything below ran on the amd64 Linux workstation with Go 1.25.11. `go build`,
`go vet` and `go test ./...` are green, including the live half: the node agent's
object-store cases and the Docker runtime cases execute against real containers on
this host rather than being skipped, so `TestANodeReplicatesAnArtifactFromARealObjectStore`
and `TestDockerRuntimeReportsTheLayersItUnpacked` are evidence rather than
simulation.

Three defects held. Both new assertions the previous entry added were among them.

The generation rule compared one value with itself. `simulatedWorld.ProvisionCapacity`
stored `command.Generation` on the lease, and `deliverEnrolments` wrote that same
field into the enrolment facts, so `invitedGenerations` and the rule read the same
number twice. The generation the bootstrap was minted under, which is what the node
redeems and what the fencing token is issued against, never reached the ledger. The
lease now holds the bootstrap verbatim and the enrolment is recorded under what the
agent redeems.

It was verified by injecting the defect into the control plane rather than into the
recorder. In a scratch copy of the tree, `allocateCapacity` sending
`Generation: requested.Generation + 1` while the token is minted for generation 1:

```text
enrolment effect_d3c9… opened a session under Rental "rnt_c915…" generation 1,
  and the machines allocated for that lease were invited for [2]
```

15 Lab tests fail on it, `TestProvisionedCapacityBecomesAMachineMercatorHolds` and
`TestDefaultInvariantRegistryPassesTheCanonicalExecution` included. At 3b2c3e4 the
same defect drove a fully green `go test -count=1 ./...`.

The mirror-image defect, `bootstrapFor` inviting under generation 2 while the
provider is asked for generation 1, now fails the Lab with the sentence the real
registry writes, because `simulatedWorld.EnrolledAt` honours the generation on the
`NodeRef` as `node.Registry.record` does:

```text
advance Lab Run "builder": orchestrator: read whether node "nod_c915…" enrolled:
  node: "nod_c915…" is generation 2, not 1
```

Before the fix the Lab answered "enrolled" to that question and reported a machine
ready to launch on where the real deployment cannot make progress at all.

The stage record carried the polling grid. Each stage was `now.Sub(since)` with both
ends at reconcile moments, and the fixture's 30s, 4m and 45s were exact multiples of
the fifteen second cadence, so grid and spend coincided. Each stage is now dated by
its authority, and where an authority does not date its transitions the record says
`bounded` and the seconds are published as the upper bound they are.

Both halves were shown failing. Forcing every stage to fall back to the look:

```text
agent_ready is recorded at 60s and this world spends 51s on it        (Lab)
the acquisition stage was recorded as 37s, and this machine spent 30s on it
the boot stage was recorded as 253s, and this machine spent 240s on it
the agent_ready stage was recorded as 70s, and this machine spent 45s on it
  and each of the three is also reported as a bound                   (orchestrator)
```

The probe the reviewers used now passes. With the fixture's `boot` at `4m10s` and
the expectation at 250 the case is green, and with the expectation left at 247 it
fails with `boot is recorded at 250s and this world spends 247s on it`. The record
follows the world rather than the reconcile interval, and the fixture's three
stages, 37s, 4m7s and 51s, are deliberately none of them multiples of it.

`TestAStageNoAuthorityDatesIsRecordedAsABound` holds the other half: a provider that
reports a state without saying since when yields the whole 37 second interval marked
`bounded`, rather than a 30 second measurement nobody made.

Two cases are new and both are load-bearing.
`TestTheWorldRecordsTheGenerationTheAgentRedeems` fails if the world's enrolment
line is restored to `lease.Generation`, and
`TestTheWorldAnswersAboutANodeAndAGenerationTogether` fails if the Lab's registry
goes back to looking a node up by name alone. Neither can be stated as a Blueprint:
a scenario describes a world, and what has to disagree here is two acts of
Mercator's, so both reach the world's own contracts directly.

### Phase 5 what a machine has to be for anything to accumulate on it

`safety.reusable_capacity_has_an_enrolled_runtime` is registered, and the Lab
world writes the enrolment it reads. It ran on the amd64 Linux workstation with
Go 1.25.11, which is a different host and a different architecture from the arm64
macOS laptop the phase 3 and phase 4 slices were verified on, and nothing in this
slice behaved differently for it.

Both halves were shown failing. Removing the world's enrolment record fails 20 Lab
tests across the corpus, and the violations name the machine and the clause:

```text
offer "producer-rental" holds sha256:9f2c1e2a…, and no agent has enrolled on
  machine "node-1", so nothing of Mercator's fetched or enumerated it
offer "rental-warm" holds cache "compiler-cache" for workspace "ws_lab", and no
  agent has enrolled on machine "node-1", so no workload of Mercator's ever wrote
  it there
Rental "rental-only" holds 2 Bookings and no agent ever enrolled against it, so
  the Runs waiting there wait for a dispatch nothing can carry
```

The fourth clause, an Artifact copy on a machine nobody enrolled on, is reachable
from no Blueprint: every fixture holding a copy on a Rental holds image content
there too, so the image clause answers first and the copy clause is never asked.
It is driven directly by `TestEveryClauseOfTheEnrolledRuntimeRuleCanFail`, and
deleting it from the rule fails that case alone.

One existing test changed. The hand-written ledger in
`TestWhatThisWorldDidOnItsOwnAccountIsNotACommandMercatorRepeated` wrote
`node.enrolled` records with no request projection, and this rule reads which
machine and which lease a session was opened for. An enrolment naming no machine
is not a record this world would ever write, so the fixture now states the
projection each of its three operations really carries.

### Phase 5 the Rental aggregate and the node a generation's end retires

Everything below ran on the amd64 Linux workstation with Go 1.25.11. `go build`,
`go vet`, and `go test ./...` are green, and so is `go test -race` over
`internal/domain`, `internal/rental`, `internal/node`, `internal/storage/sqlite`,
and `internal/lab`. Nothing in `web/app` was touched.

Every behaviour was shown failing with the production code broken.

```text
Retire leaves the record alone          -> nodetest "a retired node can never enroll again"
  (both stores)                            nodetest "a retired node renews no lease ..."
                                           rental TestEndingAGenerationRetiresTheRuntimeItWasServing
                                           rental TestARetiredRuntimeIsNoLongerPublishableAsCapacity
                                           node   TestARetiredRuntimeCannotHeartbeatItselfBackIntoTheFleet
Heartbeat sets StateReady               -> nodetest "a retired node renews no lease ..." (both stores)
  unconditionally again                    node   TestARetiredRuntimeCannotHeartbeatItselfBackIntoTheFleet
Save keeps only the current generation   -> rentaltest "a lease comes back with the generations
                                             it has been through" (both stores)
Save ignores the version it replaces     -> rentaltest "a write that does not follow the version
                                             the store holds is refused" (both stores)
                                           rentaltest "a lease identity is taken once" (both stores)
End the generation before retiring      -> rental TestALeaseNothingCanWriteRetiresNoRuntime
  its runtime
A destroyed machine leaves the lease     -> domain TestAnEndingThatLeavesNothingReleasesTheLease
  held                                     domain "a destroyed machine still held"
                                           rentaltest "a released lease comes back released"
Two generations may be open at once      -> domain "two generations open at once"
                                           rentaltest "a lease Mercator could not have reached
                                             is refused before it is written"
authenticate stops refusing a retired   -> node   TestARetiredRuntimeOpensNoFurtherSession
  identity                                          OnTheCredentialItHolds
dispatch stops refusing a retired       -> node   TestARetiredRuntimeIsAskedForNothingFurther
  identity
EndGeneration ends whichever            -> domain TestAnEndingNamesTheGenerationItDecidedAbout
  generation is current                    domain TestAGenerationRecordsOneEnding
                                           rental TestAnEndingRetriedAcrossAResume
                                                    TouchesNeitherTheLiveMachineNorItsRuntime
                                           rental TestAnEndingRefusesAGenerationTheLeaseHasNotReached
A lease-ending ending may be followed   -> domain "a generation after the lease was given up"
  by another generation                    rentaltest "a generation after the lease was given up"
                                             (both stores)
```

The resurrection is the one worth stating on its own, because it made the whole
retirement a no-op against any live agent. `Heartbeat` wrote `StateReady`
unconditionally in both stores, and `StateReady` inside its lease is exactly what
`Registry.Offers` publishes, so an agent on a machine being torn down put itself
back into the fleet with its next report. Both stores now refuse it, and the
SQLite one matches the state inside the statement rather than reading it first, so
a retirement landing between the two cannot be missed.

### Phase 5 the Rental aggregate under review

Two reviewers read beacf7e adversarially. Three findings were real and are fixed
at the root; one framing inside them was rejected.

Fixed. Retirement stopped nothing an agent actually does. `Retire` wrote
`StateRetired` and closed the open session, and `authenticate` never read the
state, so the agent's transport reconnected on the same credential within
milliseconds and `OpenSession` handed it every unacknowledged operation. The
retired machine launched the container of a generation whose lease the record says
was released. `dispatch` had the same hole from the other side: a `NodeRef`
resolved before the generation ended appended a durable command that nothing would
ever expire. Both now refuse a retired record, and the state is checked after the
credential verifies so an unauthenticated caller learns nothing about the node.

Rejected, one framing. The finding asked `dispatch` to check liveness. It checks
retirement instead. `StateLost` is a node Mercator has stopped hearing from rather
than a node that is gone, commands are durable and redelivered on the next
session, and `StopWorkload` against a machine that went quiet is exactly the
command an operator most needs to land. Refusing dispatch on liveness would delete
that. Retirement is the only terminal state and it is the only one refused.

Fixed. `Leases.EndGeneration` took no generation number, so a retry read the lease
fresh and ended whatever was current at read time. A reconcile loop that stopped
generation 1 and lost its answer would, after a resume, terminate generation 2 and
retire the runtime of a live machine mid-Run. `domain.Rental.EndGeneration` now
names the generation, answers a repeat of the same ending with the lease unchanged
and no write, and refuses a second, different ending for a generation that already
has one. `Rental.Generation` reads a generation by number, which the numbering
invariant `Validate` already holds makes a position.

Fixed. `Validate` accepted a lease whose earlier generation ended in a termination
or a reclamation and was then followed by a new open generation. `BeginGeneration`
refuses that, but `Validate` is what every store calls: `generationsRunInOrder`
only checked numbering and that non-final generations are closed, and
`releaseFollowsTheLastEnding` only inspects the newest generation. Written and
read back, `Held` reported true and `Current` reported the later generation open,
so Placement would send a Run to a host the record says was destroyed and the
workspace would keep a lease nothing releases. Nothing may follow the ending that
gave the lease up, and the conformance suite now holds both directions against
both stores.

### Phase 5 the enrolment rule under review

`safety.reusable_capacity_has_an_enrolled_runtime` landed in be301d5 and was never
refuted, because that run died with both reviewers in flight. It was read
adversarially before anything was built on it. Three findings were fixed at the
root and one was rejected.

Fixed. An enrolment naming no machine or no lease was skipped rather than refused.
It was a silent weakening in the one direction that matters: the listing clause
was keyed on a machine handle being absent, so one such record read as an
enrolment of the empty handle would have cleared every listing in the world at
once. It is now a violation naming the effect, held by
`TestAnEnrolmentThatNamesNoMachineIsRefusedRatherThanDropped`.

Fixed. The rule asked every offer although its name says reusable capacity, and it
is registered ahead of `safety.locality_provenance`, which owns exactly the same
worlds for a listing and for a one-shot host and refuses them the same three
things. A driver reports the first broken rule, so a bad ephemeral world was
answered with "no agent has enrolled on machine X", and enrolling an agent is the
one remedy the ephemeral lane must never apply. The rule now asks only capacity
that keeps what it runs, which deleted `describeUnenrolledMachine` and the listing
branch with it. The division of labour is asserted in
`TestCapacityThatKeepsNothingIsAnsweredByTheRuleAboutKeeping`, which holds that
this rule stays quiet and `localityProvenance` objects. Removing the world's
enrolment still fails the same 48 Lab cases on the same three clauses, so the
narrowing cost the corpus nothing.

Fixed. The Lab world recorded the machine handle as the enrolment's causation ID
and left its correlation ID empty, which is backwards from every other writer in
`world.go`: the correlation is what an entry is about and the causation is what
brought it about. These were the only entries in the ledger a Run Bundle could not
tie to anything. They are now correlated on the machine and caused by the
enrolment.

Rejected. The commit message explains the change to
`TestWhatThisWorldDidOnItsOwnAccountIsNotACommandMercatorRepeated` as an
ontological correction, and the mechanical reason is that without a request
projection the rule reports `decode enrolment : unexpected end of JSON input` as a
failed invariant rather than as a violation. That was reproduced directly. It is
not this commit's defect: every effect reader in `invariant.go` returns a decode
error the same way, and turning a tree-wide convention over in a vocabulary slice
would bury it. The fixture change is correct on its own terms either way, because
an enrolment naming no machine is now refused outright.

### What phase 5 slice 3 does not yet do

`enrolled-node-survives-its-first-run` is green as of 2026-07-28. Its four pending
assertions are gone: the second Run wins the machine rather than the listing, at a
boot and an image fetch of exactly zero, on a Rental Schedule that did not exist
when the first Run was placed. The listing beside it is refused as capacity
already leased rather than weighed and rejected on price. The counts move to 61
regression Blueprints, 59 green and 2 target.

What is missing, in the order it has to land. The Rental aggregate, its two
stores, the node retirement a generation's end performs, the provisioning path in
the orchestrator, and the disposition that leaves a provisioned host standing all
landed on 2026-07-28 and are struck from this list; the rest is not built, and
none of it is half built.

- The launch re-addressed to the machine that was built, filed as #207.
  `broker.launchOnNode`
  resolves the node from the selected offer's native ref, and the offer a
  provisioned Run was placed on is a marketplace listing whose ref is not a node
  id, so a Run that provisions a machine and watches its agent enrol cannot then
  be launched on it. This is why the whole provisioned lane is unreachable end to
  end in production today, and why the disposition that landed is held at L1 and
  in the orchestrator's own cases rather than against a real provider.
- An end for a lease nobody is using, filed as #206. A provisioned machine now
  outlives its Run,
  which is the point, and nothing yet decides when it stops being Mercator's:
  `domain.Rental`, its stores, and `rental.Leases.EndGeneration` have no
  production caller, so no idle machine is ever handed back. The one path that
  destroys capacity is the enrolment deadline in `orchestrator.reclaimCapacity`,
  and it fires only for a machine whose agent never came.
- An adoption that keeps the machine it says it kept, filed as #208.
  `janitor.reclaim` carries an adoption by calling `adapter.Release`, and `vast`
  and `runpod` implement `Release` as the same instance delete their `Terminate`
  performs. This said the fix had to land before any provider joined the reusable
  lane; Shadeform joined it on 2026-07-29 and the hazard is still out of reach,
  for a reason worth writing down rather than relying on: the janitor acts on
  `adapter.OwnedExternalObject`s, a capacity connection answers `Backend.ListOwned`
  with nothing, and a promoted backend no longer implements `Release` at all. So
  the issue is about the ephemeral lane's two remaining backends and about any
  future adapter that serves both, and the regression case belongs with it: no
  adapter in the tree can currently express an adoption that leaves a machine
  standing.
- The Lab world publishing the machine a listing became, filed as #209. The
  placement world does as of 2026-07-28 and the Lab still does not:
  `deliverEnrolments` writes `node.enrolled` and adds nothing to `world.truth`, so
  a Lab scenario with two Runs would send the second one back to the listing.
  Publishing alone is half of it, which is why it is its own slice: the Lab's
  launch, disk ledger, replicas and caches are all keyed by the selected offer,
  which for a provisioned Run is the listing, so a published machine would stay
  cold for ever. Making the execution land on the machine means an execution's
  offer and the decision's selected offer become legitimately different strings,
  which several invariants compare. Nothing in the conformance corpus runs two
  Runs against one provisioned machine today, so it has no reader yet.
- A stop this world performs, filed as #210. `fake.World.StopCapacity` returns a receipt saying
  the machine is stopped and changes nothing: the allocation carries no stopped
  state, `capacityStateAt` never reports one, and the machine goes on publishing
  itself as available capacity somebody could launch on. The negotiated capability
  set says this provider stops and resumes, so nothing refuses a Blueprint that
  asks it to. It predates the publication and is untouched by it, and the machine
  a stop should hold with its disk is now a thing this world has, which is what
  makes stating it worthwhile. Terminate's own half landed on 2026-07-28.
- `safety.capacity_lifecycle_is_negotiated` and
  `liveness.provisioned_capacity_enrolls_or_is_reclaimed`. Neither can be stated
  without a world that issues capacity commands, so both wait on the item above
  rather than on the rule being written.

### Phase 5 the capacity connection under review

Two reviewers refuted parts of the slice that made the capacity contract
reachable. Six of the eight findings were one defect described twice from two
angles, and every one of them was reproduced before anything was changed, against
the real daemon on the amd64 Linux workstation with Go 1.25.11: real SQLite, the
real HTTP API, the production reconcile path, and the slice's own provider fixture.

What the reproductions showed.

```text
FIRST reconcile:  reclaimed=0 err=capability: operation unsupported by this
                  backend: machines connection in the reusable lane does not
                  provide one-shot execution
SECOND reconcile: reclaimed=0 err=(the same, forever)
```

`ReconcileWorkspace` is what the one-minute reconcile loop calls. Before it failed
it appended `compute.capacity.orphan_converged.v1` to stream `orphan/i-held` with
`{"outcome":"terminated","reason":"unattributed","external_id":"i-held"}`, a
durable statement that a machine the provider deliberately holds was destroyed,
and `recordedDecision` reads that back on every later sweep, so the decision was
sticky and would have been carried out by the first sweep after #199 gave
`Terminate` a capacity route.

With the offer's container facts left unstated, which is all a provider can
honestly state, the recorded Booking Decision was `feasible:false` with
`UNKNOWN_FACT container.max_containers`, `CAPACITY_UNAVAILABLE capacity.available`
and `CAPABILITY_MISMATCH container.supports_digest_refs`, and the Run sat in
`admission_deferred NO_CAPACITY_FITS`. With them filled in, the Run was booked:
`rental_id:off_1c722f8c…`, `disposition:run_now_existing_rental`,
`REUSE_EXISTING_RENTAL`, and then
`ERROR provider operation failed operation=launch offer_native_ref=i-held`,
because the reusable lane sends a launch to a node and `i-held` is not one.

Each new rule was then shown failing with the production behaviour broken, which
is the discipline this project holds an invariant to.

```text
publish the listing again        -> broker TestACapacityConnectionPublishesNoCandidateForAMachineNobodyIsOn
                                   broker TestNoAdapterListingBringsALeaseIntoPlacement
                                   daemon TestACapacityConnectionIsHeldByTheProductionControlPlane
report machines to the sweep     -> broker TestASweepOfAWorkspaceHoldingCapacityConvergesTheWorkloadsItLeaked
                                   daemon TestReconcilingAWorkspaceHoldingCapacityConvergesTheExecutionsThatLeaked
                                   (failing with the reviewer's exact error)
keep the adapter's rental_id     -> capability TestNoAdapterCanStateARentalIdentity
                                   broker TestNoAdapterListingBringsALeaseIntoPlacement
count an unasked connection      -> broker TestACapacityConnectionPublishesNoCandidateForAMachineNobodyIsOn
  as asked                         daemon TestACapacityConnectionIsHeldByTheProductionControlPlane
publish a lease on capacity      -> Lab safety.a_rental_identity_is_capacity_mercator_holds
  Mercator does not hold           (13 Lab cases, against labOffer minting for every offer)
mint one from OfferKind again    -> nothing, and this row said otherwise
```

The last row is the correction. Restoring the deleted aggregation mint verbatim
leaves the whole suite green, which two reviewers demonstrated in a copy of the
worktree and this round reproduced before changing anything. The named test could
not fail: its double is a one-shot executor, so its offer is stamped ephemeral and
the reusable branch never ran, and the assertion passed because `StampLane` had
cleared the field rather than because aggregation minted nothing. After the slice
the branch is unreachable by construction, because `Backend.ListOffers` publishes
nothing for a capacity connection and every other declaration is ephemeral.

So the rule is now stated as the construction it rests on.
`TestNoAdapterListingBringsALeaseIntoPlacement` holds two things that can fail:
an adapter's stated `rental_id` does not survive aggregation, which fails when
`StampLane` stops clearing it, and every candidate an adapter published arrives in
the ephemeral lane, which fails the moment a capacity connection publishes its
listing again. A future slice that re-mints a Rental identity from `OfferKind` now
lands on a test with something to say about it.

Every finding was confirmed. One proposed remedy was not taken: consulting
`ListOwnedCapacity` during aggregation to decide which listing earns a Rental
identity. A provider saying it owns a machine is not Mercator holding a lease on
one, so that would have replaced the wrong evidence with different wrong evidence.
The machines Mercator holds are the ones its own agents enrolled on, and the
identity comes from the invitation that named the Rental.

### Phase 5 the second review round

Two reviewers refuted the round above. Four of the seven findings were real and are
fixed here; the rest are answered rather than changed, and each answer is a
measurement.

The census was recording an unasked question. `Backend.ListOffers` returns no
candidates for a capacity connection and makes no call to reach that answer, and
aggregation named the connection in `connections_queried` regardless, so a Booking
Decision stated that a provider had been consulted about a Run nothing had
contacted it about. `Backend.ListOffers` now answers with a `Publication` carrying
either the offers or the reason nobody asked for any, and aggregation files the
second under `excluded_connections`, where a de-authorised connection already goes.
`fanOut` carries one value per connection rather than a slice, which is what lets
the answer be typed where it is known. `StampLane` moved into `Backend.ListOffers`
with it, because that is where the offers cross out of the adapter.

The aggregation mint had no falsifying test, and the evidence table above said it
did. That row is corrected in place, and the rule is restated as the construction
it rests on.

One host is genuinely published twice, and this plan and ADR 0005 both asserted the
opposite. Reproduced live here through the production daemon, with a real Docker
daemon and the production agent enrolled on this same workstation:

```text
offer=nod_VSeQSRVTJ4IE9bJR connection=connection:nodes adapter=node
  machine=nod_VSeQSRVTJ4IE9bJR lane=reusable max_containers=1
  rate=0.00034722 available=true
offer=off_960b765d…e81e759e connection=docker adapter=docker
  machine=aa8e26d6-09e8-4870-91e9-c3979dc55ab9 lane=ephemeral max_containers=8
  rate=0 available=true
```

Two offers, one physical machine, no shared machine identity to deduplicate on: the
node names it by node ID and the Docker connection by the Docker daemon's ID. The
free copy wins every cost-based ranking and can hold eight containers on a host
whose runtime declares one, and per-machine learning splits across the two keys.
That is [#201](https://github.com/benngarcia/mercator/issues/201), with the
reproduction, and ADR 0005 now says nothing correlates the two rather than that the
registry supersedes the connection. It is not the capacity lane: a provider listing
publishes nothing at all.

An enrolled node's offer does not state what its runtime reported. Three of the five
capability facts, `container.max_containers`, `capacity.available` and the
digest-ref, entrypoint-override and `operation_id` launch promises, come from
`Registry.NodeSupport`, a static literal describing the agent Mercator ships, and
`capability.NodeSupport` never crosses the enrolment. ADR 0005 called all five the
agent's own report; it now attributes each one, and
[#202](https://github.com/benngarcia/mercator/issues/202) puts the set on the
enrolment. It is the same defect as the lane guard deleted in the round above, one
level down, and it is a wire change rather than a comment.

The corpus is blind to the offer route, and that stays true. The Lab serves
`/v1/offers` from `labOfferAggregator` and reads placement from
`simulatedWorld.CollectOffers`, so no scenario executes `broker.AggregateOffers`,
`Backend.ListOffers`, or `capability.StampLane`, and the four publication rules are
held by package tests. Closing it is not a comment fix: the Lab world is the
provider and the enrolled fleet at once, `capability.Declare` refuses one backend
that is both, and its standing reusable offers would have to come from a node
registry, which is the enrolment half of #200.
[#203](https://github.com/benngarcia/mercator/issues/203) records it, in the shape
#193 already records two orphan-policy rules held the same way.

What did land in the corpus is the half that fits it.
`safety.a_rental_identity_is_capacity_mercator_holds` fails when either simulated
world publishes a Rental identity on capacity Mercator does not hold, which is the
production rule stated over a fleet. Its deliberate failing case is in the registry
test, and it was also shown failing against the real corpus: minting an identity for
every offer in `labOffer`, which is the defect in the shape production had it, fails
thirteen Lab cases and names the offer and the reason each time.

Rejected, with the reason. Consulting `ListCapacity` during a placement read so a
broken provider fails the collection closed: a connection with no candidate to
contribute cannot change a placement, so failing every Run in a workspace over an
unreachable provider replaces an inaccurate census with an outage. A revoked
credential is caught by `Verify` at authorize time and by provisioning in #200,
which is the first caller `ListCapacity` gets. And the claim that
`ephemeral-execution-is-never-a-rental` is a false green: it is a `green` blueprint
because it passes, and it specifies the acquisition path production does not have
yet, which is #200, the same way twenty-five other fixtures in the corpus do. The
Lab spends `acquisition`, `boot` and `agent_ready` on a reusable listing and hands
back placeable capacity; what is missing is production, not the specification.


```text
go build ./... && go vet ./... && go test ./...
go test ./internal/nodeagent ./internal/ociresolver -count=1
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration -count=1
go test ./internal/daemon -run TestTheFleetListingReportsTheRoomThisMachineReallyHas -count=1
```

The live half ran. Docker here is native on Linux at 29.6.2, so nothing skipped:
`internal/nodeagent` and `internal/ociresolver` are 67 cases against a real
engine, a real object store and a real registry with zero skips, the three
`internal/adapter/docker` integration cases ran with the gate set, and the third of
them is the janitor sweeping a real container this adapter launched, which is the
case that would catch the sweep change breaking a real reclamation. The daemon
case that stands up the production agent against this host's own Docker daemon and
reads the disk it really has passed as well. All green. #165 does not reproduce
here and was left alone.

### Phase 5 the publication reviewed again

Two more reviewers went at the same slice and its plan entry. Five findings
arrived, one of them the same claim about the plan sentence stated twice. All of
them hold. Every one was reproduced against the tree before anything changed, on
this amd64 Linux workstation with Go 1.25.11, and each fix was then shown failing
by reverting it in turn.

A sold listing was counted as a wait that ends. Once a listing's machine enrols,
one host is published twice: the listing advertises the whole machine a buyer of
it would get, and the machine publishes the room it has left. Both are true and
they answer different questions. What was wrong is what the listing's refusal was
read as. `CAPACITY_UNAVAILABLE` carries `EndedByWaiting`, because capacity
somebody is spending comes back when they stop, and here Mercator is the one
spending it, on a machine standing in the same fleet under its own name, which
nothing hands back. So a Run no machine in the fleet could ever hold was recorded
as waiting for capacity to come free, named nothing it was behind, dated the wait
at nothing, and held the queue against everything after it. That is the
head-of-line block the same slice's `RENTAL_SCHEDULE_EXHAUSTED` half was written
to prevent, arriving through the other half.

The fix is where the refusal is stamped rather than in the world.
`domain.HolderOfMachine` reads the collision off the offer set, which is where
the two names for one host are both visible, and the scheduler refuses such a
listing `CAPACITY_ALREADY_HELD` without `EndedByWaiting`: whether this Run can
ever run on that host is the machine's own answer beside it. Making the listing
restate the machine's free bytes was tried first and fixed nothing, which is
worth recording. Content a candidate does not enumerate is never counted against
its room, on purpose, so a listing publishing the true 110GB still passed the
disk check for a Run needing 120GB, and the fleet still answered that one machine
could hold it once free. It is also not a lie to correct: a marketplace
publishing what a buyer of a leased-out host would get is answering a different
question from the fleet's.

It is stated in the corpus twice.
`a-listing-of-a-machine-mercator-holds-is-not-a-wait` drives the queue
consequence end to end, and `enrolled-node-survives-its-first-run` now records
both refusals on the listing it already weighed. Both fail without the rule. The
Lab states nothing about it and cannot: `simulatedWorld` never publishes a
machine beside the listing it was allocated from, so an invariant over this
collision could not fire in any Lab case, and an invariant that can never fire is
worse than none.

`TerminateCapacity` withdrew the machine on every call, including the repeat it
had just detected and reported as a duplicate. A listing that names a machine
hands the same handle to whoever buys it next, so giving one lease back twice
destroyed the next lease's live machine, and the provider then owned and billed a
host it published nowhere while no launch could resolve a host for its ownership
token. That is the mirror of the state the withdrawal exists to prevent. It now
belongs to the terminate that performed it, beside the moment the bill ended,
which a repeat does not move either. The path is real: reclaim terminates and
then commits the events recording it, and a failed commit leaves the next sweep
re-entering the same branch under the same operation key, which is what the
receipt's `Duplicate` field is for.

`ListCapacity` stated its rule on the Rental identity, which is too narrow for
the world to keep. Capacity that keeps nothing carries no Rental identity, so a
standing host was published as capacity to acquire, allocating it minted a lease
over a machine already in the fleet, and the fleet then held that one host twice
with the pre-existing one silently marked sold. The test is now what the capacity
is: a listing is for sale and a machine that already exists is a host this world
publishes. `ProvisionCapacity` refuses the rest at the source rather than leaving
the catalogue as the only thing between a caller and a lease on a machine that
was already there.

The plan sentence the last round wrote was false and is corrected above. Those
last two rules are held by package tests on the simulated world and by no
Blueprint, and this round cannot change that either. Production's only
`TerminateCapacity` caller is the enrolment-deadline reclaim, whose Blueprint
reclaims capacity that never enrolled, so no machine was ever published there and
the withdrawal is a no-op that cannot fail. `ListCapacity` has no caller at all
until [#200](https://github.com/benngarcia/mercator/issues/200), so no Blueprint
can reach it. Both were landed against the mandatory order and neither is
protected by the corpus an agent gates on; the honest record is that they are
package-level rules on the simulated provider until #200 gives them a caller a
Blueprint can drive.

```
go build ./... && go vet ./... && go test ./... -count=1
go test ./internal/nodeagent ./internal/ociresolver -count=1
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration -count=1
go test ./internal/daemon -run TestAMachineItsProviderBootstrappedIsWarmCapacityForTheNextRun -count=1
```

### Phase 5 the publication under review

Two reviewers refuted parts of the commit that published the machine a listing
becomes. Seven findings arrived and five are distinct: the same collision and the
same missing withdrawal were each described twice from two angles. Every one was
reproduced against the tree before anything changed, on this amd64 Linux
workstation with Go 1.25.11.

What the reproductions showed. `history-answers-for-the-machine-it-was-measured-on`
had grown two candidates the commit never touched it to state, and it stayed green
only because its measuring Runs held Bookings 2100 and 600 seconds past the
runtime Mercator enforces and nothing in that world could ever end one; stating
runtimes for those Runs made it fail on the winner. A second purchase from one
listing produced a second Rental on the same handle and a fresh `machineBehind`
that wiped the first machine's layers, its busy window and its Rental. A
terminated Rental went on publishing itself as available standing reusable
capacity while `ListOwnedCapacity` in the same world reported nothing owned.
`ListCapacity` offered a workspace a machine it was already leasing. And the
scheduler's new `RENTAL_SCHEDULE_EXHAUSTED` was stamped `EndedByWaiting`, which
made a machine 2100 seconds past its bound count as capacity that comes back.

All five are fixed at the root. Three of them are stated in the corpus above and
two are not, which the round below corrects the record on: the `TerminateCapacity`
withdrawal and the `ListCapacity` filter are held by package tests on the
simulated world and by no Blueprint or Lab invariant. One attribution in the
findings is wrong and is recorded here rather than accepted:
`ListCapacity` selling capacity the querying workspace already holds was not
introduced by the publication. `w.machines` is this world's whole fleet and
`ListOffers` is the fleet census, so a Rental a Blueprint declares was already in
`ListCapacity`'s answer before any machine was ever published, under its own
Rental ID. That was reproduced directly. The publication added one more machine
to a list that was already the wrong list. The fix is the same either way and it
landed.

The reviewers also missed the same collision in its other form, which the fix
covers: a listing that declares no `machine` was having its own catalog entry
replaced by the machine it yielded, because the publication key fell back to the
listing's ID. Two green fixtures were doing it silently on every run.

```
go test ./... -count=1
go test ./internal/daemon -run TestAMachineItsProviderBootstrappedIsWarmCapacityForTheNextRun -count=1
```

The live half ran. Docker here is native on Linux at 29.6.2 on amd64, and the
daemon case that bootstraps the production `nodeagent.Agent` from the provider's
own copy of the invitation, runs two Runs on the machine it enrolled, and reads
the second one warm off the first passed against it. #165 does not reproduce here
and was left alone.

### Phase 5 the credential a machine fetches content with

`PrepareImageCommand.RegistryCredential` and
`PrepareArtifactCommand.SourceCredential` had been declared since phase 2 and
populated by nobody. The node ran a bare `docker pull` and issued a plain
unauthenticated GET, and the only way a private image ever reached a rented host
was a registry account written onto the machine, which outlived every pull it was
ever needed for. This slice fills both fields and makes the far side refuse
anything else.

What crosses to a machine is `domain.ContentCredentialScope`: one operation, one
workspace, one piece of content, one expiry, with `RegistryPull` and
`ArtifactRead` carrying what is presented. `internal/credential.Mint` holds the
registry accounts and the object-store key, the orchestrator asks it as part of
deciding what a machine should be holding, and the Broker delivers the answer
without narrowing anything of its own. A public image is minted nothing, which is
the answer rather than a failure.

The shipped daemon builds that mint from what the deployment already states, and
for one revision of this slice it built none, which made every sentence above
true of the Lab and false of the product: `daemon.New` composed the orchestrator
without `WithContentCredentials`, so `mintPull` and `mintRead` returned zero
silently and every `PrepareItem` in a real deployment carried nothing. That was a
regression rather than an unbuilt path, because the same slice moved `PrepareImage`
off the Docker CLI, which reads `config.json` and sends the auth itself, onto the
daemon API, which does not. Registry accounts now come from the file `docker
login` writes, read by `ociresolver.DockerConfigAccounts` and keyed the way an
image reference names a registry, because the manifest resolver already reads that
file and a second place to say the same thing is one place to say it wrong. The
object store is `MERCATOR_OBJECT_STORE_ENDPOINT`, `_BUCKET`, `_REGION`,
`_ACCESS_KEY` and `_SECRET`, with nothing defaulted: unset is a deployment that
has none, which is real and refuses to mint a read rather than inventing a
location, and partially set is a startup error rather than a machine reporting it
was handed nothing.

Three seams enforce it, and each is tested where it lives. The registry and the
object store refuse: in the Lab because `ImageSpec.private` says the world serves
that image to nobody anonymous and the store answers only a signed read, and on
this host because distribution behind htpasswd really does. The machine refuses
before presenting anything: `authorisedPull` and `authorisedRead` check the scope
against the command, so material minted for another operation never reaches the
network. And the record holds the bound without the material.

Those last two are one seam rather than two, and keeping them apart is what the
slice got wrong the first time. A node command is durable so a machine that was
disconnected still receives it, and `node_operations` is kept for the life of the
deployment and pruned by nothing, so one encoding used for both the wire and the
record puts a fifteen minute credential in a SQLite blob for years. `dispatch`
now marshals twice: the machine gets the command as issued, and the row gets the
command without its material. The registry secret and the signed location go; the
operation, the workspace, the content, the expiry and the catalog's own durable
`Source` stay, which is the record of what Mercator authorised and is presentable
to nobody. The failure string was the same leak one hop on, because a transport
error from a presigned GET is a `*url.Error` whose message is the whole URL and
that string is stored in `node_operations.failure`.

What that costs is a replay. A command outlives the credential inside it, so an
agent reconnecting to a command issued while it was down is handed the record
rather than the pull, says so in Mercator's vocabulary, and the identity stays
reissuable. It replaces a worse failure: before this the same command arrived
carrying material minted before the disconnection and was refused as expired,
with the secret still in the row. Neither version recovers on its own, because
the orchestrator never learns a machine refused a preparation at all, which is
filed as [#224](https://github.com/benngarcia/mercator/issues/224) and reaches
every other way a prepare can fail.

The Broker hop has its own test because nothing else can see it. The Lab replaces
the world at the desired-set seam, which sits above `Broker.prepareOnNode`, so a
Broker that dropped the credential leaves every rule in the corpus green and
every private pull on a real machine refused. That was verified by breaking it:
the Lab stays green, `internal/broker` fails.

`PrepareImage` now pulls through the daemon API rather than the CLI, landed in the
production half of this slice. The CLI reads registry material out of a config
file, so a private pull through it means writing the credential onto a rented host
and remembering to remove it, and a pull killed halfway leaves it there.
`X-Registry-Auth` keeps it in the agent's memory for one request. A refused pull
arrives as a 500 whose body carries the denial, so the status is read and the
stream is drained rather than the status alone: a node reading less than that
reports every image it may not have as content it holds.

Shadeform's standing account is gone. The adapter read `registry_username` and
`registry_password` out of connection config, undeclared by its own manifest, and
wrote them verbatim into every create body, from where Shadeform put them on the
machine. The wire type went with them. The obvious replacement, a credential on
`adapter.LaunchRequest`, is wrong twice over: that struct is the private payload
of `launch_intent_recorded`, so minted material on it is a credential in the
durable record, and `RequestHash` is a canonical hash of the whole struct, so
material re-minted on a retry moves the operation identity every adapter
deduplicates on and a redelivered launch creates a second machine. The ephemeral
lane needs a seam that is not the recorded launch intent, filed as
[#218](https://github.com/benngarcia/mercator/issues/218); until it lands, an
ephemeral workload runs an image an anonymous reader can pull. The reusable lane
is unaffected.

The live half ran on this host. Docker here is native on Linux at 29.6.2 on
amd64. `registry:3` behind htpasswd, an image built for the case so it shares no
image object with anything else on the daemon, pushed through the daemon API,
removed locally, and pulled back three ways: with minted material it arrives,
with nothing the registry refuses it, and with material minted for another
operation the node refuses to present it. Each was proven able to fail by
breaking the production half it covers. The MinIO-backed Artifact cases stay
green with zero skips.

What this slice does not close.
`capability.LaunchWorkloadCommand` has no credential field, and the only producer
of `PrepareImageCommand.RegistryCredential` is the preparation sweep, which runs
for queued placements. A Run admitted and dispatched straight to an enrolled node
never passes through prepare, so `LaunchWorkload` runs `docker run` and the daemon
pulls implicitly, anonymously, and untimed. Whether a private image works there is
decided by whether prewarm happened to reach that machine first, which is a
scheduling accident rather than a contract. Filed as
[#221](https://github.com/benngarcia/mercator/issues/221), where the preferable
fix is to precede a node launch with a `PrepareImage` so a node has exactly one
place it ever fetches an image, rather than to grow a second credential path.

The Lab does not catch that, it licenses it. `contentRefusal` is consulted from
`startPrefetch` alone; `pullRunImage` moves the bytes with no reference to
`ImageSpec.Private` and no credential at all, so a private image launched on a
machine the sweep never reached is served anonymously and every rule stays green.
Making the world honest there needs a launch failure path, which needs the launch
to carry a credential for the corpus to stay green, which is #221's own slice.
It is recorded on that issue rather than half-changed here, and the Blueprint's
summary no longer claims a registry that serves nothing to an anonymous reader
without saying which seam that is true of.

A registry account is still the operator's standing account when it reaches the
machine. `credential.Mint` performs no token exchange, so what is minted for a
password registry is the same username and secret with a scope attached that only
Mercator's own node-side check reads. The rule registry entry above says what
that does and does not buy, and the live conformance case demonstrates the shape
rather than counterexampling it: `registryAccountSecret` there is the htpasswd
account itself. Narrowing it needs a per-registry token exchange, which no seam
in this phase asks for.

Preparation a machine refuses is never asked for again, whatever the reason:
`Broker.Prepare` reports what it dispatched rather than what the machine did with
it, and the orchestrator's in-process memory reads the unchanged desire on the
next sweep and stays quiet. Filed as
[#224](https://github.com/benngarcia/mercator/issues/224). It predates the
credential work and is reached by a broken link, a full disk, or a command
replayed past the material it was dispatched with.

One real defect surfaced from running the whole tree rather than one package.
Tagging a shared image into a local registry adds a repository digest to the image
object every other case on this daemon reads, and mercator#212's lock does not
help because the object outlives the lock: `ociresolver`'s conformance case took
busybox's first repository digest and got a registry on a port nothing listens on
any more. The case builds its own image now.

```
go build ./... && go vet ./... && go test ./... -count=1
go test -race -count=1 ./internal/lab ./internal/scenario ./internal/broker ./internal/adapter/shadeform ./internal/credential
go test -count=1 ./internal/nodeagent -run 'PrivateImage|SameReference|MintedForAnother' -v
```

### Phase 4 close-out

The branch was cut from `beng/artifact-locality` before that branch's own phase 3
close-out landed, so the pull request opened with a merge conflict and GitHub
computed no merge ref, which is why it had no CI at all rather than failing CI.
Merging the base in conflicted in eighteen files. Most were both sides appending
to one list. Six needed a decision about which model is current, and the rule
applied was that the newer answer wins and the superseded code is deleted rather
than left beside it: phase 4's class-derived weights and stage waterfall replace
the dead-weight score and `ArtifactSeconds`, and phase 3's fleet-wide preparation
pass with a durable clock replaces the per-workspace pass with an in-process one,
with phase 4's refusal handling reapplied on top of that shape.

The merge surfaced two real defects that neither branch could have seen alone,
and both are worth recording because both were silent until the other side's code
existed.

The first is a rate bound that stopped pacing anything. Phase 3's `remember`
answers whether stating a desire began any preparation, and the durable clock is
recorded only when it did. Phase 4 made the memory record what the holder kept
rather than what was sent, so that content a machine refused can be asked for
again. Composed naively, a wholly refused desire began nothing, moved no clock,
and was therefore re-askable in the same instant, forever:
`TestOneMachineRefusingIsNotEveryMachineStopping` caught it, reporting the cheap
machine answering a refusal and an acceptance at one timestamp. The two questions
are now asked of different sets. What Mercator remembers asking for is what the
holder kept, because a refusal left nothing anywhere. What the rate bound measures
is the attempt, refused or not, because the bound is on how often Mercator may
begin asking a fleet to move bytes.

The second is a test reading an API that phase 4 replaced. Phase 3's prewarm case
read `decision` from `GET /v1/runs/{id}/decision`, and phase 4 made that route
answer `decisions`, the chain, because a Booking Decision is appended and never
rewritten. The helper decoded nothing, returned the empty string, and reported
that the Run had never been placed. It had been placed the whole time, on the
machine the case named, and the case would have gone on reporting a placement
failure for an API change. It reads the last entry of the chain now.

CI then found four more defects that no command on this workstation could reach,
because the browser proof needs a Chromium this host cannot launch: Playwright's
system libraries are absent and there is no sudo to install them. All four are
recorded here rather than in the merge entry above, because they are what the
phase's own changes did to the console and they were invisible until a machine
with a browser ran them.

The first was a Run the Lab can build and production cannot. ServiceClass
replaced the placement objective outright, so every reader downstream of a Run
was promised one of five words, and the operator API keeps that promise by
normalising an omitted class to standard before validation sees it.
`WorkloadForRun` cast a Blueprint's request straight into the domain instead, so
three Blueprints produced revisions carrying the empty class.
`TestEveryArrivingRunStatesAClassMercatorKnows` is the rule now, over every
arriving Run in the corpus, with the fixture that states an unknown class on
purpose excluded by name.

The second is what actually blanked the console, and the first fix did not
address it. `OfferSnapshot.Reliability` is `omitzero`, so a machine whose
publisher has measured nothing sends no `reliability` key at all, which is every
provider in this tree. openapi.json listed it as required anyway and the
console's hand-written decoder mirrored the document rather than the wire, so the
offer catalog frame failed to decode, the feed ended before `ready`, and the
canvas drew its skeleton for ever. Phase 4 exposed it by turning reliability from
one confidence that always serialised into two rates that are absent when nobody
measured them. Fixed in the document, both generated readers, and the
hand-written decoder. `feed.contract.test.ts` decodes the real captured feed,
all forty frames of it, in `bun run test` rather than behind a browser, because
what broke was two documents disagreeing about one payload.

The third is the one worth remembering. Normalising the class turned the score
weights on for the demo Blueprint, which had been ranked on dollars alone, and
the consumer stopped choosing the machine holding its input. The Blueprint now
states that these Runs are batch work, which is what they are and which is the
only one of the five classes for which the demo's claim holds: dataset gravity
decides exactly when a caller can afford to wait for it. The coverage gap
underneath it is the finding.
`TestVerifyVerticalProofPassesEveryDeclaredCheckpoint` steps once, restarts, and
drives to completion, so every placement happens after the restart, and it stays
green through the whole regression. The console steps twice, advances, restarts,
and then advances until the Run closes, so the consumer is placed before the
restart against a fleet the other order never presents.
`TestVerticalProofHoldsInTheOrderTheConsoleDrivesIt` asks the same fifteen
checkpoints in the console's order, carries no browser, and fails on it.

The fourth is the Lab job being killed at Go's implicit ten minute test timeout.
`TestAgingLiftsABatchRunPastSustainedArrivals` drove a fixed hundred and fifty
sweeps for a Run that succeeds on the eighty fifth, and the remaining sixty five
assert nothing while being the most expensive in the suite, because each
re-decides the queue against an event log that is longest at the end. It was a
hundred and twenty three of the package's two hundred and seventy five seconds
under the race detector, and is fifty four now that it stops when the Run has
run. The step also states a twenty five minute budget, because ten minutes was a
boundary nobody chose that this job happened to sit on.

Run on 2026-07-27 on the merged tree, in the
`beng/prediction-and-service-classes` worktree, on an amd64 Linux workstation
with Go 1.25.11 and Docker Engine 29.6.2, which is not the arm64 macOS the phase
3 slices were built on. The whole verification was run again after the merge
rather than carried over from before it, because a merge that resolves eighteen
files is exactly the change most likely to invalidate an earlier green. Every
command below was executed and its real outcome recorded. Nothing is quoted from
an earlier run.

`go build ./...` and `go vet ./...` both exit zero with no output.

`go test ./...` exits zero across every package, 36 of which have tests. The
suite terminates. That matters, because an earlier slice on this branch
recorded that `go test ./...` did not terminate: `internal/daemon` spun forever on
a Run with no feasible offer, because `stepAdmit` reported progress while
`recordDeferral` suppressed the repeated deferral and returned nil, so
`AdvanceRun` looped on unchanged state re-scanning the event log. The queue slices
that followed replaced that path, and `internal/daemon` now completes in 9.0s.
The slowest packages are `internal/lab` at 18.6s, `internal/daemon` at 10.1s, and
`internal/conformance` at 2.1s.

`go test -race -count=1` over every package this phase touched, which is every
directory holding a Go file changed between `beng/artifact-locality` and this
branch, exits zero across the 24 of them that have tests. No data race is
reported anywhere. `internal/lab` takes 275.3s under the race detector,
`internal/cli` 31.8s, `internal/nodeagent` 26.7s, `internal/daemon` 17.7s, and
`internal/orchestrator` 15.3s. The package list is derived from the diff rather
than typed by hand, so a package this phase touched cannot be omitted by
forgetting it.

The corpus is 59 regression Blueprints, 56 green and 3 target, and 40 conformance
Blueprints, all green. Those figures are asserted by
`internal/scenario/blueprint_test.go` rather than counted by eye, and it passes.
The three targets each name the capability no simulated world performs yet:
`enrolled-node-survives-its-first-run` (agent bootstrap, phase 5),
`queued-booking-deadline-expiry` (`schedule_advancement`), and
`bad-host-facts-rejected-loudly`.

The web console changed in this phase, so its checks were run too. In `web/app`,
`bun run typecheck` is clean under `tsc --noEmit`, `bun run test` passes 17 tests
across 6 files under vitest 4.1.10, and `bun run build` produces the three
artifacts into `web/static`.

Two limits on what this evidence covers, stated rather than left to be inferred.
The live half of the phase ran against a real local Docker daemon, a real
`registry:2` container, and a real MinIO container, so the node, disk, registry,
artifact-replication, and janitor-sweep paths were exercised against real
software. No marketplace was contacted: this host holds no Vast, Shadeform, or
RunPod credential, so the claim that a provider's own identifier recurs across
listings, which is what the prediction key rests on, is held by unit cases against
recorded response shapes and by the Lab, and by nothing live. And
`TestRegistryResolverAgreesWithDockerAboutAPublicImage` still skips, because
Docker Hub rate-limits this host.

### Phase 4 replanning by explicit policy

On 2026-07-27, two defects the plan already recorded as owed were repaired on a
Linux workstation with Docker Engine 29.6.2 on the overlayfs driver, which is
amd64 and not the arm64 macOS the phase 3 slices were built on. Nothing behaved
differently there.

The state-blind dedupe. `node.Operation.Reissuable` is the whole rule and it
reuses the line `CommandKind.MayLeaveEffectOnFailure` already drew. Restoring the
state-blind answer fails both stores through `nodetest.RunStoreSuite` with "a
preparation the node refused must be askable again, not answered as already
recorded", fails the Lab fixture with "the ledger records 1 asks for the refused
corpus, want the refusal and the ask that followed it", and fails the daemon case
with "the machine never held content it refused once and was asked for again".
Marking a refused fetch as prepared in the Lab world fails
`safety.idempotent_external_commands` with "duplicate effect
node.prepare_artifact/prepare-artifact/builder/artifact:corpus:v9 has no accepted
command".

The orphan policy. Converging without recording fails
`safety.orphan_policy_is_explicit` with "orphaned capacity orphan-nobody-claims is
gone from this world and no decision names the policy that took it". Reclaiming
unattributed capacity by releasing its slot, which is what the sweep did before it
had a policy, fails `an-orphan-is-adopted-or-destroyed-by-policy` with "the fleet
is [forgotten keeper stranded], and the machine the policy terminated is still
billing".

Reviewed and repaired the same day, before the slice was taken as done. Two of
those repairs are stated by the corpus and two are stated only by package tests,
and they are listed apart, because a reader who takes the green corpus as the
guard for all four would let a later refactor undo half of them in silence.

The corpus states these two. Reading the cleanup request ahead of the recorded
launch fails `an-orphan-is-adopted-or-destroyed-by-policy` with "the capacity of a
Run that gave up was converged as terminated / closed_without_a_cleanup_request,
want it adopted on its recorded disposition". Matching a refusal on content alone
fails `a-refusal-on-one-machine-is-not-a-withdrawal-on-another` with "the ledger
records 0 withdrawals, want the transfer nothing was waiting for any more".

`internal/janitor` alone states the other two, and `go test ./internal/lab
./internal/scenario` is fully green with either of them reverted. Acting before
recording fails `TestJanitorRecordsItsDecisionBeforeItActsOnIt` with "the record
holds 0 orphan decisions, want exactly one" once the provider refuses one reclaim.
Routing terminate at a provider that holds no machine of Mercator's fails
`TestJanitorReleasesTheSlotOfCapacityItsProviderCannotDestroy` with "sweep:
adapter: terminate unsupported for standing capacity", which before the repair
returned before every later object in the same listing. Both gaps are in the
Blueprint vocabulary rather than in the rules: the Lab's provider destroys
anything asked of it and cannot be made to refuse a reclaim, and a sweep that
returned an error would fail the Lab control plane's tick rather than leave a
state a rule could read. Earning those two a world is owed.

Reviewed again and repaired the same day. Deciding capacity by a Run's last
recorded launch rather than by the launch whose identity the capacity carries
fails `a-machine-two-launches-disagree-about-is-not-adopted` with "the capacity two
launches disagree about was converged as adopted / recorded_disposition_release,
want it destroyed as capacity no launch accounts for", and fails
`internal/janitor` on both mixes: the machine one attempt provisioned handed back
as a slot, and a slot another attempt borrowed routed a terminate. It fails on
this host's own Docker daemon too, through
`TestIntegrationTheJanitorConvergesOneAttemptsContainerByThatAttemptsLaunch`, with
"the sweep of this daemon reports {Found:1 Adopted:1 Terminated:0}". That case is
the live half of the rule and it exists because nothing below the control plane
tells the janitor which launch a container came from: the attempt and the launch
key travel as labels the adapter writes and reads back, so the rule is only as
true as that round trip through a real engine. Local Docker reaches the same
daemon command either way, so the recorded reason is the assertion. Reading a
control plane's own absence as a desire for nothing fails
`a-restart-still-withdraws-what-nobody-waits-for` with "the ledger records 0
withdrawals, want the transfer the restart left running", and the replica lands at
00:31:40 for Runs withdrawn at 00:12. Counting a withdrawal against the rate bound
fails the daemon's own `TestAQueuedRunPreparesTheMachineItIsGoingTo` with "the
queued Run's host was never asked to prepare anything", because a control plane
that sends one desire of nothing at startup then owes the interval before it may
prepare anything real.

What is left. The control plane's own second ask still rides on
`PrepareReceipt.Refused`, and no production prepare lane fills it: `broker.Prepare`
answers Started or Duplicate, a node settles a refusal asynchronously, and nothing
in the prewarm controller subscribes to that, so what triggers a second ask on a
node is a change to the desired set. The store change is what makes that second
ask reach the runtime when it comes, and it is the whole of what this slice
delivered on that lane. Orphaned reusable capacity is still only what `ListOwned`
reports, which is the ephemeral executor's view;
`capability.CapacityProvider.ListOwnedCapacity` has no caller, so a machine
Mercator provisioned and lost the Rental record for is not yet something the
policy can see.

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/node/... ./internal/storage/sqlite ./internal/janitor \
  ./internal/orchestrator ./internal/broker ./internal/adapter/... \
  ./internal/lab ./internal/scenario ./internal/daemon -count=1
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration -count=1
go test ./internal/nodeagent ./internal/ociresolver -count=1
```

The live half ran against this host's own daemon. `internal/adapter/docker`'s
three integration cases are gated on `MERCATOR_DOCKER_INTEGRATION=1` and were run
with it set; without it they skip. The third is the janitor sweeping a real
container this adapter launched, which is where the per-launch rule meets a real
engine's labels. `internal/nodeagent` ran against a real daemon and a real MinIO
object store, and `internal/ociresolver` against a real registry. The daemon suite
is not part of that claim: `startFleet` empties `PATH` on purpose, so no local
Docker connection is seeded and this slice's conformance case there runs against a
scripted runtime. All green. #165 does not reproduce here and was left alone.

### Phase 4 no capacity is free

On 2026-07-27, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2 on Ubuntu 26.04, against
`beng/prediction-and-service-classes` with this slice's commits on it, from
`65b2fa1`. `go build ./...`, `go vet ./...` and `go test ./... -count=1` all clean over 36
packages, and `go test -race -count=1` clean over `internal/domain`,
`internal/scheduler`, `internal/scenario/...`, `internal/node`, `internal/httpapi`,
`internal/storage/sqlite`, `internal/daemon` and `internal/lab`, `internal/lab`
taking 216s of it. `cd web/app && bun run typecheck && bun run test && bun run
build` clean, because the contract was regenerated from `openapi.json` rather than
hand edited.

The root corpus is 59 Blueprints, 56 of them green, with 32 conformance fixtures. Two
Blueprints were added and no fixture moved classification.

The Blueprint is red under the one-number shadow price, which is the acceptance this
slice was set. With the commitment and the increment unpriced and the setup fee charged
to everything, `an-idle-machine-is-not-free` reports the placement itself as well as
the arithmetic: `expected "ask-minute" to win, but the decision placed on "node-owned"`,
`candidate "node-owned": cost_usd: want at least 1.16, got 0.5`, `cost term
committed_rent: want at least 0.166, got 0`, `cost term idle_tail: want at least 0.666,
got 0`, and then the fourth step never gets a decision at all, because the node is
still running the first Run. The same mutation fails
`TestAnIdleMachineIsNotFreeAtL1` on its first line, with the half-hour Run landing on
the node through the real control plane.

Both laws are red against the record they exist to forbid, and the failing cases are
permanent rather than mutations.

- `safety.no_capacity_is_free` fails on `ownedMachinePricedAtNothing`, an owned machine
  weighed for a Run and priced at nothing because the hour it sits inside was already
  paid for. `TestAPriceItsOwnTermsDoNotAddUpToIsRefused` is the accounting half, a
  candidate priced at 0.85 USD out of terms adding up to 0.80, and
  `TestAnUnquotedMachineCarriesNoPriceToAccountFor` is the exemption, so the law cannot
  be satisfied by inventing dollars for a machine nobody quoted.
- `safety.committed_cost_is_not_double_counted` fails on `oneCommittedHourSoldTwice`,
  two placements on one machine each charged everything still outstanding when its own
  decision was taken, which is what pricing a commitment from the decision's moment
  produces. `TestCommittedRentStopsAtTheEndOfTheInterval` is the single-placement form
  of the same overselling, and `TestTwoRunsMaySpendOneCommittedHourBetweenThem` is the
  lawful case that keeps the law from being a ban on committed rent.

Per-candidate oracle agreement still holds with the new terms, and the reference model
derives every one of them independently: its own occupancy, its own overlap with the
committed interval, its own rounding up to the increment, and its own reading of what
has to be acquired. A reference model that called the production pricing function would
agree with it about a bug in the rounding, which is the one thing an independent model
is for.

Three terms this slice was scoped to carry are not priced, and each is a decision with
a reason rather than an omission.

- Stopped-state storage has no horizon anything states. A machine Mercator stops rather
  than releases costs its disk until something uses it again, and nothing here predicts
  when that is; every honest-looking substitute made a longer Run cheaper than a shorter
  one, because it charged the part of a commitment the placement did not consume.
- Preemption risk is not priced, and the corpus already argued this out under
  `a-published-risk-history-ranks-nothing`: expected redo cost is a probability times a
  predicted redo, and what the probability multiplies is the placement the work would be
  redone on rather than this one. A published interruption rate is a rate rather than a
  hazard over the length of a Run, so nothing here can say how much of a Run is lost
  when a machine drops it. The availability window is the part of that risk that can be
  stated without inventing either: the moment is declared, so it is a refusal.
- Warm-capacity opportunity cost is not a term of its own, because it would double
  count. An owned machine's shadow price is exactly the statement that its seconds are
  worth something to somebody else, which is why committed rent is charged to the Run
  that spends those seconds even though no invoice depends on the decision.

The idle tail is deliberately conservative and this is where that shows. It charges the
whole unused remainder of the increment a placement forces Mercator to buy, to the
placement that forced it, and a later Run that used part of that remainder would be
charged nothing for it. Splitting the tail between a Run that bought it and Runs that
have not arrived needs a model of what arrives next, which nothing here has; the
direction of the error is the safe one, because it overstates what committing to fresh
capacity costs rather than understating it, and the alternative is the previous model,
which charged the remainder to nobody.

What the window does not yet cover, said plainly. A placement is refused when the
runtime Mercator enforces could outlive the window, which is checked once, at the
decision. A Booking queued behind another one is projected from where that Booking is
at that moment, so a predecessor that overruns can push a queued Booking's own
enforced end past a window that was clear when it was admitted. Nothing reconciles
that today: no Blueprint can state it either, because the corpus has no world where a
window closes over a queue, and the slice that adopts or terminates capacity by policy
is where a Booking caught by a closing window belongs.

Named and not fixed here. The local Docker adapter publishes `RatePerSecondUSD: 0` with
`Known: true`, which is the one production publisher of capacity somebody says is free,
and `safety.no_capacity_is_free` cannot see it because invariants read Lab executions.
The honest answer is either a configured shadow price on the connection, as a node has,
or an unpriced offer that a Run must allow, and both change where every local Run lands.
It wants a slice of its own. `gofmt -l .` still reports
`internal/adapter/vast/client.go` and `internal/scheduler/scheduler_test.go`, struct tag
alignment left earlier on this branch.

The live half ran on this host's own daemon rather than in simulation:
`MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration`
launches, observes, and releases a real container on the native engine. Nothing in this
pass needed a container of its own, because what it changes is arithmetic over
published facts and what an operator can say about a machine they own; the highest
fidelity that means anything for it is the real node protocol over the real event log
and SQLite, which the two `internal/daemon` cases drive. Mercator issue #165 does not
reproduce here and was left alone.

```text
go build ./... && go vet ./... && go test ./... -count=1
go test -race -count=1 -timeout 900s ./internal/domain ./internal/scheduler \
  ./internal/scenario/... ./internal/node ./internal/httpapi \
  ./internal/storage/sqlite ./internal/daemon ./internal/lab
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration
cd web/app && bun run typecheck && bun run test && bun run build
```

### Phase 4 the divided wait, and the six findings

On 2026-07-27, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2 on Ubuntu 26.04, against
`beng/prediction-and-service-classes` at the five commits above `1d62556`.
`go build ./...`, `go vet ./...` and `go test ./... -count=1` all clean over 36
packages, and `go test -race -count=1` clean over `internal/domain`, `internal/lab`,
`internal/orchestrator`, `internal/scenario/...` and `internal/daemon`, `internal/lab`
taking 202s of it.

The root corpus is 58 Blueprints, 55 of them green, with 31 conformance fixtures. Two
Blueprints were added and no fixture moved classification.

Every repair is red against the reading it replaces, mutated back one at a time.

- Charging the whole wait against the class's queue delay again, with the
  latest-answer exemption restored, fails
  `TestAWaitAFamilyHeldIsNotChargedWhenTheFleetTakesOver` with `the member was
  answered "QUEUE_DELAY_EXCEEDED" after seventy minutes its own family held it, and
  what holds it now is one busy machine`, and fails
  `TestOnlyThePartOfAWaitMercatorCausedIsCharged` with `the held member is "closed"
  with outcome "failed", and every second of its wait so far was its own family's
  declared width`.
- The same mutation fails `TestAHeldMemberPastItsDeadlineIsRefusedForItsDeadline` with
  `a member its own family held for a day and a minute was refused
  "QUEUE_DELAY_EXCEEDED", and the only bound its wait broke is its own deadline`,
  which is the second door naming the wrong broken promise.
- Exempting the record on its latest answer in the starvation law instead of dividing
  the wait fails `TestAFleetStarvedWaitIsNotExcusedByASibling`, and the new fixture
  `a-wait-the-fleet-caused-is-not-excused-by-a-sibling` reports `run "sweep-2":
  expected outcome "refuse", and admission recorded
  "compute.run.admission_deferred.v1" with reason "GROUP_AT_PARALLELISM"`.
- Removing the departure on a launch failure from the admission queue leaves
  `internal/lab` and `internal/scenario` entirely green, including
  `a-member-that-gave-its-capacity-back-leaves-room`, and fails only
  `internal/orchestrator`. That is the reviewer's own mutation and it stands: the
  Blueprint that was promoted for the reading cannot state it, which the fixture and
  the coverage list now say.

Two findings were real about their evidence and wrong about the repair they asked for.
Both concerned the class that states no deadline. The review asked for an opportunistic
member held by its own family to be refused at the class's queue delay, which is the
refusal the pass above removed and for the same reason: Mercator had kept it waiting for
capacity for none of that time, and `QUEUE_DELAY_EXCEEDED` is a statement about
Mercator's own promise. The review also held that no invariant anywhere could report
such a member. `liveness.admitted_run_progress` reports any Run of a declared arrival
still open a day into an execution, whatever it is waiting for, and
`TestAFamilyHeldMemberIsStillHeldToProgress` states it over the held opportunistic
record. What was real is that the entry claimed the deadline "still ends the wait"
without disclosing the one class it does not end, and that is disclosed now.

What this pass could not reach, unchanged. The Lab still has no sweep of its own, so
both new fixtures drive the clock themselves, one through a `reconcile` step and one
through explicit advances in its test. Concurrency still has no Blueprint.

The live half ran on this host's own daemon rather than in simulation.
`TestANodeReplicatesAnArtifactFromARealObjectStore`,
`TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth` and
`TestANodeMeasuresTheObjectStorePathItJustCrossed` pass against MinIO containers of the
native engine, and `MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker
-run TestIntegration` passes against real containers. Nothing in this pass needed a
container of its own: what it changes is how admission divides a wait and the laws
stated over it. Mercator issue #165 does not reproduce here and was left alone.

Named and not fixed here, unchanged. `gofmt -l .` reports
`internal/adapter/vast/client.go`, `internal/scheduler/scheduler.go` and
`internal/scheduler/scheduler_test.go`, struct tag alignment left by `595f7b0` and
`1e13518` earlier on this branch.

```text
go build ./... && go vet ./... && go test ./... -count=1
go test -race -count=1 ./internal/domain ./internal/lab ./internal/orchestrator \
  ./internal/scenario/... ./internal/daemon
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration
```

### Phase 4 the group bound under review, and the four findings

On 2026-07-27, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2 on Ubuntu 26.04, against
`beng/prediction-and-service-classes` at the four commits above `0683288`.
`go build ./...`, `go vet ./...` and `go test ./... -count=1` all clean over 36
packages, and `go test -race -count=1` clean over `internal/domain`, `internal/lab`,
`internal/orchestrator`, `internal/scenario/...` and `internal/daemon`, `internal/lab`
taking 202s of it.

The root corpus is 57 Blueprints, 54 of them green, with 30 conformance fixtures. Three
Blueprints were added and no fixture moved classification.

Every repair is red against the reading it replaces, mutated back one at a time.

- Removing the workspace lock from `stepAdmit` fails
  `TestAFamilyBurstSubmittedIsStillHeldToItsWidth` five times out of five with `a family
  declared 1 wide was given capacity for [run_a on off_one run_b on off_one], and every
  member of it asked at the same instant`. The offers are provisionable on purpose: a
  queued Booking on an existing Rental commits through a Rental Schedule whose version is
  checked, so two members competing for one machine would be serialised by that check for
  a reason that has nothing to do with the family, and provisioning mints a fresh Rental
  per Booking.
- Charging a self-imposed wait against the class's queue delay again fails
  `a-family-holds-its-own-members-past-the-queue-bound` with `expected outcome "defer",
  and admission recorded "compute.run.admission_refused.v1" with reason
  "QUEUE_DELAY_EXCEEDED"`, and fails `TestAFamilyNarrowerThanItsClassPatienceStillDrains`
  with `the third member is "closed" with outcome "failed" after waiting 4200s`.
- Removing the new exemption from the first half of
  `liveness.aging_prevents_starvation` instead fails the same conformance case from the
  law rather than from the assertion: `Run "run-member-003" of class "batch" has waited
  1h10m0s, which is past the 3600s its class allows, behind run-member-002`. That is the
  two halves of one law disagreeing, which is what the repair ended.
- Counting executions rather than placements fails
  `a-family-place-is-taken-by-a-member-that-waits-its-turn` with `run "sweep-2": expected
  outcome "defer", and admission recorded nothing at all about this Run waiting`, and
  leaves `a-group-never-runs-wider-than-it-declared`,
  `a-group-of-eight-runs-three-at-a-time`, `a-family-narrower-than-its-class-patience-
  still-drains` and every group law green. That is the reviewer's own mutation and its
  own evidence: the corpus could not see the count before.
- Removing the departure on a launch failure fails
  `TestAMemberThatGaveItsCapacityBackLeavesRoomForItsFamily` with `the second member is
  held by a family whose only other member holds no capacity: [run_first]`, and leaves
  `a-member-that-gave-its-capacity-back-leaves-room` green. That asymmetry is stated
  rather than hidden: under the real control plane the replacement follows in the same
  pass, so a member whose launch failed is either placed again or closed
  `RETRY_EXHAUSTED` before anything else asks, and the moment in between is what a sweep
  interrupted by an error or a restart leaves behind.

One finding was real about its evidence and wrong about the repair it asked for. The
review asked for the population of `holding` to move from the decision to the dispatch,
as the mutation that proves the corpus blind, and that is the reading the count must not
have: a member given a queued Booking is never asked about again, so a family of one
would commit a second machine and then run two. What was missing is the Blueprint, and
the Blueprint is what this pass added.

What this pass could not reach. The Lab has no sweep of its own, so no Blueprint can ask
a held Run a question in the middle of a wait, and the two fixtures that need one drive
the clock themselves. Concurrency has no Blueprint either and the daemon harness cannot
force the interleaving. Both are disclosed in the progress entry rather than implied by a
green corpus.

The live half ran on this host's own daemon rather than in simulation.
`TestANodeReplicatesAnArtifactFromARealObjectStore`,
`TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth`,
`TestANodeMeasuresTheObjectStorePathItJustCrossed` and
`TestAStartBoundRefusesOnlyThePathThisNodeMeasured` pass against MinIO containers of the
native engine, and `MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run
TestIntegration` passes against real containers. Nothing in this pass needed a container
of its own: what it changes is admission's own bookkeeping and the laws stated over it.
Mercator issue #165 does not reproduce here and was left alone.

Named and not fixed here, unchanged from the entries below. `gofmt -l .` reports
`internal/adapter/vast/client.go`, `internal/scheduler/scheduler.go` and
`internal/scheduler/scheduler_test.go`, struct tag alignment left by `595f7b0` and
`1e13518` earlier on this branch, untouched by this pass and still held in another
session's stash against this worktree.

```text
go build ./... && go vet ./... && go test ./... -count=1
go test -race -count=1 ./internal/domain ./internal/lab ./internal/orchestrator \
  ./internal/scenario/... ./internal/daemon
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration
```

### Phase 4 run groups and interruption

On 2026-07-27, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2 on Ubuntu 26.04, so nothing that wants a daemon skipped:

```text
go build ./... && go vet ./... && go test ./... -count=1
go test -race -count=1 ./internal/lab ./internal/orchestrator ./internal/scheduler \
  ./internal/domain ./internal/scenario
cd web/app && bun run typecheck && bun run test && bun run build
```

Both Blueprints were red before and are green after, and each one and each half of
each law was measured by breaking the production behaviour and reading what failed.

- Dropping the family from `WorkloadForRun` fails
  `a-group-never-runs-wider-than-it-declared` six ways: five members reported as
  `admission recorded nothing at all about this Run waiting`, and the Run outside the
  family placed on `rental-1` as a queued Booking rather than on the idle machine. The
  same mutation fails `safety.group_parallelism_respected` on its first half, with
  `Run "run-member-001" was submitted into family "sweep" at a width of 3 and Mercator
  recorded family "" at a width of 0`.
- Stopping admission from asking about the family fails the second half, with `group
  "sweep" declared that 3 of its Runs may hold capacity at once, and at effect 15 it
  held 4`, and fails the daemon case with `run is "requested" waiting for "", want a
  Run queued waiting for "GROUP_AT_PARALLELISM"`.
- Letting the standard class permit interruption fails
  `only-work-that-may-be-interrupted-runs-on-reclaimable-capacity` three ways, on the
  placement and on both halves of the refusal the record should carry.
- Dropping the feasibility refusal fails `safety.interruption_was_permitted` with `Run
  "run-trainer" of class "standard" was running when the capacity it was placed on was
  reclaimed at effect 13`. That is the mutation the law is red for rather than the class
  table itself: the law reads the permission off the class, as the neighbouring laws read
  the queue bounds off it, so editing what a class permits is a change to the contract
  and not a break of it.
- The bound is tight rather than merely respected. The launch ledger of
  `a-group-of-eight-runs-three-at-a-time` peaks at exactly three, in three waves at 0s,
  9m26s and 18m47s, and every one of the eight members ends succeeded.

What this slice could not reach. No production adapter publishes `reclaimable`, so the
live half of the interruption rule is the class refusing a machine a simulated world
sold that way; the daemon case that runs against real capacity is the group bound, over
the public API, the real event log, and two nodes enrolled over the node protocol. A
real-container case for it would prove the container runtime rather than the bound,
which is a seam this slice does not touch, and the fleet harness clears PATH so that
the enrolled node is the only capacity in play.

### Phase 4 the review of the queue review, and the three repairs that stopped short

On 2026-07-27, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine on Ubuntu 26.04, against `beng/prediction-and-service-classes` at
the four commits above `fc3fbdb`. `go build ./...`, `go vet ./...` and `go test ./...
-count=1` all clean over 36 packages, and `go test -race -count=1` clean over
`internal/domain`, `internal/lab`, `internal/orchestrator` and `internal/scenario/...`,
`internal/lab` taking 198s of it.

Every repair is red against the reading it replaces, mutated back one at a time.

- Letting `waitsBegan` move the moment a wait began on a later deferral fails
  `TestAReplacedRunIsHeldToTheDeadlineOfItsWholeWait`, which is a standard Run placed
  15000 seconds into one wait against the 14400 its class states, with a placement and
  a second deferral in the middle of it. Before the repair the law returned nothing on
  that record and `replayQueueDepartures` beside it reported the same wait as 15000
  seconds, which is the two readings that were in one file.
- Dropping the placement from the fleet's answer fails
  `TestAPlacementRevokesTheFleetExemption`, a batch Run measured unholdable at the
  first deferral, given a machine at 600 seconds, refused at its bound at 3601, with an
  interactive Run that had waited nothing admitted at 2000.
- Reading that answer off the latest measurement rather than off all of them fails
  `TestARefusedQueueDelayIsStarvationWhenTheFleetOnceHeldIt`, which is the same shape
  without the placement: the fleet could hold the Run when its wait began and could not
  when it ended.
- Restoring the delete of the wait's own moment on a placement in `applyToQueue` fails
  `TestAPlacementDoesNotRestartTheWaitTheQueueOrdersOn`, which reports the fresh
  standard Run waiting on `NO_FEASIBLE_OFFER` where the batch Run twenty minutes into
  its wait should have held it.
- Deleting the projected-miss branch from `deferOrRefuse` fails
  `a-start-nobody-can-reach-is-refused-at-the-door` in `internal/scenario` with
  `expected outcome "refuse", and admission recorded ... reason "NO_FEASIBLE_OFFER"`.
  The same deletion against `fc3fbdb` left all 36 packages green, which is the coverage
  the reason change vacated.

One existing case changed the world it states rather than the claim it makes.
`TestARefusedQueueDelayIsNotStarvationWhenTheFleetCouldHoldNothing` built its opening
deferral with the generic helper, which records a machine that could hold the Run, and
then asserted the law is silent about a fleet too small. Under the reading that looks
at every answer during the wait that is a fleet which could hold it, so the fixture now
states the world its own comment describes and the case it used to state is the new
deliberate failure beside it.
`TestARefusedQueueDelayIsNotStarvationWhenTheFleetLastSaidItCouldHoldNothing` is
renamed for the reading it exercises.

The live half ran on this host's own daemon rather than in simulation.
`TestANodeReplicatesAnArtifactFromARealObjectStore`,
`TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth`,
`TestANodeMeasuresTheObjectStorePathItJustCrossed` and
`TestAStartBoundRefusesOnlyThePathThisNodeMeasured` pass against MinIO containers of
the native engine, and `MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker
-run TestIntegration` passes against real containers. Nothing in this pass needed a
container of its own: what it changes is the control plane's own record and the laws
stated over it. Mercator issue #165 does not reproduce here and was left alone.

Named and not fixed here, unchanged from the entry below. `gofmt -l .` reports
`internal/adapter/vast/client.go`, `internal/scheduler/scheduler.go` and
`internal/scheduler/scheduler_test.go`, struct tag alignment left by `595f7b0` and
`1e13518` earlier on this branch, untouched by this pass and still held in another
session's stash against this worktree.

The root corpus is 53 Blueprints, 50 of them green, with 26 conformance fixtures. One
green Blueprint was added and no fixture moved classification.

```text
go build ./... && go vet ./... && go test ./... -count=1
go test -race -count=1 ./internal/domain ./internal/lab ./internal/orchestrator \
  ./internal/scenario/...
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration
```

### Phase 4 the queue slice under review, and the five readings it got wrong

On 2026-07-27, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2 on Ubuntu 26.04, against `beng/prediction-and-service-classes`
at the three commits above `b41429b`. `go build ./...`, `go vet ./...` and `go test
./... -count=1` all clean over 36 packages, and `go test -race -count=1` clean over
`internal/domain`, `internal/lab`, `internal/orchestrator` and `internal/scenario/...`,
`internal/lab` taking 198s of it.

Every repair is red against the reading it replaces, mutated back one at a time.

- Naming the deadline at the door on the way to Placement fails
  `TestAnImpossibleAskEmptiesNoFleetUnderTheRealControlPlane` with `the wait ended as
  "DEADLINE_UNREACHABLE" after 17940s, against the 1800s of queue this class allows`,
  and fails `a-machine-that-came-free-too-late-is-not-a-start` in
  `internal/scenario` on the reason its own timeline states.
- Filtering the starvation law's second half on `QUEUE_DELAY_EXCEEDED` again leaves it
  silent on a standard Run refused 17940 seconds into its wait under the other name,
  while an interactive arrival that had waited nothing was admitted 3000 seconds in.
- Dropping the workspace from the adjudication reports `Run "run-quiet" of class
  "batch" was refused after waiting 3601s, and "run-other-tenant" of class
  "interactive" was admitted 1900s into that wait having waited 0s` for two Runs in
  two tenants, and dropping it from the ordering law reports the same shape for an
  opportunistic Run admitted in `ws_beta`.
- Restoring the delete on each Booking Decision reports `"run-watched" of class
  "interactive" was admitted 2000s into that wait having waited 0s` for a Run that had
  been waiting for three thousand seconds and was placed again after a failed launch.
- Reading the exemption off the refusal's own fleet answer reports starvation for
  `run-unholdable`, a Run whose fleet had already answered that no machine it published
  can ever hold it, refused at its bound through the priority door.

The live half ran on this host's own daemon rather than in simulation.
`TestANodeReplicatesAnArtifactFromARealObjectStore`,
`TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth`,
`TestANodeMeasuresTheObjectStorePathItJustCrossed`,
`TestAFloorOnReadingTheDataIsAskedOfWhatThisNodeDelivers` and
`TestAStartBoundRefusesOnlyThePathThisNodeMeasured` all pass against MinIO containers
of the native engine, and both cases of `MERCATOR_DOCKER_INTEGRATION=1 go test
./internal/adapter/docker -run TestIntegration` pass against real containers. Nothing
in this pass needed a container of its own, because what it changes is the control
plane's own record and the laws stated over it. Mercator issue #165 does not reproduce
here and was left alone.

Named and not fixed here. `gofmt -l .` reports `internal/adapter/vast/client.go`,
`internal/scheduler/scheduler.go` and `internal/scheduler/scheduler_test.go`, which are
struct tag alignment left by `595f7b0` and `1e13518` earlier on this branch and are
untouched by this pass. They are not reformatted here because a concurrent session
holds `internal/scheduler/scheduler.go` in a stash against this worktree, and a
whitespace commit under it would conflict for no gain.

The root corpus is 52 Blueprints, 49 of them green, with 26 conformance fixtures. No
fixture moved classification: one green Blueprint states a different refusal reason,
which is the behaviour under repair. That sentence was written as though the change
were costless, and the review of this review established that it was not:
`a-machine-that-came-free-too-late-is-not-a-start` was the only fixture in the tree
asserting `DEADLINE_UNREACHABLE`, so the move left the reason pinned by nothing and the
projected miss in `deferOrRefuse` deletable with the whole suite green. The entry above
this one records the fixture that pins it.

```text
go build ./... && go vet ./... && go test ./... -count=1
go test -race -count=1 ./internal/domain ./internal/lab ./internal/orchestrator \
  ./internal/scenario/...
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration
```

### Phase 4 a decision is added, and a Run that found nothing gets one

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2 on Ubuntu 26.04. `go build ./...`, `go vet ./...` and
`go test ./...` all clean, `go test -race` clean over `internal/orchestrator`,
`internal/scheduler`, `internal/lab`, `internal/scenario`, `internal/httpapi`,
`internal/domain`, `internal/conformance` and `internal/daemon`, and the console's
typecheck, tests and build clean.

The live half ran. `TestANodeReplicatesAnArtifactFromARealObjectStore` stands up
MinIO in a container of this machine's own daemon and passes here, which is what
establishes that this host's Docker is real and that the container-backed
conformance path is available on it. Nothing in this slice needed a container of its
own: what it changes is the control plane's own record, so its highest-fidelity level
is the real daemon over HTTP with a real enrolled node, which is where
`TestARunPlacesOnANodeWithRoomForItAndNotOnOneWithout` now reads the refusal.

The two laws were both shown failing against the code before the fix, one at a time.

Dropping the supersession, by making `runState.supersession` return nothing, fails
`safety.decisions_are_never_rewritten` by name at L1 on the real launch-failure
re-placement: `Run "run-unlucky": decision "dec_c30b5ca387bdf3f61" supersedes "", and
the decision recorded before it was "dec_b392eef53b4daf83a"`. The same mutation fails
the corpus Blueprint on both halves of the claim, once on the predecessor and once on
the reason.

Editing a recorded decision in place, by changing its model version after the
identity was derived, fails `safety.decision_is_reproducible` on the canonical
execution: `carries decision "dec_197f366a3d7d323dd", and re-deriving that decision's
own inputs yields "dec_4e49890b611303c6a"`. Before the scheduler derived its ID
through `domain.BookingDecision.Identity`, that law failed on every real decision in
the tree, which is the same statement from the other side: an ID computed one way and
checked another is a claim about the content that the content does not answer.

Naming the wrong reason for a refused launch, by reporting
`PREVIOUS_DECISION_SELECTED_NOTHING` there, fails
`TestAReplacementNamesTheDecisionItReplaces`.

Dropping the recorded refusal fails two layers at once. The daemon case answers `404
DECISION_NOT_FOUND` from the decision route, which is exactly the hole this closes,
and `an-impossible-ask-empties-no-fleet` reports the Run nothing could place having no
recorded decision to be explained from.

What the corpus Blueprint reported while it was red, before any of the production
behaviour: `expected 1 recorded decisions, and the record holds 0` on the refusal, and
`expected 2 recorded decisions, and the record holds 1` on the answer that replaces
it. Everything else in that fixture, the deferral's reason, the five Bookings it names
as work ahead, the count of machines weighed and the placement six minutes later, was
already green, so what it added is only the two claims.

One consequence that had to be fixed with it, and is worth recording because it is the
kind of thing an appended record breaks. The Run projection decided a Run was queued
by asking whether it had no decision. A Run nothing could place has one now, so that
test reported every queued Run as `requested` again, and the phase asks whether
anything was chosen instead. "Decided" and "placed" are separate questions from this
slice onwards, and `runState.placed` is the one place that difference is stated.

What is not closed. The suppression that keeps a Run from writing a decision every
tick means the recorded refusal is the one from the moment the answer last changed,
not from the latest evaluation: an operator reading a Run that has waited an hour
against an unchanged fleet is reading evidence an hour old, and the deferral beside it
says the same. That is the right trade for the log's size and it is a difference a
reader cannot see, because nothing in the record says the fleet was asked again and
gave the same answer.

### Phase 4 a rung of the ladder may not answer content it does not name

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2. Two independent reviewers refuted the entry below it.
Both raised the same defect through different routes and it was real: the ladder
carried the image into its narrowest key alone, so the two coarse rungs answered
`application_ready` out of launches of other content.

The consequence was not one wrong number on a record. Readiness is the only stage
this fleet measures, so every sample in every history was a readiness sample, and any
candidate with no launch of its own was answered from whatever else its provider had
run: `stagePredictor` feeds the answer into both the score and the established half,
the established half becomes `EstablishedStartSeconds`, and `scheduler.go` strikes out
any candidate whose P90 there exceeds `Placement.MaxP90StartSeconds`. A Run of a
static page in a region where a 70B model server had once taken fifteen minutes was
struck out with `LATENCY_SLO_EXCEEDED` on evidence about the model server. The keys
the record showed named a lane, a provider, and a place, and no image, so neither a
reader nor the invariant could see what the seconds were of.

Fixed at the source. `domain.keyForContent` is now the one place any level of the
hierarchy asks whether the content belongs in the key, `ProviderAndRegion` and
`ProviderKey` take the same question `Candidate` already took, and `levelKeys` asks it
of the stage once and gives every rung the same answer. Content nobody could name has
no key at any level, so a fleet of unresolved manifests does not collapse into one
bucket the way it would have if the coarse keys had grown an `image=` with nothing
after it.

`safety.prediction_states_its_provenance` could not have caught it, which is the
second half of the finding and the reason this is not only a one-line fix. The rule
derived the candidate's own key at the exact-candidate level and returned early at
every other level, so a coarse answer only had to name some key that was not the
listing. It now derives the key at whichever level answered, through `keyOfLevel`, and
refuses an answer whose level this candidate has no key at.

The coverage the entry below promoted could not have caught it either: both its
worlds held one image, so every rung answered the same launch whether or not the key
named it. The placement Blueprint now has a second image and a fourth Run asking the
same six listings about it, and the conformance fixture a second image on all three
Rentals and a third Run of it, so each coarse rung appears twice in a world, once
where it answers and once where it must be silent.

What the new coverage reports when the fix is removed. Building the two coarse keys
without the image again fails `history-answers-for-the-machine-it-was-measured-on`
sixteen ways across four candidates, each of the form `application_ready level: want
"prior", answered at "provider_and_region" from 2 samples`. It also fails twenty-two
cases in `internal/lab` on the invariant rather than on an assertion, of the form `Run
"run-checkpoint-consumer" answered candidate "doomed-rental"'s application_ready stage
out of "lane=reusable;provider=lab", and at level "provider" this candidate is
"lane=reusable;provider=lab;image=sha256:9f2c..."`. All but one of those twenty-two are
fixtures about Artifacts, caches, Run Bundles, or preparation that have nothing to say
about prediction, which is how far the collapse reached: readiness answered out of
another image's launch was in the green corpus everywhere a second Run existed, and no
fixture's expectations had to change when it stopped.

Two deliberate Lab cases hold the tightened rule, one per coarse rung, with every
other stage of the same record at an honest prior so what fails is the rung and not
the company it is in. Both keys they use recur and name no listing; what makes them
violations is the stage they answered.

At unit level the estimator now holds that no rung answers content it does not name
and that the same content on an unmeasured neighbour is still answered at the region
rung, which is what keeps the first rule from being satisfiable by a ladder that
answers nothing.

A second finding was half real and is recorded above, in the entry it refuted: the
region rung's L1 coverage is in the reusable lane, and no production backend states a
region there, so what that fixture holds is the path from the offer to the key and not
a backend stating a place. The entry now says which half it holds and where the other
half is held, and `TestAnEnrolledMachineStatesNoPlaceAndSaysSo` states the production
fact in the package that builds those offers.

Refused: the same finding called `lane=reusable;provider=node` a bucket that is not a
provider, merging every enrolled machine of every provider and region, and asked for it
to be treated as a defect. It is the coarsest rung behaving as designed. That rung is
already "this source, anywhere, of any shape" for a marketplace too, where it merges a
3060 in Poland with an 8xH100 in Texas, and the estimator's own comment says an answer
from it is evidence that this candidate resembles them rather than evidence about this
candidate: it is worth 0.4 at most and 0.2 from one launch, the record names the level
and the key beside the seconds, and the rung above it is skipped rather than faked when
no place is stated. What made the reviewer's example alarming was the collapse this
entry fixes, because the bucket was answering about content nobody had run on any of
those machines. Now that the key names the content, the claim it makes is that this
image came up in this many seconds somewhere in this fleet, which is weak evidence
honestly labelled. The rung stops merging unlike machines when an enrolment carries the
provider and the region of the machine underneath it, which is capacity the node
registry does not model yet and is not something to fake in a key.

```text
go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1
go test -race -count=1 ./internal/lab ./internal/scenario/... ./internal/prediction \
  ./internal/domain ./internal/node/...
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration
```

The live half ran again on this host's own engine, and both Docker integration cases
pass against Engine 29.6.2, including the one holding that a daemon reached twice
through two endpoints is one machine. Nothing here touches the web console, so its
checks were not re-run. Mercator issue #165 does not reproduce on this host and was
left alone.

### Phase 4 the middle rung of the ladder, held through the real control plane

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2. The hierarchical estimator was re-verified end to end
rather than reimplemented, because it had already landed with its two reviews. Four
of the deliberate breaks the entry below records were re-run on this host and each
reported what that entry says it reports, with two figures changed by the slices that
landed on top of it: `History.Predict` short-circuited to the prior now fails the
placement Blueprint twenty ways across five candidates rather than twenty-four,
because the transfer stages the fixture later gained answer the prior in both the
broken and the whole tree, and at L1 the offer-ID break is now reported by
`safety.candidate_identity_recurs` before the provenance rule reaches the same
record. Both rules fail on it; the driver stops at the first.

One thing the estimator's own ladder had no coverage for above the Lab. The three
keyed rungs were held at L0 by the placement Blueprint and directly by the
invariant's deliberate cases, and the L1 conformance asserted the exact candidate and
the provider with nothing between them, because its two Rentals published no region.
That left the rung the region exists to create untested through the control plane's
own path to it, which is a path with steps in it: an offer states the region,
aggregation carries it, the decision records the identity, and the estimator files a
launch under the key. Any of those dropping the field would collapse the region rung
onto the provider rung and no test above a unit test would have said so.

This paragraph claimed more than that when it was written, and a reviewer was right to
refute it. It said the fixture holds an adapter stating the region, and the fixture is
in the reusable lane, where no production backend states one: `internal/node/offers.go`
is the only source of reusable offers today, it deliberately publishes no region, and
`capability.Declare` makes vast, shadeform, runpod, and docker ephemeral-only. So the
first step of that path is held where it exists, in the adapters, in the lane where
they do state a place: `TestTwoSearchesOfOneMachineAreOneCandidate` pins Vast's
geolocation reaching `lane=ephemeral;provider=vast;region=US-CA`,
`TestOneRegionNameInTwoCloudsIsTwoPlaces` pins Shadeform's cloud and region reaching
the same rung as one key, and `TestOneProductInTwoCloudsIsTwoCandidates` pins a RunPod
catalog naming no datacenter having no such rung at all. What the conformance fixture holds is
the offer reaching the recorded identity and the identity reaching the key, in a world
whose reusable backend states a place because the Blueprint schema has always let one.
That is the target ontology this corpus is written against, and the machine half of it
is now stated as a fact of its own: `TestAnEnrolledMachineStatesNoPlaceAndSaysSo`
holds that an enrolled machine publishes no region and that its ladder is therefore
this machine and then every machine this control plane has enrolled, so a reader of
the reusable fixtures is not left thinking a region survives that path in production.

The fixture now states a region on its two Rentals and adds a third in another
region, so the second Run's decision answers at all three levels at L1: the machine
that ran the first Run at the exact candidate, its unmeasured neighbour at the
provider and place, and the machine elsewhere at the provider, all three for the same
forty-five seconds out of the same single launch, at strictly declining confidence.
That the seconds are equal across the three is the point of asserting the level and
the confidence beside them: the seconds alone read identically for a machine measured
and a machine two rungs away from anything measured.

The new coverage fails two ways. Dropping the region from `CandidateIdentityOf`
answers the neighbour at `provider` from a key naming only the lane and the provider,
which is the collapse described above. Moving the far Rental into the measured region
answers it at `provider_and_region`, which holds that the third machine is
discriminated by where it is rather than by being third.

Two further reviewers refuted this entry again, and both were right. It still counted
aggregation among the steps the conformance fixture holds, and the Lab runs no
aggregation at all: `internal/lab/control_plane.go` hands the simulated world to
`orchestrator.New` as the Adapter, where `internal/daemon/runtime.go` hands it the
Broker. So the one production step that rewrites an offer was covered nowhere, and for
that step this entry was wrong in the stronger direction: no test at any level would
have said so, not merely no test above a unit test. The four adapter suites stamp the
adapter type and the lane with their own local `aggregated()` helper rather than through
broker code, and no test in the repository carried a non-empty region through
`broker.AggregateOffers`. Verified by breaking it on this host: dropping the region
inside the rewrite loop left `go test ./... -count=1` completely green.

`TestAggregationCarriesWhatACandidateCanBeLearnedAbout` now holds that step where it
happens. A marketplace ask shaped like the ones Vast publishes, a place and a card and
no machine, goes through `broker.ListOffers`, and the identity derived from what comes
out has to equal the identity derived from what the backend stated, plus the lane and
the provider that aggregation itself stamps. Asserting the derived identity rather than
the fields is deliberate: it holds a rewrite that reprojects the snapshot instead of
mutating it in place, which is the shape the second reviewer proposed as the realistic
way this field gets lost. It fails both ways. Dropping the region in the existing
rewrite loop and reprojecting the snapshot without it each report the ask as learnable
in no place, against a backend that named one. The L1 comment and this entry now say
which steps the fixture holds and where the other two are held, so no reader takes the
conformance corpus for coverage of the Broker.

```text
go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1
go test -race -count=1 ./internal/lab ./internal/scenario/... ./internal/broker
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration
```

The live half ran on this host's own engine: both Docker integration cases pass
against Engine 29.6.2, including the one holding that a daemon reached twice through
two endpoints is one machine, which is the identity half of the estimator against
real hardware. What still has not run live is the learning half, for the reason the
entry below gives: the readiness callback is authenticated per Run and the daemon
fixture configures no reporting. Mercator issue #165 does not reproduce here and was
left alone.

One flake to name rather than to hide, seen once while re-verifying this on the
workstation. `TestTheFleetListingReportsTheRoomThisMachineReallyHas` compares what the
production agent measured against a second reading of the same live filesystem, and it
allows a thousandth of the total for the drift between them. On this host that is 3.4GB
of a 3.5TB disk, and a full `go test ./...` writing a cold build cache moved 7.4GB of
free space between the two readings. It passed three times in isolation and again on a
repeat full run, and it is measuring a quantity that genuinely moves, so nothing was
changed here: the tolerance is a property of that live test and belongs to whoever
tightens it, not to a locality slice touching the Broker.

### Phase 4 a transfer is not a stage a launch history can answer

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2, against the working tree of
`beng/prediction-and-service-classes`. Two independent reviewers refuted the entry
below it. Four of the five findings were real, one was refused with its evidence, and
the central one is that the entry below fixed the wrong half of the collision it
found.

**The estimator was lying, not only the record.** The entry below found the transfer
law reading an assumption's name over seconds an assumption did not produce, and it
deleted the rate. The seconds were the invented half. `stages.Answered` replaces a
transfer's prediction with what measured launches of this candidate spent, with no
regard to what this launch has to move, so a machine holding a verified copy of the
whole forty gigabyte dataset was charged 920 seconds from two launches of itself and
refused: `LATENCY_SLO_EXCEEDED`, `Offered 937.25` against a bound of 180, with its own
`ArtifactEvidence` reading `Locality:hot FetchBytes:0` in the same record. The same
reproduces on the image side, where a host reporting every layer of the image was
charged the pull it had already performed and struck out for it. That inverts the
premise of Artifact locality: a host that holds the data is the reason the data is
worth holding.

A transfer is a byte count over a throughput, and the byte count belongs to the launch
rather than to the candidate: what a host still has to move is whatever it does not
already hold at the moment it is asked. A `CandidateIdentity` names the machine and
the image and can name neither what is resident on the disk now nor which Artifact
versions the Run consumes, so a warm launch and a cold launch of one machine land in
one bucket. So `image_fetch`, `unpack` and `artifact_fetch` are filed nowhere and
answer nothing, `prediction.contentStage` is left holding the one stage it is true of,
and the deleted rate comes back: every stage with bytes to move records the throughput
it was divided by, with no exception, which is the account both
`safety.transfer_rate_is_attributed` and `safety.locality_is_never_infeasibility`
read.

Nothing measured is lost. What recurs about a transfer is the throughput of the path,
and an enrolled node already measures it on the reads it performs and publishes it as
a fact with a validity window, which the inventory's byte count is multiplied by when
the decision is taken. Seconds over a whole stage are that product with both factors
thrown away, and `TestANodeMeasuresTheObjectStorePathItJustCrossed` is that half
against a real store.

**The Lab reported a lawful refusal as a violation, and it needed no clause of its
own.** The second finding was `safety.locality_is_never_infeasibility` failing a host
that could not enumerate its copies and had its fetch answered from history: `charged
920.00s for content nobody could describe, of which only 0.00s was left out of the
established start`, because `pricedSilenceSeconds` multiplies a share of bytes by
seconds those bytes did not produce. Once no transfer's seconds come from anywhere but
bytes and a rate, the share is again a share of the quantity that produced them, and
the same fixture now passes with 640 predicted seconds and 1.25 established. Refusing
the answer outright is what the Lab states instead, in
`safety.prediction_states_its_provenance`, so no other law has to ask first whether
the seconds it is reading are a transfer's seconds.

**A conformance case freed the bytes it was about to measure.** The disk case removed
any container an interrupted earlier run had left behind, and it did so after taking
the reading it compares against. The leftover holds the same half gigabyte the case
writes, so `before` counted those bytes as used, the removal freed them, and the new
write took them again: `a workload wrote 512MiB and the room this node reports fell by
0 bytes`, reproduced on this host by recreating the container with the case's own
`dd`, and green three runs over with the removal hoisted in front of the first
reading. The node's measurement was correct both times. Review's wider point stands
and is now written into the case: the surviving lower bound is an assertion about the
rest of the machine too, in the other direction, and it stays because it is the claim.

**Half of the drain was asked for by nothing.** `Registry.draining` and the
`OpenSession` refusal it gates could both be deleted with `go test ./internal/node
./internal/daemon -count=26` green, which review demonstrated. The guard is not a
race in the production binary: `http.Server` closes its listeners and leaves every
open keep-alive connection usable, and the agent posts events and opens its session
over one `http.Transport`, so a session request arriving on a connection the sweep did
not close begins a fresh long-lived read that `Shutdown` waits out for the whole
fifteen seconds. `TestADrainedRegistryOpensNoFurtherSession` fails with the flag and
the refusal deleted, and `TestADrainEndsTheSessionANodeIsHoldingOpen` fails with the
sweep deleted, both at the object that owns the sessions rather than through an HTTP
server.

**The corpus can now catch the change that brings the defect back.** The fifth finding
was that the transfer-rate change was adjudicated by one Go fixture and by nothing in
the executable specification, and that is true and half of it stays true.
`history-answers-for-the-machine-it-was-measured-on` now asks its two measured
machines what they will spend pulling as well as what they spend coming up, and the
answer is the prior with the throughput it was divided by. Asserting the exact
candidate instead fails on both machines, and asserting a measured path fails on both
rates, so the assertion binds. Nothing in the world produces a timed transfer today,
so it cannot fail on this tree; the moment `Launch.Observations` emits one and the
estimator files it, that Blueprint's third Run reports a machine's own pull history
answering a stage it may not answer. The rest of the finding is answered by giving the
rate law the stage it was silent about: its three clauses are all stated over recorded
rates, so charging seconds and leaving the throughput off the record was a cheaper way
out than inventing a measurement, and `everyTransferNamesItsRate` now fails on each of
the three transfer stages on its own. One hand-stated fixture was charging
`artifact_fetch` while pricing `unpack`, which is a record no decision writes.

What is refused, with its evidence. Nothing here is deferred on the grounds that a
Blueprint could have said it: no Blueprint can, because `Launch.Observations` emits
`application_ready` alone, so no Lab world can produce the timed transfer that would
be answered from. The four unit and Lab cases drive the production scheduler and the
production registry, and the corpus assertion above is what will fail on the day the
world can. The slice that makes it corpus-adjudicable is the one where a node reports
the stages it performs, and it is named in the plan rather than smuggled in here.

The live half ran on this host's own daemon rather than in simulation.
`TestANodeReplicatesAnArtifactFromARealObjectStore`,
`TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth`,
`TestANodeMeasuresTheObjectStorePathItJustCrossed`,
`TestTheDiskANodeReportsIsTheDiskItsWorkloadsGet` and the disk case above all pass
against MinIO containers and busybox writes of the native engine. The full suite is
green three times, `internal/daemon` is green over 25 runs of the package, and the
race detector is green over the packages this pass touched, `internal/lab` at 77s
among them. The root corpus is unchanged at 45 Blueprints, 42 of them green, and no
fixture moved: the one that changed gained assertions and lost none. The console's
generated contract was regenerated, because it is derived from `openapi.json` and the
commit that changed that description had left it behind, so the two clients carried
two wordings of one field.

One failure in this pass was somebody else's. `TestRegistryResolverAgreesWithDockerAboutAPublicImage`
failed once with `toomanyrequests: You have reached your unauthenticated pull rate
limit` from Docker Hub, in a `go test ./...` between two green ones, and passed again
immediately. It is written down rather than folded in: the case compares Mercator's
resolver against `docker manifest inspect` over the public registry, so it depends on
an allowance this host shares with everything else that pulls, and a case that cannot
tell that apart from a resolver defect is its own thing to fix.

Named and not fixed here. The operator console's event stream is still the same shape
of long-lived read as a node session, and `Runtime.Shutdown` still waits for one.
Mercator issue #165 still does not reproduce on this host and was left alone.

```text
go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1
go test -race -count=1 ./internal/prediction ./internal/scheduler ./internal/domain \
  ./internal/lab ./internal/scenario/... ./internal/node ./internal/nodeagent \
  ./internal/daemon ./internal/orchestrator
go test ./internal/daemon -count=25
cd web/app && bun run generate:api && bun run typecheck && bun run test
```

### Phase 4 three defects two reviewers found under the transfer-path pass

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2, against the working tree of
`beng/prediction-and-service-classes`. Two independent reviewers refuted parts of the
entry below it, all three findings were real, and each was fixed at its source rather
than restated. Production code changed in two of the three.

**The daemon was intermittently red, and it was two defects rather than a sighting.**
`go test ./internal/daemon -count=1`, run 48 times against the tree the entry below
describes, failed twice, both at `node_protocol_test.go:105` with `shutdown runtime:
context deadline exceeded`, under two unrelated case names. The same site is where the
sighting the entry wrote down as unreproducible had landed.

The first cause is production. A node session is a long-lived read: the machine holds
the connection open and the control plane writes commands down it, so the request stays
active for as long as the node is healthy. `http.Server.Shutdown` waits for active
requests and cancels none of them, and nothing in the tree ended a session, so the
drain could only finish if the agent's own connection happened to drop first. In the
suite that is a flake. In `mercator serve` it is not a race at all: the binary gives
`Shutdown` fifteen seconds, so a control plane with one enrolled machine burned all
fifteen and then exited 1 on a deadline it could never have met.
`Registry.Drain` now ends every open session and refuses to open another, registered
through `RegisterOnShutdown` so it runs while the drain waits rather than before it or
after it. `TestADaemonDrainsWhileANodeHoldsItsSessionOpen` fails at the deadline
without it and returns in 0.076s with it. A drained node loses nothing, because a
command is durable before it reaches a session. Only the sweep was asked for by
anything: review deleted the refusal and the flag with every package green, and the
entry above is where they are asked for.

The second cause is the case's own bound. `net/http` keeps a connection that was
accepted and has sent nothing out of its quiescent set for five seconds, so a client
slow with its first header is not cut off, and an `http.Transport` dials
speculatively: an agent reporting every twenty milliseconds routinely leaves one
behind. A goroutine dump taken at the failing site showed exactly that, one connection
parked in `conn.serve` on the first `Peek` of a request that never came. Away from
Mercator entirely, a bare `http.Server` with one silent connection fails `Shutdown` at
a two second budget and returns in 3.2s at eight. The cases now give the daemon the
window the production binary gives it. With both fixes, 80 runs of the package are
green.

**The transfer law and the hierarchical estimator contradicted each other.** The
estimator replaces a stage's seconds with what measured launches of this candidate
really spent, `artifact_fetch` included, and the confidence it carries is what those
launches are worth. The record went on stating the offer's link speed beside them, so
`safety.transfer_rate_is_attributed` read an assumption's name over seconds an
assumption did not produce: `candidate "rental-far" priced its artifact_fetch stage
from "assumed_object_store_rate", which nothing on this machine measured, and the
estimate it produced is worth 0.60 where a duration over an unmeasured rate is worth at
most 0.50`. Neither acceptance break of the slice below could reach it, because both
mutate the rate rather than the answer.

This pass concluded the record was the half that was lying and deleted the rate, and
review refuted that: the estimate was lying too, a machine holding every byte was
charged the transfer it performed the last time it held none, and the entry above is
the correction. The reproduced violation quoted here is real and its cause is one
level down. The rate is back, and it is the answer that is refused.

It was invisible because `prediction.Launch.Observations` emits `application_ready`
alone and that stage carries no rate. Nothing pinned that, and the estimator already
declared `artifact_fetch` a content stage, so the collision arrived with the first node
that reported a fetch it timed. That is still the trigger, and what arrives there now
is a transfer priced from its own bytes: the corpus asserts it of the two machines it
has measured, and the entry above says what it took.

**A conformance case asserted the rest of the machine was quiet.**
`TestTheDiskANodeReportsFallsAsItsWorkloadsWriteToIt` asserted that writing 512MiB
moved this host's global docker-root free space by between 400 and 700MiB, which fails
whenever anything else keeps more than 188MiB during the same 0.7 seconds. Reproduced
on demand with one neighbouring container retaining 300MiB chunks: the room fell by
851554304 bytes, and by 1480986624 on a second run. The suite pulls images in another
package beside this one, which is what took a full-suite run down. The lower bound is
what the case is about and stays. Which filesystem the number describes is already
pinned by the total size beside it and by a container's own root in the case before it,
so the upper bound caught nothing the rest of the file does not. What this pass missed
is that the case released half a gigabyte inside its own window, which review
reproduced and the entry above fixes.

The live half ran on this host's own daemon again rather than in simulation.
`TestANodeReplicatesAnArtifactFromARealObjectStore`,
`TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth`,
`TestANodeMeasuresTheObjectStorePathItJustCrossed`,
`TestTheDiskANodeReportsIsTheDiskItsWorkloadsGet` and the disk case above all pass
against MinIO containers and busybox writes of the native engine, so the store, the
presigned reads, the node's own timing and the filesystem it reports are real rather
than scripted. The full suite is green three times, and the race detector is green over
the packages this pass touched, `internal/lab` at 77s among them. The root corpus is
unchanged at 45 Blueprints, 42 of them green: no fixture moved, because no fixture in
the corpus answers a rate-carrying stage out of history.

Named and not fixed here. The operator console's event stream is the same shape of
long-lived read as a node session, and `Runtime.Shutdown` still waits for one: ending
it needs a signal that stops streaming while requests can still write events, which is
its own change with its own regression test. Mercator issue #165 still does not
reproduce on this host and was left alone.

```text
go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1
go test -race -count=1 ./internal/domain ./internal/scheduler ./internal/lab \
  ./internal/scenario/... ./internal/node ./internal/nodeagent ./internal/daemon
```

### Phase 4 the transfer path, held under the slices that landed on top of it

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2, against the working tree of
`beng/prediction-and-service-classes` at `4dead6a`. No code changed. The four entries
above closed the transfer-path slice, and what this pass adds is evidence rather than
behaviour: it is the first run of the slice's own breaks with the hierarchical
estimator, the queue slices and the wait classification sitting on top of it, and the
earlier passes ran against a `git archive` of their commit because a concurrent session
shared the worktree.

Both acceptance breaks were reproduced here.

- The Blueprint is still red under the flat constant. Dropping the fact read from
  `OfferSnapshot.DownloadRate`, so every path answers `AssumedDownloadRate`, fails
  `a-fast-machine-far-from-the-data-loses` with `expected "rental-near-the-data" to
  win, but the decision placed on "rental-far-from-the-data"`, and beside it
  `artifact_fetch_seconds: want exactly 1600, got 640`, `artifact_fetch confidence:
  want 0.9, got 0.5`, `artifact_fetch rate: want 200 Mbps, priced at 500`, and the
  measurement `blueprint_path` recorded as the assumption `assumed_object_store_rate`.
  Five more fixtures fail with it: `a-disowned-fact-is-not-an-answer`,
  `a-floor-on-reading-the-data-is-a-floor-on-delivery`,
  `a-floor-refuses-a-measurement-and-not-a-silence`,
  `a-start-bound-refuses-only-what-it-can-prove` and `silence-is-not-infeasibility`.
- A rate no host reported, presented as measured, is still a violation. Naming
  `nodeagent.ArtifactCopySource` as the measurement on the object-store assumption
  fails `safety.transfer_rate_is_attributed` throughout `internal/lab` with `candidate
  "doomed-rental" priced its artifact_fetch stage at 500.00 Mbps measured by
  "node_artifact_copy", and nothing its publisher stood behind was published about its
  "object_store" path when the decision was taken`.

The live half ran on this host's own daemon rather than in simulation.
`TestANodeReplicatesAnArtifactFromARealObjectStore`,
`TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth`,
`TestANodeMeasuresTheObjectStorePathItJustCrossed`,
`TestAFloorOnReadingTheDataIsAskedOfWhatThisNodeDelivers` and
`TestAStartBoundRefusesOnlyThePathThisNodeMeasured` all pass against MinIO containers
of the native engine, so the store, the presigned reads and the node's own timing are
real. Mercator issue #165 does not reproduce here and was left alone.

Two paragraphs of this entry were refuted by review and are corrected in the entry
below, which is where the reader should go. The failure this pass recorded as an
unreproducible sighting in `internal/daemon` reproduces on this tree at about one run
in twenty-four, and it was two defects rather than none. `go test ./... -count=1` was
not the deterministic gate this entry presents it as either, for a reason in
`internal/nodeagent` rather than the package the sighting was attributed to. And the
two breaks above, which still reproduce, cannot reach the one place this slice's law
and the estimator that landed on top of it contradicted each other.

Open, unchanged, and named in the entries above: whether
`DefaultObjectStoreDownloadMbps` is a pessimistic prior or an optimistic one, whether
0.03 USD a point is the right price for doubt, and a measured unpack rate. All three
are calibration against measurements this tree does not yet hold.

The root corpus is 45 Blueprints, 42 of them green.

```text
go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1
go test -race -count=1 ./internal/domain ./internal/scheduler ./internal/lab \
  ./internal/scenario/... ./internal/adapter/fake ./internal/capability ./internal/node \
  ./internal/nodeagent
```

### Phase 4 a hierarchical estimator keyed on what recurs

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2. A concurrent session held its own slice open across
`internal/scheduler/scheduler.go`, `internal/domain/types.go` and one corpus fixture
in the same worktree throughout, so the full suite, `go vet`, `gofmt` and the race
detector were all run against a `git archive` of the commit rather than against the
shared working directory, and only this slice's own files were staged.

Every stage estimate now names the level its answer came from and the number of
measured launches behind it, keyed on `domain.CandidateIdentity` rather than on
anything a listing was numbered with. `SchedulingInput.LatencyEstimates` is deleted.

- The new Blueprint is red before the estimator and green after. With
  `History.Predict` short-circuited to the prior, it fails twenty-four ways across
  five candidates: `application_ready_seconds: want exactly 30, got 300`,
  `application_ready source: want "history", got "workload.expected_ready"`,
  `application_ready level: want "exact_candidate", answered at "prior" from 0
  samples`, and the sample count beside each. Every level in the fixture is asserted
  by all three.
- Keying the history on the offer snapshot ID, on both the writing and the reading
  side, fails the Blueprint on the second listing of the measured machine with
  `application_ready level: want "exact_candidate", answered at "provider_and_region"
  from 2 samples`, and fails `safety.prediction_states_its_provenance` at L1 with the
  key naming the listing. Filing the history under the Run ID instead fails the L1
  case with the machine answered at the provider level from a key naming nothing but
  the provider.
- Deleting the machine handle from the Vast adapter fails
  `TestTwoSearchesOfOneMachineAreOneCandidate` with `the key names machine ""`, and
  fails `TestTwoMachinesWithOneCardInOnePlaceAreTwoCandidates` with two machines
  sharing one key. `machine_id` was decoded and read by nothing before this: in a
  catalog that is other people's hardware, a region full of identical 4090s is the
  ordinary case, so a fast host and a slow host in one city shared a history and each
  was served back as evidence about the other.
- Each clause of the provenance rule fails on the one record it exists to catch, and
  the counterpart holds that an answered stage and a prior are both honest, because a
  rule that failed those could only be satisfied by a tree that predicts nothing.

Four judgment calls are worth stating rather than hiding.

One stage has an actual today and the record says so about the other seven.
Readiness is bounded by two moments Mercator observes from independent authorities:
the machine states when the process began and the application states when it began
serving. Every other stage happens inside one observed interval, because a provider
reports a machine running from the moment it accepts the launch, and attributing that
interval across seven stages would be arithmetic wearing a measurement's clothes.
Those stages keep the published claims and stated constants they had, now named as
the prior they are, and they become learnable without touching the estimator when a
node reports the stages it performs.

A coarser level beats a published claim. A provider's own boot window is a claim
about a listing and a region's samples are launches somebody watched. The level is
charged for its breadth instead: the confidence declines from 0.9 at the exact
candidate to 0.6 at the provider and region and 0.4 at the provider, and one sample
is worth half of what its level is worth. None of the three is 1, because the next
launch is a draw from a distribution rather than a repeat of its median.

The start is the sum of its stages, always. What was deleted replaced that sum with a
measured start for the offer snapshot ID, which nothing ever wrote and which could
not have been written honestly: a start latency is the sum of seven stages whose
costs depend on what the machine holds now, so the measurement of a machine that
pulled forty gigabytes last week would be served back as the prediction for the same
machine now holding the image.

The history is rebuilt per placement from the Booking Decisions and the Run
projection. A Workspace pays one filtered scan of its decisions and one page walk of
its Runs to place a Run, which is honest and is not free; a projection maintained on
append is worth building when a Workspace's history is long enough for it to show.

The live half ran on this host's own engine.
`MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration`
launches, observes and releases a real container on Docker Engine 29.6.2 and reaches
the same daemon twice, once through the ambient socket and once through a docker
context, holding that the daemon's own ID names one machine where the endpoint label
names two. That is the identity half of this slice against real hardware. What did
not run live is the learning half above the Lab: the readiness callback is
authenticated with a per-run token and the daemon fixture does not configure
reporting, so a fleet case in which a real node's workload reports itself ready and a
second Run is then predicted from it is deferred rather than claimed. Mercator issue
#165 does not reproduce here and was left alone.

```text
go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1
go test -race -count=1 ./internal/prediction ./internal/scheduler ./internal/orchestrator \
  ./internal/lab ./internal/scenario/... ./internal/domain ./internal/adapter/vast \
  ./internal/adapter/fake
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration
```

### Phase 4 the third review of the transfer path

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine. Two reviewers refuted four things about the transfer-path slice.
Three were real, one was two claims of which the gating half is refuted, and the
central one was that the slice's own headline claim was false in the production code
its fixture certified. A concurrent session held its own slice open across
`internal/domain/types.go`, `internal/scheduler/scheduler.go`, `internal/lab/oracle.go`
and `internal/scenario/schema.go` in the same worktree, so the suites below were run
against a `git archive` of the commit rather than against the working tree, and only
this slice's own files were staged.

What the corpus and the laws could not say, and now can.

- A machine nobody measured the path of could be refused capacity. Adding
  `"max_start_latency": "15m"` to `a-fast-machine-far-from-the-data-loses` on the
  reviewed commit reports `rental-nobody-measured-the-path-of`: "expected
  feasible=true, got false (rejections [LATENCY_SLO_EXCEEDED@placement.max_p90_start_seconds])",
  on 640 seconds derived entirely from `DefaultObjectStoreDownloadMbps`.
  `safety.locality_is_never_infeasibility` passed that decision, because the byte count
  was established and the rule measured silence only in unknown-locality bytes.
- Restoring those seconds to the established start, by dropping the measured-path test
  from `establishedOverAMeasuredPath`, fails
  `a-start-bound-refuses-only-what-it-can-prove` with `no feasible offers`, and fails
  `TestAStartBoundRefusesOnlyThePathThisNodeMeasured` against real content out of MinIO
  with "nothing measured this machine's path and a bound struck it out anyway:
  [LATENCY_SLO_EXCEEDED ... Required:12.51 Offered:961.25]". The node had just
  delivered 12787 Mbps of real content onto its own disk, and the machine refused
  beside it had published nothing.
- The Lab law now sees it. `pricedSilenceSeconds` reads the transfer rates the decision
  recorded as well as its localities, and the deliberate case is the third row of
  `TestSilenceIsPricedAndAMeasurementBinds`: a machine that enumerated its copies
  exactly, refused on 640 seconds priced from `assumed_object_store_rate`. The row
  below it changes one field, the provenance of the rate, and is lawful.
- Telling the score an assumed read was certain while leaving the estimate honest is
  no longer green. Stating `artifact_fetch_seconds` as 1 in `scheduler.confidences`
  while `Estimates.Stages.ArtifactFetch.Confidence` stays at 0.5 now fails
  `safety.transfer_rate_is_attributed` through the whole Lab with "the doubt the score
  charged for it is worth 1.00 where a duration over an unmeasured rate is worth at
  most 0.50", on every world that reads an Artifact. Before this it was green across
  the tree except for one unrelated fixture's hard-coded uncertainty.
- The determinism claim the last entry credited to the third machine is not there.
  With `OfferSnapshot.DownloadRate` stripped of its fact read, the three-machine
  fixture reports eleven failures and every one of them names
  `rental-near-the-data` or `rental-far-from-the-data`. The placement falls to
  `rental-far-from-the-data`, which is where it fell before the third machine existed.

Suites. The full `go test ./...` is green on the extracted commit, including the live
half: `TestANodeReplicatesAnArtifactFromARealObjectStore`,
`TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth`,
`TestANodeMeasuresTheObjectStorePathItJustCrossed`,
`TestAFloorOnReadingTheDataIsAskedOfWhatThisNodeDelivers` and
`TestAStartBoundRefusesOnlyThePathThisNodeMeasured` all ran against MinIO containers on
this host's own daemon. `go test -race` is green over `internal/domain`,
`internal/scheduler`, `internal/lab` and `internal/nodeagent`. The regression corpus is
39 Blueprints, 36 of them green.

Not done, and why. `DefaultObjectStoreDownloadMbps` is a flat 500 answering the same
question a node's p10 answers, and nothing has measured whether that is a pessimistic
prior or an optimistic one. A host whose true p10 is under it is ranked worse for
having published it. The uncertainty term does counterweight it and has since the
service class landed, at 0.03 USD a point for a standard Run, so what is open is
whether 500 and 0.03 are the right numbers rather than whether either is charged.
Both belong to the calibration work rather than to a locality slice, and repricing
either here would move every fixture in the corpus against no measurement at all.

### Phase 4 what an unmeasured path costs

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2. This pass closes the transfer-path slice on the machine
nobody measured and on what an admitted assumption may be worth. The full suite, the
race detector over the five packages touched, and the live half all ran on this host
against the working tree.

What the corpus and the laws could not say, and now can.

- Restoring the flat constant, by making `OfferSnapshot.DownloadRate` ignore the facts
  it reads, fails `a-fast-machine-far-from-the-data-loses` on both measured candidates
  and on the placement: "expected `rental-near-the-data` to win, but the decision
  placed on `rental-far-from-the-data`", plus the seconds, the confidence and the rate
  provenance for each. The third machine's own expectations do not move under that
  break, which is what makes them the fallback half rather than a restatement of the
  measured half.
- Raising the object-store assumption's own confidence to 1 in
  `domain.AssumedDownloadRate` fails
  `conformance/a-path-a-host-disowned-is-still-the-path` with "priced its
  artifact_fetch stage from \"assumed_object_store_rate\", which nothing on this
  machine measured, and the rate itself is worth 1.00 where a duration over an
  unmeasured rate is worth at most 0.50". Stamping `objectStoreRead` at 1 while leaving
  the rate honest fails the same world on "the estimate it produced". Both breaks were
  green before this clause existed.
- Each clause of the rule fails on the one record it exists to catch in
  `TestEveryClauseOfTheTransferRateRuleCanFail`, which now carries the two records a
  guess charged as knowledge looks like.

The live half ran, on this host's own daemon rather than in simulation.
`go test ./internal/nodeagent -run TestANodeMeasuresTheObjectStorePathItJustCrossed`
starts MinIO in a container of the native engine, PUTs sixteen megabytes over a
presigned write, has the node stream it back over a presigned read, and then asserts
the node published a real throughput it timed itself, named `node_artifact_copy`, dated
so Mercator may act on it, and that the production scheduler priced the next forty
gigabyte read off that number rather than off the assumption. Dropping
`pathMeasurements.record` from `fetchArtifact` fails it with "the node published [],
and nothing there describes its path to the object store". The two replication cases
beside it, including the wrong-content case, ran on the same daemon. Mercator issue
#165 does not reproduce here and was left alone.

```text
go build ./... && go vet ./... && go test ./... -count=1
go test -race -count=1 ./internal/lab ./internal/scenario/... ./internal/nodeagent \
  ./internal/domain ./internal/scheduler
go test -count=1 ./internal/nodeagent -run 'TestANode|TestACopy'
```

### Phase 4 the second review of the candidate identity

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and this host's own
native Docker Engine 29.6.2. Two reviewers refuted the first review of the identity,
and all six of their findings were real. The key was blind to the execution lane and
to content nobody could name, the rule could not judge two of the three clauses it
advertised, neither simulated world could state the machine or the split inventory the
breaks are about, and two claims in this plan were false. A concurrent session held its own slice
open across `internal/lab/world.go`, `internal/lab/invariant.go` and
`internal/scenario/schema.go` in the same worktree, so every suite below was run
against a `git archive` of the commit rather than against the working tree, and only
this slice's own hunks were staged.

What the worlds could not say, and now can.

- A machine reports its cards the way its probe grouped them. Restoring the
  deduplication fails `safety.candidate_identity_recurs` through the whole control
  plane with "ask-2211 is reusable machine "" on simvast/US-CA/ with 8 cards of
  640000000000 bytes, and ask-2212 is ... 4 cards of 320000000000 bytes", and fails
  `a-candidate-is-what-recurs` on ask-2211's key. Before this, that break failed two
  domain unit tests and nothing else: no Blueprint could state a machine whose
  inventory arrived split, which is the only shape the bug has.
- A simulated machine is named something its lease and its listing are not. Naming
  the machine from the Rental, which is the production defect the derivation exists
  to prevent, now fails the rule on every Blueprint the Lab drives, including the
  generated ones, with "filed candidate "rental-quoted" under machine
  "rental-quoted", and the machine it is is "node-1"". Before this it was green
  everywhere: both worlds used one string as the offer ID, the Rental ID and the
  machine, so the honesty clause had nothing to compare against.
- One product a provider sells in both lanes is two candidates. Dropping the lane
  fails the rule with "ask-2211 is reusable ... and ask-4417-oneshot is ephemeral
  ...", fails the corpus, and fails `TestOneProductInTwoLanesIsTwoCandidates`.
  `capability.Declare` refuses a backend implementing both `NodeRuntime` and
  `EphemeralExecutor`, so this is a world the specification can state and production
  cannot yet reach; it becomes reachable when RunPod's lane migration lands.
- Unknown content has no content key. `registry-silence-has-a-name` states that
  neither Run has one, and without the fix it fails with
  `lane=reusable;provider=fake;machine=node-1;image=` on all four candidates, which
  is every unresolvable image in a fleet sharing one key per machine.
- The rule holds the converse of its own third clause. Letting any provider recur
  fails it on the one-shot pool with "this world publishes nothing about it that
  outlives the listing"; before this, capacity that wrongly acquired a key was never
  judged at all.
- Every clause has a case that fails it, one at a time, in
  `TestEveryClauseOfTheCandidateIdentityRuleCanFail`. The content clause is only
  there: no Blueprint states a world where two registries go silent on one machine.

Two claims in this plan were false and are corrected in place, in the first review's
section above: the break said to prove the rule has bite did not fail it, and the
justification for keeping the node generation out of the key was arithmetic the
codebase contradicts, because `internal/httpapi/nodes.go` hardcodes `Generation: 1`
and nothing in `internal/` increments one. The rejection itself stands on the reason
that a machine which stops and resumes is the same hardware.

The live half ran. `MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker
-run TestIntegration` launches, observes and releases a real container on this host's
engine and reaches the same daemon twice, once through the ambient socket and once
through a docker context, and both cases now check that the key names the engine's
own ID before comparing two keys: an unstamped offer produces no key at all, and two
keys that name nothing are equal. Mercator issue #165 does not reproduce here and was
left alone.

```text
go build ./... && go vet ./... && go test ./... -count=1
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run TestIntegration
cd web/app && bun run typecheck && bun run test
```

### Phase 4 the review of the measured transfer path

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and a real native
Docker daemon. A concurrent session shared the worktree with its own slice in flight
across several of the same files, so the full suite was run against a `git archive` of
the commit under test rather than against the shared working directory. Every claim is
held by a deliberate break that fails it:

- restoring the running floor, so that later transfers re-date the slowest reading,
  fails `TestASlowReadingRetiresWhileTheNodeKeepsWorking` with the reviewers' own
  record: `100 Mbps, sample_count 25, observed_at 2026-07-27T00:00:00Z`, published by
  a node that had measured a gigabit every half hour since noon. Dating the fact by
  the latest transfer instead of the transfer that measured it fails
  `TestANodePublishesTheSlowestTransferItHasSeen` on the date alone;
- adding a ten minute idle lease to `rental-far-from-the-data` in
  `conformance/a-path-somebody-measured-prices-the-read`, which is the reviewers'
  repro, fails the old attribution rule with `candidate
  "rental-far-from-the-data" priced its artifact_fetch stage at 200.00 Mbps measured by
  "blueprint_path", and this world publishes no such machine to have measured it`. The
  lease is now part of the fixture, so the corpus drives a decision past the retirement
  of the machine it was about. `TestARateMeasuredOnCapacitySinceRetiredIsNotAViolation`
  states the same law on its own;
- putting the day-long expiry back on a fixture-declared path fails
  `TestADeclaredPathIsStillPublishedADayLater`, which is the second instance of the
  same defect: past the expiry Mercator reads silence about a path both worlds are
  still crossing at the declared rate;
- replacing the body of `simulatedWorld.linkMbps` with a read of the published-fact
  channel, which is the reviewers' break and which used to leave the suite green,
  fails `conformance/a-path-a-host-disowned-is-still-the-path` with `this world spent
  640.00s reading forty gigabytes over the 200 Mbps path it declared, and it costs
  sixteen hundred`;
- the five clauses of `safety.transfer_rate_is_attributed` are still each driven by a
  record no code in this tree writes, now stated over the publication record rather
  than the standing fleet.

The live half ran again on this host. `TestANodeReplicatesAnArtifactFromARealObjectStore`
and `TestANodeMeasuresTheObjectStorePathItJustCrossed` start MinIO in a container of
this daemon and stream real content over a presigned GET, and the reading the node
publishes is the one it timed over that transfer. Mercator issue #165 was left alone.

One limit is stated rather than hidden. A fixture's `p10_mbps` is one figure for what
this world delivers and what the host publishes, and what separates the two channels
is the confidence the host puts on its own number, which is what the new Blueprint
spends. A world where a machine publishes one positive rate and delivers a different
one is still unstatable, and it stays that way until something needs it: the number a
node measures is delivery end to end, so a host whose disk is slower than its link is
a host with a slower object-store path, stated as one.

```text
go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1
go test -race ./internal/domain ./internal/scheduler ./internal/scenario ./internal/lab \
  ./internal/adapter/fake ./internal/node ./internal/nodeagent ./internal/httpapi ./internal/daemon
```

### Phase 4 transfer rates from a measured path

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and a real native
Docker daemon. A concurrent session shared the worktree throughout and had its own
slice in flight across several of the same files, so every command was run against a
`git archive` of the commit under test rather than against the shared working
directory. Each claim is held by a deliberate break that fails it:

- pinning the scheduler's object-store rate back to `domain.AssumedDownloadRate`
  fails `a-fast-machine-far-from-the-data-loses` five ways per candidate, with
  `artifact_fetch_seconds: want exactly 1600, got 640`, `artifact_fetch confidence:
  want 0.9, got 0.5`, `artifact_fetch rate: want 200 Mbps, priced at 500`, the
  measurement recorded as the assumption, and the placement on
  `rental-far-from-the-data`. That is the state the tree shipped in: two machines
  twenty times apart on the path to their data, priced identically;
- the same break fails `TestAMeasuredPathPricesTheReadAndThenSpendsIt` with `the
  decision placed on "rental-far-from-the-data", and the machine beside the data
  reads the dataset twenty times faster`;
- dropping the Lab world's reading of the Blueprint's paths fails that same case with
  `this world spent 640.00s reading forty gigabytes over a 4 Gbps path, and the path
  says eighty`, which is the tautology the slice exists to remove: the prediction
  still said eighty;
- each of the five clauses of `safety.transfer_rate_is_attributed` is driven by a
  record no code in this tree writes, one case at a time, in
  `TestEveryClauseOfTheTransferRateRuleCanFail`. The counterpart,
  `TestARatePricedFromTheStatedAssumptionIsNotAViolation`, holds that an honest
  assumption passes: nothing measures a host's storage, so every assembly in the
  fleet is priced from Mercator's own constant, and a rule that failed that could
  only be satisfied by claiming measurements the tree does not have;
- raising `minimumMeasuredBytes` past the object's size fails
  `TestANodeMeasuresTheObjectStorePathItJustCrossed` with `the node published [], and
  nothing there describes its path to the object store`, and so does dropping
  `Network` from the reported host facts;
- publishing the latest reading instead of the slowest fails
  `TestANodePublishesTheSlowestTransferItHasSeen` with `the node published 2000 Mbps,
  and the slowest of its three reads was 100`.

The live half ran. `TestANodeMeasuresTheObjectStorePathItJustCrossed` starts MinIO in
a container of this host's own daemon, writes a sixteen-megabyte object, and has the
node fetch it over a real presigned GET: the throughput it publishes is one it
measured over that transfer, and the production scheduler then prices a
forty-gigabyte read off the reported number rather than the assumption. Docker is
native here, so the store, the presigned read, and the node's own timing are all
real. Mercator issue #165 was left alone.

Two limits are worth stating rather than hiding. Each simulated world's constant for
a path no fixture declared is the same figure as the scheduler's assumption, so an
undeclared path still has prediction and actual agreeing by construction; what
separates them is a declaration, which is why the fixture declares one. And
`MeasuredLinkConfidence` is a stated 0.9: a node's own reading is worth more than a
fleet-wide guess and less than certainty, and the figure is an assumption about
assumptions until predicted-versus-actual for the fetch stage can replace it.

```text
go build ./... && go vet ./... && go test ./... -count=1
go test -race ./internal/domain ./internal/scheduler ./internal/scenario ./internal/lab \
  ./internal/adapter/fake ./internal/node ./internal/nodeagent ./internal/httpapi ./internal/daemon
cd web/app && bun run typecheck && bun run test && bun run build
```

### Phase 4 the review of the candidate identity

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and a real native
Docker daemon. Two reviewers refuted the slice that keyed a launch history, and
every claim below is held by a deliberate break that fails it.

- restoring `slices.Compact` over the sorted inventory fails
  `TestTwoSpellingsOfOneCardAreOneProduct` and `TestTwiceTheCardsIsNotOneProduct`,
  which is the reviewers' case that a four-GPU machine and a two-GPU machine keyed as
  one exact candidate. It does not fail `TestOneModelInTwoMemorySizesIsTwoProducts`,
  which this section claimed until the second review measured it: that test holds the
  memory half of the product and is broken by dropping the memory, not by the
  grouping;
- naming the machine from the endpoint label again fails
  `TestTwoDaemonsOnOneBoxAreTwoMachines`, `TestOneDaemonReachedTwoWaysIsOneMachine`,
  and `TestAnUnreachableDaemonNamesNoMachine`, and fails the live case against this
  host's own engine with `lane=ephemeral;provider=docker;machine=loopback` beside
  `lane=ephemeral;provider=docker;machine=mercator-machine-...`, the lane having
  joined the key in the second review;
- naming the machine from the Rental again fails
  `TestTwoMachinesOnOneLeaseOfferTwoMachines` in the node registry and
  `TestTwoMachinesOnOneLeaseAreTwoCandidates` in the domain, which is the case an
  operator reaches by inviting two machines against one rental_id. Since the second
  review it also fails `safety.candidate_identity_recurs` on every Blueprint, which
  it did not when this was written;
- deleting `Region: o.Geolocation` fails `TestTwoSearchesOfOneMachineAreOneCandidate`,
  deleting `InstanceType: cloud + "/" + g.ID` fails
  `TestOneProductInTwoCloudsIsTwoCandidates`, and deleting either Shadeform fact
  fails `TestOneRegionNameInTwoCloudsIsTwoPlaces`. All four lines could be deleted
  with the suite green before this pass;
- dropping the region from either simulated world fails
  `a-candidate-is-what-recurs` on every key that states one and fails
  `TestACandidateIsWhatRecursThroughTheWholeLabWorld` on the ask it states in full;
- dropping the accelerator memory from the product fails
  `safety.candidate_identity_recurs` through the whole control plane, with `ask-4417`
  and `ask-51120` under one name at eight cards each. This is the break that fires the
  rule, and until the second review this section named a different one.

The live half ran. `MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker
-run TestIntegrationOneDaemonReachedTwoWaysIsOneMachine` reaches this host's Docker
Engine 29.6.2 twice, once through the ambient socket and once through a docker
context created for the case, and holds that the daemon's own ID names one machine
where the endpoint label names two. Mercator issue #165 does not reproduce here and
was left alone.

One refutation is rejected. The reviewers asked for the node generation in the key.
It is not there on purpose: the generation exists so a command is never sent to a
runtime that has been replaced, which is fencing, and a machine that stops and resumes
is the same disk and the same hardware, so what it spends pulling and booting is the
same thing to learn about across the boundary. Keying on it would split one machine's
launch history at every stop and resume while nothing about the machine changed, and
what a machine currently holds is read from its live inventory rather than from its
history. The collision the reviewers found in the same finding, two machines on one
lease, is real and is fixed by naming the node.

This paragraph first justified the rejection by claiming that keying on the
generation would leave every launch history one sample long, and the second review
established that the codebase says otherwise. `internal/httpapi/nodes.go` is the only
production construction of a `node.Invitation` and sets `Generation: 1`
unconditionally from a request body with no generation field, and nothing in
`internal/` increments one, so a key carrying it would partition nothing at all
today. On the definition in `internal/node/node.go` it changes on stop and resume, so
a machine running fifty launches between two resumes would hold fifty samples and not
one. The rejection stands on the reason above; the arithmetic it was first argued
from was wrong.

```text
go build ./... && go vet ./... && go test ./... -count=1
```

### Phase 4 the second review of the start moment

On 2026-07-26, on the amd64 Linux workstation, with Go 1.25.11 and a real native
Docker daemon. Each claim is held by a deliberate break that fails it:

- reverting `bookingStartedAt` to adopting any moment that is not nil fails
  `TestANodeWithASkewedClockDoesNotSetMercatorsOwn` with `the record says the Booking
  has 3509.47s of enforced runtime left, and it ran out`, and fails
  `TestABookingClockIsHeldToTheSameLawAsTheRunStream` at the Lab rule;
- copying the node's own `ObservedAt` through `broker.observeOnNode` fails the same
  fleet case with `the Run records a start of 2026-07-26T13:59:23Z, and its machine's
  clock is an hour ahead of the control plane's`;
- deleting the registry's receipt stamp fails
  `TestAReportedWorkloadIsDatedByTheClockMercatorKeeps`, and fails both fleet start
  cases with a 502 from the Broker refusing a report it cannot place on Mercator's
  clock;
- deleting the clause about a moment ahead of its read from
  `adapter.EstablishedStart` fails `a-clock-nobody-shares-is-not-a-start` with
  `records a start moment of 2030-01-01T01:00:20Z, and the fixture says nobody
  observed one`, and the Lab rule keeps failing the same record on its own terms,
  which is the independence the delegation had removed;
- publishing the world's own truth instead of the machine's reading fails
  `TestAHostRunningAheadIsRefusedThroughTheWholeLabWorld` with `the start-latency row
  is sourced "run_stream.execution_started" with 20.00s`;
- dropping the parse error on the node runtime's `State.StartedAt` fails
  `TestARuntimeThatStatesAnUnreadableStartMomentFailsTheRead`, which drives a daemon
  printing Go's default time form;
- dropping the error on the Docker adapter's `Created` moment fails
  `TestContainerFromInspectRefusesAMomentItCannotRead/Created`, which nothing in the
  tree could fail before.

The live half ran. `go test ./internal/nodeagent -run TestTheNodeReportsWhenTheContainerStarted`
and the Docker adapter's integration case were exercised against this host's own
Docker Engine, which is native here rather than behind a VM, so the two moments the
adapter parses are the ones a real daemon printed. Mercator issue #165, the
reachability probe with no timeout, does not reproduce on this host and was
deliberately left alone.

One limit is worth stating rather than hiding. The Booking clock's refusal is driven
end to end only on the reusable lane. Both simulated worlds report running from the
moment a launch is accepted, so the first observation the control plane gets carries
no start moment and the schedule is measured from that read, which is the fallback
the rule has to allow rather than the case it exists to catch. The lane where a start
arrives with the first running observation is the one an enrolled node serves, and
that is where the fleet case drives it.

### Phase 4 the review of the launch waterfall

On 2026-07-26, on the amd64 Linux workstation. Every command was run against a `git
archive` of the commit under test with only this pass's files overlaid, because a
concurrent session shared the worktree and had it non-compiling for stretches of the
work. Each fix is held by a break that fails it:

- restoring the unconditional assignment of `RunRecord.ReadyAt` fails three of the
  four readiness cases in `internal/orchestrator`: a moment an hour ahead of the read
  is recorded, a moment a minute before the container start is recorded, and a second
  report moves a readiness already taken;
- publishing the world's own readiness truth instead of the machine's reading leaves
  `a-clock-nobody-shares-is-not-a-start` unable to say anything about a refused
  readiness, and reverting `Session.RunRecord` to read the report makes the same
  fixture assert that no workload spoke rather than that Mercator refused;
- deleting any one clause of `safety.readiness_is_reported_not_inferred` leaves the
  record it exists to catch passing, which is what
  `TestEveryClauseOfTheReadinessRuleCanFail` drives one case at a time;
- restoring `UnpackBytes: 0` for bytes a host has to fetch fails
  `unpacked-is-not-the-same-as-pulled` with `unpack_seconds: want at least 70, got 0`
  and `unpack confidence: want 0.5, got 1`, and fails
  `TestBothModelsPriceDoubtTheSameWay` on the doubt a cold candidate carries;
- replacing a fixture's `boot` stage assertion with the record's own JSON key
  `boot_seconds` now fails at load with `stage "boot_seconds", which is not one of
  [...]`, where before it silently asserted zero seconds and passed;
- deleting `"stage_seconds": world.stageSeconds(execution)` now fails
  `safety.prediction_is_recorded_against_its_actual`, which it did not before;
- restoring the worlds that always report ready fails
  `a-running-process-is-not-a-serving-one` with `records its application ready at
  2030-01-01T00:00:20Z, and the fixture says it has not said so` and fails
  `TestAWorkloadThatNeverBecomesReadyMeasuresNoReadiness` with a readiness row sourced
  `effect_ledger.launch.stage_seconds` at 0.00s.

The six live Docker cases named in the section below were re-run unskipped on this
host against Docker Engine 29.6.2 with the overlayfs storage driver.

```text
go build ./... && go vet ./... && go test ./... -count=1
```

### Phase 4 the launch waterfall

On 2026-07-26, on the amd64 Linux workstation, the eight-stage record was written
against the world and both Blueprints were promoted in the same change once green.
Each claim is held by a deliberate break that fails it:

- returning nothing from `LaunchSpec.ContainerStartSpend` fails
  `a-launch-is-eight-stages` with `start_latency_seconds: want at least 658, got
  638`. That is the state both worlds shipped in: a container runtime asked for a
  process handed one back in the same instant;
- returning nothing from `LaunchSpec.UnpackSpend` fails it with `want at least
  658, got 628`;
- returning nothing from `ProvisioningSpec.BootSpend` fails it with `want at least
  658, got 298` and, one stage down the waterfall, `records its application ready
  at 2030-01-01T00:07:58Z, and the fixture says it has not said so`. A machine that
  boots instantly is ready four minutes before the fixture says anything can be;
- returning nothing from `LaunchSpec.ApplicationReadySpend` fails it with
  `ready_latency_seconds: want exactly 180, got 0` and with the same premature
  readiness. That is the state the tree shipped in for readiness as a whole: an
  untyped callback nothing keyed on, so a workload was serving the moment its
  process existed;
- dropping one stage out of the world's launch consequence fails
  `safety.prediction_is_recorded_against_its_actual` through the registry's own
  deliberate case: the decision predicted all eight, the ledger reports seven, and
  the unpack the machine really did is a prediction measured against nothing;
- deleting `controlPlane.deliverReadiness` fails
  `TestEveryStageOfALaunchHasAnActual` with `the Run projection carries start
  2030-01-01 00:10:58 +0000 UTC and readiness <nil>`;
- refusing to reduce a readiness report into `RunRecord.ReadyAt` fails
  `TestRunnerVerifiesARealReportedRunAndConfirmedCleanup` with `verdict =
  "failed"` and `ApplicationReadyAt:<nil>`, which is the trial reading
  `PROBE_READY_MOMENT_MISSING` off a probe that ran, reported, and left the last
  stage of its launch unproven;
- pricing the whole image answer as one stage fails
  `TestTheReferenceModelPricesAssemblyTheSameWayProductionDoes` and
  `TestPlacementChargesAssemblyForAnImageTheNodeHasNotUnpacked`, which now assert
  that a machine holding every byte of an unassembled image is charged the assembly
  and no transfer.

Two limits are worth stating rather than hiding.

No new live-container case was added for readiness. The application-ready stage has
no node-side and no provider-side participant by construction: its authority is the
workload posting to the public report endpoint, which is exercised against the real
HTTP server, the real report signer, and the real probe code in
`internal/conformance`. Driving the probe inside a real container would need the
committed probe image built in the test and a daemon whose public URL is reachable
from the Docker bridge, which is the conformance runner's own production
configuration and belongs with the phase 5 provider work rather than with this
slice. What did run live on this machine, against Docker Engine 29.6.2 with the
overlayfs storage driver: `TestTheNodeReportsWhenTheContainerStarted`,
`TestDockerRuntimeReportsTheLayersItUnpacked`,
`TestEveryImageThisDaemonHoldsIsAssembled`,
`TestALaunchThatNeverRunsLeavesNoCacheBehind`,
`TestAContainerThatNeverStartsIsNotACacheThisNodeHolds`, and
`TestTheFleetListingReportsTheRoomThisMachineReallyHas`, none of them skipped. Each
of those either gates on `requireDocker` or is handed
`nodeagent.NewDockerRuntime`, which is what makes the daemon version and the storage
driver mean anything about it.

This list first named `TestANodeReportsTheMomentItsContainerReallyStarted` as well,
and that was wrong. `startFleet` sets `PATH` to an empty directory precisely so the
daemon seeds no local connection, and the case takes no `runningOn` option, so the
runtime under it is `scriptedRuntime` and the start moment it compares is one that
fake fabricated as `time.Now().Add(-scriptedStartDelay)`. The case is worth having
and it holds the end-to-end seam over the public API, but it would pass with the
Docker daemon stopped or uninstalled, so nothing about Docker Engine 29.6.2 or
overlayfs is established by it. The container-start stage's live evidence is
`TestTheNodeReportsWhenTheContainerStarted`, which reads `State.StartedAt` back off a
container this machine's own daemon really ran.

Nothing in production predicts acquisition or agent enrollment, and nothing
measures either. Both are recorded as unpublished, which is honest and is a
prediction of zero seconds for the two stages a fresh machine spends most of its
first minutes in. The hierarchical estimator is what replaces them with a history,
and the actuals it will read now exist.

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/domain ./internal/scheduler ./internal/lab \
  ./internal/scenario ./internal/adapter/fake ./internal/orchestrator \
  ./internal/conformance ./internal/conformanceprobe ./internal/daemon \
  ./internal/nodeagent ./internal/httpapi -count=1
cd web/app && bun run typecheck && bun run test && bun run build
```

### Phase 3 the second review of the service class

On 2026-07-26, on the amd64 Linux workstation, `go build ./...` and `go test
./internal/...` pass, and `bun run typecheck` and `bun run test` pass in
`web/app`. Each fix is held by a break that fails it:

- restoring the empty-fact-list test in `downloadFloorViolations` refuses
  `rental-disowned` with `NETWORK_FACT_UNSATISFIED` while `rental-silent` is
  selected, which fails `a-floor-refuses-a-measurement-and-not-a-silence`,
  `a-link-nobody-measured-is-not-a-slow-link` at L1, and
  `TestADownloadFloorRefusesOnlyWhatWasMeasuredTooSlow`;
- `Confidence: 1` in `applyOfferWorldFacts` places the Run on the machine that
  disowned 5 Gbps, and so does dropping the world's paths altogether, each failing
  `TestADisownedLinkFactBuysWhatSilenceBuysAtL1`. Both mutations left the whole
  tree green before this Blueprint existed;
- deleting the `Unpriced` block in `newSimulatedWorld` places the Run on the
  machine nobody quoted at a rate of zero, failing
  `TestAnUnquotedMachineIsTheLastResortAtL1`. That mutation also left the whole
  tree green before;
- not wiring `migrateStoredRevisionSecrets` leaves `hf_live_SECRETVALUE` in the
  public payload of the fixture database, failing
  `TestOpenMovesAStoredRevisionsSecretsOutOfThePublicPayload`, and reverting the
  door leaves the token in the event
  `TestAStoredRevisionKeepsItsSecretsOutOfThePublicEvent` reads.
### Phase 3 close-out

On 2026-07-25, on ws, an amd64 Linux workstation with 24 cores and Docker Engine
29.6.2, native and healthy. Two sessions were committing to this worktree while
the phase closed, so every command below ran against a copy of one named commit
exported with `git archive` rather than against the working tree, and the numbers
describe that commit rather than anybody's work in progress. The commit is the
one carrying the whole phase apart from the conformance-gate work recorded below,
which was verified on its own and changes no production code.

- `go build ./...` and `go vet ./...`: clean.
- `go test ./... -count=1`: 35 packages ok, exit 0, nothing skipped that this
  machine could have run. Before the gate fix in this close-out the same command
  exited 1 on `internal/ociresolver` with `toomanyrequests` from Docker Hub, and
  ten live cases in `internal/nodeagent` skipped while holding the images they
  needed.
- `go test -race -count=1` over every package this phase touched, which is
  `cmd/mercator`, `cmd/mercator-node`, `internal/adapter/...`, `internal/broker`,
  `internal/capability`, `internal/daemon`, `internal/domain`, `internal/httpapi`,
  `internal/lab`, `internal/node/...`, `internal/nodeagent`,
  `internal/ociresolver`, `internal/orchestrator`, `internal/scenario`,
  `internal/scheduler`, and `internal/storage/sqlite`: all ok, exit 0.
  `internal/lab` takes about 75 seconds under the race detector and is the
  longest.
- The corpus: `corpus: 16 green, 8 target`, with
  `TestOpenCatalogPreservesPlacementClassifications` holding those counts and the
  24 regression Blueprints they come from. Twelve conformance Blueprints run from
  `internal/lab`, beside one demo and one minimized case. No target passed, which
  is what the corpus contract requires until one is promoted deliberately.
- `go generate ./...` and `bun run generate:api` both leave the tree byte
  identical, checked by hashing every file before and after rather than by reading
  a diff, because the worktree was busy.
- The console: `bun install --frozen-lockfile`, `bun run check:react-effects`,
  `bun run typecheck`, `bun run test` (5 files, 12 tests), and `bun run build` all
  pass.

The live half ran, and this is the first time all of it has. On this host, with
this daemon:

- `internal/nodeagent`: `TestEveryImageThisDaemonHoldsIsAssembled`,
  `TestDockerRuntimeReportsTheLayersItUnpacked`,
  `TestTheDiskANodeReportsIsTheDiskItsWorkloadsGet`,
  `TestTheDiskANodeReportsFallsAsItsWorkloadsWriteToIt`,
  `TestTwoWorkspacesGetTwoVolumesForOneCacheName`,
  `TestAContainerThatNeverStartsIsNotACacheThisNodeHolds`,
  `TestANodeReplicatesAnArtifactFromARealObjectStore`,
  `TestACopyThatIsNotTheContentItWasAskedForIsNotWarmth`, and
  `TestANodeReportsNoCopyOfWhatItsOwnWorkloadWrote` all pass against real
  containers, including a MinIO endpoint for the object-store half.
- `internal/ociresolver`: `TestRegistryResolverAuthenticatesAgainstAPrivateRegistry`
  passes against a `registry:2` container behind htpasswd, with the anonymous
  attempt as its control.
- `internal/adapter/docker`: `TestIntegrationDockerAdapterLaunchObserveRelease`
  passes with `MERCATOR_DOCKER_INTEGRATION=1 MERCATOR_DOCKER_IMAGE=busybox:latest`,
  which is the first time it has run on an amd64 host.

Five cases skip in the whole suite, and none of them for a reason this tree can
fix. `TestRegistryResolverAgreesWithDockerAboutAPublicImage` skips because Docker
Hub refuses this address an anonymous manifest read, and no Docker Hub credential
is configured here. `TestConsoleRunsNavigation` and
`TestLabConsoleUsesNormalAPIAndSSE` want `MERCATOR_BROWSER_TEST=1` and a
Playwright install, so CI's Console job is where they run.
`TestIntegrationDockerAdapterLaunchObserveRelease` and
`TestE2EFakeAdapterHTTPAndCLI` are opt-in behind `MERCATOR_DOCKER_INTEGRATION=1`
and `MERCATOR_E2E_FAKE=1`, and both were run on their own here and pass. Eight
further skips are the target Blueprints in `TestPlacementScenarios`, which skip
by design until a phase promotes them, for thirteen `SKIP` lines in a verbose run
of the whole suite. All of this is recorded in
`docs/production/known-limitations.md`.

`TestBuiltIndexReferencesAbsoluteAssets` is a sixth case whose outcome depends on
what ran before it rather than on this machine. It skips while `web/static` holds
nothing but its `.gitkeep`, because the console bundle is embedded at compile
time, and it passes once `bun run build` has populated that directory, which is
the order CI uses and the order the close-out re-run below used. A Go suite run
before the console build reports it as a skip, which is why an earlier draft of
this entry counted it among the environment's gaps. It is not one.

Both gate changes are themselves checked, because a gate that skips too readily
is worse than the failure it replaced.

- `pull` was called on a reference no registry serves and this daemon does not
  hold, with a `t.Fatalf` behind it. The case skipped and the `Fatalf` was never
  reached, so content genuinely absent still stops a case rather than being waved
  through.
- The first version of the public-image gate proved one anonymous manifest read
  and then let three more reads cross the same quota, so it passed and the case
  failed 25 seconds later, which is a flake wearing an environment's clothes. The
  throttle is answered where it appears now, on the resolver's own `ErrThrottled`
  and on each `docker manifest inspect`, and five consecutive runs of the package
  skip that case in 1.4 seconds and pass the private-registry case. A probe
  pointed the same helper at a registry refusing the connection rather than the
  quota, and the case failed as it should.
- Both probes were removed.

Mercator [#165](https://github.com/benngarcia/mercator/issues/165), the
reachability probe with no timeout, was deliberately left alone. It does not
reproduce on this host, because `docker info` answers immediately here, and
smuggling a timeout into a locality slice would hide the regression test it owes.

The whole of it was then run a second time by a different session against commit
`f9f496f`, exported to a directory of its own so no other session could reach it,
and the outcome is the record above with the two skip corrections already applied:

- `go build ./...` and `go vet ./...`: no output, exit 0.
- `go test ./... -count=1`: exit 0, 35 packages `ok`.
- `go test -race -count=1` over `./cmd/...`, `./internal/adapter/...`,
  `./internal/broker/...`, `./internal/capability/...`, `./internal/daemon/...`,
  `./internal/domain/...`, `./internal/httpapi/...`, `./internal/lab/...`,
  `./internal/node/...`, `./internal/nodeagent/...`, `./internal/ociresolver/...`,
  `./internal/orchestrator/...`, `./internal/scenario/...`,
  `./internal/scheduler/...`, `./internal/storage/sqlite/...`, and
  `./internal/workload/...`: exit 0, every package `ok`. `internal/lab` is the
  longest at 74.4 seconds, then `internal/nodeagent` at 15.9 and
  `internal/orchestrator` at 13.2.
- `TestCorpusCoversBothStatuses` logs `corpus: 16 green, 8 target` over the 24
  Blueprints in `internal/scenario/scenarios`, beside 12 in
  `internal/scenario/scenarios/conformance`.
- The ten live cases the phase rests on pass in that default run, against this
  host's own daemon and against `minio/minio` and `registry:2` containers it
  started: image assembly, unpacked layer reporting, both disk cases, both cache
  cases, all three Artifact replication cases, and the private-registry resolver
  case.
- The two opt-in cases pass when asked for: the Docker adapter integration case
  with `MERCATOR_DOCKER_INTEGRATION=1 MERCATOR_DOCKER_IMAGE=busybox:latest`, and
  the fake-adapter end-to-end case with `MERCATOR_E2E_FAKE=1`.
- `go generate ./...` and `bun run generate:api` leave the tree byte identical,
  checked by hashing every tracked file before and after.
- The console: `bun install --frozen-lockfile`, `bun run check:react-effects`,
  `bun run typecheck`, `bun run test` (5 files, 12 tests), and `bun run build`
  (3 artifacts) all pass, and `TestBuiltIndexReferencesAbsoluteAssets` passes once
  that build has run.

CI then failed the branch where this workstation could not, on
`TestAQueuedRunIsPreparedForWithoutWaitingForASweep`, and it was a flake rather
than a regression: the Go job passed on the commit before a documentation-only
change and failed on it. The mechanism was reproduced rather than retried.

A node offer stays selectable for a third of the lease, and the daemon fleet
harness leased its one machine for 900 milliseconds, so 300 milliseconds without a
report is a machine Mercator can say nothing about. Stalling the scripted
runtime's `Facts` call for 500 milliseconds as the second Run arrives fails that
case three runs out of three, with the message CI reported and within a quarter of
a second of its duration. The case gets one trigger, the Booking that names the
machine, and a trigger that lands while the machine is off the catalog states
nothing, because the sweep is what restates it and this case deliberately never
sweeps. A loaded two-core runner executing the whole suite stalls a goroutine past
300 milliseconds with nothing wrong.

The lease is the fleet's own parameter now, 30 seconds by default, and
`TestANodeThatGoesQuietStopsBeingOffered` states the 900 milliseconds it is
measured in. That raises the threshold rather than removing it: the same stall
passes, and a machine genuinely gone for twelve seconds still fails the case. The
case also asserts the Booking before the prefetch, so a Run that was never given
the machine reports that instead of reporting a preparation that never came, and
`awaitPredictedStart` reads both halves of the restraint off the ledger rather
than sleeping a margin from a clock the rule does not use. Both probes were
removed. The daemon package is green 3 times under `-race`, and 3 times pinned to
two cores against 30 spinning processes.

What that leaves standing in production is a real consequence and it is recorded
in `docs/production/known-limitations.md`: a Run queued while its machine's facts
are momentarily stale is prepared for on the next sweep rather than on its own
arrival, so the interval an operator states bounds how often preparation may
begin and the sweep still bounds how late it may be.

CI then failed the Console job three times running, and that one was no flake. It
is the most valuable thing this close-out found, because it is a defect an operator
would have met and no Go test can see.

`TestLabConsoleUsesNormalAPIAndSSE` timed out waiting for the consumer's `Booking
decided` row. The row was in the document and visible the whole time. What was
wrong was the canvas: it positioned every block and every column against
`Date.now()` while every moment it reads comes out of the workspace's own event
stream. Those are two clocks. A Lab execution runs on virtual time in 2030, so the
moment one of its Bookings was queued behind another Run, `workspaceHorizon` was
asked to reach a projected start three and a half years out and `tickMinutes` built
a column per ten minutes of it: 723,040 elements, and a main thread held for
seventy seconds. The feed was not slow, the tab was unusable, and the fifteen
second wait expired inside that freeze.

This host cannot launch the browser those checkpoints need. Playwright's Chromium
wants nine system libraries that are not installed here and installing them is not
this branch's business, so the flow was driven inside a
`mcr.microsoft.com/playwright` container with `--network host`, the host's own
browser cache mounted in, and `mercator lab serve` running the real console on
loopback. That reproduced CI exactly, three times, and it is how each fix was
checked. Anyone can repeat it without a display.

Three things changed, each with its own reason:

- the canvas reads the workspace's clock, which is the moment it last said
  something. In production that is seconds old and nothing moves. In the Lab it is
  the virtual moment every projection on the screen was computed from, which is
  what makes the axis mean anything there at all.
- the horizon is bounded at two days, 289 columns. Every term in it is a difference
  between two clocks, and a renderer that draws whatever that difference says is a
  hazard on its own: a wrong projection, a skewed server clock, or an absurd
  maximum runtime now costs a clipped axis rather than a frozen tab.
- the flow advances until the console shows the Run closed instead of advancing a
  fixed thirty minutes. Thirty was enough while a consumer read nothing and is ten
  minutes short now that reading an Artifact costs what it costs, so the number was
  asserting today's physics as a deadline without saying so.

The vertical proof needed one correction to accept what the world now does.
`queue_vs_fresh_compared` required a `run_now_existing_rental` candidate beside the
fresh one, which is the case where standing capacity has no queue delay to weigh,
and refused the case the checkpoint is named after. It takes either standing
disposition now and still requires the evidence: a standing candidate whose queue
delay was established, and a fresh candidate priced to provision.

That correction uncovered something worse, and it is filed rather than fixed here.
The same Blueprint, the same World Tape and the same policy record
`queue_existing_rental` for that candidate when driven by successive advances and
`run_now_existing_rental` when driven to completion. One world, two answers,
decided by how the caller drove it. ADR 0004 makes determinism the Lab's central
promise, the corpus drives conformance Blueprints in one-minute advances while the
vertical proof drives to completion, and a Run Bundle that depends on its driver is
a record of the driver as well as of the world. The `queue` answer is the honest
one: the consumer is placed when its input becomes durable, which is while the
producer is still on the machine.
[#182](https://github.com/benngarcia/mercator/issues/182) owes the fix and an L1
case driving one Blueprint two ways to the same decision. Widening the checkpoint
made CI green and also stopped CI noticing this, which is why it is written down
here and in known limitations rather than left in a commit message.

### Phase 3 producer affinity, withdrawn under review

On 2026-07-25, on a Linux workstation against Docker Engine 29.6.2 on the
containerd snapshotter. Two independent reviewers refuted the affinity slice and
every load-bearing finding held, so the preference is gone rather than defended.
What was checked before removing it:

- the discount was unreachable. `cmd/mercator-node` sets an artifact root
  unconditionally, so `nodeagent.artifacts` reports an enumerated inventory,
  `internal/node/offers.go` carries it through unchanged, and `internal/node` is
  the only source of reusable-lane offers. Every production offer that can carry a
  Rental identity therefore takes the enumerated branch, where the record was
  deliberately given no say. The only world the discount fired in was the fixture
  knob added with it;
- the node that could have reported an unknown inventory cannot hold a copy
  either. `nodeagent.PrepareArtifact` refuses a runtime with no artifact root
  before touching the network, so the machine a record could have preferred is the
  machine preparation cannot prepare;
- nothing in production writes the record at all. The only assignments to
  `ProducedOnRentalID` were in the two simulators, because no production
  implementation of `orchestrator.ArtifactCatalog` exists;
- the L1 conformance rested on the simulated world filing a verified copy of a
  Run's own output on the host that computed it, which this slice's own live test
  contradicts.

The live half ran, and it is the fact the withdrawal rests on.
`TestANodeReportsNoCopyOfWhatItsOwnWorkloadWrote` starts a MinIO container on
this machine's own daemon and has the production `DockerRuntime` launch a real
busybox workload that generates its output inside its own container. The test then
reads those bytes back out of the running container, which is the only way
anything outside it can see them, and only then asks the node what it holds:
enumerated, and empty. Every byte of that content is on this machine, under the
digest a catalog would name it by, and the node reports no copy of it. Those same
bytes, the ones that came out of the container, are what the object store is
loaded with, so the content that arrives back through `PrepareArtifact` over a
presigned GET and is reported verified is the content the workload wrote. The
upload itself is issued by the test rather than by busybox, which can neither sign
an S3 request nor reach this host's loopback, and it carries the container's own
bytes so that both halves are about one piece of content. What a real node can say
about content its own workload produced is nothing, so a record of where bytes
were written buys a consumer nothing either.

Three deliberate breaks hold that live case, because the first version of it did
not have any and a reviewer was right that it could not fail. Sending the
producer's output to another path fails it with `the workload in
mercator-run-producer-1 never wrote /checkpoint`, so no container, no write, and
no daemon means no assertion. Serving the store bytes the container never held
fails it with an `unverified` replica, so the second half is tied to the first.
And filing a copy of a workload's own output would fail the enumeration check that
sits between them.

Two claims survive the withdrawal, each held by a deliberate break:

- restoring the scheduler's own two-term uncertainty penalty fails
  `TestBothModelsPriceUncertaintyFromTheSameFacts` with `reference scored
  3.433333 and production scored 1.433333`, which is the divergence the dead
  weight was hiding, worth exactly the two facts the oracle counted and the
  scheduler did not;
- restoring the world that kept a verified copy of a Run's own output fails
  `safety.artifact_replica_verified` with `offer "producer-rental" holds a copy of
  Artifact "artifact:checkpoint:v1", which nothing published`, through both
  `TestTheMachineThatWroteTheContentStillReadsTheObjectStore` and
  `TestAConsumerReadsTheCopyAFetchLeftBehind`.

The second of those two cases now names no machine either. It reads the producing
host off the `artifact.written` effect and requires the consumer's selected offer
to be that host, so what is asserted is that one machine both wrote the content
and read the object store rather than that a fixture called something
`producer-rental`. Recording the write against another machine fails it with `the
checkpoint was written on "some-other-machine" and its consumer was placed on
"producer-rental"`. Swapping the two Rentals' rates now moves the assertion to
`doomed-rental`, which is where both the producer and its consumer go in that
world, instead of leaving the sentence exercised by nothing.

What the review leaves open is one gap, now filed as
[#171](https://github.com/benngarcia/mercator/issues/171): a verified replica in a
node's replica store is not reachable from inside the container a Run executes in,
so the zero seconds Placement prices for a host holding a checked copy is a
specification exercised at L1 and not a saving any production workload can
collect. `go test ./...` is green on this host, including the live daemon and
MinIO cases.

The reachability probe's missing timeout, issue #165, is again deliberately
untouched. It does not reproduce on this host, and it has its own regression test
to write.

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

### Phase 3 prewarming, the second review

On 2026-07-25, two more reviewers refuted four things about the prewarming slice
on this host. All four were real, all four are repaired, and one of them was the
Lab world rather than Mercator.

An image's preparation content was not always a digest. Nothing enforced it:
`domain.ValidateWorkloadRevision` only rejects an empty image and
`domain.ReferenceDigest` answers empty for a tag, so `PrepareItem.Content()` was
the empty string for an unpinned reference. Driving `prewarmItemKey` directly, two
Runs wanting `registry.example/trainer:v1` and `registry.example/analyst:v2` on
machine `builder` both produce the key `image:builder/`, so the second image was
dropped from the desired set entirely, the node operation identity
`prepare:image:builder:` was the same for both, and `ImageInventory.Holds("")` is
false so the unidentifiable content was re-asked for on every changed set. A Run
whose image is not digest-pinned is now refused at intake, which is where a Run
commits to bytes. A stored workload revision may still name a tag, because it is a
template and resolution is deferred to run-create, and that decision has its own
case in `internal/workload`. The `httpapi` resolver hook no longer substitutes the
submitted tag when a resolver answers with no reference.

`PrewarmPolicy` was enforced per workspace while the invariant and this plan
stated a fleet-wide bound. Reproduced by copying the rate-bound fixture into a
two-tenant world: each tenant's clock was empty at its own first send, the
per-workspace bound was honoured, and the run aborted with `speculative
preparation started at 2030-01-01T00:05:00Z and again 1m30s later at
2030-01-01T00:06:30Z`. Production had the same shape in the other direction, since
the sweep called `Prewarm` once per workspace and a deployment with N tenants could
begin N transfers per interval. Preparation is now one pass over every tenant: the
desire is ordered across all of them by when the Run waiting for it is projected to
start, truncated once, and a send that names new content moves one clock. While the
bound holds, a desire loses its additions rather than being withheld whole, so a
withdrawal never waits behind an addition it travelled with.

Writing the two-tenant Blueprint caught the Lab world reading one tenant's desired
set as the whole fleet's: it stopped every speculative transfer the request did not
name, so the second tenant's set cancelled the first tenant's transfer and no two
prefetches were ever in flight at once whatever Mercator asked for. That made the
concurrency bound unfailable in the only world where it matters. Withdrawal is now
decided against the union of every tenant's latest set.

The rate clock lived in the process. A restarted Mercator found no last-sent time
and was free to begin a transfer immediately, and a control plane restarting in a
loop would begin one on every boot. What is wanted is derived from the Runs and the
machines every time and stays in process; when preparation last began is one
durable row, recording a decision Mercator made rather than what any machine holds.
`prewarming-holds-its-own-rate-bound` now restarts the control plane when its third
Run is recorded, and with an in-process clock it fails with `speculative preparation
started at 2030-01-01T00:01:00Z and again 2m0s later at 2030-01-01T00:03:00Z`. The
price is stated where the memory is: a restarted Mercator cannot tell content it has
already asked for from content it has not, so it states nothing until the bound
allows a beginning, and a withdrawal it discovers inside that window waits the same
interval.

The bound could not fire in any default production deployment. The only production
caller was the sixty second reconcile sweep and `DefaultPrewarmPolicy.MinInterval`
is thirty seconds, so two sweeps were never closer together than the cadence and
the observed spacing between fetches was the sweep's. Preparation now also runs when
something is recorded that could change what Mercator wants prepared: a Booking that
named a machine, one that was dispatched, a launch a host is getting ready for, a
withdrawal, or a Run whose machine is free again. The orchestrator names those
events because it derives the answer from them, and the daemon subscribes from the
log's head so a restart wakes on what happens next.
`TestAQueuedRunIsPreparedForWithoutWaitingForASweep` drives it through the
production daemon and the real node protocol and sweeps nothing; it fails with the
subscription removed. The sweep stays and is the only timer, because a desire also
changes when a moment passes rather than when anything is recorded: the case in
point is a machine that stops being one Mercator must not disturb when the start its
own decision predicted elapses, and the new case had to wait that moment out before
the Run it is about could be prepared for at all.

Preparing on every Booking made the daemon busier at shutdown and exposed two
things in the shutdown path. Reconciliation and preparation send commands to
machines, and an enrolled node receives one by holding a request open on the same
server the daemon drains, so background work is told to stop before the drain and
joined after it. The node protocol harness then gave shutdown two seconds where the
production entrypoint gives itself fifteen, which asserted something about this
machine's timing rather than about the daemon shutting down cleanly. Three of eight
`-race -count=3` runs of `internal/daemon` failed in cleanup before this, and five
consecutive runs pass after it in the same twenty seven seconds.

What is left. A refused preparation is still terminal, which is the refutation the
previous review accepted and left, and it is still owed a world that can refuse a
fetch. The rate bound is reachable in production now and is stated in the Lab,
where two fixtures fail without it; no L3 case states it, because the fleet harness
serves two images and seeing a bound hold one piece of content back needs three on
one machine. What the new L3 case holds is the trigger: a Run queued through the
production API is prepared for without anything sweeping.

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/capability ./internal/scheduler ./internal/lab \
  ./internal/scenario ./internal/orchestrator ./internal/daemon \
  ./internal/storage/sqlite -count=1
go test ./internal/nodeagent -run TestANodeReplicatesAnArtifactFromARealObjectStore -count=1
```

The last of those is the live half: MinIO in a container of this machine's own
Docker daemon, the node reading one version over a presigned URL the control plane
minted, and the digest recomputed from the bytes that landed. It passes here.
`internal/ociresolver`'s two Docker Hub conformance cases fail on this host with
`429 Too Many Requests` from an unauthenticated pull of `busybox:latest`, which is
the registry rate limiting this address and not this slice.

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
  Artifact locality was scored anywhere. Withdrawing producer affinity moved that
  checkpoint again: the copy it reads is one a fetch put on the machine before the
  world started, beside the whole read the consumer owes on the checkpoint its own
  producer wrote.

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
Withdrawing producer affinity moved it to
`sha256:2a2c9e3d7480a510d905b5af67451f551ce9eb996f561d0666f26c9be6c82db6`, which
is the demo's consumer no longer being warm for content its own producer wrote,
and reading a dataset a fetch had checked there instead.

Three limits are worth stating rather than hiding.

Nothing in production implements `orchestrator.ArtifactCatalog`, so a production
Mercator still refuses a Run that reads an Artifact at the door. What changed is
that the two simulated worlds now both answer as object stores, so the scoring
this slice adds is exercised at L0, in the placement corpus, and at L1 through
the real control plane, and none of it is exercised against a real store.

Nothing yet fetches an Artifact onto a host before a Run needs it. The estimate
prices what a candidate would have to read, and controlled prewarming is the
later slice that acts on it. Producer-consumer soft affinity was the other slice
this paragraph promised, and it was built and withdrawn: no shipped node can be in
the state its discount fired in, which is under "Phase 3 producer affinity,
withdrawn under review".

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

### Phase 3 and 4 fleet answers

On 2026-07-26, on amd64 Linux with a real Docker daemon, so nothing skipped:

```text
go build ./... && go vet ./... && go test ./...
go test -race ./internal/domain ./internal/scheduler ./internal/lab ./internal/scenario \
  ./internal/adapter/fake ./internal/adapter/vast ./internal/adapter/shadeform \
  ./internal/node ./internal/nodeagent ./internal/daemon ./internal/orchestrator \
  ./internal/broker ./internal/capability ./internal/conformance -count=1
cd web/app && bun run typecheck && bun run test && bun run build
```

Each fix answering the third round of review was measured by breaking it and reading
what failed.

- deleting the locality and the confidences from `FleetVerdict` fails
  `TestALocalityThatWentSilentIsADifferentVerdict` with `a machine that stopped saying
  what it holds gave the same verdict as one that said: off_only_machine:
  LATENCY_SLO_EXCEEDED at placement.max_p90_start_seconds`, which is the reviewer's
  own scenario read off the record. The two cases beside it hold the suppression the
  verdict exists for, so the widening cannot be answered by comparing decisions whole;
- reading an unmeasured disk as a shortfall fails
  `a-machine-that-could-not-look-is-not-a-machine-with-no-room` four ways at once:
  the reason, the count of machines that said too little, and both halves of the
  ordering the second Run asserts;
- removing the shape filter from the fake world fails
  `an-ask-nothing-matches-holds-no-queue` with `the machines weighed: want exactly 0,
  got 1`; requiring a weighed machine before a fleet may say it holds nothing fails
  the same case with `no booking decision recorded` for the Run that fits. Both halves
  are load-bearing, which is the point: the world has to be able to answer one ask
  with nothing while answering another with a machine;
- carrying the queue exemption forward through a wait that asked the fleet nothing
  fails `a-wait-the-queue-caused-says-nothing-about-capacity` on the ordering its last
  Run states;
- `safety.a_silence_is_not_an_answer_about_capacity` fails on the record its
  deliberate case builds: one machine refused for a disk nobody measured, and a wait
  claiming no machine in the fleet can ever hold the Run;
- adding `CollectOffers` to the orchestrator's seam failed ten orchestrator cases and
  a green Blueprint before every double that states its own offers stated its own
  census, because Go resolves an embedded method against the embedded value.

What this round could not reach. Neither marketplace adapter has a conformance trial,
because both need credentials and real money, so the corrected offer queries are held
by unit cases against recorded response shapes and by the two Blueprints that state
what an empty answer means. The claim that an empty answer means the shape is not sold
is now true of every adapter in the tree, and nothing yet holds a new adapter to it.

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
