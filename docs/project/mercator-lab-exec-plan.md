# Build Mercator Lab

This is a living execution plan. Update progress, decisions, evidence, and
unexpected findings in the same implementation-bearing pull request that
changes them. The tracking issue is
[#146](https://github.com/benngarcia/mercator/issues/146), and the architecture
decision is [ADR 0004](../adr/0004-mercator-lab-deterministic-executable-specification.md).

## Purpose

Mercator Lab makes the scenario catalog Mercator's canonical executable
specification. The same logical scenarios must drive Placement tests,
controller and reconciliation tests, HTTP/SSE integration tests, console smoke
tests, policy comparisons, fault campaigns, and exact replay of CI failures.

The core implementation rule is:

> Run real Mercator production logic inside a deterministic simulated world.
> Simulate external boundaries, clocks, and delivery. Keep Placement,
> orchestration, reconciliation, persistence, projections, HTTP, SSE, and the
> console real.

## Approved decisions

- `A1`: `internal/scenario` owns Blueprint schemas and the catalog;
  `internal/lab` owns execution.
- `B1`: `.mlab` is a deterministic uncompressed tar.
- `C1 + gh-stack`: eight serial, independently green stacked pull requests.
- `D1`: absorb #142 and #140 in the second foundation slice.
- `E1`: immutable datasets are Artifacts; Cache Mounts remain mutable and
  name-only.

## Progress

- [x] 2026-07-23: Audit the current scenario harness, fake world, production
  composition seams, dashboard playback, console reducer/API/SSE clients,
  scenario corpus, projections, workflows, and focused verification baseline.
- [x] 2026-07-23: Approve ADR ontology and public execution interface.
- [x] 2026-07-23: Create tracking issue #146.
- [x] 2026-07-23: Initialize `gh-stack` on
  `beng/mercator-lab-contracts` from `origin/master` at `6698032`.
- [x] 2026-07-23: Keep the eight-slice graph in local `gh-stack` metadata.
  GitHub rejected remote stack creation with exit code 9 because Stacked PRs
  remains a private preview and this repository is not enabled. Publish the
  same serial base-branch chain as standard pull requests until the remote
  stack API becomes available.
- [x] 2026-07-23: Complete Slice 01 contracts and executable plan. All 12
  placement scenarios load through Blueprint v1 while preserving four green
  and eight target classifications. The catalog also validates the complete
  15-checkpoint target demonstration and its UI sidecar.
- [x] 2026-07-23: Complete Slice 02 stable read models. Global scans stop at
  a captured filtered head. The SQLite Run projection commits with every Run
  fact, including the coupled Rental Schedule transaction, and serves bounded
  cursor pages plus the open-Run index. Existing databases rebuild once from
  the event log before the daemon serves requests.
- [x] 2026-07-24: Complete Slice 03 deterministic kernel, entropy, World
  Tape, and Run Bundle skeleton. Execution owns immutable copies of its inputs,
  enforces every configured limit, and supports step, duration, event,
  predicate, and quiescence drives. Replay accepts only the strict, canonical
  uncompressed tar contract.
- [x] 2026-07-24: Complete Slice 04 World Truth, Observed State, effects,
  Artifact and Cache Mount consequences, and real Level 1 control plane. The
  Lab now runs the production scheduler, orchestrator, reconciliation, SQLite
  event log, durable Run projection, and Rental Schedule store against a
  simulated provider. Deterministic restart reconstructs the control plane
  while preserving external executions.
- [ ] Slice 05: invariants, metamorphic tests, and reference solver.
- [ ] Slice 06: generators, fuzzing, and semantic shrinking.
- [ ] Slice 07: Lab server and normal UI path.
- [ ] Slice 08: complete vertical proof, CI tiers, and documentation.

## Current architecture evidence

- `internal/scenario.SimBackend` already composes the real orchestrator,
  Placement implementation, SQLite event log, and fake provider.
- `fake.Clock` exposes mutable `Now` and `Advance`; it has no event queue,
  predicate drive, quiescence, or runaway detection.
- `fake.World.ListOffers` constructs scheduler-visible facts directly from its
  simulated state, so truth and observations are currently the same object.
- intake IDs and several HTTP/SSE identities still use nondeterministic UUIDs.
- daemon reconciliation and dashboard playback use wall-clock tickers.
- before Slice 02, `GET /v1/runs` rebuilt state by scanning the full event
  history. #142 and #140 now resolve through snapshot-bounded scans and the
  atomic indexed Run projection.
- dashboard playback owns three hard-coded transcripts and a special 250 ms SSE
  path beside the normal API/SSE feed.
- the current top-level placement corpus contains 12 scenarios: four green and
  eight target.
- the legacy adapter converts mutable image tags and layer names into stable
  synthetic sha256 identities. Canonical Blueprint v1 rejects both forms and
  requires exact digests at the source.
- the Slice 01 review moved contract test inputs into named fixtures and made
  `Blueprint` the strict wire representation, deleting a duplicate decode
  structure and 292 lines of test setup.

## Verification evidence

### Slice 01

On 2026-07-23, the exact reviewed worktree passed:

```text
go test ./internal/scenario -count=1
go test -race ./internal/scenario -count=1
go generate ./...
go test ./...
go vet ./...
go build ./...
scripts/build-release-archives.sh v0.0.0-ci /private/tmp/mercator-release-dist-slice01
scripts/check-open-source-launch.sh
cd web/app
bun install --frozen-lockfile
bun run generate:api
bun run check:react-effects
bun run typecheck
bun run test
bun run build
git diff --check
```

### Slice 02

On 2026-07-23, the exact reviewed worktree passed:

```text
go generate ./...
go test ./...
go vet ./...
go build ./...
go test -race ./internal/eventlog ./internal/storage/sqlite ./internal/orchestrator ./internal/httpapi ./internal/daemon ./internal/cli ./internal/rentalschedule ./internal/broker
bun run check:react-effects
bun run typecheck
bun run test
bun run build
scripts/build-release-archives.sh v0.0.0-ci /private/tmp/mercator-release-dist-slice02
scripts/check-open-source-launch.sh
git diff --check
```

A temporary local measurement issued 500 indexed reads of the first 50-Run
page, then deleted the measurement harness:

```text
5,000 Runs:  69.215us per page
50,000 Runs: 59.395us per page
```

The stable primary-key cursor keeps page work independent of total Run history.

### Slice 03

On 2026-07-24, the exact reviewed worktree passed:

```text
go test ./internal/lab ./internal/scenario -count=1
go test -race ./internal/lab ./internal/scenario -count=1
go generate ./...
go test ./...
go vet ./...
go build ./...
cd web/app
bun install --frozen-lockfile
bun run generate:api
bun run check:react-effects
bun run typecheck
bun run test
bun run build
cd ../..
scripts/build-release-archives.sh v0.0.0-ci /private/tmp/mercator-release-dist-slice03
scripts/check-open-source-launch.sh
MERCATOR_BROWSER_TEST=1 go test -count=1 ./internal/httpapi -run '^TestConsoleRunsNavigation$'
git diff --check
```

The browser test needed an unsandboxed local rerun because Chromium's macOS
Mach-port registration is denied inside the command sandbox. The same test
then passed in 5.981 seconds.

### Slice 04

On 2026-07-24, the exact reviewed worktree passed:

```text
go test -race ./internal/lab ./internal/scenario -count=1
go test ./...
go vet ./...
go build ./...
cd web/app
bun install --frozen-lockfile
bun run generate:api
bun run check:react-effects
bun run typecheck
bun run test
bun run build
cd ../..
scripts/build-release-archives.sh v0.0.0-ci /private/tmp/mercator-release-dist-slice04
scripts/check-open-source-launch.sh
MERCATOR_BROWSER_TEST=1 go test -count=1 ./internal/httpapi -run '^TestConsoleRunsNavigation$'
git diff --check
```

The browser test again needed an unsandboxed local rerun because Chromium's
macOS Mach-port registration is denied inside the command sandbox. The same
test then passed in 5.920 seconds.

World Tape v2 records actual Run runtime as sampled exogenous reality instead
of deriving it from Mercator's prediction. Simultaneous arrivals preserve
Blueprint order and receive global sequence numbers after time ordering.
Artifact replicas are immutable facts keyed by Artifact ID; Cache Mounts remain
mutable facts keyed only by mount name and node. The Run Bundle now carries
public Mercator events, effects, prediction-versus-actual records, and summary
metrics without private event data or effect secrets.

## Public contracts

### Scenario catalog

`internal/scenario` exposes version-aware loading. Blueprint v1 uses
`mercator.lab/blueprint.v1`. Current placement fixtures have a one-way adapter
into Blueprint v1. Unknown versions fail loudly.

Catalog classification preserves the current green and target promotion rule
and adds generated, minimized, demo, and conformance sources. Browser
checkpoints live in optional sidecar metadata.

### Lab execution

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

The kernel queue, continuations, truth store, observation store, effect
adapters, invariant dispatch, and SQLite composition remain private.

### Run Bundle

The initial `.mlab` entry order is:

```text
manifest.json
configuration.json
blueprint.json
world-tape.json
samples.jsonl
events/mercator.jsonl
events/world.jsonl
effects.jsonl
predictions.jsonl
invariants.json
metrics.json
ui/trace.zip
ui/screenshots/*
```

UI members are optional. Every other member is required. Replay fails loudly
when the current binary cannot support the recorded schema or Mercator build
identity.

## Delivery slices

### 01. Contracts and executable plan

Add ADR 0004 and this plan. Introduce Blueprint v1, strict version handling,
the current-fixture adapter, catalog classification, UI sidecar metadata, and
the target-promotion guard. Migrate dataset-gravity fixtures from content-keyed
Cache Mounts to Artifacts. Add the complete producer-consumer demonstration as
a target Blueprint.

Exit evidence:

- all 12 current scenarios load through the canonical contract;
- the current corpus remains four green and eight target;
- legacy fixtures adapt to Blueprint v1;
- unsupported schema versions fail loudly;
- the vertical Blueprint has all 15 named proof checkpoints and validates;
- focused scenario tests and `git diff --check` pass.

### 02. Stable read models

Land snapshot-bounded scans from #142. Add the atomic Run projection from #140,
snapshot reads, deterministic rebuild, and API-visible state comparison.

Exit evidence:

- sustained appends cannot prevent a snapshot scan from terminating;
- event append and projection writes commit atomically;
- rebuilding a closed-Run corpus produces byte-equivalent API state;
- full Go tests, vet, build, race coverage for touched packages, and launch
  checks pass.

### 03. Kernel, entropy, and World Tape

Add virtual time ordered by timestamp and sequence, deep `Drive` commands,
deterministic IDs, keyed entropy, Blueprint compilation, World Tape validation,
execution limits, and the Run Bundle skeleton.

Exit evidence:

- repeated compilation and replay produce identical normalized hashes;
- an unrelated keyed sample cannot perturb existing samples;
- the same World Tape can drive two policy configurations;
- step, duration, predicate, and quiescence commands pass kernel laws;
- runaway, livelock, and same-timestamp limits fail loudly.

### 04. World, effects, and real control plane

Separate World Truth from Observed State. Implement simulated provider, node,
registry, Artifact storage, callback, and telemetry adapters. Record every
external command and consequence in the Effect Ledger. Compose the real control
plane and support deterministic restart with surviving external resources.

Exit evidence:

- lost responses, delayed responses, duplicate callbacks, stale facts, and
  reordered delivery converge through real reconciliation;
- no production decision package can depend on World Truth;
- actual outcomes remain independent from Mercator predictions;
- restart does not duplicate external consequences.

### 05. Invariants and policy oracles

Run the invariant registry after every transition or declared checkpoint. Add
the required safety checks, bounded liveness checks, metamorphic relations, and
a deliberately simple exhaustive Placement solver for small worlds.

Exit evidence:

- every required invariant has a passing and deliberately failing replayable
  case;
- every liveness check records assumptions and a virtual-time bound;
- all required metamorphic checks are reusable;
- small valid worlds agree with the independent feasibility and winner oracle.

### 06. Generation and shrinking

Add typed valid-world generators for catalogs, capacity, arrivals, exact OCI
graphs, immutable Artifact DAGs, workload phases, path throughput, actual
runtime models, and faults. Add Go fuzz targets and deterministic semantic
shrinking.

Exit evidence:

- one generated failure shrinks across timeline operations, Runs, Rentals,
  Offers, image layers, Artifacts, faults, and optional fields;
- the minimized case persists as a normal catalog scenario;
- one `.mlab` file replays the original failure fingerprint.

### 07. Lab server and real UI path

Add `mercator lab serve`, Lab-only drive/restart/truth/bundle routes, the normal
production HTTP APIs and SSE feed, and a Playwright fixture that drives virtual
time. Converge dashboard demos onto the catalog and delete transcript playback
after parity.

Exit evidence:

- `mercator serve` has no route, flag, environment variable, or dependency that
  can mount Lab controls;
- Playwright starts an isolated Lab server and uses the normal API/SSE clients;
- browser tests contain no arbitrary sleeps;
- failures retain a Playwright trace and Run Bundle;
- curated visual snapshots and broad semantic/accessibility assertions pass.

### 08. Vertical proof, CI, and documentation

Promote the complete producer-consumer scenario when all behavior is real. Add
fast pull-request CI, broader scheduled campaigns, fidelity documentation, and
author, generate, replay, minimize, and promote instructions.

Exit evidence:

- all 15 vertical demonstration checkpoints pass;
- restart and no-restart normalized outputs match;
- a replay command reconstructs the full run from one `.mlab`;
- every completion-contract row below has current authoritative evidence;
- `go test ./...`, `go vet ./...`, `go build ./...`, frontend typecheck and
  Vitest, the chosen Lab Playwright smoke, and
  `scripts/check-open-source-launch.sh` pass;
- the exact final stacked PR head clears CI and reviewer feedback.

## Completion contract

| Requirement | Authoritative proof |
| --- | --- |
| 1. Deterministic execution | Kernel law tests plus identical normalized bundle hashes |
| 2. Four layers | Strict versioned round trips and current-fixture migration |
| 3. Keyed entropy | Unrelated draws do not perturb existing samples |
| 4. Truth vs observation | Delayed or stale observations diverge from fixed truth |
| 5. Independent actuals | World models disagree with predictions without changing actual outcomes |
| 6. Production code | Public events and API state prove the real L1 composition |
| 7. Effects | Fault table covers every required ambiguity and restart |
| 8. Bundle and replay | One complete `.mlab`, one local command, one normalized comparison |
| 9. Catalog | All scenario sources resolve through one catalog and promotion guard |
| 10. Invariants | Passing and deliberately failing replayable cases with explicit bounds |
| 11. Metamorphic/reference | Seven required relations and the small-world exhaustive oracle |
| 12. Synthetic data | Typed generators cover every required world dimension |
| 13. Fuzz and shrink | One minimized failure persists and replays |
| 14. Lab API/UI | Production routes plus SSE under Playwright, driven by virtual time |
| 15. Fidelity/conformance | L0 through L5 claims documented and enforced at seams |

The final audit also checks every named deliverable, every vertical demonstration
step, every success criterion, and every non-goal. Missing or indirect evidence
keeps #146 open.

## Surprises and discoveries

- The accepted Artifact ontology corrects existing scenario drift:
  `cache_mounts[].key` currently acts as immutable dataset identity even though
  `CONTEXT.md` defines Cache Mount contents as mutable and application-owned.
- The current scenario backend is already the right production seam for
  Placement and orchestration. Its limitations come from mutable scripted
  boundaries and missing read models, not from a need to replace the runner.
- The console reducer and normal SSE client are reusable. The dashboard
  transcript and playback protocol are the parallel path to delete.
- Blueprint values need explicit JSON marshaling for durations, relative
  moments, and exact numeric bounds. Without those reciprocal encoders, a
  bundle can serialize a valid in-memory Blueprint into a document its strict
  decoder cannot replay.
- Repeated-event livelock detection must include virtual time. Identical
  periodic events at different timestamps are progress; only consecutive
  identical transitions at one timestamp count toward the repeated-event
  limit.

Add dated findings here as implementation changes the plan.
