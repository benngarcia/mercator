# Remove the Language SDKs

Status: complete on 2026-07-15 in [PR #34](https://github.com/benngarcia/mercator/pull/34)

## Decision

Mercator exposes three interfaces:

- Operators use the CLI or embedded console.
- Integrations use the versioned HTTP interface documented by OpenAPI.
- Workloads use the injected report URL and run token with an ordinary HTTP client.

Mercator no longer maintains language SDKs or reporter libraries. There is no
compatibility shim or deprecation layer because the project is pre-1.0 and the
SDK packages were never published to package registries.

Historical release notes remain accurate. References to SDKs shipped in tagged
releases link to the immutable tag instead of paths removed from the current
tree.

## Execution

- [x] Delete the Python, TypeScript, and Ruby SDK implementations and tests.
- [x] Delete SDK-only examples and issue drafts.
- [x] Remove SDK jobs and ownership from repository automation.
- [x] Replace current SDK documentation with CLI, OpenAPI, and raw HTTP paths.
- [x] Preserve tagged release history with immutable links.
- [x] Verify the repository contains no live SDK promise.
- [x] Run Go tests, Go builds, the launch audit, and real HTTP/reporting flows.
- [x] Push the branch, open a pull request, and resolve CI and review feedback.

The real reporting flow exposed an existing Docker exit-report cleanup defect.
It is tracked separately in [issue #33](https://github.com/benngarcia/mercator/issues/33)
so this deletion remains scoped to the approved interfaces.
