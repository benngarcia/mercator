# Security Model

Mercator is a single-deployment compute control plane. It authenticates
operators, stores provider credentials, dispatches workload specifications,
mints narrow runtime credentials, and records lifecycle events. It is not a
product-tenancy boundary.

## Trust Boundaries

- `MERCATOR_API_TOKEN` authenticates machine clients to the whole deployment.
- OIDC browser sessions and CLI tokens authenticate an allowed human to the
  whole deployment and record that email on mutations.
- `MERCATOR_ADMIN_ADDR` keeps node invitation and forced sink delivery off the
  public listener.
- `MERCATOR_RUN_TOKEN` authorizes one workload to report only for its Run.
- Node enrollment and session credentials authorize one enrolled Node
  generation to the node-agent protocol.
- Provider and registry credentials remain on the control-plane side. Nodes
  receive only operation- and content-bound material when a fetch requires it.

Product tenancy must be enforced before dispatch. Deploy a separate Mercator
process with its own database, keys, token, Connections, and object-store scope
when two execution domains must not share authority or mutable cache state.

## Workload Secrets

Mercator accepts literal workload environment values but redacts them from
public events. It does not implement a secret vault and rejects `secret_ref`
bindings. Prefer passing non-secret configuration that lets the workload obtain
its own secret from the application's chosen backend.

Before sharing run evidence, inspect the public event stream:

```sh
mercator run events --run-id run_secret_1 \
  | jq '.. | objects | select(has("env") or has("environment"))'
```

Provider API keys may be supplied through the environment or Mercator's sealed
Connection credential store. Stored credentials are encrypted with an
HKDF-derived subkey from `MERCATOR_SECRET_KEY`; the raw master key is not used
directly as an encryption key. Failed provider responses remain private process
logs and are redacted before logging.

## Runtime Credentials

Run reporting tokens are HMACs over the deployment-global Run ID. They never
appear in public events and are rejected on every other Run. A token has no
expiry or re-issue path, so rotating `MERCATOR_SECRET_KEY` while a workload is
running permanently ends that workload's ability to report.

Registry pulls and Artifact reads use credentials scoped to one operation, one
content identity, and an expiry. The node validates that scope before spending
the material. Signed URLs and registry passwords are not written to durable
events, operation failures, or command-line arguments.

Node invitations are one-time enrollment material. Redeeming one creates a
short-lived renewable session bound to the Node generation; replayed invitations
and stale fencing tokens are refused.

## Network Exposure

- Non-loopback serving requires TLS or a trusted TLS-terminating proxy.
- Health and OpenAPI routes are intentionally unauthenticated.
- `/v1/*` requires a valid operator credential except the Run reporting route,
  which verifies its Run token.
- A wrong bearer token fails outright even when a valid browser cookie is also
  present.
- Administrative operations return an indistinguishable `404` on the public
  listener.

## Key Rotation

Back up the SQLite database and keys before rotation. Let in-flight Runs finish,
then run `mercator rekey` with both the new and previous master keys configured.
The command is transactional: one credential that neither key opens aborts the
whole rewrite. After verification, remove the previous key.

## Operator Checklist

- Keep API, session, master, provider, and node credentials out of source control
  and process arguments.
- Restrict the administrative listener to a private interface.
- Back up the SQLite database together with every key required to open it.
- Review public events and evidence bundles for redaction before distribution.
- Verify cleanup confirmation; a closed Run must not leave owned compute behind.
- Treat a shared broker as shared execution authority and shared mutable cache.
