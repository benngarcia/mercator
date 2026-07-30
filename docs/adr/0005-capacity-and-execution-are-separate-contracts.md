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
satisfies. Capacity plus a node runtime is the reusable lane. Anything else is
ephemeral. Capacity without a node runtime is refused outright, because nothing
could execute a second workload on it. A backend implementing both `NodeRuntime`
and `EphemeralExecutor` is refused, because that claims one backend both
controls and does not control its host runtime.

`domain.ExecutionLane` carries the answer onto every offer. It is orthogonal to
`OfferKind`: Kind says who owns the host, Lane says whether a second workload
can run there. A standing Docker host with an enrolled node and a provisioned VM
with an enrolled node are both reusable; a provider-native one-shot container is
ephemeral however it was allocated.

The Broker stamps the lane during aggregation from the negotiated Declaration
and clears the Rental identity from offers that cannot become Rentals. An
adapter never states its own lane on an offer.

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

Docker joins the reusable lane when a node agent enrolls on the host. Shadeform
and Vast join it when they provision capacity an agent enrolls on, which is
phases 2 and 5 of #155. `internal/providers` has a standing test that fails the
moment a backend claims reuse without a `NodeRuntime`, so promotion is a
deliberate act rather than a drift.

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

Defaulting an unstated lane to ephemeral would have been safe but silent.
Placement rejects the offer instead, so a producer that forgets is a loud
failure.

## Non-goals

This ADR does not implement the node agent, the node protocol, locality,
prediction, service classes, or provider bootstrap. It establishes the
boundaries those land behind.
