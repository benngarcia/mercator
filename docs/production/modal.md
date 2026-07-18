# Modal Provider Runbook

Mercator's `modal` adapter launches container runs as **Modal Sandboxes**
(https://modal.com/docs/guide/sandboxes), observes them to completion, and
terminates them on cleanup. Unlike RunPod, Modal reports the container's real
exit code, so the adapter's `Observe` is **authoritative on success vs
failure** — a run that exits 0 is recorded succeeded even if the workload
never self-reports.

## Credential format

Modal authenticates with a token pair. Mercator's credential model stores one
secret per connection, so the pair is packed into a single value:

```
<token_id>:<token_secret>          # e.g. ak-abc123:as-xyz789
```

Create the token in the Modal dashboard (Settings → API Tokens).

## Adding the connection

1. Export the packed credential where Mercator runs:
   ```sh
   export MODAL_CREDENTIAL="ak-...:as-..."   # never commit this
   ```
2. Add the connection (UI **Connections → Add connection**, adapter type
   `modal`), or via the API:
   ```sh
   curl -X POST "$MERCATOR/v1/connections" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
     -H 'Idempotency-Key: conn-modal-1' \
     -H 'Content-Type: application/json' \
     -d '{"workspace_id":"ws_1","connection_id":"conn_modal_main",
          "adapter_type":"modal",
          "credential":{"source":"env","ref":"MODAL_CREDENTIAL"}}'
   ```
3. Authorize it (exchanges the token pair for a Modal auth token — a full
   credential check that launches nothing):
   ```sh
   curl -X POST "$MERCATOR/v1/connections/conn_modal_main:authorize?workspace_id=ws_1" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN"
   ```

## Connection config (optional)

| Key | Default | Meaning |
|-----|---------|---------|
| `gpu_types` | `T4` | Comma-separated list of Modal GPU types advertised as offers (e.g. `T4,A100-80GB,H100`). The special entry `cpu` advertises a CPU-only offer. |
| `environment` | workspace default | Modal environment sandboxes run in. |
| `app_name` | `mercator` | Modal App the sandboxes are created under. |
| `timeout_seconds` | `86400` | Sandbox max lifetime; Modal kills the sandbox at this TTL, capping the cost of a lost run. |
| `server_url` | `https://api.modal.com:443` | Modal control plane (testing only). |

## Offers are catalog entries, not live capacity

Modal is serverless: there is no marketplace inventory to list. The adapter
synthesizes one offer per configured GPU type, priced from Modal's published
on-demand rates (GPU + CPU + memory, https://modal.com/pricing). A GPU type
the adapter has no rate for is still offered but with **unknown pricing**,
which the default scheduling policy rejects; set the workload's
`allow_unknown_pricing` to accept it.

Offers are **provisionable** ⇒ disposition **terminate** ⇒ cleanup terminates
the sandbox.

## How runs land on Modal

The scheduler picks the lowest-cost feasible offer. The local docker offer is
priced at 0, so a run lands on Modal when it is infeasible on docker — e.g. a
GPU requirement:

```json
"resources": { "accelerators": [ { "vendor": "NVIDIA", "count": 1 } ] }
```

or when the workspace has no authorized docker connection (the `cpu` offer
then serves CPU-only workloads).

## Lifecycle, ownership, cleanup

- Sandboxes are named `mercator-<sha256(launchKey)>` (hashed because Modal
  caps object names at 63 characters). The name is unique among the App's
  *live* sandboxes, so a concurrent duplicate launch collides at create; a
  retry arriving after the first sandbox exited is deduped by an ownership
  lookup before create. Both resolve to the existing sandbox instead of
  running the workload twice.
- Ownership fields ride on sandbox **tags** (`mercator_launch_key`,
  `mercator_ownership_token`, …) and are also injected as `MERCATOR_*`
  environment variables for the workload report path.
- Transient control-plane failures retry under a stable idempotency key
  (mirroring Modal's official SDK); an ambiguous `SandboxCreate` failure is
  reported as launch-indeterminate so the orchestrator reconciles instead of
  assuming nothing was created.
- The workload's command **must** be explicit: Modal sandboxes run the
  provided entrypoint/args and never the image's default `CMD`. A workload
  without either fails at launch rather than idling silently until the TTL.
- Exit mapping: exit code preserved as reported; sandbox TTL expiry maps to
  failed/124; an external kill (including `sandbox.terminate`) maps to
  failed/137.
- Reclamation (`ListOwned`) reports live sandboxes only. Exited sandboxes
  hold no billable capacity and Modal retains their records indefinitely, so
  they are deliberately excluded from the janitor's working set.

## Image refs must be real and pullable

Modal builds the image from `FROM <ref>` on its own builders, so the ref must
be a real, registry-pullable tag or digest — synthetic dev digests fail the
build with a manifest-not-found error, surfaced in the launch error. Private
registries are not yet wired up (the connection has no registry-secret
config); use public images or images in Modal's cache.
