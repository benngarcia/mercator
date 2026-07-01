import json
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from mercator import Reporter, ReporterError, run_reporter


# ---------------------------------------------------------------------------
# Minimal HTTP server that records incoming requests and returns configurable
# responses — mirrors the RecordingHandler pattern in test_client.py.
# ---------------------------------------------------------------------------

class ReportHandler(BaseHTTPRequestHandler):
    """Records every POST and replies with a configurable status code."""

    requests = []
    response_status = 202

    def do_POST(self):
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
        status = self.response_status
        data = json.dumps({"ok": status == 202}).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, format, *args):
        return


class ReporterServerTestCase(unittest.TestCase):
    def setUp(self):
        ReportHandler.requests = []
        ReportHandler.response_status = 202
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), ReportHandler)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.daemon = True
        self.thread.start()
        host, port = self.server.server_address
        self.base_url = f"http://{host}:{port}"

    def tearDown(self):
        self.server.shutdown()
        self.thread.join(timeout=5)
        self.server.server_close()

    def _make_reporter(self, run_id="run_abc", workspace_id="ws_xyz", token="tok_secret"):
        return Reporter(
            run_id=run_id,
            workspace_id=workspace_id,
            report_url=self.base_url,
            token=token,
        )

    # ------------------------------------------------------------------
    # report()
    # ------------------------------------------------------------------

    def test_report_posts_to_correct_url_with_auth_and_body(self):
        reporter = self._make_reporter()

        reporter.report("progress", {"pct": 50})

        self.assertEqual(len(ReportHandler.requests), 1)
        req = ReportHandler.requests[0]
        self.assertEqual(req["method"], "POST")
        self.assertEqual(req["path"], "/v1/runs/run_abc:report?workspace_id=ws_xyz")
        self.assertEqual(req["headers"]["Authorization"], "Bearer tok_secret")
        self.assertIn("application/json", req["headers"]["Content-Type"])
        self.assertEqual(req["body"], {"type": "progress", "data": {"pct": 50}})

    def test_report_omits_data_when_not_provided(self):
        reporter = self._make_reporter()

        reporter.report("started")

        req = ReportHandler.requests[0]
        self.assertEqual(req["body"], {"type": "started"})
        self.assertNotIn("data", req["body"])

    # ------------------------------------------------------------------
    # report_exit()
    # ------------------------------------------------------------------

    def test_report_exit_posts_exit_event_with_exit_code(self):
        reporter = self._make_reporter()

        reporter.report_exit(0)

        req = ReportHandler.requests[0]
        self.assertEqual(req["path"], "/v1/runs/run_abc:report?workspace_id=ws_xyz")
        self.assertEqual(req["body"], {"type": "exit", "exit_code": 0})

    def test_report_exit_encodes_non_zero_exit_code(self):
        reporter = self._make_reporter()

        reporter.report_exit(1)

        self.assertEqual(ReportHandler.requests[0]["body"], {"type": "exit", "exit_code": 1})

    # ------------------------------------------------------------------
    # URL encoding
    # ------------------------------------------------------------------

    def test_run_id_and_workspace_id_with_special_chars_are_url_encoded(self):
        reporter = Reporter(
            run_id="run/with spaces",
            workspace_id="ws/special&chars",
            report_url=self.base_url,
            token="tok",
        )

        reporter.report("test")

        req = ReportHandler.requests[0]
        self.assertEqual(
            req["path"],
            "/v1/runs/run%2Fwith%20spaces:report?workspace_id=ws%2Fspecial%26chars",
        )

    # ------------------------------------------------------------------
    # Error handling
    # ------------------------------------------------------------------

    def test_report_raises_reporter_error_on_non_202(self):
        ReportHandler.response_status = 500
        reporter = self._make_reporter()

        with self.assertRaises(ReporterError) as raised:
            reporter.report("progress")

        self.assertEqual(raised.exception.status, 500)
        self.assertIn("202", str(raised.exception))
        self.assertIn("500", str(raised.exception))

    # ------------------------------------------------------------------
    # Context manager
    # ------------------------------------------------------------------

    def test_context_manager_calls_report_exit_0_on_clean_exit(self):
        reporter = self._make_reporter()

        with reporter:
            reporter.report("started")

        self.assertEqual(len(ReportHandler.requests), 2)
        self.assertEqual(ReportHandler.requests[1]["body"], {"type": "exit", "exit_code": 0})

    def test_context_manager_calls_report_exit_1_on_exception(self):
        reporter = self._make_reporter()

        with self.assertRaises(ValueError):
            with reporter:
                raise ValueError("oops")

        self.assertEqual(len(ReportHandler.requests), 1)
        self.assertEqual(ReportHandler.requests[0]["body"], {"type": "exit", "exit_code": 1})

    def test_context_manager_does_not_suppress_original_exception(self):
        reporter = self._make_reporter()

        with self.assertRaises(RuntimeError):
            with reporter:
                raise RuntimeError("should propagate")


