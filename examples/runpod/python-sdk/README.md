# python — custom events via the Mercator Python SDK

Proves the SDK path: arbitrary event types plus automatic exit reporting.

## Create the run

This example is self-contained: the container start command installs the SDK
and runs the reporting logic inline via a heredoc, so no pre-baked image or
file on disk is required. Run it verbatim:

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
          "args": ["sh","-c","pip install --quiet mercator-sdk && python - <<'\''PY'\''\nimport time\nfrom mercator import run_reporter\n\nreporter = run_reporter()\nif reporter is None:\n    raise SystemExit(0)\nwith reporter:\n    reporter.report(\"model.loaded\", {\"name\": \"demo-model\"})\n    for pct in (25, 50, 75, 100):\n        reporter.report(\"progress\", {\"pct\": pct})\n        time.sleep(1)\nPY"]
        }],
        "resources": { "accelerators": [ { "vendor": "NVIDIA", "count": 1 } ] }
      }
    }
  }'
```

The exact install line depends on how the Python SDK is distributed; adapt
`pip install mercator-sdk` to the published package name or a vendored copy.

[`run.py`](./run.py) in this directory is the same logic in readable,
standalone form — use it as the reference. For a real workload you would bake
`run.py` into a custom image (and run `python run.py`) rather than inlining the
script in the start command.

## SDK API used

The script calls `run_reporter()` from the Mercator Python SDK. The function
returns a `Reporter` when the injected reporting env is present, or `None` when
the required vars (`MERCATOR_REPORT_URL`, `MERCATOR_RUN_ID`,
`MERCATOR_RUN_TOKEN`) are missing — so it degrades gracefully when run outside
Mercator. Events are emitted via:

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
