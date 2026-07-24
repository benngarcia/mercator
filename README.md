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
hand it a container. It compares every place that could take it, machines you
already rent and fresh capacity from your GPU provider accounts, books the
workload where it should start fastest for the money, launches it, and watches
it to a clean exit.

Four terms recur: an **offer** is a place a run could land (a Docker host, or a
RunPod or Vast.ai GPU once connected), a **rental** is a machine Mercator keeps
between runs, your **fleet** is the rentals you hold, and **warmth** is how much
of a run's image and data a rental already holds locally, which is why warm
machines start in seconds.

Every decision is recorded. Three weeks later you can still ask what ran where,
why there, which candidates lost and for what reason, and whether it exited
clean, and get the answer from the event log instead of your memory. That
audited record is the part `docker run`, and most brokers, cannot give you.

It is one Go process with SQLite as the event log. No Kubernetes, no Slurm, no
control plane. The JSON HTTP API, the CLI, and the operator console all come
out of the same binary.

![Mercator console showing runs](docs/assets/mercator-runs.png)

Mercator is V1 evaluation-ready, not GA infrastructure. Read
[known limitations](docs/production/known-limitations.md) before you rely on it
in production.

## How it compares

Like Modal, you push a workload with one command and never pick a machine.
Unlike Modal, you bring an OCI image rather than Python code, so Mercator does
not build or sync your code, and runs land on capacity you control, with no
serverless markup and a fleet that stays yours.

Like SkyPilot, Mercator brokers one workload across many providers. Unlike
SkyPilot, it does not bootstrap a conda environment first. The container is the
environment, so a cold start is an image pull and a warm start skips the layers
the machine already holds.

Use it if you are a small team running recurring accelerated jobs, training,
batch inference, image generation, on GPUs you rent, and you want Modal's
ergonomics without serverless prices or giving up the machines. Recurring is
where the fleet pays off: when two jobs declare the same data cache, the second
can land on the machine that already synced it. 

## Quickstart

You need a running Docker daemon. Install the binary with Go, or download one
from the [releases page](https://github.com/benngarcia/mercator/releases/latest):

```sh
go install github.com/benngarcia/mercator/cmd/mercator@latest
```

Start the broker:

```sh
mercator serve
```

Then, in another shell, push a workload and ask why it landed where it did:

```sh
mercator run create busybox -- echo hi
mercator run decision
```

On loopback, `serve` generates its own token, writes a `local` CLI context, and
seeds a `docker` connection when the daemon answers, so nothing else needs
configuring. `run create` resolves `busybox` to its digest and platform against
your host, so you pin nothing by hand. `run decision` returns the offer it
picked, every candidate it rejected, and the reason codes; on a single host the
one offer wins uncontested, and the record fills in as you add offers.

Open the console at [`http://127.0.0.1:8080`](http://127.0.0.1:8080) for the
live Workspace canvas. For the broker-in-a-container setup and the full
walkthrough with expected output, see
[install and configuration](docs/production/install-configuration.md) and
[Docker adapter operation](docs/production/docker-adapter-operation.md).

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
[provider conformance](docs/production/provider-conformance.md). Project process
lives under [docs/project](docs/project) and [docs/launch](docs/launch).

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
`modernc.org/sqlite`. Project spaces are covered by the
[code of conduct](CODE_OF_CONDUCT.md), maintainer decision rules are in
[GOVERNANCE.md](GOVERNANCE.md), and security issues go privately through
[SECURITY.md](SECURITY.md).

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

The RunPod, Shadeform, and Vast.ai adapters share the Docker adapter's run
contract and each has a runbook and a conformance trial, but they are
experimental: registry credential handling and quota setup are still thin.
Rentals and their schedules are scored today, weighing the runtime queued on a
busy rental against the cost of provisioning fresh capacity; scoring warmth
itself, image layers and cache mounts, is the program being built. The
[placement scenario corpus](internal/scenario/README.md) states the decisions
that program must satisfy, and its `target` scenarios fail CI the moment they
start passing, so the corpus always says where the work stands. Multi-node
operation, the gap between evaluation and production, is tracked in
[known limitations](docs/production/known-limitations.md) and
[the roadmap](ROADMAP.md).

## License

Mercator is licensed under the Apache License, Version 2.0. See
[LICENSE](LICENSE) and [NOTICE](NOTICE).
