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
An isolated verification of one Connection through Mercator's public run lifecycle.
It owns its temporary workspace, Runs, evidence, and provider cleanup.
_Avoid_: Provider test, integration test

**Scenario**:
One expected lifecycle inside a Conformance Trial, such as successful completion,
failed completion, or cancellation.
_Avoid_: Test case

**Evidence Bundle**:
The sanitized record of a Conformance Trial's placements, lifecycle events,
terminal Runs, timings, and final provider inventory.
_Avoid_: Logs, debug output

**Verdict**:
The Conformance Trial's passed, failed, or blocked conclusion derived from its
Evidence Bundle.
_Avoid_: Status (alone), result (alone)
