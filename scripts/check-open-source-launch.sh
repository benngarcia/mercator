#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FULL=0
FAILURES=0
CHECKS=0

usage() {
  cat <<'USAGE'
Usage: scripts/check-open-source-launch.sh [--full]

Checks the repository's open-source launch surface: README sections, launch
docs, public-facing assets, GitHub templates, CI/release workflow hooks, local
Markdown links, and unresolved placeholder markers.

Options:
  --full   Also run the fake-adapter smoke test and release archive builder.
  -h, --help
           Show this help.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --full)
      FULL=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "check-open-source-launch: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
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
  local name="$1"
  if command -v "${name}" >/dev/null 2>&1; then
    record_ok "command available: ${name}"
  else
    record_fail "missing required command: ${name}"
  fi
}

require_file() {
  local path="$1"
  if [[ -f "${path}" ]]; then
    record_ok "file exists: ${path}"
  else
    record_fail "missing file: ${path}"
  fi
}

require_nonempty_file() {
  local path="$1"
  if [[ -s "${path}" ]]; then
    record_ok "non-empty file exists: ${path}"
  else
    record_fail "missing or empty file: ${path}"
  fi
}

require_executable() {
  local path="$1"
  if [[ -x "${path}" ]]; then
    record_ok "executable exists: ${path}"
  else
    record_fail "missing executable: ${path}"
  fi
}

require_pattern() {
  local path="$1"
  local pattern="$2"
  local label="$3"
  if grep -Eq -- "${pattern}" "${path}"; then
    record_ok "${label}"
  else
    record_fail "${label}"
  fi
}

require_readme_heading() {
  local heading="$1"
  if grep -Fxq "${heading}" README.md; then
    record_ok "README has heading: ${heading}"
  else
    record_fail "README missing heading: ${heading}"
  fi
}

check_markdown_links() {
  if ruby <<'RUBY'
files = (
  `git ls-files '*.md'`.lines.map(&:chomp) +
  `git ls-files --others --exclude-standard '*.md'`.lines.map(&:chomp)
).uniq
missing = []

files.each do |file|
  text = File.read(file)
  text.scan(/\[[^\]]+\]\(([^)]+)\)/).flatten.each do |href|
    next if href =~ /\A(?:https?:|mailto:|#)/

    target = href.split('#', 2).first
    target = target.split('?', 2).first
    next if target.empty?

    path = File.expand_path(target, File.dirname(file))
    missing << "#{file}: #{href}" unless File.exist?(path)
  end
end

if missing.empty?
  puts "checked #{files.length} markdown files"
else
  warn missing.join("\n")
  exit 1
end
RUBY
  then
    record_ok "Markdown local links resolve"
  else
    record_fail "Markdown local links resolve"
  fi
}

check_yaml_parse() {
  if ruby <<'RUBY'
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
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c checksums.txt
  else
    return 1
  fi
}

check_placeholder_markers() {
  if ruby <<'RUBY'
files = %w[
  README.md
  CONTRIBUTING.md
  SECURITY.md
  ROADMAP.md
]
files.concat(Dir["docs/{launch,project,production}/**/*.md"].sort)
pattern = /\b(TODO|TBD|FIXME|XXX)\b|lorem ipsum/i
matches = []

files.each do |file|
  File.readlines(file, chomp: true).each_with_index do |line, index|
    matches << "#{file}:#{index + 1}:#{line}" if line.match?(pattern)
  end
end

if matches.empty?
  puts "checked #{files.length} launch-facing docs"
else
  warn matches.join("\n")
  exit 1
end
RUBY
  then
    record_ok "no unresolved placeholder markers in launch-facing docs"
  else
    record_fail "no unresolved placeholder markers in launch-facing docs"
  fi
}

