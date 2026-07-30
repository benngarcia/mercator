# Mercator Lab

Mercator Lab turns one scenario into a deterministic control-plane run, a live
console proof, and a replayable `.mlab` file. For example, the green
`artifact-warmth-restart` demonstration submits a producer, publishes an
immutable Artifact, admits its dependent consumer, loses one provider launch
response, restarts Mercator, and still closes both Runs successfully.

Lab runs real Mercator placement, orchestration, reconciliation, SQLite
persistence, projections, HTTP, SSE, and console code. It simulates external
provider, node, registry, Artifact store, callback, clock, and delivery
boundaries.

## Author and generate

Create a valid Blueprint to edit:

```sh
mercator lab author --output my-scenario.json
```

Generate a deterministic world from a semantic seed:

```sh
mercator lab generate \
  --seed nightly-2026-07-24 \
  --arrival fixed \
  --runs 4 \
  --rentals 3 \
  --offers 3 \
  --images 2 \
  --artifacts 1 \
  --faults \
  --output generated.json
```

The generator derives each choice from `seed + semantic key`. Adding an
unrelated draw does not perturb existing values.

## Execute and inspect

Run a Blueprint to completion and retain one bundle:

```sh
mercator lab run \
  --blueprint generated.json \
  --bundle generated.mlab
```

Open the isolated process server and production console:

```sh
mercator lab serve \
  --blueprint internal/scenario/scenarios/demos/artifact-warmth-restart.json \
  --addr 127.0.0.1:8081
```

The command prints a generated operator token when
`MERCATOR_API_TOKEN` is absent. Lab controls require that bearer token and bind
only to loopback. The console continues to use the production Run API and SSE
feed.

## Replay

One command reconstructs the exact recorded drive transcript and compares
normalized semantic output:

```sh
mercator lab replay --bundle generated.mlab
```

Write the reconstructed raw bundle when byte-level inspection is useful:

```sh
mercator lab replay \
  --bundle generated.mlab \
  --output replayed.mlab
```

The normalized hash covers configuration, Blueprint, World Tape, public
Mercator events, world events, predictions, invariants, and world-mutating
consequences. It ignores the execution-control transcript, screenshots,
Playwright trace bytes, read-only provider observations, and a
behavior-preserving control-plane restart.

## Minimize

Shrink the first rejected or ambiguously delivered external effect while
preserving its operation, command result, response result, and fault identity:

```sh
mercator lab minimize \
  --bundle failing.mlab \
  --output minimized.json
```

The shrinker removes timeline operations, Runs, Rentals, offers, image layers,
Artifacts, faults, and optional fields. Each removal remains only when the
failure fingerprint still reproduces.

## Promote

Promotion needs the target Blueprint and the exact proving bundle:

```sh
mercator lab promote \
  --blueprint target.json \
  --bundle target-proof.mlab \
  --output promoted.json
```

The command rejects a bundle from another Blueprint. For the complete vertical
demonstration, it also rejects promotion unless all 15 declared checkpoints
pass, including UI evidence, restart equivalence, replay equivalence, and every
latest invariant.

## Run Bundle contract

`.mlab` is a deterministic, uncompressed tar with this canonical order:

```text
manifest.json
configuration.json
blueprint.json
world-tape.json
drives.jsonl
samples.jsonl
events/mercator.jsonl
events/world.jsonl
effects.jsonl
predictions.jsonl
invariants.json
metrics.json
ui/trace.zip
ui/screenshots/*.png
```

UI entries are optional. Every other entry is required. Replay rejects missing,
duplicate, unknown, oversized, unsafe, and out-of-order entries.

## Fidelity

| Level | What it proves | Current seam |
| --- | --- | --- |
| L0 | Domain and scheduler laws without a control plane | independent small-world solver and metamorphic checks |
| L1 | Deterministic behavior through the real in-process control plane | `lab.Open`, production scheduler, orchestrator, persistence, and projections |
| L2 | Generated worlds, fault campaigns, fuzzing, and semantic shrinking | typed generator, World Tape, Effect Ledger, invariant registry, fuzz targets |
| L3 | Process and browser behavior over real HTTP and SSE | `mercator lab serve` and Playwright prove the server and console; provider and node doubles still share the Lab process |
| L4 | Protocol compatibility and bounded real-provider behavior | `mercator verify` and `internal/conformance` through the production `adapter.Provider` seam |
| L5 | Calibration against production telemetry and shadow replay | future work; Lab records prediction-versus-actual rows but does not ingest production telemetry |

The `adapter.Provider` interface is the explicit provider conformance seam.
Lab's simulated provider implements it for deterministic control-plane tests.
`mercator verify` exercises a real implementation through the same public
broker flow. A passing Lab scenario does not prove provider protocol
compatibility, real launch latency, image pull behavior, billing, callback
delivery, or cleanup on a provider.

## CI tiers

Pull requests run focused Lab race tests, a short generated-world fuzz
campaign, and the Playwright vertical proof. The scheduled `Lab Campaigns`
workflow runs longer generator and scenario fuzz campaigns plus the browser
proof. Production conformance remains separately bounded because it can launch
billable infrastructure.
