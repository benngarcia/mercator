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
`demos/artifact-warmth-restart.json` is Mercator Lab's complete
15-checkpoint green demonstration. `mercator lab promote` accepts its
classification change only when one Run Bundle proves every checkpoint.

## Placement fixture shape

A single-decision Blueprint:

```json
{
  "schema": "mercator.lab/blueprint.v1",
  "classification": "green",
  "summary": "The Rental holding the immutable input beats a colder Rental.",
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
      {
        "id": "artifact:imagenet:v2.41",
        "content_digest": "sha256:1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a",
        "size": "40GB"
      }
    ],
    "rentals": [
      {
        "id": "rental-warm",
        "artifact_replicas": [
          {"artifact": "artifact:imagenet:v2.41", "state": "verified"}
        ],
        "cache_mounts": [
          {"name": "compiler-cache", "compatibility_key": "cuda-12.4", "size": "8GB"}
        ],
        "rate_per_hour_usd": 2.5
      }
    ]
  },
  "request": {
    "image": "trainer@sha256:5d7e0dc3bcc75e4b3639ed8b3badf9b610b97221c7f8013edc0beebcf34fbc58",
    "consumes_artifacts": ["artifact:imagenet:v2.41"],
    "cache_mounts": [
      {"name": "compiler-cache", "compatibility_key": "cuda-12.4", "size": "8GB"}
    ]
  },
  "expect": {
    "outcome": "place",
    "offer": "rental-warm",
    "candidates": {
      "rental-warm": {
        "stages": {"artifact_fetch": {"seconds": 0, "source": "artifact_inventory"}},
        "artifact_evidence": {"artifact:imagenet:v2.41": "hit"}
      }
    }
  }
}
```

`artifact_evidence` says what each candidate was found holding of each Artifact
the Run reads: `"hit"` for a checked copy of exactly that version, `"miss"` for
none, and `"unknown"` for a machine that could not enumerate its copies at all.
A copy nobody checked is a miss, because it is not evidence the right bytes are
here.

`stages` is what that candidate was predicted to spend on each stage of the
launch, by the stage's own name: `acquisition`, `boot`, `agent_ready`,
`image_fetch`, `unpack`, `artifact_fetch`, `container_start`, and
`application_ready`. Each states any of `seconds`, `source`, `confidence`,
`level`, and `samples`, and the first three belong together because zero seconds
means two opposite things: nothing to do where somebody answered, and nobody
could say where nothing did. A
name outside those eight is refused at load, because the record answers about an
unknown stage with a zero estimate from no source, so a misspelled key would assert
nothing and quietly replace the assertion the fixture was written for.
What the world then spends on the last three is `world.launch`, stated separately
from any of this so that no actual is derived from the prediction it is measured
against. `world.launch.application_never_ready` is the world where the process runs
and the application behind it never reports that it can do work, which has to be
stated because an omitted `application_ready` already means a world that spends
nothing on the stage.

`level` and `samples` are where a stage's answer came from: `exact_candidate` for
measured launches of this candidate, then `provider_and_region`, then `provider`,
then `prior` for a stage nothing has ever measured, which is a published claim, a
stated constant, or what the workload declared about itself. `samples` is how many
measured launches stand behind the answer, and zero is a real assertion because it
is the one a prior makes. A fixture about the hierarchy states both beside the
seconds: the same ninety seconds read from this machine's own launches and from
the province it sits in are different claims, and only these say which was made.
A machine that has to answer differently from the rest of the fleet states its own
`application_ready` on its listing; a marketplace that names the hardware behind a
listing states `machine`, which is what lets one machine be published under two
listing IDs.

