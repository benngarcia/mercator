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

### 1. Add Copy-Paste CLI Examples To The CLI Reference

Suggested labels: `good first issue`, `docs`, `cli`

Problem: `docs/reference/cli.md` explains the command groups but has only a few
examples.

Acceptance criteria:

- Add a short example for `run list`, `run wait`, `run events`, `run decision`,
  and `sink status`.
- Use the existing fake-adapter quickstart environment: `ws_1`, `dev-token`,
  and `http://127.0.0.1:8080`.
- Do not add new CLI flags or behavior.
- Run `go run ./cmd/mercator --help` and `git diff --check`.

### 2. Add Captions Or A Text Transcript For The README Demo

Suggested labels: `good first issue`, `docs`, `console`

Problem: The README has a WebM demo and GIF fallback, but users who cannot or
do not want to watch the animation need a text equivalent.

Acceptance criteria:

- Add a short transcript or caption list under `docs/assets/README.md` or a
  dedicated launch doc.
- Cover the run list, run detail, placement decision, and public events shown
  in the demo.
- Link the transcript near the README demo links.
- Do not remove the WebM or GIF fallback.

### 3. Add SDK Source-Install Examples

Suggested labels: `good first issue`, `docs`, `sdk`

Problem: SDK package registry publishing is intentionally deferred for the first
public launch, so source-install paths should be easy to copy.

Acceptance criteria:

- Update each SDK README with a source-checkout install command:
  TypeScript from `sdk/typescript`, Python from `sdk/python`, Ruby from
  `sdk/ruby`.
- Keep package-registry language future-facing unless packages are actually
  published.
- Run the relevant SDK tests for any README command that executes code.

### 4. Document Release Archive Verification On macOS And Linux

Suggested labels: `good first issue`, `docs`, `release`

Problem: `docs/project/package-distribution.md` shows one checksum command, but
macOS and Linux users may have different defaults.

Acceptance criteria:

- Add macOS and Linux checksum verification examples.
- Keep artifact names aligned with `.github/workflows/release.yml`.
- Do not claim a release exists before the first tag is published.
- Run `git diff --check`.

### 5. Expand The Fake Adapter Evaluation Transcript

Suggested labels: `good first issue`, `docs`

Problem: `docs/production/fake-eval-path.md` gives commands but could show one
complete transcript of the expected JSON fields.

Acceptance criteria:

- Add a short transcript block for `scripts/smoke-test-fake.sh`.
- Include `run_id`, `outcome`, `exit_code`, `cleanup`, and `closed`.
- Keep generated IDs as `run_...`; do not commit local machine-specific paths or
  ports.
- Run `scripts/smoke-test-fake.sh`.

### 6. Add Console Screenshot Capture Notes

Suggested labels: `good first issue`, `docs`, `console`

Problem: Launch screenshots are checked in, but future contributors need a
repeatable way to refresh them.

Acceptance criteria:

- Add capture guidance to `docs/assets/README.md`.
- Name the required fake-adapter setup and which console screens to capture.
- Keep raw local captures in ignored `output/` until selected.
- Do not replace existing screenshots unless the new ones are clearer.

### 7. Add OpenAPI Smoke Commands

Suggested labels: `good first issue`, `docs`

Problem: `/openapi.json` is available but not easy for a new integrator to
inspect from the docs map.

Acceptance criteria:

- Add a short section to `docs/reference/cli.md` or
  `docs/production/fake-eval-path.md` showing `curl /openapi.json`.
- Include a `jq` command that lists paths or schemas.
- Do not regenerate the OpenAPI document.
- Run the fake adapter server or smoke path needed to verify the command.

### 8. Triage A Production Hardening Issue

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
