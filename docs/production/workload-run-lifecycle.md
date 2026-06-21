# Workload And Run Lifecycle

Mercator V1 accepts immutable OCI workload revisions and drives a run through
event-recorded placement, launch, observation, cleanup, and closure.

## Workload Contract

The validator enforces:

- one container exactly;
- container name `main`;
- image reference matching `image@sha256:<64 hex chars>`;
- platform `linux/amd64` or `linux/arm64`;
- env names matching `^[A-Z_][A-Z0-9_]*$`;
- each env binding has exactly one literal value or exact secret version;
- literal env values are at most 32768 bytes;
- ports are TCP only and in range `1-65535`;
- public port exposure requires `spec.network.inbound` set to `public_port`;
- `spec.raw` extension payloads are rejected.

## Create A Run Directly

```sh
WORKLOAD_JSON="$(jq -c . /tmp/mercator-workload.json)"

go run ./cmd/mercator run create \
  --workspace-id ws_eval \
  --run-id run_eval_1 \
  --idempotency-key idem-run-eval-1 \
  --workload-json "$WORKLOAD_JSON"
```

Equivalent REST shape:

```sh
curl -fsS -X POST "$MERCATOR_API_URL/v1/runs" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H "Idempotency-Key: idem-run-eval-1" \
  -H "Content-Type: application/json" \
  -d "$(jq -n --argjson workload "$WORKLOAD_JSON" \
    '{workspace_id:"ws_eval", run_id:"run_eval_1", workload:$workload}')"
```

## Store And Run A Workload Revision

```sh
curl -fsS -X POST "$MERCATOR_API_URL/v1/workloads" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H "Idempotency-Key: idem-workload-wrk-eval" \
  -H "Content-Type: application/json" \
  -d '{"workspace_id":"ws_eval","workload_id":"wrk_eval","name":"eval"}'

curl -fsS -X POST "$MERCATOR_API_URL/v1/workloads/wrk_eval/revisions?workspace_id=ws_eval" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H "Idempotency-Key: idem-wrev-eval-1" \
  -H "Content-Type: application/json" \
  -d "$(jq -n --argjson revision "$(jq -c . /tmp/mercator-workload.json)" \
    '{revision:$revision}')"

curl -fsS -X POST "$MERCATOR_API_URL/v1/runs" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H "Idempotency-Key: idem-run-from-revision-1" \
  -H "Content-Type: application/json" \
  -d '{"workspace_id":"ws_eval","run_id":"run_from_revision_1","workload_id":"wrk_eval","workload_revision_id":"wrev_fake_1"}'
```

## Read Lifecycle State

```sh
go run ./cmd/mercator run list --workspace-id ws_eval | jq .
go run ./cmd/mercator run get --workspace-id ws_eval --run-id run_eval_1 | jq .
go run ./cmd/mercator run wait --workspace-id ws_eval --run-id run_eval_1 | jq .
go run ./cmd/mercator run events --workspace-id ws_eval --run-id run_eval_1 | jq .
go run ./cmd/mercator run decision --workspace-id ws_eval --run-id run_eval_1 | jq .
```

`run wait` polls the HTTP wait endpoint for up to about 30 seconds. It returns
`200` if the run closes before the deadline and `202` with the current run state
if it is still open.

## Refresh And Cancel

```sh
go run ./cmd/mercator run refresh --workspace-id ws_eval --run-id run_eval_1 | jq .
go run ./cmd/mercator run cancel --workspace-id ws_eval --run-id run_eval_1 | jq .
```

Refresh resumes event-log-authoritative advancement. Cancel records cancellation
through the adapter-backed lifecycle and still requires cleanup confirmation
before the run is closed.

## Event Order To Check

For a successful fake run, the public event stream should show the core shape:

1. run requested;
2. placement decided;
3. launch intent recorded;
4. launch accepted or duplicate launch observed;
5. terminal observation;
6. cleanup requested;
7. cleanup confirmed;
8. run closed.

Do not infer state from adapter observations that are not represented by events.
