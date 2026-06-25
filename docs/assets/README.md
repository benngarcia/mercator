# Documentation Assets

Tracked assets in this directory are selected for launch-facing docs. Raw local
captures stay in ignored `output/` until they are reviewed and renamed.

Current screenshots:

- `mercator-runs.png` - run list with status, exit code, cleanup disposition,
  and workspace context.
- `mercator-run-decision.png` - run detail placement decision and lifecycle
  state.
- `mercator-connections.png` - connection list and authorization status.

Demo video plan:

1. Start Mercator with `MERCATOR_FAKE_OFFER=1`.
2. Create `busybox -- echo hi` from the CLI.
3. Open the console and show the run moving to `succeeded`.
4. Inspect the decision tab and public events.
5. End with the fake evaluation command and docs pointer.

Target output: `docs/assets/mercator-demo.webm`, plus a shorter GIF cut for the
README if the final file size is reasonable.
