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

Create a run from an image reference. On the Docker adapter the image must be
digest-pinned — a mutable tag like `busybox:latest` is rejected, and registry
tag→digest resolution is not implemented yet (see
[known-limitations.md](../production/known-limitations.md)), so pin the digest
yourself:

```sh
IMAGE="$(docker inspect --format '{{index .RepoDigests 0}}' busybox:latest)"
mercator run create "$IMAGE" -- echo hi
```

That command omits `--run-id` and `--idempotency-key`; the server generates a
run id, and the CLI mints or derives the idempotency key needed by the API. The
bare `busybox` shorthand resolves only under the internal test resolver, not
against a real Docker host.

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
Docker quickstart server from the README. If you installed a release
binary, replace `go run ./cmd/mercator` with `mercator`.

```sh
export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_API_TOKEN='dev-token'
export MERCATOR_WORKSPACE_ID=ws_1

docker pull -q busybox:latest >/dev/null
IMAGE="$(docker inspect --format '{{index .RepoDigests 0}}' busybox:latest)"
RUN_ID="$(go run ./cmd/mercator run create "$IMAGE" -- echo hi | jq -r '.run.id')"

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

## Exit Codes

The CLI uses a small exit-code contract so scripts can distinguish local setup
mistakes from API/runtime failures:

| Exit | Meaning | Examples |
| --- | --- | --- |
| `0` | Command completed successfully. | Help output, successful `run`/`sink` responses. |
| `1` | The command reached the request/response layer, but the request failed or the API returned an error response. | Network/transport failure, non-2xx API response, response-read failure, or non-JSON API response. |
| `2` | Local argument or configuration validation failed before an API request was sent. | Missing `MERCATOR_API_URL`, missing workspace, missing `--run-id`, unknown command, invalid flags, or invalid `--workload-json`. |

## Error Responses

The CLI prints JSON on stdout for successful responses and JSON on stderr for
local validation errors or non-2xx API responses. Common setup mistakes:

Local validation example:

```sh
unset MERCATOR_API_URL
mercator run list --workspace-id ws_1
# stderr, exit 2:
# {"code":"BASE_URL_REQUIRED","message":"MERCATOR_API_URL or --api-url is required"}
```

API response example:

```sh
export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_API_TOKEN='wrong'
export MERCATOR_WORKSPACE_ID=ws_1

mercator run list
# stderr, exit 1:
# {"code":"UNAUTHORIZED","message":"Bearer token is required."}
```

```sh
export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_API_TOKEN='dev-token'
export MERCATOR_WORKSPACE_ID=ws_nope
# Server was started with MERCATOR_AUTH_WORKSPACES=ws_1.

mercator run list
# stderr, exit 1:
# {"code":"FORBIDDEN","message":"Principal is not authorized for this workspace."}
```

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

From a source checkout with a running Docker daemon, start `mercator serve`
with the Docker adapter, then create a digest-pinned run through the CLI and
confirm it closes successfully:

```sh
docker pull -q busybox:latest >/dev/null
IMAGE="$(docker inspect --format '{{index .RepoDigests 0}}' busybox:latest)"
RUN_ID="$(go run ./cmd/mercator run create "$IMAGE" -- echo hi | jq -r '.run.id')"
go run ./cmd/mercator run get --run-id "$RUN_ID" \
  | jq '{outcome: .run.outcome, exit_code: .run.exit_code, cleanup: .run.cleanup, closed: .run.closed}'
```

A healthy run reports `outcome=succeeded`, `exit_code=0`, `cleanup=confirmed`,
and `closed=true`. For the full serve configuration and runbook, see
[Docker adapter operation](../production/docker-adapter-operation.md).
