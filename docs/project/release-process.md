# Release Process

Mercator does not have a public release yet. This file defines the intended
release mechanics so the first tagged release can be reviewed instead of
invented during launch.

## Release Artifacts

The tag workflow builds:

- `mercator_<version>_linux_amd64.tar.gz`
- `mercator_<version>_linux_arm64.tar.gz`
- `mercator_<version>_darwin_amd64.tar.gz`
- `mercator_<version>_darwin_arm64.tar.gz`
- `checksums.txt`

Each archive contains:

- `mercator`
- `README.md`
- `LICENSE`
- `NOTICE`

SDK package publishing is not part of the first binary release. Until package
publishing is decided, SDK users should install from the repository checkout and
follow the language-specific README. See `docs/project/package-distribution.md`
for the package plan.

## Pre-Tag Checklist

Run locally before creating a tag:

```sh
git status --short --branch
git diff --check
go test ./...
go build ./...

cd web/app && bun install && bun run typecheck && bun run build
cd ../../sdk/typescript && npm ci && npm test
cd ../python && python3 -m unittest discover -s tests
cd ../ruby && bundle install && bundle exec ruby -Ilib:test test/test_client.rb
```

Run the fake-adapter smoke:

```sh
scripts/smoke-test-fake.sh
```

Expected: `outcome=succeeded`, `exit_code=0`, `cleanup=confirmed`,
`closed=true`.

## Tag And Publish

Use an annotated tag:

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

The GitHub Actions release workflow builds archives, writes checksums, and
creates a GitHub Release with generated notes.

## Post-Release Checklist

- Confirm the release workflow passed.
- Download one archive and run `./mercator serve --help` or start the server.
- Verify `checksums.txt` matches the uploaded archives.
- Add the release badge/link to `README.md`.
- Update `docs/launch/open-source-readiness.md` with public CI/release evidence.
- If any SDK package is published, update the matching SDK README and root
  README install section.
