# Mercator

Mercator runs OCI containers across Docker hosts and GPU providers, then keeps
the evidence that explains each placement and cleanup. You get a push-to-run
CLI on capacity you control, with one Go process and SQLite instead of a
separate cluster control plane.

- **One-command runs.** `mercator run create busybox -- echo hi` resolves the
  image, generates the run identifiers, selects capacity, launches the
  container, and tracks it through cleanup.
- **Four adapters.** Docker, RunPod, Shadeform, and Vast.ai implement the same
  offer, launch, observation, and cleanup contract.
- **Explainable placement.** Every decision records the selected offer, each
  rejected candidate, expected cost and latency, and stable reason codes.
- **Reproducible inputs.** A local image tag becomes a recorded digest and
  platform before the run launches.
- **Small control plane.** One Go binary contains the HTTP API, CLI, and
  embedded operator console, while SQLite stores the event log.
- **Evaluation-ready.** Mercator runs real workloads end to end, but it remains
  pre-1.0 software with [documented production gaps](docs/production/known-limitations.md).

```sh
mercator run create busybox -- echo hi
mercator run wait | jq -c '.run | {outcome, exit_code, cleanup}'
# {"outcome":"succeeded","exit_code":0,"cleanup":"confirmed"}
```

- [Get started](#get-started)
- [How it works](#how-it-works)
- [Where it fits](#where-it-fits)
- [Documentation](#documentation)
- [Roadmap and status](#roadmap-and-status)
- [Contributing](#contributing)

---

## Get started

You need Go 1.25 or newer and a running Docker daemon.

### 1. Install Mercator and pull the example image

```sh
go install github.com/benngarcia/mercator/cmd/mercator@latest
mercator_bin="$(go env GOBIN)"
export PATH="${mercator_bin:-$(go env GOPATH)/bin}:$PATH"
docker pull busybox:latest
```

You can also download a binary from the
[latest release](https://github.com/benngarcia/mercator/releases/latest).

### 2. Start the broker

```sh
mercator serve
```

On loopback, `serve` generates an API token, writes a `local` CLI context, and
creates an authorized `docker` connection when the daemon answers. Local CLI
commands need no further configuration.

### 3. Run a container

In another shell:

```sh
mercator run create busybox -- echo hi
mercator run wait | jq -c '.run | {outcome, exit_code, cleanup}'
mercator run decision \
  | jq -c '{candidates: (.decision.candidates | length), reason_codes: .decision.selection_reason_codes}'
```

The completed run reports:

```json
{"outcome":"succeeded","exit_code":0,"cleanup":"confirmed"}
{"candidates":1,"reason_codes":["FEASIBLE","LOWEST_SCORE","REUSE_EXISTING_RENTAL"]}
```

Open [`http://127.0.0.1:8080`](http://127.0.0.1:8080) to inspect runs and fleet
state in the operator console.

For a broker running in a container, explicit authentication, and remote
Docker hosts, continue with
[install and configuration](docs/production/install-configuration.md) and
[Docker adapter operation](docs/production/docker-adapter-operation.md).

---

## How it works

1. **Resolve the workload.** Mercator inspects a local image tag and records its
   digest and platform, so a later replay uses the same image revision.
2. **Collect offers.** Authorized Docker and GPU-provider connections describe
   their available or provisionable capacity through one typed contract.
3. **Evaluate placement.** Mercator rejects offers that cannot satisfy the
   workload, scores the feasible candidates by expected cost and latency, and
   books the lowest-scored offer.
4. **Run and reconcile.** The selected adapter launches the container,
   observes its external state, and confirms release or termination before the
   run closes.
5. **Preserve the evidence.** SQLite keeps the run lifecycle, placement
   decision, provider-neutral events, outcome, and cleanup state.

An **offer** is a place where a run could land. A **rental** is a machine
Mercator retains between runs. A **fleet** is the set of rentals you hold.
Mercator can queue work on a busy rental when waiting costs less than
provisioning fresh capacity.

![Mercator console showing runs](docs/assets/mercator-runs.png)

---

## Where it fits

Mercator is built for small teams running recurring training, batch inference,
image generation, and other accelerated container workloads on machines they
rent. It fits when the team wants a short push-to-run path and a durable answer
to what ran, where it ran, why that offer won, and whether cleanup finished.

The container is the deployment unit. Mercator does not build or sync
application code, and provider capacity stays in your own accounts. The
current runtime uses one process and one SQLite event log, so operators do not
need Kubernetes or Slurm to evaluate it.

> Mercator is not production GA. It does not provide multi-process failover,
> built-in TLS, per-user authorization, or a managed secret vault. Read the
> [known limitations](docs/production/known-limitations.md) before relying on
> it in production.

---

## Documentation

| Need | Start here |
| --- | --- |
| Install, start, and check health | [Install and configuration](docs/production/install-configuration.md) |
| Evaluate with local Docker | [Docker adapter operation](docs/production/docker-adapter-operation.md) |
| Use CLI commands and contexts | [CLI reference](docs/reference/cli.md) |
| Call the HTTP and OpenAPI routes | [OpenAPI overview](docs/reference/openapi.md) |
| Understand the run lifecycle | [Workload and run lifecycle](docs/production/workload-run-lifecycle.md) |
| Configure authentication and workspaces | [Authentication and workspaces](docs/production/authentication-workspaces.md) |
| Review security boundaries | [Security model](docs/production/security-model.md) |
| Check unsupported production behavior | [Known limitations](docs/production/known-limitations.md) |

Provider runbooks cover [RunPod](docs/production/runpod.md),
[Shadeform](docs/production/shadeform.md), and
[Vast.ai](docs/production/vast.md). These adapters remain experimental.
`mercator verify --spec trial.json` launches a bounded conformance trial against
an adapter and returns a sanitized evidence bundle. See
[provider conformance](docs/production/provider-conformance.md) for the trial
contract.

The versioned HTTP API is available as
[OpenAPI](docs/reference/openapi.md). Project process and launch material live
under [docs/project](docs/project) and [docs/launch](docs/launch).

---

## Roadmap and status

| Capability | Status |
| --- | --- |
| Event-sourced run lifecycle with recorded outcomes and cleanup | Shipped |
| Placement decisions with the winner and every rejection | Shipped |
| Local Docker launch and cleanup | Shipped |
| Persisted rental schedules and queued bookings | Shipped |
| Live fleet console streamed over SSE | Shipped |
| RunPod, Shadeform, and Vast.ai adapters | Experimental |
| Image-layer and cache-aware placement | Building |
| Multi-node failover, built-in TLS, and per-user authorization | Planned |

Rental scheduling already weighs queued runtime against the cost and latency
of provisioning fresh capacity. Image-layer and cache warmth do not affect
placement yet. The [placement scenario corpus](internal/scenario/README.md)
states the decisions that the scheduler must eventually satisfy, and
[ROADMAP.md](ROADMAP.md) tracks the wider program.

---

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. Keep
changes narrow, include tests or documentation evidence, and update production
docs when behavior changes.

```sh
go test ./...
go build ./cmd/mercator
scripts/check-open-source-launch.sh
```

The React console lives in [web/app](web/app/README.md), builds into
`web/static`, and embeds into the Go binary. Run `mise run ui` before building
the binary when console source changes. Mercator uses the pure-Go
`modernc.org/sqlite` driver, so Go builds do not require cgo.

Project spaces follow the [code of conduct](CODE_OF_CONDUCT.md). Maintainer
decision rules live in [GOVERNANCE.md](GOVERNANCE.md), support expectations in
[SUPPORT.md](SUPPORT.md), and private vulnerability reporting in
[SECURITY.md](SECURITY.md).

## License

Mercator is licensed under the Apache License, Version 2.0. See
[LICENSE](LICENSE) and [NOTICE](NOTICE).
