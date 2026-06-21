# Secrets And Grants

Mercator V1 has an encrypted event-backed secret vault and run-scoped grants.
Public APIs return metadata and grants, not plaintext secret values.

## Configure A Stable Secret Key

```sh
export MERCATOR_SECRET_KEY_HEX="$(openssl rand -hex 32)"
```

The key must decode to exactly 32 bytes. If it is omitted, the process generates
an ephemeral key; that is suitable only for disposable evaluation.

## Create A Secret Version

```sh
curl -fsS -X POST "$MERCATOR_API_URL/v1/secrets/sec_api_token/versions" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H "Idempotency-Key: idem-secret-sec-api-token-v1" \
  -H "Content-Type: application/json" \
  -d '{"workspace_id":"ws_eval","value":"replace-with-eval-secret"}' | jq .
```

Expected response:

```json
{"secret_id":"sec_api_token","version":1}
```

## List Secret Metadata

```sh
curl -fsS \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  "$MERCATOR_API_URL/v1/secrets?workspace_id=ws_eval" | jq .
```

The response contains secret IDs and versions, not plaintext.

## Grant A Secret To A Run

Before creating a run whose workload references a secret, create a run-scoped
grant for that exact run ID:

```sh
curl -fsS -X POST "$MERCATOR_API_URL/v1/secrets/sec_api_token/grants" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"workspace_id":"ws_eval","version":1,"scope_type":"run","scope_id":"run_secret_1"}' | jq .
```

Workload env binding shape:

```json
{
  "API_TOKEN": {
    "secret_ref": {"name": "sec_api_token", "version": 1}
  }
}
```

If a run references a secret without an active grant, the API returns
`SECRET_GRANT_REQUIRED`.

## Redaction Checks

```sh
go run ./cmd/mercator run events --workspace-id ws_eval --run-id run_secret_1 \
  | jq -e '.events | tostring | contains("replace-with-eval-secret") | not'
```

Also inspect API responses and UI views for absence of plaintext. The current
test suite includes redaction checks, but operators should repeat a live check
when changing launch or sink behavior.

## Current Docker Limitation

Secret references are authorized and carried as descriptors. The Docker adapter
does not yet materialize secret values at launch; a Docker workload with
`secret_ref` env bindings fails launch with a secret materialization error. Use
literal non-sensitive env values for Docker adapter evaluation.
