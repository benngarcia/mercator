# Release Process

Mercator does not have a public release yet. This file defines the intended
release mechanics so the first tagged release can be reviewed instead of
invented during launch. For the full public cutover sequence, including repo
visibility, public CI, starter issues, and proof collection, see
`docs/launch/public-launch-runbook.md`.

## Release Artifacts

The tag workflow builds:

- `mercator_<version>_linux_amd64.tar.gz`
- `mercator_<version>_linux_arm64.tar.gz`
- `mercator_<version>_darwin_amd64.tar.gz`
- `mercator_<version>_darwin_arm64.tar.gz`
- `checksums.txt`

Each archive contains:

- `mercator`
- `README.md`
- `LICENSE`
- `NOTICE`

The archive builder is reusable outside GitHub Actions:

```sh
scripts/build-release-archives.sh v0.1.0 dist
```

SDK package publishing is not part of the first binary release. SDK users should
install from the repository checkout until package names, registry ownership,
provenance, and clean-environment install tests are confirmed. See
`docs/project/package-distribution.md` for the package plan.

## Pre-Tag Checklist

Run locally before creating a tag:

```sh
git status --short --branch
git diff --check
scripts/check-open-source-launch.sh
go test ./...
go build ./...
scripts/build-release-archives.sh v0.0.0-local /tmp/mercator-release-dist

cd web/app && bun install && bun run typecheck && bun run build
cd ../../sdk/typescript && npm ci && npm test
cd ../python && python3 -m unittest discover -s tests
cd ../ruby && bundle install && bundle exec ruby -Ilib:test test/test_client.rb
```

Run the Docker adapter smoke against a running Docker daemon (`go test ./...`
above already covers the broker in CI):

```sh
export MERCATOR_ADDR=127.0.0.1:8080
export MERCATOR_SQLITE_DSN='file:/tmp/mercator-demo.db'
export MERCATOR_API_TOKEN='dev-token'
export MERCATOR_AUTH_WORKSPACES='ws_1'
export MERCATOR_DOCKER_ARCH=amd64
go run ./cmd/mercator serve &

export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_WORKSPACE_ID=ws_1
docker pull -q busybox:latest >/dev/null
IMAGE="$(docker inspect --format '{{index .RepoDigests 0}}' busybox:latest)"
RUN_ID="$(go run ./cmd/mercator run create "$IMAGE" -- echo hi | jq -r '.run.id')"
go run ./cmd/mercator run get --run-id "$RUN_ID" | jq '{outcome:.run.outcome, exit_code:.run.exit_code, cleanup:.run.cleanup, closed:.run.closed}'
```

Expected: `outcome=succeeded`, `exit_code=0`, `cleanup=confirmed`,
`closed=true`.

## Tag And Publish

Use an annotated tag:

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The GitHub Actions release workflow builds archives, writes checksums, and
creates a GitHub Release. For `v0.1.0`, the workflow uses the curated notes in
`docs/project/release-notes/v0.1.0.md`; later tags can add a matching
`docs/project/release-notes/<tag>.md` file or fall back to generated notes. The
workflow calls `scripts/build-release-archives.sh`, so local archive
verification and release archive generation share the same implementation.

## Post-Release Checklist

- Confirm the release workflow passed.
- Confirm the published release notes match
  `docs/project/release-notes/v0.1.0.md` and still link known limitations.
- Download one archive and run `./mercator help` (prints CLI usage and exits 0
  with no server, DSN, or Docker daemon required) or start the server.
- Verify `checksums.txt` matches the uploaded archives.
- Add the release badge/link to `README.md`.
- Update `docs/launch/open-source-readiness.md` with public CI/release evidence.
- If any SDK package is published, update the matching SDK README and root
  README install section.
