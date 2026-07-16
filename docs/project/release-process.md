# Release Process

Mercator publishes tagged releases through the release workflow. This file
defines the repeatable mechanics. The original public cutover sequence remains
recorded in `docs/launch/public-launch-runbook.md`.

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

Mercator publishes the Go CLI/server archives and container image. Integrations
use the versioned HTTP interface described by `/openapi.json`.

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
```

Run the root README Docker quickstart against a real daemon. It detects the
host architecture and must close with `outcome=succeeded`, `exit_code=0`,
`cleanup=confirmed`, and `closed=true`. `go test ./...` above covers the broker
in CI; this smoke proves the Docker boundary.

## Tag And Publish

Releasing is always a deliberate act; nothing publishes on ordinary pushes.
There are two equivalent triggers.

**Manual dispatch (preferred).** From the Actions tab, run the `Release`
workflow on the branch to release (normally `master`) with the version as
input, or from a checkout:

```sh
gh workflow run release.yml --ref master -f version=v0.2.0
```

The workflow validates the version (`vMAJOR.MINOR.PATCH`, optional prerelease
suffix), refuses an existing tag, runs the tests, and creates the tag at the
branch head as part of publishing the release.

**Tag push.** Push an annotated tag; the workflow releases exactly that
commit:

```sh
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

Either way the workflow builds archives, writes checksums, creates a GitHub
Release, and publishes the container image to ghcr.io. When
`docs/project/release-notes/<tag>.md` exists it becomes the release notes;
otherwise notes are generated. The workflow calls
`scripts/build-release-archives.sh`, so local archive verification and release
archive generation share the same implementation.

## Post-Release Checklist

- Confirm the release workflow passed.
- Confirm the published release notes match
  `docs/project/release-notes/v0.1.0.md` and still link known limitations.
- Download one archive and run `./mercator help` (prints CLI usage and exits 0
  with no server, DSN, or Docker daemon required) or start the server.
- Verify `checksums.txt` matches the uploaded archives.
- Add the release badge/link to `README.md`.
- Update `docs/launch/open-source-readiness.md` with public CI/release evidence.
