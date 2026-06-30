# Add Release Archive Install Smoke Notes

Suggested labels: `good first issue`, `docs`, `release`

## Problem

The release workflow builds archives and checksums, and the package plan
documents install commands. After `v0.1.0` exists, maintainers should capture a
clean install smoke from a downloaded archive so users can trust the binary
path instead of only the source checkout path.

## Acceptance Criteria

- Download one published `v0.1.0` archive and `checksums.txt` from GitHub
  Releases.
- Verify the checksum using the platform-appropriate command.
- Extract the archive and run `mercator --help`.
- Start a local fake-adapter server from the archive binary or document why the
  source checkout smoke remains the only supported path.
- Record sanitized commands and output in release docs or launch proof notes.
- Do not mark the release artifact gate complete until the archive was
  downloaded from the public release, not built locally.

## Relevant Docs

- `docs/project/package-distribution.md`
- `docs/project/release-process.md`
- `docs/project/release-notes/v0.1.0.md`
- `docs/launch/public-launch-runbook.md`
