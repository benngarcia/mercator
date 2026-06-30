# Pre-Public Exposure Review

Run this review after the launch-prep PR is merged to the default branch and
before making the repository public. It is a human review checklist with exact
commands, not a guarantee that every possible secret pattern can be detected
automatically.

Do not make the repository public if this review finds a real secret, private
customer detail, private hostname, unreleased downstream implementation detail,
or local machine identifier that would still be exposed after publication.

## Inputs

Start from a clean default-branch checkout:

```sh
git status --short --branch
git rev-parse HEAD
gh repo view --json nameWithOwner,visibility,isPrivate,url
scripts/check-open-source-launch.sh
```

Record the commit SHA, repository visibility, reviewer, date, and any findings
in the review note or launch issue used by the maintainer.

## Text Scan

Scan tracked text files for common token, credential, and private-key shapes:

```sh
git ls-files -z \
  | xargs -0 rg -n --hidden \
      -e 'AKIA[0-9A-Z]{16}' \
      -e 'ASIA[0-9A-Z]{16}' \
      -e 'ghp_[A-Za-z0-9_]{30,}' \
      -e 'github_pat_[A-Za-z0-9_]{30,}' \
      -e 'sk-[A-Za-z0-9]{20,}' \
      -e 'xox[baprs]-[A-Za-z0-9-]{20,}' \
      -e '-----BEGIN (RSA |DSA |EC |OPENSSH |)PRIVATE KEY-----' \
      -e 'RUNPOD_API_KEY=' \
      -e 'MERCATOR_API_TOKEN=' \
      -e 'MERCATOR_SECRET_KEY='
```

Expected intentional examples may include `dev-token`, `dev-smoke-token`,
`restore-eval-token`, `RUNPOD_API_KEY` as an environment-variable name, and
`MERCATOR_API_TOKEN` as an environment-variable name. Any other token-shaped
hit needs a human decision before launch.

Also scan launch-facing prose for private context markers:

```sh
rg -n --hidden \
  -e 'customer|client|internal|private hostname|localhost:[0-9]+|/Users/|/home/' \
  README.md CONTRIBUTING.md SECURITY.md ROADMAP.md docs .github scripts
```

This command can produce legitimate hits. The review decision is whether the
hit exposes private information, not whether the word appears.

## Asset Review

Inspect launch assets before publication:

```sh
file docs/assets/mercator-runs.png \
     docs/assets/mercator-run-decision.png \
     docs/assets/mercator-connections.png \
     docs/assets/mercator-demo.webm \
     docs/assets/mercator-demo.gif
```

Open the screenshots and video/GIF. Confirm that they show only Mercator UI,
sample/demo data, sample tokens, `ws_1`, local loopback URLs, and intentional
demo content. Do not publish assets that show private tabs, local usernames,
real provider account identifiers, real run IDs from downstream systems, real
tokens, customer names, or machine hostnames.

## GitHub Surface Review

Review the GitHub-facing files that will become visible immediately:

```sh
sed -n '1,220p' .github/PULL_REQUEST_TEMPLATE.md
sed -n '1,220p' .github/ISSUE_TEMPLATE/bug_report.yml
sed -n '1,220p' .github/ISSUE_TEMPLATE/feature_request.yml
sed -n '1,220p' .github/ISSUE_TEMPLATE/proof_point.yml
sed -n '1,220p' .github/workflows/ci.yml
sed -n '1,220p' .github/workflows/release.yml
```

Confirm that issue forms ask reporters to remove secrets, vulnerability reports
route through `SECURITY.md`, and workflows do not require unpublished secrets
for normal PR CI.

## Release Surface Review

Before tagging `v0.1.0`, review the exact release notes and package docs:

```sh
sed -n '1,220p' docs/project/release-notes/v0.1.0.md
sed -n '1,240p' docs/project/package-distribution.md
sed -n '1,220p' docs/project/release-process.md
```

Confirm that release notes still describe Mercator as pre-GA evaluation
software, link known limitations, and do not imply package registries,
production readiness, or social proof that does not exist yet.

## Decision Template

Use this shape for the maintainer review note:

```md
# Pre-Public Exposure Review

- Commit: <sha>
- Date: <YYYY-MM-DD>
- Reviewer: <name>
- Repository visibility before review: <private/public>
- Commands run: `scripts/check-open-source-launch.sh`, text scan, asset review
- Findings: <none / list>
- Decision: proceed / fix before public launch

## Notes

<Any intentional sample-token hits or follow-up issues.>
```

The launch can proceed only when findings are resolved or explicitly documented
as intentional public examples.
