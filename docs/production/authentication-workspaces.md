# Authentication And Workspaces

Mercator V1 uses one bearer token principal at the HTTP boundary. This is enough
for local evaluation and single-operator hardening, but it is not a full
multi-user identity system.

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

- There is one configured bearer token, not per-user credentials.
- Workspace authorization is an allow-list on that bearer principal.
- Health, OpenAPI, and the embedded UI shell are public on the listening
  interface; do not bind Mercator directly to an untrusted network.
