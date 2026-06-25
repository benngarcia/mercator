# External Reviewer Packet

Use this packet when asking a staff engineer, prospective user, or open source
developer to evaluate Mercator for public launch. It is intentionally separate
from the maintainer-authored scorecard: a launch proof point must come from a
real public review, trial, integration note, benchmark, or case study.

Mercator is V1 evaluation-ready, not production GA. Reviewers should judge the
public first impression, the ability to try the project quickly, and the
honesty of the maturity boundaries.

## 20-Minute Review Path

1. Read the root `README.md` through `Try It In 5 Minutes`.
2. Watch the README demo, or read the transcript in `docs/assets/README.md`.
3. Run `scripts/smoke-test-fake.sh`, or inspect the latest public CI run after
   the repository is public.
4. Skim `docs/launch/open-source-readiness.md` for the maintainer scorecard.
5. Use the persona checklist below for the review lens that matches you.
6. Submit feedback through the GitHub proof-point issue form after the
   repository is public, or follow `docs/launch/proof-point-template.md` for a
   linked write-up.

Do not include secrets, private hostnames, customer data, unreleased downstream
details, or local machine identifiers in public feedback.

## Staff Engineer Checklist

Evaluate whether the repository earns trust from another infrastructure owner.

| Question | Evidence To Inspect | A+ Bar |
| --- | --- | --- |
| Does the README explain the problem and scope honestly? | `README.md`, `ROADMAP.md`, `docs/production/known-limitations.md` | A new reviewer can say when to use Mercator and when not to. |
| Is the run lifecycle auditable? | `docs/production/workload-run-lifecycle.md`, `docs/production/observability-audit.md` | Placement, events, cleanup, and closure have clear evidence paths. |
| Are security boundaries explicit? | `SECURITY.md`, `docs/production/security-model.md`, `docs/project/threat-model.md` | Public APIs/events avoid secret claims that are not backed by docs/tests. |
| Is release risk controlled? | `docs/project/release-process.md`, `docs/project/package-distribution.md`, `.github/workflows/release.yml` | First release artifacts and checksums have a reproducible path. |

Recommended verdict format:

```md
Staff-engineer verdict: A+ | A | B | not ready
Top reason:
Blocking concern:
Evidence reviewed:
```

## Prospective User Checklist

Evaluate whether you would try Mercator for an auditable container-dispatch
workflow.

| Question | Evidence To Inspect | A+ Bar |
| --- | --- | --- |
| Can you understand the problem in under two minutes? | README opening, demo GIF/WebM, `What It Does` | The value proposition is clear without reading internals. |
| Can you get a deterministic first success? | `scripts/smoke-test-fake.sh`, README quickstart | The fake adapter reaches a closed succeeded run. |
| Can you see the operator experience? | `docs/assets/mercator-runs.png`, demo transcript, console screenshot | Runs, decisions, events, and cleanup are visible. |
| Can you imagine integration work? | SDK happy path in README, `sdk/*/README.md` | The first API/SDK path does not require learning idempotency internals. |

Recommended verdict format:

```md
Prospective-user verdict: would try | maybe | would not try
Use case:
What worked:
What blocked confidence:
Evidence reviewed:
```

## Open Source Developer Checklist

Evaluate whether the project is approachable without weakening infrastructure
safety.

| Question | Evidence To Inspect | A+ Bar |
| --- | --- | --- |
| Are contribution expectations clear? | `CONTRIBUTING.md`, `.github/PULL_REQUEST_TEMPLATE.md` | A contributor knows the evidence expected in a PR. |
| Are safe starter tasks available? | `docs/project/contributor-starter-queue.md`, issue templates | First issues are bounded away from unsafe lifecycle/auth changes. |
| Is project direction understandable? | `ROADMAP.md`, `docs/launch/open-source-readiness.md` | Launch, hardening, later work, and non-goals are separated. |
| Is security reporting handled responsibly? | `SECURITY.md`, `.github/ISSUE_TEMPLATE/config.yml` | Vulnerability reports are routed privately. |

Recommended verdict format:

```md
OSS-developer verdict: A+ | A | B | not ready
First issue I would pick:
Missing contributor signal:
Evidence reviewed:
```

## What Counts As Launch Proof

A useful proof point must be public and must name the Mercator version or
commit. It can be positive, mixed, or negative; an honest unsuccessful trial is
better launch evidence than maintainer-only optimism.

Counts:

- a public GitHub issue from the proof-point form;
- a linked user trial with commands or screenshots;
- a downstream integration note;
- an external design or security review;
- a reproducible benchmark or evaluation;
- a maintainer-approved public case study.

Does not count:

- private maintainer notes;
- a green private PR check by itself;
- a README claim without reviewer evidence;
- a review that omits the commit or release tested;
- evidence that includes secrets or private customer details.

After a proof point exists, link it from
`docs/launch/open-source-readiness.md`. Link it from the README only if it is
useful to a new evaluator and the author gave permission.
