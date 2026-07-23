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

For evaluation without a Go toolchain, take a binary from the
[releases page](https://github.com/benngarcia/mercator/releases/latest), or run
the image CI publishes to `ghcr.io/benngarcia/mercator`:

```sh
docker run --rm \
  -e MERCATOR_ADDR=0.0.0.0:8080 \
  -e MERCATOR_API_TOKEN=dev-token \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -p 8080:8080 ghcr.io/benngarcia/mercator:latest serve
```

Mounting the Docker socket grants the container root-equivalent control of the
host Docker daemon. That is fine on a machine you own for local evaluation, and
not fine on an untrusted host. Set `MERCATOR_API_TOKEN` yourself for the
container: the token handoff that spares a local CLI any configuration writes
into the container filesystem, where your shell cannot read it.

## Required Runtime Configuration

Set explicit process configuration for any evaluation you want to reproduce
after restart:

```sh
export MERCATOR_ADDR=127.0.0.1:8080
export MERCATOR_SQLITE_DSN='file:/var/lib/mercator/mercator.db'
export MERCATOR_API_TOKEN="$(openssl rand -hex 32)"
```

`serve` registers the Docker adapter type but starts with no connections.
Create and authorize each local or remote Docker endpoint through the
connection API before launching a run.

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
| `MERCATOR_SQLITE_DSN` | `$XDG_DATA_HOME/mercator/mercator.db`, else `~/.local/share/mercator/mercator.db` | SQLite event-log DSN. The directory is created at startup. The container image sets this to `file:/data/mercator.db`. |
| `MERCATOR_API_TOKEN` | generated at startup | Bearer token for `/v1/*`. Set explicitly for operations. |
| `MERCATOR_SECRET_KEY` | none | Master key for stored connection credentials and workload reporting (32+ decoded bytes, hex or base64). A present malformed value stops startup. |
| `MERCATOR_API_URL` | `http://127.0.0.1:8080` | CLI base URL. Falls back to the current context, then the local `serve` address. |
| `MERCATOR_OIDC_ISSUER` | none | OIDC issuer URL for human console login. Setting any `MERCATOR_OIDC_*` variable requires the full set; see [authentication-workspaces.md](authentication-workspaces.md). |
| `MERCATOR_OIDC_CLIENT_ID` | none | OIDC client ID. |
| `MERCATOR_OIDC_CLIENT_SECRET` | none | OIDC client secret. |
| `MERCATOR_OIDC_ALLOWED_DOMAIN` | none | Comma-separated email domains admitted at login (and/or `MERCATOR_OIDC_ALLOWED_EMAILS`). |
| `MERCATOR_OIDC_ALLOWED_EMAILS` | none | Comma-separated email addresses admitted at login. |
| `MERCATOR_SESSION_KEY` | none | Session-cookie signing key (32+ bytes, hex or base64). Required with OIDC. |

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
