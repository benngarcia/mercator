# OpenAPI And HTTP Route Overview

Mercator serves its OpenAPI 3.0 contract at `/openapi.json`. The checked-in
`internal/httpapi/openapi.json` file generates the Go router and the console's
private transport types. Run `scripts/generate-api-contracts.sh` after editing
it; CI rejects stale generated code. Go handlers implement the generated strict
interface, so request decoding and status-specific response serialization stay
inside the generated transport seam.

```sh
curl -fsS "$MERCATOR_API_URL/openapi.json" | jq '.info'
curl -fsS "$MERCATOR_API_URL/openapi.json" | jq -r '.paths | keys[]'
```

Health, OpenAPI, and the UI shell are public on the configured listen
interface. The OpenAPI-listed `/v1/*` operator routes require bearer auth with
`Authorization: Bearer $MERCATOR_API_TOKEN`; workspace-scoped reads and writes
also require an explicit `workspace_id` query parameter or request field.

## Route Families

| Family | Routes | Use |
| --- | --- | --- |
| Health and discovery | `GET /health/live`, `GET /health/ready`, `GET /openapi.json`, `GET /` | Process liveness/readiness, OpenAPI discovery, and the embedded console shell. These are public on the listen interface. |
| Runs | `POST /v1/runs`, `GET /v1/runs`, `GET /v1/runs/{run_id}`, `GET /v1/runs/{run_id}/wait`, `POST /v1/runs/{run_id}/refresh`, `POST /v1/runs/{run_id}/cancel`, `POST /v1/runs/{run_id}/report`, `GET /v1/runs/{run_id}/events`, `GET /v1/runs/{run_id}/decision` | Create runs, list/read them, wait for closure, refresh/cancel adapter state, ingest workload reports, inspect public events, and read the placement decision. |
| Placement | `POST /v1/placements:preview` | Evaluate a workload against current offers and policy without creating a run. |
| Workloads and images | `POST /v1/workloads`, `GET/POST /v1/workloads/{workload_id}/revisions`, `GET /v1/workloads/{workload_id}/revisions/{revision_id}`, `POST /v1/images:resolve` | Store workload names/revisions, read immutable revisions, and resolve image tags to immutable image metadata. |
| Connections and offers | `GET /v1/adapters`, `GET /v1/connections`, `POST /v1/connections`, `POST /v1/connections/{id}/authorize`, `DELETE /v1/connections/{id}`, `GET /v1/offers` | Discover registered adapters' onboarding manifests (display metadata, config fields, setup steps), create/verify/delete provider connections, and inspect the offers visible to the placement engine for a workspace. |
| Sinks | `GET /v1/sinks/{sink_id}`, `POST /v1/sinks/{sink_id}/deliver`, `POST /v1/sinks/{sink_id}/replay` | Read sink cursor state, deliver pending events, and replay events after a global position. |

`GET /v1/offers` returns Broker-assigned snapshot IDs derived from the
Connection and the adapter-local capacity identity. The IDs are stable and
unique within a workspace, including when two Connections expose the same
provider catalog item.

`GET /v1/runs/{run_id}/decision` returns the latest recorded placement
decision. A stale-Offer replacement therefore supersedes the initial decision.
If every remaining Offer is infeasible, the latest decision has no
`selected_offer_snapshot_id`, records `NO_FEASIBLE_OFFERS`, and retains each
candidate rejection before the Run closes with `reason=RETRY_EXHAUSTED`.

The public `compute.run.launch_failed.v1` event exposes the provider-neutral
`code`, `retryable`, and `side_effect` fields. A replacement requires
`code=PROVIDER_CAPACITY_UNAVAILABLE`, `retryable=true`, and
`side_effect=none`. Other definitive failures close the Run, while an
indeterminate side effect keeps reconciliation on the original launch key.

## First Integrator Path

For the smallest HTTP integration, start with the Docker adapter and use the
image shorthand with a digest-pinned image (mutable tags are rejected with a
`400` at create time):

```sh
docker pull -q busybox:latest >/dev/null
IMAGE="$(docker inspect --format '{{index .RepoDigests 0}}' busybox:latest)"

RUN_ID="$(curl -fsS -X POST "$MERCATOR_API_URL/v1/runs?workspace_id=$MERCATOR_WORKSPACE_ID" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: idem-minimal-1' \
  -d "{\"image\":\"$IMAGE\",\"args\":[\"echo\",\"hi\"]}" | jq -r '.run_id')"

printf '%s\n' "$RUN_ID"
```

Then read the run, events, decision, and sink cursor:

```sh
curl -fsS "$MERCATOR_API_URL/v1/runs/$RUN_ID?workspace_id=$MERCATOR_WORKSPACE_ID" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  | jq '{id: .run.id, outcome: .run.outcome, exit_code: .run.exit_code, cleanup: .run.cleanup, closed: .run.closed}'

curl -fsS "$MERCATOR_API_URL/v1/runs/$RUN_ID/events?workspace_id=$MERCATOR_WORKSPACE_ID" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  | jq '.events[] | .type'

curl -fsS "$MERCATOR_API_URL/v1/runs/$RUN_ID/decision?workspace_id=$MERCATOR_WORKSPACE_ID" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  | jq '{selected_offer_snapshot_id: .decision.selected_offer_snapshot_id, candidate_count: (.decision.candidates | length), rejected_count: ([.decision.candidates[] | select(.feasible | not)] | length)}'

curl -fsS "$MERCATOR_API_URL/v1/sinks/audit" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  | jq '{sink_id, cursor}'
```

For a CLI-first walkthrough that produces the same state, see
the [CLI reference](cli.md) and
[Docker adapter operation](../production/docker-adapter-operation.md).
