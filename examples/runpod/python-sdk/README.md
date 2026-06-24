# python — custom events via the Mercator Python SDK

Proves the SDK path: arbitrary event types plus automatic exit reporting.

## Create the run

The pod installs the SDK and runs `run.py`. Point `args` at a shell that
installs the SDK from your published package (or vendors it) and executes the
script:

```sh
curl -X POST "$MERCATOR/v1/runs" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H 'Idempotency-Key: python-sdk-1' \
  -H 'Content-Type: application/json' \
  -d '{
    "workspace_id": "ws_1",
    "workload": {
      "workspace_id": "ws_1",
      "spec": {
        "containers": [{
          "image": "python:3-slim",
          "args": ["sh","-c","pip install --quiet mercator-sdk && python /app/run.py"]
        }],
        "resources": { "accelerators": [ { "vendor": "NVIDIA", "count": 1 } ] }
      }
    }
  }'
```

`run.py` must be present in the image (bake it in, or fetch it in the start
command). The exact install line depends on how the Python SDK is distributed;
adapt `pip install ...` to the published package name or a vendored copy.

## SDK API used

`run.py` calls `run_reporter()` from the Mercator Python SDK. The function
returns a `Reporter` when the `MERCATOR_*` env is present, or `None` when
running outside Mercator (graceful degradation). Events are emitted via:

```python
reporter.report("model.loaded", {"name": "demo-model"})  # type str, data dict
reporter.report("progress", {"pct": 50})
```

The context manager (`with reporter:`) automatically calls
`reporter.report_exit(0)` on a clean exit, or `report_exit(1)` if an
uncaught exception propagates out of the block.

## Expected

- The Events tab shows `model.loaded`, several `progress` events, then the exit
  event.
- Outcome **succeeded**; the pod is terminated automatically.
