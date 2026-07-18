# Shadeform Provider Runbook

Mercator's `shadeform` adapter provisions GPU **VMs** through Shadeform
(api.shadeform.ai), a marketplace aggregator that fronts ~21 provider clouds
(Lambda, Nebius, Crusoe, Voltage Park, Vultr, Paperspace, and more) behind one
API and one invoice. Each run creates an instance whose **docker launch
configuration** pulls and runs exactly one container with `--network=host`.

Shadeform's lifecycle is **VM-only**: an instance reports
`creating → pending_provider → pending → active → deleting → deleted` (with an
`error` off-ramp) and stays `active` forever no matter what the container does.
There is no container status, no exit code, no logs endpoint, and no webhooks.
Mercator therefore observes only the VM phase, and the **workload's signed exit
report** (see `workload-reporting.md`) is the authoritative run outcome — the
same pessimistic pattern as the RunPod adapter.

## Adding the connection

<!-- Follow-up for the beng/connection-wizard branch: once the adapter-manifest
contract merges, serve this section as a Shadeform manifest (display metadata +
setup steps + credential form) from the API instead of docs-only prose. -->

1. Mint an API key at **platform.shadeform.ai → Settings → API**. Note:
   Shadeform API keys are **admin-scoped** — there are no read-only or
   restricted keys, so treat the key like a billing credential. The adapter
   sends it as the `X-API-KEY` header.
   ```sh
   export SHADEFORM_API_KEY=...      # never commit this
   ```
2. Add the connection (UI **Connections → Add connection**, adapter type
   `shadeform`), or via the API:
   ```sh
   curl -X POST "$MERCATOR/v1/connections" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
     -H 'Idempotency-Key: conn-shadeform-1' \
     -H 'Content-Type: application/json' \
     -d '{"workspace_id":"ws_1","connection_id":"conn_shadeform_main",
          "adapter_type":"shadeform",
          "credential":{"source":"env","ref":"SHADEFORM_API_KEY"}}'
   ```
3. Authorize it (runs a cheap `GET /instances` to validate the key):
   ```sh
   curl -X POST "$MERCATOR/v1/connections/conn_shadeform_main:authorize?workspace_id=ws_1" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN"
   ```

## Connection config (optional)

| Key | Default | Meaning |
|-----|---------|---------|
| `shade_cloud` | `true` | `true` launches in Shadeform's managed account (one invoice); `false` uses your linked bring-your-own-cloud accounts. |
| `allowed_clouds` | *(all)* | Comma-separated allow-list of provider cloud slugs (e.g. `lambdalabs,nebius`). When set, offers are filtered to it and launches outside it are rejected. This is the only "secure cloud" control: the API exposes no per-provider trust attributes (SOC2/Tier claims are platform-level marketing), so vetting a provider means putting it on this list. |
| `max_lifetime_hours` | `24` | Reclamation backstop, **not** the run timeout. Every instance gets Shadeform `auto_delete` thresholds: a date threshold and a spend cap of the catalog hourly price over that window. When the run carries an execution bound (`max_runtime_seconds`), the horizon is that bound plus one hour of slack; this config is the horizon only for runs without one. Zero-priced catalog entries (bring-your-own-cloud inventory bills through your provider, not Shadeform) get the date threshold only — Shadeform leaves `"0.00"` spend-threshold semantics undefined. If the whole broker dies, Shadeform reclaims the instance on its own. |
| `os` | *(auto)* | Explicit OS image. By default the adapter picks the first `*_shade_os` option for the instance type — those images bake in GPU drivers and the container runtime the docker launch configuration depends on. If a type offers no shade_os image, launches on it fail loudly rather than booting a VM whose container may never start; set this key explicitly to override. |
| `registry_username` / `registry_password` | *(none)* | Registry credentials passed through to `docker_configuration.registry_credentials` for private images. Username/password is all Shadeform supports (no token exchange); for ghcr.io, use a GitHub PAT with `read:packages` as the password. |

## How offers work

