# CLI Reference

The `mercator` binary has three modes:

- `mercator serve` starts the HTTP API and embedded console.
- `mercator verify --spec FILE` starts an isolated broker and launches a real,
  bounded provider conformance Run.
- Every other command (`run`, `sink`, `login`, `context`, ...) targets an
  existing Mercator API and prints JSON responses.

Run `mercator --help` or `mercator help run create` for the in-binary reference.
Help does not require a running server or `MERCATOR_API_URL`.

## Provider Verification

`mercator verify --spec trial.json` validates provider credentials, pricing,
launch, signed workload reporting, terminal cleanup, and empty owned-resource
inventory. It does launch an instance. The command refuses to launch when the
offer's hourly rate multiplied by the trial timeout exceeds
`max_expected_cost_usd`.

The trial's optional `mode` is `probe` by default. `launch-cancel` proves that
Mercator can cancel an accepted instance and drive cleanup to completion.

The trial names a credential environment variable. Mercator reads that one
value in-process when it constructs the provider adapter. It does not accept
the API key in the JSON document, command arguments, or evidence output.

```sh
export RUNPOD_API_KEY='rpa_...'
export MERCATOR_CONFORMANCE_LISTEN_ADDR='0.0.0.0:8082'
export MERCATOR_CONFORMANCE_PUBLIC_URL='https://reports.example.com'

mercator verify --spec trial.json | jq .
```

Local Docker verification needs no credential or public URL. Cloud and remote
Docker verification require a fixed `MERCATOR_CONFORMANCE_LISTEN_ADDR` and an
origin-only `MERCATOR_CONFORMANCE_PUBLIC_URL` routed to that listener. Invalid
topology fails before Mercator contacts the provider. See
[provider-conformance.md](../production/provider-conformance.md) for the full
trial schema and a local Docker proof.

## Environment And Contexts

Server targeting resolves in this order: explicit flags, then environment
variables, then the current context from the config file, then
`http://127.0.0.1:8080` where `mercator serve` listens. Environment always wins
over the file so CI needs no config file.

`mercator serve` writes a `local` context holding its address and generated
token whenever it binds a loopback address and no `MERCATOR_API_TOKEN` was set,
so the CLI on that machine needs no configuration at all. It claims
`current_context` only when nothing else has.

| Variable | Purpose |
| --- | --- |
| `MERCATOR_API_URL` | API URL, for example `http://127.0.0.1:8080`. |
| `MERCATOR_API_TOKEN` | Bearer token sent as `Authorization: Bearer ...`. |
| `MERCATOR_WORKSPACE_ID` | Default workspace. Optional: commands resolve the broker's only workspace, and name the candidates when there are several. |
| `MERCATOR_CONFIG` | Config file path (default `~/.config/mercator/config.json`). |

The global `--api-url URL` flag overrides `MERCATOR_API_URL`.

**Contexts** name deployments so an operator can target staging or production
by name. A context holds `api_url`, a default `workspace_id`, and a credential
(a static API token, or a login-minted token tied to a user identity):

```sh
mercator context set staging --api-url https://staging.example.com --workspace-id ws_1
mercator context set production --api-url https://mercator.example.com --workspace-id ws_prod
mercator context use production
mercator context list
```

The config file is written with owner-only permissions since it stores
credentials.

## Login

When the server has OIDC configured
([authentication-workspaces.md](../production/authentication-workspaces.md)),
`mercator login` signs you in with the standard native-app flow: a browser
opens to the server's login, you authenticate with the identity provider, and
the CLI receives a token tied to your identity on a localhost redirect. The
token is stored in the current (or `--context`-named) context, lives 30 days,
and audits your mutations under your email instead of `bearer`:

```sh
mercator login                 # into the current context
mercator login --context prod  # into a named context
mercator logout                # clear the stored credential
```

`mercator login` fails with `OIDC_NOT_CONFIGURED` against a token-only server;
static `MERCATOR_API_TOKEN` auth continues to work everywhere.

## Run Commands

Create a run from an image reference:

```sh
mercator run create busybox -- echo hi
```

The broker resolves a tag against its Docker host and stores the resulting
digest and platform, so the recorded revision is reproducible even though you
typed a tag. The image has to be on that host already; when it is not, the
error names the `docker pull` to run. A reference you pin yourself is kept
verbatim.

That command omits `--run-id` and `--idempotency-key`. The server generates a
run id, and the CLI mints or derives the idempotency key the API needs.

Common commands. Each defaults `--run-id` to the most recent run in the
workspace, so the run you just created needs no id:

```sh
mercator run list
mercator run get
mercator run wait
mercator run events
mercator run decision
mercator run refresh
mercator run cancel
```

Follow-up commands after the README quickstart. Each one defaults the
workspace and the run, so nothing here restates an id:

```sh
mercator run create busybox -- echo hi

mercator run list \
  | jq '.runs[] | {id, outcome, closed}'

mercator run wait \
  | jq '{id: .run.id, outcome: .run.outcome, exit_code: .run.exit_code, cleanup: .run.cleanup, closed: .run.closed}'

mercator run events \
  | jq '.events[] | .type'

mercator run decision \
  | jq '{selected_offer_snapshot_id: .decision.selected_offer_snapshot_id, candidate_count: (.decision.candidates | length), rejected_count: ([.decision.candidates[] | select(.feasible | not)] | length)}'

mercator sink status --sink-id audit \
  | jq '{sink_id, cursor}'
```

`run decision` returns the latest recorded placement decision. After stale
capacity triggers replacement, this is the replacement decision rather than
the initial selection. If no eligible Offer remains, the decision has no
`selected_offer_snapshot_id`, carries `NO_FEASIBLE_OFFERS`, and preserves the
candidate rejection that explains the terminal `RETRY_EXHAUSTED` Run event.

`run events` exposes provider-neutral launch failure fields. Replacement is
limited to `PROVIDER_CAPACITY_UNAVAILABLE` with `retryable=true` and
`side_effect=none`; indeterminate outcomes keep their original launch key for
reconciliation.

Use `--workspace-id ID` on any run command to override
`MERCATOR_WORKSPACE_ID`.

## Flag Placement

Flags work in any position: `mercator run create busybox --workspace-id ws_1`
and `mercator run create --workspace-id ws_1 busybox` are the same command,
and the global `--api-url` may appear before or after the command. An unknown
flag-looking token is a loud error, never a silent container argument.
Container arguments that begin with `-` belong after a bare `--`:

```sh
mercator run create "$IMAGE" -- sh -c 'echo hi'
```

## Connection Commands

Connections register provider endpoints (Docker hosts, RunPod). These
previously required hand-written `curl` with `Idempotency-Key` headers:

```sh
mercator connection create --adapter-type runpod \
  --credential-source mercator --secret-stdin < runpod-key.txt
mercator connection authorize
mercator connection list
mercator connection delete --connection-id runpod
```

`--connection-id` defaults to the adapter type on create, and to the
workspace's only connection on authorize and delete. Name it explicitly once a
workspace holds more than one.

`--config key=value` is repeatable for adapter config. Prefer `--secret-stdin`
over `--secret` so secrets stay out of shell history. The create idempotency
key derives from the connection id when omitted.

## Workload Commands

```sh
mercator workload create --workload-id wl_train --name "trainer"
mercator workload revision create --workload-id wl_train --revision-json "$(cat revision.json)"
mercator workload revision list --workload-id wl_train
mercator workload revision get --workload-id wl_train --revision-id wrev_...
```

## Exit Codes

The CLI uses a small exit-code contract so scripts can distinguish local setup
mistakes from API/runtime failures:

| Exit | Meaning | Examples |
| --- | --- | --- |
| `0` | Command completed successfully. | Help output, successful `run`/`sink` response, or passed provider trial. |
| `1` | Runtime work failed. | Network/API failure, blocked trial, failed probe, or unconfirmed cleanup. |
| `2` | Local argument or configuration validation failed. | Missing base URL, invalid flags, invalid workload JSON, or invalid trial document. |

## Error Responses

The CLI prints JSON on stdout for successful responses and JSON on stderr for
local validation errors or non-2xx API responses. Common setup mistakes:

Local validation example:

```sh
mercator run list --api-url http://127.0.0.1:9999
# stderr, exit 1:
# {"code":"REQUEST_FAILED","message":"... connection refused"}
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
unset MERCATOR_WORKSPACE_ID

mercator run list
# stderr, exit 1:
# workspace id is required; pass --workspace-id or set MERCATOR_WORKSPACE_ID
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

With a running Docker daemon, start `mercator serve`, register the Docker
adapter, and confirm a run closes successfully:

```sh
docker pull -q busybox:latest >/dev/null
mercator run create busybox -- echo hi
mercator run get \
  | jq '{outcome: .run.outcome, exit_code: .run.exit_code, cleanup: .run.cleanup, closed: .run.closed}'
```

A healthy run reports `outcome=succeeded`, `exit_code=0`, `cleanup=confirmed`,
and `closed=true`. For the full serve configuration and runbook, see
[Docker adapter operation](../production/docker-adapter-operation.md).
