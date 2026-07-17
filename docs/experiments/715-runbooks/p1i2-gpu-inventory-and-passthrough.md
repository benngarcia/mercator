# Runbook: verify GPU inventory and passthrough on a remote docker connection

Item p1i2 of bucket-rails issue #715 (Track A). The code is in this PR: the
Docker adapter probes real accelerator inventory (one-shot `nvidia-smi` probe
container, gated on the daemon's `nvidia` runtime) and passes `--gpus <count>`
at container create when the launched workload requested accelerators. This
runbook covers the live acceptance steps an operator must run; the
implementing agent is fenced to repo changes only.

## What the code does now

- `docker info` parsing surfaces the daemon's registered runtimes;
  `HostInfo.HasNvidiaRuntime()` gates the GPU probe so CPU-only endpoints
  never launch it.
- `CLIClient.AcceleratorInventory` runs
  `docker run --rm --network=none --gpus all busybox:1.37 nvidia-smi
  --query-gpu=name,memory.total --format=csv,noheader,nounits` against the
  connection's endpoint (loopback, `ssh://`, or `tcp://` alike — the NVIDIA
  container runtime injects `nvidia-smi` into any container, so no CUDA image
  pull). Results are cached for one minute per endpoint, like the disk probe.
- Offers advertise `resources.accelerators` with the canonical model id from
  `internal/gpunorm` (nvidia-smi's "NVIDIA GeForce RTX 5090" and RunPod's
  "RTX 5090" both canonicalize to `nvidia-rtx-5090`), so the scheduler's
  existing `ModelAnyOf` matching works across providers.
- `docker create` gets `--gpus <count>` where count is the sum of the
  workload's accelerator requirement counts; a workload that requested no
  accelerators gets no GPU access even on a GPU host.

## Preconditions (live, operator-only)

1. ws has nvidia-container-toolkit configured for Docker:

   ```bash
   ssh ws docker info --format '{{json .Runtimes}}' | jq 'keys'   # must include "nvidia"
   ```

   If missing: `sudo nvidia-ctk runtime configure --runtime=docker && sudo systemctl restart docker`.
2. ws is reachable from the staging Mercator accessory as a remote docker
   endpoint for the experiment window (issue #715 Q3: tagged ephemeral
   tailscale key from Infisical; deregister after the window; never mutate the
   bootstrap connection).

## 1. Register ws as a second docker connection (staging Mercator)

The bootstrap connection is immutable; ws joins as a NEW connection via the
API (config keys: `bin`, `host`, `context`):

```bash
curl -sS -X POST https://mercator.bucket.bot/v1/connections \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"workspace_id":"<staging workspace>","connection_id":"conn_docker_ws","adapter_type":"docker","config":{"host":"ssh://root@ws"}}'
# then authorize it via POST /v1/connections/{conn_action}
```

Note: the accessory's container must be able to `docker --host ssh://root@ws`
— it needs an ssh client + key material for ws. If that transport is not
provisioned in the accessory image, expose ws's daemon as tcp:// bound to its
tailnet address instead (never a public interface).

## 2. Verify the offer advertises the RTX 5090

```bash
curl -sS "https://mercator.bucket.bot/v1/offers" -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  | jq '.[] | select(.connection_id=="conn_docker_ws") | .resources.accelerators'
# expect: vendor NVIDIA, canonical_model "nvidia-rtx-5090", count 1, memory ~32 GiB
```

The first call pays the one-shot probe (up to ~30 s including the busybox
pull); subsequent calls within a minute are cached.

## 3. Acceptance run (GPU workload schedules onto ws, nvidia-smi works)

1. Create a workload revision whose spec requests one nvidia accelerator
   (`resources.accelerators: [{"vendor":"nvidia","model_any_of":["nvidia-rtx-5090"],"count":1}]`)
   and whose container runs `nvidia-smi` (any CUDA-enabled image, or busybox —
   the runtime injects the binary).
2. `POST /v1/runs` and follow `GET /v1/runs/{run_id}/decision`:
   - the selected offer must be `conn_docker_ws`'s;
   - the bootstrap CPU-only connection's candidate must show
     `RESOURCE_INSUFFICIENT resources.accelerators`.
3. Confirm GPU visibility inside the launched container:

   ```bash
   ssh ws docker logs <launch_key>   # nvidia-smi table listing the RTX 5090
   ```

## 4. Negative check (CPU-only endpoint rejected)

Submit the same spec with placement restricted to (or offers collected only
from) the bootstrap CPU-only connection: the run must fail placement with
`no feasible offers`, and the decision must record
`RESOURCE_INSUFFICIENT resources.accelerators` for that offer. Unit coverage
for exactly this matrix lives in
`internal/adapter/docker/offer_test.go`
(`TestGPUSpecSchedulesOnGPUDockerOfferAndRejectsCPUOnlyOffer`).

## 5. Teardown

Deregister/deauthorize `conn_docker_ws` at the end of the experiment window
and revoke the ephemeral tailscale key (Q3). The bootstrap connection stays
untouched.
