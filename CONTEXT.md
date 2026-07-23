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

**Rental**:
Reusable machine capacity whose lifecycle and standard Docker endpoint Mercator owns.
_Avoid_: Worker, host (alone), machine (alone)

**Fleet**:
The set of Rentals a workspace currently owns, across all its Connections.
Derived from Rental state, never configured directly; fleet management means
driving Rental lifecycles (provision, reuse, retire).
_Avoid_: Cluster, pool, worker pool

**Rental Schedule**:
Mercator's ordered sequence of nonterminal Bookings assigned to one Rental.
It contains at most one running Booking followed by at most four queued Bookings.
_Avoid_: Machine queue, daemon queue

**Placement**:
The activity of evaluating Offers and Rentals to choose where a Run goes.
Its audited outcome is a Booking Decision. "Scheduling" refers only to queue
positions within a Rental Schedule, never to this choosing.
_Avoid_: Scheduling (for the choosing), the Scheduler

**Cache Mount**:
A workload-declared named mount whose content persists on a Rental across
Runs. Its identity is the workspace-scoped cache name; two Runs share data
exactly when they declare the same name. Mercator manages the cache's
presence on a Rental; its contents and any sync-from-remote logic belong to
the application. Declaring a shared name is a Warmth signal for Placement,
never an exclusivity or single-writer guarantee.
_Avoid_: Volume (alone), dataset, shared storage

**Warmth**:
How much of a Run's needs are already present on a Rental. Its components are
Image Warmth (Docker layers already on the Rental) and Data Warmth (the
Run's Cache Mounts already populated there). Placement scores Warmth; a warm
Rental is one with nonzero Warmth for a given Run.
_Avoid_: Code plane, data plane, cache affinity, locality (alone)

**Booking Decision**:
The audited choice to assign a Run to an existing Rental, provision an Offer,
or fail because no feasible capacity exists.
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
