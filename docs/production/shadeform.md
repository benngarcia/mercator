# Shadeform Provider Runbook

Mercator's `shadeform` adapter **rents GPU VMs** through Shadeform
(api.shadeform.ai), a marketplace aggregator that fronts ~21 provider clouds
(Lambda, Nebius, Crusoe, Voltage Park, Vultr, Paperspace, and more) behind one
API and one invoice, and **bootstraps a Mercator node agent onto every machine
it rents**.

It is a capacity provider and nothing else. A machine it rents is one Mercator
holds across Runs, so what executes there is the enrolled agent's business: the
create body carries a bootstrap script and never a workload image. Shadeform is
therefore in the **reusable** lane, and the one-shot launch path this adapter
used to have is gone. If you need a one-shot container on a rented VM, use the
`runpod` or `vast` connections.

Shadeform's lifecycle is **VM-only**: an instance reports
`creating → pending_provider → pending → active → deleting → deleted` (with an
`error` off-ramp). There is no stop, no resume, and no suspend. `ObserveCapacity`
reports the machine and says nothing about the work on it; what makes work on
that machine observable is the node's own session (see `node-agent.md`).

## What the connection can and cannot promise

| Promise | Shadeform | Why |
|---|---|---|
| Stop | no | `/instances/{id}/delete` destroys a machine; nothing suspends one. |
| Resume | no | Nothing was suspended. |
| Persistent disk across a stop | no | A disk that survives a stop is a claim about a provider that can stop. |
| Spot / interruptible | no | Not offered through this API. |
| Exact pricing | yes | The catalog states an hourly price in cents. |
| Idempotent provision | by tag reconciliation | Create honours no operation key, so the Rental's own tag is the identity. |
| Owned capacity listable | yes | `GET /instances` returns the whole account, filtered client-side. |
| Observable after terminate | yes, briefly | A destroyed instance stays listed while it is `deleting`, then disappears. |

Mercator refuses a stop or a resume at the seam, before any API call, with
`capability: operation unsupported by this backend`.

## Adding the connection

1. Mint an API key at **platform.shadeform.ai → Settings → API**. Shadeform API
   keys are **admin-scoped** — there are no read-only or restricted keys, so
   treat the key like a billing credential. The adapter sends it as the
   `X-API-KEY` header.
   ```sh
   export SHADEFORM_API_KEY=...      # never commit this
   ```
