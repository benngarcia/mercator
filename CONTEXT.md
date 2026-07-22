# Mercator

Mercator brokers compute runs onto provider capacity and records their lifecycle.

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

**Rental Schedule**:
Mercator's ordered sequence of nonterminal Bookings assigned to one Rental.
It contains at most one running Booking followed by at most four queued Bookings.
_Avoid_: Machine queue, daemon queue

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
