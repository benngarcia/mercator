#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-${GITHUB_REF_NAME:-}}"
DIST_DIR="${2:-dist}"
TARGETS="${MERCATOR_RELEASE_TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}"

fail() {
  echo "build-release-archives: $*" >&2
  exit 1
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "missing required command: $1"
  fi
}

checksum() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$@"
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  else
    fail "missing required command: shasum or sha256sum"
  fi
}

if [[ -z "${VERSION}" ]]; then
  fail "usage: scripts/build-release-archives.sh <version> [dist-dir]"
fi

case "${VERSION}" in
  v*) ;;
  *) fail "version must start with v, got ${VERSION}" ;;
esac

need go
need bun
need tar

cd "${ROOT}"
mkdir -p "${DIST_DIR}"

(
  cd web/app
  bun install --frozen-lockfile
  bun run build
)

archives=()
for target in ${TARGETS}; do
  os="${target%/*}"
  arch="${target#*/}"
  if [[ -z "${os}" || -z "${arch}" || "${os}" == "${arch}" ]]; then
    fail "invalid target ${target}; expected os/arch"
  fi

  name="mercator_${VERSION}_${os}_${arch}"
  work_dir="${DIST_DIR}/${name}"
  archive="${DIST_DIR}/${name}.tar.gz"

  rm -rf "${work_dir}" "${archive}"
  mkdir -p "${work_dir}"

  GOOS="${os}" GOARCH="${arch}" go build -trimpath -ldflags="-s -w" -o "${work_dir}/mercator" ./cmd/mercator
  cp README.md LICENSE NOTICE "${work_dir}/"
  tar -C "${DIST_DIR}" -czf "${archive}" "${name}"
  rm -rf "${work_dir}"
  archives+=("${archive}")
done

if [[ "${#archives[@]}" -eq 0 ]]; then
  fail "no release archives were built; MERCATOR_RELEASE_TARGETS resolved to an empty target list"
fi

(
  cd "${DIST_DIR}"
  rm -f checksums.txt
  archive_names=()
  for archive in "${archives[@]}"; do
    archive_names+=("$(basename "${archive}")")
  done
  checksum "${archive_names[@]}" > checksums.txt
)

printf 'built release archives in %s\n' "${DIST_DIR}"
for archive in "${archives[@]}"; do
  printf '%s\n' "${archive}"
done
printf '%s\n' "${DIST_DIR}/checksums.txt"
