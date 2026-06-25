# CLI Reference

The `mercator` binary has two modes:

- `mercator serve` starts the HTTP API and embedded console.
- `mercator run ...` and `mercator sink ...` call an existing Mercator API and
  print JSON responses.

Run `mercator --help` or `mercator help run create` for the in-binary reference.
Help does not require a running server or `MERCATOR_API_URL`.

## Environment

CLI commands read these variables:

| Variable | Purpose |
| --- | --- |
| `MERCATOR_API_URL` | API URL, for example `http://127.0.0.1:8080`. |
| `MERCATOR_API_TOKEN` | Bearer token sent as `Authorization: Bearer ...`. |
| `MERCATOR_WORKSPACE_ID` | Default workspace for `run` commands. |

The global `--api-url URL` flag overrides `MERCATOR_API_URL`.

## Run Commands

The image shorthand is the normal first-run path:

```sh
mercator run create busybox -- echo hi
```

That command omits `--run-id` and `--idempotency-key`; the server generates a
run id, and the CLI mints or derives the idempotency key needed by the API.

Common commands:

```sh
mercator run list
mercator run get --run-id run_...
mercator run wait --run-id run_...
mercator run events --run-id run_...
mercator run decision --run-id run_...
mercator run refresh --run-id run_...
mercator run cancel --run-id run_...
```

From a source checkout, use these follow-up commands after starting the
fake-adapter quickstart server from the README. If you installed a release
binary, replace `go run ./cmd/mercator` with `mercator`.

```sh
export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_API_TOKEN='dev-token'
export MERCATOR_WORKSPACE_ID=ws_1

RUN_ID="$(go run ./cmd/mercator run create busybox -- echo hi | jq -r '.run.id')"

go run ./cmd/mercator run list \
  | jq '.runs[] | {id, outcome, closed}'

go run ./cmd/mercator run wait --run-id "$RUN_ID" \
  | jq '{id: .run.id, outcome: .run.outcome, exit_code: .run.exit_code, cleanup: .run.cleanup, closed: .run.closed}'

go run ./cmd/mercator run events --run-id "$RUN_ID" \
  | jq '.events[] | .type'

go run ./cmd/mercator run decision --run-id "$RUN_ID" \
  | jq '{selected_offer_snapshot_id: .decision.selected_offer_snapshot_id, rejected_count: (.decision.rejected | length)}'

go run ./cmd/mercator sink status --sink-id audit \
  | jq '{sink_id, cursor}'
```

Use `--workspace-id ID` on any run command to override
`MERCATOR_WORKSPACE_ID`.

## Workload JSON

For full control, pass a workload revision JSON object instead of an image
shorthand:

```sh
WORKLOAD_JSON="$(jq -c . workload.json)"

mercator run create \
  --workspace-id ws_1 \
  --run-id run_example_1 \
  --idempotency-key idem-run-example-1 \
  --workload-json "$WORKLOAD_JSON"
```

## Sink Commands

The default launch path configures an `audit` sink:

```sh
mercator sink status --sink-id audit
mercator sink deliver --sink-id audit
mercator sink replay --sink-id audit --from 0 --limit 100 --replay-id replay_1
```

Sink commands also print JSON.

## Local Smoke Test

From a source checkout, run:

```sh
scripts/smoke-test-fake.sh
```

The script builds a temporary binary, starts Mercator with the fake adapter,
creates a run through the CLI, and verifies the run closes successfully.
