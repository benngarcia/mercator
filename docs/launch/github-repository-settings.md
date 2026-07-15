# GitHub Repository Settings Checklist

Use this checklist after the launch-prep PR lands on the default branch and
before treating the repository as public-launch complete. These settings are
owner-level GitHub controls, so the runbook records what to configure rather
than trying to change them from CI.

Official GitHub references:

- [Branch protection rules](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/managing-a-branch-protection-rule)
- [GitHub Actions repository settings](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository)
- [Dependabot security updates](https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/secure-your-dependencies/configure-security-updates)
- [Private vulnerability reporting](https://docs.github.com/en/code-security/how-tos/report-and-fix-vulnerabilities/configure-vulnerability-reporting/configure-for-a-repository)
- [Code owners](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners)

## Branch Protection

Protect `master` before inviting external contributors:

- Require a pull request before merging.
- Require at least one approval.
- Require review from Code Owners once `.github/CODEOWNERS` is on `master`.
- Require status checks to pass before merging:
  - `Go`
  - `Console`
  - `Image`
- Prefer "Require branches to be up to date before merging" unless it makes
  the first public issue triage too noisy.
- Require conversation resolution before merging.
- Do not allow force pushes.
- Do not allow deletions.
- Record any maintainer bypass exception in the PR or release notes that needed
  it.

## Actions And Fork Pull Requests

In repository Settings, confirm the Actions policy before the repo is public:

- Keep GitHub Actions enabled for the repository.
- Allow only the actions and reusable workflows needed by `.github/workflows/`.
- Set Workflow permissions to read-only by default.
- Keep "Allow GitHub Actions to create and approve pull requests" disabled
  unless a future maintainer decision documents why it is needed.
- Require approval for all external contributors until maintainers explicitly
  relax that setting.
- Do not attach self-hosted runners to public pull-request workflows unless a
  separate threat model covers untrusted workflow execution.

## Dependency And Vulnerability Settings

Before announcing the public launch:

- Confirm `.github/dependabot.yml` is valid in the PR checks.
- Enable dependency graph if it is not already enabled.
- Enable Dependabot alerts.
- Enable Dependabot security updates.
- Keep Dependabot version updates governed by `.github/dependabot.yml`.
- Enable Private vulnerability reporting once the repository is public.
- Confirm the maintainer watches Security alerts for the repository.
- Enable secret scanning and push protection if they are available on the
  repository plan; if they are not available, record that limitation in the
  launch issue or release notes.

## Launch Evidence To Record

After the settings are configured, update
`docs/launch/open-source-readiness.md` with:

- the public repository URL and visibility check;
- the default-branch CI run URL;
- the `v0.1.0` release URL and checksum verification result;
- the starter issues created from `docs/project/contributor-starter-queue.md`;
- the first public proof point when it exists.

Do not mark the launch A+ while branch protection, workflow permissions,
security reporting, release artifacts, public CI, starter issues, or public
proof are still unverified.
