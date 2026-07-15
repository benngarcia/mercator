# Open Source Launch Readiness

Date: 2026-07-15

This scorecard evaluates the repo as a public first impression. It is not a
production readiness claim; production hardening remains tracked in
`docs/production/known-limitations.md`.

## Current Grade After This Slice

| Area | Evidence | Grade | A+ Gap |
| --- | --- | --- | --- |
| README explains the problem | Root README now leads with the compute-dispatch problem, why a broker exists, a Docker quickstart, screenshots, demo video link, docs map, and maturity stance. | A | Keep the quickstart and maturity claims aligned with current behavior. |
| New-user likelihood to try | The Docker quickstart needs only Go 1.25+, a running Docker daemon, and `jq`; the README quickstart gives a copy-paste first run against a digest-pinned image; CLI help works before server configuration; CLI hides run IDs and idempotency on the happy path; the README routes evaluators across the Docker and RunPod paths with requirements, start docs, and provider examples; the CLI reference has copy-paste follow-up commands, JSON error examples, and an exit-code reference; the OpenAPI reference maps route families, auth boundaries, and a first HTTP integration path; the Docker adapter runbook shows OpenAPI smoke commands and a sanitized run transcript; the package/distribution plan names source and archive paths, per-OS checksum verification, and archive troubleshooting. | A | Keep the published binary and container paths current. |
| Staff-engineer trust | Production docs, known limitations, security model, threat model, contribution bar, governance policy, code of conduct, support policy, dependency update policy, GitHub repository settings checklist, Apache-2.0 license, CI/release workflows, local release archive builder, curated release notes, launch audit script, compatibility policy, a concrete external-sink hardening issue draft, and explicit pre-GA status are present. | A | Configure the remaining repository protections and obtain one external security/design review. |
| OSS contributor path | `CONTRIBUTING.md`, `GOVERNANCE.md`, `CODE_OF_CONDUCT.md`, `SUPPORT.md`, question/bug/feature/proof issue templates, PR template, roadmap, security policy, four launch-ready starter issue drafts, and public issue-conversion commands are checked in. | A | Convert starter queue entries into labeled GitHub issues. |
| Assets | Three console screenshots plus `docs/assets/mercator-demo.webm`, a README-linked GIF fallback, a text demo transcript, and screenshot capture notes are tracked in `docs/assets/`. | A | Optional post-launch polish: add a longer narrated demo from the shot list. |
| Social proof | Repo has durable verification docs, a real operator-oriented runbook set including the Docker adapter operation runbook, and a public proof-point intake template. | B+ | Add a real public user story, integration note, benchmark, external review, or maintainer-approved case study. |

Overall current launch grade: **A**. The repository is public, tagged release
artifacts exist, and default-branch CI is green. An A+ still needs the remaining
repository protections and at least one real external proof point. The original
launch sequence remains recorded in `docs/launch/public-launch-runbook.md`.

## Current GitHub Evidence

- Public repository: `https://github.com/benngarcia/mercator`.
- Latest verified default-branch CI run:
  `https://github.com/benngarcia/mercator/actions/runs/29374419800`.
- Tagged releases exist through `v0.2.1`, with archives and checksums published
  through GitHub Releases.
- Public proof collection path: `.github/ISSUE_TEMPLATE/proof_point.yml` and
  `docs/launch/proof-point-template.md` are checked in, but no public proof
  point exists yet.
- Reproducible first run: the README Docker quickstart and
  `docs/production/docker-adapter-operation.md` give a reproducible Docker
  adapter run reaching a closed, succeeded run. It is useful launch evidence,
  but it does not close the external/public proof gate by itself.
- External review packet: `docs/launch/reviewer-packet.md` gives staff
  engineer, prospective-user, and OSS-developer reviewers concrete questions
  and verdict formats. `docs/launch/reviewer-outreach.md` gives maintainers
  ready-to-send request copy for those reviewers.
