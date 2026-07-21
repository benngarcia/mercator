#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FULL=0
FAILURES=0
CHECKS=0

usage() {
  cat <<'USAGE'
Usage: scripts/check-open-source-launch.sh [--full]

Checks structural launch requirements, local Markdown links, GitHub YAML, and
unresolved placeholders. With --full, builds release archives and verifies
their checksums.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --full) FULL=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "check-open-source-launch: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

cd "${ROOT}"

record_ok() {
  CHECKS=$((CHECKS + 1))
  printf 'ok - %s\n' "$1"
}

record_fail() {
  CHECKS=$((CHECKS + 1))
  FAILURES=$((FAILURES + 1))
  printf 'not ok - %s\n' "$1" >&2
}

need_command() {
  if command -v "$1" >/dev/null 2>&1; then
    record_ok "command available: $1"
  else
    record_fail "missing required command: $1"
  fi
}

require_file() {
  if [[ -f "$1" ]]; then
    record_ok "file exists: $1"
  else
    record_fail "missing file: $1"
  fi
}

require_nonempty_file() {
  if [[ -s "$1" ]]; then
    record_ok "non-empty file exists: $1"
  else
    record_fail "missing or empty file: $1"
  fi
}

require_executable() {
  if [[ -x "$1" ]]; then
    record_ok "executable exists: $1"
  else
    record_fail "missing executable: $1"
  fi
}

require_absent() {
  if [[ -e "$1" ]]; then
    record_fail "unexpected path exists: $1"
  else
    record_ok "path is absent: $1"
  fi
}

check_required_paths() {
  local files=(
    README.md
    LICENSE
    NOTICE
    CODE_OF_CONDUCT.md
    SECURITY.md
    SUPPORT.md
    GOVERNANCE.md
    CONTRIBUTING.md
    ROADMAP.md
    .github/workflows/ci.yml
    .github/workflows/release.yml
  )

  for file in "${files[@]}"; do
    require_file "${file}"
  done

  require_nonempty_file docs/assets/mercator-demo.webm
  require_nonempty_file docs/assets/mercator-demo.gif
  require_executable scripts/build-release-archives.sh
  require_executable scripts/check-open-source-launch.sh

  require_absent sdk
  require_absent examples/runpod/python-sdk
  require_absent docs/project/contributor-starter-queue.md
  require_absent docs/project/issue-drafts
}

check_markdown_links() {
  if ruby -E UTF-8:UTF-8 <<'RUBY'
files = (
  `git ls-files '*.md'`.lines.map(&:chomp) +
  `git ls-files --others --exclude-standard '*.md'`.lines.map(&:chomp)
).uniq.select { |file| File.file?(file) }
missing = []

files.each do |file|
  File.read(file).scan(/\[[^\]]+\]\(([^)]+)\)/).flatten.each do |href|
    next if href.match?(/\A(?:https?:|mailto:|#)/)

    target = href.split('#', 2).first.split('?', 2).first
    next if target.empty?

    path = File.expand_path(target, File.dirname(file))
    missing << "#{file}: #{href}" unless File.exist?(path)
  end
end

abort missing.join("\n") unless missing.empty?
puts "checked #{files.length} markdown files"
RUBY
  then
    record_ok "Markdown local links resolve"
  else
    record_fail "Markdown local links resolve"
  fi
}

check_yaml_parse() {
  if ruby -E UTF-8:UTF-8 <<'RUBY'
require "yaml"
files = Dir[".github/**/*.yml", ".github/**/*.yaml"]
files.each { |file| YAML.load_file(file) }
puts "parsed #{files.length} yaml files"
RUBY
  then
    record_ok "GitHub YAML files parse"
  else
    record_fail "GitHub YAML files parse"
  fi
}

check_placeholder_markers() {
  if ruby -E UTF-8:UTF-8 <<'RUBY'
files = %w[
  README.md
  CONTRIBUTING.md
  CODE_OF_CONDUCT.md
  SECURITY.md
  SUPPORT.md
  GOVERNANCE.md
  ROADMAP.md
] + Dir["docs/{launch,project,production}/**/*.md"].sort
pattern = /\b(TODO|TBD|FIXME|XXX)\b|lorem ipsum/i
matches = files.flat_map do |file|
  next [] unless File.file?(file)

  File.readlines(file, chomp: true).filter_map.with_index do |line, index|
    "#{file}:#{index + 1}:#{line}" if line.match?(pattern)
  end
end

abort matches.join("\n") unless matches.empty?
puts "checked #{files.length} launch-facing docs"
RUBY
  then
    record_ok "no unresolved placeholder markers in launch-facing docs"
  else
    record_fail "no unresolved placeholder markers in launch-facing docs"
  fi
}

check_internal_artifacts_absent() {
  if git ls-files docs/superpowers docs/long-horizon | grep -q .; then
    record_fail "internal planning artifacts are absent from docs"
  else
    record_ok "internal planning artifacts are absent from docs"
  fi
}

need_checksum_command() {
  if command -v shasum >/dev/null 2>&1; then
    record_ok "command available: shasum"
  elif command -v sha256sum >/dev/null 2>&1; then
    record_ok "command available: sha256sum"
  else
    record_fail "missing required command: shasum or sha256sum"
  fi
}

verify_checksums() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c checksums.txt
  else
    sha256sum -c checksums.txt
  fi
}

run_full_checks() {
  local dist_dir
  dist_dir="$(mktemp -d "${TMPDIR:-/tmp}/mercator-launch-audit.XXXXXX")"

  if scripts/build-release-archives.sh v0.0.0-launch-audit "${dist_dir}"; then
    record_ok "full check: release archive build"
  else
    record_fail "full check: release archive build"
  fi

  if [[ -f "${dist_dir}/checksums.txt" ]] &&
    (cd "${dist_dir}" && verify_checksums >/dev/null 2>&1); then
    record_ok "full check: release archive checksums"
  else
    record_fail "full check: release archive checksums"
  fi

  rm -rf "${dist_dir}"
}

need_command git
need_command ruby
check_required_paths
check_markdown_links
check_yaml_parse
check_placeholder_markers
check_internal_artifacts_absent

if [[ "${FULL}" == "1" ]]; then
  need_command go
  need_command bun
  need_command tar
  need_checksum_command
  run_full_checks
fi

if [[ "${FAILURES}" == "0" ]]; then
  printf 'open-source launch audit passed (%d checks)\n' "${CHECKS}"
else
  printf 'open-source launch audit failed (%d failures across %d checks)\n' "${FAILURES}" "${CHECKS}" >&2
  exit 1
fi
