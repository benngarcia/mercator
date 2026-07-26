# Mercator

Mercator is a compute broker and fleet manager: it places Runs on the warmest
capacity a workspace controls, rents more when none fits, and records every
decision and lifecycle.

## Language

**Run**:
A unit of work Mercator places and drives to a terminal outcome on external capacity.
_Avoid_: Job, task, execution

**Workload**:
The container specification a Run executes, including image, resources, and environment.
_Avoid_: Spec (alone), definition

**Offer**:
A snapshot of placeable capacity from a Connection at a point in time.
Its Broker-assigned snapshot ID combines the Connection with the adapter-local
capacity identity, so two Connections can expose the same provider catalog item
without becoming the same Offer.
_Avoid_: Quote, instance type (alone)

**Connection**:
An authorized link to a provider control plane, with credentials Mercator uses to list offers and launch.
_Avoid_: Account, integration, provider config

**Execution Lane**:
Whether Mercator can run a second workload on capacity without allocating new
capacity. Reusable capacity is controlled through an enrolled Node and may
become a Rental; ephemeral capacity is a provider-native one-shot product that
holds nothing after its workload exits. The lane is orthogonal to an Offer's
kind, which says who owns the host rather than what survives the workload. The
Broker stamps it from the backend's negotiated capabilities, so an adapter
cannot advertise reuse it cannot perform.
_Avoid_: Type (alone), mode, reusability flag

**Rental**:
The capacity lease: machine capacity a workspace holds from a Connection, with
its own billing interval and lifecycle generation. Only capacity in the
reusable lane becomes a Rental.
_Avoid_: Worker, host (alone), machine (alone)

**Node**:
The enrolled Mercator runtime on one Rental generation. It is the authority on
node liveness, host and inventory facts, and container lifecycle, and it is
what makes a Rental capable of executing successive workloads. A Rental without
a Node is capacity nothing can run on.
_Avoid_: Agent (for the concept), daemon, instance

**Fleet**:
The set of Rentals a workspace currently owns, across all its Connections.
Derived from Rental state, never configured directly; fleet management means
driving Rental lifecycles (provision, reuse, retire).
_Avoid_: Cluster, pool, worker pool

**Rental Schedule**:
Mercator's ordered sequence of nonterminal Bookings assigned to one Rental.
It contains at most one running Booking followed by at most four queued Bookings.
_Avoid_: Machine queue, daemon queue

**Service Class**:
The kind of work a Run is, as its caller declared it, and the only thing that
says what waiting is worth to it. The five classes are interactive, standard,
batch, experimental, and opportunistic, and each declares its own exchange rates:
what a second of waiting costs, whether that second is counted to the start or to
the finish, and what an answer nobody stands behind costs. Placement computes one
dollar score over those rates, so cost and waiting are comparable quantities
rather than two rankings, and every Booking Decision records the rates it was
scored at. It replaced the placement objective outright: an objective named a
quantity to minimise and never what a second of it was worth, so the terms that
would have converted seconds into dollars were multiplied by zero. A class
Mercator does not know is refused where the Run enters.
_Avoid_: Objective, priority (alone), tier, QoS

**Placement**:
The activity of evaluating Offers and Rentals to choose where a Run goes.
Its audited outcome is a Booking Decision. "Scheduling" refers only to queue
positions within a Rental Schedule, never to this choosing.
_Avoid_: Scheduling (for the choosing), the Scheduler

**Artifact**:
Immutable, versioned content one Run publishes and other Runs read. The
version ID is its identity and never changes what it names; the catalog entry
carries the content digest, the object-store location, the size, the producing
Run, and the workspace it is scoped to. A Run that declares an Artifact input is
accepted at once and held unplaced until that version is durable, so it waits
for a publication and never for a machine.
_Avoid_: Dataset, output, blob, file

**Object Store**:
Where an Artifact's durable copy lives, and the only authority on whether it
exists. Mercator implements no distributed filesystem: content is in the object
store or it is not available, whatever any host is holding.
_Avoid_: Storage (alone), bucket, backend

**Artifact Replica**:
One host's local copy of an Artifact version, carrying the digest it claims and
when that claim was last checked. A verified replica is what a Run may read
instead of the object store, which makes it a speedup; an unverified one is
bytes nobody vouched for. Losing every replica of an Artifact costs time and
never availability.
_Avoid_: Cache (for Artifacts), local dataset, mirror

**Cache Mount**:
A workload-declared named mount whose content persists on a Rental across
Runs. Its identity is the workspace-scoped cache name; two Runs share data
exactly when they declare the same name in the same workspace, and two
workspaces that declare one name have two caches that never meet. It is mutable
and application-owned: Mercator manages its presence on a Rental and knows
nothing about its contents, so it can never carry immutable identity the way an
Artifact does. Beside the name, a Run states a compatibility key naming which
generation of content it can use; Mercator compares that key and never
interprets it, so content declared under another generation is worth what no
content is worth and gets storage of its own. Its contents and any
sync-from-remote logic belong to the application, and declaring a shared name is
never an exclusivity or single-writer guarantee.
_Avoid_: Volume (alone), dataset, shared storage

**Warmth**:
How much of a Run's needs are already present on a Rental. Its components are
Image Warmth (the layers of the Run's image already unpacked there) and Data
Warmth (verified local copies of the Artifacts the Run reads). Placement scores
Warmth; a warm Rental is one with nonzero Warmth for a given Run. A populated
Cache Mount is not Warmth: its content has no identity Mercator can compare a
Run's needs against, so it is best-effort speed rather than something scoring
can be founded on.
_Avoid_: Code plane, data plane, cache affinity, locality (alone)

**Booking Decision**:
The audited choice to assign a Run to an existing Rental, provision an Offer,
launch a one-shot ephemeral execution, or fail because no feasible capacity
exists.
_Avoid_: Booking (for the decision), scheduling result

**Booking**:
The durable binding of one Run to one Rental. Its state is `running` while the
Run executes or `queued` while it waits for every earlier Booking in the Rental
Schedule to become terminal.
_Avoid_: Placement, reservation, deferred run, waiter

**Conformance Trial**:
An isolated verification that launches one probe Run through a Connection and
proves either signed successful exit or explicit launch cancellation, followed
by terminal provider cleanup.
_Avoid_: Credential check, provider test, smoke test

**Evidence Bundle**:
The sanitized record of a Conformance Trial's Connection, selected Offer, Run,
Booking, public events, terminal outcome, cost bound, timing, primary
failure, cleanup failure, and final provider inventory.
_Avoid_: Logs, debug output

**Verdict**:
The passed, failed, or blocked conclusion derived from an Evidence Bundle.
_Avoid_: Status (alone), result (alone)
