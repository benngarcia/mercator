# Fake Adapter Baseline Evaluation

- Type: benchmark or reproducible evaluation
- Status: maintainer baseline, not external proof
- Mercator commit evaluated: `cda1d4e09f4b85e8ce15c7b363996461c1a8cb37`
- Date: 2026-06-25
- Reviewer or project: Mercator launch-prep maintainer run
- Permission: linkable after the repository is public

## Scenario

This baseline checks whether a new evaluator can get a deterministic success
through Mercator without Docker, RunPod, a container registry, provider
credentials, or billable compute. It exercises the fake-adapter path that the
README recommends as the first five-minute trial.

The command under test builds a temporary binary, starts a local Mercator server
with `MERCATOR_FAKE_OFFER=1`, waits for readiness, creates a `busybox` run
through the CLI, reads the closed run, verifies public events, and verifies the
placement decision.

## Setup

- OS and architecture: Darwin arm64
- Go: `go version go1.25.11 darwin/arm64`
- jq: `jq-1.8.1`
- Adapter: fake standing offer
- Workspace: sample workspace `ws_smoke`
- Token: sample token `dev-smoke-token`
- Command: `scripts/smoke-test-fake.sh`

The workspace and token values above are sample-only launch fixtures. They do
not authorize any public service.

## Evidence

```text
$ scripts/smoke-test-fake.sh
Mercator fake-adapter smoke test passed
run_id=run_019efd40-c77a-73d0-81b5-e646e95656e7 outcome=succeeded exit_code=0 cleanup=confirmed closed=true
```

The command also printed a loopback console URL with an ephemeral local port.
That URL is omitted from this report because it is only useful inside the
machine running the smoke test.

## Outcome

The run reached the expected terminal fake-adapter result:

- `outcome=succeeded`
- `exit_code=0`
- `cleanup=confirmed`
- `closed=true`

The smoke command also verified that public events were present and the
placement decision recorded a selected offer snapshot.

## Privacy Review

This report uses sample token and workspace values, omits the loopback console
URL, and contains no provider credentials, private hostnames, customer data,
local usernames, or unpublished downstream implementation details.

## Reproduction

From a clean source checkout:

```sh
scripts/smoke-test-fake.sh
```

For manual inspection of the same path, follow
[Fake Adapter Evaluation Path](../../production/fake-eval-path.md).
