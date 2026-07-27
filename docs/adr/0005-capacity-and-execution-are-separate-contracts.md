# ADR 0005: Capacity And Execution Are Separate Contracts

Status: Accepted

Date: 2026-07-24

Tracking issue:
[#155](https://github.com/benngarcia/mercator/issues/155)

## Context

`adapter.Provider` was one interface doing two jobs. `ListOffers` and
`Terminate` are about allocating and destroying machine capacity. `Launch`,
`Observe`, and `Release` are about executing a workload. Every backend had to
implement all of it, so the type system could not tell the difference between a
machine Mercator holds and a container a provider runs once.

That conflation shows up in three places.

The Broker stamped a Rental identity onto every standing offer, so a local
Docker daemon became a "Rental" although Mercator controls no runtime on that
host between Runs. `CONTEXT.md` defines a Rental as reusable machine capacity
whose lifecycle Mercator owns, which was not true of anything in the code.

RunPod, Shadeform, and Vast each provision an instance, run one workload, and
destroy it. Nothing in the offer, the decision, or the event log said so. A
reader of a Booking Decision could not tell whether the selected capacity would
exist a minute after the Run ended.

Placement could queue a Booking behind a candidate that will not exist by the
time the queue drains, because queueing was gated on `OfferKind`, which answers
who owns the host rather than whether anything survives the workload.

The migration in #155 depends on this distinction being real: locality,
prewarming, run groups, owned-capacity economics, and replanning are all
statements about capacity that persists.

## Decision

Three contracts replace `adapter.Provider`, in `internal/capability`.

`CapacityProvider` allocates and holds machine capacity: list, provision,
observe, start, stop, terminate, and list owned. It knows nothing about
workloads.

`NodeRuntime` executes successive workloads on capacity Mercator controls
through an enrolled agent: enroll, report facts, prepare images and artifacts,
launch, observe, stop, and reconcile after either side restarts.

`EphemeralExecutor` runs one workload on a provider-native execution product.
Its methods are the previous provider seam unchanged, because that seam was
always describing one-shot execution. What changes is that it now names one
lane instead of standing for every backend Mercator has.

### A lane is evidence, not a claim

`capability.Declare` derives a backend's lane from the contracts it actually
satisfies. A `CapacityProvider` allocates and holds machines, which is capacity
that outlives the workload run on it, so that is the reusable lane. An
`EphemeralExecutor` sells one execution that holds nothing afterwards, which is
the ephemeral one. A backend implementing both `NodeRuntime` and
`EphemeralExecutor` is refused, because that claims one backend both controls and
does not control its host runtime.

A backend implementing both `CapacityProvider` and `EphemeralExecutor` is
refused as well. One lane is stamped on every offer a connection publishes, so a
connection answering both `ListCapacity` and `ListOffers` would publish machines
and one-shot executions under one word, and nothing downstream could say which of
the two an offer came from. A provider that sells both is two connections.

There is no second condition on the reusable lane, and an earlier revision of
this ADR had one that did not work. It required a `NodeRuntime`, first on the
same Go value, which no provider adapter can be, and then anywhere in the
deployment, which every deployment has: `daemon.New` always builds a node
registry, so the check was satisfied by a registry object existing rather than by
an agent existing, and it refused nothing while licensing a Rental identity for
machines Mercator had not allocated. Whether a workload can run on one machine is
that machine's own fact, and the agent enrolled on it is what establishes it.
That is a per-machine claim and the reason it is made where offers are published
rather than where a lane is declared.

### A lane is not a licence to place work

The offers Placement chooses among are the enrolled nodes' own, published by the
node registry from the enrollment: the Rental the invitation named, and the
container runtime, idempotent launch, free capacity, image inventory and disk the
agent itself reported.

A capacity connection publishes no candidate. What `ListCapacity` returns is
capacity to acquire, and acting on that selection means provisioning a Rental and
bootstrapping an agent onto it, which
[#200](https://github.com/benngarcia/mercator/issues/200) builds.

Publishing it before then had no correct outcome: stated as completely as
a provider honestly can, the offer is struck out of every placement with
`UNKNOWN_FACT container.max_containers` and pollutes every decision record with a
candidate that can never be feasible; stated with the container facts filled in,
Placement selects it, records `disposition:run_now_existing_rental` for a machine
nobody rented, and the launch fails because the offer's `NativeRef` resolves to no
enrolled node. A machine that does have an agent on it is published by the
registry already, so publishing the provider's copy beside it counted one host
twice under two Rental identities.

### A Rental identity is Mercator's to mint

`StampLane` clears whatever `rental_id` an adapter stated, in every lane. A
Rental is Mercator's own lease record, and the offers that carry one are the
enrolled nodes', minted from the invitation. An adapter populating the field from
its instance type or its contract id would otherwise publish a Rental Mercator
does not hold on the public offer route, and a Booking bound to it would let a
second Run queue behind a lease that never existed.

Aggregation mints none either. It used to mint one for a standing offer in the
reusable lane, which is `OfferKind` answering a question it does not answer:
Kind says who owns the host, so a marketplace listing of somebody else's idle
machine is standing, and a Booking bound to it accumulated Warmth and a queue
against capacity nobody had allocated.

`domain.ExecutionLane` carries the answer onto every offer. It is orthogonal to
`OfferKind`: Kind says who owns the host, Lane says whether a second workload
can run there. A standing Docker host with an enrolled node and a provisioned VM
with an enrolled node are both reusable; a provider-native one-shot container is
ephemeral however it was allocated.

The Broker stamps the lane during aggregation from the negotiated Declaration.
An adapter never states its own lane on an offer, and never its own Rental
identity.

### Placement acts on the lane

An offer that does not state its lane is infeasible with `UNKNOWN_FACT`. There
is no default, because a silent default is exactly how the previous conflation
survived.

Nothing queues behind ephemeral capacity. A selected ephemeral candidate records
the `launch_ephemeral` disposition and reason code `LAUNCH_EPHEMERAL` rather
than claiming a Rental was reused, queued on, or provisioned. Its binding is
single-use: it never joins a schedule another Run could later wait behind.

The Lab invariant `safety.ephemeral_capacity_not_reused` reads the recorded
Booking Decisions back and fails if a Run was ever queued behind one-shot
capacity, or if capacity held for a one-shot execution accumulated more than one
Booking.

### Every current backend is in the ephemeral lane

Docker, RunPod, Shadeform, and Vast all declare ephemeral, because that is what
they do today: each launch creates capacity for one workload and destroys it
afterwards.

Docker joins the reusable lane when a node agent enrolls on the host, at which
point the machine is published by the node registry rather than by the docker
connection. Shadeform and Vast declare the reusable lane when they implement
`CapacityProvider`, which is phase 5 of #155, and the machines they allocate
become placement candidates when an agent enrolls on one. `internal/providers` has
a standing test that fails the moment a backend claims reuse while allocating no
capacity to reuse, so promotion is a deliberate act rather than a drift.

## Consequences

`internal/capability` owns the three contracts, capability negotiation, and the
capacity and node vocabularies. `internal/adapter` keeps the ephemeral lane's
wire types, the shared error sentinels, and the provider-failure
classification; moving those 458 references would have buried the contract
split in a rename.

`broker.Backend` is what a connection resolves to. Callers ask it for the lane
they need and get a typed `ErrCapabilityUnsupported` when the connection cannot
serve it, instead of type-asserting at the call site.

`OfferSnapshot.lane` is a required field on the public API and the console
contract. The console derives Rentals only from reusable capacity, so a one-shot
execution never appears in the fleet as a machine.

The Blueprint contract gains `lane` on marketplace offers, defaulting to
reusable so the existing corpus keeps meaning what it meant. A scenario about
the one-shot lane says so explicitly.

`CandidateExpectation` gains `disposition`, so a scenario can assert what
Placement recorded a candidate as rather than inferring it from the winner.

### What is still conflated

An ephemeral execution still commits a Booking against a single-use Rental
identity, because the Booking is how the orchestrator's placement commit,
schedule store, and projections bind a Run to capacity. The lane makes that
binding single-use and unqueueable, and the audit trail says `launch_ephemeral`,
but the record type is still shared. Phase 2 introduces the Node, at which point
a reusable Booking binds a Run to a Node and the ephemeral binding becomes its
own record. This is tracked in #155 and stated in known limitations.

`internal/scheduler` still owns Placement, which `CONTEXT.md` says is a distinct
activity from scheduling. That rename is [#129](https://github.com/benngarcia/mercator/issues/129)
and stays out of this change.

## Rejected alternatives

Adding an `is_reusable` flag to the existing `Provider` interface would have let
any adapter assert reuse without implementing anything that performs it, which
is the failure this ADR exists to prevent.

Moving the ephemeral wire types into `internal/capability` would have produced a
correct package boundary at the cost of a 74-file rename in the same commit as
the behavior change, making the split unreviewable.

Deriving the lane from `OfferKind` would have collapsed two orthogonal
questions. A standing pool and a reusable machine are both "standing"; a
one-shot pod and a durable VM are both "provisionable".

Letting adapters stamp their own lane onto offers would have made the lane a
claim again. Declaration happens once, at the Broker, from the contracts the
implementation satisfies.

Having a `CapacityProvider` state the container runtime, idempotent launch and
free capacity of the machines it lists, so its offers could be placed on today,
would cross the authority boundary this ADR draws: the provider owns allocation
and provider facts, and the node owns inventory and container lifecycle. A
provider does not run the container runtime it would be asserting, and on a
machine with no agent there is none to assert.

Defaulting an unstated lane to ephemeral would have been safe but silent.
Placement rejects the offer instead, so a producer that forgets is a loud
failure.

## Non-goals

This ADR does not implement the node agent, the node protocol, locality,
prediction, service classes, or provider bootstrap. It establishes the
boundaries those land behind.
