# Mercator

[![CI](https://github.com/benngarcia/mercator/actions/workflows/ci.yml/badge.svg)](https://github.com/benngarcia/mercator/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/benngarcia/mercator)](https://github.com/benngarcia/mercator/releases/latest)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

Modal-style push-to-run for containers, on capacity you control: Mercator
starts each workload fast on the warmest machine in your fleet, rents new GPU
capacity when none fits, and records what it decided and why.

[About](#about) ·
[How it compares](#how-it-compares) ·
[Quickstart](#quickstart) ·
[Documentation](#documentation) ·
[Contributing](#contributing-and-developing) ·
[Roadmap](#roadmap-and-status)

## About

Mercator is a compute broker and fleet manager for accelerated workloads. You
hand it a container. It compares every place that could take it: machines you
already rent, and fresh capacity from your GPU provider accounts. It books the
workload where it should start fastest for the money, launches it, and watches
it to a clean exit.

A few words carry precise meanings here. An **offer** is one concrete place
Mercator could run your container, with its platform, resources, accelerators,
and price: your local Docker host today, a RunPod or Vast.ai GPU when you
connect one. A **rental** is a machine Mercator keeps after a run instead of
releasing it, with a schedule that can queue the next runs behind the current
one. Your **fleet** is the set of rentals you currently hold. **Warmth** is
how much of a run's needs a rental already has locally: the image layers it
holds and the data caches it has populated. Warm machines start work in
seconds because there is less to pull and nothing to re-sync.

Today placement scores price and start latency, and can queue a booking on a
busy rental when waiting beats provisioning (the wait estimate uses each
queued booking's expected runtime from reservation, not elapsed progress). Scoring warmth
itself, image layers and declared cache mounts, is the current program of
work; [Roadmap and status](#roadmap-and-status) says exactly what is shipped
and what is being built.

Every decision is recorded. Three weeks later you can still ask what ran
where, why there, which candidates lost and for what reason codes, and whether
it exited clean, and get an answer out of the event log rather than out of
your memory. That audited record is the part `docker run`, and most brokers,
cannot give you.

It is one Go process with SQLite as the event log. No Kubernetes, no Slurm, no
cluster control plane. The JSON HTTP API, the CLI, and the operator console
all come out of the same binary.

![Mercator console showing runs](docs/assets/mercator-runs.png)

Mercator is V1 evaluation-ready, not GA infrastructure. Read
[known limitations](docs/production/known-limitations.md) before you rely on it
for production workloads.

## How it compares

Like Modal, you push a workload with one command and never pick a machine.
Unlike Modal, you bring an OCI image rather than Python code (Mercator does
not build or sync your code), and runs land on capacity you control: your own
Docker hosts and your own accounts with GPU providers, so there is no
serverless markup and your fleet is yours.

Like SkyPilot, Mercator brokers one workload across many providers. Unlike
SkyPilot, it does not bootstrap a conda environment on the machine before your
code runs. The container is the environment, so a cold start is an image pull,
and a warm start skips the layers the machine already holds.

## Who it is for

Use Mercator if you are a small team running recurring accelerated jobs,
training, batch inference, image generation, on GPUs you rent from providers
like RunPod or Vast.ai, and you want Modal's ergonomics without giving up
control of the machines or paying serverless prices. The recurring part is
where the fleet pays off: when your image-generation job and your training job
declare the same data cache, the second one can land on the machine that
already synced it.

It also fits if you run ordinary container workloads on a handful of machines,
have outgrown `ssh in && docker run`, and are nowhere near wanting Kubernetes,
Slurm, or Nomad.

Skip it for now if you need multi-node failover, per-user authorization, or TLS
today, or if you only ever target one local host and never need the audit
trail. A shell script around `docker run` is genuinely fine for that.

Mercator is built by a solo maintainer, in the open, with nothing to sell. No
company, no upsell, no telemetry. For the right person that is a feature. Set
your support expectations accordingly, and see [SUPPORT.md](SUPPORT.md).

## Quickstart

You need a running Docker daemon. Install the binary with Go, or download one
from the [releases page](https://github.com/benngarcia/mercator/releases/latest)
if you would rather not build:

```sh
go install github.com/benngarcia/mercator/cmd/mercator@latest
```

Start the broker. On a loopback address it generates its own API token, writes
a `local` CLI context, and, when the local Docker daemon answers, seeds and
authorizes a `docker` connection, so nothing else on this machine needs
configuring:

```sh
mercator serve
```

In another shell, run something on it:

```sh
mercator run create busybox -- echo hi
```

`run create` resolves `busybox` against your Docker host, so the run it records
is pinned to the image digest and the platform that image was actually built
for. You pin nothing by hand.

If Docker was not running when you started the broker, no connection is seeded:
start Docker and restart, or add it yourself with `mercator connection create
--adapter-type docker`. A `docker` connection you delete stays deleted; the
broker never seeds it again.

Then ask the question Mercator exists to answer:

```sh
mercator run decision
```

Abridged, that answers with the offer it picked and why:

```json
{
  "candidates": [
    {
      "offer_snapshot_id": "off_960b765d0b608632bf7083a0fdcc1032b851e4fa...",
      "connection_id": "docker",
      "feasible": true,
      "score_usd": 0.0005
    }
  ],
  "selected_offer_snapshot_id": "off_960b765d0b608632bf7083a0fdcc1032b851e4fa...",
  "selection_reason_codes": ["FEASIBLE", "LOWEST_SCORE"]
}
```

On a single Docker host there is one offer, so it wins uncontested. Every run
records the selected offer, every rejected candidate, and the reason codes, and
that record fills in as you add offers.

Open the console at [`http://127.0.0.1:8080`](http://127.0.0.1:8080). The
Workspace canvas is the console home: each rental with the run it is
executing and the bookings queued behind it, updating live as events stream
in. Paste the token from the `serve` log when it asks and pick your workspace
in the switcher, or start the broker with `mercator serve --dev` to skip the
token prompt on a loopback address.

![Mercator console demo: runs list, run detail, placement decision, and events](docs/assets/mercator-demo.gif)

Commands take the ids you care about and default the rest: the workspace when
the broker has one, the connection when the workspace has one, the run when you
mean the last one. When any of those is genuinely ambiguous, Mercator names the
candidates instead of guessing.

To run the broker itself in a container, see
[install and configuration](docs/production/install-configuration.md).

## Documentation

| Need | Start here |
| --- | --- |
| Install, start, health checks | [install and configuration](docs/production/install-configuration.md) |
| First local evaluation on Docker | [Docker adapter operation](docs/production/docker-adapter-operation.md) |
| CLI commands and environment | [CLI reference](docs/reference/cli.md) |
| HTTP and OpenAPI routes | [OpenAPI overview](docs/reference/openapi.md) |
| Workload and run lifecycle | [workload and run lifecycle](docs/production/workload-run-lifecycle.md) |
| Authentication and workspaces | [authentication and workspaces](docs/production/authentication-workspaces.md) |
| Security boundaries | [security model](docs/production/security-model.md) |
| What does not work yet | [known limitations](docs/production/known-limitations.md) |

Provider runbooks: [RunPod](docs/production/runpod.md),
[Shadeform](docs/production/shadeform.md), [Vast.ai](docs/production/vast.md).
`mercator verify --spec trial.json` launches a bounded, real conformance trial
against any of them and returns a sanitized evidence bundle. See
[provider conformance](docs/production/provider-conformance.md).

Project process lives under [docs/project](docs/project) and
[docs/launch](docs/launch).

## Contributing and developing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. Keep
changes narrow, include tests or docs evidence, and update production docs when
behavior changes.

```sh
go test ./...
go build ./cmd/mercator
scripts/check-open-source-launch.sh
```

The console is a React app in [web/app](web/app/README.md), built into
`web/static` and embedded in the binary. Rebuild it with `mise run ui`. Builds
need no cgo, because Mercator uses the pure-Go SQLite driver
`modernc.org/sqlite`.

Project spaces are covered by the [code of conduct](CODE_OF_CONDUCT.md).
Maintainer decision rules are in [GOVERNANCE.md](GOVERNANCE.md). Report security
issues privately as described in [SECURITY.md](SECURITY.md).

## Roadmap and status

Mercator runs real workloads end to end and is exercised by a full Go test
suite. It is ready to evaluate, and it is not yet ready to be the thing you page
someone about.

| # | Step | Status |
| :-: | --- | :-: |
| 1 | Event-sourced run lifecycle with recorded outcomes and cleanup | ✅ |
| 2 | Placement decisions that record the winner and every rejection | ✅ |
| 3 | Local Docker adapter with real container launch and cleanup | ✅ |
| 4 | GPU provider adapters behind one run contract | 🚧 |
| 5 | Rentals with persisted schedules of queued bookings | ✅ |
| 6 | Live fleet canvas in the console, streamed over SSE | ✅ |
| 7 | Warmth-aware placement: image layers and cache mounts | 🚧 |
| 8 | Multi-node operation: failover, TLS, per-user authorization | ❌ |

#### Provider adapters

RunPod, Shadeform, and Vast.ai adapters exist and share the run contract the
Docker adapter uses. They are experimental. Each one has a runbook and a
conformance trial that proves credentials, pricing, launch, signed workload
reporting, and terminal cleanup against the real provider. What is still thin
is registry credential handling and the setup documentation around quotas.

#### Warm placement

Rentals, their schedules, and queued bookings are modeled, persisted, and
scored today: placement weighs the expected runtime queued on a busy rental
(measured from reservation, not from evaluation time) against the cost and
latency of provisioning fresh capacity. What is being built on top is
the warmth program: advancing a schedule the moment the running booking
finishes, scoring the image layers a rental already holds, and cache mounts,
named data mounts that persist on a rental so runs sharing data land where the
data already is. The
[placement scenario corpus](internal/scenario/README.md) states the decisions
this program has to satisfy. Scenarios marked `target` are the contract for
work that is not built yet, and they fail CI the moment they start passing, so
the corpus always says exactly where the program stands.

#### Multi-node operation

Mercator is one process with one SQLite event log, one bearer-token principal
plus audited OIDC identities, and no built-in TLS. Backup and restore are
manual. These are the gaps that separate evaluation from production, and they
are tracked in [known limitations](docs/production/known-limitations.md) and
[the roadmap](ROADMAP.md).

## License

Mercator is licensed under the Apache License, Version 2.0. See
[LICENSE](LICENSE) and [NOTICE](NOTICE).
