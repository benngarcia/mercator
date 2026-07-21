# Provider Conformance Trials

`mercator verify` launches a real probe through the same authenticated HTTP,
placement, provider, reporting, and cleanup path used by the server. It creates
an isolated temporary SQLite database and one temporary workspace and
connection. A passing verdict means all of these conditions held:

1. the provider authorized the supplied credential;
2. a known-USD offer fit the declared maximum cost;
3. the provider launched the digest-pinned probe image;
4. the selected scenario reached its expected terminal outcome;
5. the Run closed with confirmed cleanup; and
6. the provider listed zero objects owned by the trial workspace.

The command returns JSON evidence on stdout. `passed` exits 0. `failed` or
`blocked` exits 1. An invalid trial document exits 2.

## Trial Document

```json
{
  "adapter_type": "runpod",
  "credential_env": "RUNPOD_API_KEY",
  "config": {
    "gpu_types": "NVIDIA RTX A4000"
  },
  "image": "ghcr.io/benngarcia/mercator-conformance-probe@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "mode": "probe",
  "max_expected_cost_usd": 0.50,
  "timeout": "12m"
}
```

`adapter_type` accepts `docker`, `runpod`, `shadeform`, or `vast`. Cloud
providers require `credential_env`; Docker rejects it. `image` must be an OCI
digest reference. Resolve the published probe tag to its current digest before
creating the trial. `config` accepts the same keys documented by the selected
provider manifest. `mode` defaults to `probe`, which requires a signed zero
exit. Set it to `launch-cancel` to prove that an accepted instance can be
cancelled and cleaned up through the same Run lifecycle.

The cost gate is deliberately conservative and simple:

```text
maximum expected cost = offer rate per second x timeout seconds
```

Mercator rejects unknown or non-USD pricing and refuses to launch when every
offer exceeds the declared budget. The limit also becomes the Run placement
budget and the timeout becomes its maximum runtime.

## Credentials And Report Routing

The JSON document contains the name of an environment variable, never its
value. The verifier resolves only that named variable when it constructs the
provider adapter. Credential material is absent from arguments, evidence, and
persisted events.

Cloud and remote-Docker instances must reach the verifier to report completion.
They require a fixed listener port and an externally reachable origin. The
verifier rejects missing, dynamic-port, or path-bearing callback topology
before it contacts the provider:

```sh
export RUNPOD_API_KEY='rpa_...'
export MERCATOR_CONFORMANCE_LISTEN_ADDR='0.0.0.0:8082'
export MERCATOR_CONFORMANCE_PUBLIC_URL='https://reports.example.com'

mercator verify --spec runpod-trial.json | tee runpod-evidence.json
```

Terminate TLS before the verifier and route the public URL to its listen
address. Keep the endpoint private except for the duration of the trial where
possible. Per-Run bearer tokens authenticate probe reports.

## Local Docker Proof

Docker Desktop and OrbStack expose the host as `host.docker.internal`, which
the verifier uses for probe reports. Build the dedicated probe image, publish
it to a registry the launched container can pull from, and use the returned
digest in a Docker trial:

```sh
docker build -f conformance/Dockerfile -t registry.example.com/mercator-conformance-probe:local .
docker push registry.example.com/mercator-conformance-probe:local
IMAGE="$(docker image inspect registry.example.com/mercator-conformance-probe:local --format '{{index .RepoDigests 0}}')"

jq -n --arg image "$IMAGE" '{
  adapter_type: "docker",
  image: $image,
  max_expected_cost_usd: 0.01,
  timeout: "2m"
}' > docker-trial.json

MERCATOR_CONFORMANCE_LISTEN_ADDR='0.0.0.0:8082' \
  mercator verify --spec docker-trial.json | jq .
```

A passing result includes `run.outcome: "succeeded"`, `run.exit_code: 0`,
`run.cleanup: "confirmed"`, `run.closed: true`, and `inventory.owned: 0`.
The verifier attempts cancellation on every non-terminal failure and checks
provider inventory before returning. Cleanup uses an independent deadline and
retries Run cancellation, reconciliation, orphan reclamation, evidence capture,
and inventory inspection until the Run is closed and inventory is empty. A
cleanup failure is reported separately so the primary scenario failure remains
available.

To exercise cancellation instead of natural exit, add this field to the same
trial document:

```json
{"mode":"launch-cancel"}
```

A passing cancellation trial has `run.outcome: "cancelled"`, confirmed cleanup,
and zero owned inventory. Evidence for either mode includes the placement
decision, public events, and scenario timing.
