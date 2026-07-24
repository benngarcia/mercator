# ADR 0004: Mercator Lab Is The Deterministic Executable Specification

Status: Accepted

Date: 2026-07-23

Tracking issue:
[#146](https://github.com/benngarcia/mercator/issues/146)

## Context

`internal/scenario` already runs real Placement and orchestration code against a
fake provider and a SQLite event log. Its fixtures protect four current
decisions and state eight target decisions. The dashboard also has a separate
scenario transcript and wall-clock playback path.

The current harness stops at scripted Placement decisions. It has no
discrete-event kernel, immutable sampled reality, replay artifact, independent
actual-world model, external-effect history, invariant registry, generation,
shrinking, or process-backed UI mode. Its fake provider also exposes simulated
truth directly as scheduler input, which cannot represent stale or missing
observations.

Mercator needs one scenario contract that can exercise Placement, controllers,
reconciliation, HTTP/SSE, the console, policy experiments, fault campaigns,
and exact CI-failure replay. This architecture must be established before the
larger runtime migration.

## Decision

Mercator Lab deepens the existing scenario system. It does not create a second
scenario framework.

### Four execution layers

1. A **Scenario Blueprint** is a versioned authored or generated description of
   possible worlds. `internal/scenario` owns Blueprint schemas, classification,
   catalog loading, fixture migration, and test-only UI checkpoint sidecars.
2. A **World Tape** is the fully sampled, immutable, policy-neutral record of
   exogenous reality and keyed potential outcomes.
3. A **Lab Execution** runs one Mercator build and policy configuration against
   one World Tape.
4. A **Run Bundle** is the portable artifact used to inspect, compare, minimize,
   and replay one Lab Execution.

The first Run Bundle format is a deterministic, uncompressed tar with the
`.mlab` extension. Entry order, paths, modes, owners, and timestamps are fixed.
Normalization removes only declared unstable metadata and retains all
semantically relevant behavior.

### Deterministic execution

`internal/lab` will own one dispatcher. Every scheduled transition is ordered
by virtual timestamp and stable sequence number. Callers drive an Execution
through one deep interface:

```go
func Compile(Blueprint, CompileOptions) (WorldTape, []Sample, error)
func Open(context.Context, Config) (*Execution, error)

func (e *Execution) Drive(
    context.Context,
    DriveCommand,
) (Checkpoint, error)

func (e *Execution) Restart(context.Context) error
func (e *Execution) Export(context.Context) (RunBundle, error)
func (e *Execution) Close() error
```

`DriveCommand` supports stepping one event, advancing by a duration, running
until a predicate or event, and running until quiescence. The dispatcher
detects excessive total transitions, excessive activity at one timestamp,
livelock, and non-quiescent execution.

Production packages receive narrow clock, timer, ID, retry, and entropy
interfaces. Production adapters use wall time and system entropy. Lab adapters
use the deterministic kernel. Domain packages contain no Lab flags.

### Keyed entropy

Material samples derive from the Blueprint seed, logical entropy stream,
semantic operation key, draw name, and distribution version. Independent
subsystems use independent streams. Adding an unrelated draw must not shift
existing outcomes.

The World Tape records all material sampled values or enough input to reproduce
them. Policy comparisons reuse the same World Tape so arrivals, market changes,
faults, and candidate-specific actual outcomes stay fixed.

### Truth and observation

Lab stores **World Truth** and **Observed State** separately.

World Truth owns actual provider capacity, external resources, process state,
image layers, Cache Mount presence, Artifact replicas, network conditions,
command acceptance, exits, and failures.

Observed State contains only facts delivered through production seams, such as
offer snapshots, provider responses, heartbeats, inventories, callbacks, and
timeouts. Delivery may be stale, delayed, lost, duplicated, or reordered.
Mercator Placement and controllers can read Observed State. They cannot read
World Truth.

World actual-runtime, transfer, provision-latency, and failure models are
independent from Mercator prediction code. Run Bundles record predicted and
actual values together for calibration.

### Production code and simulated seams

L1 executes the real domain validation, Placement, orchestrator, reconciliation,
event append path, SQLite event log, projections, HTTP API, and SSE feed.

Lab replaces only external provider, node/runtime, OCI registry, Artifact
storage, callback/message, clock/timer, and telemetry seams. Tests assert public
events and API-visible state.

Every external command and consequence enters an Effect Ledger with stable
operation, causation, and correlation identities. It represents rejection,
acceptance, lost or delayed responses, duplicate commands and responses, stale
responses, callback loss or reordering, independent provider changes, and
external resources that survive a control-plane restart.

### Artifact and Cache Mount identity

An **Artifact** is immutable, versioned content produced or consumed by Runs.
Artifact identity, dependency edges, and replica locality are exact.

A **Cache Mount** is mutable, application-owned state identified only by its
workspace-scoped cache name. Cache contents do not provide immutable identity.
Existing dataset-gravity scenario fixtures migrate their content-keyed data to
Artifacts.

### One catalog and explicit fidelity

The canonical catalog supports green regressions, targets, generated cases,
minimized regressions, UI demos, and conformance scenarios. A target that starts
passing still fails until deliberately promoted. UI checkpoints remain sidecar
metadata and do not enter the domain model.

The documented fidelity levels are:

- L0: pure domain and Placement unit tests
- L1: deterministic in-process simulation with the real control plane
- L2: generated, fuzz, and fault campaigns
- L3: process-in-the-loop server with networked fake providers and nodes
- L4: local Docker and bounded real-provider conformance
- L5: production telemetry calibration and shadow-policy replay

Every Run Bundle records its fidelity level. Simulated adapters cannot establish
provider protocol compatibility. L4 conformance requires explicit real
implementations behind conformance-test interfaces.

## Delivery

Implementation lands as eight serial, independently green pull requests managed
with `gh-stack`. The living plan is
[Mercator Lab execution plan](../project/mercator-lab-exec-plan.md).
Snapshot-bounded event scans in #142 and the Run projection in #140 form the
second foundation slice.

## Consequences

The existing fast placement harness remains useful while its fixtures migrate
into the versioned Blueprint contract. The separate dashboard transcript and
wall-clock playback implementation is deleted after the Lab-backed normal
API/SSE path has equivalent UI coverage.

Determinism becomes a production seam requirement. Relevant clocks, timers,
IDs, retries, and entropy can no longer be acquired implicitly inside control
plane packages.

Run Bundles become public versioned contracts. Schema changes require explicit
versioning and replay compatibility decisions.

## Rejected alternatives

- Replacing `internal/scenario` with a separate Lab framework would split the
  executable specification and abandon the useful corpus.
- One global pseudo-random sequence would make unrelated samples perturb later
  outcomes and invalidate fair policy comparison.
- Feeding World Truth directly into adapter reads would make stale or lost
  observations impossible to model.
- Using Mercator predictions as actual outcomes would make calibration
  tautological.
- A directory-only Run Bundle would make CI retention and one-command replay
  less reliable.
- Preserving content-keyed Cache Mounts would give two domain concepts the same
  immutable identity.

## Non-goals

Mercator Lab does not implement a filesystem, GPU kernels, low-level hardware,
Linux, or TCP. It does not complete the node-agent migration, add Kubernetes,
replace SQLite, build a general workflow engine, optimize for many providers,
or rewrite working domain code for appearance.
