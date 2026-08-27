# Authentication

Mercator authenticates access to one deployment:

- Machine clients present the static bearer token in `MERCATOR_API_TOKEN`.
- Humans use a signed browser session established through OIDC, or `--dev` on a
  loopback source-development server.
- Workloads report through a separate token minted for exactly one Run from
  `MERCATOR_SECRET_KEY`.

There is no tenant or Workspace scope inside Mercator. Product tenancy belongs
to the application dispatching the Run. Deploy a separate broker, database,
token, and key set when an execution scope needs hard isolation.

Human mutations record the signed-in email in the event envelope. Machine-token
mutations record `bearer`. Actor identities appear only in authenticated record
reads, never public event payloads delivered to sinks.

## Bearer Token

```sh
export MERCATOR_API_TOKEN="$(openssl rand -hex 32)"
./bin/mercator serve
```

Requests to `/v1/*` must include `Authorization: Bearer <token>`. The CLI reads
the same token:

```sh
MERCATOR_API_URL=http://127.0.0.1:8080 \
MERCATOR_API_TOKEN="$MERCATOR_API_TOKEN" \
./bin/mercator run list
```

## Browser Login

Source development can establish the local operator session directly:

```sh
./bin/mercator serve --dev
```

`--dev` requires a loopback `MERCATOR_ADDR` and a valid
`MERCATOR_SECRET_KEY`. It establishes an HTTP-only SameSite=Lax session for
`developer@localhost`; it cannot be combined with OIDC.

For production OIDC, set the complete configuration. A partial configuration
refuses startup:

```sh
export MERCATOR_OIDC_ISSUER='https://accounts.google.com'
export MERCATOR_OIDC_CLIENT_ID='...'
export MERCATOR_OIDC_CLIENT_SECRET='...'
export MERCATOR_OIDC_ALLOWED_DOMAIN='example.com'
export MERCATOR_SESSION_KEY="$(openssl rand -hex 32)"
export MERCATOR_PUBLIC_URL='https://mercator.example.com'
./bin/mercator serve
```

`MERCATOR_OIDC_ALLOWED_EMAILS` may be used with or instead of the domain
allowlist. Browser sessions last 24 hours. `mercator login` uses the same OIDC
flow to mint a 30-day CLI token tied to the human email. A presented but invalid
bearer token fails; Mercator does not silently fall back to a valid cookie.

## Administrative Listener

Node invitation and forced sink delivery change the deployment itself. When
`MERCATOR_ADMIN_ADDR` is configured, these operations answer only there:

- `POST /v1/nodes`
- `POST /v1/sinks/{id}/deliver`
- `POST /v1/sinks/{id}/replay`

The public listener returns the same `404` it returns for an unknown path.
`MERCATOR_ADMIN_ADDR` is required when the public address is reachable beyond
the host, must name a concrete private interface, and shares the server's TLS
and shutdown lifecycle. A loopback development server may serve both surfaces
on one listener.

## Quick Check

```sh
curl -fsS \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  "$MERCATOR_API_URL/v1/runs" | jq .

curl -i \
  -H "Authorization: Bearer wrong" \
  "$MERCATOR_API_URL/v1/runs"
```

The first request returns the deployment's Runs. The second returns `401
UNAUTHORIZED`.
