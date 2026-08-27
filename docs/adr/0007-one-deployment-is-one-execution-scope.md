# ADR 0007: One Deployment Is One Execution Scope

Date: 2026-08-26

Status: Accepted

## Context

Mercator previously partitioned every identity, command, stream, credential,
cache, and console projection by an internal Workspace. In practice Bucket runs
one Mercator deployment per application environment. The application already
owns customer tenancy, while the broker's second tenancy model duplicated that
authority and made every call, migration, and recovery path harder.

The split also implied isolation the process could not actually provide. All
Workspaces shared one event log, one bearer token, one provider fleet, one
console, and one administrative surface.

## Decision

One Mercator deployment is one execution scope. Run, Connection, Rental, Node,
Artifact, and cache identities are unique within that deployment. HTTP and CLI
calls carry no workspace selector. Human sessions and machine tokens authorize
access to the deployment itself.

Product tenancy belongs in the application that dispatches work. A caller may
carry its own tenant identity in application-owned metadata, but Mercator does
not interpret or authorize it.

An operator that needs a harder isolation boundary runs another Mercator
deployment with its own address, token, keys, database, object-store prefix,
and provider Connections.

## Migration

Startup performs a one-way transactional flattening of legacy SQLite tables.
It refuses duplicate stream identities, command keys, Connections, Runs,
Rentals, Nodes, and Rental Schedules before changing data. Legacy Workspace
streams and their command records are discarded; all other history is retained.
The Workspace catalog and membership tables are then dropped.

There is no default Workspace, compatibility query parameter, or fallback
lookup. Old clients fail against the new contract and must be upgraded with the
deployment.

## Consequences

- Broker APIs, CLI commands, run reporting, provider ownership markers, caches,
  credentials, and the console are deployment-global.
- Cache names intentionally share mutable content across Runs in one deployment;
  compatibility keys still separate incompatible generations.
- IDs that only collided because they lived in different Workspaces block the
  migration. Operators must resolve the collision or isolate the datasets into
  separate deployments before upgrading.
- Mercator no longer claims to be a multi-tenant authorization boundary.
