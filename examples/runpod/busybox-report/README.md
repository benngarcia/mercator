# busybox - raw-HTTP reporting (no SDK)

Proves any minimal image can report to Mercator using only the injected env and
a plain HTTP POST. `busybox` ships `wget`, which we use to POST the `:report`
endpoint.

This example uses no SDK package. It is the lowest-dependency RunPod proof path
for validating that injected reporting env, per-run tokens, exit reporting, and
provider cleanup work.

Before running, replace `busybox@sha256:<real-index-digest>` with a real
registry-pullable BusyBox digest for the platform RunPod will pull. The current
dev resolver can synthesize fake digests for local evaluation; those are not
pullable by RunPod.

## Create the run

```sh
curl -X POST "$MERCATOR/v1/runs" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H 'Idempotency-Key: busybox-report-1' \
  -H 'Content-Type: application/json' \
  -d '{
    "workspace_id": "ws_1",
    "workload": {
      "workspace_id": "ws_1",
      "spec": {
        "containers": [{
          "image": "busybox@sha256:<real-index-digest>",
          "args": ["sh","-c",
            "set -e; URL=\"$MERCATOR_REPORT_URL/v1/runs/$MERCATOR_RUN_ID:report?workspace_id=$MERCATOR_WORKSPACE_ID\"; AUTH=\"Authorization: Bearer $MERCATOR_RUN_TOKEN\"; wget -q -O- --header \"$AUTH\" --header \"Content-Type: application/json\" --post-data \"{\\\"type\\\":\\\"progress\\\",\\\"data\\\":{\\\"pct\\\":50}}\" \"$URL\"; sleep 5; wget -q -O- --header \"$AUTH\" --header \"Content-Type: application/json\" --post-data \"{\\\"type\\\":\\\"exit\\\",\\\"exit_code\\\":0}\" \"$URL\""
          ]
        }],
        "resources": { "accelerators": [ { "vendor": "NVIDIA", "count": 1 } ] }
      }
    }
  }'
```

## Expected

- The run lands on RunPod (GPU accelerator required ⇒ docker infeasible).
- The Events tab shows a `compute.run.reported.v1` progress event then an exit
  event ("Workload report").
- On the exit report, the run outcome becomes **succeeded** and Mercator issues
  `DELETE /pods/{id}` — confirm the pod disappears from RunPod.
