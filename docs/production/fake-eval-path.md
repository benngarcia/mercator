# Fake Adapter Evaluation Path

Use the fake adapter first. It is deterministic, requires no Docker daemon, and
exercises the event-first broker path through placement, launch intent, launch,
observation, cleanup, closure, public events, decision reads, sink cursor reads,
and CLI JSON behavior.

## One-Command Smoke Test

From a source checkout:

```sh
scripts/smoke-test-fake.sh
```

The script builds a temporary binary, starts Mercator with `MERCATOR_FAKE_OFFER=1`,
waits for `/health/ready`, creates a `busybox` run through the CLI, then asserts
the run closes with `outcome=succeeded`, `exit_code=0`, `cleanup=confirmed`, and
`closed=true`. Use the manual steps below when you want to inspect each response
or keep the server running for the console.

## Start Server

```sh
rm -f /tmp/mercator-fake.db /tmp/mercator-fake.db-wal /tmp/mercator-fake.db-shm

export MERCATOR_ADDR=127.0.0.1:8080
export MERCATOR_SQLITE_DSN='file:/tmp/mercator-fake.db'
export MERCATOR_API_TOKEN="$(openssl rand -hex 32)"
export MERCATOR_AUTH_WORKSPACES='ws_eval'
export MERCATOR_FAKE_OFFER=1

go run ./cmd/mercator serve
```

`MERCATOR_FAKE_OFFER=1` serves a single **standing** offer, so runs record
cleanup `disposition=release` (cleanup removes only the job, never a host). To
exercise the **terminate** path instead — a *provisionable* offer whose runs
record `disposition=terminate` and tear down via the adapter's `Terminate` verb
— set `MERCATOR_FAKE_OFFER=provisionable`. Both paths run end-to-end with no
network. See "Both Cleanup Dispositions" below.

In another shell:

```sh
export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_API_TOKEN='<token from first shell>'
export MERCATOR_WORKSPACE_ID=ws_eval
```

## The Minimal Run: One Field

The simplest run is just an image. The CLI takes it as the first positional
argument; container args follow `--`. You do not pass `--run-id` (the server
generates one), `--idempotency-key` (the CLI mints a stable one), or a workload
spec (the server defaults everything). In fake mode the image tag resolves to a
deterministic synthetic digest with no network and no pre-pinned digest.

```sh
go run ./cmd/mercator run create busybox -- echo hi | jq .
```

That prints the unified envelope (`202`); read the server-generated id from
`.run.id`. Capture it and watch the run close:

```sh
RUN_ID="$(go run ./cmd/mercator run create busybox -- echo hi | jq -r '.run.id')"

go run ./cmd/mercator run get --run-id "$RUN_ID" \
  | jq '{phase: .run.phase, outcome: .run.outcome, exit_code: .run.exit_code, closed: .run.closed}'
# => {"phase":"closed","outcome":"succeeded","exit_code":0,"closed":true}
```

The equivalent raw HTTP call is equally small (note the required
`Idempotency-Key` header and the omitted `run_id`):

```sh
curl -sS -X POST "$MERCATOR_API_URL/v1/runs?workspace_id=$MERCATOR_WORKSPACE_ID" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: idem-minimal-1' \
  -d '{"image":"busybox","args":["echo","hi"]}' | jq '.run.id'
```

The rest of this page exercises the full digest-pinned workload form for
completeness.

## Create A Digest-Pinned Workload File

```sh
cat >/tmp/mercator-workload.json <<'JSON'
{
  "id": "wrev_fake_1",
  "workspace_id": "ws_eval",
  "workload_id": "wrk_fake",
  "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "spec": {
    "containers": [
      {
        "name": "main",
        "image": "example.com/eval/fake@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "platform": {"os": "linux", "architecture": "amd64"},
        "entrypoint": ["/bin/sh"],
        "args": ["-c", "echo mercator-fake-eval"],
        "env": {"LOG_LEVEL": {"value": "debug"}},
        "ports": []
      }
    ],
    "resources": {
      "cpu": {"min_millis": 100},
      "memory": {"min_bytes": 67108864},
      "ephemeral_disk": {"min_bytes": 1048576}
    },
    "network": {"inbound": "none"},
    "placement": {"objective": "balanced", "expected_runtime_seconds": 1},
    "execution": {"max_runtime_seconds": 60, "max_pre_start_attempts": 1}
  }
}
JSON
```