check_required_files() {
  local files=(
    README.md
    LICENSE
    NOTICE
    SECURITY.md
    CONTRIBUTING.md
    ROADMAP.md
    .github/PULL_REQUEST_TEMPLATE.md
    .github/ISSUE_TEMPLATE/bug_report.yml
    .github/ISSUE_TEMPLATE/feature_request.yml
    .github/ISSUE_TEMPLATE/proof_point.yml
    .github/ISSUE_TEMPLATE/config.yml
    .github/workflows/ci.yml
    .github/workflows/release.yml
    docs/assets/README.md
    docs/launch/open-source-readiness.md
    docs/launch/public-launch-runbook.md
    docs/launch/proof-point-template.md
    docs/launch/reviewer-packet.md
    docs/launch/reviewer-outreach.md
    docs/project/compatibility.md
    docs/project/contributor-starter-queue.md
    docs/project/issue-drafts/external-sink-configuration.md
    docs/project/package-distribution.md
    docs/project/release-process.md
    docs/project/release-notes/v0.1.0.md
    docs/project/threat-model.md
    docs/production/known-limitations.md
    docs/production/fake-eval-path.md
    docs/production/docker-adapter-operation.md
    docs/production/runpod.md
    docs/production/security-model.md
    docs/production/workload-run-lifecycle.md
    docs/reference/cli.md
    docs/reference/openapi.md
    sdk/typescript/README.md
    sdk/python/README.md
    sdk/ruby/README.md
  )

  for file in "${files[@]}"; do
    require_file "${file}"
  done

  local assets=(
    docs/assets/mercator-runs.png
    docs/assets/mercator-run-decision.png
    docs/assets/mercator-connections.png
    docs/assets/mercator-demo.webm
    docs/assets/mercator-demo.gif
  )

  for asset in "${assets[@]}"; do
    require_nonempty_file "${asset}"
  done

  require_executable scripts/smoke-test-fake.sh
  require_executable scripts/build-release-archives.sh
  require_executable scripts/check-open-source-launch.sh
}

check_readme_surface() {
  local headings=(
    "# Mercator"
    "## Why It Exists"
    "## What It Does"
    "## Try It In 5 Minutes"
    "## Pick The Right Evaluation Path"
    "## SDK Happy Path"
    "## Console"
    "## Documentation Map"
    "## Build And Test"
    "## Project Status"
    "## Contributing"
    "## License"
  )

  for heading in "${headings[@]}"; do
    require_readme_heading "${heading}"
  done

  require_pattern README.md 'docs/assets/mercator-demo\.webm' "README links demo WebM"
  require_pattern README.md 'docs/assets/mercator-demo\.gif' "README links demo GIF"
  require_pattern README.md 'scripts/smoke-test-fake\.sh' "README links fake smoke test"
  require_pattern README.md 'docs/launch/open-source-readiness\.md' "README links launch scorecard"
  require_pattern README.md 'docs/launch/proof-point-template\.md' "README links proof-point template"
}

check_workflow_hooks() {
  require_pattern .github/workflows/ci.yml 'go test \./\.\.\.' "CI runs Go tests"
  require_pattern .github/workflows/ci.yml 'scripts/smoke-test-fake\.sh' "CI runs fake smoke test"
  require_pattern .github/workflows/ci.yml 'scripts/build-release-archives\.sh v0\.0\.0-ci' "CI builds release archives"
  require_pattern .github/workflows/ci.yml 'npm test' "CI runs TypeScript SDK tests"
  require_pattern .github/workflows/ci.yml 'python -m unittest discover -s tests' "CI runs Python SDK tests"
  require_pattern .github/workflows/ci.yml 'bundle exec ruby -Ilib:test test/test_client\.rb' "CI runs Ruby SDK tests"
  require_pattern .github/workflows/ci.yml 'bun run typecheck' "CI typechecks console"
  require_pattern .github/workflows/ci.yml 'bun run build' "CI builds console"
  require_pattern .github/workflows/release.yml 'scripts/build-release-archives\.sh "\$GITHUB_REF_NAME" dist' "Release workflow builds archives"
  require_pattern .github/workflows/release.yml 'docs/project/release-notes/\$\{GITHUB_REF_NAME\}\.md' "Release workflow checks for curated release notes"
  require_pattern .github/workflows/release.yml '--notes-file "\$notes_file"' "Release workflow uses curated release notes when present"
  require_pattern .github/workflows/release.yml 'gh release create "\$GITHUB_REF_NAME" dist/\* --generate-notes' "Release workflow publishes GitHub release"
}

