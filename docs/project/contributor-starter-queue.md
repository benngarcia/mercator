# Contributor Starter Queue

This queue is for the first public triage pass. It gives maintainers a set of
starter issues that are useful, bounded, and safe for new contributors. When the
repository is public, convert these into GitHub issues with the suggested labels.

## Label Set

Create these labels during public triage:

| Label | Purpose |
| --- | --- |
| `good first issue` | Small, well-scoped contribution with low product risk. |
| `help wanted` | Maintainers want external help and can review the result. |
| `docs` | Documentation-only change. |
| `cli` | CLI behavior or reference docs. |
| `sdk` | SDK examples, tests, or docs. |
| `console` | Embedded operator console polish. |
| `release` | Release packaging, checksums, and install docs. |
| `needs-maintainer-input` | Blocked on a project decision before implementation. |
| `launch` | Open source launch preparation and evidence. |
| `proof-point` | Public trial, integration note, benchmark, review, or case study. |

## Starter Issues

### 1. Record A Longer Launch Demo From The Shot List

Suggested labels: `good first issue`, `docs`, `console`

Problem: The README has a short demo and transcript, but the launch scorecard
also includes a 75-100 second shot list that could show the full fake-adapter
evaluation path.

Acceptance criteria:

- Follow the shot list in `docs/launch/open-source-readiness.md`.
- Keep the video free of private tokens, hostnames, and local machine
  identifiers.
- Add the selected video under `docs/assets/` or document why it should stay
  externally hosted.
- Include either captions or a text transcript.
- Do not remove the existing short WebM/GIF demo.

### 2. Expand The Fake Adapter Evaluation Transcript

Suggested labels: `good first issue`, `docs`

Problem: `docs/production/fake-eval-path.md` gives commands but could show one
complete transcript of the expected JSON fields.

Acceptance criteria:

- Add a short transcript block for `scripts/smoke-test-fake.sh`.
- Include `run_id`, `outcome`, `exit_code`, `cleanup`, and `closed`.
- Keep generated IDs as `run_...`; do not commit local machine-specific paths or
  ports.
- Run `scripts/smoke-test-fake.sh`.

### 3. Add Console Screenshot Capture Notes

Suggested labels: `good first issue`, `docs`, `console`

Problem: Launch screenshots are checked in, but future contributors need a
repeatable way to refresh them.

Acceptance criteria:

- Add capture guidance to `docs/assets/README.md`.
- Name the required fake-adapter setup and which console screens to capture.
- Keep raw local captures in ignored `output/` until selected.
- Do not replace existing screenshots unless the new ones are clearer.

### 4. Triage A Production Hardening Issue

Suggested labels: `help wanted`, `needs-maintainer-input`

Problem: Some production hardening items need maintainer direction before
implementation, especially registry credentials and external sink configuration.

Acceptance criteria:

- Pick one item from `docs/production/known-limitations.md`.
- Open a design issue that states the problem, non-goals, proposed first slice,
  and acceptance criteria.
- Do not implement the behavior in the same starter issue.

## Maintainer Rules

- Do not label run lifecycle, auth, secret handling, cleanup, or provider launch
  behavior as `good first issue` unless the acceptance criteria are already
  narrow and a maintainer is ready to review closely.
- Prefer docs, examples, screenshots, SDK README improvements, and release
  verification notes for first-time contributors.
- Convert this queue into real issues only after the launch-prep PR is merged
  and the repository is public.
