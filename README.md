# Mercator

Run a container on the best place that can take it, and get an audited record of
why it landed there and how it exited — what Mercator decided, where it ran, the
exit code, and whether it cleaned up. Today that place is your local Docker host;
multi-provider placement (RunPod and other GPU providers) is emerging.

Under the hood, Mercator is an event-sourced run broker for OCI containers: one
Go process, a SQLite event log, a JSON HTTP API and CLI, and an embedded
console — no Kubernetes, Slurm, or cluster control plane.

![Mercator console showing runs](docs/assets/mercator-runs.png)

Watch the short console demo — the runs list, a run's lifecycle and exit code,
the placement decision that explains why it landed there, and the event-sourced
audit trail:
[WebM](docs/assets/mercator-demo.webm) or
[GIF fallback](docs/assets/mercator-demo.gif). A text transcript is in
[docs/assets/README.md](docs/assets/README.md#demo-transcript).

![Mercator console demo: runs list, run detail, placement decision, and events](docs/assets/mercator-demo.gif)

## Why It Exists

Small teams often outgrow "ssh into a box and start Docker" before they are
ready to operate Kubernetes, Slurm, or a custom GPU scheduler. The hard parts
show up quickly:

- placement decisions need to explain why one machine or provider won;
- retries need idempotency, not duplicate launches;
- operators need a run history, exit codes, cleanup status, and event audit;
- workloads should not leak secrets into public events;
- local Docker, standing pools, and cloud GPU providers need one run contract.

Mercator keeps that surface small. It runs as one Go process with SQLite as the
event log, exposes a JSON HTTP API and CLI, embeds an operator console, and
drives provider adapters through an auditable run lifecycle.

## What It Does

- Accepts a minimal image shorthand or a full OCI workload revision.
- Resolves and records immutable workload/run state in an event log.
- Filters offers by platform, resources, accelerator needs, capability facts,
  price constraints, and policy.
- Records placement decisions and rejected candidates for audit.
- Launches through Docker and RunPod-oriented adapter paths.
- Surfaces run status, exit codes, cleanup disposition, public events, sink
  cursors/replay, and workspace-scoped reads.
- Provides hand-written TypeScript, Python, and Ruby SDKs for the V1 API.

Mercator is **V1 evaluation-ready, not GA infrastructure yet**. See
[Known Limitations](docs/production/known-limitations.md) and
[Roadmap](ROADMAP.md) before relying on it for production workloads.

## The Model

An **offer** is one concrete place Mercator could run your container — your local
Docker host today, a RunPod GPU or a standing pool later — described by capability
facts (platform, resources, accelerators) and price. When you ask Mercator to run
a workload, it filters the available offers against what the workload needs,
**records which offer won and why (and which candidates it rejected)**, then
launches the container and watches it to a clean exit. That placement decision —
not the launch itself — is the part `docker run` can't give you.

## Who It's For

**For you if:** you run real container workloads on a handful of machines (one
box, a couple of hosts, a spot GPU), you've outgrown `ssh in && docker run`, but
you're nowhere near wanting Kubernetes, Slurm, or Nomad — and you want an
auditable answer to "what ran where, why there, and did it exit clean?"

**Not for you (yet) if:** you need multi-node failover, per-user auth, or TLS
today; you want a GA platform with a support SLA; or you only ever target one
local host and never need the audit trail (a shell script around `docker run` is
genuinely fine — Mercator adds idempotent retries, a recorded placement decision
across multiple offers, and a queryable run history on top of that).

Mercator is built by a solo maintainer, in the open, with nothing to sell — no
company, no upsell, no telemetry. For the right person that is a feature; set
your support expectations accordingly (see [SUPPORT.md](SUPPORT.md)).

## Try It In 5 Minutes

Mercator launches your workload as a real Docker container, so **a running
Docker daemon is the only thing you need** — no Go toolchain. From a source
checkout, plus `curl` and `jq` to talk to the API:

### 1. Run the broker (Docker)

```sh
docker build -t mercator:local .

docker run --rm \
  -e MERCATOR_ADDR=0.0.0.0:8080 \
  -e MERCATOR_ADAPTER=docker -e MERCATOR_DOCKER_ARCH=amd64 \
  -e MERCATOR_API_TOKEN=dev-token -e MERCATOR_AUTH_WORKSPACES=ws_1 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -p 8080:8080 mercator:local serve
```

The image build compiles Mercator inside the container, so you do not need Go
installed. Mounting `/var/run/docker.sock` lets the broker launch containers on
your host Docker — and grants the container root-equivalent control of that
daemon, which is fine on a machine you own for local evaluation but not on an
untrusted host.

### 2. Create a run and read the result

In another shell. On the Docker adapter a workload must reference a
**digest-pinned image** — a mutable tag like `busybox:latest` is rejected, and
tag→digest resolution against a registry is not implemented yet — so pin one
first:

```sh
export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_API_TOKEN=dev-token
export MERCATOR_WORKSPACE_ID=ws_1

docker pull -q busybox:latest >/dev/null
IMAGE="$(docker inspect --format '{{index .RepoDigests 0}}' busybox:latest)"

RUN_ID="$(curl -fsS -X POST "$MERCATOR_API_URL/v1/runs?workspace_id=$MERCATOR_WORKSPACE_ID" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: quickstart-1' \
  -d "{\"image\":\"$IMAGE\",\"args\":[\"echo\",\"hi\"]}" | jq -r '.run_id')"

curl -fsS "$MERCATOR_API_URL/v1/runs/$RUN_ID?workspace_id=$MERCATOR_WORKSPACE_ID" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  | jq '{outcome: .run.outcome, exit_code: .run.exit_code, cleanup: .run.cleanup, closed: .run.closed}'
```

Expected:

```json
{
  "outcome": "succeeded",
  "exit_code": 0,
  "cleanup": "confirmed",
  "closed": true
}
```

### 3. See why it ran there

The point of a broker is the placement decision — ask for it:

```sh
curl -fsS "$MERCATOR_API_URL/v1/runs/$RUN_ID/decision?workspace_id=$MERCATOR_WORKSPACE_ID" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  | jq '{selected: .decision.selected_offer_snapshot_id, candidate_count: (.decision.candidates | length)}'
```

```json
{ "selected": "offer_docker_loopback", "candidate_count": 1 }
```

On a single Docker host there is one offer, so it wins uncontested — but the
selected offer, every rejected candidate, and the reason codes are recorded for
each run, and that record fills in as more offers appear (a second host, a
RunPod GPU). Open the console at
[`http://127.0.0.1:8080/?workspace_id=ws_1`](http://127.0.0.1:8080/?workspace_id=ws_1)
and paste `dev-token` when prompted.

### From source (contributors)

With Go 1.25+, skip the image and use the binary directly — compile once, then
reuse it:

```sh
go build -o mercator ./cmd/mercator

export MERCATOR_ADDR=127.0.0.1:8080 MERCATOR_ADAPTER=docker MERCATOR_DOCKER_ARCH=amd64
export MERCATOR_API_TOKEN=dev-token MERCATOR_AUTH_WORKSPACES=ws_1
export MERCATOR_SQLITE_DSN="file:$HOME/.mercator/mercator.db" && mkdir -p "$HOME/.mercator"
./mercator serve   # then drive it with ./mercator run create "$IMAGE" -- echo hi
```

For the full Docker runbook — preconditions, container labels, and cleanup
verification — see
[docs/production/docker-adapter-operation.md](docs/production/docker-adapter-operation.md).

## Pick The Right Evaluation Path

The Docker adapter is the default local path: it launches real containers on a
single host with no provider account or billable compute. Use RunPod when you
need to prove the provisionable GPU-provider flow.

| Path | Use It To Prove | Requires | Start Here |
| --- | --- | --- | --- |
| Docker adapter | Broker lifecycle, placement, public events, cleanup, CLI, SDKs, and console with real local container launch, observation, and labels on one host. | Go 1.25+, a running Docker daemon, a digest-pinned Linux image, `jq`, and local host capacity. | [Docker adapter operation](docs/production/docker-adapter-operation.md) |
| RunPod adapter | Provisionable GPU-provider flow and terminate cleanup. | RunPod API key, a GPU workload, a real registry-pullable image digest, and workload exit-code reporting. | [RunPod runbook](docs/production/runpod.md) |

If the broker lifecycle passes but a specific provider does not, treat that as
adapter or environment evidence rather than broker evidence. Known gaps are
tracked in
[docs/production/known-limitations.md](docs/production/known-limitations.md).

## SDK Happy Path

The SDKs hide the low-level idempotency mechanics for the common case. A caller
can ask Mercator to run an image, wait for closure, and read the exit code from
the run object.

```python
from mercator import MercatorClient

client = MercatorClient("http://127.0.0.1:8080", token="dev-token", workspace_id="ws_1")

# On the Docker adapter the image must be digest-pinned (see the quickstart).
image = "busybox@sha256:<digest>"
created = client.run_image(image, args=["echo", "hi"])
result = client.wait_run_until_terminal(created["run_id"])

print(result["run"]["outcome"], result["run"]["exit_code"])
```

- TypeScript: [sdk/typescript](sdk/typescript/README.md)
- Python: [sdk/python](sdk/python/README.md)
- Ruby: [sdk/ruby](sdk/ruby/README.md)

First-launch SDK registry publishing is deferred. Until packages are published,
install SDKs from a source checkout; each SDK README includes copy-paste source
install commands.

## Console

The embedded React console is built into `web/static` and served by the Go
binary. It is meant for operators to scan runs, inspect placement decisions,
manage connections, and replay sink delivery.

![Mercator placement decision](docs/assets/mercator-run-decision.png)

Frontend source lives in [web/app](web/app/README.md). To rebuild the embedded
assets:

```sh
mise run ui
go build ./cmd/mercator
```

## Documentation Map

| Need | Start Here |
| --- | --- |
| Install, start, health checks | [docs/production/install-configuration.md](docs/production/install-configuration.md) |
| First local evaluation (Docker) | [docs/production/docker-adapter-operation.md](docs/production/docker-adapter-operation.md) |
| CLI commands and environment | [docs/reference/cli.md](docs/reference/cli.md) |
| HTTP/OpenAPI route overview | [docs/reference/openapi.md](docs/reference/openapi.md) |
| Workload and run lifecycle | [docs/production/workload-run-lifecycle.md](docs/production/workload-run-lifecycle.md) |
| Authentication and workspaces | [docs/production/authentication-workspaces.md](docs/production/authentication-workspaces.md) |
| Security boundaries | [docs/production/security-model.md](docs/production/security-model.md) |
| Known limitations | [docs/production/known-limitations.md](docs/production/known-limitations.md) |
| RunPod workload examples | [examples/runpod/README.md](examples/runpod/README.md) |

Project process — governance, compatibility, threat model, release process, and
launch prep — lives under [docs/project/](docs/project) and
[docs/launch/](docs/launch).

## Build And Test

```sh
go test ./...
go build ./...
go run ./cmd/mercator --help
scripts/check-open-source-launch.sh

cd sdk/typescript && npm ci && npm test
cd ../python && python3 -m unittest discover -s tests
cd ../ruby && bundle install && bundle exec ruby -Ilib:test test/test_client.rb
```

For the heavier launch gate that also exercises the local release archive
builder, run:

```sh
scripts/check-open-source-launch.sh --full
```

The Go binary uses the pure-Go SQLite driver `modernc.org/sqlite`, so normal
builds do not require cgo.

## Project Status

Current branch status:

- The core run lifecycle works end to end against local Docker and a
  RunPod-oriented path, exercised by a full Go test suite. RunPod support is
  experimental.
- Embedded operator console and JSON-first CLI.
- Hand-written SDKs for TypeScript, Python, and Ruby.
- Production evaluation docs and honest known limitations are checked in.
- No tagged release or published packages yet; install from a source checkout.

Important pre-GA gaps include package publishing, release tags, public CI run
history, stronger external sink wiring, registry credential flows, and external
threat-model review.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. The
short version: keep changes narrow, include tests or docs evidence, run the
relevant local checks, and update production docs when behavior changes.

Project spaces are covered by the [Code Of Conduct](CODE_OF_CONDUCT.md).
For questions and evaluation help, see [SUPPORT.md](SUPPORT.md).
For maintainer decision rules and project boundaries, see
[GOVERNANCE.md](GOVERNANCE.md).
Security issues should be reported privately. See [SECURITY.md](SECURITY.md).

## License

Mercator is licensed under the Apache License, Version 2.0. See
[LICENSE](LICENSE) and [NOTICE](NOTICE).