A listing's `capacity` is what its provider negotiated over the machine, in the
shape a `CapacityProvider` really answers with: `stop` and `resume` for suspending
a machine and bringing the same one back, `persistent_disk` for a stopped machine
that keeps its disk, `spot` for interruptible capacity, `exact_pricing` for a rate
Mercator can bill against, `idempotent_provision` for what the provider honours
when the same provision is asked twice (`operation_key` or `none`), `list_owned`
for whether Mercator can ask what it owns there, and `observe_after_terminate` for
whether a destroyed machine can still be looked at. A set that contradicts itself
is refused at load by the contract that owns it, so a fixture cannot state a
provider Mercator would refuse to build a connection for: a resume without a stop,
a persistent disk without a stop, and no deduplication with no owned listing are
each impossible. `capacity` and `bootstrap` belong to the reusable lane and are
refused on an `ephemeral` listing, because `Declare` admits a `CapacityProvider`
only alongside a `NodeRuntime` and stamps every such connection reusable: a
one-shot execution product holds nothing after its workload exits, so it has no
machine to stop, no machine to bring back, and no agent to enrol. A listing that
states any of it must also name its `machine`, because every promise in the set is
about one machine keeping its identity and a listing ID is numbered afresh on every
search.

A listing's `bootstrap` is how the node agent arrives on a fresh machine.
`never_enrolls` is a machine the provider allocates and boots whose agent never
opens a session, which has to be stated because an omitted `agent_ready` stage
already means enrolment that costs nothing: a listing that says its agent never
enrols states no `agent_ready` at all. `deadline` is how long Mercator goes on
expecting that session, and `reclaim_after` is the provider's own backstop that
destroys the machine whatever the control plane does. A listing whose agent never
enrols must name one of the two, because a machine nobody gives up on bills for
ever.

`request` and `expect` are the single-decision shorthand. A Placement fixture
that advances virtual time or submits several Runs uses `timeline`; each step
is exactly one `submit`, `advance`, or `reconcile`.

`request.service_class` is the kind of work the Run says it is, which is the only
thing that says what a second of waiting is worth to it and therefore what the
score is computed over. `request.max_cost_usd` and `request.max_start_latency`
are the two bounds the class cannot argue with: a class states an exchange rate
and can always be talked into a costlier or a later machine, and these say how
far. A candidate over the cost bound is refused `COST_LIMIT_EXCEEDED` at
`placement.max_expected_cost_usd`, and a bound of zero dollars is refused at
load, because a fixture whose budget refuses every quoted machine is a world to
state on purpose rather than by leaving a number out.

`request.group` is the family of Runs this one arrived with and how wide that
family may run at once, as `{"id": "sweep", "max_parallel": 3}`. Every member
states the width, because a group is a label the work carries rather than an
object an operator creates first, and a name without a width or a width without a
name is refused where the Run enters. It is a bound on the work rather than a
preference for a machine: a member whose family is already that wide is deferred
`GROUP_AT_PARALLELISM` with capacity standing idle beside it, and the wait ends
when a member of the same family finishes rather than when a machine comes free.

The other bounds are the class's own and no request states them: how long a Run
may be kept waiting, and the moment it must have started by. Both are measured
from when admission first told the Run to wait, both end the wait rather than
describe it, and a fixture states either by letting virtual time pass and
asserting the `refuse` below. The queue delay is the earlier of the two in every
class, and it is the only one a class that declares no deadline has.

`outcome` is one of four sentences about one Run. `place` and `fail` are about a
Booking Decision: an offer was selected, or none was. `defer` and `refuse` are
about admission, which decides before Placement is asked, and each states a
`deferral`:

```json
{
  "outcome": "defer",
  "deferral": {
    "reason": "BEHIND_HIGHER_PRIORITY",
    "behind": ["watched"],
    "effective_priority": 50,
    "queued_seconds": 60
  }
}
```

