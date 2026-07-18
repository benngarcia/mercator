# OpenAPI And HTTP Route Overview

Mercator serves a hand-written OpenAPI 3.1 document at `/openapi.json`. Use it
as the machine-readable contract for generated clients, schema inspection, and
route discovery.

```sh
curl -fsS "$MERCATOR_API_URL/openapi.json" | jq '.info'
curl -fsS "$MERCATOR_API_URL/openapi.json" | jq -r '.paths | keys[]'
```

Health, OpenAPI, and the UI shell are public on the configured listen
interface. The OpenAPI-listed `/v1/*` operator routes require bearer auth with
`Authorization: Bearer $MERCATOR_API_TOKEN`; workspace-scoped reads and writes
also require a `workspace_id` query parameter or request field unless the
server is configured with exactly one allowed workspace.

## Route Families

| Family | Routes | Use |
| --- | --- | --- |
| Health and discovery | `GET /health/live`, `GET /health/ready`, `GET /openapi.json`, `GET /` | Process liveness/readiness, OpenAPI discovery, and the embedded console shell. These are public on the listen interface. |
| Runs | `POST /v1/runs`, `GET /v1/runs`, `GET /v1/runs/{run_id}`, `GET /v1/runs/{run_id}:wait`, `POST /v1/runs/{run_id}:refresh`, `POST /v1/runs/{run_id}:cancel`, `GET /v1/runs/{run_id}/events`, `GET /v1/runs/{run_id}/decision` | Create runs, list/read them, wait for closure, refresh/cancel adapter state, inspect public events, and read the placement decision. |
| Placement | `POST /v1/placements:preview` | Evaluate a workload against current offers and policy without creating a run. |
| Workloads and images | `POST /v1/workloads`, `GET/POST /v1/workloads/{workload_id}/revisions`, `GET /v1/workloads/{workload_id}/revisions/{revision_id}`, `POST /v1/images:resolve` | Store workload names/revisions, read immutable revisions, and resolve image tags to immutable image metadata. |
| Connections and offers | `GET /v1/adapters`, `GET /v1/connections`, `GET /v1/offers` | Discover registered adapters' onboarding manifests (display metadata, config fields, setup steps), inspect configured provider connections, and the offers visible to the placement engine for a workspace. |
| Sinks | `GET /v1/sinks/{sink_id}`, `POST /v1/sinks/{sink_id}:deliver`, `POST /v1/sinks/{sink_id}:replay` | Read sink cursor state, deliver pending events, and replay events after a global position. |

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