2. Publish the `mercator-node` binary where a rented machine can fetch it over
   https. Mercator's release archives do not carry it today
   ([#234](https://github.com/benngarcia/mercator/issues/234)), so build and host
   it yourself:
   ```sh
   GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
     -o mercator-node ./cmd/mercator-node
   # upload it to, for example, https://downloads.example.com/mercator-node/$VERSION/linux-amd64
   ```
   The URL you configure must contain `{version}`, which Mercator replaces with
   the agent build the bootstrap pinned.
3. Add the connection (UI **Connections → Add connection**, adapter type
   `shadeform`), or via the API:
   ```sh
   curl -X POST "$MERCATOR/v1/connections" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
     -H 'Idempotency-Key: conn-shadeform-1' \
     -H 'Content-Type: application/json' \
     -d '{"workspace_id":"ws_1","connection_id":"conn_shadeform_main",
          "adapter_type":"shadeform",
          "config":{"agent_download_url":"https://downloads.example.com/mercator-node/{version}/linux-amd64"},
          "credential":{"source":"env","ref":"SHADEFORM_API_KEY"}}'
   ```
4. Authorize it (runs a cheap `GET /instances` to validate the key):
   ```sh
   curl -X POST "$MERCATOR/v1/connections/conn_shadeform_main/authorize?workspace_id=ws_1" \
     -H "Authorization: Bearer $MERCATOR_API_TOKEN"
   ```

## Connection config

| Key | Required | Default | Meaning |
|-----|----------|---------|---------|
| `agent_download_url` | yes | *(none)* | Where a rented machine fetches the node agent. Must be https and must contain `{version}`, replaced with the build the bootstrap pinned. There is no default: Mercator publishes no agent binary, so a guessed URL would be a paid machine fetching a 404 and never enrolling. A connection without it still verifies and still lists capacity; it refuses to provision. |
| `shade_cloud` | no | `true` | `true` rents in Shadeform's managed account (one invoice); `false` uses your linked bring-your-own-cloud accounts. |
| `allowed_clouds` | no | *(all)* | Comma-separated allow-list of provider cloud slugs (e.g. `lambdalabs,nebius`). When set, listings are filtered to it and a provision outside it is rejected. This is the only "secure cloud" control: the API exposes no per-provider trust attributes, so vetting a provider means putting it on this list. |
| `max_lifetime_hours` | no | `24` | Reclamation backstop, **not** the lease. Every instance gets Shadeform `auto_delete` thresholds: a date threshold and a spend cap of the catalog hourly price over that window. When the provision command carries a lifetime bound, the horizon is that bound plus one hour of slack. Zero-priced catalog entries (bring-your-own-cloud inventory bills through your provider, not Shadeform) get the date threshold only — Shadeform leaves `"0.00"` spend-threshold semantics undefined. If the whole broker dies, Shadeform reclaims the instance on its own. |
| `os` | no | *(auto)* | Explicit OS image. By default the adapter picks the first `*_shade_os` option for the instance type — those images bake in the GPU driver and the container runtime the node agent needs to run anything. If a type offers no shade_os image, provisioning on it fails loudly rather than renting a machine whose agent can start no container; set this key to override. |
| `base_url` | no | `https://api.shadeform.ai/v1` | Shadeform API origin. Set it to reach Shadeform through an egress proxy of your own. |

## What a rented machine holds

The create body's `launch_configuration` is a **script**, base64-encoded, that
runs once the instance is active. It:

- installs the pinned `mercator-node` binary from `agent_download_url` to
  `/usr/local/bin/mercator-node`;
- writes the node identity and the enrollment token to
  `/etc/mercator-node/bootstrap.env`, mode `0600`;
- installs and starts `mercator-node.service` with `Restart=always`, so a crashed
  agent comes back on a machine nobody can log into.

The machine therefore holds **one credential**: a single-use enrollment token
that expires in 30 minutes and is spent the moment the agent redeems it. No
Mercator API token, no provider credential, and no registry account is ever
written to it. It listens on nothing, publishes no Docker socket, and every
exchange with the control plane is one the agent opens outbound. See
`node-agent.md` for what happens to the session after that.

Values that no unattended script can carry (empty, or containing whitespace or
non-printable characters) are refused before an instance is created, and the
refusal never quotes the value, because one of them is a credential.

## How listings work

`GET /instances/types?sort=price` is the catalog. Placement on Shadeform is an
explicit **(cloud, region, shade_instance_type)** triple, so each listed region
of each type becomes one listing whose native ref is that triple. Listings carry
the catalog's `hourly_price` (cents → USD/second) and `boot_time` estimates.

A listing states the machine, and nothing about what executing on it would be
like. A container runtime, an idempotent launch and a concurrency limit are the
enrolled agent's facts, established from the machine itself, and they arrive on
that node's own offer once an agent is on it.

Only `deployment_type: "vm"` inventory is listed. The docs never define what a
launch configuration means on `container`- or `baremetal`-typed inventory; the
adapter excludes those and logs the excluded count (open question with Shadeform
support).

A region with no stock right now is published as capacity that is unavailable
rather than dropped: a sold-out region is a wait, and a machine type nobody
sells is a shape that has to be added, and the queue is ordered on the
difference.

The catalog exposes no host CPU architecture, so listings advertise `amd64`
except Grace-superchip types (GH200/GB200), which are advertised as `arm64` —
renting a Grace host for an amd64 image would die at exec, invisibly to the
VM-only status. Verify the architecture of any new exotic type before relying
on it.

## Lifecycle, ownership, and reconciliation

- Instances are named `mercator-<rentalID>` and carry `mercator:*` **tags**:
  rental, generation, workspace, and ownership token. Those are exactly the
  fields the reconciler reads back, because the account listing is the only place
  it can read them from.
- Create has **no idempotency key**, so provisioning is made idempotent
  client-side: scan for a live instance tagged with this Rental before creating;
  scan again afterwards and, if a concurrent duplicate slipped through, keep the
  oldest and destroy the rest. A create whose outcome is unknown is reconciled
  the same way rather than by a second create. The residual race (two
  provisioners both pass the pre-scan and both die before reconciling) is bounded
  by every later path plus the `auto_delete` caps.
- `TerminateCapacity` destroys **every** live instance tagged with the Rental,
  not merely the one named, so a reconciliation that failed halfway converges
  back to zero. A terminate that finds nothing live reports a duplicate: the
  machine is already gone.
- `ListOwnedCapacity` filters the full-account `GET /instances` list (the
  endpoint has no query parameters) client-side by the **Rental tag** and
  excludes instances already `deleting` — Shadeform stops billing when `deleting`
  starts. A machine carrying no Rental tag is not capacity Mercator holds.
- An instance that has left the listing entirely observes as **terminated**:
  either it was destroyed or `auto_delete` reclaimed it, and a caller told
  "unknown" would go on waiting for an agent that has no machine to arrive from.
- The observation carries no `state_since`. The only moment a Shadeform instance
  record holds is `created_at`, which is when the machine was asked for rather
  than when it reached the state being reported.
- An `error` status observes as **unknown**. The machine may still exist and
  still bill, so it is not reported terminated; the enrolment deadline reclaims
  it.

## Correlating provider failures

The public run event identifies the failure without exposing Shadeform's
response. Read the run's events and find `compute.run.launch_failed.v1` or
`compute.run.launch_indeterminate.v1`:

```sh
curl -fsS "$MERCATOR/v1/runs/$RUN_ID/events?workspace_id=$WORKSPACE_ID" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  | jq '.events[] | select(.type == "compute.run.launch_failed.v1" or .type == "compute.run.launch_indeterminate.v1") | {correlationid, data}'
```

The event's `data.code`, `data.retryable`, and `data.side_effect` are stable,
provider-neutral fields. Use its `correlationid` (the run ID) with the workspace
ID to find the matching `provider operation failed` process-log record. For the
default text log, the correlation looks like:

```sh
grep 'provider operation failed' /path/to/mercator.log \
  | grep "workspace_id=$WORKSPACE_ID" \
  | grep "run_id=$RUN_ID"
```

Keep that process log on the operator side of the trust boundary. Mercator
redacts the API key and the bootstrap script from any response body it records,
and publishes no provider response body, authorization header, or request payload
through run events or sinks.

## Live verification, and what is blocked

**No live Shadeform run has been performed against this adapter since it became a
capacity provider.** The whole path is proven under the package's httptest fake.
The live half is [#235](https://github.com/benngarcia/mercator/issues/235), and
these are the commands it needs, with a funded account:

```sh
export SHADEFORM_API_KEY=...          # from 1Password, never a shell rc file
export MERCATOR=http://127.0.0.1:8080
export MERCATOR_API_TOKEN=...

# 1. Authorize the connection (validates the key with GET /instances).
curl -fsS -X POST "$MERCATOR/v1/connections/conn_shadeform_main/authorize?workspace_id=ws_1" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN"

# 2. Rent one machine and watch its agent enrol.
go run ./cmd/mercator run create --workspace-id ws_1 ...   # a Run whose placement provisions
go run ./cmd/mercator run events --workspace-id ws_1 --run-id "$RUN_ID" \
  | jq '.events[] | select(.type | startswith("compute.run.capacity")) | {type, data}'
curl -fsS "$MERCATOR/v1/nodes?workspace_id=ws_1" -H "Authorization: Bearer $MERCATOR_API_TOKEN" | jq

# 3. Confirm the machine is destroyed and the account is empty of Mercator tags.
curl -fsS https://api.shadeform.ai/v1/instances -H "X-API-KEY: $SHADEFORM_API_KEY" \
  | jq '[.instances[] | select(.tags[]? | startswith("mercator:"))]'
```

Two things block step 2 in production regardless of credentials, and both are
filed: a capacity connection publishes no placement candidate
([#200](https://github.com/benngarcia/mercator/issues/200)), and a launch is
still addressed through the selected offer's native ref rather than the machine a
provisioning built ([#207](https://github.com/benngarcia/mercator/issues/207)).
Until those land, a live run exercises provisioning through the capacity seam
directly rather than through a Run.

Rotate the API key after testing — keys are admin-scoped.