- Dependency maintenance: `.github/dependabot.yml` checks GitHub Actions, Go
  modules, and Bun console dependencies on a conservative weekly cadence. CI
  and release workflows pin Node 24-compatible major versions of the official
  GitHub checkout/setup actions to avoid stale-runtime warnings.
- Repository settings: `.github/CODEOWNERS` and
  `docs/launch/github-repository-settings.md` document branch protection,
  workflow permissions, external-contributor workflow approval, dependency
  security settings, and private vulnerability reporting before the repository
  is treated as public-launch complete.
- Pre-public exposure review: `docs/launch/pre-public-exposure-review.md`
  gives maintainers a final text, asset, GitHub-surface, and release-surface
  review before making the repository public.

## Persona Evaluation

### Board Of Staff Engineers

Likely reaction: "This is a serious small-control-plane design with unusually
good audit docs for a pre-1.0 project."

Strengths:

- Honest maturity language instead of GA overclaiming.
- Event log, idempotency, workspace auth, cleanup, and public-event redaction
  are visible in docs and tests.
- Dependency maintenance is explicit instead of relying on ad hoc maintainer
  memory after the repository becomes public.
- Launch-time GitHub settings are explicit, including branch protection,
  code-owner review, Actions permissions, and private vulnerability reporting.
- The root README now gives a clear problem statement and the docs map points
  to operational evidence.
- External reviewer packet gives a concise staff-engineer checklist.

Concerns before A+:

- Need branch protection, security reporting, and workflow permissions verified
  in the public repository settings.
- Need external review of the checked-in threat model.

Grade: **A**.

### Prospective User Persona

Persona: an infra or ML platform engineer who has local Docker, occasional GPU
provider usage, and wants an auditable alternative to bespoke dispatch scripts.

Likely reaction: "I can try this quickly and understand why it exists."

Strengths:

- Docker quickstart gives a successful run with only a local Docker daemon.
- README evaluation ladder explains when to stay on local Docker or move to
  RunPod.
- RunPod provider examples use ordinary HTTP and do not assume synthetic image
  digests will work on a real provider.
- Console screenshots and the short WebM demo make the run/decision experience
  concrete.
- The demo has a GIF fallback and text transcript.
- The Docker adapter runbook shows the expected output and environment for a
  reproducible first run.
- `mercator --help` works before the user has configured a server URL.
- The HTTP/OpenAPI example shows the intended integration shape.

Concerns before A+:

- Docker evaluation still requires a local daemon, while RunPod evaluation
  requires provider setup and billable capacity.
- Mercator remains pre-GA infrastructure without a support SLA.

Grade: **A-** for local evaluation, **B+** for immediate production adoption.

### Open Source Developer

Likely reaction: "I can see how to contribute without guessing maintainer
expectations."

Strengths:

- Contribution guide names checks, behavior-risk areas, and docs update rules.
- Issue/PR templates guide useful reports.
- Starter queue identifies four bounded `good first issue` and `help wanted`
  candidates without steering newcomers into run-safety-critical code first.
- Proof-point issue form gives users a structured way to share trials,
  integration notes, benchmarks, or external reviews.
- External reviewer packet gives contributors a clear way to judge the launch
  surface before opening feedback.
- Reviewer outreach prompts make external proof collection reproducible after
  the repository is public.
- Code of conduct gives maintainers a public baseline for contributor behavior
  and private reporting of sensitive conduct concerns.
- Support policy tells evaluators where to ask questions, how to provide useful
  evidence, and what must stay out of public issues.
- The question issue form gives first-run evaluators a structured path before
  they know whether they have found a bug.
- Governance policy explains maintainer decision rules, safety boundaries, and
  which changes need maintainer direction before implementation.
- Security policy avoids public vulnerability disclosure by default.
- Roadmap separates launch polish, production hardening, later work, and
  non-goals.

Concerns before A+:

- No labeled beginner issues yet.
- Repository settings are documented, but branch protection and security
  settings still need to be configured on the public repository.
- Starter queue still needs to be converted into real GitHub issues after the
  repository is public.

Grade: **A**.

## A+ Launch Checklist

