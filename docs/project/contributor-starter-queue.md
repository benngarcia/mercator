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
| `console` | Embedded operator console polish. |
| `release` | Release packaging, checksums, and install docs. |
| `needs-maintainer-input` | Blocked on a project decision before implementation. |
| `launch` | Open source launch preparation and evidence. |
| `proof-point` | Public trial, integration note, benchmark, review, or case study. |
| `question` | First-run help or evaluation questions. |

## Starter Issues

Four launch-ready issue drafts are checked in under
`docs/project/issue-drafts/`. Convert them into public GitHub issues after the
repository is public, keeping the suggested labels and acceptance criteria.

### 1. Record A Longer Launch Demo From The Shot List

Suggested labels: `good first issue`, `docs`, `console`

Problem: The README has a short demo and transcript, but the launch scorecard
also includes a 75-100 second shot list that could show the full Docker adapter
evaluation path.

Acceptance criteria:

- Follow the shot list in `docs/launch/open-source-readiness.md`.
- Keep the video free of private tokens, hostnames, and local machine
  identifiers.
- Add the selected video under `docs/assets/` or document why it should stay
  externally hosted.
- Include either captions or a text transcript.
- Do not remove the existing short WebM/GIF demo.

Issue body: `docs/project/issue-drafts/longer-launch-demo.md`

### 2. Capture A Sanitized Docker Adapter Evaluation Transcript

Suggested labels: `help wanted`, `docs`

Problem: The Docker adapter path still needs a compact public transcript showing
what success looks like on a real local container host.

Acceptance criteria:

- Follow `docs/production/docker-adapter-operation.md` on a disposable local
  Docker host.
- Record commands and successful run evidence: outcome, exit code, cleanup
  disposition, and closed state.
- Include OS, architecture, Docker version, Mercator commit or release, and
  non-secret adapter settings.
- Remove provider credentials, registry credentials, private hostnames, local
  machine identifiers, customer data, and unpublished downstream details.
- Do not change Docker adapter behavior in the same issue.

Issue body: `docs/project/issue-drafts/docker-eval-transcript.md`

### 3. Verify Release Archive Install After `v0.1.0`

Suggested labels: `good first issue`, `docs`, `release`

Problem: After `v0.1.0` is published, the project needs a public install-smoke
note proving that a fresh user can download an archive, verify it, and run the
binary without a source checkout.

Acceptance criteria:

- Download one OS/architecture archive and `checksums.txt` from the public
  release.
- Verify the archive checksum.
- Extract the archive in a temporary directory and run `mercator --help`.
- Record OS, architecture, release URL, checksum command, and observed output.
- Do not overwrite or retag `v0.1.0`.

Issue body: `docs/project/issue-drafts/release-archive-install-smoke.md`

### 4. Open The External Sink Configuration Design Issue

Suggested labels: `help wanted`, `needs-maintainer-input`

Problem: External sink configuration needs maintainer direction before
implementation. The issue draft at
`docs/project/issue-drafts/external-sink-configuration.md` narrows this to the
first `cmd/mercator` sink configuration surface and failure model.

Acceptance criteria:

- Open a GitHub issue from
  `docs/project/issue-drafts/external-sink-configuration.md`.
- Keep the `help wanted` and `needs-maintainer-input` labels.
- Do not implement sink behavior in the same starter issue.

Issue body: `docs/project/issue-drafts/external-sink-configuration.md`

## Maintainer Rules

- Do not label run lifecycle, auth, secret handling, cleanup, or provider launch
  behavior as `good first issue` unless the acceptance criteria are already
  narrow and a maintainer is ready to review closely.
- Prefer docs, examples, screenshots, CLI improvements, and release verification
  notes for first-time contributors.
- Convert this queue into real issues only after the launch-prep PR is merged
  and the repository is public.
