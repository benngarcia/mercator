# OCI Run Broker V1 Agent Verification

Date: 2026-06-20
Branch: `beng/v1-run-broker`
Reviewed commit: `76d05b1d5397fbc0af3e9b1c774d1925d3dc0c13`

## Bottom Line

The branch is not a complete V1 implementation. It is a tested foundation slice
covering event log basics, domain validation basics, scheduler basics, a fake
adapter, a narrow internal orchestrator happy path, and a minimal HTTP surface.

The implementation currently violates or does not implement several original
V1 invariants. The previous status/README language overclaims what is usable.

## High Severity Findings

1. The executable server cannot run the advertised broker fast path.
   `POST /v1/runs` only appends `RunRequested`; it does not call `AdvanceRun`.
   `cmd/mercator` wires no offers, and the production `staticOfferAdapter`
   cannot launch workloads.

2. `AdvanceRun` is not idempotent after partial progress. Re-running it after a
   nonterminal observation can replay launch and attempt to append duplicate
   fixed event IDs.

3. Recovery after durable `LaunchIntentRecorded` is not event-log authoritative.
   `AdvanceRun` re-runs placement before checking existing launch intent, so
   changed offers can prevent recovery from an already logged intent.

4. Launch timeout and indeterminate behavior are collapsed into `LaunchFailed`.
   There is no required observe/ListOwned reconciliation before retrying after
   ambiguous launch outcomes.

5. The adapter launch contract does not carry the OCI workload. It omits
   platform, entrypoint, args, ports, resources, selected offer/native ref, and
   secret binding metadata.

6. Public events and event APIs can expose literal environment values.
   `RunRequested` stores the full workload revision as public event data, and
   `GET /events` returns that public payload unchanged.

7. Scheduler ignores accelerator/GPU requirements even though the domain models
   accelerator requirements and offer inventories.

8. Fake adapter idempotency is incomplete for `LaunchKey`. A different operation
   key with the same launch key and ownership token can overwrite the object.

9. Placement is not fully deterministic for identical offer sets if input order
   changes. Candidate order and decision hash follow input order.

10. Required V1 run endpoints are missing: cancel, get/list runs, wait,
    decision, refresh, and links.

11. API idempotency conflict behavior is not represented correctly. Reusing an
    idempotency key with a different request hash collapses to HTTP 400 instead
    of a machine-readable conflict.

## Medium Severity Findings

1. Cleanup retry/no-orphan behavior is not robust. If release fails after
   `CleanupRequested`, retry goes back through launch instead of resuming
   cleanup from the logged request.

2. Unknown or dishonest capability facts can pass feasibility checks:
   `MaxContainers == 0`, `CapacityEvidence.Available == false`, and unknown
   image cache facts are not handled conservatively.

3. Price constraints and penalties are incomplete. `MaxExpectedCostUSD` is not
   enforced, and start-failure, interruption, and uncertainty weights are
   defined but unused.

4. Placement preview bypasses workload validation and can panic on empty
   container arrays.

5. Decision audit contents are incomplete. `CollectionReport` is never
   populated, and candidates lack connection/adapter/native-ref context.

6. OpenAPI is materially incomplete: no request bodies, response schemas, error
   schema, 409 conflict, or event schemas.

7. Workspace isolation at the HTTP boundary is weak. Event listing defaults to
   `ws_1`, and create-run does not require request workspace to match workload
   workspace.

8. `subscription_offsets` are written by `Ack` but not used by `Subscribe`.

9. OCI-only validation is not hardened. `WorkloadSpec.Raw` can carry arbitrary
   extension payloads, and validation does not reject non-empty raw fields,
   empty env bindings, invalid env names, invalid ports, or oversized env data.

10. The known-gaps list under-reports missing V1 areas: connection service,
    authorization service, offer service/cache, reconciler/lease janitor,
    projection runner, durable sink replay/cursors, CLI behavior, and more.

## Implemented And Reasonably Tested

- SQLite event log with atomic append/read, optimistic concurrency, command
  idempotency, global reads, basic subscription wakeup, and CloudEvents-shaped
  conversion.
- Workload validation for one `main` Linux container, digest-pinned images,
  ambiguous literal/secret env bindings, and public port/network consistency.
- Basic deterministic scheduler tests for stale offers, network unknowns, hard
  rejections, and score-based selection.
- Fake adapter tests for same-operation idempotent launch, conflict detection,
  observe, release, and list-owned behavior.
- Internal orchestrator tests for create idempotency, launch intent before
  adapter launch, launch conflict recording, and one successful fake cleanup
  path.
- Minimal HTTP smoke tests for create, event listing, placement preview, health,
  and OpenAPI availability.

## Verification Commands Reported By Reviewers

- `go test ./...` passed.
- `go build ./...` passed.
- `GOCACHE=$(mktemp -d) go test -mod=readonly ./...` passed.

Passing tests confirm the implemented slice compiles and its narrow tests pass.
They do not establish original V1 completeness.

## Recommended Next Step

Do not proceed from this branch as if V1 is implemented. First decide whether to:

1. Reframe this branch explicitly as a foundation scaffold and fix the README,
   status, and design overclaims.
2. Start a real V1 implementation plan from the original prompt, beginning with
   event-log authority, replay-safe orchestrator/reconciler behavior, workload
   redaction, and a usable adapter path.
3. Split the original V1 into milestone branches: event core, workload/revision
   service, scheduler/offer service, fake adapter/reconciler, Docker adapter,
   secrets, public API/OpenAPI/CLI, sinks, and UI.
