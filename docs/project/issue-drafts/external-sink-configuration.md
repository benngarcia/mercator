# Design External Sink Configuration For `cmd/mercator`

Suggested labels: `help wanted`, `needs-maintainer-input`

## Problem

Mercator has durable sink cursor and replay mechanics, and the internal sink
package already has webhook, Kafka, and Postgres boundaries. The executable
server still wires only the in-process `audit` discard sink, so operators cannot
configure a real external sink from documented process configuration.

Before implementing this, maintainers need to decide the first supported sink
shape and the production failure model. This should be a design issue first,
not a behavior-changing starter implementation.

## Non-Goals

- Do not implement webhook, Kafka, or Postgres wiring in this issue.
- Do not add a sink management UI.
- Do not add a new secret-management system.
- Do not claim external sink delivery is production-ready before the first
  configured sink has an incident runbook and failure tests.

## Proposed First Slice

Design one externally configurable sink for `cmd/mercator`, preferably webhook
delivery because it has the smallest operator dependency surface.

The design should answer:

- configuration names and precedence, for example env vars versus a config file;
- how sink credentials are supplied without writing secret values to events,
  logs, API responses, CLI output, or UI responses;
- retry, timeout, and dead-letter behavior;
- cursor advancement rules after partial failures;
- local Docker-adapter verification commands;
- production deployment notes, including where TLS and outbound network policy
  are enforced.

## Acceptance Criteria

- Pick exactly one first sink target, or explicitly decide that none should be
  first until a separate prerequisite lands.
- State the non-goals and first implementation slice.
- Define the configuration surface and secret-handling boundary.
- Define failure behavior for retryable errors, permanent errors, partial batch
  delivery, and cursor advancement.
- Add or link the tests and docs that an implementation PR must include.
- Keep the issue design-only; do not implement runtime sink behavior in the same
  issue.

## Relevant Docs

- `docs/production/sinks-replay.md`
- `docs/production/known-limitations.md`
- `docs/project/threat-model.md`
