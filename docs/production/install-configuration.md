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

For evaluation without a Go toolchain, a prebuilt image is published to
`ghcr.io/benngarcia/mercator` by CI. Use
`docker run ghcr.io/benngarcia/mercator:latest` with the same environment
variables and Docker-socket mount as the README quickstart instead of building
the image locally.

## Required Runtime Configuration

Set explicit process configuration for any evaluation you want to reproduce
after restart:

```sh
export MERCATOR_ADDR=127.0.0.1:8080
export MERCATOR_SQLITE_DSN='file:/var/lib/mercator/mercator.db'
export MERCATOR_API_TOKEN="$(openssl rand -hex 32)"
export MERCATOR_AUTH_WORKSPACES='ws_eval'
```

`serve` always uses the Docker host adapter, so a running Docker daemon is a
hard requirement for a first run.

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
| `MERCATOR_DOCKER_BIN` | `docker` lookup behavior in CLI client | Docker executable path. |
| `MERCATOR_DOCKER_NATIVE_REF` | `loopback` | Native reference in the synthetic Docker offer. |
| `MERCATOR_DOCKER_OFFER_ID` | `offer_docker_loopback` | Synthetic Docker offer ID. |
| `MERCATOR_DOCKER_CONNECTION_ID` | `conn_docker_loopback` | Synthetic Docker connection ID. |
| `MERCATOR_DOCKER_ARCH` | Docker host architecture, or `amd64` if the host probe fails | Optional architecture override for an intentionally emulated Docker offer. |
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
  startup log and changes across restarts. Because the token lands in stdout,
  operators who ship or aggregate logs should always set `MERCATOR_API_TOKEN`
  explicitly rather than rely on the generated one.
- The no-secrets-in-public-events guarantee redacts env **values** only. Image
  references and container args are recorded verbatim in public events, so do
  not embed secrets in image refs or args.
- Binding `MERCATOR_ADDR` to a non-loopback address serves plaintext HTTP (the
  server logs a warning when it does). Put a TLS-terminating reverse proxy in
  front for any non-local exposure.
- Mercator does not expose a secret vault. Put stable non-sensitive defaults in
  workload env and pass per-run overrides through `create_run`; workloads that
  need secrets should call their own secret-management backend.
