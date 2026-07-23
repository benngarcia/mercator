# Reposition as a warm fleet broker

Status: accepted (2026-07-22)

Mercator's docs launched it as an auditable run broker: the recorded placement
decision was the headline and start speed was incidental. The work since the
placement-scenario harness (rental schedules, queued bookings, the Workspace
canvas, the warmth program) exists to make starts fast, and the tools readers
compare it to, Modal and SkyPilot, sell speed and ergonomics. We decided the
public identity is a fast compute broker and fleet manager: docs lead with
Modal-style push-to-run on capacity you control, Warmth (image layers and
cache mounts already on a rental) is the optimization target, and the audited
decision record stays a first-class pillar without leading.

The Modal comparison is deliberately scoped to the interaction. Mercator takes
an OCI image, does not build or sync code into images, and runs on the user's
own providers; those differences are stated wherever Modal is named.

Consequences worth recording:

- README and ROADMAP are organized around this identity and stay strict about
  tense: shipped capability and the warmth program under construction are
  always distinguished.
- The collateral in `docs/launch/` was written for the audit-first positioning
  and is stale until rewritten; each file carries a banner saying so.
- Declaring a shared cache mount is a warmth signal for placement, never an
  exclusivity or single-writer guarantee. Two runs naming the same cache may
  run in parallel on different rentals, each with its own copy; single-writer
  safety stays application responsibility. This keeps warmth features from
  smuggling in concurrency-control promises.
- `CONTEXT.md` gains Fleet, Placement, Warmth, and Cache Mount, and bans
  "scheduler" as a component name: scheduling refers only to queue positions
  in a rental schedule.
