$LOAD_PATH.unshift(File.expand_path("../lib", __dir__))

require "json"
require "minitest/autorun"
require "webrick"

require "mercator"

class RecordingServlet < WEBrick::HTTPServlet::AbstractServlet
  class << self
    attr_accessor :requests
  end

  self.requests = []

  def do_GET(request, response)
    record_and_respond(request, response)
  end

  def do_POST(request, response)
    record_and_respond(request, response)
  end

  private

  def record_and_respond(request, response)
    body = request.body && !request.body.empty? ? JSON.parse(request.body) : nil
    request_path = request.request_uri.to_s.sub(%r{\Ahttps?://[^/]+}, "")
    self.class.requests << {
      method: request.request_method,
      path: request_path,
      headers: request.header.transform_values(&:first),
      body: body
    }

    if request_path.start_with?("/v1/runs/missing")
      return send_json(response, 404, {
        code: "RUN_NOT_FOUND",
        message: "run was not found",
        details: [{ field: "run_id" }]
      })
    end

    if request.request_method == "POST" && request.path == "/v1/runs"
      run_id = body.fetch("run_id", "run_generated_1")
      return send_json(response, 202, {
        run_id: run_id,
        run: {
          id: run_id,
          workspace_id: body.fetch("workspace_id", ""),
          phase: "requested",
          cleanup: "not_required",
          disposition: "release",
          closed: false
        },
        duplicate: false
      })
    end

    if request_path.start_with?("/v1/runs/run%201")
      return send_json(response, 200, { run: { id: "run 1" } })
    end

    send_json(response, request.request_method == "GET" ? 200 : 202, { ok: true })
  end

  def send_json(response, status, payload)
    response.status = status
    response["Content-Type"] = "application/json"
    response.body = JSON.generate(payload)
  end
end

class ClientTest < Minitest::Test
  def setup
    RecordingServlet.requests = []
    @server = WEBrick::HTTPServer.new(
      BindAddress: "127.0.0.1",
      Port: 0,
      Logger: WEBrick::Log.new(File::NULL),
      AccessLog: []
    )
    @server.mount "/", RecordingServlet
    @thread = Thread.new { @server.start }
    @base_url = "http://127.0.0.1:#{@server.config[:Port]}"
  end

  def teardown
    @server.shutdown
    @thread.join(5)
  end

  def test_create_run_sends_auth_json_idempotency_key_and_decodes_response
    client = Mercator::Client.new(@base_url, token: "secret-token")

    result = client.create_run(
      {
        "workspace_id" => "ws_1",
        "run_id" => "run_1",
        "workload" => { "workspace_id" => "ws_1" }
      },
      idempotency_key: "idem-1"
    )

    assert_equal "run_1", result.fetch("run").fetch("id")
    assert_equal "run_1", result.fetch("run_id")
    assert_equal result.fetch("run_id"), result.fetch("run").fetch("id")
    assert_equal "release", result.fetch("run").fetch("disposition")
    assert_equal false, result.fetch("duplicate")
    request = RecordingServlet.requests.last
    assert_equal "POST", request.fetch(:method)
    assert_equal "/v1/runs", request.fetch(:path)
    assert_equal "Bearer secret-token", request.fetch(:headers).fetch("authorization")
    assert_equal "idem-1", request.fetch(:headers).fetch("idempotency-key")
    assert_equal "application/json", request.fetch(:headers).fetch("accept")
    assert_includes request.fetch(:headers).fetch("content-type"), "application/json"
    assert_equal "run_1", request.fetch(:body).fetch("run_id")
  end

  def test_get_run_encodes_path_and_query_parameters
    client = Mercator::Client.new(@base_url, token: "secret-token")

    result = client.get_run("run 1", workspace_id: "ws/1")

    assert_equal({ "run" => { "id" => "run 1" } }, result)
    assert_equal "/v1/runs/run%201?workspace_id=ws%2F1", RecordingServlet.requests.last.fetch(:path)
  end

  def test_http_errors_raise_mercator_error_with_error_payload
    client = Mercator::Client.new(@base_url, token: "secret-token")

    error = assert_raises(Mercator::Error) do
      client.get_run("missing", workspace_id: "ws_1")
    end

    assert_equal 404, error.status_code
    assert_equal "RUN_NOT_FOUND", error.code
    assert_equal "run was not found", error.message
    assert_equal [{ "field" => "run_id" }], error.details
    assert_includes error.to_s, "RUN_NOT_FOUND"
  end

  def test_main_v1_methods_map_to_expected_routes
    client = Mercator::Client.new(@base_url, token: "secret-token")

    client.list_runs(workspace_id: "ws_1")
    client.wait_run("run_1", workspace_id: "ws_1")
    client.refresh_run("run_1", workspace_id: "ws_1")
    client.cancel_run("run_1", workspace_id: "ws_1")
    client.list_run_events("run_1", workspace_id: "ws_1")
    client.get_run_decision("run_1", workspace_id: "ws_1")
    client.preview_placement({ "workspace_id" => "ws_1", "workload" => { "workspace_id" => "ws_1" } })
    client.list_connections(workspace_id: "ws_1")
    client.list_offers(workspace_id: "ws_1")
    client.create_workload("ws_1", "workload_1", "demo", idempotency_key: "workload-key")
    client.list_workload_revisions("workload_1", workspace_id: "ws_1")
    client.create_workload_revision("workload_1", "ws_1", { "id" => "rev_1" }, idempotency_key: "revision-key")
    client.get_workload_revision("workload_1", "rev_1", workspace_id: "ws_1")
    client.resolve_image("repo/image:tag", "linux/amd64")
    client.get_sink_status("audit")
    client.deliver_sink("audit")
    client.replay_sink("audit", from_exclusive: 10, limit: 50, replay_id: "replay_1")

    assert_equal [
      ["GET", "/v1/runs?workspace_id=ws_1"],
      ["GET", "/v1/runs/run_1:wait?workspace_id=ws_1"],
      ["POST", "/v1/runs/run_1:refresh?workspace_id=ws_1"],
      ["POST", "/v1/runs/run_1:cancel?workspace_id=ws_1"],
      ["GET", "/v1/runs/run_1/events?workspace_id=ws_1"],
      ["GET", "/v1/runs/run_1/decision?workspace_id=ws_1"],
      ["POST", "/v1/placements:preview"],
      ["GET", "/v1/connections?workspace_id=ws_1"],
      ["GET", "/v1/offers?workspace_id=ws_1"],
      ["POST", "/v1/workloads"],
      ["GET", "/v1/workloads/workload_1/revisions?workspace_id=ws_1"],
      ["POST", "/v1/workloads/workload_1/revisions?workspace_id=ws_1"],
      ["GET", "/v1/workloads/workload_1/revisions/rev_1?workspace_id=ws_1"],
      ["POST", "/v1/images:resolve"],
      ["GET", "/v1/sinks/audit"],
      ["POST", "/v1/sinks/audit:deliver"],
      ["POST", "/v1/sinks/audit:replay"]
    ], RecordingServlet.requests.map { |request| [request.fetch(:method), request.fetch(:path)] }
    assert_equal "workload-key", RecordingServlet.requests[9].fetch(:headers).fetch("idempotency-key")
    assert_equal "revision-key", RecordingServlet.requests[11].fetch(:headers).fetch("idempotency-key")
    assert_equal(
      { "from_exclusive" => 10, "limit" => 50, "replay_id" => "replay_1" },
      RecordingServlet.requests[16].fetch(:body)
    )
  end

  def test_client_scoped_workspace_id_applies_and_is_overridable
    client = Mercator::Client.new(@base_url, token: "secret-token", workspace_id: "ws_default")

    client.create_run({ "run_id" => "run_1", "workload" => { "workspace_id" => "ws_default" } }, idempotency_key: "idem-1")
    client.get_run("run_1")
    client.get_run("run_1", workspace_id: "ws_override")

    assert_equal "ws_default", RecordingServlet.requests[0].fetch(:body).fetch("workspace_id")
    assert_equal "/v1/runs/run_1?workspace_id=ws_default", RecordingServlet.requests[1].fetch(:path)
    assert_equal "/v1/runs/run_1?workspace_id=ws_override", RecordingServlet.requests[2].fetch(:path)
  end

  def test_explicit_workspace_id_in_create_body_is_not_overwritten
    client = Mercator::Client.new(@base_url, token: "secret-token", workspace_id: "ws_default")

    client.create_run({ "run_id" => "run_1", "workspace_id" => "ws_explicit" }, idempotency_key: "idem-1")

    assert_equal "ws_explicit", RecordingServlet.requests[0].fetch(:body).fetch("workspace_id")
  end

  def test_run_image_shorthand_omits_run_id_and_returns_generated_id
    client = Mercator::Client.new(@base_url, token: "secret-token", workspace_id: "ws_default")

    result = client.run_image("busybox", args: ["echo", "hi"], idempotency_key: "idem-shorthand")

    body = RecordingServlet.requests[0].fetch(:body)
    assert_equal "busybox", body.fetch("image")
    assert_equal ["echo", "hi"], body.fetch("args")
    refute body.key?("run_id")
    assert_equal "ws_default", body.fetch("workspace_id")
    assert_equal "idem-shorthand", RecordingServlet.requests[0].fetch(:headers).fetch("idempotency-key")
    assert_equal "run_generated_1", result.fetch("run").fetch("id")
  end

  def test_run_image_shorthand_honors_explicit_run_id_and_env
    client = Mercator::Client.new(@base_url, token: "secret-token", workspace_id: "ws_default")

    client.run_image(
      "busybox",
      run_id: "run_explicit",
      env: { "K" => { "value" => "v" } },
      idempotency_key: "idem-explicit"
    )

    body = RecordingServlet.requests[0].fetch(:body)
    assert_equal "run_explicit", body.fetch("run_id")
    assert_equal({ "K" => { "value" => "v" } }, body.fetch("env"))
    refute body.key?("args")
  end

  def test_run_image_shorthand_derives_stable_key_from_explicit_run_id
    client = Mercator::Client.new(@base_url, token: "secret-token", workspace_id: "ws_default")

    client.run_image("busybox", run_id: "run_explicit")

    assert_equal "run_explicit:create", RecordingServlet.requests[0].fetch(:headers).fetch("idempotency-key")
  end

  def test_run_image_requires_idempotency_key_when_run_id_omitted
    client = Mercator::Client.new(@base_url, token: "secret-token", workspace_id: "ws_default")

    assert_raises(ArgumentError) do
      client.run_image("busybox")
    end

    assert_empty RecordingServlet.requests
  end
end

class WaitServlet < WEBrick::HTTPServlet::AbstractServlet
  class << self
    attr_accessor :open_responses, :seen
  end

  self.open_responses = 0
  self.seen = 0

  def do_GET(_request, response)
    self.class.seen += 1
    closed = self.class.seen > self.class.open_responses
    response.status = closed ? 200 : 202
    response["Content-Type"] = "application/json"
    response.body = JSON.generate({
      run: {
        id: "run_1",
        workspace_id: "ws_1",
        phase: closed ? "closed" : "launch",
        outcome: closed ? "succeeded" : nil,
        exit_code: closed ? 0 : nil,
        cleanup: closed ? "confirmed" : "pending",
        closed: closed
      }
    })
  end
end

class WaitUntilTerminalTest < Minitest::Test
  def setup
    WaitServlet.seen = 0
    @server = WEBrick::HTTPServer.new(
      BindAddress: "127.0.0.1",
      Port: 0,
      Logger: WEBrick::Log.new(File::NULL),
      AccessLog: []
    )
    @server.mount "/", WaitServlet
    @thread = Thread.new { @server.start }
    @base_url = "http://127.0.0.1:#{@server.config[:Port]}"
  end

  def teardown
    @server.shutdown
    @thread.join(5)
  end

  def test_wait_run_until_terminal_repolls_until_closed
    WaitServlet.open_responses = 2
    client = Mercator::Client.new(@base_url, token: "secret-token", workspace_id: "ws_1")

    result = client.wait_run_until_terminal("run_1")

    assert_equal 3, WaitServlet.seen
    assert_equal true, result.fetch("run").fetch("closed")
    assert_equal 0, result.fetch("run").fetch("exit_code")
  end

  def test_wait_run_until_terminal_stops_at_deadline_and_returns_open_run
    WaitServlet.open_responses = 100
    client = Mercator::Client.new(@base_url, token: "secret-token", workspace_id: "ws_1")

    result = client.wait_run_until_terminal("run_1", deadline: 0.0)

    assert_equal 1, WaitServlet.seen
    assert_equal false, result.fetch("run").fetch("closed")
  end
end
