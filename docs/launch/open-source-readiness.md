# Open Source Launch Readiness

Date: 2026-06-25

This scorecard evaluates the repo as a public first impression. It is not a
production readiness claim; production hardening remains tracked in
`docs/production/known-limitations.md`.

## Current Grade After This Slice

| Area | Evidence | Grade | A+ Gap |
| --- | --- | --- | --- |
| README explains the problem | Root README now leads with the compute-dispatch problem, why a broker exists, a Docker quickstart, screenshots, demo video link, docs map, and maturity stance. | A | Add public CI/release badges once the repo is public and the first public run exists. |
| New-user likelihood to try | The Docker quickstart needs only Go 1.25+, a running Docker daemon, and `jq`; the README quickstart gives a copy-paste first run against a digest-pinned image; CLI help works before server configuration; CLI hides run IDs and idempotency on the happy path; SDK docs show run, event, decision, and sink status reads; the README now routes evaluators across the Docker and RunPod paths with requirements, start docs, and provider examples; the CLI reference now has copy-paste follow-up commands, JSON error examples, and an exit-code reference; the OpenAPI reference now maps route families, auth boundaries, and a first HTTP integration path; the Docker adapter runbook shows OpenAPI smoke commands and a sanitized run transcript; package/distribution plan names source, archive, SDK install paths, per-OS checksum verification, and archive troubleshooting. | A | Publish binaries so users do not need a source checkout. |
| Staff-engineer trust | Production docs, known limitations, security model, threat model, contribution bar, governance policy, code of conduct, support policy, dependency update policy, GitHub repository settings checklist, Apache-2.0 license, CI/release workflows, local release archive builder, curated `v0.1.0` release notes, launch audit script, pre-public exposure review, compatibility policy, a concrete external-sink hardening issue draft, and explicit pre-GA status are present. | A | Public CI history, tagged releases, configured branch/security settings, and one external security/design review. |
| OSS contributor path | `CONTRIBUTING.md`, `GOVERNANCE.md`, `CODE_OF_CONDUCT.md`, `SUPPORT.md`, question/bug/feature/proof issue templates, PR template, roadmap, security policy, five launch-ready starter issue drafts, and public issue-conversion commands are checked in. | A | Convert starter queue entries into labeled GitHub issues after the repo is public. |
| Assets | Three console screenshots plus `docs/assets/mercator-demo.webm`, a README-linked GIF fallback, a text demo transcript, and screenshot capture notes are tracked in `docs/assets/`. | A | Optional post-launch polish: add a longer narrated demo from the shot list. |
| Social proof | Repo has durable verification docs, a real operator-oriented runbook set including the Docker adapter operation runbook, and a public proof-point intake template. | B+ | Add a real public user story, integration note, benchmark, external review, or maintainer-approved case study. |

Overall current launch grade: **A**. The repo is credible and tryable in the
private PR state, but an A+ public launch still needs public visibility,
configured repository settings, public CI/release evidence, actual release
artifacts, and at least one real external proof point. The remaining
permission-bound steps are sequenced in
`docs/launch/public-launch-runbook.md`.

## Current GitHub Evidence

- Draft PR: `https://github.com/benngarcia/mercator/pull/7`
- Current CI evidence should be read from the PR checks. During this launch-prep
  session, Go, SDKs, and Console jobs were observed green on the PR branch.
- Repository visibility at evaluation time: private. This means the CI run is
  useful launch-prep evidence, but not public social proof yet.
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
  modules, TypeScript SDK npm dependencies, Bun console dependencies, and Ruby
  SDK Bundler dependencies on a conservative weekly cadence. CI and release
  workflows pin Node 24-compatible major versions of the official GitHub
  checkout/setup actions to avoid stale-runtime warnings before public launch.
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

- Need public CI run history and release artifacts.
- Need branch protection, security reporting, and workflow permissions verified
  in the public repository settings.
- Need the launch-prep PR merged or otherwise promoted to the default branch.
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
- RunPod provider examples are documented without assuming SDK registry
  publishing or synthetic image digests will work on a real provider.
- Console screenshots and the short WebM demo make the run/decision experience
  concrete.
- The demo has a GIF fallback and text transcript.
- The Docker adapter runbook shows the expected output and environment for a
  reproducible first run.
- `mercator --help` works before the user has configured a server URL.
- SDK happy path shows the intended integration shape.

Concerns before A+:

- No binary release yet.
- Docker and RunPod evaluation still require local/provider setup and a source
  checkout until release artifacts exist.

Grade: **A-** for local evaluation, **B+** for immediate production adoption.

### Open Source Developer

Likely reaction: "I can see how to contribute without guessing maintainer
expectations."

Strengths:

- Contribution guide names checks, behavior-risk areas, and docs update rules.
- Issue/PR templates guide useful reports.
- Starter queue identifies five bounded `good first issue` and `help wanted`
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
- CI/release workflows are configured, but need successful public runs.
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
- [x] API/SDK compatibility policy documented.
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
- [x] CI runs Go test/build, release-archive build, SDK tests, and console build.
- [x] CLI help available without a configured API URL.
- [x] Copy-paste CLI follow-up examples documented.
- [x] CLI JSON error response examples documented.
- [x] CLI exit-code reference documented.
- [x] OpenAPI smoke commands documented.
- [x] OpenAPI route overview documented.
- [x] Starter contributor queue documented.
- [x] Five launch-ready starter issue drafts documented.
- [x] Public starter-issue conversion commands documented.
- [x] SDK package publishing decision made for first public release.
- [x] SDK source-install commands documented.
- [x] SDK event and decision examples documented.
- [x] SDK sink status examples documented.
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
- [ ] Launch-prep PR merged to the default branch.
- [ ] Repository visibility changed from private to public.
- [ ] Launch GitHub repository settings configured.
- [ ] First public CI run is green.
- [ ] Tagged release with binaries/checksums.
- [ ] Downloadable CLI/server artifacts exist.
- [ ] Starter queue converted into labeled public GitHub issues.
- [ ] Public proof point: user story, integration note, benchmark, or case study.

## Longer Demo Video Shot List

The committed `docs/assets/mercator-demo.webm` is a short terminal walkthrough
of the Docker quickstart. For a fuller launch video, target 75-100 seconds:

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