- [x] Problem-first README with quickstart.
- [x] Screenshots committed under `docs/assets/`.
- [x] License, notice, security policy, contribution guide, roadmap.
- [x] Code of conduct documented.
- [x] Support policy documented.
- [x] Question issue template documented.
- [x] Governance policy documented.
- [x] Dependency update policy documented.
- [x] GitHub issue templates and PR template.
- [x] CI workflow added.
- [x] Node 24-compatible workflow action pins documented.
- [x] Release workflow and release process documented.
- [x] HTTP/CLI compatibility policy documented.
- [x] Threat model documented.
- [x] Package/install story documented.
- [x] Release checksum verification commands documented for macOS and Linux.
- [x] Release archive troubleshooting documented.
- [x] Demo video recorded and linked from the README.
- [x] README GIF fallback generated from the demo video.
- [x] Text transcript added for the README demo.
- [x] Docker adapter run transcript documented.
- [x] Docker/RunPod evaluation ladder documented in the README.
- [x] RunPod provider examples documented.
- [x] Console screenshot capture notes documented.
- [x] Copy-paste Docker quickstart documented for a first run.
- [x] CI runs Go test/build, release-archive build, launch audit, and console build.
- [x] CLI help available without a configured API URL.
- [x] Copy-paste CLI follow-up examples documented.
- [x] CLI JSON error response examples documented.
- [x] CLI exit-code reference documented.
- [x] OpenAPI smoke commands documented.
- [x] OpenAPI route overview documented.
- [x] Starter contributor queue documented.
- [x] Four launch-ready starter issue drafts documented.
- [x] Public starter-issue conversion commands documented.
- [x] Release archive builder reused by CI and release workflow.
- [x] Curated `v0.1.0` release notes checked in and wired into release workflow.
- [x] Open-source launch audit script added and wired into CI.
- [x] Public launch runbook documented.
- [x] Repository settings checklist documented.
- [x] Pre-public exposure review documented.
- [x] Public proof-point intake path documented.
- [x] Reproducible first-run path documented in the Docker adapter runbook.
- [x] External reviewer packet documented.
- [x] External reviewer outreach prompts documented.
- [x] Concrete production hardening issue draft documented.
- [x] Private draft PR CI run is green.
- [x] Launch-prep PR merged to the default branch (PR #7, followed by the
  pre-launch fix PRs #18 and #19).
- [x] Repository visibility changed from private to public.
- [ ] Launch GitHub repository settings configured.
- [x] First public CI run is green (CI on PRs #18/#19 and the v0.1.0 Release
  workflow completed successfully).
- [x] Tagged release with binaries/checksums (`v0.1.0`, 2026-07-01: four
  platform archives plus `checksums.txt`, published with the curated notes).
- [x] Downloadable CLI/server artifacts exist (verified: `checksums.txt`
  matches the downloaded archive and the extracted `mercator help` exits 0).
- [ ] Starter queue converted into labeled public GitHub issues.
- [ ] Public proof point: user story, integration note, benchmark, or case study.

Post-release caveat: the `ghcr.io/benngarcia/mercator` package must be set to
public visibility in the GitHub package settings before anonymous
`docker pull` works; the Release workflow pushes it but cannot change
visibility.

## Longer Demo Video Shot List

The committed `docs/assets/mercator-demo.webm` is a short console walkthrough
(runs list, run detail, placement decision, events). For a fuller launch video,
target 75-100 seconds:

1. Show the README quickstart and start the Docker adapter server.
2. Run `go run ./cmd/mercator run create "$IMAGE" -- echo hi`, where `$IMAGE` is
   a digest-pinned reference (mutable tags are rejected).
3. Capture the returned `run_id`, then show `run get` with `outcome`,
   `exit_code`, `cleanup`, and `closed`.
4. Open the console runs page.
5. Open the run detail page and switch to the decision tab.
6. Show public events and the audit decision.
7. End on the docs map and known limitations.

Do not show private tokens, provider credentials, or local machine identifiers.
