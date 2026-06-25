# Install And Configuration

This runbook starts the current V1 process and documents only environment
variables wired by `cmd/mercator/main.go`.

## Build

```sh
go build ./...
go test ./...
go build -o ./bin/mercator ./cmd/mercator
```

The binary is cgo-free because the project uses `modernc.org/sqlite`.

## Required Runtime Configuration

Set explicit process configuration for any evaluation you want to reproduce
after restart:

```sh
export MERCATOR_ADDR=127.0.0.1:8080
export MERCATOR_SQLITE_DSN='file:/var/lib/mercator/mercator.db'
export MERCATOR_API_TOKEN="$(openssl rand -hex 32)"
export MERCATOR_AUTH_WORKSPACES='ws_eval'
```

Then start the server:

```sh
./bin/mercator serve
```

`serve` is optional. Running `./bin/mercator` with no subcommand starts the same
server path.

## Environment Variables

| Variable | Default | Use |
| --- | --- | --- |
| `MERCATOR_ADDR` | `127.0.0.1:8080` | HTTP listen address. |
| `MERCATOR_SQLITE_DSN` | `file:/data/mercator.db` | SQLite event-log DSN. |
| `MERCATOR_API_TOKEN` | generated at startup | Bearer token for `/v1/*`. Set explicitly for operations. |
| `MERCATOR_AUTH_WORKSPACES` | `*` | Comma-separated workspace allow list for the bearer principal. |
| `MERCATOR_ADAPTER` | unset | Leave unset for broker-backed service mode with the bootstrap Docker connection. Set to `fake` only for fake/offline smoke tests. |
| `MERCATOR_DOCKER_BIN` | `docker` lookup behavior in CLI client | Docker executable path. |
| `MERCATOR_DOCKER_NATIVE_REF` | derived from Docker context/host, otherwise `loopback` | Native reference in the bootstrap Docker offer. |
| `MERCATOR_DOCKER_OFFER_ID` | `offer_docker_<label>` | Bootstrap Docker offer ID. |
| `MERCATOR_DOCKER_CONNECTION_ID` | `conn_docker_<label>` | Bootstrap Docker connection ID. |
| `MERCATOR_DOCKER_ARCH` | probed from Docker, fallback `amd64` | Optional architecture override for the Docker offer. |
| `MERCATOR_API_URL` | none | CLI base URL; required for CLI mode unless `--api-url` is provided. |

## Health And Discovery

```sh
curl -fsS http://127.0.0.1:8080/health/live
curl -fsS http://127.0.0.1:8080/health/ready
curl -fsS http://127.0.0.1:8080/openapi.json | jq '.info'
open http://127.0.0.1:8080/
```

Health, OpenAPI, and the UI are not bearer-token protected. In `cmd/mercator`,
executable `/v1/*` API routes are always bearer-token protected; if
`MERCATOR_API_TOKEN` is omitted, the server generates an ephemeral token and
prints it to the startup log.

## Operator Notes

- Keep the SQLite database and its WAL/shm siblings on durable local storage.
- Run one Mercator process against one SQLite DSN. The event log sets SQLite max
  open connections to one inside the process.
- If `MERCATOR_API_TOKEN` is omitted, the generated token is printed to the
  startup log and changes across restarts.
- Mercator does not expose a secret vault. Put stable non-sensitive defaults in
  workload env and pass per-run overrides through `create_run`; workloads that
  need secrets should call their own secret-management backend.
