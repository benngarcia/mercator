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

**Placement**:
The decision that selects an Offer for a Run (or preview) under the scheduler model.
_Avoid_: Scheduling result (as a noun for the decision)

**Conformance Trial**:
An isolated verification that launches one probe Run through a Connection,
observes its signed successful exit, and proves provider cleanup.
_Avoid_: Credential check, provider test, smoke test

**Evidence Bundle**:
The sanitized record of a Conformance Trial's Connection, selected Offer, Run,
terminal outcome, cost bound, timing, and final provider inventory.
_Avoid_: Logs, debug output

**Verdict**:
The passed, failed, or blocked conclusion derived from an Evidence Bundle.
_Avoid_: Status (alone), result (alone)