`reason` is why the Run is not running: `NO_FEASIBLE_OFFER` for a wait on
capacity to come free, `NO_CAPACITY_FITS` for a wait on capacity to be added
because nothing the fleet published can hold this Run, whether it weighed machines
and refused every one of them or published nothing this ask even matches,
`CAPACITY_UNSTATED` for a wait on a machine that has not said enough for anybody to
tell, which is what an enrolled node whose disk probe failed publishes,
`BEHIND_HIGHER_PRIORITY` for a Run the queue in front of it
outranks, and `GROUP_AT_PARALLELISM` for a Run whose own family is already as wide
as its caller declared, which says nothing about capacity at all and carries no
`fleet` answer because no machine was weighed. Two reasons are what a `refuse` states, and each is a bound on waiting
that has gone by: `QUEUE_DELAY_EXCEEDED` for a Run Mercator has already kept
waiting longer than its class allows, and `DEADLINE_UNREACHABLE` for one where the
moment its class says it must have started by is already past. `behind`
names the work the record says is in front of it, by the fixture's own names for
those Runs, and it is a whole-set assertion: a deferral naming half of what a Run
waits for is a queue an operator cannot read. `effective_priority` and
`queued_seconds` are what the record says the Run was worth and how long it had
been waiting, which is the only visible evidence that waiting promotes anything.

`fleet` is the answer the fleet gave about this Run, which is the evidence the
reason is derived from and the classification the queue is ordered on. Its
`weighed` and `could_hold` are how many machines the fleet published that this Run
was measured against and how many of those could have taken it once the capacity
they are spending came back, and a machine that is both busy and too small counts
in the first and never in the second. `unstated` is how many of them refused this
Run only for facts nobody published, which is a third answer rather than either of
the other two: a machine that could not measure its disk is not a machine with no
room, and counting it as one lets a failed measurement say a whole fleet has
nothing. `"absent": true` states the opposite: this
wait rests on no answer about capacity at all, because the fleet was never asked.
A fixture says that rather than asking for two zeroes, because a fleet that
published nothing an ask matches also weighed no machines and the two are opposite
statements.
`projected_wait_seconds` is the wait the record projects from the Bookings on the
capacity that could hold this Run, and zero states that nothing projected one.

An arrival-driven Lab Blueprint uses:

```json
{
  "seed": "stable-semantic-seed",
  "arrivals": {
    "type": "fixed",
    "runs": [
      {"name": "producer", "at": "0s", "request": {}},
      {"name": "consumer", "at": "0s", "workspace": "other-tenant", "request": {}}
    ]
  },
  "faults": [],
  "proof": []
}
```

The initial authored arrival type is `fixed`. Periodic and burst families land
with the typed generator slice. Faults and proof checkpoints are typed and
strictly validated before execution.

A fault with the `provider.launch` trigger and the `reject_command` action is a
machine refusing to start the work it was given. What follows a refusal is
another placement, and `request.max_pre_start_attempts` is the complete bound on
those placements: how many times Mercator hands a Run to a machine, the initial
attempt included, and one where a Blueprint says nothing. The refusals a Run
survives are therefore one fewer than the bound, so a Run left at the default
closes with `RETRY_EXHAUSTED` the moment any machine turns it away, and stating
one refusal and the redo that follows it takes `2`.

## Identity and units

- Durations use Go syntax such as `"6m"` and `"1h30m"`.
- Sizes use decimal units such as `"40GB"` and `"512MB"`.
- Image references are digest-pinned OCI identities.
- Image layers use exact `sha256:` digests. Shared digests mean shared content.
- Artifacts are immutable and versioned. An Artifact states its
  `content_digest`, and `produced_by` when a Run in the same Blueprint publishes
  it; an Artifact with a producer is not durable at virtual time zero and no
  machine may be seeded holding a copy of it. Runs declare `consumes_artifacts`
  and `produces_artifacts`. Rentals carry exact `artifact_replicas`, each
  stating whether that copy was checked against the catalog (`verified`) or is
  merely present (`unverified`). The object store is what makes an Artifact
  consumable; a replica only makes reading it faster.
- Cache Mounts are mutable application-owned state. Their only identity is the
  workspace-scoped `name`, and they never carry a content key: a key that
  identifies content is what an Artifact version is for. A Run declares
  `cache_mounts` with a `name`, the `compatibility_key` naming which generation
  of content it can use, and the `size` it expects to take; a Rental holds
  `cache_mounts` with the same fields plus the `workspace` that owns each one.
  Mercator compares the compatibility key and never interprets it.
