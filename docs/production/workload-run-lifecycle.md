# Workload Run Lifecycle

A Run is a deployment-global identity bound to one immutable workload revision.
Mercator accepts it, decides where it should execute, drives the external
lifecycle, records the outcome, converges cleanup, and closes the stream.

## Configure The Client

```sh
export MERCATOR_API_URL='http://127.0.0.1:8080'
export MERCATOR_API_TOKEN='...'
```

## Create

```sh
curl -fsS -X POST "$MERCATOR_API_URL/v1/runs" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: run-eval-1:create' \
  -d '{"run_id":"run_eval_1","image":"busybox:1.37","args":["echo","hello"]}'

mercator run create --run-id run_eval_1 busybox:1.37 -- echo hello
```

For production workloads, create an immutable workload revision and submit its
IDs. A Run may override declared environment bindings for that execution, but
the saved revision itself never changes.

## Observe

```sh
mercator run list
mercator run get --run-id run_eval_1
mercator run wait --run-id run_eval_1
mercator run events --run-id run_eval_1
mercator run decision --run-id run_eval_1
```

The Run record is a projection of its event stream. Public events contain the
placement decision and lifecycle facts without provider credentials, workload
secret values, or private provider response bodies.

## Advance And Cancel

```sh
mercator run refresh --run-id run_eval_1
mercator run cancel --run-id run_eval_1
```

Refresh performs the same idempotent reconciliation as the background sweep.
Cancellation records intent first, converges the external execution, records a
terminal outcome, and closes only after cleanup is confirmed. Cleanup failure
leaves the Run open with `cleanup: "blocked"`; another sweep or refresh retries
the same operation.

## Workload Reporting

When reporting is configured, Mercator injects `MERCATOR_RUN_ID`,
`MERCATOR_REPORT_URL`, and `MERCATOR_RUN_TOKEN`. A workload posts to
`/v1/runs/{run_id}/report`; see [workload-reporting.md](workload-reporting.md).

## Isolation

All Runs, Connections, Offers, Rentals, Nodes, Artifacts, and cache names share
one deployment scope. Product tenancy is the dispatching application's job.
Use another Mercator deployment when workloads require a distinct execution
and credential boundary.
