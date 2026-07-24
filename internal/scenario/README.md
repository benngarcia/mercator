# Mercator Lab scenario catalog

`internal/scenario` owns Mercator's canonical Scenario Blueprint contract and
catalog. A Blueprint describes a possible world, authored arrivals and faults,
and externally visible evidence Mercator must produce. Placement regression
fixtures continue to run through the real orchestrator and Placement
implementation over SQLite.

Every versioned document starts with:

```json
{
  "schema": "mercator.lab/blueprint.v1",
  "classification": "green",
  "kind": "regression"
}
```

`kind` defaults to `regression`. The other catalog kinds are `generated`,
`minimized`, `demo`, and `conformance`. An optional sibling
`<blueprint>.ui.json` file carries semantic UI checkpoints. Browser metadata
never enters the Blueprint domain model.

`LoadBlueprint` accepts Blueprint v1 and rejects unknown versions. It also
provides the one-way compatibility path for unversioned placement fixtures:
mutable image tags and synthetic layer names become deterministic synthetic
digests, and content-keyed dataset caches become immutable Artifacts. Versioned
Blueprints reject those legacy forms.

`OpenCatalog` loads Blueprints recursively and attaches UI sidecars.
`LoadCorpus` remains the top-level placement-runner adapter while the later Lab
execution slices come online.

## Green and target classification

Classification controls how a runner treats failed expectations:

- `green` asserts behavior Mercator has today. Any failure is a regression.
- `target` states desired behavior that is not built yet. Its failures remain
  pending. A target that starts passing fails until someone deliberately
  promotes it to green.

Every target declares `missing_capabilities`. Green Blueprints declare none.
Fixture parse, schema, and coherence errors always fail. Missing capabilities
only explain why valid executed expectations remain red.

The 12 top-level Placement Blueprints remain four green and eight target.
`demos/artifact-warmth-restart.json` is the complete 15-checkpoint target for
Mercator Lab.

## Placement fixture shape

A single-decision Blueprint:

```json
{
  "schema": "mercator.lab/blueprint.v1",
  "classification": "target",
  "summary": "The Rental holding the immutable input beats a colder Rental.",
  "missing_capabilities": ["artifacts", "artifact_evidence"],
  "world": {
    "images": {
      "trainer@sha256:5d7e0dc3bcc75e4b3639ed8b3badf9b610b97221c7f8013edc0beebcf34fbc58": {
        "layers": [
          {
            "digest": "sha256:2d0fa50ae86c5b612afb532d93850529d2c65dad1e40e8b8904b0967309984de",
            "size": "18GB"
          }
        ]
      }
    },
    "artifacts": [
      {"id": "artifact:imagenet:v2.41", "size": "40GB"}
    ],
    "rentals": [
      {
        "id": "rental-warm",
        "artifact_replicas": ["artifact:imagenet:v2.41"],
        "cache_mounts": ["compiler-cache"],
        "rate_per_hour_usd": 2.5
      }
    ]
  },
  "request": {
    "image": "trainer@sha256:5d7e0dc3bcc75e4b3639ed8b3badf9b610b97221c7f8013edc0beebcf34fbc58",
    "consumes_artifacts": ["artifact:imagenet:v2.41"],
    "cache_mounts": [{"name": "compiler-cache"}]
  },
  "expect": {
    "outcome": "place",
    "offer": "rental-warm",
    "candidates": {
      "rental-warm": {
        "artifact_evidence": {"artifact:imagenet:v2.41": "hit"}
      }
    }
  }
}
```

`request` and `expect` are the single-decision shorthand. A Placement fixture
that advances virtual time or submits several Runs uses `timeline`; each step
is exactly one `submit`, `advance`, or `reconcile`.

An arrival-driven Lab Blueprint uses:

```json
{
  "seed": "stable-semantic-seed",
  "arrivals": {
    "type": "fixed",
    "runs": [
      {"name": "producer", "at": "0s", "request": {}},
      {"name": "consumer", "at": "0s", "request": {}}
    ]
  },
  "faults": [],
  "proof": []
}
```

The initial authored arrival type is `fixed`. Periodic and burst families land
with the typed generator slice. Faults and proof checkpoints are typed and
strictly validated before execution.

## Identity and units

- Durations use Go syntax such as `"6m"` and `"1h30m"`.
- Sizes use decimal units such as `"40GB"` and `"512MB"`.
- Image references are digest-pinned OCI identities.
- Image layers use exact `sha256:` digests. Shared digests mean shared content.
- Artifacts are immutable and versioned. Runs declare
  `consumes_artifacts` and `produces_artifacts`; Rentals carry exact
  `artifact_replicas`.
- Cache Mounts are mutable application-owned state. Their only identity is the
  workspace-scoped `name`; they never carry content keys or sizes.
- The world clock starts at `2030-01-01T00:00:00Z` unless `world.clock` says
  otherwise. Placement deadlines are offsets such as `"+6m"`.

Rentals default to a generous GPU-box inventory. State only resources relevant
to the scenario. `world.rental_schedules` belongs to Mercator and references
Rentals by ID. A nonempty schedule has a positive version, exactly one running
Booking, and at most four ordered queued Bookings.

Every Booking carries stable Booking and Run IDs. Max runtimes are enforced
bounds. Expected runtimes are p50 estimates used for projected starts and
queue-delay scoring.

## Target Placement evidence

Targets pin event contracts that production types may not carry yet:

- assigning a Run records a Booking with ID, Rental ID, state, predecessor,
  projected start, latest start, and schedule version;
- a busy Rental candidate records ordered Rental Schedule evidence;
- a full schedule rejects with `SCHEDULE_FULL` at
  `rental_schedule.queued`;
- Artifact locality is
  `"artifact_evidence": [{"artifact_id", "present"}]` on each candidate;
- a false host fact is `CAPABILITY_MISMATCH` and an absent fact is
  `UNKNOWN_FACT`.

## Placement backend

`Backend` and `Session` are the seam between the placement fixture and its
capacity implementation. `SimBackend` uses the fake provider world under the
real orchestrator, Placement implementation, and SQLite event log. Tests assert
recorded events, never private Placement state.

The current backend can execute offer, image-layer, and basic Rental behavior.
It records explicit notes when an Artifact, Cache Mount, seeded Rental
Schedule, or host fact cannot yet cross the production seam. Later Lab slices
replace this mutable scripted boundary with World Truth, Observed State, and a
deterministic dispatcher while keeping the real control plane in the loop.
