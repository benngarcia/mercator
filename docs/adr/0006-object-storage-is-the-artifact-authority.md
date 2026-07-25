# ADR 0006: Object Storage Is The Artifact Authority

Status: Accepted

Date: 2026-07-25

Tracking issue:
[#155](https://github.com/benngarcia/mercator/issues/155)

## Context

Phase 3 of the capacity broker migration makes locality exact in three classes:
OCI images, immutable Artifacts, and mutable application caches. The image class
landed first. This ADR is about the second, and about the one modelling mistake
that would make every later slice wrong.

Before this change Mercator had no Artifact at all. `internal/domain` had no
type for one, `domain.WorkloadSpec` could not carry one, and
`scenario.WorkloadForRun` dropped a Blueprint's `consumes_artifacts` and
`produces_artifacts` on the way into the real orchestrator. The only place
Artifacts existed was the simulated worlds, where a replica was a boolean that
appeared instantly and free when a launch happened to touch the content.

That left the Lab's admission gate reading `hasAnyReplica`: a Run could start
when some Rental was holding bytes. Three things follow from that predicate, and
all three are wrong:

- content becomes available the moment it lands on one machine, so a consumer
  can be admitted on a copy that no other host can reach;
- content stops being available when that machine goes away, so releasing a
  Rental can make a Run that was ready become impossible;
- a copy nobody checked is worth exactly as much as a copy that matches its
  digest, because presence is the only fact the model has.

Those are the semantics of a distributed filesystem assembled out of whatever
machines happen to be up. Mercator is not that, and the phase goal says so:
object storage is the durable authority, local replicas are an optimisation, and
replicas must be verified.

## Decision

An **Artifact** is immutable, versioned content one Run publishes and other Runs
read. Its authority is an object store. Mercator records two facts about it,
and neither substitutes for the other.

`domain.ArtifactVersion` is the catalog entry: version ID, workspace scope,
content digest, size, object-store location, producing Run, and publication
time. `Durable()` is true when the store holds the bytes, and that is the only
admissible answer to whether a consumer may run.

`domain.ArtifactReplica` is one host's local copy: the digest it claims, its
size, its verification state, and when that state was established. A verified
replica is one whose bytes were hashed and matched the catalog entry, and it is
the only kind a Run may read instead of the object store. Replicas reach
Mercator on `OfferSnapshot.Artifacts`, an `ArtifactInventory` that states
separately whether the holder enumerated at all, exactly as `ImageInventory`
does: a machine Mercator runs nothing of its own on reports none of its content,
and that silence is not absence.

`domain.WorkloadSpec.Artifacts` is what a workload declares it reads and
publishes, by version ID. It is a public contract field, it reaches the public
event log through the run-requested payload, and it is what admission decides
on. A workload may not consume a version it produces, because a version is
immutable and cannot be its own input.

### Admission blocks on durability

A Run whose declared inputs are not all durable is not admitted. It waits for a
publication, never for a machine. This is what makes a replica an optimisation:
losing every copy of an Artifact costs a fetch and never costs availability, and
gaining a copy on one host makes nothing possible that was not possible before.

### The simulated worlds gained an object store

`internal/lab` owns an `objectStore` holding the catalog and the publication
times. Publication takes the world's own transfer time, which is what makes a
producer's local write and its durable publication two moments rather than one.
Between them a copy of the content exists on a machine and the Artifact does
not, and that gap is where a consumer gated on presence and a consumer gated on
durability behave differently. The Blueprint
`artifact-must-be-durable-before-a-consumer-runs` is that gap.

A copy is written by exactly two things: a Run publishing its output onto the
host it ran on, and a fetch from a durable publication. Both are recorded in the
Effect Ledger, and both land when their bytes land. Capacity that keeps nothing
keeps no Artifact copy either, for the same two reasons it keeps no image: a
provisionable offer is a machine that does not exist yet, and a one-shot product
is gone once its workload exits.

### Two Lab invariants police it

`safety.artifact_replica_verified` holds four things at once. No copy exists of
content the catalog cannot name. No copy claims a digest that version does not
have. Every copy traces back to the object store, which has exactly two shapes:
fetched from a publication, or written by the Run that produces it on its way to
becoming one. And no Run reads a copy nothing checked against the catalog.

`safety.artifact_dependencies` was re-pointed. It used to read a Run's inputs
out of `RunArrival.Request`, which is the World Tape rather than anything
Mercator holds, so it checked the world against itself and would have passed on
a Run whose declaration the control plane never received. It now reads the
workload Mercator recorded in its own public event log, and orders each
consuming launch against the publication effect it depended on.

`safety.locality_provenance` gained the Artifact half of its own rule: capacity
that keeps nothing holds no Artifact copy either.

## Consequences

`OfferSnapshot.Artifacts` and `WorkloadSpec.artifacts` are public API contract
fields, generated into both the Go and the TypeScript contracts.

A Blueprint's `ArtifactSpec` now states a content digest and, when a Run in the
same Blueprint publishes it, which Run that is. An Artifact with a producer is
not durable at virtual time zero and no machine may be seeded holding a copy of
it, because a copy of content nothing has produced is a copy of nothing.
`RentalSpec.artifact_replicas` states a verification state per copy, so a
fixture can no longer say only that a machine "has" something.

Publication taking time changes how far a Lab execution must be driven: the
completion driver now advances until the world owes nothing it started,
including an upload, because a parked consumer is waiting on one.

## Rejected alternatives

Keeping presence-on-some-host as the admission predicate would have kept the
model this ADR exists to remove. It is also what makes producer-consumer
affinity dangerous rather than useful: a preference for the host that produced
an Artifact is sound only when going anywhere else still works.

Modelling a replica as a boolean and adding verification later would have left
the corpus asserting that an unchecked copy is as good as a checked one, which
is the claim a digest exists to refuse.

Publishing an Artifact instantly at the producing Run's completion would have
made the local copy and the durable one the same moment, which is precisely the
distinction the goal asks Mercator to enforce. No fixture could then tell a
control plane that waits for durability from one that waits for presence.

Giving `ArtifactSpec` a workspace field was dropped. A Blueprint has no
workspace vocabulary and the backends name their own workspace, so the only
honest value a fixture could write is "mine". The catalog entry carries the
scope, filled from the world's workspace; a corpus statement about cross
workspace isolation waits for a Blueprint that can express two workspaces.

Implementing an artifact controller, a real object-store client, replication
policy, or prewarming is out of scope here. This ADR establishes the domain
model, the contract, and the admission rule those land behind.

## Non-goals

No distributed filesystem. No content-addressed store of Mercator's own. No
scheduler term for Artifact locality yet: producer-consumer soft affinity is a
later slice of phase 3, and it depends on the authority question being settled
first.
