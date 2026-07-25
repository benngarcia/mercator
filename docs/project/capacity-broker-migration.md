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
- `V1`: the Lab's provider seam answers from world state at the moment it is
  asked, which is what the production Broker does when Placement lists offers.
  The separate observed catalog it replaced was written once at construction and
  refreshed by nothing outside a test, so every Lab placement priced a frozen
  world and stamped that frozen answer with the current virtual time. ADR 0004's
  separation is kept where it is load-bearing: Mercator reads only what
  `offerSnapshots` projects and never world state. A provider whose own answer
  is stale is modelled on `ImageInventory.ObservedAt` when a fixture needs it,
  rather than by a lag no fixture asked for.

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
  and leaves it there once the bytes have arrived, which is when the pull's
  simulated duration has elapsed and not when the container was dispatched. The
  Lab records every pull in the effect ledger with what it fetched and what the
  host kept, so a warm start reads back as a pull that moved nothing. This is
  also the first writer of `ImageInventory.ImageDigests`, which makes the
  whole-image fast path in `TransferBytes` live rather than dead.
  `domain.OfferSnapshot.KeepsWhatItRuns` is the single answer to whether content
  survives here, read by both simulators and the Lab invariant so they cannot
  drift: a provisionable offer is a machine that does not exist yet, and an
  ephemeral-lane offer holds nothing once its workload exits.
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
| 3 | Exact OCI and artifact locality; prefetch; producer affinity | image inventory and execution-driven warming done at L1 and against a real node; artifacts, caches, and prefetch remain |
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
- `safety.locality_provenance` (Lab invariant): every digest a host holds is
  either seeded by the World Tape or retained by an accepted `image.pull`
  against that same host, and only capacity Mercator keeps holds anything beyond
  its seed. It says nothing about a host holding less than before: locality
  decays, and a machine that lost what it held is a fact the World Tape must be
  able to state.

The corpus is 16 regression Blueprints: 7 green and 9 target, beside one demo,
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

Neither simulator can construct a standing offer in the ephemeral lane, so the
lane half of `KeepsWhatItRuns` is unexercised by the corpus. That quadrant is
real in production: the local Docker host is standing capacity in the ephemeral
lane until an agent enrols it, and it reports `Images.Known: false` because
nothing there can enumerate what it holds. Making a fixture that constructs one
is what the corpus still owes this guard.

Neither simulator models a pull that fails, is throttled, or half-completes, and
no fixture holds back a provider observation. The Lab world answers `ListOffers`
from its own state at the moment it is asked, which is what the production
Broker does; a provider whose own facts are stale is a fidelity gap, and the
place it would be modelled is the age already carried on
`ImageInventory.ObservedAt`.

## Verification evidence

### Phase 3 warming

On 2026-07-24, both new Blueprints were red before the world changed, each on
exactly one assertion (`pull_seconds: want exactly 0, got 289.14`), and green
after, at which point `TestPlacementScenarios` failed them for passing and they
were promoted in the same commit.

Two independent reviews then refuted parts of that commit, and the claims it
made were re-established by deliberate breaks rather than by argument:

- changing `running-warms-the-host`'s advance from `15m` to `1s` fails it with
  `pull_seconds: want exactly 0, got 289.14`. The host holds the image when its
  bytes have arrived, not when the container was dispatched;
- deleting the `KeepsWhatItRuns` guard from the fake world's pull fails
  `ephemeral-execution-holds-nothing` with
  `one-shot-second: pull_seconds: want at least 200, got 0`, and fails
  `TestWorldCapacityItDoesNotKeepHoldsNothingItRan`. Marketplace offers and
  Rentals are now the same kind of entry in that world, so the guard is the only
  thing separating them and it is reachable;
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
down. `TestLocalityProvenanceAllowsAHostToLoseWhatItHeld` pins the new answer.

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
