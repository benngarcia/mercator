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
- each env binding is exactly one literal value (`secret_ref` bindings are
  rejected; see [ADR 0001](../adr/0001-env-api-no-secret-vault.md));
- literal env values are at most 32768 bytes;
- ports are TCP only and in range `1-65535`;
- public port exposure requires `spec.network.inbound` set to `public_port`;
- `spec.raw` extension payloads are rejected.

## The Minimal Run: One Field

The smallest run is just an image. The CLI takes it as the first positional
argument; any container args follow `--`. `--run-id` is optional (the server
generates a uuidv7-based id and returns it at `.run.id`), `--idempotency-key` is
optional (the CLI mints a stable one), and no workload spec is needed — the
server synthesizes the single container and defaults the container name (`main`),
platform (`linux/amd64`), resources, network, placement, and execution policy.

The image must be **digest-pinned** (`image@sha256:...`). A mutable tag such as
`busybox` or `busybox:latest` is rejected at create time with a `400` error —
registry tag→digest resolution is not implemented — so resolve the digest
yourself first:

```sh
export MERCATOR_WORKSPACE_ID=ws_eval
docker pull -q busybox:latest >/dev/null
IMAGE="$(docker inspect --format '{{index .RepoDigests 0}}' busybox:latest)"
go run ./cmd/mercator run create "$IMAGE" -- echo hi | jq '.run.id'
```

The equivalent raw HTTP call carries the required
`Idempotency-Key` header and omits `run_id`:

```sh
curl -fsS -X POST "$MERCATOR_API_URL/v1/runs?workspace_id=ws_eval" \
  -H "Authorization: Bearer $MERCATOR_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: idem-minimal-1' \
  -d "{\"image\":\"$IMAGE\",\"args\":[\"echo\",\"hi\"]}" | jq '.run.id'
```

## Create A Run From A Full Workload Spec

```sh
WORKLOAD_JSON="$(jq -c . /tmp/mercator-workload.json)"

go run ./cmd/mercator run create \
  --workspace-id ws_eval \
  --run-id run_eval_1 \
  --idempotency-key idem-run-eval-1 \
  --workload-json "$WORKLOAD_JSON"
```

`POST /v1/runs` returns the same envelope as the read endpoints and a `202`
status: `{"run_id": "...", "run": {id, workspace_id, workload_revision_id,
phase, outcome, exit_code, cleanup, cleanup_error, disposition, closed}, "metadata": {...},
"links": {...}, "duplicate": <bool>}`. The convenience top-level `run_id` is
returned on every run response (create, get, wait, refresh, cancel) and always
equals `.run.id`; `metadata` is reserved for future per-response metadata. Read
the run id from `.run_id` or `.run.id`. `duplicate` is `true` (and otherwise
omitted) when the create was a safe idempotent replay.

The durable `compute.run.requested.v1` event is the acceptance point. Once that
event is recorded, create returns `202` with the canonical Run identifier and
latest Run record even when eager advancement encounters a provider failure. A
definitive launch failure is returned as a closed failed Run; an indeterminate
launch is returned as an open Run that reconciliation will continue. Validation,
authorization, idempotency, image resolution, and initial persistence failures
occur before acceptance and retain their explicit error responses.

Provisionable capacity can become stale between placement and Create. When a
provider returns the typed `capacity_unavailable` outcome and proves that
Create produced no external object, Mercator completes that attempt and places
the Run again. The new decision excludes the exact rejected Offer snapshot ID,
records a new attempt and launch intent, and stays within the workload's
`max_pre_start_attempts` bound. The initial attempt counts toward that bound.
Exhaustion closes the Run as failed with `RETRY_EXHAUSTED` on the public
`compute.run.closed.v1` event.

Other failures do not enter replacement placement. Invalid requests,
authentication failures, ownership conflicts, rate-limit exhaustion, and
unclassified failures close terminally. A timeout, transport failure, or 5xx
Create whose side effect is indeterminate stays attached to its original
launch key while Mercator reconciles Observe and ListOwned. It never creates a
replacement attempt while an external object may exist.

You can omit `--workspace-id` on every `run` subcommand by exporting
`MERCATOR_WORKSPACE_ID`:

```sh
export MERCATOR_WORKSPACE_ID=ws_eval
go run ./cmd/mercator run create \
  --run-id run_eval_1 \
  --idempotency-key idem-run-eval-1 \
  --workload-json "$WORKLOAD_JSON"
```

An explicit `--workspace-id` flag always overrides the env default.

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
  -d '{"workspace_id":"ws_eval","run_id":"run_from_revision_1","workload_id":"wrk_eval","workload_revision_id":"wrev_eval_1"}'
```

## Read Lifecycle State

```sh
go run ./cmd/mercator run list --workspace-id ws_eval | jq .
go run ./cmd/mercator run get --workspace-id ws_eval --run-id run_eval_1 | jq .
go run ./cmd/mercator run wait --workspace-id ws_eval --run-id run_eval_1 | jq .
go run ./cmd/mercator run events --workspace-id ws_eval --run-id run_eval_1 | jq .
go run ./cmd/mercator run decision --workspace-id ws_eval --run-id run_eval_1 | jq .
```

`run get` returns the run object directly, including `exit_code` once a terminal
observation has been recorded:

```sh
go run ./cmd/mercator run get --workspace-id ws_eval --run-id run_eval_1 \
  | jq '{phase: .run.phase, outcome: .run.outcome, exit_code: .run.exit_code, closed: .run.closed}'
