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

## Optional Sentry Provider-Failure Reporting

Mercator sends final provider launch and cleanup failures to Sentry only when
`SENTRY_DSN` is set. The minimum configuration is:

```sh
export SENTRY_DSN='https://<public-key>@<sentry-host>/<project-id>'
export SENTRY_ENVIRONMENT=staging
export SENTRY_RELEASE="mercator@$(git rev-parse --short=12 HEAD)"
```

Copy the DSN from **Sentry project settings → Client Keys (DSN)**. Choose the
environment name used by that deployment, and set the release to the immutable
tag, image release, or Git commit being run. A DSN without either companion
variable fails startup. An absent DSN disables the integration.

Sentry events carry workspace, Run, attempt, connection, adapter, operation,
Offer snapshot/native identity, provider status and code, retryability,
side-effect certainty, retry count, environment, and Mercator release. The
fingerprint groups on adapter, operation, failure kind, provider code, and HTTP
status. A single capacity rejection is a warning; an exhausted retry and every
other final failure is an error.

Events are built from an explicit allow-list. They do not include provider
response bodies, provider request payloads, authorization headers, connection
credentials, registry credentials, or workload environment values. Mercator
uses the generic server name `mercator` rather than sending the host name.

### Safe Verification

First exercise the real SDK boundary with the recording transport. These tests
send no network requests and allocate no provider capacity:

```sh
go test ./internal/sentryreporter \
  -run 'TestAbsentDSNDisablesReporting|TestPresentDSNRequiresEnvironmentAndRelease|TestProviderCapacityFailureRecordsSafeCorrelatedWarning|TestProviderFailureGroupingAndSeverity|TestFlushStopsAtCallerDeadline' \
  -count=1
go test ./internal/broker \
  -run 'TestShadeformOutOfStockFailureIsPrivateAndPublicSafe|TestBrokerReportsTerminateFailureWithRunCorrelation' \
  -count=1
```

For a deployment check, start Mercator with the three real values and require
`/health/ready` to pass. Confirm the next naturally occurring provider failure
appears in the intended Sentry project with the configured environment and
release. Do not force a paid launch merely to create a test event. Delivery is
asynchronous; shutdown waits at most two seconds for queued events and logs
`flush Sentry events: deadline exceeded` if that bound expires.

## Useful Verification Commands

```sh
go test ./...
go build ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
go test -race ./internal/eventlog ./internal/orchestrator ./internal/sinks/...
MERCATOR_E2E_FAKE=1 go test ./... -run E2E -count=1
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run Integration -count=1
```

Run the Docker integration only on hosts where Docker is intentionally available
for evaluation.
