import json
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from mercator import MercatorClient, MercatorError


class RecordingHandler(BaseHTTPRequestHandler):
    requests = []

    def do_GET(self):
        self._record_and_respond()

    def do_POST(self):
        self._record_and_respond()

    def log_message(self, format, *args):
        return

    def _record_and_respond(self):
        length = int(self.headers.get("Content-Length", "0"))
        raw_body = self.rfile.read(length) if length else b""
        body = json.loads(raw_body.decode("utf-8")) if raw_body else None
        self.requests.append(
            {
                "method": self.command,
                "path": self.path,
                "headers": dict(self.headers.items()),
                "body": body,
            }
        )

        if self.path.startswith("/v1/runs/missing"):
            self._send_json(
                404,
                {
                    "code": "RUN_NOT_FOUND",
                    "message": "run was not found",
                    "details": [{"field": "run_id"}],
                },
            )
            return

        if self.command == "POST" and self.path == "/v1/runs":
            # Mirror the server: run_id is optional and generated when omitted.
            run_id = (body or {}).get("run_id") or "run_generated_1"
            self._send_json(
                202,
                {
                    "run_id": run_id,
                    "run": {
                        "id": run_id,
                        "workspace_id": (body or {}).get("workspace_id", ""),
                        "phase": "requested",
                        "cleanup": "not_required",
                        "disposition": "release",
                        "closed": False,
                    },
                    "duplicate": False,
                },
            )
            return

        if self.path.startswith("/v1/runs/run%201"):
            self._send_json(200, {"run": {"id": "run 1"}})
            return

        self._send_json(200 if self.command == "GET" else 202, {"ok": True})

    def _send_json(self, status, payload):
        data = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class ClientServerTestCase(unittest.TestCase):
    def setUp(self):
        RecordingHandler.requests = []
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), RecordingHandler)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.daemon = True
        self.thread.start()
        host, port = self.server.server_address
        self.base_url = f"http://{host}:{port}"

    def tearDown(self):
        self.server.shutdown()
        self.thread.join(timeout=5)
        self.server.server_close()

    def test_create_run_sends_auth_json_idempotency_key_and_decodes_response(self):
        client = MercatorClient(self.base_url, token="secret-token")

        result = client.create_run(
            {
                "workspace_id": "ws_1",
                "run_id": "run_1",
                "workload": {"workspace_id": "ws_1"},
            },
            idempotency_key="idem-1",
        )

        self.assertEqual(result["run"]["id"], "run_1")
        # Convenience top-level run_id is returned alongside the full run record.
        self.assertEqual(result["run_id"], "run_1")
        self.assertEqual(result["run_id"], result["run"]["id"])
        self.assertEqual(result["run"]["disposition"], "release")
        self.assertEqual(result["duplicate"], False)
        request = RecordingHandler.requests[-1]
        self.assertEqual(request["method"], "POST")
        self.assertEqual(request["path"], "/v1/runs")
        self.assertEqual(request["headers"]["Authorization"], "Bearer secret-token")
        self.assertEqual(request["headers"]["Idempotency-Key"], "idem-1")
        self.assertEqual(request["headers"]["Accept"], "application/json")
        self.assertIn("application/json", request["headers"]["Content-Type"])
        self.assertEqual(request["body"]["run_id"], "run_1")

    def test_get_run_encodes_path_and_query_parameters(self):
        client = MercatorClient(self.base_url, token="secret-token")

        result = client.get_run("run 1", workspace_id="ws/1")

        self.assertEqual(result, {"run": {"id": "run 1"}})
        self.assertEqual(RecordingHandler.requests[-1]["path"], "/v1/runs/run%201?workspace_id=ws%2F1")

    def test_http_errors_raise_mercator_error_with_error_payload(self):
        client = MercatorClient(self.base_url, token="secret-token")

        with self.assertRaises(MercatorError) as raised:
            client.get_run("missing", workspace_id="ws_1")

        error = raised.exception
        self.assertEqual(error.status_code, 404)
        self.assertEqual(error.code, "RUN_NOT_FOUND")
        self.assertEqual(error.message, "run was not found")
        self.assertEqual(error.details, [{"field": "run_id"}])
        self.assertIn("RUN_NOT_FOUND", str(error))

    def test_main_v1_methods_map_to_expected_routes(self):
        client = MercatorClient(self.base_url, token="secret-token")

        client.list_runs("ws_1")
        client.wait_run("run_1", "ws_1")
        client.refresh_run("run_1", "ws_1")
        client.cancel_run("run_1", "ws_1")
        client.list_run_events("run_1", "ws_1")
        client.get_run_decision("run_1", "ws_1")
        client.preview_placement({"workspace_id": "ws_1", "workload": {"workspace_id": "ws_1"}})
        client.list_connections("ws_1")
        client.list_offers("ws_1")
        client.create_workload("ws_1", "workload_1", "demo", idempotency_key="workload-key")
        client.list_workload_revisions("workload_1", "ws_1")
        client.create_workload_revision("workload_1", "ws_1", {"id": "rev_1"}, idempotency_key="revision-key")
        client.get_workload_revision("workload_1", "rev_1", "ws_1")
        client.resolve_image("repo/image:tag", "linux/amd64")
        client.get_sink_status("audit")
        client.deliver_sink("audit")
        client.replay_sink("audit", from_exclusive=10, limit=50, replay_id="replay_1")

        self.assertEqual(
            [(request["method"], request["path"]) for request in RecordingHandler.requests],
            [
                ("GET", "/v1/runs?workspace_id=ws_1"),
                ("GET", "/v1/runs/run_1:wait?workspace_id=ws_1"),
                ("POST", "/v1/runs/run_1:refresh?workspace_id=ws_1"),
                ("POST", "/v1/runs/run_1:cancel?workspace_id=ws_1"),
                ("GET", "/v1/runs/run_1/events?workspace_id=ws_1"),
                ("GET", "/v1/runs/run_1/decision?workspace_id=ws_1"),
                ("POST", "/v1/placements:preview"),
                ("GET", "/v1/connections?workspace_id=ws_1"),
                ("GET", "/v1/offers?workspace_id=ws_1"),
                ("POST", "/v1/workloads"),
                ("GET", "/v1/workloads/workload_1/revisions?workspace_id=ws_1"),
                ("POST", "/v1/workloads/workload_1/revisions?workspace_id=ws_1"),
                ("GET", "/v1/workloads/workload_1/revisions/rev_1?workspace_id=ws_1"),
                ("POST", "/v1/images:resolve"),
                ("GET", "/v1/sinks/audit"),
                ("POST", "/v1/sinks/audit:deliver"),
                ("POST", "/v1/sinks/audit:replay"),
            ],
        )
        self.assertEqual(RecordingHandler.requests[9]["headers"]["Idempotency-Key"], "workload-key")
        self.assertEqual(RecordingHandler.requests[11]["headers"]["Idempotency-Key"], "revision-key")
        self.assertEqual(RecordingHandler.requests[16]["body"], {"from_exclusive": 10, "limit": 50, "replay_id": "replay_1"})

    def test_client_scoped_workspace_id_applies_and_is_overridable(self):
        client = MercatorClient(self.base_url, token="secret-token", workspace_id="ws_default")

        client.create_run({"run_id": "run_1", "workload": {"workspace_id": "ws_default"}}, idempotency_key="idem-1")
        client.get_run("run_1")
        client.get_run("run_1", workspace_id="ws_override")

        create_request = RecordingHandler.requests[0]
        self.assertEqual(create_request["body"]["workspace_id"], "ws_default")
        self.assertEqual(RecordingHandler.requests[1]["path"], "/v1/runs/run_1?workspace_id=ws_default")
        self.assertEqual(RecordingHandler.requests[2]["path"], "/v1/runs/run_1?workspace_id=ws_override")

    def test_explicit_workspace_id_in_create_body_is_not_overwritten(self):
        client = MercatorClient(self.base_url, token="secret-token", workspace_id="ws_default")

        client.create_run({"run_id": "run_1", "workspace_id": "ws_explicit"}, idempotency_key="idem-1")

        self.assertEqual(RecordingHandler.requests[0]["body"]["workspace_id"], "ws_explicit")

    def test_run_image_shorthand_omits_run_id_and_returns_generated_id(self):
        client = MercatorClient(self.base_url, token="secret-token", workspace_id="ws_default")

        result = client.run_image("busybox", args=["echo", "hi"], idempotency_key="idem-shorthand")

        body = RecordingHandler.requests[0]["body"]
        self.assertEqual(body["image"], "busybox")
        self.assertEqual(body["args"], ["echo", "hi"])
        self.assertNotIn("run_id", body)  # omitted -> server generates it
        self.assertEqual(body["workspace_id"], "ws_default")
        self.assertEqual(RecordingHandler.requests[0]["headers"]["Idempotency-Key"], "idem-shorthand")
        # The SDK surfaces the server-generated id directly.
        self.assertEqual(result["run"]["id"], "run_generated_1")

    def test_run_image_shorthand_honors_explicit_run_id_and_env(self):
        client = MercatorClient(self.base_url, token="secret-token", workspace_id="ws_default")

        client.run_image(
            "busybox",
            run_id="run_explicit",
            env={"K": {"value": "v"}},
            idempotency_key="idem-explicit",
        )

        body = RecordingHandler.requests[0]["body"]
        self.assertEqual(body["run_id"], "run_explicit")
        self.assertEqual(body["env"], {"K": {"value": "v"}})
        self.assertNotIn("args", body)  # omitted when no args supplied

    def test_run_image_shorthand_derives_stable_key_from_explicit_run_id(self):
        client = MercatorClient(self.base_url, token="secret-token", workspace_id="ws_default")

        client.run_image("busybox", run_id="run_explicit")

        self.assertEqual(
            RecordingHandler.requests[0]["headers"]["Idempotency-Key"], "run_explicit:create"
        )

    def test_run_image_requires_idempotency_key_when_run_id_omitted(self):
        # Retry-safety regression guard: minting a fresh random key per call
        # silently breaks at-most-once for the generated-run_id path. The SDK
        # must refuse rather than create a second run on transport retry.
        client = MercatorClient(self.base_url, token="secret-token", workspace_id="ws_default")

        with self.assertRaises(ValueError):
            client.run_image("busybox")

        self.assertEqual(RecordingHandler.requests, [])