# ---------------------------------------------------------------------------
# run_reporter() factory
# ---------------------------------------------------------------------------

class RunReporterFactoryTests(unittest.TestCase):
    FULL_ENV = {
        "MERCATOR_REPORT_URL": "https://pub.example",
        "MERCATOR_RUN_ID": "run_1",
        "MERCATOR_WORKSPACE_ID": "ws_42",
        "MERCATOR_RUN_TOKEN": "tok",
    }

    def _env_without(self, *names):
        return {key: value for key, value in self.FULL_ENV.items() if key not in names}

    def test_returns_none_when_env_is_empty(self):
        result = run_reporter(env={})
        self.assertIsNone(result)

    def test_raises_when_report_url_missing(self):
        with self.assertRaises(ValueError) as raised:
            run_reporter(env=self._env_without("MERCATOR_REPORT_URL"))
        self.assertIn("MERCATOR_REPORT_URL", str(raised.exception))

    def test_raises_when_run_id_missing(self):
        with self.assertRaises(ValueError) as raised:
            run_reporter(env=self._env_without("MERCATOR_RUN_ID"))
        self.assertIn("MERCATOR_RUN_ID", str(raised.exception))

    def test_raises_when_run_token_missing(self):
        with self.assertRaises(ValueError) as raised:
            run_reporter(env=self._env_without("MERCATOR_RUN_TOKEN"))
        self.assertIn("MERCATOR_RUN_TOKEN", str(raised.exception))

    def test_raises_when_workspace_id_missing(self):
        # A reporter without a workspace id fails every report server-side
        # (400 WORKSPACE_REQUIRED), so construction must fail fast instead.
        with self.assertRaises(ValueError) as raised:
            run_reporter(env=self._env_without("MERCATOR_WORKSPACE_ID"))
        self.assertIn("MERCATOR_WORKSPACE_ID", str(raised.exception))

    def test_raises_when_workspace_id_is_empty_string(self):
        env = dict(self.FULL_ENV, MERCATOR_WORKSPACE_ID="")
        with self.assertRaises(ValueError) as raised:
            run_reporter(env=env)
        self.assertIn("MERCATOR_WORKSPACE_ID", str(raised.exception))

    def test_returns_reporter_when_all_required_vars_present(self):
        result = run_reporter(env=dict(self.FULL_ENV))
        self.assertIsInstance(result, Reporter)

    def test_workspace_id_is_read_from_env(self):
        reporter = run_reporter(env={
            "MERCATOR_REPORT_URL": "https://pub.example",
            "MERCATOR_RUN_ID": "run_1",
            "MERCATOR_WORKSPACE_ID": "ws_42",
            "MERCATOR_RUN_TOKEN": "tok",
        })
        self.assertIsNotNone(reporter)
        self.assertEqual(reporter._workspace_id, "ws_42")

    def test_run_reporter_with_live_server(self):
        """run_reporter() wires up the env vars correctly end-to-end."""
        server = ThreadingHTTPServer(("127.0.0.1", 0), ReportHandler)
        thread = threading.Thread(target=server.serve_forever)
        thread.daemon = True
        thread.start()
        host, port = server.server_address
        base_url = f"http://{host}:{port}"
        ReportHandler.requests = []
        ReportHandler.response_status = 202
        try:
            reporter = run_reporter(env={
                "MERCATOR_REPORT_URL": base_url,
                "MERCATOR_RUN_ID": "run_env",
                "MERCATOR_WORKSPACE_ID": "ws_env",
                "MERCATOR_RUN_TOKEN": "tok_env",
            })
            self.assertIsNotNone(reporter)
            reporter.report("ping")
            self.assertEqual(len(ReportHandler.requests), 1)
            req = ReportHandler.requests[0]
            self.assertEqual(req["path"], "/v1/runs/run_env:report?workspace_id=ws_env")
            self.assertEqual(req["headers"]["Authorization"], "Bearer tok_env")
        finally:
            server.shutdown()
            thread.join(timeout=5)
            server.server_close()


if __name__ == "__main__":
    unittest.main()
