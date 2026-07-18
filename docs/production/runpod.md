# RunPod Provider Runbook

<!-- Deferred (tracked in issue #60): once the adapter-manifest contract
merges, move the setup steps and the Secure vs Community explanation below
into the runpod adapter manifest so the console wizard renders them. -->

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
   curl -X POST "$MERCATOR/v1/connections/conn_runpod_main/authorize?workspace_id=ws_1" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN"
   ```

## Connection config (optional)

| Key | Default | Meaning |
|-----|---------|---------|
| `gpu_types` | `NVIDIA RTX A2000,NVIDIA RTX A4000` | Comma-separated allow-list of GPU type ids advertised as offers. |
| `allow_community_cloud` | `false` | Opt community-cloud capacity in. See below. |
| `container_disk_gb` | `20` | Pod container disk size. |

## Secure Cloud vs Community Cloud

RunPod sells two kinds of capacity. **Secure Cloud** runs in vetted partner
datacenters (RunPod's trusted tier); **Community Cloud** runs on hardware
operated by individual peer hosts, so your image, environment (including any
`MERCATOR_*` tokens), and data are handled by machines RunPod does not
operate. Community capacity is often cheaper; Secure is the safe default.

Mercator's adapter is **secure-cloud only by default** and enforces it in both
directions:

- **Offers**: each offer names its cloud (`<gpu>|SECURE` / `<gpu>|COMMUNITY`)
  and carries that cloud's own price and stock. Without the opt-in, only
  secure-cloud offers are advertised.
- **Launch**: pod creation always requests the offer's cloud explicitly
  (`cloudType`), refuses any community-cloud offer — even a stale one that
  predates a config change — unless `allow_community_cloud=true`, and if
  RunPod reports the created pod's machine as explicitly non-secure despite a
  SECURE request, the pod is destroyed and the launch fails loudly.

Set connection config `allow_community_cloud` to `true` to additionally
advertise community-cloud offers and permit launches on them. The removed
`cloud_type` key is rejected at connection build time.

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

### Cost attribution

When a run is launched, the adapter sends `gpuTypeIds = [selected offer's GPU] + (the rest of the allow-list as fallbacks)` to improve scheduling odds. **RunPod may provision any GPU in that list**, so the realized GPU — and therefore the actual hourly cost — may differ from the offer the scheduler quoted and costed the run against. Operators who need realized cost to exactly match the quoted offer should set the connection config `gpu_types` to a single GPU id, which removes all fallbacks.

## Live verification

See `examples/runpod/` for provider validation examples. Both require the GPU
accelerator so they land on RunPod, use the cheapest allow-listed secure-cloud
GPU, and auto-terminate on their exit report. They also require a public Mercator report
URL and real registry-pullable image digests; the server rejects mutable tags
at create time, and RunPod can only pull digests that actually exist in a
registry.

The BusyBox example reports progress and exit through ordinary HTTP using the
injected `MERCATOR_*` environment. Rotate the API key after testing.

### Image refs must be real and pullable

Unlike the local docker adapter, RunPod **actually pulls** the container image
on a provisioned host. The image ref that reaches the adapter must therefore be
a real, registry-pullable tag or digest. Two consequences:

- Mercator's image resolver must produce real registry digests. The default dev
  binary resolves tags to **synthetic** digests (for offline testing); those are
  not on any registry and RunPod rejects them with HTTP 500 "image … was not
  found on the registry." When testing RunPod with such a build, submit images
  already pinned to a **real** digest (e.g.
  `busybox@sha256:<real-index-digest>`); the resolver passes already-pinned refs
  through unchanged.
- Workloads self-report their exit code through `MERCATOR_REPORT_URL`. Behind a
  Cloudflare-fronted Mercator, the report client must send a non-default
  `User-Agent`. Raw `curl` or `wget` works, while plain `python-urllib` default
  agents can receive a 403 from Cloudflare's managed rules.
