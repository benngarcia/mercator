# Open Source Launch Readiness

Date: 2026-06-25

This scorecard evaluates the repo as a public first impression. It is not a
production readiness claim; production hardening remains tracked in
`docs/production/known-limitations.md`.

## Current Grade After This Slice

| Area | Evidence | Grade | A+ Gap |
| --- | --- | --- | --- |
| README explains the problem | Root README now leads with the compute-dispatch problem, why a broker exists, a fake-adapter quickstart, screenshots, demo video link, docs map, and maturity stance. | A | Add public CI/release badges once the repo is public and the first public run exists. |
| New-user likelihood to try | Fake adapter quickstart needs only Go and `jq`; `scripts/smoke-test-fake.sh` gives a one-command first run; CLI help works before server configuration; CLI hides run IDs and idempotency on the happy path; package/distribution plan names source, archive, and SDK paths. | A | Publish binaries so users do not need a source checkout. |
| Staff-engineer trust | Production docs, known limitations, security model, threat model, contribution bar, Apache-2.0 license, CI/release workflows, compatibility policy, and explicit pre-GA status are present. | A | Public CI history, tagged releases, and one external security/design review. |
| OSS contributor path | `CONTRIBUTING.md`, issue templates, PR template, roadmap, security policy, and a starter contributor queue are checked in. | A | Convert starter queue entries into labeled GitHub issues after the repo is public. |
| Assets | Three console screenshots plus `docs/assets/mercator-demo.webm` are tracked in `docs/assets/`. | A | Optionally add a compressed GIF to the README if GitHub video rendering is not prominent enough. |
| Social proof | Repo has durable verification docs and a real operator-oriented runbook set. | B- | Add a public user story, integration note, benchmark, or maintainer-approved case study. |

Overall current launch grade: **A**. The repo is credible and tryable in the
private PR state, but an A+ public launch still needs public visibility,
public CI/release evidence, actual release artifacts, and at least one real
external proof point.

## Current GitHub Evidence

- Draft PR: `https://github.com/benngarcia/mercator/pull/7`
- Current CI evidence should be read from the PR checks. During this launch-prep
  session, Go, SDKs, and Console jobs were observed green on the PR branch.
- Repository visibility at evaluation time: private. This means the CI run is
  useful launch-prep evidence, but not public social proof yet.

## Persona Evaluation

### Board Of Staff Engineers

Likely reaction: "This is a serious small-control-plane design with unusually
good audit docs for a pre-1.0 project."

Strengths:

- Honest maturity language instead of GA overclaiming.
- Event log, idempotency, workspace auth, cleanup, and public-event redaction
  are visible in docs and tests.
- The root README now gives a clear problem statement and the docs map points
  to operational evidence.

Concerns before A+:

- Need public CI run history and release artifacts.
- Need the launch-prep PR merged or otherwise promoted to the default branch.
- Need external review of the checked-in threat model.

Grade: **A**.

### Prospective User Persona

Persona: an infra or ML platform engineer who has local Docker, occasional GPU
provider usage, and wants an auditable alternative to bespoke dispatch scripts.

Likely reaction: "I can try this quickly and understand why it exists."

Strengths:

- Fake adapter quickstart gives a successful run without provider setup.
- Console screenshots and the short WebM demo make the run/decision experience
  concrete.
- `mercator --help` works before the user has configured a server URL.
- SDK happy path shows the intended integration shape.

Concerns before A+:

- No binary release yet.
- RunPod and Docker paths still require reading deeper docs before confidence.

Grade: **A-** for local evaluation, **B+** for immediate production adoption.

### Open Source Developer

Likely reaction: "I can see how to contribute without guessing maintainer
expectations."

Strengths:

- Contribution guide names checks, behavior-risk areas, and docs update rules.
- Issue/PR templates guide useful reports.
- Starter queue identifies bounded `good first issue` and `help wanted`
  candidates without steering newcomers into run-safety-critical code first.
- Security policy avoids public vulnerability disclosure by default.
- Roadmap separates launch polish, production hardening, later work, and
  non-goals.

Concerns before A+:

- No labeled beginner issues yet.
- CI/release workflows are configured, but need successful public runs.
- Starter queue still needs to be converted into real GitHub issues after the
  repository is public.

Grade: **A**.

## A+ Launch Checklist

- [x] Problem-first README with quickstart.
- [x] Screenshots committed under `docs/assets/`.
- [x] License, notice, security policy, contribution guide, roadmap.
- [x] GitHub issue templates and PR template.
- [x] CI workflow added.
- [x] Release workflow and release process documented.
- [x] API/SDK compatibility policy documented.
- [x] Threat model documented.
- [x] Package/install story documented.
- [x] Demo video recorded and linked from the README.
- [x] One-command fake-adapter smoke test added and wired into CI.
- [x] CLI help available without a configured API URL.
- [x] Starter contributor queue documented.
- [x] SDK package publishing decision made for first public release.
- [x] Private draft PR CI run is green.
- [ ] Launch-prep PR merged to the default branch.
- [ ] Repository visibility changed from private to public.
- [ ] First public CI run is green.
- [ ] Tagged release with binaries/checksums.
- [ ] Downloadable CLI/server artifacts exist.
- [ ] Starter queue converted into labeled public GitHub issues.
- [ ] Public proof point: user story, integration note, benchmark, or case study.

## Longer Demo Video Shot List

The committed `docs/assets/mercator-demo.webm` is a short console walkthrough.
For a fuller launch video, target 75-100 seconds:

1. Show the README quickstart and start the fake adapter server.
2. Run `go run ./cmd/mercator run create busybox -- echo hi`.
3. Capture the returned `run_id`, then show `run get` with `outcome`,
   `exit_code`, `cleanup`, and `closed`.
4. Open the console runs page.
5. Open the run detail page and switch to the decision tab.
6. Show public events and the audit decision.
7. End on the docs map and known limitations.

Do not show private tokens, provider credentials, or local machine identifiers.
