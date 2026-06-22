# Mercator Python SDK

Small, dependency-free Python client for the Mercator V1 HTTP API.

## Run an image, get the exit code

The minimal one-liner: hand `run_image` an image and (optionally) some args.
You do not supply a full workload spec (the server defaults everything). Then
block until the run closes and read the container exit code straight off the run
object.

```python
from mercator import MercatorClient

client = MercatorClient("http://127.0.0.1:8080", token="dev-token", workspace_id="ws_1")

created = client.run_image("busybox", args=["echo", "hi"], idempotency_key="echo-hi:create")
run_id = created["run_id"]                           # == created["run"]["id"]

result = client.wait_run_until_terminal(run_id)
print(result["run"]["outcome"], result["run"]["exit_code"])  # => succeeded 0
print(result["run"]["disposition"])                  # => release (or terminate)
```

Every run response carries a convenience top-level `run_id` (equal to
`run["id"]`) alongside the full `run` record, plus a reserved `metadata` object.
The run record exposes `run["disposition"]` (`release` or `terminate`), the
recorded cleanup intent.

`run_image` derives a stable `Idempotency-Key` from `run_id`
(`f"{run_id}:create"`) when you pass one. If you omit `run_id` (so the server
generates the id) you MUST pass an explicit `idempotency_key` and reuse it
verbatim across retries of the same logical call -- `run_image` will not
silently mint a random key, because a per-attempt key would create a second run
instead of replaying the first on a transport retry. Pass
`env={"K": {"value": "v"}}` for environment.

## Full workload form

```python
from mercator import MercatorClient, MercatorError

# Scope the workspace once on the client; every call inherits it. A per-call
# workspace_id still overrides this default.
client = MercatorClient("http://127.0.0.1:8080", token="dev-token", workspace_id="ws_1")

try:
    # create_run returns the same envelope as get/wait/cancel: {"run": {...}}.
    created = client.create_run(
        {
            "run_id": "run_1",
            "workload": {
                "spec": {"containers": [{"name": "main", "image": "repo/app@sha256:..."}]},
            },
        },
        idempotency_key="run_1:create",
    )
    run_id = created["run"]["id"]
    if created.get("duplicate"):
        print("idempotent replay of an existing run")

    # Block until terminal, then read the container exit code from ONE response.
    # No need to parse the compute.run.external_state_observed.v1 event log.
    result = client.wait_run_until_terminal(run_id)
    run = result["run"]
    print(run["outcome"], run.get("exit_code"))  # e.g. "succeeded" 0
except MercatorError as exc:
    print(exc.status_code, exc.code, exc.message)
```

The recommended `idempotency_key` is a stable string derived from `run_id`
(for example `f"{run_id}:create"`). A logical retry that reuses the same key,
the same `run_id`, and the same logical workload is a safe replay (the response
sets `duplicate: true`) even if the client regenerates a cosmetic
`workload.id`; only a substantively different payload under a reused key
returns `409 IDEMPOTENCY_CONFLICT`.

## Install

From the repository checkout:

```sh
python3 -m pip install -e sdk/python
```

The package has no runtime dependencies outside the Python standard library.

## Client

```python
client = MercatorClient(
    base_url="http://127.0.0.1:8080",
    token="dev-token",
    workspace_id="ws_1",
    timeout=30.0,
)
```

`Authorization: Bearer <token>` is sent for every request when `token` is set.
When `workspace_id` is set on the client it is applied to every call: as the
query parameter on reads and as the `workspace_id` body field on `create_run`
(unless the body already carries one). Pass `workspace_id=...` to any method to
override the default for a single call. If neither is set, the parameter is
omitted and the server applies its own default only when it is configured with a
single concrete workspace.
Mutation methods that require idempotency take an explicit `idempotency_key`.
Flexible API objects such as workloads, revisions, decisions, offers, and events
are returned as dictionaries because the current V1 OpenAPI contract leaves many
of those schemas intentionally open.

## Methods

- `health_live()`, `health_ready()`, `get_openapi()`
- `run_image(image, *, args=None, env=None, run_id=None, workspace_id=None, idempotency_key=None)` — minimal image shorthand
- `list_runs(workspace_id=None)`, `create_run(payload, idempotency_key=..., workspace_id=None)`
- `get_run(run_id, workspace_id=None)`, `wait_run(run_id, workspace_id=None)`
- `wait_run_until_terminal(run_id, workspace_id=None, deadline=300.0)` — poll-until-terminal
- `refresh_run(run_id, workspace_id=None)`, `cancel_run(run_id, workspace_id=None)`
- `list_run_events(run_id, workspace_id=None)`, `get_run_decision(run_id, workspace_id=None)`
- `preview_placement(payload)`
- `list_connections(workspace_id=None)`, `list_offers(workspace_id=None)`
- `create_workload(workspace_id, workload_id, name, idempotency_key=...)`
- `list_workload_revisions(workload_id, workspace_id=None)`
- `create_workload_revision(workload_id, workspace_id, revision, idempotency_key=...)`
- `get_workload_revision(workload_id, revision_id, workspace_id=None)`
- `resolve_image(image, platform)`
- `get_sink_status(sink_id)`, `deliver_sink(sink_id)`
- `replay_sink(sink_id, from_exclusive=None, limit=None, replay_id=None)`

For lower-level or newly added routes, use:

```python
client.request("GET", "/v1/runs", query={"workspace_id": "ws_1"})
```

## Waiting for a terminal run

`wait_run(run_id)` issues a single long-poll: the server advances the run for up
to ~30 seconds and returns the run, which may still be open at the deadline.
`wait_run_until_terminal(run_id)` wraps that loop: it re-issues the wait until
`run["closed"]` is true or its own `deadline` (default 300s) elapses, then
returns the latest envelope. Inspect `result["run"]["closed"]` to distinguish a
terminal run from a timed-out wait, and read `result["run"]["exit_code"]` for
the container exit code.

## Errors

Non-2xx API responses raise `MercatorError` with:

- `status_code`
- `code`
- `message`
- `details`
- `response`

Transport failures also raise `MercatorError` with `code == "REQUEST_FAILED"`
and `status_code is None`.
