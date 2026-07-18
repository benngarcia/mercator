# Vast.ai Provider Runbook

<!-- Deferred (tracked in issue #60): once the adapter-manifest contract
merges, move the setup steps and the secure-tier explanation below into the
vast adapter manifest so the console wizard renders them. -->

Mercator's `vast` adapter rents container instances on Vast.ai, a marketplace
of GPU hosts, observes them, and destroys them on cleanup. Vast's API never
exposes a container exit code, so the **workload self-reports its exit code**
(see `workload-reporting.md`); Mercator treats that report as the
authoritative run outcome.

## Secure tier only — not configurable

Vast.ai sells capacity across trust tiers: unverified peer hosts, verified
machines, and machines in **certified datacenters** (Vast's "Secure Cloud"
offering, ISO/IEC 27001-certified facilities run by vetted datacenter
partners). On lower tiers your image, environment (including `MERCATOR_*`
tokens), and data are handled by hardware operated by anonymous individuals.

This adapter only touches the top tier, and there is deliberately **no
configuration that relaxes this**:

- **Offers**: the marketplace search hard-codes `datacenter=true` and
  `verification=verified`, and every returned offer is re-checked before it is
  advertised.
- **Launch**: the selected offer's ask id is re-resolved against the live
  marketplace under the same secure-tier filters before renting. A stale offer
  or misconfigured caller pointing at community capacity does not resolve and
  the launch is refused before any money moves. After create, an instance
  whose machine reports an explicitly non-verified status is destroyed and the
  launch fails loudly.

## Adding the connection

1. Provision a Vast.ai API key (console → Account → API Keys) and export it
   where Mercator runs:
   ```sh
   export VAST_API_KEY=...            # never commit this
   ```
2. Add the connection (adapter type `vast`):
   ```sh
   curl -X POST "$MERCATOR/v1/connections" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
     -H 'Idempotency-Key: conn-vast-1' \
     -H 'Content-Type: application/json' \
     -d '{"workspace_id":"ws_1","connection_id":"conn_vast_main",
          "adapter_type":"vast",
          "credential":{"source":"env","ref":"VAST_API_KEY"}}'
   ```
3. Authorize it (runs a cheap `GET /users/current/` to validate the key):
   ```sh
   curl -X POST "$MERCATOR/v1/connections/conn_vast_main/authorize?workspace_id=ws_1" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN"
   ```

## Connection config (optional)

| Key | Default | Meaning |
|-----|---------|---------|
| `gpu_names` | (all) | Comma-separated Vast GPU names to search for (e.g. `RTX 4090,H100 SXM`; underscores are accepted and normalized to spaces). |
| `container_disk_gb` | `20` | Instance disk size when the workload declares no disk requirement. |
| `offer_limit` | `20` | Max marketplace offers advertised per query (cheapest first). |

## Offers

Each offer is one live marketplace ask: real per-offer pricing (`dph_total`,
the all-in on-demand rate for the GPU slice plus the requested disk), the
host's GPU model and count, and the host's empirical reliability score
(`reliability2`), which the scheduler prices as an interruption risk. Offers
expire after five minutes; the ask id is the offer's native ref.

## Lifecycle & cleanup

- Instances are labeled `mercator-<launchKey>` and carry `MERCATOR_*`
  ownership env (readable back through the instances API), so launches are
  idempotent: find-before-create, and concurrent duplicate creates collapse
  deterministically (lowest contract id wins, the loser destroys its own
  instance).
- Vast offers are **provisionable** ⇒ disposition **terminate** ⇒ cleanup
  issues `DELETE /instances/{id}/` (destroy, not stop: a stopped Vast instance
  keeps billing for storage).
- On the workload's **exit report**, Mercator records the authoritative
  outcome and destroys the instance promptly. An instance showing `exited`
  with no report is marked **failed** (indeterminate) and destroyed. A host
  showing `offline` is non-terminal; the run's timeouts decide when to give
  up.

## Limitations

- **Entrypoint overrides are rejected.** Vast's create API accepts an args
  array for the image's own ENTRYPOINT but no exec-form entrypoint override
  (only a shell-parsed startup string, which would corrupt argv). Bake the
  entrypoint into the image and pass `args`.
- **Private registry images are not supported.** Vast supports registry
  credentials only as a `image_login` string at create time
  (`-u user -p pass server`); Mercator's connection credential carries exactly
  one secret (the Vast API key) and does not smuggle registry passwords
  through adapter config. Use public, digest-pinned images.
- **Port publishing is not mapped.** Workload `ports` are ignored; the
  workload report path needs only outbound HTTP.
- GPU workloads should declare an accelerator requirement so the local docker
  offer is infeasible and the Vast offer can win placement, same as RunPod
  (see `runpod.md`).
