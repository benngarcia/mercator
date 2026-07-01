# External Reviewer Outreach

Use these prompts after the repository is public and the maintainer has chosen
reviewers. They are designed to collect real public evidence without turning
private maintainer notes into social proof.

Each request should include:

- the public repository URL;
- the Mercator commit or release being reviewed;
- the README link;
- the reviewer packet link;
- the proof-point issue form or proof-point template link;
- a reminder to remove secrets, private hostnames, customer data, and
  unpublished downstream details.

## Staff Engineer Review Request

```md
Subject: Mercator launch review request

I am preparing Mercator for a public open-source launch and would value a
staff-engineer review of the repo as infrastructure software.

Please review:

- README: <repo URL>
- Reviewer packet: <repo URL>/blob/<commit>/docs/launch/reviewer-packet.md
- Threat model: <repo URL>/blob/<commit>/docs/project/threat-model.md
- Release process: <repo URL>/blob/<commit>/docs/project/release-process.md

Suggested path: spend about 20 minutes on the README, Docker quickstart,
threat model, known limitations, and release mechanics. A useful verdict can be
positive, mixed, or negative; the goal is honest launch evidence.

Please submit public feedback through the proof-point issue form, or send a
linkable write-up using the proof-point template:
<repo URL>/blob/<commit>/docs/launch/proof-point-template.md

Do not include secrets, private hostnames, customer data, local machine
identifiers, or unpublished downstream details.
```

## Prospective User Trial Request

````md
Subject: Mercator first-run trial request

I am looking for a prospective-user read on Mercator's public first impression.
Could you try the Docker quickstart and tell me whether you would continue
evaluating it for auditable container dispatch?

Please review:

- README: <repo URL>
- 20-minute reviewer path: <repo URL>/blob/<commit>/docs/launch/reviewer-packet.md
- Docker adapter operation: <repo URL>/blob/<commit>/docs/production/docker-adapter-operation.md

The quickstart needs Go 1.25+, a running Docker daemon, and `jq`. In one
terminal start the broker with the Docker adapter:

```sh
export MERCATOR_ADDR=127.0.0.1:8080
export MERCATOR_SQLITE_DSN='file:/tmp/mercator-demo.db'
export MERCATOR_API_TOKEN='dev-token'
export MERCATOR_AUTH_WORKSPACES='ws_1'
export MERCATOR_DOCKER_ARCH=amd64
go run ./cmd/mercator serve
```

In a second terminal create a run with a digest-pinned image (mutable tags are
rejected):

```sh
export MERCATOR_API_URL=http://127.0.0.1:8080
export MERCATOR_API_TOKEN='dev-token'
export MERCATOR_WORKSPACE_ID=ws_1
docker pull -q busybox:latest >/dev/null
IMAGE="$(docker inspect --format '{{index .RepoDigests 0}}' busybox:latest)"
RUN_ID="$(go run ./cmd/mercator run create "$IMAGE" -- echo hi | jq -r '.run.id')"
go run ./cmd/mercator run get --run-id "$RUN_ID" | jq '{outcome:.run.outcome, exit_code:.run.exit_code, cleanup:.run.cleanup, closed:.run.closed}'
```

Useful feedback includes what worked, what blocked confidence, and whether the
README made the problem and next step clear. Please submit feedback through the
proof-point issue form after the repository is public, or use:
<repo URL>/blob/<commit>/docs/launch/proof-point-template.md

Please remove secrets, private hostnames, customer data, local machine
identifiers, and unpublished downstream details before sharing evidence.
````

## Open Source Developer Review Request

```md
Subject: Mercator contributor-readiness review request

I am checking whether Mercator is approachable for outside contributors without
steering them into unsafe lifecycle, auth, or secret-handling work.

Please review:

- README: <repo URL>
- Contributor guide: <repo URL>/blob/<commit>/CONTRIBUTING.md
- Starter queue: <repo URL>/blob/<commit>/docs/project/contributor-starter-queue.md
- Reviewer packet: <repo URL>/blob/<commit>/docs/launch/reviewer-packet.md

I am especially interested in whether the starter issues are bounded, whether
the PR evidence expectations are clear, and whether project direction and
security reporting are easy to understand.

Please submit public feedback through the proof-point issue form, or use:
<repo URL>/blob/<commit>/docs/launch/proof-point-template.md

Do not include secrets, private hostnames, customer data, local machine
identifiers, or unpublished downstream details.
```

## Maintainer Use

Before sending:

1. Replace `<repo URL>` and `<commit>` with the public repository URL and the
   exact commit or release under review.
2. Send only after the launch-prep PR is merged or otherwise promoted to the
   reviewed branch.
3. Link the resulting public issue or write-up from
   `docs/launch/open-source-readiness.md` only if the reviewer grants quote or
   link permission.
4. Keep the A+ social-proof gate open until at least one public proof point
   exists.
