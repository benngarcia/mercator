from __future__ import annotations

import json
import time
import uuid
from dataclasses import dataclass
from typing import Any, Mapping
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


JSONValue = dict[str, Any] | list[Any] | str | int | float | bool | None
JSONObject = dict[str, Any]

# Sentinel distinguishing "not passed" from an explicit ``timeout=None``.
_UNSET: Any = object()


@dataclass
class MercatorError(Exception):
    status_code: int | None
    code: str
    message: str
    details: Any = None
    response: Any = None

    def __str__(self) -> str:
        if self.status_code is None:
            return f"{self.code}: {self.message}"
        return f"Mercator API error {self.status_code} {self.code}: {self.message}"


class MercatorClient:
    """Dependency-free client for the Mercator V1 HTTP API."""

    # Read timeout applied to `:wait` long-polls. The server holds each wait
    # open for up to ~30s before answering, so the effective read timeout on
    # that call must comfortably exceed 30s or every wait on a long run times
    # out client-side instead of re-polling. The user's configured ``timeout``
    # still applies to all other calls (and wins here when it is larger).
    WAIT_READ_TIMEOUT = 60.0

    def __init__(
        self,
        base_url: str,
        token: str | None = None,
        *,
        workspace_id: str | None = None,
        timeout: float | None = 30.0,
        user_agent: str = "mercator-python/0.1.0",
    ) -> None:
        normalized_base_url = base_url.rstrip("/")
        if not normalized_base_url:
            raise ValueError("base_url must not be empty")
        self.base_url = normalized_base_url
        self.token = token
        # Default workspace applied to every call (query param on reads, body
        # field on create_run) when a per-call workspace_id is not supplied.
        # Per-call values always win.
        self.workspace_id = workspace_id
        self.timeout = timeout
        self.user_agent = user_agent

    def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, Any] | None = None,
        json_body: JSONValue = None,
        headers: Mapping[str, str] | None = None,
        idempotency_key: str | None = None,
        timeout: float | None = _UNSET,
    ) -> Any:
        """Send an HTTP request and return the decoded JSON response.

        ``timeout`` overrides the client-wide timeout for this call only; omit
        it to use the client default.
        """

        if not path.startswith("/"):
            raise ValueError("path must start with '/'")

        body = None
        request_headers = {
            "Accept": "application/json",
            "User-Agent": self.user_agent,
        }
        if self.token is not None:
            request_headers["Authorization"] = f"Bearer {self.token}"
        if idempotency_key is not None:
            request_headers["Idempotency-Key"] = idempotency_key
        if json_body is not None:
            body = json.dumps(json_body, separators=(",", ":")).encode("utf-8")
            request_headers["Content-Type"] = "application/json"
        if headers is not None:
            request_headers.update(headers)

        request = Request(
            self._url(path, query),
            data=body,
            headers=request_headers,
            method=method.upper(),
        )
        effective_timeout = self.timeout if timeout is _UNSET else timeout
        try:
            if effective_timeout is None:
                response = urlopen(request)
            else:
                response = urlopen(request, timeout=effective_timeout)
            with response:
                return self._decode_response(response.getcode(), response.headers.get("Content-Type", ""), response.read())
        except HTTPError as exc:
            payload = self._decode_error_payload(exc)
            code = self._error_field(payload, "code", exc.reason or "HTTP_ERROR")
            message = self._error_field(payload, "message", exc.reason or "HTTP error")
            details = payload.get("details") if isinstance(payload, dict) else None
            raise MercatorError(exc.code, code, message, details=details, response=payload) from exc
        except URLError as exc:
            reason = getattr(exc, "reason", exc)
            raise MercatorError(None, "REQUEST_FAILED", str(reason)) from exc
        except TimeoutError as exc:
            # urllib only wraps errors raised while sending the request; a
            # socket timeout while reading the response escapes as a bare
            # TimeoutError, so normalize it to the same transport error.
            raise MercatorError(None, "REQUEST_FAILED", str(exc) or "timed out") from exc

    def health_live(self) -> Any:
        return self.request("GET", "/health/live")

    def health_ready(self) -> Any:
        return self.request("GET", "/health/ready")

    def get_openapi(self) -> Any:
        return self.request("GET", "/openapi.json")

    def list_runs(self, workspace_id: str | None = None) -> Any:
        return self.request("GET", "/v1/runs", query=self._workspace_query(workspace_id))

    def create_run(self, payload: Mapping[str, Any], *, idempotency_key: str, workspace_id: str | None = None) -> Any:
        body = dict(payload)
        effective_workspace = workspace_id if workspace_id is not None else self.workspace_id
        if effective_workspace is not None and not body.get("workspace_id"):
            body["workspace_id"] = effective_workspace
        return self.request("POST", "/v1/runs", json_body=body, idempotency_key=idempotency_key)

    def run_image(
        self,
        image: str,
        *,
        args: list[str] | None = None,
        env: Mapping[str, Any] | None = None,
        run_id: str | None = None,
        workspace_id: str | None = None,
        idempotency_key: str | None = None,
    ) -> Any:
        """Create a run from just an image (the server shorthand form).

        Only ``image`` is required. ``run_id`` is optional: omit it and the SDK
        generates one, then derives a retry-safe ``Idempotency-Key`` as
        ``f"{run_id}:create"`` unless you pass ``idempotency_key`` explicitly.
        The server applies all other defaults (container name=main,
        platform=linux/amd64, resources, network, placement, execution). Returns
        the same envelope as :meth:`create_run`.
        """

        effective_run_id = run_id or self._new_run_id()
        payload: dict[str, Any] = {"image": image}
        if args:
            payload["args"] = list(args)
        if env:
            payload["env"] = dict(env)
        payload["run_id"] = effective_run_id
        key = idempotency_key or f"{effective_run_id}:create"
        return self.create_run(payload, idempotency_key=key, workspace_id=workspace_id)

    def get_run(self, run_id: str, workspace_id: str | None = None) -> Any:
        return self.request("GET", f"/v1/runs/{self._path(run_id)}", query=self._workspace_query(workspace_id))

    def wait_run(self, run_id: str, workspace_id: str | None = None) -> Any:
        return self.request(
            "GET",
            f"/v1/runs/{self._path(run_id)}:wait",
            query=self._workspace_query(workspace_id),
            timeout=self._wait_timeout(),
        )

    def wait_run_until_terminal(
        self,
        run_id: str,
        workspace_id: str | None = None,
        *,
        deadline: float = 300.0,
    ) -> Any:
        """Block until a run reaches a terminal (closed) state.

        Honors the server's long-poll semantics: ``:wait`` returns 202 with the
        latest still-open run at its internal (~30s) deadline, so this helper
        re-issues the wait until the run closes or ``deadline`` seconds elapse.
        Returns the latest run envelope either way; inspect
        ``result["run"]["closed"]`` to distinguish terminal from timed-out, and
        read ``result["run"]["exit_code"]`` for the container exit code.
        """

        end = time.monotonic() + deadline
        while True:
            response = self.wait_run(run_id, workspace_id)
            run = response.get("run") if isinstance(response, dict) else None
            if isinstance(run, dict) and run.get("closed"):
                return response
            if time.monotonic() >= end:
                return response

    def refresh_run(self, run_id: str, workspace_id: str | None = None) -> Any:
        return self.request("POST", f"/v1/runs/{self._path(run_id)}:refresh", query=self._workspace_query(workspace_id))

    def cancel_run(self, run_id: str, workspace_id: str | None = None) -> Any:
        return self.request("POST", f"/v1/runs/{self._path(run_id)}:cancel", query=self._workspace_query(workspace_id))

    def list_run_events(self, run_id: str, workspace_id: str | None = None) -> Any:
        return self.request("GET", f"/v1/runs/{self._path(run_id)}/events", query=self._workspace_query(workspace_id))

    def get_run_decision(self, run_id: str, workspace_id: str | None = None) -> Any:
        return self.request("GET", f"/v1/runs/{self._path(run_id)}/decision", query=self._workspace_query(workspace_id))

    def preview_placement(self, payload: Mapping[str, Any]) -> Any:
        return self.request("POST", "/v1/placements:preview", json_body=dict(payload))

    def list_connections(self, workspace_id: str | None = None) -> Any:
        return self.request("GET", "/v1/connections", query=self._workspace_query(workspace_id))

    def create_connection(self, payload: Mapping[str, Any], *, idempotency_key: str, workspace_id: str | None = None) -> Any:
        """Create a connection (POST /v1/connections).

        ``payload`` carries the server's create-connection body:
        ``connection_id``, ``adapter_type``, optional ``config``, optional
        ``credential`` (``{"source": ..., "ref": ...}``), and optional
        write-only ``secret``. ``workspace_id`` follows the same fallback rules
        as :meth:`create_run`.
        """

        body = dict(payload)
        effective_workspace = workspace_id if workspace_id is not None else self.workspace_id
        if effective_workspace is not None and not body.get("workspace_id"):
            body["workspace_id"] = effective_workspace
        return self.request("POST", "/v1/connections", json_body=body, idempotency_key=idempotency_key)

    def authorize_connection(self, connection_id: str, workspace_id: str | None = None) -> Any:
        return self.request(
            "POST",
            f"/v1/connections/{self._path(connection_id)}:authorize",
            query=self._workspace_query(workspace_id),
        )

    def list_offers(self, workspace_id: str | None = None) -> Any:
        return self.request("GET", "/v1/offers", query=self._workspace_query(workspace_id))

    def create_workload(self, workspace_id: str, workload_id: str, name: str, *, idempotency_key: str) -> Any:
        return self.request(
            "POST",
            "/v1/workloads",
            json_body={"workspace_id": workspace_id, "workload_id": workload_id, "name": name},
            idempotency_key=idempotency_key,
        )

    def list_workload_revisions(self, workload_id: str, workspace_id: str | None = None) -> Any:
        return self.request(
            "GET",
            f"/v1/workloads/{self._path(workload_id)}/revisions",
            query=self._workspace_query(workspace_id),
        )

    def create_workload_revision(
        self,
        workload_id: str,
        workspace_id: str | None,
        revision: Mapping[str, Any],
        *,
        idempotency_key: str,
    ) -> Any:
        return self.request(
            "POST",
            f"/v1/workloads/{self._path(workload_id)}/revisions",
            query=self._workspace_query(workspace_id),
            json_body={"revision": dict(revision)},
            idempotency_key=idempotency_key,
        )

    def get_workload_revision(self, workload_id: str, revision_id: str, workspace_id: str | None = None) -> Any:
        return self.request(
            "GET",
            f"/v1/workloads/{self._path(workload_id)}/revisions/{self._path(revision_id)}",
            query=self._workspace_query(workspace_id),
        )

    def resolve_image(self, image: str, platform: str) -> Any:
        return self.request("POST", "/v1/images:resolve", json_body={"image": image, "platform": platform})

    def get_sink_status(self, sink_id: str) -> Any:
        return self.request("GET", f"/v1/sinks/{self._path(sink_id)}")

    def deliver_sink(self, sink_id: str) -> Any:
        return self.request("POST", f"/v1/sinks/{self._path(sink_id)}:deliver")

    def replay_sink(
        self,
        sink_id: str,
        *,
        from_exclusive: int | None = None,
        limit: int | None = None,
        replay_id: str | None = None,
    ) -> Any:
        return self.request(
            "POST",
            f"/v1/sinks/{self._path(sink_id)}:replay",
            json_body=self._compact(
                {
                    "from_exclusive": from_exclusive,
                    "limit": limit,
                    "replay_id": replay_id,
                }
            ),
        )

    def _url(self, path: str, query: Mapping[str, Any] | None) -> str:
        if not query:
            return self.base_url + path
        pairs = []
        for key, value in query.items():
            if value is None:
                continue
            if isinstance(value, (list, tuple)):
                pairs.extend((key, item) for item in value if item is not None)
            else:
                pairs.append((key, value))
        encoded = urlencode(pairs, doseq=True)
        if not encoded:
            return self.base_url + path
        return f"{self.base_url}{path}?{encoded}"

    def _decode_response(self, status_code: int, content_type: str, raw_body: bytes) -> Any:
        if not raw_body:
            return None
        text = raw_body.decode("utf-8")
        if "json" not in content_type.lower():
            return text
        try:
            return json.loads(text)
        except json.JSONDecodeError as exc:
            raise MercatorError(status_code, "INVALID_RESPONSE", "Response body was not valid JSON.", response=text) from exc

    def _decode_error_payload(self, exc: HTTPError) -> Any:
        raw_body = exc.read()
        if not raw_body:
            return None
        text = raw_body.decode("utf-8", errors="replace")
        content_type = exc.headers.get("Content-Type", "")
        if "json" not in content_type.lower():
            return text
        try:
            return json.loads(text)
        except json.JSONDecodeError:
            return text

    def _error_field(self, payload: Any, field: str, fallback: str) -> str:
        if isinstance(payload, dict):
            value = payload.get(field)
            if isinstance(value, str) and value:
                return value
        return fallback

    def _wait_timeout(self) -> float | None:
        """Effective read timeout for `:wait` long-polls.

        ``None`` (no timeout) already outlasts the server's ~30s window;
        otherwise never poll with less than :data:`WAIT_READ_TIMEOUT`.
        """

        if self.timeout is None:
            return None
        return max(self.timeout, self.WAIT_READ_TIMEOUT)

    def _workspace_query(self, workspace_id: str | None) -> dict[str, str]:
        effective = workspace_id if workspace_id is not None else self.workspace_id
        if effective is None:
            return {}
        return {"workspace_id": effective}

    def _path(self, value: str) -> str:
        return quote(value, safe="")

    def _compact(self, payload: Mapping[str, Any]) -> JSONObject:
        return {key: value for key, value in payload.items() if value is not None}

    def _new_run_id(self) -> str:
        return f"run_{uuid.uuid4()}"
