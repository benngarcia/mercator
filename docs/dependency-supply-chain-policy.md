# Dependency Supply-Chain Policy

Mercator keeps runtime dependencies small because every dependency becomes
part of the control-plane binary and release trust boundary.

## Required Review

Before adding or materially upgrading a runtime dependency, record:

- the exact module, version, upstream repository, release tag, and source
  commit;
- the license and any distribution notice required by that license;
- the runtime packages and transitive modules added to the compiled binary;
- the release and maintenance evidence used to choose the version;
- the module checksum and whether the upstream tag or commit is signed;
- `govulncheck` results against the finished repository, including the Go
  toolchain version;
- the data, network, file, process, or credential access the dependency gains;
- the disable and rollback path.

Pin direct dependencies to an exact version in `go.mod` and commit the
`go.sum` checksum. Do not use branches, floating pseudo-versions, or an
unpinned `latest` requirement in release code. Run the normal CI/review gates
after the review; this document records evidence and does not replace them.

## Reviewed Dependency: Sentry Go SDK

Review date: 2026-07-20

- **Module and pin:** [`github.com/getsentry/sentry-go`](https://github.com/getsentry/sentry-go)
  `v0.48.0`, released 2026-07-14 from commit
  [`d02f9bac86b8cbed57ce2023cea5465a7dc47609`](https://github.com/getsentry/sentry-go/commit/d02f9bac86b8cbed57ce2023cea5465a7dc47609).
  The module is a direct exact requirement and requires the same Go 1.25 line
  Mercator already uses.
- **Integrity:** the module archive is pinned by
  `h1:FRZNr7Uk1C86ev1bSJmYlUkL9oyivQA6YOcdYfaaMmY=` and its `go.mod` by
  `h1:E5UkA5wp1qR2+MDydNYlVeUiNN2xEdjYMidkgf0Qoss=`. The Go command verified
  them through `proxy.golang.org` and `sum.golang.org`. The release commit is
  unsigned, so the checksum transparency record and exact source review are
  the integrity controls; a future signed release would reduce this residual
  publisher-authentication risk.
- **Maintenance:** Sentry publishes the SDK from its official organization;
  `v0.48.0` was the latest stable release at review time. The reviewed source
  includes upstream `govulncheck` CI and active work after the release.
- **Compiled dependency surface:** `go list -deps ./cmd/mercator` adds the SDK
  plus upgrades the already-present `golang.org/x/sys` to `v0.45.0` and
  `golang.org/x/text` to `v0.37.0`. The SDK's test-only requirements do not
  enter the compiled Mercator binary.
- **License:** the SDK is MIT licensed. `NOTICE` carries its copyright and full
  permission notice in every release archive. The two Go subrepository modules
  retain their BSD-3-Clause licenses.
- **Vulnerability review:**
  `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` reports no
  vulnerabilities with Go 1.25.12. The first scan on Go 1.25.11 found
  [`GO-2026-5856`](https://pkg.go.dev/vuln/GO-2026-5856) on reachable standard
  library paths and `GO-2026-4970` in an imported package; both are fixed in
  Go 1.25.12, so this change raises Mercator's minimum patch version.
- **Data and network access:** only `internal/sentryreporter` owns the SDK
  client. It sends asynchronous HTTPS events to the configured DSN and builds
  each event from an explicit allow-list. Automatic logs, metrics, telemetry
  buffering, default PII, request bodies, provider bodies, credentials,
  headers, workload environment values, and host names are disabled or never
  supplied. Shutdown flushing is bounded to two seconds.
- **Disable and rollback:** absence of `SENTRY_DSN` constructs no SDK client
  and performs no Sentry network delivery. Rollback is therefore an environment
  change; code rollback removes `internal/sentryreporter`, the broker option,
  and the exact module requirement without changing run records or public
  events.

Review result: accepted with the Go 1.25.12 floor and the unsigned-upstream-tag
risk documented above.
