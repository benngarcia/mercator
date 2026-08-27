# OpenAPI Reference

The generated contract is served at `GET /openapi.json`. Every resource is
deployment-global; no route or schema contains a Workspace selector.

Authenticated `/v1/*` requests present either the deployment bearer token, an
OIDC browser session, or a human CLI token:

```http
Authorization: Bearer <MERCATOR_API_TOKEN>
```

Run reporting is the exception: `POST /v1/runs/{run_id}/report` accepts only
the token minted for that Run.

## Main Resources

| Resource | Operations |
|---|---|
| Runs | create, list, get, wait, events, decision, refresh, cancel, report |
| Connections and Offers | list adapter manifests, create/authorize/delete Connections, list Offers |
| Workloads | create immutable workload definitions and revisions |
| Fleet | list and invite Nodes; inspect Rentals and schedules |
| Sinks | inspect delivery state; force deliver or replay on the admin listener |
| Console | deployment event stream and embedded application shell |

IDs and idempotency keys are unique within the deployment. Two callers that
need independent identity namespaces or provider fleets must use separate
Mercator deployments.

## Create And Inspect A Run

```sh
RUN_ID="$(curl -fsS -X POST "$MERCATOR_API_URL/v1/runs" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: example-create-1' \
  -d '{"image":"busybox:1.37","args":["echo","hello"]}' \
  | jq -r '.run.id')"

curl -fsS "$MERCATOR_API_URL/v1/runs/$RUN_ID" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" | jq .

curl -fsS "$MERCATOR_API_URL/v1/runs/$RUN_ID/events" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" | jq .
```

The checked-in OpenAPI document and generated Go client are contract-tested;
changes regenerate both and fail if the removed Workspace abstraction returns.
