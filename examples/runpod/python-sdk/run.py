"""Custom-event reporting on RunPod using the Mercator Python SDK.

Reads the injected MERCATOR_* env, emits two custom event types, and reports
exit automatically via the context manager. Run inside a python:3-slim pod.

When the injected reporting env is missing (MERCATOR_REPORT_URL, MERCATOR_RUN_ID,
or MERCATOR_RUN_TOKEN absent — e.g. running locally without Mercator),
run_reporter() returns None — the script degrades gracefully and still exits 0.
"""
import time

from mercator import run_reporter


def main() -> int:
    reporter = run_reporter()
    if reporter is None:
        print("No MERCATOR_* env detected — running without reporting.")
        return 0

    with reporter:
        reporter.report("model.loaded", {"name": "demo-model"})
        for pct in (25, 50, 75, 100):
            reporter.report("progress", {"pct": pct})
            time.sleep(1)
        # The context manager calls reporter.report_exit(0) on a clean exit,
        # or report_exit(1) if an exception propagates out of the block.
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
