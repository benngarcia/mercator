# Contributing To Mercator

Mercator is pre-1.0 infrastructure software. Contributions are welcome, but the
bar is intentionally high for behavior that affects run safety, workspace
isolation, credentials, cleanup, or public event data.

## Development Setup

Required tools:

- Go 1.25+
- Bun 1.3+ for the embedded console
- Node 20+ for the TypeScript SDK tests
- Python 3.11+ for the Python SDK tests
- Ruby 3.2+ for the Ruby SDK tests
- Docker only when working on live Docker adapter behavior

Useful local checks:

```sh
go test ./...
go build ./...

cd web/app && bun install && bun run typecheck && bun run build
cd ../../sdk/typescript && npm ci && npm test
cd ../python && python3 -m unittest discover -s tests
cd ../ruby && bundle install && bundle exec ruby -Ilib:test test/test_client.rb
```

The Docker integration test is opt-in:

```sh
MERCATOR_DOCKER_INTEGRATION=1 go test ./internal/adapter/docker -run Integration -count=1
```

## Pull Request Bar

Every PR should answer:

- What user/operator problem does this solve?
- What behavior changed?
- What tests or docs prove the behavior?
- Does this alter run lifecycle, idempotency, cleanup, auth, secrets, or public
  event visibility?
- Does any production doc or SDK README need to change?

For code changes, prefer focused tests near the package being changed. For docs
or launch materials, include the exact command, screenshot, or source path that
backs the claim.

## Project Boundaries

Mercator should stay small:

- One process, one event log, explicit provider adapters.
- OCI workload semantics first.
- Auditable placement and lifecycle decisions.
- No hidden secret vault behavior for workload-owned secrets.
- No Kubernetes, SSH bootstrap, or provider-specific control plane hidden behind
  the core run contract.

Large changes should start with an issue or design note before implementation.
For maintainer decision rules and changes that need explicit approval, see
[GOVERNANCE.md](GOVERNANCE.md).

For first-time contributors, start with the
[Contributor Starter Queue](docs/project/contributor-starter-queue.md). It lists
bounded issues that maintainers can convert into `good first issue` or
`help wanted` tickets after the repository is public.

For questions, evaluation help, and issue-routing expectations, see
[SUPPORT.md](SUPPORT.md).

## Documentation

Update docs in the same PR when behavior changes:

- operator-facing behavior: `docs/production/`
- SDK behavior: `sdk/*/README.md`
- console behavior: `web/app/README.md`
- launch and trust surface: `docs/launch/open-source-readiness.md`
- public proof points: `docs/launch/proof-point-template.md`

Screenshots used in docs should live under `docs/assets/`. Raw local captures
belong in ignored `output/` until they are selected and named.

## Community Conduct

Project spaces are covered by the [Code Of Conduct](CODE_OF_CONDUCT.md). The
short version is: be concrete, respectful, and technical; critique code and
claims rather than people; keep private security or operational details out of
public threads.

Maintainers may close issues or PRs that are abusive, off-topic, spam, or
outside the project scope.
