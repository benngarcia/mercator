# Security Model

This document describes the current V1 security boundary as implemented for
evaluation. It is not a GA security assurance statement.

## Trust Boundaries

- Mercator is a single trusted process.
- SQLite is the internal source of truth.
- `mercator serve` terminates TLS in its own process when
  `MERCATOR_TLS_CERT_FILE` and `MERCATOR_TLS_KEY_FILE` are set, and has no
  plaintext fallback off loopback. `mercator verify` is a second server on the
  same code and does not read those variables, so a remote provider trial serves
  in cleartext on the address it is given: see
  [Transport Security](#transport-security).
- `/v1/*` API routes are protected by a configured bearer token, or — when a
  deployment configures OIDC — a signed HTTP-only session cookie carrying a
  logged-in human identity. Sessions are HMAC-signed under
  `MERCATOR_SESSION_KEY`; OIDC config is fail-closed (partial config refuses
  to boot).
- A workspace is the tenancy boundary. The machine bearer token is the instance
  credential and reaches every workspace; a human reaches only the workspaces
  they are a member of, refused everywhere else by the one chokepoint every
  workspace-scoped route resolves its workspace through. The two node routes
  refuse a non-member with `400 WORKSPACE_FORBIDDEN` rather than `403`, because
  neither declares `403` in the API contract
  ([#222](https://github.com/benngarcia/mercator/issues/222)). `serve --dev`
  authenticates one human who is the deployment's own operator, and that subject
  is not scoped to memberships.
- Creating a tenant, inviting a machine, and forcing a sink to deliver answer on
  the administrative listener named by `MERCATOR_ADMIN_ADDR` and are not routed
  on the public one. That variable is required whenever this deployment is
  reachable beyond this host: a non-loopback `MERCATOR_ADDR`, or a
  `MERCATOR_PUBLIC_URL` naming anything but this machine, which is the reverse
  proxy topology.
- Health, OpenAPI, and (without OIDC) the UI shell are public on the listen
  interface. With OIDC configured, unauthenticated console loads redirect to
  the login flow.
- Runtime adapters are trusted in-process code.
- Docker runs on the local host and should be treated as part of the trusted
  evaluation environment.

## Environment Non-Observability

Implemented protections:

- Public event APIs skip private events and expose public CloudEvents only.
- Workload env literal values are redacted from public run events. The
  redaction covers env **values** only: image references and container args are
  recorded verbatim in public events, so do not put secrets in them.
- `secret_ref` env bindings are rejected; Mercator does not own secret storage,
  grants, KMS integration, or runtime secret materialization.
- Sink delivery skips private events.
- An optional Docker registry pull token is resolved as connection credential
  material. It is written only to an operation-owned mode-0600 Docker config,
  removed from the Docker subprocess environment, never passed in Docker argv
  or workload env, and removed after the container create command.

Operator checks:

```sh
go run ./cmd/mercator run events --workspace-id ws_eval --run-id run_secret_1 \
  | jq '.events'
```

## Transport Security

Mercator serves HTTPS from its own listener. Set both files and it terminates
TLS itself; a reverse proxy is an option rather than a requirement.

```sh
export MERCATOR_ADDR=0.0.0.0:8443
export MERCATOR_TLS_CERT_FILE=/etc/mercator/tls.crt   # PEM certificate chain
export MERCATOR_TLS_KEY_FILE=/etc/mercator/tls.key    # PEM private key
```

The listener floors at TLS 1.2 and offers `h2` ahead of `http/1.1` through
ALPN, so an HTTP/2 client gets HTTP/2. A modern client negotiates TLS 1.3.

Four absences stop startup rather than degrading the listener:

| Situation | Result |
| --- | --- |
| A configured certificate or key file cannot be read or parsed | Startup fails naming the file |
| One of the two variables is set and the other is not | Startup fails naming the missing variable |
| `MERCATOR_ADDR` is not a loopback address and no TLS material is configured | Startup fails naming both variables |
| This deployment is reachable beyond this host and `MERCATOR_ADMIN_ADDR` is unset | Startup fails naming which fact made it reachable |

The last rule replaces a warning that was logged before serving plaintext
anyway. A warning in a startup log is not a security control.

Terminating TLS in front of Mercator with a proxy is still supported. Bind
`MERCATOR_ADDR` to loopback and point the proxy at it; a loopback listener may
serve plaintext because nothing off the host can reach it. That topology still
needs `MERCATOR_ADMIN_ADDR`, because the proxy makes the loopback bind reachable
from the internet and the administrative routes must not be among what it
forwards. Startup refuses a deployment that announces `MERCATOR_PUBLIC_URL` and
names no administrative address, and refuses a `MERCATOR_PUBLIC_URL` that is not
an absolute `http://` or `https://` URL naming a host. A schemeless value would
otherwise announce nothing and exempt the very topology this rule exists for.

The three rules above are `mercator serve`'s. They are enforced in the process
entrypoint against `MERCATOR_ADDR`, not in the server itself, and `mercator
verify` is the other caller that builds the same server. A provider Conformance
Trial binds `MERCATOR_CONFORMANCE_LISTEN_ADDR`, which must be a fixed routable
address for any remote adapter, reads neither TLS variable, and therefore serves
the whole `/v1` API and its generated operator token in cleartext for the
duration of the trial. Put a TLS terminator in front of it and keep the address
private, as [provider-conformance.md](provider-conformance.md) says. Moving the
rule into the server, where it would cover both callers, needs the trial to be
able to carry a certificate of its own:
[#216](https://github.com/benngarcia/mercator/issues/216).

Not covered: certificate reload without a restart, ACME or any automatic
issuance, client certificates, and an HTTP listener that redirects to HTTPS.
See [known-limitations.md](known-limitations.md).

## Master Key And Rotation

`MERCATOR_SECRET_KEY` is required. `serve` refuses to start without it, because
three separate subkeys are derived from it and each derivation answers an absent
master key by disabling itself:

| Subkey | Purpose | Behaviour with no master key |
| --- | --- | --- |
| HKDF-SHA256, `mercator/credential-seal/v1` | AES-256-GCM sealing of stored connection credentials | Storing a credential is refused; stored ones are unreadable |
| HMAC-SHA256, `mercator-report-token-v1` | Signing per-run workload report tokens | Reporting answers `501 REPORTING_DISABLED` |
| HMAC-SHA256, `mercator-node-identity-v1` | Signing node enrollment and session tokens | Node identity is unsigned |

The key is never generated for you. A generated key would change on every
restart and orphan every credential sealed under the previous one, so an absent
key is a startup failure naming the variable.

Rotate it with `mercator rekey`. Two ways of rotating the wrong thing are
refusals rather than advice: the command will not run while another process
holds the database, and it will not run against a database it would have to
create. What it cannot see is a rotation against some other database that does
exist, so the DSN below has to be the one the server runs with, and the value
here is the one [install-configuration.md](install-configuration.md) prescribes.

```sh
export MERCATOR_SQLITE_DSN='file:/var/lib/mercator/mercator.db'
export MERCATOR_SECRET_KEY="$(openssl rand -hex 32)"     # the new key
export MERCATOR_SECRET_KEY_PREVIOUS='<the retired key>'
mercator rekey
# re-sealed 3 credential(s) under the new MERCATOR_SECRET_KEY; remove MERCATOR_SECRET_KEY_PREVIOUS from the environment
```

Every stored credential is re-sealed in one transaction, so a rotation that
fails part way leaves every row readable under the key it was written with. Rows
already sealed under the new key are left alone, so an interrupted rotation is
re-run rather than reasoned about. A row neither key opens names the connection
it belongs to and aborts the rotation.

Rotation is an operator command rather than a startup step on purpose. Rotating
at boot would require the retired key to stay in the deployment's environment
forever, which is the opposite of retiring it. Remove
`MERCATOR_SECRET_KEY_PREVIOUS` once the command succeeds, and restart the server
with the new `MERCATOR_SECRET_KEY` only.

### Let The Runs Finish First

Rotation re-seals stored connection credentials. Nothing else that was signed
under the retired key is carried across, and one of those things is not
short-lived.

A workload's report token is minted once, when its attempt is dispatched, and is
never re-issued and never expires. It is an HMAC over the workspace and run id
under a subkey of the master key, handed to the container as
`MERCATOR_RUN_TOKEN`, and a six-hour training run holds the one it was given for
its whole life. Rotate underneath it and every report it sends afterwards is
answered `401 INVALID_RUN_TOKEN`, permanently: progress, checkpoints, and its own
terminal verdict. The workload application owns semantic success in this
architecture, so that run is adjudicated without the one report that would have
said whether it worked.

So the rotation procedure is: stop admitting work, let in-flight runs reach a
terminal phase, then stop the server and rotate. Mercator has no drain command;
`mercator run list` is how you see what is still running.

Browser sessions are signed under `MERCATOR_SESSION_KEY`, which is separate
material, so a logged-in human is unaffected by a master-key rotation.

Report-token expiry and re-issue is
[#215](https://github.com/benngarcia/mercator/issues/215).

### Retire The Nodes Too

A node's two credentials are both short-lived, and neither survives a rotation.
An enrolled node does not come back on its own.

Both are HMACs under the node subkey of the master key: the session token it
authenticates with, valid 30 minutes from enrollment, and the enrollment token
it was bootstrapped with, valid 30 minutes from the moment the identity was
invited. Nothing re-signs an issued one, and the enrollment token is the only
input to the one route that mints a session. So after the restart the node's
heartbeats, events, and results are all refused; the agent goes on presenting
the credential it holds, which a refusal does not replace; and the enrollment it
eventually falls back to is refused as well, because that token was signed under
the retired key too and its own window closed 30 minutes after the identity was
invited. The lease expires, the control plane records the node lost, the rental
keeps billing, and its workloads are orphaned.

Recovery is an operator action rather than the node's next attempt. Inviting the
same `node_id` again is refused because the identity already exists, and that
refusal arrives today as a `500` naming a unique-constraint failure, so the
machine needs a fresh identity and a fresh agent state file. The procedure is
therefore to drain enrolled nodes and retire their identities before rotating,
and to bootstrap them again afterwards.

Carrying node credentials across a rotation is
[#217](https://github.com/benngarcia/mercator/issues/217), and the missing
operator route back is
[#211](https://github.com/benngarcia/mercator/issues/211).

## Idempotency And Side Effects

- Mutation routes require `Idempotency-Key` where implemented.
- Reusing a command key with a different request hash returns
  `IDEMPOTENCY_CONFLICT`.
- Launch and cleanup side effects are preceded by durable intent events.
- Docker launch uses deterministic container names and ownership labels.

## Current Risks

- Mercator terminates TLS itself but does not manage certificates. Issuance,
  renewal, and reload are the operator's, and a renewed certificate is picked up
  only by restarting the process.
- When `MERCATOR_API_TOKEN` is unset, `serve` logs a generated token to stdout;
  operators shipping logs should set the variable explicitly.
- There is one machine bearer token, and it is the instance credential: it
  reaches every workspace. A human reaches only the workspaces they are a member
  of, and the only way to become one over HTTP is to create the workspace
  ([#219](https://github.com/benngarcia/mercator/issues/219)). Membership rows
  carry a role that nothing yet checks.
- Secret management is delegated to the workload/runtime. Mercator has no
  secret vault, grant API, or KMS adapter surface.
- Registry-backed tag resolution is not implemented. Docker connections can
  authenticate digest-pinned private-image pulls with a pull-only connection
  credential; other adapters retain their documented registry boundaries.
- External Kafka/Postgres sink auth/config is not wired through the executable.
