from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any, Mapping
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


JSONValue = dict[str, Any] | list[Any] | str | int | float | bool | None
JSONObject = dict[str, Any]


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

    def __init__(
        self,
        base_url: str,
        token: str | None = None,
        *,
        timeout: float | None = 30.0,
        user_agent: str = "mercator-python/0.1.0",
    ) -> None:
        normalized_base_url = base_url.rstrip("/")
        if not normalized_base_url:
            raise ValueError("base_url must not be empty")
        self.base_url = normalized_base_url
        self.token = token
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
    ) -> Any:
        """Send an HTTP request and return the decoded JSON response."""

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
        try:
            if self.timeout is None:
                response = urlopen(request)
            else:
                response = urlopen(request, timeout=self.timeout)
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

    def health_live(self) -> Any:
        return self.request("GET", "/health/live")

    def health_ready(self) -> Any:
        return self.request("GET", "/health/ready")

    def get_openapi(self) -> Any:
        return self.request("GET", "/openapi.json")

    def list_runs(self, workspace_id: str) -> Any:
        return self.request("GET", "/v1/runs", query=self._workspace_query(workspace_id))

    def create_run(self, payload: Mapping[str, Any], *, idempotency_key: str) -> Any:
        return self.request("POST", "/v1/runs", json_body=dict(payload), idempotency_key=idempotency_key)

    def get_run(self, run_id: str, workspace_id: str) -> Any:
        return self.request("GET", f"/v1/runs/{self._path(run_id)}", query=self._workspace_query(workspace_id))

    def wait_run(self, run_id: str, workspace_id: str) -> Any:
        return self.request("GET", f"/v1/runs/{self._path(run_id)}:wait", query=self._workspace_query(workspace_id))

    def refresh_run(self, run_id: str, workspace_id: str) -> Any:
        return self.request("POST", f"/v1/runs/{self._path(run_id)}:refresh", query=self._workspace_query(workspace_id))

    def cancel_run(self, run_id: str, workspace_id: str) -> Any:
        return self.request("POST", f"/v1/runs/{self._path(run_id)}:cancel", query=self._workspace_query(workspace_id))

    def list_run_events(self, run_id: str, workspace_id: str) -> Any:
        return self.request("GET", f"/v1/runs/{self._path(run_id)}/events", query=self._workspace_query(workspace_id))

    def get_run_decision(self, run_id: str, workspace_id: str) -> Any:
        return self.request("GET", f"/v1/runs/{self._path(run_id)}/decision", query=self._workspace_query(workspace_id))

    def preview_placement(self, payload: Mapping[str, Any]) -> Any:
        return self.request("POST", "/v1/placements:preview", json_body=dict(payload))

    def list_connections(self, workspace_id: str) -> Any:
        return self.request("GET", "/v1/connections", query=self._workspace_query(workspace_id))

    def list_offers(self, workspace_id: str) -> Any:
        return self.request("GET", "/v1/offers", query=self._workspace_query(workspace_id))

    def create_workload(self, workspace_id: str, workload_id: str, name: str, *, idempotency_key: str) -> Any:
        return self.request(
            "POST",
            "/v1/workloads",
            json_body={"workspace_id": workspace_id, "workload_id": workload_id, "name": name},
            idempotency_key=idempotency_key,
        )

    def list_workload_revisions(self, workload_id: str, workspace_id: str) -> Any:
        return self.request(
            "GET",
            f"/v1/workloads/{self._path(workload_id)}/revisions",
            query=self._workspace_query(workspace_id),
        )

    def create_workload_revision(
        self,
        workload_id: str,
        workspace_id: str,
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

    def get_workload_revision(self, workload_id: str, revision_id: str, workspace_id: str) -> Any:
        return self.request(
            "GET",
            f"/v1/workloads/{self._path(workload_id)}/revisions/{self._path(revision_id)}",
            query=self._workspace_query(workspace_id),
        )

    def resolve_image(self, image: str, platform: str) -> Any:
        return self.request("POST", "/v1/images:resolve", json_body={"image": image, "platform": platform})

    def list_secrets(self, workspace_id: str) -> Any:
        return self.request("GET", "/v1/secrets", query=self._workspace_query(workspace_id))

    def create_secret_version(self, secret_id: str, workspace_id: str, value: str, *, idempotency_key: str) -> Any:
        return self.request(
            "POST",
            f"/v1/secrets/{self._path(secret_id)}/versions",
            json_body={"workspace_id": workspace_id, "value": value},
            idempotency_key=idempotency_key,
        )

    def grant_secret(self, secret_id: str, workspace_id: str, version: int, scope_type: str, scope_id: str) -> Any:
        return self.request(
            "POST",
            f"/v1/secrets/{self._path(secret_id)}/grants",
            json_body={
                "workspace_id": workspace_id,
                "version": version,
                "scope_type": scope_type,
                "scope_id": scope_id,
            },
        )

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

    def _workspace_query(self, workspace_id: str) -> dict[str, str]:
        return {"workspace_id": workspace_id}

    def _path(self, value: str) -> str:
        return quote(value, safe="")

    def _compact(self, payload: Mapping[str, Any]) -> JSONObject:
        return {key: value for key, value in payload.items() if value is not None}
