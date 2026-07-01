# Documentation Assets

Tracked assets in this directory are selected for launch-facing docs. Raw local
captures stay in ignored `output/` until they are reviewed and renamed.

Current assets:

- `mercator-demo.webm` - ~30-second terminal demo of the README Docker
  quickstart: start the broker in Docker, create a digest-pinned run, read the
  terminal result (`succeeded` / exit code / cleanup), and ask for the placement
  decision. Rendered deterministically from `mercator-demo.tape`.
- `mercator-demo.gif` - GIF fallback of the same recording for README contexts
  where WebM links are less prominent.
- `mercator-demo.tape` - the [VHS](https://github.com/charmbracelet/vhs) script
  that produces both files, so the demo is reproducible.
- `mercator-runs.png` - run list with status, exit code, cleanup disposition,
  and workspace context.
- `mercator-run-decision.png` - run detail placement decision and lifecycle
  state.
- `mercator-connections.png` - connection list and authorization status.

## Screenshot Capture Notes

Use the Docker quickstart path for launch screenshots so captures are
reproducible against the current build. It requires a running Docker daemon but
no RunPod, registry credentials, or private workloads.

1. Start Mercator with the Docker adapter following the
   [Docker quickstart](../production/docker-adapter-operation.md): set
   `MERCATOR_ADAPTER=docker`, `MERCATOR_DOCKER_ARCH=amd64`,
   `MERCATOR_API_TOKEN`, and a launch-safe workspace such as `ws_1`.
2. Create a digest-pinned `busybox -- echo hi` run through the CLI and wait for
   the run to close (mutable tags are rejected, so resolve the digest first with
   `docker inspect --format '{{index .RepoDigests 0}}' busybox:latest`).
3. Open the embedded console for that workspace.
4. Capture these screens: run list, selected run detail, placement decision,
   public events, and connections/offers if they materially improve the docs.
5. Keep raw captures under ignored `output/` until reviewed.
6. Move only selected images into `docs/assets/`, with descriptive filenames and
   no private tokens, hostnames, local usernames, or machine identifiers.
7. Do not replace the existing screenshots unless the new captures are clearer
   or show a launch-relevant state the current images miss.

## Demo Transcript

The committed recording follows the README
[Docker quickstart](../../README.md#try-it-in-5-minutes) end to end:

1. Start the broker in Docker with the Docker adapter
   (`docker run ... -v /var/run/docker.sock:/var/run/docker.sock mercator:local serve`)
   and confirm `GET /health/ready` returns `{"status": "ready"}`.
2. Pin a digest (`docker inspect --format '{{index .RepoDigests 0}}' busybox:latest`)
   and create a run with `mercator run create "$IMAGE" -- echo hi`.
3. Read the terminal result with `mercator run get`:
   `outcome: succeeded`, `exit_code: 0`, `cleanup: confirmed`, `closed: true`.
4. Ask why it landed there with `mercator run decision`:
   `selected: offer_docker_loopback`, `candidate_count: 1`.

## Demo Regeneration

The demo is rendered deterministically from `mercator-demo.tape` with
[VHS](https://github.com/charmbracelet/vhs), so it stays in sync with the
quickstart. From the repository root, with a running Docker daemon:

```sh
docker build -t mercator:local .        # broker image used by the tape
go build -o mercator ./cmd/mercator      # CLI on $PWD, driven by the tape
vhs docs/assets/mercator-demo.tape       # writes gif + webm to docs/assets/output/
```

Review the result under `docs/assets/output/`, then move the `mercator-demo.gif`
and `mercator-demo.webm` files into `docs/assets/`. The tape sets a clean `$ `
prompt so no local path, hostname, or username is captured; keep it that way if
you edit it.
