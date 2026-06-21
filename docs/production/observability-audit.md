# Observability And Audit

Mercator V1 exposes operational state through health endpoints, public events,
placement decisions, run records, sink cursors, Docker labels, and process logs.

## Health And Static Surfaces

```sh
curl -fsS "$MERCATOR_API_URL/health/live"
curl -fsS "$MERCATOR_API_URL/health/ready"
curl -fsS "$MERCATOR_API_URL/openapi.json" | jq '.paths | keys'
open "$MERCATOR_API_URL/"
```

Health and OpenAPI currently report process/API availability, not deep adapter
or downstream sink health.

## Run Audit

```sh
go run ./cmd/mercator run get --workspace-id ws_eval --run-id run_eval_1 | jq .
go run ./cmd/mercator run events --workspace-id ws_eval --run-id run_eval_1 | jq .
go run ./cmd/mercator run decision --workspace-id ws_eval --run-id run_eval_1 | jq .
```

Use events to establish lifecycle facts. Use the placement decision to inspect:

- selected offer snapshot ID;
- candidate feasibility;
- rejection reasons;
- cost/start/pull estimates;
- collection report and model version.

## Sink Cursor Audit

```sh
go run ./cmd/mercator sink status --sink-id audit | jq .
go run ./cmd/mercator sink deliver --sink-id audit | jq .
go run ./cmd/mercator sink replay --sink-id audit --from 0 --limit 25 --replay-id audit-sample | jq .
```

## Docker Adapter Audit

```sh
docker ps -a \
  --filter label=mercator.workspace_id=ws_eval \
  --format '{{json .}}' | jq .
```

Compare Docker labels against run events before removing any external object.

## Process Logs

Startup logs include:

- generated API token if `MERCATOR_API_TOKEN` was omitted;
- listen address;
- fatal configuration/startup errors.

Do not rely on logs as the source of truth for run state. Logs are supporting
evidence; the event log is authoritative.

## Useful Verification Commands

```sh
go test ./...
go build ./...
go test -race ./internal/eventlog ./internal/orchestrator ./internal/reconciler ./internal/sinks/...
MERCATOR_E2E_FAKE=1 go test ./... -run E2E -count=1
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run Integration -count=1
```

Run the Docker integration only on hosts where Docker is intentionally available
for evaluation.
