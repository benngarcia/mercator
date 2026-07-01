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
  --full   Also build the release archives and verify their checksums.
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
  # -E UTF-8:UTF-8 keeps file reads UTF-8 even under POSIX/C locales, where
  # Ruby would otherwise fail on non-ASCII bytes in the docs.
  if ruby -E UTF-8:UTF-8 <<'RUBY'
files = (
  `git ls-files '*.md'`.lines.map(&:chomp) +
  `git ls-files --others --exclude-standard '*.md'`.lines.map(&:chomp)
).uniq.select { |file| File.file?(file) }
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
  if ruby -E UTF-8:UTF-8 <<'RUBY'
files = %w[
  README.md
  CONTRIBUTING.md
  CODE_OF_CONDUCT.md
  SECURITY.md
  SUPPORT.md
  GOVERNANCE.md
  ROADMAP.md
]
files.concat(Dir["docs/{launch,project,production}/**/*.md"].sort)
pattern = /\b(TODO|TBD|FIXME|XXX)\b|lorem ipsum/i
matches = []

files.each do |file|
  next unless File.file?(file)

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

check_internal_artifacts_absent() {
  local found
  found="$(while IFS= read -r path; do
    if [[ -e "${path}" ]]; then
      printf '%s\n' "${path}"
    fi
  done < <(git ls-files docs/superpowers docs/long-horizon))"

  if [[ -n "${found}" ]]; then
    record_fail "internal agent planning artifacts are not tracked under docs/"
  else
    record_ok "internal agent planning artifacts are not tracked under docs/"
  fi
}

check_required_files() {
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
    .github/PULL_REQUEST_TEMPLATE.md
    .github/ISSUE_TEMPLATE/bug_report.yml
    .github/ISSUE_TEMPLATE/feature_request.yml
    .github/ISSUE_TEMPLATE/question.yml
    .github/ISSUE_TEMPLATE/proof_point.yml
    .github/ISSUE_TEMPLATE/config.yml
    .github/CODEOWNERS
    .github/dependabot.yml
    .github/workflows/ci.yml
    .github/workflows/release.yml
    docs/assets/README.md
    docs/launch/open-source-readiness.md
    docs/launch/pre-public-exposure-review.md
    docs/launch/proof-points/README.md
    docs/launch/github-repository-settings.md
    docs/launch/public-launch-runbook.md
    docs/launch/proof-point-template.md
    docs/launch/reviewer-packet.md
    docs/launch/reviewer-outreach.md
    docs/project/compatibility.md
    docs/project/contributor-starter-queue.md
    docs/project/issue-drafts/docker-eval-transcript.md
    docs/project/issue-drafts/external-sink-configuration.md
    docs/project/issue-drafts/longer-launch-demo.md
    docs/project/issue-drafts/release-archive-install-smoke.md
    docs/project/issue-drafts/sdk-fake-adapter-examples.md
    docs/project/package-distribution.md
    docs/project/release-process.md
    docs/project/release-notes/v0.1.0.md
    docs/project/threat-model.md
    docs/production/known-limitations.md
    docs/production/docker-adapter-operation.md
    docs/production/runpod.md
    docs/production/security-model.md
    docs/production/workload-run-lifecycle.md
    docs/reference/cli.md
    docs/reference/openapi.md
    examples/runpod/README.md
    examples/runpod/busybox-report/README.md
    examples/runpod/python-sdk/README.md
    examples/runpod/python-sdk/run.py
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
  require_pattern README.md 'docs/launch/' "README points to the launch/process docs directory"
  require_pattern README.md 'examples/runpod/README\.md' "README links RunPod examples"
  require_pattern README.md 'CODE_OF_CONDUCT\.md' "README links code of conduct"
  require_pattern README.md 'SUPPORT\.md' "README links support policy"
  require_pattern README.md 'GOVERNANCE\.md' "README links governance policy"
}