- A Run arrival states the `workspace` it belongs to. It is a label rather than
  an identity, because each backend mints its own workspace IDs, and an omitted
  label is the Blueprint's default workspace. Two labelled tenants on one machine
  is what makes cross-workspace isolation something a fixture can state, and an
  Artifact belongs to the workspace that declared it, so a Run outside the
  default workspace may not name one.
- The world clock starts at `2030-01-01T00:00:00Z` unless `world.clock` says
  otherwise. Placement deadlines are offsets such as `"+6m"`.

Rentals default to a generous GPU-box inventory. State only resources relevant
to the scenario. `resources.disk_unmeasured` is the machine that could not measure
its disk at all, which is a different fixture from a machine with no room and is
refused as a silence rather than as a shortfall. A marketplace listing is a search
result, so both worlds publish one only to an ask its room and its cards match;
capacity Mercator holds is listed whole and refused in the record.
A Rental states `reclaimable` when whoever sold it may take the machine back while
Mercator is still using it, which is the term a spot ask is sold on. A Run whose
class does not permit interruption is refused such a machine with
`INTERRUPTION_NOT_PERMITTED`, before anything starts, because nothing Mercator
holds survives a machine being reclaimed. `world.preemptions` is the provider
taking one back, as `{"rental": "rental-spot", "at": "5m"}`, and it may only name a
Rental this world declared reclaimable: a world reclaiming anything else would be
describing a provider breaking its own contract. Whatever was running there is
gone, and Mercator learns of it the way it does in production, by looking.
A machine's `billing` says how it is charged rather than how much: `setup_fee_usd`
to hand it over, `minimum_charge` for the shortest allocation its publisher sells,
and `granularity` for the block of time it bills in. A machine sold by the hour
charges the hour for twenty minutes of use, and the part of the block nothing uses
is charged to the placement that bought it. Stating no granularity is a publisher
that bills continuously.

A Rental's `terms` are what it was sold on beyond its price, every duration
measured from the world's own start. `committed_for` is how long Mercator already
owes rent whatever it does with the machine, which is what makes a Run that fits
inside the interval cheap and a Run that overruns it expensive.
`eligible_service_classes` is the work the machine is held for, and every other
class is refused it with `CLASS_NOT_ELIGIBLE` rather than priced on it.
`available_for` is how long the capacity stays Mercator's, and work Mercator would
let hold the machine past that moment is refused with
`AVAILABILITY_WINDOW_CLOSES` before it starts. A marketplace listing carries none of
them, because nothing is owed on a machine that does not exist yet.

A candidate expectation states `cost` to pin what a machine costs and which parts
of a sale that price is made of: `usd` for the total, `terms` by the name the
record files each part under (`setup_fee`, `committed_rent`, `keep_alive`,
`idle_tail`), `committed_seconds` for how much of an already-owed interval the
placement spends, and `unpriced` for a machine nobody quoted. A term nothing
charges is refused at load, because the record carries only the terms it charged
and a fixture naming another would read the absence as agreement.

`world.rental_schedules` belongs to Mercator and references Rentals by ID. A nonempty schedule has a positive version, exactly one running
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
- a false host fact is `CAPABILITY_MISMATCH` and an absent fact is
  `UNKNOWN_FACT`.

## Placement backend

`Backend` and `Session` are the seam between the placement fixture and its
capacity implementation. `SimBackend` uses the fake provider world under the
real orchestrator, Placement implementation, and SQLite event log. Tests assert
recorded events, never private Placement state.

The current backend can execute offer, image-layer, and basic Rental behavior.
It records explicit notes when a Cache Mount, seeded Rental Schedule, or host
fact cannot yet cross the production seam. Later Lab slices
replace this mutable scripted boundary with World Truth, Observed State, and a
deterministic dispatcher while keeping the real control plane in the loop.
