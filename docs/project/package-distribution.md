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
```

For Linux, keep `os=linux` and set `arch=amd64` or `arm64` as appropriate. For
macOS, set `os=darwin` and `arch=arm64` or `amd64`.

Verify the archive checksum before extraction. `checksums.txt` contains all
platform archives, so `--ignore-missing` lets you verify only the archive you
downloaded.

macOS:

```sh
shasum -a 256 -c checksums.txt --ignore-missing
```

Linux:

```sh
sha256sum -c checksums.txt --ignore-missing
```

After the checksum passes:

```sh
tar -xzf "mercator_${version}_${os}_${arch}.tar.gz"
sudo install "mercator_${version}_${os}_${arch}/mercator" /usr/local/bin/mercator
```

### Troubleshooting Release Archives

If the archive install fails after a release exists, check these first:

- **Checksum mismatch:** delete the local `.tar.gz` and `checksums.txt`, then
  download both files again from the same release version. Do not use an archive
  if `shasum -a 256 -c checksums.txt --ignore-missing` or
  `sha256sum -c checksums.txt --ignore-missing` fails.
- **Wrong OS or architecture:** confirm the archive name matches the machine.
  Linux uses `mercator_${version}_linux_amd64.tar.gz` or
  `mercator_${version}_linux_arm64.tar.gz`; macOS uses
  `mercator_${version}_darwin_amd64.tar.gz` or
  `mercator_${version}_darwin_arm64.tar.gz`.
- **Binary is not executable:** run
  `chmod +x "mercator_${version}_${os}_${arch}/mercator"` before copying it, or
  reinstall with
  `sudo install -m 0755 "mercator_${version}_${os}_${arch}/mercator" /usr/local/bin/mercator`.
- **Command not found after install:** check `command -v mercator` and confirm
  the install directory is on `PATH`. If `/usr/local/bin` is not on `PATH`,
  install into another directory that is.
- **Permission denied during install:** install to a user-writable directory,
  for example `mkdir -p "$HOME/.local/bin"` followed by
  `install -m 0755 "mercator_${version}_${os}_${arch}/mercator" "$HOME/.local/bin/mercator"`.

## SDK Distribution

First-launch decision: **do not publish SDK packages for `v0.1.0`**. The first
public launch should publish the Go CLI/server archives only. SDK users should
install from a source checkout until package names, registry ownership,
provenance, and clean-environment install tests are confirmed.

The SDKs are source-installable today:

TypeScript local tarball:

```sh
git clone https://github.com/benngarcia/mercator.git
cd mercator/sdk/typescript
npm ci
npm run build
npm pack --pack-destination /tmp

cd /path/to/your/app
npm install /tmp/mercator-sdk-0.1.0.tgz
```

Python editable install:

```sh
git clone https://github.com/benngarcia/mercator.git
cd mercator
python3 -m pip install -e sdk/python
```

Ruby local path or gem install:

```ruby
# Gemfile
gem "mercator-sdk", path: "/path/to/mercator/sdk/ruby"
```

```sh
cd mercator/sdk/ruby
gem build mercator-sdk.gemspec
gem install ./mercator-sdk-0.1.0.gem
```

See the language READMEs for usage examples:

- TypeScript: `sdk/typescript/README.md`
- Python: `sdk/python/README.md`
- Ruby: `sdk/ruby/README.md`

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
- The release notes come from `docs/project/release-notes/v0.1.0.md` or an
  equivalent tag-specific note that states Mercator is pre-1.0 and V1
  evaluation-ready, not production GA.
- `docs/production/known-limitations.md` is linked from the release notes.
- The archives and checksums can be downloaded and verified.

## Non-Goals For First Launch

- Homebrew, apt, yum, Docker Hub, or container image publishing.
- Automated SDK registry publishing.
- Signed/notarized macOS binaries.
- Windows binaries.

Those are reasonable follow-up issues once the first public release proves the
basic archive workflow.
