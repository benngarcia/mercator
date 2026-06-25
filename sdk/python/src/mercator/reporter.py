"""Workload reporter — posts structured events from a running workload back to Mercator.

Usage inside a workload container::

    from mercator import run_reporter

    reporter = run_reporter()
    if reporter:
        reporter.report("started")
        # ... do work ...
        reporter.report("progress", {"pct": 50})
        reporter.report_exit(0)

Or use as a context manager (automatically reports exit code on __exit__)::

    with run_reporter() as r:
        if r:
            r.report("started")
"""

from __future__ import annotations

import json
import os
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


class ReporterError(Exception):
    """Raised when the Mercator report endpoint returns a non-202 response."""

    def __init__(self, status: int, body: str) -> None:
        self.status = status
        self.body = body
        super().__init__(f"Mercator reporter: expected HTTP 202, got {status}: {body}")


class Reporter:
    """Posts structured events to Mercator from inside a running workload.

    Do not instantiate directly — use :func:`run_reporter`.
    """

    def __init__(
        self,
        run_id: str,
        workspace_id: str,
        report_url: str,
        token: str,
        *,
        opener=None,
        timeout: float = 30.0,
    ) -> None:
        self._run_id = run_id
        self._workspace_id = workspace_id
        self._report_url = report_url.rstrip("/")
        self._token = token
        # Allow injecting a custom opener (e.g. in tests).  When None, the
        # standard urllib.request.urlopen is used.
        self._opener = opener
        self._timeout = timeout

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def report(self, type: str, data: dict[str, Any] | None = None) -> None:  # noqa: A002
        """POST an event to Mercator.

        :param type: Event type string (e.g. ``"progress"``).
        :param data: Optional structured payload attached to the event.
        """
        payload: dict[str, Any] = {"type": type}
        if data is not None:
            payload["data"] = data
        self._post(payload)

    def report_exit(self, code: int) -> None:
        """POST an exit event with the given exit code."""
        self._post({"type": "exit", "exit_code": code})

    # ------------------------------------------------------------------
    # Context manager
    # ------------------------------------------------------------------

    def __enter__(self) -> "Reporter":
        return self

    def __exit__(self, exc_type, exc_val, exc_tb) -> bool:
        code = 0 if exc_type is None else 1
        try:
            self.report_exit(code)
        except Exception:
            # Do not suppress original exceptions, and don't let reporter
            # failures mask them either — just swallow the reporter error.
            pass
        return False  # never suppress the original exception

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _url(self) -> str:
        run_id_enc = quote(self._run_id, safe="")
        qs = urlencode({"workspace_id": self._workspace_id})
        return f"{self._report_url}/v1/runs/{run_id_enc}:report?{qs}"

    def _post(self, payload: dict[str, Any]) -> None:
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        request = Request(
            self._url(),
            data=body,
            headers={
                "Authorization": f"Bearer {self._token}",
                "Content-Type": "application/json",
                "Accept": "application/json",
                # Set an explicit User-Agent. urllib otherwise sends
                # "Python-urllib/X.Y", which Cloudflare's managed rules ban
                # (HTTP 1010 / 403) — so reports through a Cloudflare-fronted
                # Mercator (e.g. a named tunnel) would be silently rejected.
                "User-Agent": "mercator-reporter (python)",
            },
            method="POST",
        )
        try:
            open_fn = self._opener if self._opener is not None else urlopen
            if self._timeout is None:
                response = open_fn(request)
            else:
                response = open_fn(request, timeout=self._timeout)
            with response:
                status = response.getcode()
        except HTTPError as exc:
            raw = exc.read() or b""
            raise ReporterError(exc.code, raw.decode("utf-8", errors="replace")) from exc
        except URLError as exc:
            raise ReporterError(0, str(getattr(exc, "reason", exc))) from exc
        else:
            if status != 202:
                raise ReporterError(status, "")


# ------------------------------------------------------------------
# Factory
# ------------------------------------------------------------------

def run_reporter(env=None, *, opener=None) -> Reporter | None:
    """Create a :class:`Reporter` from environment variables injected by Mercator.

    Returns ``None`` (without raising) when the required variables are absent,
    so workloads running outside Mercator degrade gracefully.

    :param env: Mapping to read env vars from (defaults to ``os.environ``).
    :param opener: Override the HTTP opener (useful in tests).
    """
    source = env if env is not None else os.environ
    report_url = source.get("MERCATOR_REPORT_URL")
    run_id = source.get("MERCATOR_RUN_ID")
    workspace_id = source.get("MERCATOR_WORKSPACE_ID", "")
    token = source.get("MERCATOR_RUN_TOKEN")

    if not report_url or not run_id or not token:
        return None

    return Reporter(
        run_id=run_id,
        workspace_id=workspace_id,
        report_url=report_url,
        token=token,
        opener=opener,
    )
