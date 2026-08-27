# CLI Reference

The `mercator` CLI addresses one broker deployment. Set the API URL and one
credential; commands do not carry a tenant selector.

```sh
export MERCATOR_API_URL='https://mercator.example.com'
export MERCATOR_API_TOKEN='...'
mercator run list
```

`MERCATOR_API_TOKEN` is the deployment's machine credential. Human operators
may instead run `mercator login`; the OIDC flow stores a 30-day CLI token in the
local config and audits mutations under the signed-in email. See
[authentication.md](../production/authentication.md).

## Contexts

Contexts store an API URL and credential, never deployment data:

```sh
mercator context set staging --api-url https://staging.example.com --token "$TOKEN"
mercator context use staging
mercator context list
mercator context delete staging
```

Explicit environment values override the selected context. `--api-url` may be
passed before or after the command. Configuration is fail-closed: no hidden
localhost URL or token is invented.

## Runs

```sh
mercator run create busybox:1.37 -- echo hello
mercator run create --file workload.json
mercator run list
mercator run get --run-id run_...
mercator run wait --run-id run_...
mercator run events --run-id run_...
mercator run decision --run-id run_...
mercator run refresh --run-id run_...
mercator run cancel --run-id run_...
```

`run create` accepts `--run-id` and `--idempotency-key`. When a run ID is
provided and the idempotency key is omitted, the CLI derives a stable create key
from that ID. A generated run ID gets a fresh key.

## Connections

```sh
mercator connection create \
  --connection-id conn_runpod \
  --adapter-type runpod \
  --secret "$RUNPOD_API_KEY"

mercator connection authorize --connection-id conn_runpod
mercator connection list
mercator connection delete --connection-id conn_runpod
```

Connections are deployment-global and their IDs are unique. Adapter manifests
describe required config and credential fields through `mercator adapter list`.

## Other Surfaces

```sh
mercator offer list
mercator node list
mercator sink list
mercator verify --connection-id conn_runpod
mercator lab run path/to/scenario.json
```

Node invitation and forced sink delivery are administrative operations. Point
the CLI at `MERCATOR_ADMIN_ADDR` when the deployment separates its public and
administrative listeners.

## Output And Errors

Successful commands write JSON to stdout. Errors write a structured JSON object
to stderr and return nonzero. Required identity, token, URL, and configuration
values fail immediately; the CLI does not guess an old default.
