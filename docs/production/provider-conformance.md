# Provider Conformance Trials

`mercator verify` launches a real probe through the same authenticated HTTP,
placement, provider, reporting, and cleanup path used by the server. It creates
an isolated temporary SQLite database and
connection. A passing verdict means all of these conditions held:

1. the provider authorized the supplied credential;
2. a known-USD offer fit the declared maximum cost;
3. the provider launched the digest-pinned probe image;
4. the selected scenario reached its expected terminal outcome;
5. the Run closed with confirmed cleanup; and
6. the provider listed zero objects owned by the trial deployment.

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
cancelled and cleaned up through the same Run lifecycle, or to `capacity` to run
the machine-renting suite described below.

A `shadeform` trial in either launch mode validates and then finds no offers:
those modes launch through the ephemeral lane, and Shadeform sells capacity
rather than one-shot execution. `capacity` is the mode for it.

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
address. The verifier's own listener is plaintext and cannot be told otherwise:
it reads no TLS variables, and unlike `mercator serve` it does not refuse a
routable address without a certificate
([#216](https://github.com/benngarcia/mercator/issues/216)). What it serves
there is the full `/v1` API behind a bearer token it generates for the trial, so
put a terminator in front of it and keep the address private for the duration of
the trial. Per-Run bearer tokens authenticate probe reports.

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

## Capacity Trials

A capacity trial asks a different question. The launch modes ask whether a
workload runs; this asks whether a backend keeps the `CapacityProvider`
contract, which is what the reusable lane rests on. It rents machines, gives
them back, and launches nothing:

```json
{
  "adapter_type": "shadeform",
  "credential_env": "SHADEFORM_API_KEY",
  "config": {
    "agent_download_url": "https://downloads.example.com/mercator-node/{version}/linux-amd64",
    "allowed_clouds": "hyperstack",
    "max_lifetime_hours": "1"
  },
  "mode": "capacity",
  "max_expected_cost_usd": 2.0,
  "timeout": "20m"
}
```

The document names no `image`, and one that does is refused: a capacity trial
runs no workload, so an image on it is a promise nothing keeps. The trial rents
the cheapest listing available at a known USD rate whose cost over the whole
timeout stays inside `max_expected_cost_usd`, and asks the provider for a
reclamation backstop of the same length, so a trial that dies between renting
and returning cannot bill for ever.

The callback topology rules are the launch modes' rules, unchanged. A capacity
trial serves nothing itself, and every machine it rents is handed a bootstrap
naming `MERCATOR_CONFORMANCE_PUBLIC_URL`: a trial that wrote an origin nothing
serves onto a real machine could not tell a provider defect from a control plane
nobody could have reached.

The promises, and the Lab rule each one is the higher-fidelity half of:

| Promise | Lab rule |
| --- | --- |
| `listed_capacity_is_capacity_to_acquire` | `safety.capacity_lifecycle_is_negotiated` |
| `the_negotiated_set_is_one_a_provider_could_keep` | `safety.capacity_lifecycle_is_negotiated` |
| `a_credential_check_allocates_nothing` | `safety.capacity_lifecycle_is_negotiated` |
| `one_provision_command_produces_one_machine` | `safety.idempotent_external_commands` |
| `a_lost_answer_costs_no_second_machine` | `liveness.lost_response_reconciliation` |
| `terminate_is_confirmed_and_stays_confirmed` | `liveness.provisioned_capacity_enrolls_or_is_reclaimed` |
| `an_operation_the_provider_never_promised_is_refused` | `safety.capacity_lifecycle_is_negotiated` |
| `a_trial_leaves_nothing_owned` | `liveness.provisioned_capacity_enrolls_or_is_reclaimed` |

`safety.capacity_lifecycle_is_negotiated` is deliberately not registered in the
Lab: nothing in the tree stops or resumes a machine yet, so the rule could only
fail against a hand-written observation. Its production half is
`broker.Backend.CapacityFor`, which refuses an operation a connection never
claimed before any request is sent, and the suite is the provider's own side of
that refusal.

Each promise is reported `kept`, `broken`, or `out_of_reach`. Out of reach is
neither: a provider that deduplicates on an operation key is entitled to
enumerate nothing it owns, and calling the two owned-capacity promises green
against one would claim to have read a listing that does not exist.

A promise gives back every machine its Rental is known to hold, not the one
machine whose receipt it accepted. The second machine a non-idempotent provider
allocates for one command is destroyed by the same promise that reports it, so a
connection that enumerates nothing it owns still ends the trial holding nothing.
Each destruction is keyed by the machine as well as the lease it was taken out
under, because a key performs its effect exactly once: two machines sent under
one key are one destruction and one machine still billing. The trial's own sweep
keys the same way, which is where two machines wearing one Rental's tag turn up.

A provision answered with `ErrCapacityIndeterminate`, or accepted without naming
a machine at all, is asked about through every mechanism that provider
negotiated rather than the first one it happens to have. The trial reads what the
connection owns for that Rental, and a provider that deduplicates on the
operation key is then sent the same command again and answered with the machine
it already made. Both are asked because a listing that already named the machine
would have made the outcome slow rather than unknown: a provider reports a
provision indeterminate precisely when it cannot find what it just created, so
the repeat is what names that machine even on a connection whose listing exists.
When nothing names one, the promise is reported broken and says so, because a
machine nothing can address may still be billing and an operator has to hear
about it rather than read a clean return.

```sh
export SHADEFORM_API_KEY='sf_...'
export MERCATOR_CONFORMANCE_LISTEN_ADDR='0.0.0.0:8082'
export MERCATOR_CONFORMANCE_PUBLIC_URL='https://reports.example.com'

mercator verify --spec shadeform-capacity-trial.json | tee shadeform-capacity-evidence.json
```

What the suite deliberately does not establish:

- Whether an agent enrolled. A capacity trial waits for no machine to join, and
  the material it hands one is an enrolment token nothing minted, so a rented
  machine boots, is turned away, and is destroyed. Proving a machine enrols
  needs the whole control plane and is the launch path's job.
- How many API calls a provider took to keep a promise. The suite reads what the
  account holds, so a backend that creates a duplicate and then converges on one
  machine keeps `one_provision_command_produces_one_machine`. Counting requests
  is a fact about one API, and each adapter's own tests hold it.
- What a stop or a resume does to a running workload. The suite sends both to
  every provider that claims them and asserts only that a claimed operation is
  not refused and an unclaimed one is.

The same suite runs on every build without a credential or a network:
`internal/adapter/fake` runs it against the simulated provider the Blueprint
corpus is written against, and `internal/adapter/shadeform` runs it against that
package's in-memory marketplace served over `httptest`. A live Shadeform run is
gated on both `SHADEFORM_API_KEY` and `MERCATOR_SHADEFORM_LIVE=1`, because an
exported API key is not consent to rent a GPU inside `go test ./...`.
