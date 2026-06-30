# Launch Proof Points

This directory holds launch-facing proof artifacts that are safe to publish with
the repository. A proof point can be a trial report, integration note,
benchmark, external review, or maintainer-approved case study.

Current entries:

- [Fake adapter baseline evaluation](fake-adapter-baseline.md) - a
  maintainer-run reproducible evaluation of the no-provider fake-adapter path.

The checked-in baseline is useful because it gives reviewers a concrete command,
environment summary, and expected output shape. It does not satisfy the
external/public proof gate by itself. Before calling the launch A+, maintainers
still need at least one public proof point from a real user trial, downstream
integration note, external review, benchmark, or approved case study that can be
linked from the launch scorecard.

## Publication Rules

- Keep evidence public, reproducible, and tied to a Mercator commit or release.
- Remove secrets, private hostnames, customer data, local usernames, and
  unreleased downstream details.
- Include the commands, screenshots, logs, benchmark output, or review notes
  that support the outcome.
- State whether the proof is linkable, quoteable, summary-only, or private.
- Link public proof from
  [open-source-readiness.md](../open-source-readiness.md) only after permission
  and privacy review are explicit.
