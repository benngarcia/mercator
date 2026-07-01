# Documentation Assets

Tracked assets in this directory are selected for launch-facing docs. Raw local
captures stay in ignored `output/` until they are reviewed and renamed.

Current assets:

- `mercator-demo.webm` - ~13-second embedded-console walkthrough: the runs list,
  a run's detail (phase timeline, outcome, exit code, cleanup), the placement
  decision (selected offer and reason codes), and the event-sourced audit trail.
  A synthetic cursor glides to each target, clicks leave a ripple, and the view
  eases-zooms into the decision and the event trail. Recorded headlessly and
  deterministically by `record-demo.mjs`.
- `mercator-demo.gif` - GIF fallback of the same recording for README contexts
  where WebM links are less prominent.
- `record-demo.mjs` - the [Playwright](https://playwright.dev) +
  [ghost-cursor](https://github.com/Xetera/ghost-cursor) script that seeds a run
  and records the console (pointer motion, click ripples, zoom), so the demo is
  reproducible.
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
   `MERCATOR_DOCKER_ARCH=amd64`, `MERCATOR_API_TOKEN`, and a launch-safe
   workspace such as `ws_1`.
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

The committed recording is a walk through the embedded console for workspace
`ws_1`:

1. The runs list shows two `Succeeded` runs with their status, cleanup
   disposition (`release · confirmed`), and exit code.
2. Opening a run shows its detail: the phase timeline
   (`Requested → Launching → Running → Cleaning up → Succeeded`) and run facts —
   `outcome: succeeded`, `exit code: 0`, `cleanup: confirmed`, `closed: yes`.
3. The Decision tab shows the placement decision: the selected offer
   (`offer_docker_loopback`), reason codes (`FEASIBLE`, `LOWEST_SCORE`), the
   model version, and the collection report (`conn_docker_loopback` queried).
4. The Events tab shows the public, event-sourced audit trail — placement
   decided, launch accepted, external state observed, outcome recorded, cleanup
   confirmed, and closed.

## Demo Regeneration

The demo is recorded headlessly with [Playwright](https://playwright.dev) by
`record-demo.mjs`, so it stays in sync with the console. From the repository
root, with a running Docker daemon and the broker up (see the README
[quickstart](../../README.md#try-it-in-5-minutes)):

```sh
npm i playwright ghost-cursor && npx playwright install chromium
node docs/assets/record-demo.mjs      # seeds a run, writes webm to docs/assets/output/
```

Then generate the GIF fallback (the zoom motion inflates the GIF, so this keeps
it under 5 MB) and move both files into `docs/assets/`:

```sh
ffmpeg -y -i docs/assets/output/mercator-demo.webm \
  -vf "fps=12,scale=880:-1:flags=lanczos,split[s0][s1];[s0]palettegen=max_colors=96[p];[s1][p]paletteuse=dither=bayer:bayer_scale=5" \
  docs/assets/output/mercator-demo.gif
mv docs/assets/output/mercator-demo.{webm,gif} docs/assets/
```

The script seeds the session (token + workspace) into `localStorage` exactly as
the console does — nothing sensitive is captured, and the seeded run is a
short-lived `busybox echo` with no private image, host path, or credential.
