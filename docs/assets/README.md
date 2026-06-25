# Documentation Assets

Tracked assets in this directory are selected for launch-facing docs. Raw local
captures stay in ignored `output/` until they are reviewed and renamed.

Current assets:

- `mercator-demo.webm` - 10-second fake-adapter console walkthrough from run
  list to run detail, placement decision, and public events.
- `mercator-demo.gif` - 640x360 GIF fallback generated from
  `mercator-demo.webm` for README contexts where WebM links are less prominent.
- `mercator-runs.png` - run list with status, exit code, cleanup disposition,
  and workspace context.
- `mercator-run-decision.png` - run detail placement decision and lifecycle
  state.
- `mercator-connections.png` - connection list and authorization status.

## Screenshot Capture Notes

Use the fake-adapter path for launch screenshots so captures are reproducible
and do not require Docker, RunPod, registry credentials, or private workloads.

1. Start Mercator with `MERCATOR_FAKE_OFFER=1`, `MERCATOR_API_TOKEN` set, and a
   launch-safe workspace such as `ws_1`.
2. Create `busybox -- echo hi` through the CLI and wait for the run to close.
3. Open the embedded console for that workspace.
4. Capture these screens: run list, selected run detail, placement decision,
   public events, and connections/offers if they materially improve the docs.
5. Keep raw captures under ignored `output/` until reviewed.
6. Move only selected images into `docs/assets/`, with descriptive filenames and
   no private tokens, hostnames, local usernames, or machine identifiers.
7. Do not replace the existing screenshots unless the new captures are clearer
   or show a launch-relevant state the current images miss.

## Demo Transcript

The README demo shows the fake-adapter path a new evaluator can run without
Docker, RunPod, or a registry.

1. The console opens on the runs list for workspace `ws_1`.
2. A fake `busybox` run appears with terminal status, exit code, cleanup, and
   closure state visible in the table.
3. The run detail view shows the selected run and its lifecycle state.
4. The decision tab shows the placement decision and why the fake local offer
   was selected.
5. The events view shows public run events so the evaluator can connect the UI
   back to Mercator's event-sourced audit trail.
6. The demo ends by returning to the documented fake evaluation path.

Demo video regeneration:

1. Start Mercator with `MERCATOR_FAKE_OFFER=1`.
2. Create `busybox -- echo hi` from the CLI.
3. Open the console and show the run moving to `succeeded`.
4. Inspect the decision tab and public events.
5. End with the fake evaluation command and docs pointer.

Target output: replace `docs/assets/mercator-demo.webm`, then regenerate the
README GIF fallback under 5 MB:

```sh
ffmpeg -y -i docs/assets/mercator-demo.webm \
  -vf "fps=8,scale=640:-1:flags=lanczos,split[s0][s1];[s0]palettegen=max_colors=128[p];[s1][p]paletteuse=dither=bayer:bayer_scale=5" \
  docs/assets/mercator-demo.gif
```
