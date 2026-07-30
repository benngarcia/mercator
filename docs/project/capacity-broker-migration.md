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

## Progress

- [x] 2026-07-24: Audit the conflated provider seam, the four production
  adapters, Broker aggregation and stamping, Placement disposition logic, the
  orchestrator commit path, and the Lab's simulated world.
- [x] 2026-07-24: Approve the lane ontology and the three contracts. ADR 0005.
- [x] 2026-07-24: Create tracking issue #155.
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
| 3 | Exact OCI and artifact locality; prefetch; producer affinity | not started |
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

The corpus is 14 regression Blueprints: 5 green and 9 target.

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

## Verification evidence

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
