$LOAD_PATH.unshift(File.expand_path("../lib", __dir__))

require "json"
require "minitest/autorun"
require "webrick"

require "mercator"

class ReportServlet < WEBrick::HTTPServlet::AbstractServlet
  class << self
    attr_accessor :requests, :response_status
  end

  self.requests = []
  self.response_status = 202

  def do_POST(request, response)
    body = request.body && !request.body.empty? ? JSON.parse(request.body) : nil
    request_path = request.request_uri.to_s.sub(%r{\Ahttps?://[^/]+}, "")
    self.class.requests << {
      method: request.request_method,
      path: request_path,
      headers: request.header.transform_values(&:first),
      body: body
    }
    response.status = self.class.response_status
    response["Content-Type"] = "application/json"
    response.body = JSON.generate({ ok: self.class.response_status == 202 })
  end
end

class ReporterTest < Minitest::Test
  def setup
    ReportServlet.requests = []
    ReportServlet.response_status = 202
    @server = WEBrick::HTTPServer.new(
      BindAddress: "127.0.0.1",
      Port: 0,
      Logger: WEBrick::Log.new(File::NULL),
      AccessLog: []
    )
    @server.mount "/", ReportServlet
    @thread = Thread.new { @server.start }
    @base_url = "http://127.0.0.1:#{@server.config[:Port]}"
  end

  def teardown
    @server.shutdown
    @thread.join(5)
  end

  def make_reporter(run_id: "run_abc", workspace_id: "ws_xyz", token: "tok_secret")
    Mercator::Reporter.new(
      run_id: run_id,
      workspace_id: workspace_id,
      report_url: @base_url,
      token: token
    )
  end

  def test_report_posts_to_correct_url_with_auth_and_body
    reporter = make_reporter

    reporter.report("progress", { "pct" => 50 })

    assert_equal 1, ReportServlet.requests.length
    request = ReportServlet.requests[0]
    assert_equal "POST", request.fetch(:method)
    assert_equal "/v1/runs/run_abc:report?workspace_id=ws_xyz", request.fetch(:path)
    assert_equal "Bearer tok_secret", request.fetch(:headers).fetch("authorization")
    assert_includes request.fetch(:headers).fetch("content-type"), "application/json"
    assert_equal({ "type" => "progress", "data" => { "pct" => 50 } }, request.fetch(:body))
  end

  def test_report_omits_data_when_not_provided
    reporter = make_reporter

    reporter.report("started")

    assert_equal({ "type" => "started" }, ReportServlet.requests[0].fetch(:body))
  end

  def test_report_exit_posts_exit_event_with_exit_code
    reporter = make_reporter

    reporter.report_exit(0)

    request = ReportServlet.requests[0]
    assert_equal "/v1/runs/run_abc:report?workspace_id=ws_xyz", request.fetch(:path)
    assert_equal({ "type" => "exit", "exit_code" => 0 }, request.fetch(:body))
  end

  def test_report_exit_encodes_non_zero_exit_code
    reporter = make_reporter

    reporter.report_exit(1)

    assert_equal({ "type" => "exit", "exit_code" => 1 }, ReportServlet.requests[0].fetch(:body))
  end

  def test_run_id_and_workspace_id_with_special_chars_are_url_encoded
    reporter = Mercator::Reporter.new(
      run_id: "run/with spaces",
      workspace_id: "ws/special&chars",
      report_url: @base_url,
      token: "tok"
    )

    reporter.report("test")

    assert_equal(
      "/v1/runs/run%2Fwith%20spaces:report?workspace_id=ws%2Fspecial%26chars",
      ReportServlet.requests[0].fetch(:path)
    )
  end

  def test_report_raises_reporter_error_on_non_202
    ReportServlet.response_status = 500
    reporter = make_reporter

    error = assert_raises(Mercator::ReporterError) { reporter.report("progress") }

    assert_equal 500, error.status
    assert_includes error.message, "202"
    assert_includes error.message, "500"
  end
end

class ReporterFromEnvTest < Minitest::Test
  FULL_ENV = {
    "MERCATOR_REPORT_URL" => "https://pub.example",
    "MERCATOR_RUN_ID" => "run_1",
    "MERCATOR_WORKSPACE_ID" => "ws_42",
    "MERCATOR_RUN_TOKEN" => "tok"
  }.freeze

  def env_without(*names)
    FULL_ENV.reject { |key, _value| names.include?(key) }
  end

  def test_returns_nil_when_env_is_empty
    assert_nil Mercator::Reporter.from_env({})
  end

  def test_raises_when_report_url_missing
    error = assert_raises(ArgumentError) { Mercator::Reporter.from_env(env_without("MERCATOR_REPORT_URL")) }
    assert_includes error.message, "MERCATOR_REPORT_URL"
  end

  def test_raises_when_run_id_missing
    error = assert_raises(ArgumentError) { Mercator::Reporter.from_env(env_without("MERCATOR_RUN_ID")) }
    assert_includes error.message, "MERCATOR_RUN_ID"
  end

  def test_raises_when_run_token_missing
    error = assert_raises(ArgumentError) { Mercator::Reporter.from_env(env_without("MERCATOR_RUN_TOKEN")) }
    assert_includes error.message, "MERCATOR_RUN_TOKEN"
  end

  def test_raises_when_workspace_id_missing
    # A reporter without a workspace id fails every report server-side
    # (400 WORKSPACE_REQUIRED), so construction must fail fast instead.
    error = assert_raises(ArgumentError) { Mercator::Reporter.from_env(env_without("MERCATOR_WORKSPACE_ID")) }
    assert_includes error.message, "MERCATOR_WORKSPACE_ID"
  end

  def test_raises_when_workspace_id_is_empty_string
    env = FULL_ENV.merge("MERCATOR_WORKSPACE_ID" => "")
    error = assert_raises(ArgumentError) { Mercator::Reporter.from_env(env) }
    assert_includes error.message, "MERCATOR_WORKSPACE_ID"
  end

  def test_returns_reporter_when_all_required_vars_present
    reporter = Mercator::Reporter.from_env(FULL_ENV)
    assert_instance_of Mercator::Reporter, reporter
  end

  def test_from_env_with_live_server
    server = WEBrick::HTTPServer.new(
      BindAddress: "127.0.0.1",
      Port: 0,
      Logger: WEBrick::Log.new(File::NULL),
      AccessLog: []
    )
    server.mount "/", ReportServlet
    thread = Thread.new { server.start }
    base_url = "http://127.0.0.1:#{server.config[:Port]}"
    ReportServlet.requests = []
    ReportServlet.response_status = 202

    begin
      reporter = Mercator::Reporter.from_env(FULL_ENV.merge(
        "MERCATOR_REPORT_URL" => base_url,
        "MERCATOR_RUN_ID" => "run_env",
        "MERCATOR_WORKSPACE_ID" => "ws_env",
        "MERCATOR_RUN_TOKEN" => "tok_env"
      ))
      refute_nil reporter
      reporter.report("ping")

      assert_equal 1, ReportServlet.requests.length
      request = ReportServlet.requests[0]
      assert_equal "/v1/runs/run_env:report?workspace_id=ws_env", request.fetch(:path)
      assert_equal "Bearer tok_env", request.fetch(:headers).fetch("authorization")
    ensure
      server.shutdown
      thread.join(5)
    end
  end
end