class WaitHandler(BaseHTTPRequestHandler):
    open_responses = 0
    seen = 0

    def do_GET(self):
        WaitHandler.seen += 1
        closed = WaitHandler.seen > WaitHandler.open_responses
        status = 200 if closed else 202
        payload = {
            "run": {
                "id": "run_1",
                "workspace_id": "ws_1",
                "phase": "closed" if closed else "launch",
                "outcome": "succeeded" if closed else None,
                "exit_code": 0 if closed else None,
                "cleanup": "confirmed" if closed else "pending",
                "closed": closed,
            }
        }
        data = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, format, *args):
        return


class WaitUntilTerminalTestCase(unittest.TestCase):
    def setUp(self):
        WaitHandler.seen = 0
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), WaitHandler)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.daemon = True
        self.thread.start()
        host, port = self.server.server_address
        self.base_url = f"http://{host}:{port}"

    def tearDown(self):
        self.server.shutdown()
        self.thread.join(timeout=5)
        self.server.server_close()

    def test_wait_run_until_terminal_repolls_until_closed(self):
        WaitHandler.open_responses = 2
        client = MercatorClient(self.base_url, token="secret-token", workspace_id="ws_1")

        result = client.wait_run_until_terminal("run_1")

        self.assertEqual(WaitHandler.seen, 3)
        self.assertTrue(result["run"]["closed"])
        self.assertEqual(result["run"]["exit_code"], 0)

    def test_wait_run_until_terminal_stops_at_deadline_and_returns_open_run(self):
        WaitHandler.open_responses = 100
        client = MercatorClient(self.base_url, token="secret-token", workspace_id="ws_1")

        result = client.wait_run_until_terminal("run_1", deadline=0.0)

        self.assertEqual(WaitHandler.seen, 1)
        self.assertFalse(result["run"]["closed"])


if __name__ == "__main__":
    unittest.main()
