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
- [x] 2026-07-25: Make a node report what it established rather than what it
  could list, and withdraw the four capabilities it declared and does not
  perform. `internal/nodeagent/docker.go` hardcoded `Unpacked: true` and
  `State: hot` for every image `docker images --digests` returned, so a machine
  holding content it cannot start a container on was priced an instant start,
  and partial reuse was invisible on any real node. Being listed says the
  content arrived; only the storage driver's own layer chain says the image is
  assembled. The runtime now reads that chain beside `RootFS.Layers` in one
  `docker image inspect`, reports only the layers it can mount, and states hot,
  partial, cold, or unknown for what it found. The chain is ordered from the
  base up exactly as the config orders diff IDs, so a chain of n directories is
  the image's first n layers and no others.
  - `domain.ImageInventory` gained `PulledImageDigests` and `ValidUntil`, and
    `TransferBytes` became `StartWork`, which answers with two kinds of work and
    a `domain.LocalityState`. Fetching and unpacking are different work over
    different resources: a host that fetched an image and never assembled it
    owes local assembly at `AssumedUnpackMBps` and no transfer, and charging it
    a pull would bill the network twice for bytes already on the disk while
    sending an operator after a problem that is not there.
  - The decision records which of the four states each candidate was found in.
    Only the control plane can state it: the host says what it holds, the
    manifest says what the image is, and the answer is the subtraction.
    `capability.LocalityState` is gone, because two vocabularies for one answer
    is how they drift.
  - `ObservedAt` said the age of an inventory was material and nothing read it.
    A holder now states how long it stands behind what it enumerated, and past
    that moment its answer is `inventory_stale` rather than warmth. The node
    stands behind its enumeration exactly as long as the offer built from it.
  - `node.Registry.NodeSupport` declared ArtifactReplicas, CacheMounts, Prewarm,
    and GarbageCollection true while the Docker runtime implemented none of
    them, which is the failure ADR 0005 exists to prevent one layer down: a
    negotiated capability set is a promise Placement routes work against. Each
    becomes true again in the slice that earns it.
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
| 3 | Exact OCI and artifact locality; prefetch; producer affinity | image inventory, execution-driven warming, registry manifest resolution, and exact node-side reporting done at L1 and against a real daemon; artifacts, caches, and prefetch remain |
| 4 | Candidate prediction, service classes, owned economics, replanning | not started |
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
- `unpacked-is-not-the-same-as-pulled` (green): four machines hold every byte of
  one 18.04GB image and are not in the same condition. The one that assembled it
  wins at zero seconds; the one that fetched it and never unpacked it is
  recorded partial and priced the assembly it still owes, which is what stops a
  machine sitting on the image from being priced either an instant start or a
  fresh pull. The last two differ only in how long they stand behind what they
  said, so half an hour in one is `inventory_stale` and unknown while the other
  is still hot. No new Lab invariant: what a node says it holds is an
  observation, and the rule that matters is `safety.locality_provenance`, which
  now reads unassembled content too.
- `safety.locality_provenance` (Lab invariant): every digest a host holds is
  either seeded by the World Tape or recorded as retained there by an
  `image.retained` effect, and only capacity Mercator keeps holds anything beyond
  its seed. Retention is written when the bytes land, so a host that holds
  content nothing has delivered fails the rule. It says nothing about a host
  holding less than before: locality decays, and a machine that lost what it held
  is a fact the World Tape must be able to state.

The corpus is 20 regression Blueprints: 11 green and 9 target, beside one demo,
one minimized case, and one conformance Blueprint.

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

### Phase 3 exact node reporting

On 2026-07-25, `unpacked-is-not-the-same-as-pulled` was written against the
world and promoted in the same change once green. Each claim it makes is held by
a deliberate break that fails it:

- reporting every layer the config declares rather than every layer the storage
  driver can mount fails `TestDockerRuntimeSeparatesWhatItUnpackedFromWhatItPulled`
  three ways at once, starting with `an image missing part of its chain was
  reported ... Unpacked:true State:hot, want partial`;
- projecting a whole-image identity without requiring `Unpacked`, and dropping
  the pulled projection, fails `TestANodeOffersTheContentItActuallyHolds` on both
  new cases with `pulled and not assembled = false, want true`, and fails the
  daemon case with `the node never reported the image it fetched and never
  assembled`;
- pricing content a host pulled and never assembled at nothing fails the
  Blueprint on four assertions at once, starting with `expected "ready-host" to
  win, but the decision placed on "pulled-host"` and including `image_locality:
  want "partial", got "hot"`, and fails
  `TestWhatANodeHoldsDecidesWhatItStillHasToDo` on the two assembly cases;
- letting an inventory answer forever fails the Blueprint with `pull_source:
  want "inventory_stale", got "image_inventory"` and `image_locality: want
  "unknown", got "hot"` for the host that looked once, and fails
  `TestANodeStopsStandingBehindWhatItSaidWhenItStopsLooking`;
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
  hot and unpacked;
- `TestEveryImageThisDaemonHoldsIsAssembled` is the rule's other half. Every
  image this daemon lists is one it can run, so every one must come back hot: a
  readiness test that called a working host partial would price local assembly
  nobody owes. Checked by hand across the 12 images on this machine, the mount
  chain depth equals `len(.RootFS.Layers)` for every one.

Two limits are worth stating rather than hiding.

The daemon is the only authority on its own storage, and the agent does not stat
it. A node agent may run beside a daemon whose filesystem it cannot see, which is
true of every Docker Desktop and OrbStack install, so statting the paths
`GraphDriver.Data` names would report every image on such a host as unassembled.
What the runtime reads is the daemon's own account of the chain it can mount,
which is exactly the evidence a graph-driver daemon and a content-store daemon
both offer: the latter reports no chain at all for an image it has fetched and
not unpacked, which is where the state routinely occurs. Constructing a broken
chain on a live daemon requires writing inside its storage root, so the partial
and cold arms are driven by a scripted daemon rather than by damaging the one on
this machine.

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
  `ephemeral-execution-holds-nothing` with
  `one-shot-second: pull_seconds: want at least 200, got 0`. Deleting only its
  lane term fails `unenrolled-host-holds-nothing` with
  `borrowed-second: pull_seconds: want at least 200, got 0`, and deleting only
  its kind term fails `TestOneShotCapacityKeepsNothingItPulled`, so both halves
  of the predicate are load-bearing. The lane break also fails
  `TestABorrowedSlotIsPricedTheWholePullEveryTime` at L1 with `run-borrowed-second
  was priced 0.00s of pull on capacity Mercator keeps nothing on`;
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
