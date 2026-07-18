# AGENTS.md

## Cursor Cloud specific instructions

Mercator is a single self-contained Go process: a SQLite event log, a JSON HTTP
API + CLI, and an embedded React console — all compiled into one binary. There
is no docker-compose, no separate database server, and no standalone frontend
server. See `README.md` ("Build And Test", "Console") and `CONTRIBUTING.md`
("Development Setup") for the canonical commands; notes below are only the
non-obvious caveats.

### Toolchain / services

- Go 1.25+ (`go.mod` pins `go 1.25.0`); the toolchain auto-downloads if missing.
- Bun (from `mise.toml`, `bun = "1.3"`) is only needed to rebuild the embedded
  console. Bun installs to `~/.bun/bin`; ensure that is on `PATH` before running
  `bun`. The update script installs Bun on startup.
- Docker daemon is a hard requirement for any real end-to-end run: the `serve`
  command always uses the Docker host adapter and launches real containers via
  the `docker` CLI. In this VM `dockerd` is not managed by systemd — start it
  manually (e.g. `sudo dockerd` in a background/tmux session) and it must run
  with `storage-driver: fuse-overlayfs` and the `containerd-snapshotter` feature
  disabled (see `/etc/docker/daemon.json`) for Docker 29 to work here. The
  `docker` CLI must be usable without sudo by the run user (the update script
  loosens `/var/run/docker.sock` permissions if the daemon is already up).

### Build / lint / test / run

- Build console then binary (order matters — `//go:embed` needs fresh
  `web/static`): `cd web/app && bun install && bun run build`, then
  `go build -o mercator ./cmd/mercator`. `mise run build` does both when `mise`
  is installed.
- Lint/typecheck: `go vet ./...` and `cd web/app && bun run typecheck`.
- Tests: `go test ./...` (fast, uses embedded SQLite + a `fake` adapter, no
  Docker needed).
- The opt-in Docker integration test
  (`MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run Integration`)
  hardcodes `arm64` platform and an arm64 alpine digest, so it FAILS on amd64
  hosts. This is a test/platform constraint, not a broker bug — validate the
  Docker path via the README quickstart instead (it is architecture-aware).
- Run the server:
  `MERCATOR_ADDR=127.0.0.1:8080 MERCATOR_API_TOKEN=dev-token MERCATOR_AUTH_WORKSPACES=ws_1 MERCATOR_SQLITE_DSN="file:$HOME/.mercator/mercator.db" ./mercator serve`
  (create `$HOME/.mercator` first). Console is at
  `http://127.0.0.1:8080/?workspace_id=ws_1` — paste the API token when prompted.
- End-to-end smoke test: follow README "Try It In 5 Minutes" step 2 — create a
  run with a digest-pinned image (mutable tags like `busybox:latest` are
  rejected) using the architecture-aware payload, then read the run outcome and
  `/decision`.
