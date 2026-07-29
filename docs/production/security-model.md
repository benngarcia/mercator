# Security Model

This document describes the current V1 security boundary as implemented for
evaluation. It is not a GA security assurance statement.

## Trust Boundaries

- Mercator is a single trusted process.
- SQLite is the internal source of truth.
- Mercator terminates TLS in its own process when `MERCATOR_TLS_CERT_FILE` and
  `MERCATOR_TLS_KEY_FILE` are set. There is no plaintext fallback: see
  [Transport Security](#transport-security).
- `/v1/*` API routes are protected by a configured bearer token, or — when a
  deployment configures OIDC — a signed HTTP-only session cookie carrying a
  logged-in human identity. Sessions are HMAC-signed under
  `MERCATOR_SESSION_KEY`; OIDC config is fail-closed (partial config refuses
  to boot).
- Every authenticated principal administers the instance. Workspace ids scope
  stored records and queries; per-user identity is recorded for audit, not
  authorization.
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

Three absences stop startup rather than degrading the listener:

| Situation | Result |
| --- | --- |
| A configured certificate or key file cannot be read or parsed | Startup fails naming the file |
| One of the two variables is set and the other is not | Startup fails naming the missing variable |
| `MERCATOR_ADDR` is not a loopback address and no TLS material is configured | Startup fails naming both variables |

The last rule replaces a warning that was logged before serving plaintext
anyway. A warning in a startup log is not a security control.

Terminating TLS in front of Mercator with a proxy is still supported. Bind
`MERCATOR_ADDR` to loopback and point the proxy at it; a loopback listener may
serve plaintext because nothing off the host can reach it.

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

Rotate it with `mercator rekey`, with the server stopped:

```sh
export MERCATOR_SQLITE_DSN='file:/data/mercator.db'
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

Rotation covers stored connection credentials. Report tokens and node
enrollment tokens are short-lived and are simply re-signed under the new key as
they are minted; sessions and node tokens issued under the retired key stop
verifying at the restart.

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
- There is one machine bearer token; OIDC sessions add per-user identity for
  humans but no roles or per-user workspace grants.
- Secret management is delegated to the workload/runtime. Mercator has no
  secret vault, grant API, or KMS adapter surface.
- Registry-backed tag resolution is not implemented. Docker connections can
  authenticate digest-pinned private-image pulls with a pull-only connection
  credential; other adapters retain their documented registry boundaries.
- External Kafka/Postgres sink auth/config is not wired through the executable.
