# Add SDK Fake-Adapter Examples

Suggested labels: `good first issue`, `docs`, `sdk`

## Problem

The SDK READMEs show the happy path, event reads, placement-decision reads, and
sink-status reads. A new SDK evaluator would still benefit from small
copy-paste example files that run against `scripts/smoke-test-fake.sh` style
local state without needing Docker, RunPod, package registries, or billable
compute.

## Acceptance Criteria

- Add minimal examples for TypeScript, Python, and Ruby that run one fake
  adapter image, wait for closure, print outcome/exit code, then read events
  and the placement decision.
- Keep examples source-install friendly; do not assume SDK packages are
  published.
- Keep token and workspace values sample-only (`dev-token`, `ws_1`, or
  documented equivalents).
- Document the command sequence needed to start Mercator and run each example.
- Add or update tests only if the example code can be exercised without network
  or provider credentials.

## Relevant Docs

- `README.md`
- `sdk/typescript/README.md`
- `sdk/python/README.md`
- `sdk/ruby/README.md`
- `docs/production/fake-eval-path.md`
- `docs/project/package-distribution.md`