`GET /instances/types?available=true&sort=price` is the catalog. Placement on
Shadeform is an explicit **(cloud, region, shade_instance_type)** triple, so
each available region of each type becomes one offer whose native ref is that
triple. Offers carry the catalog's `hourly_price` (cents → USD/second) and
`boot_time` estimates so the scheduler can score cost and start latency.

Only `deployment_type: "vm"` inventory is offered. The docs never define what a
docker launch configuration means on `container`- or `baremetal`-typed
inventory; the adapter excludes those and logs the excluded count (open
question with Shadeform support).

The catalog exposes no host CPU architecture, so offers advertise `amd64`
except Grace-superchip types (GH200/GB200), which are advertised as `arm64` —
placing an amd64 image on a Grace host would die at exec, invisibly to the
VM-only status. Verify the architecture of any new exotic type before relying
on it.

## Lifecycle, ownership, and cleanup

- Instances are named `mercator-<launchKey>` and carry `mercator:*` **tags**
  (launch key, workspace, run, attempt, ownership token, request hash, cleanup
  locator) plus the matching `MERCATOR_*` container env.
- Shadeform's create has **no idempotency key**, so Launch is made idempotent
  client-side: scan for a live tagged instance before creating; scan again
  after creating and, if a concurrent duplicate slipped through, keep the
  oldest and delete the rest. The residual race (both launchers crash before
  reconciling) is bounded by every later path — Observe, cleanup, and the
  janitor all resolve **every** tagged match — plus the `auto_delete` caps.
- Offers are **provisionable** ⇒ disposition **terminate** ⇒ cleanup calls
  `POST /instances/{id}/delete`. `Release` does exactly the same thing (the
  instance is both "our slot" and "the host we own"). `/restart` is never used:
  whether it re-runs the launch configuration is undocumented.
- The janitor's `ListOwned` filters the full-account `GET /instances` list
  (the endpoint has no query parameters) client-side by our tag namespace and
  **excludes instances already in `deleting`** — Shadeform stops billing when
  `deleting` starts.
- An `error` status observes as **failed**; an instance that disappeared (or is
  deleting/deleted) observes as **released**.

## Workload semantics and limits

- **One container per run.** `launch_configuration.type: "docker"` runs exactly
  one image. Multi-container workloads are infeasible on this adapter.
- **No entrypoint override.** Shadeform's docker configuration has no
  entrypoint field. Offers declare this incapability, so the scheduler never
  places an entrypoint-overriding workload here (it falls back to adapters
  that support it); a launch that reaches the adapter anyway is rejected
  loudly. Bake the entrypoint into the image or express it as args.
- **Args are one shell string.** Mercator's argv is shell-quoted and joined
  before it reaches `docker_configuration.args`.
- **No port mappings, host networking.** The adapter maps no ports and offers
  no inbound network capability, so workloads that need public inbound ports
  never schedule here. Because the container runs with `--network=host`, any
  port the workload happens to listen on is exposed as far as the provider's
  firewall allows — treat these instances as egress-only workers and don't
  bind services you wouldn't expose.
- **GPU passthrough is implicit.** `*_shade_os` images bake in drivers and
  there is no `--gpus` equivalent in the API; the docs treat GPU visibility
  inside the container as automatic. Verify with `nvidia-smi` on first use of a
  new instance type (see below).

## Live verification checklist

With a funded account and `SHADEFORM_API_KEY` exported:

1. Launch a tiny CUDA workload (e.g. `nvidia/cuda:12.2.0-base-ubuntu22.04`
   running `nvidia-smi`) on the cheapest available type; confirm the GPU is
   visible inside the container, since passthrough is undocumented.
2. Confirm the instance carries the `mercator:*` tags and that the run's exit
   report finalizes the outcome while the instance stays `active`.
3. Confirm cleanup: on run completion the instance moves to `deleting`, and
   `mercator` (janitor) reports no owned instances afterwards.
4. Confirm the backstop: create an instance, kill the broker, and check
   Shadeform deletes it at the `auto_delete` date threshold.

Rotate the API key after testing — keys are admin-scoped.
