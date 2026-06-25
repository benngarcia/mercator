# Public Launch Runbook

This runbook starts after the launch-prep PR is approved. It covers the
permission-bound steps that should not be performed by automation without
maintainer approval: merging the PR, making the repository public, tagging the
first release, converting starter issues, and collecting public proof.

## Preconditions

- The launch-prep PR is approved by the maintainer.
- The PR branch CI is green for Go, SDKs, and Console.
- The Go CI job includes:
  - `go test ./...`
  - `go build ./...`
  - `scripts/smoke-test-fake.sh`
  - `scripts/build-release-archives.sh v0.0.0-ci /tmp/mercator-release-dist`
- The repository owner has decided to make the project public.
- No secrets, tokens, local machine identifiers, or private customer details are
  present in docs, screenshots, demo video, issues, or release notes.

## 1. Merge Or Promote The Launch-Prep PR

Confirm the PR head and checks:

```sh
gh pr view 7 --json url,isDraft,mergeable,headRefOid,statusCheckRollup
```

If the PR is still a draft, mark it ready only after the maintainer review is
complete:

```sh
gh pr ready 7
```

Merge with the strategy the maintainer wants. For a normal merge commit:

```sh
gh pr merge 7 --merge
```

After merge, sync the default branch:

```sh
git checkout master
git pull --ff-only origin master
```

## 2. Make The Repository Public

This is an owner-level decision. Do it only after the merge is on the default
branch and the owner explicitly approves.

Before changing visibility, re-check:

```sh
gh repo view --json nameWithOwner,visibility,isPrivate,url
git status --short --branch
```

Change visibility in GitHub repository settings, or with the GitHub CLI if the
owner prefers CLI operation:

```sh
gh repo edit --visibility public --accept-visibility-change-consequences
```

Afterward:

```sh
gh repo view --json nameWithOwner,visibility,isPrivate,url
```

## 3. Confirm Public Default-Branch CI

Wait for the default-branch CI run created after merge/public launch:

```sh
gh run list --branch master --limit 5
gh run watch <run-id> --exit-status
```

Record the public CI run in `docs/launch/open-source-readiness.md` only after it
has completed successfully.

## 4. Tag And Publish `v0.1.0`

Run the local release checks from a clean default branch:

```sh
git status --short --branch
git diff --check
go test ./...
go build ./...
scripts/smoke-test-fake.sh
scripts/build-release-archives.sh v0.0.0-local /tmp/mercator-release-dist
```

Verify the local archives:

```sh
cd /tmp/mercator-release-dist
shasum -a 256 -c checksums.txt
for archive in mercator_v0.0.0-local_*.tar.gz; do
  tar -tzf "$archive" | sort
done
```

Create and push the annotated tag:

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Watch the release workflow:

```sh
gh run list --workflow Release --limit 3
gh run watch <run-id> --exit-status
```

After the GitHub Release is created, download one archive and verify it before
announcing the release:

```sh
version=v0.1.0
os=linux
arch=amd64

curl -LO "https://github.com/benngarcia/mercator/releases/download/${version}/mercator_${version}_${os}_${arch}.tar.gz"
curl -LO "https://github.com/benngarcia/mercator/releases/download/${version}/checksums.txt"
shasum -a 256 -c checksums.txt --ignore-missing
tar -tzf "mercator_${version}_${os}_${arch}.tar.gz" | sort
```

## 5. Convert Starter Queue Into GitHub Issues

Use `docs/project/contributor-starter-queue.md` as the source of truth.

Create labels:

```sh
gh label create "good first issue" --description "Small, well-scoped contribution with low product risk" --color 7057ff
gh label create "help wanted" --description "Maintainers want external help and can review the result" --color 008672
gh label create docs --description "Documentation-only change" --color 0075ca
gh label create cli --description "CLI behavior or reference docs" --color 5319e7
gh label create sdk --description "SDK examples, tests, or docs" --color d876e3
gh label create console --description "Embedded operator console polish" --color fbca04
gh label create release --description "Release packaging, checksums, and install docs" --color c2e0c6
gh label create "needs-maintainer-input" --description "Blocked on a project decision before implementation" --color bfdadc
gh label create launch --description "Open source launch preparation and evidence" --color 0e8a16
gh label create "proof-point" --description "Public trial, integration note, benchmark, review, or case study" --color 5319e7
```

Then create issues from the starter queue. Keep acceptance criteria intact so
new contributors can tell when an issue is done.

## 6. Collect A Public Proof Point

Before calling the launch A+, collect at least one public proof point:

- a user story from a real trial;
- an integration note from a real downstream project;
- an external design or security review;
- a small benchmark or reproducible evaluation;
- a maintainer-approved case study.

Use `docs/launch/proof-point-template.md` as the source of truth. After the
repository is public, external users can also submit the checked-in GitHub issue
form named `Trial, integration note, or case study`.
For a concise reviewer prompt, send `docs/launch/reviewer-packet.md` with the
README and proof-point form.

Minimum bar for the proof point:

- It names what was tested.
- It links to the Mercator commit or release.
- It includes commands, screenshots, or review notes.
- It avoids private tokens, machine identifiers, customer data, and unreleased
  downstream implementation details.
- It grants permission to quote or link the evidence from the README or launch
  scorecard.

Record the proof point in `docs/launch/open-source-readiness.md` and link it
from the README only after it is public.

## 7. Post-Launch README Updates

After public CI and `v0.1.0` exist:

- Add public CI and release badges to the README.
- Replace source-only install language with release archive install language.
- Link the release from `docs/project/package-distribution.md`.
- Update `docs/launch/open-source-readiness.md` with the public CI run, release
  URL, converted starter issues, and public proof point.

## Rollback Notes

If a release artifact is wrong, do not overwrite the tag silently. Mark the
release as prerelease or withdrawn, document the issue, and publish a patch tag
such as `v0.1.1` after the fix is verified.

If the repository is made public prematurely, make it private again only after
checking whether forks, caches, package indexes, or release artifacts already
exposed the content. Treat exposed secrets or private data as a security
incident, not a docs cleanup.
