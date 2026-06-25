# Package And Distribution Plan

This document explains how users can try Mercator now and how packages should
be published after the first public tag. It is a launch-readiness plan, not a
claim that published packages already exist.

## Current Install Path

Until the first release tag exists, install from a source checkout:

```sh
git clone https://github.com/benngarcia/mercator.git
cd mercator

go test ./...
go run ./cmd/mercator serve
```

For the deterministic local evaluation path, use the root README quickstart or
run:

```sh
scripts/smoke-test-fake.sh
```

That command builds a temporary binary, starts the fake adapter, creates one run
through the CLI, and verifies the closed run fields that the README promises.
For the longer walkthrough, see `docs/production/fake-eval-path.md`.

## First Binary Release

The first release should be `v0.1.0` after the launch-prep PR is merged and CI
is green on the default branch.

The release workflow is configured to upload:

- `mercator_v0.1.0_linux_amd64.tar.gz`
- `mercator_v0.1.0_linux_arm64.tar.gz`
- `mercator_v0.1.0_darwin_amd64.tar.gz`
- `mercator_v0.1.0_darwin_arm64.tar.gz`
- `checksums.txt`

Each archive should contain `mercator`, `README.md`, `LICENSE`, and `NOTICE`.
The workflow and local verification both call
`scripts/build-release-archives.sh`.

Example install command after a release exists:

```sh
version=v0.1.0
os=linux
arch=amd64

curl -LO "https://github.com/benngarcia/mercator/releases/download/${version}/mercator_${version}_${os}_${arch}.tar.gz"
curl -LO "https://github.com/benngarcia/mercator/releases/download/${version}/checksums.txt"
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf "mercator_${version}_${os}_${arch}.tar.gz"
sudo install "mercator_${version}_${os}_${arch}/mercator" /usr/local/bin/mercator
```

For macOS, set `os=darwin` and `arch=arm64` or `amd64` as appropriate.

## SDK Distribution

First-launch decision: **do not publish SDK packages for `v0.1.0`**. The first
public launch should publish the Go CLI/server archives only. SDK users should
install from a source checkout until package names, registry ownership,
provenance, and clean-environment install tests are confirmed.

The SDKs are source-installable today:

- TypeScript: `sdk/typescript`
- Python: `sdk/python`
- Ruby: `sdk/ruby`

Recommended future package names:

| Language | Package Name | Registry | Current Status |
| --- | --- | --- | --- |
| TypeScript | `@mercator/sdk` | npm | Private package metadata exists; not published. |
| Python | `mercator-sdk` | PyPI | Build metadata exists; not published. |
| Ruby | `mercator-sdk` | RubyGems | Gemspec exists; not published. |

Before publishing SDK packages:

- Confirm package-name ownership on each registry.
- Add registry publishing tokens through GitHub Actions secrets.
- Add package provenance or signing where the registry supports it.
- Build packages in CI and install them in a clean environment.
- Update root README and language README install commands.

## Release Quality Bar

Do not publish a release unless all of these are true:

- The default branch CI is green.
- `scripts/smoke-test-fake.sh` passes against the source checkout.
- `scripts/build-release-archives.sh v0.0.0-local /tmp/mercator-release-dist`
  builds archives and checksums successfully.
- The release workflow has been reviewed on the launch-prep PR.
- `docs/project/release-process.md` has been followed.
- The release notes state that Mercator is pre-1.0 and V1 evaluation-ready, not
  production GA.
- `docs/production/known-limitations.md` is linked from the release notes.
- The archives and checksums can be downloaded and verified.

## Non-Goals For First Launch

- Homebrew, apt, yum, Docker Hub, or container image publishing.
- Automated SDK registry publishing.
- Signed/notarized macOS binaries.
- Windows binaries.

Those are reasonable follow-up issues once the first public release proves the
basic archive workflow.