## Create And Inspect A Run

```sh
WORKLOAD_JSON="$(jq -c . /tmp/mercator-workload.json)"

go run ./cmd/mercator run create \
  --workspace-id ws_eval \
  --run-id run_fake_1 \
  --idempotency-key idem-run-fake-1 \
  --workload-json "$WORKLOAD_JSON"

go run ./cmd/mercator run get --workspace-id ws_eval --run-id run_fake_1 | jq .
go run ./cmd/mercator run events --workspace-id ws_eval --run-id run_fake_1 | jq .
go run ./cmd/mercator run decision --workspace-id ws_eval --run-id run_fake_1 | jq .
go run ./cmd/mercator sink status --sink-id audit | jq .
```

`run create` returns the unified envelope (`{"run_id": "...", "run": {...},
"links": {...}}`, `202`), the same shape as `run get`/`run wait`/`run cancel`.
Read the run id from the convenience top-level `.run_id` or from `.run.id` (they
are always equal).

Confirm the run closed successfully and read the container exit code from a
single GET, without parsing the event log:

```sh
go run ./cmd/mercator run get --workspace-id ws_eval --run-id run_fake_1 \
  | jq '{phase: .run.phase, outcome: .run.outcome, exit_code: .run.exit_code, cleanup: .run.cleanup, closed: .run.closed}'
# => {"phase":"closed","outcome":"succeeded","exit_code":0,"cleanup":"confirmed","closed":true}
```

Because this shell exported `MERCATOR_WORKSPACE_ID=ws_eval`, the CLI supplies
that workspace on every `run` subcommand, so `--workspace-id` can be dropped
(an explicit `--workspace-id` flag still overrides the env default):

```sh
go run ./cmd/mercator run get --run-id run_fake_1 | jq '.run.exit_code'
```

At the HTTP layer there is a second, independent default: because
`MERCATOR_AUTH_WORKSPACES='ws_eval'` configures a single concrete workspace, the
server fills in `workspace_id` when a request omits it. A raw API call can
therefore drop the `?workspace_id=` query entirely:

```sh
curl -sS "$MERCATOR_API_URL/v1/runs/run_fake_1" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" | jq '.run.exit_code'
```

Expected evaluation signals:

- The run reaches a closed state in the fake path with `outcome: succeeded`.
- `run.exit_code` is `0`, surfaced directly on the run object.
- Public events omit private event payloads and secret material, and their
  `data` fields are snake_case (for example
  `compute.run.external_state_observed.v1` carries `exit_code`).
- Placement decision includes feasible candidate information for
  `offer_local_fake`.
- Sink `audit` exists and cursor state is readable.

## Both Cleanup Dispositions

The run above used the default standing offer, so it recorded
`disposition=release`:

```sh
go run ./cmd/mercator run get --run-id run_fake_1 | jq '.run.disposition'
# => "release"
```

Restart the server with `MERCATOR_FAKE_OFFER=provisionable` (a fresh DB) to drive
the terminate path with no network. A run placed on the provisionable offer
records `disposition=terminate` and is torn down via the adapter's `Terminate`
verb (destroying the host we provisioned) rather than `Release`:

```sh
# server shell:
export MERCATOR_FAKE_OFFER=provisionable   # then: go run ./cmd/mercator serve

# client shell:
go run ./cmd/mercator run create busybox -- echo hi | jq '{run_id, disposition: .run.disposition}'
# => {"run_id":"run_...","disposition":"terminate"}
```

The disposition is **recorded at launch time and never re-inferred at cleanup
time**: cleanup dispatches on the value on the
`compute.run.launch_intent_recorded.v1` event, which makes teardown crash-safe
even if offers change. A pre-change run with no recorded disposition defaults to
`release` (never destroys a host). See the "Cleanup Disposition" section of
`workload-run-lifecycle.md` for the full contract.

## Idempotency Check

Re-run the same create command with the same idempotency key and identical
payload. It should return the same `.run.id` and set top-level
`duplicate: true`. Regenerating only the cosmetic `workload.id` (a
client-minted field) under the same key is also a safe replay, not a conflict,
because the request hash excludes that id.

Then reuse the same idempotency key with a substantively different payload (for
example a different `run_id` or a different image); it should return a JSON
error with code `IDEMPOTENCY_CONFLICT`.
