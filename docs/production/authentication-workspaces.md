# Authentication And Workspaces

Mercator authenticates two kinds of principals at the HTTP boundary:

- **Machine clients** present the static bearer token (`MERCATOR_API_TOKEN`).
  Workloads reporting exit codes use the separate per-run signed token minted
  under `MERCATOR_SECRET_KEY`.
- **Humans** use a signed browser session. Production deployments establish it
  through OIDC; loopback source development can establish it with `--dev`.
  Without either mode, the console falls back to the machine bearer token.

Human-initiated mutations (run create/cancel, connection create/authorize)
record the acting principal in the event log envelope; run and connection
records surface it as `created_by` / `cancelled_by` / `authorized_by`. The
principal is `"bearer"` for machine-token calls and the signed-in email for
sessions. Actor identities never appear in public event payloads (which flow
to sinks), only in authenticated API record reads.

## Configure The Bearer Token

```sh
export MERCATOR_API_TOKEN="$(openssl rand -hex 32)"
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

## Local Browser Login

Source development can remove browser token handling entirely:

```sh
./bin/mercator serve --dev
```

`--dev` refuses to start unless `MERCATOR_ADDR` is loopback and
`MERCATOR_SECRET_KEY` is set, creates an
ephemeral signing key, and establishes an HTTP-only SameSite=Lax session for
`developer@localhost`. Browser mutations record that identity. The operator
bearer token remains available for the CLI and automation, and `--dev` cannot
be combined with OIDC configuration.

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
  token, scoped to the signed-in subject's workspace memberships rather than to
  the whole instance. A wrong bearer token still fails even if a valid session
  cookie accompanies it.
- `GET /auth/session` reports the `oidc`, `local`, or `token` mode plus the
  current email when the mode has a browser identity.

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

- Run, workload, secret, connection, and offer requests require an explicit
  `workspace_id` in the query or request body where the route expects one.
- Workspaces are saved SQLite records with stable IDs and display names. Create
  and select one through the authenticated API or console before creating
  workspace-owned records. Unknown workspace IDs fail with
  `WORKSPACE_NOT_FOUND`.
- Archiving removes a workspace from the default chooser and rejects new runs,
  connections, workloads, and workload revisions. Existing lifecycle commands
  remain available so operators can converge and clean up archived workspaces.
- A workspace is the tenancy boundary. A human subject reaches exactly the
  workspaces they are a member of, and reaches nothing in any other: every
  workspace-scoped route answers `403 WORKSPACE_FORBIDDEN` to a subject with no
  membership, `GET /v1/workspaces` lists only their own, and archiving a
  workspace they cannot see answers `404 WORKSPACE_NOT_FOUND` rather than
  confirming it exists.
- The machine bearer token is the instance credential. It is the deployment
  acting as itself, so it is not scoped to a workspace and reaches all of them.
  CI, the CLI with `MERCATOR_API_TOKEN`, and internal automation keep working
  exactly as before.
- Creating a workspace makes the creator its admin, in the same transaction
  that creates it. A workspace created with the bearer token is administered by
  `bearer`, which no human holds; create workspaces as the human who will own
  them, using a `mercator login` CLI token or the console.
- Memberships carry a role, `admin` or `member`. The role is recorded and is
  not yet what any single operation checks: membership is
  ([#219](https://github.com/benngarcia/mercator/issues/219)).
- Connections are created and authorized through `/v1/connections`. Server
  startup never creates or places a connection from environment variables.

## Administrative Surfaces

Some operations change what the deployment is rather than what a workspace
contains. They answer on an administrative listener and are not routed on the
public one at all, where they answer `404` exactly as a path this deployment
does not have would:

- `POST /v1/workspaces` and `POST /v1/workspaces/{id}/archive`
- `POST /v1/nodes` (node invitation)
- `POST /v1/sinks/{id}/deliver` and `POST /v1/sinks/{id}/replay`

The reads beside them stay public: listing workspaces, listing nodes, and
reading sink status are what a console renders from.

```sh
export MERCATOR_ADDR='0.0.0.0:8443'
export MERCATOR_ADMIN_ADDR='127.0.0.1:8081'
./bin/mercator serve
```

`MERCATOR_ADMIN_ADDR` is required whenever `MERCATOR_ADDR` is not loopback, and
there is no permissive default: a public deployment that has not named one
refuses to start. It must name one interface rather than the wildcard, because
an administrative surface reachable on every interface is not a private one, and
because the accepting listener's local address is what tells the two apart. Both
listeners are served by one server with one certificate and one shutdown, so an
administrative listener on a routable address is served over TLS like the public
one.

A loopback deployment that sets nothing serves every route on its single
listener, which is what source development and `--dev` want.

## Quick Checks

```sh
curl -fsS \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  "$MERCATOR_API_URL/v1/runs?workspace_id=ws_eval" | jq .

curl -i \
  -H "Authorization: Bearer wrong" \
  "$MERCATOR_API_URL/v1/runs?workspace_id=ws_eval"

curl -i -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  "$MERCATOR_API_URL/v1/runs"
```

Expected results:

- Valid token plus an explicit workspace returns JSON.
- Wrong token returns `401` with code `UNAUTHORIZED`.
- Missing `workspace_id` returns `400` with code `WORKSPACE_ID_REQUIRED`.

## Current Limitations

- There is one configured bearer token for machine clients, and it is not
  scoped to a workspace.
- No route grants a membership. A subject becomes a member by creating a
  workspace and in no other way over HTTP, because a grant endpoint is a new
  operation in the API contract
  ([#219](https://github.com/benngarcia/mercator/issues/219)). Until it lands,
  adding a second person to a workspace is a SQL statement against the database:

  ```sql
  INSERT INTO workspace_members (workspace_id, subject, role, granted_at)
  VALUES ('ws_...', 'person@example.com', 'member', '2026-07-28T00:00:00Z')
  ON CONFLICT(workspace_id, subject) DO UPDATE SET role = excluded.role;
  ```

- Upgrading an existing deployment backfills one admin per workspace from
  `workspaces.created_by`. A workspace whose creator was `bearer`,
  `system:bootstrap`, or `system:migration` has a machine principal as its only
  admin, so no human is a member of it and every human is refused there until
  somebody runs the statement above.
- Health, OpenAPI, and (without OIDC) the embedded UI shell are public on the
  listening interface; do not bind Mercator directly to an untrusted network.
- The console creates and archives workspaces through the two administrative
  routes, so on a deployment with an administrative listener those two console
  actions answer `404`. Everything else the console does is on the public
  listener ([#220](https://github.com/benngarcia/mercator/issues/220)).
