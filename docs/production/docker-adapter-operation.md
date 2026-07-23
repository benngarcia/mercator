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

go run ./cmd/mercator serve
```

On a loopback address a fresh broker already seeds and authorizes a `docker`
connection in `ws_default` when the local daemon answers, so the local endpoint
needs no setup there. Register one explicitly when you want a named connection,
a non-default workspace, or a remote endpoint:

```sh
export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_API_TOKEN='<same token>'
export MERCATOR_WORKSPACE_ID=ws_eval

go run ./cmd/mercator connection create \
  --connection-id conn_docker_loopback --adapter-type docker
go run ./cmd/mercator connection authorize \
  --connection-id conn_docker_loopback
```

Mercator probes the registered Docker endpoint and advertises its native
`linux/amd64` or `linux/arm64` platform. To advertise an emulated platform for
one endpoint, set that connection's `arch` config and make the workload
platform and image digest match it.

Remote endpoints and alternate binaries belong to the connection config:

```sh
go run ./cmd/mercator connection create \
  --connection-id conn_docker_gpu \
  --adapter-type docker \
  --config host=ssh://operator@gpu-host \
  --config bin=/usr/local/bin/docker \
  --config arch=amd64
```

Private images use the Docker connection's optional registry credential. The
connection config identifies the registry and username; the connection
credential carries a pull-only token through the existing environment or
sealed-store source:

```sh
export DOCKERHUB_PULL_TOKEN='<pull-only token>'

go run ./cmd/mercator connection create \
  --connection-id conn_docker_private \
  --adapter-type docker \
  --config registry_server=docker.io \
  --config registry_username=bucketrobotics \
  --credential-source env \
  --credential-ref DOCKERHUB_PULL_TOKEN
go run ./cmd/mercator connection authorize \
  --connection-id conn_docker_private
```

The registry server, username, and credential must be configured together.
Mercator writes the credential to a mode-0600 Docker config for each container
create operation, passes the config directory through Docker's global
`--config` flag, and removes the directory when the command finishes. The
credential does not enter Docker argv, workload environment variables, or
the Docker subprocess environment, or public events. Credential-free connections continue to pull public images
through Docker's ambient behavior.

## Workload Requirements

Docker workloads must satisfy the same V1 workload contract:

- exactly one Linux container named `main`;
- an image the broker host's Docker daemon holds, so a tag can be resolved to
  the digest and platform Mercator records. What gets stored and launched is
  always digest-pinned, whether you pinned it or the broker resolved it;
- `linux/amd64` or `linux/arm64` platform. Leave it unset and the resolved
  image supplies it;
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
