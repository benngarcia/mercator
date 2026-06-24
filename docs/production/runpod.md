# RunPod Provider Runbook

Mercator's `runpod` adapter launches container **Pods** on RunPod, observes
them, and terminates them on cleanup. RunPod's API never exposes a container
exit code, so the **workload self-reports its exit code** (see
`workload-reporting.md`); Mercator treats that report as the authoritative run
outcome.

## Adding the connection

1. Provision a RunPod API key and export it where Mercator runs:
   ```sh
   export RUNPOD_API_KEY=rpa_...      # never commit this
   ```
2. Add the connection (UI **Connections → Add connection**, adapter type
   `runpod`), or via the API:
   ```sh
   curl -X POST "$MERCATOR/v1/connections" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
     -H 'Idempotency-Key: conn-runpod-1' \
     -H 'Content-Type: application/json' \
     -d '{"workspace_id":"ws_1","connection_id":"conn_runpod_main",
          "adapter_type":"runpod",
          "credential":{"source":"env","ref":"RUNPOD_API_KEY"}}'
   ```
3. Authorize it (runs a cheap `GET /pods` to validate the key):
   ```sh
   curl -X POST "$MERCATOR/v1/connections/conn_runpod_main:authorize?workspace_id=ws_1" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN"
   ```

## Connection config (optional)

| Key | Default | Meaning |
|-----|---------|---------|
| `gpu_types` | `NVIDIA RTX A2000,NVIDIA RTX A4000` | Comma-separated allow-list of GPU type ids advertised as offers. |
| `cloud_type` | `COMMUNITY` | `COMMUNITY` or `SECURE`. |
| `container_disk_gb` | `20` | Pod container disk size. |

## How runs land on RunPod

The scheduler picks the lowest-cost **feasible** offer. The local docker offer
is priced at 0, so a run lands on RunPod only when it is **infeasible on
docker** — declare a GPU accelerator requirement in the workload:

```json
"resources": { "accelerators": [ { "vendor": "NVIDIA", "count": 1 } ] }
```

Docker advertises no accelerators (infeasible); the RunPod offer advertises one
NVIDIA accelerator (feasible) and is selected.

## Lifecycle & cleanup

- Pods are named `mercator-<launchKey>` and carry `MERCATOR_*` ownership env.
- RunPod offers are **provisionable** ⇒ disposition **terminate** ⇒ cleanup
  issues `DELETE /pods/{id}`.
- On the workload's **exit report**, Mercator records the authoritative outcome
  and terminates the pod promptly. If a pod shows `EXITED` with no report, the
  run is marked **failed** (indeterminate) and the pod is terminated.

## Live verification

See `examples/runpod/` for two ready-to-run workloads. Both require the GPU
accelerator (so they land on RunPod), use the cheapest community GPU, and
auto-terminate on their exit report (< $0.01 each). Rotate the API key after
testing.
