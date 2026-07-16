# Complete the Ponytail Cleanup

Status: complete on 2026-07-15 in PR #43

## Decision

Mercator keeps one provider-extension seam: `broker.Factory` maps an adapter
type to a constructor that builds a fresh adapter from one connection's config
and resolved credential. The unused instance registry is deleted because it
cannot safely represent multiple connections for one provider.

The cleanup preserves the CLI, HTTP interface, embedded console, provider
adapter interface, and workload reporting interface. It does not restore or
replace the deleted language SDKs.

## Execution

- [x] Delete unreachable core abstractions and use standard-library clones.
- [x] Delete unreachable console modules and unused dependencies.
- [x] Make native UI wrappers and the existing theme store own their behavior.
- [x] Reduce the demo recorder to the real Playwright flow.
- [x] Reduce the launch audit to executable evidence.
- [x] Convert the contributor queue into public GitHub issues and delete drafts.
- [x] Run core, console, launch, and real recorder verification.
- [x] Push the branch, open a pull request, and resolve CI and review feedback.
