# Support

Mercator is pre-1.0 infrastructure software. Support is best-effort and
evidence-driven: a report with commands, versions, logs, and the adapter path
used is much easier to act on than a general description.

## Where To Ask

- **First-run or evaluation help:** use the Question issue template. Include
  the command you ran, the Mercator commit or release, OS, architecture,
  adapter, and sanitized output.
- **Reproducible bugs:** use the Bug report issue template. Include the
  smallest command sequence that reproduces the behavior.
- **Feature or design proposals:** use the Feature request issue template. State
  the operator problem first, then the smallest useful change.
- **Trial notes, benchmarks, integration notes, or case studies:** use the
  proof-point issue template after the repository is public.
- **Security vulnerabilities:** use the private process in
  [SECURITY.md](SECURITY.md).
- **Conduct concerns:** use the private reporting guidance in
  [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Do Not Post Publicly

Do not put these in public issues, pull requests, screenshots, or proof points:

- provider API keys, registry credentials, bearer tokens, or per-run reporting
  tokens;
- private hostnames, customer names, local machine identifiers, or internal
  repository URLs;
- unreleased downstream implementation details;
- vulnerability details before a maintainer confirms disclosure timing.

If a support request needs that context, open a minimal public issue and ask a
maintainer for a private channel.

## Response Expectations

The project does not yet provide paid support, guaranteed response times, or
production incident coverage. Maintainers prioritize:

1. security and private-data exposure reports;
2. regressions in the fake-adapter quickstart, CLI, SDK examples, or release
   artifacts;
3. bugs with complete reproduction evidence;
4. documentation fixes that help new evaluators complete a first run;
5. scoped feature proposals that fit the project boundaries in
   [CONTRIBUTING.md](CONTRIBUTING.md).

Questions that require Kubernetes, SSH orchestration, provider-specific control
planes, or hidden secret-vault behavior may be closed as out of scope. See
[ROADMAP.md](ROADMAP.md) and
[docs/production/known-limitations.md](docs/production/known-limitations.md)
before opening a request.
