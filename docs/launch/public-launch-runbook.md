# Public Launch Runbook

This runbook starts after the launch-prep PR is approved. It covers the
permission-bound steps that should not be performed by automation without
maintainer approval: merging the PR, making the repository public, tagging the
first release, configuring GitHub repository settings, converting starter
issues, and collecting public proof.

## Preconditions

- The launch-prep PR is approved by the maintainer.
- The PR branch CI is green for Go, SDKs, and Console.
- The Go CI job includes:
  - `go test ./...`
  - `go build ./...`
  - `scripts/smoke-test-fake.sh`
  - `scripts/build-release-archives.sh v0.0.0-ci /tmp/mercator-release-dist`
- The SDK CI job includes `scripts/check-open-source-launch.sh`.
- The repository owner has decided to make the project public.
- `docs/launch/github-repository-settings.md` has been reviewed for the
  current repository owner, plan, and branch name.
- `docs/launch/pre-public-exposure-review.md` has been run from the default
  branch, and no unresolved findings remain.

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

## 2. Run The Pre-Public Exposure Review

From the synced default branch, run
`docs/launch/pre-public-exposure-review.md`. This covers text scans, launch
assets, GitHub-facing templates/workflows, and release notes.

At minimum, confirm:

```sh
git status --short --branch
gh repo view --json nameWithOwner,visibility,isPrivate,url
scripts/check-open-source-launch.sh
```

Do not continue to visibility changes if the review finds unresolved secrets,
tokens, private hostnames, local machine identifiers, private customer details,
or unpublished downstream implementation details.

## 3. Make The Repository Public

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

## 4. Configure GitHub Repository Settings

Follow `docs/launch/github-repository-settings.md` after the launch-prep PR is
on `master` and the owner has approved the public launch. At minimum, configure
or verify:

- `master` Branch Protection with required PR review, Code Owners review, and
  required `Go`, `SDKs`, and `Console` checks;
- restricted Actions Workflow permissions;
- Require approval for all external contributors before public fork pull-request
  workflows run;
- Dependabot alerts and Dependabot security updates;
- Private vulnerability reporting after the repository is public.

Record plan-specific limitations, such as unavailable secret scanning or push
protection, in the launch issue or release notes.

## 5. Confirm Public Default-Branch CI

Wait for the default-branch CI run created after merge/public launch:

```sh
gh run list --branch master --limit 5
gh run watch <run-id> --exit-status
```

Record the public CI run in `docs/launch/open-source-readiness.md` only after it
has completed successfully.

## 6. Tag And Publish `v0.1.0`

Run the local release checks from a clean default branch:

```sh
git status --short --branch
git diff --check
scripts/check-open-source-launch.sh
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

After the GitHub Release is created, confirm it used the curated release notes
and then download one archive and verify it before announcing the release:

```sh
gh release view v0.1.0 --json url,name,body
```

```sh
version=v0.1.0
os=linux
arch=amd64

curl -LO "https://github.com/benngarcia/mercator/releases/download/${version}/mercator_${version}_${os}_${arch}.tar.gz"
curl -LO "https://github.com/benngarcia/mercator/releases/download/${version}/checksums.txt"
shasum -a 256 -c checksums.txt --ignore-missing
tar -tzf "mercator_${version}_${os}_${arch}.tar.gz" | sort
```

## 7. Convert Starter Queue Into GitHub Issues

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
gh label create question --description "First-run help or evaluation question" --color d876e3
```

Then create issues from the starter queue. Keep acceptance criteria intact so
new contributors can tell when an issue is done.

```sh
cat >/tmp/mercator-issue-launch-demo.md <<'EOF'
Problem:

The README has a short demo and transcript, but the launch scorecard also
includes a 75-100 second shot list that could show the full fake-adapter
evaluation path.

Acceptance criteria:

- Follow the shot list in `docs/launch/open-source-readiness.md`.
- Keep the video free of private tokens, hostnames, and local machine
  identifiers.
- Add the selected video under `docs/assets/` or document why it should stay
  externally hosted.
- Include either captions or a text transcript.
- Do not remove the existing short WebM/GIF demo.
EOF

gh issue create \
  --title "Record a longer launch demo from the shot list" \
  --label "good first issue" \
  --label docs \
  --label console \
  --body-file /tmp/mercator-issue-launch-demo.md
```

```sh
gh issue create \
  --title "Design external sink configuration for cmd/mercator" \
  --label "help wanted" \
  --label "needs-maintainer-input" \
  --body-file docs/project/issue-drafts/external-sink-configuration.md
```

## 8. Collect A Public Proof Point

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
README and proof-point form. For ready-to-send outreach copy, use
`docs/launch/reviewer-outreach.md`.

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

## 9. Post-Launch README Updates

After public CI and `v0.1.0` exist:

- Add public CI and release badges to the README.
- Replace source-only install language with release archive install language.
- Link the release from `docs/project/package-distribution.md`.
- Update `docs/launch/open-source-readiness.md` with the repository settings
  result, public CI run, release URL, converted starter issues, and public proof
  point.

## Rollback Notes

If a release artifact is wrong, do not overwrite the tag silently. Mark the
release as prerelease or withdrawn, document the issue, and publish a patch tag
such as `v0.1.1` after the fix is verified.

If the repository is made public prematurely, make it private again only after
checking whether forks, caches, package indexes, or release artifacts already
exposed the content. Treat exposed secrets or private data as a security
incident, not a docs cleanup.
