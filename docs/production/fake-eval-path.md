# Fake Adapter Evaluation Path

Use the fake adapter first. It is deterministic, requires no Docker daemon, and
exercises the event-first broker path through placement, launch intent, launch,
observation, cleanup, closure, public events, decision reads, sink cursor reads,
and CLI JSON behavior.

## Start Server

```sh
rm -f /tmp/mercator-fake.db /tmp/mercator-fake.db-wal /tmp/mercator-fake.db-shm

export MERCATOR_ADDR=127.0.0.1:8080
export MERCATOR_SQLITE_DSN='file:/tmp/mercator-fake.db'
export MERCATOR_API_TOKEN="$(openssl rand -hex 32)"
export MERCATOR_SECRET_KEY_HEX="$(openssl rand -hex 32)"
export MERCATOR_AUTH_WORKSPACES='ws_eval'
export MERCATOR_FAKE_OFFER=1

go run ./cmd/mercator serve
```

In another shell:

```sh
export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_API_TOKEN='<token from first shell>'
```

## Create A Digest-Pinned Workload File

```sh
cat >/tmp/mercator-workload.json <<'JSON'
{
  "id": "wrev_fake_1",
  "workspace_id": "ws_eval",
  "workload_id": "wrk_fake",
  "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "spec": {
    "containers": [
      {
        "name": "main",
        "image": "example.com/eval/fake@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "platform": {"os": "linux", "architecture": "amd64"},
        "entrypoint": ["/bin/sh"],
        "args": ["-c", "echo mercator-fake-eval"],
        "env": {"LOG_LEVEL": {"value": "debug"}},
        "ports": []
      }
    ],
    "resources": {
      "cpu": {"min_millis": 100},
      "memory": {"min_bytes": 67108864},
      "ephemeral_disk": {"min_bytes": 1048576}
    },
    "network": {"inbound": "none"},
    "placement": {"objective": "balanced", "expected_runtime_seconds": 1},
    "execution": {"max_runtime_seconds": 60, "max_pre_start_attempts": 1}
  }
}
JSON
```

## Create And Inspect A Run

```sh
WORKLOAD_JSON="$(jq -c . /tmp/mercator-workload.json)"

go run ./cmd/mercator run create \
  --workspace-id ws_eval \
  --run-id run_fake_1 \
  --idempotency-key idem-run-fake-1 \
  --workload-json "$WORKLOAD_JSON"

go run ./cmd/mercator run get --workspace-id ws_eval --run-id run_fake_1 | jq .
go run ./cmd/mercator run events --workspace-id ws_eval --run-id run_fake_1 | jq .
go run ./cmd/mercator run decision --workspace-id ws_eval --run-id run_fake_1 | jq .
go run ./cmd/mercator sink status --sink-id audit | jq .
```

Expected evaluation signals:

- The run reaches a closed state in the fake path.
- Public events omit private event payloads and secret material.
- Placement decision includes feasible candidate information for
  `offer_local_fake`.
- Sink `audit` exists and cursor state is readable.

## Idempotency Check

Re-run the same create command with the same idempotency key and identical
payload. It should return JSON with the same `run_id` and may mark
`duplicate: true`.

Then reuse the same idempotency key with a different `run_id`; it should return
a JSON error with code `IDEMPOTENCY_CONFLICT`.
