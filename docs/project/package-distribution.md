# Package And Distribution Plan

This document explains how users can install Mercator from source or from its
published release artifacts.

## Current Install Path

To run from a source checkout:

```sh
git clone https://github.com/benngarcia/mercator.git
cd mercator

go test ./...
go run ./cmd/mercator serve
```

For the local evaluation path, use the root README quickstart. It probes the
Docker host architecture, runs a digest-pinned image on the matching platform,
and verifies `outcome=succeeded`, `exit_code=0`, `cleanup=confirmed`, and
`closed=true`. For the longer walkthrough, see
`docs/production/docker-adapter-operation.md`.

## Binary Releases

The release workflow uploads:

- `mercator_v0.1.0_linux_amd64.tar.gz`
- `mercator_v0.1.0_linux_arm64.tar.gz`
- `mercator_v0.1.0_darwin_amd64.tar.gz`
- `mercator_v0.1.0_darwin_arm64.tar.gz`
- `checksums.txt`

Each archive should contain `mercator`, `README.md`, `LICENSE`, and `NOTICE`.
The workflow and local verification both call
`scripts/build-release-archives.sh`.

Example install command:

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

## Distribution Surface

Mercator distributes one Go binary that contains the server, CLI, and embedded
console. Integrations call its OpenAPI-described HTTP interface with an ordinary
HTTP client. The project does not publish or maintain language-specific client
packages.

## Release Quality Bar

Do not publish a release unless all of these are true:

- The default branch CI is green.
- `go test ./...` passes and the Docker quickstart in the root README closes a
  run with `outcome=succeeded` against the source checkout.
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

- Homebrew, apt, yum, or Docker Hub distribution.
- Signed/notarized macOS binaries.
- Windows binaries.

Those are reasonable follow-up issues once demand justifies another
distribution channel.
