# Placement scenario harness

Each scenario in `scenarios/` states a world (rentals with running work and remaining runtime, cached image layers and named data caches, marketplace offers with pricing and provisioning estimates), an incoming run request, and the placement decision Mercator must record. The runner drives the real orchestrator and scheduler, then asserts only on the events in the run's stream: `compute.run.placement_decided.v1` for the decision and its per-candidate evidence, `compute.run.launch_intent_recorded.v1` for the recorded cleanup disposition. Scheduler internals stay invisible.

This corpus is the design target for the warm-rentals program (bucket-rails issue 1044). Scenarios that need unbuilt semantics carry `"status": "target"` and read as the contract those milestones must satisfy.

## Green and target scenarios

A scenario's `status` decides how the runner treats failures:

- `green` asserts behavior Mercator has today. Any failure fails CI as a regression.
- `target` encodes the future contract. Failures are reported as pending (the test skips, with the full diff of what happened instead). A target scenario that starts passing fails CI until someone promotes it to green, so the corpus always states exactly where the program stands.

## Fixture shape

A single-decision scenario:

```json
{
  "summary": "One line saying why the expected decision is right.",
  "status": "green",
  "world": {
    "images": {
      "trainer:v1": {"layers": [{"name": "cuda-base", "size": "18GB"}]}
    },
    "rentals": [
      {
        "id": "rental-warm",
        "state": "idle",
        "idle_lease_expires_in": "30m",
        "cached_images": ["trainer:v1"],
        "named_caches": {"dataset-imagenet-2a41": "40GB"},
        "rate_per_hour_usd": 2.5
      }
    ],
    "marketplace": [
      {
        "id": "fresh-4090",
        "rate_per_hour_usd": 2.0,
        "provisioning": {"expected": "4m", "p90": "8m"},
        "facts": {"ssh": true, "nvidia_driver": true}
      }
    ]
  },
  "request": {
    "image": "trainer:v1",
    "expected_runtime": "20m",
    "max_runtime": "1h",
    "cache_mounts": [{"name": "dataset", "key": "dataset-imagenet-2a41", "size": "40GB"}]
  },
  "expect": {
    "outcome": "place",
    "offer": "rental-warm",
    "disposition": "release",
    "candidates": {
      "rental-warm": {"feasible": true, "pull_seconds": 0},
      "fresh-4090": {"provision_seconds": {"at_least": 240}}
    }
  }
}
```

Scenarios that advance the clock or submit several runs replace `request`/`expect` with a `timeline`: each step is exactly one of `submit` (a named run with its request and expectation), `advance` (move the scripted clock), or `reevaluate` (drive a named run's next advancement and assert its latest decision).

Conventions:

- durations are Go syntax ("6m", "1h30m"); sizes use decimal units ("40GB", "512MB")
- the world clock starts at 2030-01-01T00:00:00Z unless `world.clock` says otherwise; deadlines are offsets from that start ("+6m")
- layer names are content identity: two images listing the same layer name share that layer
- rentals default to a generous GPU-box inventory (8 CPUs, 32GB memory, 200GB disk); state only the resources the scenario is about
- `expect.outcome` is `place` (a selected offer), `defer` (a reason and deadline, no selection), or `fail` (a recorded decision with no feasible offers)
- numeric candidate expectations are exact (`"pull_seconds": 0`) or bounded (`{"at_least": 240}`)

## Target contracts pinned here

Target scenarios assert shapes that no domain type carries yet. The runner reads them from the decision's raw JSON, which pins the contract the milestones must implement:

- a deferral is a `placement_decided` event whose decision selects no offer and carries `"defer": {"reason", "deadline"}`
- named-cache evidence is `"cache_evidence": [{"key", "hit"}]` on each candidate, recording hit or miss per declared cache key
- host facts are rejected with the existing violation vocabulary: a fact present and false is `CAPABILITY_MISMATCH` at `facts.<name>`, a fact absent is `UNKNOWN_FACT` at `facts.<name>`

## Backends

`Backend`/`Session` (runner.go) is the seam between the scenario contract and the capacity behind it. `SimBackend` (sim.go) runs decision correctness against simulated capacity: the fake adapter's `World` (daemons with layer and cache state, scripted running work, a scripted clock) under the real orchestrator, scheduler, and a real SQLite event log. It is fast enough for hundreds of cases. A later backend can execute the same fixtures against real daemons and providers to verify what the simulation assumes; nothing in the fixtures is simulation-specific.

The simulation stays inside today's offer vocabulary: a busy rental advertises unavailable capacity with its remaining max runtime as queue evidence, an expired idle lease removes the rental from the offer list, and image-layer state becomes honest `ImageCache.MissingBytes` for the image being placed. What the vocabulary cannot express yet (named caches, host facts, cache mounts), the backend reports as notes so pending results say what was dropped.
