> **Stale:** written for the audit-first "run broker" positioning and
> predates the 2026-07 repositioning as a warm fleet broker
> ([ADR-0003](../../adr/0003-reposition-as-warm-fleet-broker.md)). Rewrite before use.

# Launch Proof Points

This directory holds launch-facing proof artifacts that are safe to publish with
the repository. A proof point can be a trial report, integration note,
benchmark, external review, or maintainer-approved case study.

Current entries:

- _None yet._ The Docker adapter quickstart in the root `README.md` is the
  reproducible first-run reference until a published proof point exists.

There is no checked-in baseline yet, and a maintainer-run evaluation does not
satisfy the external/public proof gate by itself. Before calling the launch A+,
maintainers still need at least one public proof point from a real user trial,
downstream integration note, external review, benchmark, or approved case study
that can be linked from the launch scorecard.

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
