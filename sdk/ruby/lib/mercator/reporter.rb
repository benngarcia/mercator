require "cgi"
require "json"
require "net/http"
require "uri"

module Mercator
  # Raised when the Mercator report endpoint returns a non-202 response (or the
  # request fails in transit, in which case `status` is 0).
  class ReporterError < StandardError
    attr_reader :status, :body

    def initialize(status, body)
      @status = status
      @body = body
      super("Mercator reporter: expected HTTP 202, got #{status}: #{body}")
    end
  end

  # Posts structured events to Mercator from inside a running workload.
  #
  # Usage inside a workload container:
  #
  #     reporter = Mercator::Reporter.from_env
  #     if reporter
  #       reporter.report("started")
  #       # ... do work ...
  #       reporter.report("progress", { "pct" => 50 })
  #       reporter.report_exit(0)
  #     end
  #
  # Obtain an instance via {Reporter.from_env} rather than constructing one
  # directly.
  class Reporter
    # Environment variables Mercator injects into workload containers. All four
    # are required for a working reporter — the server rejects reports without
    # a workspace_id (400 WORKSPACE_REQUIRED), so a partially populated
    # environment is a misconfiguration, not "running outside Mercator".
    REQUIRED_ENV_VARS = %w[
      MERCATOR_REPORT_URL
      MERCATOR_RUN_ID
      MERCATOR_WORKSPACE_ID
      MERCATOR_RUN_TOKEN
    ].freeze

    # Build a Reporter from the environment variables injected by Mercator.
    #
    # Returns nil (without raising) when none of the Mercator variables are
    # set, so workloads running outside Mercator degrade gracefully. Raises
    # ArgumentError when the environment is only partially populated (some
    # variables set, some missing/empty) — every report from such a reporter
    # would fail server-side, so fail fast at construction instead.
    def self.from_env(env = ENV)
      values = REQUIRED_ENV_VARS.to_h { |name| [name, env[name].to_s] }
      missing = REQUIRED_ENV_VARS.select { |name| values[name].empty? }

      return nil if missing.length == REQUIRED_ENV_VARS.length # not running under Mercator

      unless missing.empty?
        raise ArgumentError,
              "Mercator reporter environment is incomplete; missing or empty: #{missing.join(', ')}"
      end

      new(
        run_id: values["MERCATOR_RUN_ID"],
        workspace_id: values["MERCATOR_WORKSPACE_ID"],
        report_url: values["MERCATOR_REPORT_URL"],
        token: values["MERCATOR_RUN_TOKEN"]
      )
    end

    def initialize(run_id:, workspace_id:, report_url:, token:, timeout: 30.0)
      @run_id = run_id
      @workspace_id = workspace_id
      @report_url = report_url.to_s.sub(%r{/+\z}, "")
      @token = token
      @timeout = timeout
    end

    # POST an event to Mercator.
    #
    # `type` is the event type string (e.g. `"progress"`); `data` is an
    # optional structured payload attached to the event.
    def report(type, data = nil)
      payload = { "type" => type }
      payload["data"] = data unless data.nil?
      post(payload)
    end

    # POST an exit event with the given exit code.
    def report_exit(code)
      post({ "type" => "exit", "exit_code" => code })
    end

    private

    def report_uri
      run_id_enc = CGI.escape(@run_id).gsub("+", "%20")
      query = URI.encode_www_form("workspace_id" => @workspace_id)
      URI.parse("#{@report_url}/v1/runs/#{run_id_enc}:report?#{query}")
    end

    def post(payload)
      uri = report_uri
      http = Net::HTTP.new(uri.host, uri.port)
      http.use_ssl = uri.scheme == "https"
      http.open_timeout = @timeout unless @timeout.nil?
      http.read_timeout = @timeout unless @timeout.nil?

      request = Net::HTTP::Post.new(uri.request_uri)
      request["Authorization"] = "Bearer #{@token}"
      request["Content-Type"] = "application/json"
      request["Accept"] = "application/json"
      # Set an explicit User-Agent. Some proxies (e.g. Cloudflare's managed
      # rules) reject default agent strings with HTTP 403, which would
      # silently drop reports through a Cloudflare-fronted Mercator.
      request["User-Agent"] = "mercator-reporter (ruby)"
      request.body = JSON.generate(payload)

      response = http.request(request)
      return if response.code.to_i == 202

      raise ReporterError.new(response.code.to_i, response.body.to_s)
    rescue Timeout::Error, SystemCallError, SocketError, IOError => e
      raise ReporterError.new(0, e.message)
    end
  end
end
