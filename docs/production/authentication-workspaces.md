# Authentication And Workspaces

Mercator authenticates two kinds of principals at the HTTP boundary:

- **Machine clients** present the static bearer token (`MERCATOR_API_TOKEN`).
  Workloads reporting exit codes use the separate per-run signed token minted
  under `MERCATOR_SECRET_KEY`.
- **Humans** sign in to the console through OIDC when a deployment configures
  it. Without OIDC config there is no human login surface and everything
  behaves exactly as a token-only deployment.

Human-initiated mutations (run create/cancel, connection create/authorize)
record the acting principal in the event log envelope; run and connection
records surface it as `created_by` / `cancelled_by` / `authorized_by`. The
principal is `"bearer"` for machine-token calls and the signed-in email for
sessions. Actor identities never appear in public event payloads (which flow
to sinks), only in authenticated API record reads.

## Configure The Bearer Token

```sh
export MERCATOR_API_TOKEN="$(openssl rand -hex 32)"
export MERCATOR_AUTH_WORKSPACES='ws_eval,ws_ci'
./bin/mercator serve
```

Requests to `/v1/*` must include:

```sh
Authorization: Bearer <MERCATOR_API_TOKEN>
```

The CLI adds the header when `MERCATOR_API_TOKEN` is set:

```sh
MERCATOR_API_URL=http://127.0.0.1:8080 \
MERCATOR_API_TOKEN="$MERCATOR_API_TOKEN" \
./bin/mercator run list --workspace-id ws_eval
```

## Configure OIDC Login (Optional)

Any spec-compliant OIDC issuer works; Google is one common choice. Register an
authorization-code client with redirect URI `<public URL>/auth/callback`, then
set the full fail-closed environment — a partial config refuses to boot:

```sh
export MERCATOR_OIDC_ISSUER='https://accounts.google.com'
export MERCATOR_OIDC_CLIENT_ID='...'
export MERCATOR_OIDC_CLIENT_SECRET='...'
# Allowlist: either or both. Comma-separated.
export MERCATOR_OIDC_ALLOWED_DOMAIN='example.com'
export MERCATOR_OIDC_ALLOWED_EMAILS='contractor@partner.dev'
# Signs the session cookie. 32+ random bytes, hex or base64.
export MERCATOR_SESSION_KEY="$(openssl rand -hex 32)"
# Externally reachable base URL; also used by run reporting.
export MERCATOR_PUBLIC_URL='https://mercator.example.com'
./bin/mercator serve
```

Behavior with OIDC enabled:

- `GET /auth/login` starts the flow; `GET /auth/callback` validates the ID
  token (signature, nonce, verified email) and checks the allowlist;
  `POST /auth/logout` clears the session.
- The session is a signed, HTTP-only, SameSite=Lax cookie valid for 24 hours.
  It is marked Secure automatically when the request arrived over TLS — either
  terminated locally or at a proxy that sets `X-Forwarded-Proto` (kamal-proxy
  does).
- Unauthenticated browser loads of the console redirect into `/auth/login`.
- `/v1/*` requests accept the session cookie as an alternative to the bearer
  token, carrying the same workspace grants. A wrong bearer token still fails
  even if a valid session cookie accompanies it.
- `GET /auth/session` reports `{"enabled": ..., "email": ...}` so clients can
  discover whether login is available and who is signed in.

The static bearer token keeps working unchanged for CI and API clients.

CLI users sign in with `mercator login` (see
[../reference/cli.md](../reference/cli.md)): the server hands the CLI a
single-use code on a localhost redirect after the same OIDC + allowlist checks,
and the CLI exchanges it at `POST /auth/cli/exchange` for a 30-day signed
bearer token tied to the user's email. The API gate accepts that token
wherever the static token is accepted, and mutations are audited under the
email. CLI tokens are stateless (like sessions): logout clears the stored
credential, and expiry bounds the remaining lifetime of a copied token.

## Workspace Rules

- If `MERCATOR_AUTH_WORKSPACES` is unset or empty, the principal is allowed for
  all workspaces through `*`.
- If it is set, use a comma-separated allow list such as `ws_eval,ws_ci`.
- Run, workload, secret, connection, and offer requests require an explicit
  `workspace_id` in the query or request body where the route expects one.
- A request outside the allow list returns `FORBIDDEN`.

## Quick Checks

```sh
curl -fsS \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  "$MERCATOR_API_URL/v1/runs?workspace_id=ws_eval" | jq .

curl -i \
  -H "Authorization: Bearer wrong" \
  "$MERCATOR_API_URL/v1/runs?workspace_id=ws_eval"

curl -i \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  "$MERCATOR_API_URL/v1/runs?workspace_id=not_allowed"
```

Expected results:

- Valid token plus allowed workspace returns JSON.
- Wrong token returns `401` with code `UNAUTHORIZED`.
- Valid token plus disallowed workspace returns `403` with code `FORBIDDEN`.

## Current Limitations

- There is one configured bearer token for machine clients; OIDC sessions add
  per-user identity for humans but no roles or per-user workspace grants —
  every authenticated principal carries the same `MERCATOR_AUTH_WORKSPACES`
  allow-list.
- Recorded principals are an audit trail, not an authorization system.
- Health, OpenAPI, and (without OIDC) the embedded UI shell are public on the
  listening interface; do not bind Mercator directly to an untrusted network.