```

Read the container exit code from `.run.exit_code` directly. It is omitted while
"not yet known"; a present `0` is a real success exit. You no longer need to
parse the `compute.run.external_state_observed.v1` event to recover it.

`run wait` issues one HTTP long-poll. The server actively advances the run
(about every 100ms) for up to ~30 seconds, then returns `200` with the terminal
run if it closed before the deadline, or `202` with the latest still-open run if
not. To block past a single deadline, loop while the response is `202` and
reissue `run wait`. On the local Docker path a short run such as `echo hi`
usually closes within the first long-poll, so a single `run wait` is often
sufficient.

A run whose launch definitively fails — for example an image the Docker daemon
cannot pull — records a `failed` outcome and closes; it is not retried
indefinitely.

Polling is optional. The serving broker runs a background reconcile sweep every
minute (alongside the orphan-reclaiming janitor) that advances every open run:
observing container exits, recording terminal outcomes, confirming cleanup, and
closing the run. A signed terminal workload report is acknowledged after its
fact is durable; this sweep performs cleanup after the report response no longer
depends on the reporting container. An exited run therefore reaches `closed`
even if no client calls `run wait` or `run refresh` again. Client polling only
changes how quickly the run converges.

## Refresh And Cancel

```sh
go run ./cmd/mercator run refresh --workspace-id ws_eval --run-id run_eval_1 | jq .
go run ./cmd/mercator run cancel --workspace-id ws_eval --run-id run_eval_1 | jq .
```

Refresh resumes event-log-authoritative advancement. Cancel records a terminal
fact, then the same lifecycle owner used for successful and failed exits invokes
the recorded `Release` or `Terminate` disposition. Cleanup confirmation remains
required before the run closes.

## Cleanup Disposition: Terminate vs Release

Every run records, at launch time, a cleanup **disposition** that determines
what teardown does. This is a cost-safety contract borrowed from the adapter
boundary:

- **`terminate`** — the run provisioned a resource **we own** (a host/instance
  from a *provisionable* offer). Cleanup must **destroy that host**.
- **`release`** — the run occupies a slot in a pool **we do not own** (a
  *standing* offer, e.g. local Docker). Cleanup removes **only our
  job/container** and never touches the host.

The disposition is derived from the selected offer's `kind`
(`provisionable -> terminate`, `standing -> release`) and **recorded explicitly
on the `compute.run.launch_intent_recorded.v1` event at launch time**. It is
surfaced on the run object as `.run.disposition`:

```sh
go run ./cmd/mercator run get --workspace-id ws_eval --run-id run_eval_1 \
  | jq '{phase: .run.phase, disposition: .run.disposition, cleanup: .run.cleanup, closed: .run.closed}'
```

**Recorded, not inferred.** The cleanup path dispatches on the value recorded at
launch time — it never re-derives the disposition from live offers or current
state at cleanup time. This is what makes teardown crash-safe and orphan-free: a
run that provisioned a host is always torn down with `terminate` even if the
offer that produced it has since changed or disappeared. The adapter exposes two
distinct cleanup verbs, `Release` and `Terminate`, and the orchestrator (and the
orphan-reclaiming janitor) call the one matching the recorded disposition.

An absent or unknown recorded disposition is invalid run history. Cleanup fails
before a provider call instead of guessing which resource Mercator owns.

**Adapter semantics.** The fake adapter (an internal test mechanism) implements
both verbs idempotently and tracks which path ran: its standing offer drives
`release` and its provisionable variant drives `terminate`, both with no network.
Local Docker is a standing pool: it implements `Release` (remove the container)
and returns an explicit error from `Terminate` (there is no broker-owned host to
destroy), which the orchestrator never reaches because a Docker offer always
records `release`.

## Event Order To Check

For a successful Docker run, the public event stream should show the core
shape:

1. run requested;
2. placement decided;
3. launch intent recorded;
4. launch accepted or duplicate launch observed;
5. first terminal fact, from an observation, signed report, or cancellation;
6. outcome recorded;
7. cleanup requested;
8. cleanup confirmed;
9. run closed.

When provider cleanup fails, the run remains open with its outcome visible,
`cleanup: "blocked"`, and a structured `cleanup_error`. The public event stream
records `compute.run.cleanup_failed.v1` with a stable redacted error. Refresh or
the next background sweep retries the same idempotent provider operation; a
later confirmation clears the projected error and closes the run.

A successful stale-Offer replacement adds a complete pre-start attempt before
the successful sequence:

1. placement decided for the stale Offer;
2. attempt created and launch intent recorded;
3. launch failed with `code=PROVIDER_CAPACITY_UNAVAILABLE` and
   `side_effect=none`;
4. a fresh placement decision records the rejected snapshot as
   `PREVIOUS_ATTEMPT_CAPACITY_UNAVAILABLE` and selects another Offer;
5. a new attempt and launch intent are recorded before the next provider call;
6. launch accepted, followed by the normal observation, cleanup, and close
   sequence.

Every attempt has a distinct attempt ID, launch key, ownership token, and
cleanup locator. `run decision` returns the latest placement. `run events`
retains every placement, attempt, intent, and outcome for audit.

Public CloudEvents `data` payloads are snake_case. For example, the terminal
`compute.run.external_state_observed.v1` event carries
`{external_id, launch_key, phase, observed_at, exit_code, native_json}`, and
`compute.run.launch_accepted.v1` carries
`{external_id, launch_key, ownership_token, cleanup_locator, phase, accepted_at,
duplicate}`, and `compute.run.cleanup_confirmed.v1` carries
`{launch_key, disposition}` recording which cleanup verb (terminate or release)
ran. Prefer reading `run.exit_code`, `run.outcome`, and `run.disposition` from
the run object over parsing these events.

Do not infer state from adapter observations that are not represented by events.
