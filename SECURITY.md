# Security Policy

Mercator handles workload metadata, workspace-scoped API access, adapter
credentials, per-run reporting tokens, and cleanup decisions. Please report
security issues privately.

## Supported Versions

Mercator is pre-1.0. The supported security target is the current `master`
branch until the project starts publishing tagged releases.

## Report A Vulnerability

Preferred path:

1. Open a private GitHub security advisory draft for this repository:
   `https://github.com/benngarcia/mercator/security/advisories/new`
2. Include reproduction steps, affected commit or version, expected impact, and
   any relevant logs or request/response bodies.
3. Do not open a public issue until a maintainer has confirmed disclosure
   timing.

If private advisories are unavailable, contact the maintainer through GitHub and
ask for a private disclosure channel.

## In Scope

- API authentication or workspace isolation bypasses.
- Public event or API responses leaking workload env values, credentials, or
  per-run reporting tokens.
- Adapter behavior that can launch duplicate workloads under one idempotency
  key.
- Cleanup bugs that leave owned compute running unexpectedly or terminate the
  wrong resource.
- Secret-store, credential-resolution, or report-token signing weaknesses.
- Cross-workspace access through the console, CLI, or HTTP API.

## Out Of Scope

- Denial-of-service reports without a plausible security impact.
- Issues requiring physical access to a maintainer machine.
- Vulnerabilities in third-party services unless Mercator configuration or code
  makes the impact materially worse.
- Reports against unsupported local forks without a path back to this repo.

## Disclosure Expectations

Maintainers will acknowledge credible reports, investigate the impact, and
coordinate a fix or mitigation before public disclosure. Because the project is
pre-1.0, release mechanics may change while the security process matures.

For the maintained threat model and launch gates, see
[docs/project/threat-model.md](docs/project/threat-model.md).
