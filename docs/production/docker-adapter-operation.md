# Docker Adapter Operation

The Docker adapter runs OCI workloads as local Docker containers through the
Docker CLI client. Use it for host-adapter evaluation, not as a complete
container-platform replacement.

## Preconditions

```sh
docker version
go test ./internal/adapter/docker -count=1
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run Integration -count=1
```

The live integration test is guarded by `MERCATOR_DOCKER_INTEGRATION=1`.

## Start Mercator With Docker

```sh
export MERCATOR_ADDR=127.0.0.1:8080
export MERCATOR_SQLITE_DSN='file:/tmp/mercator-docker.db'
export MERCATOR_API_TOKEN="$(openssl rand -hex 32)"
export MERCATOR_AUTH_WORKSPACES='ws_eval'
export MERCATOR_ADAPTER=docker
export MERCATOR_DOCKER_ARCH=amd64

go run ./cmd/mercator serve
```

Optional Docker offer identity variables:

```sh
export MERCATOR_DOCKER_BIN=/usr/local/bin/docker
export MERCATOR_DOCKER_NATIVE_REF=local
export MERCATOR_DOCKER_OFFER_ID=offer_local_docker
export MERCATOR_DOCKER_CONNECTION_ID=conn_local_docker
```

## Workload Requirements

Docker workloads must satisfy the same V1 workload contract:

- exactly one Linux container named `main`;
- digest-pinned image reference, not a mutable tag;
- `linux/amd64` or `linux/arm64` platform;
- no mounts, workdir, stdin, TTY, host networking, sidecars, setup commands, or
  raw extension payloads;
- literal env values only. If the workload needs secrets, pass the non-secret
  configuration it needs to call its own secret-management backend.

## Inspect Docker-Owned Objects

Mercator names Docker containers by the deterministic launch key and applies
labels:

```sh
docker ps -a \
  --filter label=mercator.workspace_id=ws_eval \
  --format '{{.Names}} {{.Status}} {{.Labels}}'
```

Important labels:

- `mercator.workspace_id`
- `mercator.run_id`
- `mercator.attempt_id`
- `mercator.launch_key`
- `mercator.ownership_token`
- `mercator.cleanup_locator`
- `mercator.request_hash`
- `mercator.workload_id`
- `mercator.revision_id`

## Cleanup Verification

After a completed or cancelled run:

```sh
go run ./cmd/mercator run get --workspace-id ws_eval --run-id run_docker_1 | jq .
docker ps -a --filter label=mercator.run_id=run_docker_1
```

The run record should show `cleanup: "confirmed"` before `closed: true`.
No container for that run should remain after cleanup confirmation.

If an operator finds leftover owned containers, capture events first:

```sh
go run ./cmd/mercator run events --workspace-id ws_eval --run-id run_docker_1 > /tmp/run_docker_1.events.json
docker ps -a --filter label=mercator.run_id=run_docker_1
```

Then remove only containers whose Mercator ownership labels match the affected
workspace and run.
