# Launch Proof Point Template

Mercator should not claim an A+ public launch from maintainer-written docs
alone. Use this template to collect real public evidence after the repository
is public.

For reviewer prompts by audience, see
`docs/launch/reviewer-packet.md`.

## Accepted Proof Types

At least one of these is required before the launch scorecard can mark social
proof complete:

- user story from a real trial;
- downstream integration note;
- external design or security review;
- small benchmark or reproducible evaluation;
- maintainer-approved case study.

The proof point can be a GitHub issue, a linked write-up, a public review, a
benchmark gist, or a checked-in launch note. It must be public and safe to link.

## Minimum Bar

Every proof point must include:

- what was tested;
- Mercator release tag, commit SHA, or PR branch;
- environment summary: OS, architecture, adapter, provider, and relevant tool
  versions;
- commands, screenshots, logs, benchmark output, or review notes;
- outcome: successful, partially successful, unsuccessful, or review-only;
- permission to quote or link the evidence from the README or launch scorecard;
- confirmation that secrets, private hostnames, customer data, and unpublished
  downstream details were removed.

## Issue Form

After the repository is public, prefer the GitHub issue form:
`Trial, integration note, or case study`.

Use the `proof-point` and `launch` labels. If the issue reports a bug or
design concern, keep it open as normal project feedback and link it from the
launch scorecard only if the submitter grants permission.

## Maintainer Case Study Format

Use this shape for a maintainer-approved case study or checked-in proof note:

```md
# <Short Title>

- Type: user story | integration note | benchmark | external review | case study
- Mercator version or commit: <tag or SHA>
- Date: <YYYY-MM-DD>
- Reviewer or project: <public name or approved description>
- Permission: <linkable / quoteable / summary-only>

## Scenario

What problem was being evaluated? What alternative would the user otherwise
use?

## Setup

OS, architecture, adapter, provider, Mercator configuration, and any relevant
dependency versions. Omit secrets and private infrastructure identifiers.

## Evidence

Commands, screenshots, logs, benchmark output, review notes, or links.

## Outcome

What worked, what did not work, and what follow-up issues were created?

## Privacy Review

State that secrets, private hostnames, customer data, and unpublished
downstream details were removed before publication.
```

## Promotion Checklist

Before linking a proof point from the README:

- Confirm the evidence is public.
- Confirm the proof point names the Mercator version or commit.
- Confirm quote/link permission is explicit.
- Confirm the proof point has no secrets or private identifiers.
- Add the proof point link to `docs/launch/open-source-readiness.md`.
- Add a short README link only after the proof point is public and useful to a
  new evaluator.

Do not convert private maintainer notes into social proof. If evidence is
private, use it to improve the project but keep the A+ social-proof gate open.