check_launch_docs() {
  require_pattern docs/launch/open-source-readiness.md 'Overall current launch grade: \*\*A\*\*' "Scorecard records current A state"
  require_pattern docs/launch/open-source-readiness.md 'Public proof point: user story, integration note, benchmark, or case study' "Scorecard keeps public proof-point gate open"
  require_pattern docs/launch/public-launch-runbook.md 'gh repo edit --visibility public --accept-visibility-change-consequences' "Runbook includes visibility change command"
  require_pattern docs/launch/public-launch-runbook.md 'gh issue create' "Runbook includes starter issue creation commands"
  require_pattern docs/launch/proof-point-template.md 'Do not convert private maintainer notes into social proof' "Proof template rejects private social proof"
  require_pattern docs/launch/reviewer-packet.md 'Staff-engineer verdict: A\+ \| A \| B \| not ready' "Reviewer packet includes staff verdict format"
  require_pattern docs/launch/reviewer-outreach.md 'Staff Engineer Review Request' "Reviewer outreach includes staff-engineer request"
  require_pattern docs/launch/reviewer-outreach.md 'Prospective User Trial Request' "Reviewer outreach includes prospective-user request"
  require_pattern docs/launch/reviewer-outreach.md 'Open Source Developer Review Request' "Reviewer outreach includes OSS-developer request"
  require_pattern docs/project/package-distribution.md 'First-launch decision: \*\*do not publish SDK packages for `v0\.1\.0`\*\*' "Package plan documents SDK publishing decision"
  require_pattern docs/project/release-notes/v0.1.0.md 'pre-GA evaluation release' "v0.1.0 release notes are pre-GA honest"
  require_pattern docs/project/release-notes/v0.1.0.md 'docs/production/known-limitations.md' "v0.1.0 release notes link known limitations"
  require_pattern docs/project/release-process.md 'scripts/build-release-archives\.sh v0\.1\.0 dist' "Release process documents archive builder"
  require_pattern docs/project/release-process.md 'docs/project/release-notes/v0\.1\.0\.md' "Release process references curated v0.1.0 notes"
}

run_full_checks() {
  local dist_dir
  dist_dir="$(mktemp -d "${TMPDIR:-/tmp}/mercator-launch-audit.XXXXXX")"

  if scripts/smoke-test-fake.sh; then
    record_ok "full check: fake-adapter smoke test"
  else
    record_fail "full check: fake-adapter smoke test"
  fi

  if scripts/build-release-archives.sh v0.0.0-launch-audit "${dist_dir}"; then
    record_ok "full check: release archive build"
  else
    record_fail "full check: release archive build"
  fi

  if [[ -f "${dist_dir}/checksums.txt" ]] && (cd "${dist_dir}" && verify_checksums >/dev/null 2>&1); then
    record_ok "full check: release archive checksums"
  else
    record_fail "full check: release archive checksums"
  fi

  rm -rf "${dist_dir}"
}

need_command git
need_command ruby

check_required_files
check_readme_surface
check_workflow_hooks
check_launch_docs
check_markdown_links
check_yaml_parse
check_placeholder_markers

if [[ "${FULL}" == "1" ]]; then
  need_command curl
  need_command go
  need_command jq
  need_checksum_command
  need_command tar
  run_full_checks
fi

if [[ "${FAILURES}" == "0" ]]; then
  printf 'open-source launch audit passed (%d checks)\n' "${CHECKS}"
else
  printf 'open-source launch audit failed (%d failures across %d checks)\n' "${FAILURES}" "${CHECKS}" >&2
  exit 1
fi
