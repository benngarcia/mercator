# Mercator Python SDK

Small, dependency-free Python client for the Mercator V1 HTTP API.

```python
from mercator import MercatorClient, MercatorError

client = MercatorClient("http://127.0.0.1:8080", token="dev-token")

try:
    run = client.create_run(
        {
            "workspace_id": "ws_1",
            "run_id": "run_1",
            "workload": {
                "workspace_id": "ws_1",
                "spec": {"containers": [{"name": "main", "image": "repo/app@sha256:..."}]},
            },
        },
        idempotency_key="run_1-create",
    )
except MercatorError as exc:
    print(exc.status_code, exc.code, exc.message)
```

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
    timeout=30.0,
)
```

`Authorization: Bearer <token>` is sent for every request when `token` is set.
Mutation methods that require idempotency take an explicit `idempotency_key`.
Flexible API objects such as workloads, revisions, decisions, offers, and events
are returned as dictionaries because the current V1 OpenAPI contract leaves many
of those schemas intentionally open.

## Methods

- `health_live()`, `health_ready()`, `get_openapi()`
- `list_runs(workspace_id)`, `create_run(payload, idempotency_key=...)`
- `get_run(run_id, workspace_id)`, `wait_run(run_id, workspace_id)`
- `refresh_run(run_id, workspace_id)`, `cancel_run(run_id, workspace_id)`
- `list_run_events(run_id, workspace_id)`, `get_run_decision(run_id, workspace_id)`
- `preview_placement(payload)`
- `list_connections(workspace_id)`, `list_offers(workspace_id)`
- `create_workload(workspace_id, workload_id, name, idempotency_key=...)`
- `list_workload_revisions(workload_id, workspace_id)`
- `create_workload_revision(workload_id, workspace_id, revision, idempotency_key=...)`
- `get_workload_revision(workload_id, revision_id, workspace_id)`
- `resolve_image(image, platform)`
- `list_secrets(workspace_id)`
- `create_secret_version(secret_id, workspace_id, value, idempotency_key=...)`
- `grant_secret(secret_id, workspace_id, version, scope_type, scope_id)`
- `get_sink_status(sink_id)`, `deliver_sink(sink_id)`
- `replay_sink(sink_id, from_exclusive=None, limit=None, replay_id=None)`

For lower-level or newly added routes, use:

```python
client.request("GET", "/v1/runs", query={"workspace_id": "ws_1"})
```

## Errors

Non-2xx API responses raise `MercatorError` with:

- `status_code`
- `code`
- `message`
- `details`
- `response`

Transport failures also raise `MercatorError` with `code == "REQUEST_FAILED"`
and `status_code is None`.