check_workflow_hooks() {
  require_pattern .github/workflows/ci.yml 'actions/checkout@v5' "CI uses Node 24-compatible checkout action"
  require_pattern .github/workflows/ci.yml 'actions/setup-go@v6' "CI uses Node 24-compatible setup-go action"
  require_pattern .github/workflows/ci.yml 'actions/setup-node@v6' "CI uses Node 24-compatible setup-node action"
  require_pattern .github/workflows/ci.yml 'actions/setup-python@v6' "CI uses Node 24-compatible setup-python action"
  require_pattern .github/workflows/release.yml 'actions/checkout@v5' "Release uses Node 24-compatible checkout action"
  require_pattern .github/workflows/release.yml 'actions/setup-go@v6' "Release uses Node 24-compatible setup-go action"
  require_pattern .github/workflows/ci.yml 'go test \./\.\.\.' "CI runs Go tests"
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

check_dependency_maintenance() {
  require_pattern .github/CODEOWNERS '^\* @benngarcia$' "CODEOWNERS has default maintainer owner"
  require_pattern .github/dependabot.yml 'package-ecosystem: "github-actions"' "Dependabot checks GitHub Actions"
  require_pattern .github/dependabot.yml 'package-ecosystem: "gomod"' "Dependabot checks Go modules"
  require_pattern .github/dependabot.yml 'package-ecosystem: "npm"' "Dependabot checks TypeScript SDK npm dependencies"
  require_pattern .github/dependabot.yml 'directory: "/sdk/typescript"' "Dependabot checks TypeScript SDK directory"
  require_pattern .github/dependabot.yml 'package-ecosystem: "bun"' "Dependabot checks Bun console dependencies"
  require_pattern .github/dependabot.yml 'directory: "/web/app"' "Dependabot checks console directory"
  require_pattern .github/dependabot.yml 'package-ecosystem: "bundler"' "Dependabot checks Ruby SDK dependencies"
  require_pattern .github/dependabot.yml 'directory: "/sdk/ruby"' "Dependabot checks Ruby SDK directory"
  require_pattern .github/dependabot.yml 'open-pull-requests-limit: 3' "Dependabot has conservative PR limits"
  require_pattern GOVERNANCE.md 'Dependency Maintenance' "Governance documents dependency maintenance"
  require_pattern docs/launch/open-source-readiness.md 'Dependency update policy documented' "Scorecard includes dependency update policy"
  require_pattern docs/launch/reviewer-packet.md 'Are dependency updates maintained explicitly\?' "Reviewer packet asks about dependency maintenance"
}

check_launch_docs() {
  require_pattern docs/launch/open-source-readiness.md 'Overall current launch grade: \*\*A\*\*' "Scorecard records current A state"
  require_pattern docs/launch/open-source-readiness.md 'Public proof point: user story, integration note, benchmark, or case study' "Scorecard keeps public proof-point gate open"
  require_pattern docs/launch/open-source-readiness.md 'Code of conduct documented' "Scorecard includes code of conduct"
  require_pattern docs/launch/open-source-readiness.md 'Support policy documented' "Scorecard includes support policy"
  require_pattern docs/launch/open-source-readiness.md 'Question issue template documented' "Scorecard includes question issue template"
  require_pattern docs/launch/open-source-readiness.md 'Governance policy documented' "Scorecard includes governance policy"
  require_pattern docs/launch/open-source-readiness.md 'Pre-public exposure review documented' "Scorecard includes pre-public exposure review"
  require_pattern docs/launch/open-source-readiness.md 'Repository settings checklist documented' "Scorecard includes repository settings checklist"
  require_pattern docs/launch/open-source-readiness.md 'Reproducible first run' "Scorecard documents a reproducible first-run path"
  require_pattern docs/launch/open-source-readiness.md 'Node 24-compatible workflow action pins documented' "Scorecard includes Node 24-compatible workflow action pins"
  require_pattern docs/launch/open-source-readiness.md 'RunPod provider examples documented' "Scorecard includes RunPod examples"
  require_pattern docs/launch/reviewer-packet.md 'Are public repository settings planned\?' "Reviewer packet asks about repository settings"
  require_pattern CONTRIBUTING.md 'CODE_OF_CONDUCT\.md' "Contributing guide links code of conduct"
  require_pattern CONTRIBUTING.md 'GOVERNANCE\.md' "Contributing guide links governance policy"
  require_pattern .github/ISSUE_TEMPLATE/config.yml 'SUPPORT\.md' "Issue template config links support policy"
  require_pattern .github/ISSUE_TEMPLATE/question.yml 'First-run or evaluation question' "Question template covers first-run help"
  require_pattern .github/ISSUE_TEMPLATE/question.yml 'sanitized output' "Question template asks for sanitized output"
  require_pattern SUPPORT.md 'Where To Ask' "Support policy explains where to ask"
  require_pattern SUPPORT.md 'Question issue template' "Support policy routes first-run questions to template"
  require_pattern SUPPORT.md 'Do Not Post Publicly' "Support policy protects sensitive reports"
  require_pattern GOVERNANCE.md 'Decision Rules' "Governance policy explains decision rules"
  require_pattern GOVERNANCE.md 'What Needs Maintainer Decision' "Governance policy names maintainer-decision changes"
  require_pattern CODE_OF_CONDUCT.md 'Report A Conduct Concern' "Code of conduct includes private reporting path"
  require_pattern docs/launch/pre-public-exposure-review.md 'Do not make the repository public' "Exposure review documents hard public-launch stop"
  require_pattern docs/launch/pre-public-exposure-review.md 'gh repo view --json nameWithOwner,visibility,isPrivate,url' "Exposure review includes repository visibility check"
  require_pattern docs/launch/pre-public-exposure-review.md 'dev-token|dev-smoke-token|restore-eval-token' "Exposure review documents allowed sample tokens"
  require_pattern docs/launch/public-launch-runbook.md 'gh repo edit --visibility public --accept-visibility-change-consequences' "Runbook includes visibility change command"
  require_pattern docs/launch/public-launch-runbook.md 'docs/launch/github-repository-settings\.md' "Runbook links repository settings checklist"
  require_pattern docs/launch/public-launch-runbook.md 'docs/launch/pre-public-exposure-review\.md' "Runbook links pre-public exposure review"
  require_pattern docs/launch/public-launch-runbook.md 'gh issue create' "Runbook includes starter issue creation commands"
  require_pattern docs/launch/public-launch-runbook.md 'docs/project/issue-drafts/longer-launch-demo\.md' "Runbook creates longer-demo issue from draft"
  require_pattern docs/launch/public-launch-runbook.md 'docs/project/issue-drafts/sdk-fake-adapter-examples\.md' "Runbook creates SDK examples issue from draft"
  require_pattern docs/launch/public-launch-runbook.md 'docs/project/issue-drafts/docker-eval-transcript\.md' "Runbook creates Docker transcript issue from draft"
  require_pattern docs/launch/public-launch-runbook.md 'docs/project/issue-drafts/release-archive-install-smoke\.md' "Runbook creates release smoke issue from draft"
  require_pattern docs/launch/public-launch-runbook.md 'docs/project/issue-drafts/external-sink-configuration\.md' "Runbook creates external sink issue from draft"
  require_pattern docs/project/contributor-starter-queue.md 'question` \| First-run help or evaluation questions' "Starter queue documents question label"
  require_pattern docs/project/contributor-starter-queue.md 'Five launch-ready issue drafts' "Starter queue names launch-ready issue draft count"
  require_pattern docs/launch/github-repository-settings.md 'Branch Protection' "Repository settings checklist covers branch protection"
  require_pattern docs/launch/github-repository-settings.md 'Require approval for all external contributors' "Repository settings checklist covers external contributor workflow approval"
  require_pattern docs/launch/github-repository-settings.md 'Workflow permissions' "Repository settings checklist covers workflow permissions"
  require_pattern docs/launch/github-repository-settings.md 'Dependabot security updates' "Repository settings checklist covers Dependabot security updates"
  require_pattern docs/launch/github-repository-settings.md 'Private vulnerability reporting' "Repository settings checklist covers private vulnerability reporting"
  require_pattern docs/production/runpod.md 'examples/runpod/' "RunPod docs link provider examples"
  require_pattern docs/production/runpod.md 'SDK registry packages are not published for the first launch' "RunPod docs are honest about SDK package status"
  require_pattern examples/runpod/README.md 'SDK registry packages are not published for the first launch' "RunPod examples index explains SDK package status"
  require_pattern examples/runpod/README.md 'busybox-report' "RunPod examples index links busybox example"
  require_pattern examples/runpod/README.md 'python-sdk' "RunPod examples index links Python SDK example"
  require_pattern examples/runpod/busybox-report/README.md 'no SDK package' "Busybox RunPod example is no-SDK"
  require_pattern examples/runpod/python-sdk/README.md 'git\+https://github\.com/benngarcia/mercator\.git@v0\.1\.0#subdirectory=sdk/python' "Python RunPod example source-installs SDK"
  require_pattern examples/runpod/python-sdk/run.py 'run_reporter' "Python RunPod example uses reporter helper"
  require_pattern docs/launch/proof-points/README.md 'external/public proof gate' "Proof-points index keeps external proof gate open"
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
check_dependency_maintenance
check_launch_docs
check_markdown_links
check_yaml_parse
check_placeholder_markers
check_internal_artifacts_absent

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
