# Governance

Mercator is currently a pre-1.0 project with a small maintainer surface. The
governance model should make project decisions explicit without pretending the
project has a foundation, steering committee, or paid support program.

## Current Maintainer Model

The repository is maintained by its owner until more maintainers are added.
Maintainers are responsible for:

- preserving the project scope and safety boundaries;
- reviewing pull requests and issue triage decisions;
- deciding release timing, release notes, and rollback actions;
- coordinating private security and conduct reports;
- keeping public docs honest about pre-GA limitations.

Maintainer authority is not a promise of immediate response. Support remains
best-effort; see [SUPPORT.md](SUPPORT.md).

## Decision Rules

Mercator decisions should be evidence-driven and operator-oriented:

- Start with the run-broker problem, not a preferred implementation.
- Prefer small, reviewable changes with tests or reproducible docs evidence.
- Keep public behavior, compatibility, and release-risk changes documented.
- Keep secrets, customer details, private hostnames, and downstream private
  context out of public threads.
- Reject changes that make the control plane look more mature than it is.

For code and API compatibility, use
[docs/project/compatibility.md](docs/project/compatibility.md). For release
mechanics, use [docs/project/release-process.md](docs/project/release-process.md).
For security boundaries, use
[docs/project/threat-model.md](docs/project/threat-model.md) and
[docs/production/security-model.md](docs/production/security-model.md).

## What Needs Maintainer Decision

Open an issue or design note and wait for maintainer direction before changing:

- run lifecycle semantics, cleanup disposition, idempotency, or replay behavior;
- workspace authorization, public event visibility, or secret-handling
  boundaries;
- supported adapters, provider credential flows, or external sink wiring;
- release process, package publishing, compatibility policy, or artifact shape;
- roadmap priorities, project scope, or claims about production readiness;
- public proof-point promotion into README or launch scorecard material.

Small docs fixes, typo fixes, test clarifications, and bounded examples can go
straight to a pull request if they do not change project commitments.

## Contribution Decisions

Maintainers may close or redirect work that is outside Mercator's current
scope. Common reasons include:

- hiding Kubernetes, SSH orchestration, or provider-specific control planes
  behind the core run contract;
- adding secret-vault behavior without an explicit design decision;
- expanding package publishing before release provenance and clean-install
  checks exist;
- weakening tests, public-event redaction, cleanup safety, or workspace
  isolation;
- adding broad abstractions before a concrete operator problem needs them.

When rejecting a substantial proposal, maintainers should point to the relevant
roadmap, known limitation, compatibility policy, or design boundary when
possible.

## Dependency Maintenance

Dependabot watches the public launch dependency surface weekly:

- GitHub Actions workflows under `.github/workflows/`;
- Go modules from the root `go.mod`;
- Bun console dependencies under `web/app`.

Dependency update pull requests should go through the same CI and review path
as human-authored changes. Before `v0.1.0`, maintainers should keep automated
update volume conservative, review release-risk changes manually, and prefer
small dependency PRs that can be reverted or skipped without blocking launch.

## Releases

Only maintainers cut releases. Before the first public release, follow
[docs/launch/public-launch-runbook.md](docs/launch/public-launch-runbook.md).
Release decisions should not rely on private CI or maintainer confidence alone:
the release workflow, checksums, release notes, and known limitations must be
reviewable from public artifacts after launch.

## Security And Conduct

Security reports follow [SECURITY.md](SECURITY.md). Conduct concerns follow
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Maintainers may move sensitive
discussion to a private channel before public disclosure. Public issues that
include credentials, private infrastructure details, or vulnerability detail may
be edited, hidden, or closed while the report is handled privately.

## Adding Maintainers

Future maintainers should have a record of useful contributions, sound judgment
around infrastructure safety, and willingness to uphold this governance policy,
the code of conduct, security process, and release bar. Adding a maintainer is
a project-owner decision and should be recorded in a public issue or pull
request when it happens.
