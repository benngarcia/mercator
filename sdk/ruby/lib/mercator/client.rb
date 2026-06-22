require "json"
require "net/http"
require "cgi"
require "securerandom"
require "time"
require "uri"

require_relative "error"
require_relative "version"

module Mercator
  class Client
    DEFAULT_USER_AGENT = "mercator-ruby/#{Mercator::VERSION}"

    attr_reader :base_url, :token, :workspace_id, :timeout, :user_agent

    def initialize(base_url, token: nil, workspace_id: nil, timeout: 30.0, user_agent: DEFAULT_USER_AGENT)
      normalized = base_url.to_s.sub(%r{/+\z}, "")
      raise ArgumentError, "base_url must not be empty" if normalized.empty?

      @base_url = normalized
      @token = token
      @workspace_id = workspace_id
      @timeout = timeout
      @user_agent = user_agent
    end

    def request(method, path, query: nil, json_body: nil, headers: nil, idempotency_key: nil)
      raise ArgumentError, "path must start with '/'" unless path.start_with?("/")

      uri = build_uri(path, query)
      http = Net::HTTP.new(uri.host, uri.port)
      http.use_ssl = uri.scheme == "https"
      http.open_timeout = timeout unless timeout.nil?
      http.read_timeout = timeout unless timeout.nil?

      request = request_class(method).new(uri.request_uri)
      request["Accept"] = "application/json"
      request["User-Agent"] = user_agent
      request["Authorization"] = "Bearer #{token}" unless token.nil?
      request["Idempotency-Key"] = idempotency_key unless idempotency_key.nil?
      headers&.each { |key, value| request[key] = value }
      unless json_body.nil?
        request["Content-Type"] ||= "application/json"
        request.body = JSON.generate(json_body)
      end

      response = http.request(request)
      payload = decode_response(response)
      return payload if response.is_a?(Net::HTTPSuccess)

      raise api_error(response, payload)
    rescue Timeout::Error, SystemCallError, SocketError, IOError, JSON::ParserError => e
      raise Error.new(nil, "REQUEST_FAILED", e.message)
    end

    def health_live
      request("GET", "/health/live")
    end

    def health_ready
      request("GET", "/health/ready")
    end

    def get_openapi
      request("GET", "/openapi.json")
    end

    def list_runs(workspace_id: nil)
      request("GET", "/v1/runs", query: workspace_query(workspace_id))
    end

    def create_run(payload, idempotency_key:, workspace_id: nil)
      body = stringify_keys(payload)
      effective_workspace = workspace_id.nil? ? @workspace_id : workspace_id
      body["workspace_id"] = effective_workspace if !effective_workspace.nil? && empty_value?(body["workspace_id"])
      request("POST", "/v1/runs", json_body: body, idempotency_key: idempotency_key)
    end

    def run_image(image, args: nil, env: nil, run_id: nil, workspace_id: nil, idempotency_key: nil)
      effective_run_id = run_id || new_run_id
      body = { "image" => image }
      body["args"] = args unless args.nil? || args.empty?
      body["env"] = stringify_keys(env) unless env.nil? || env.empty?
      body["run_id"] = effective_run_id

      key = idempotency_key || "#{effective_run_id}:create"
      create_run(body, idempotency_key: key, workspace_id: workspace_id)
    end

    def get_run(run_id, workspace_id: nil)
      request("GET", "/v1/runs/#{path_segment(run_id)}", query: workspace_query(workspace_id))
    end

    def wait_run(run_id, workspace_id: nil)
      request("GET", "/v1/runs/#{path_segment(run_id)}:wait", query: workspace_query(workspace_id))
    end

    def wait_run_until_terminal(run_id, workspace_id: nil, deadline: 300.0)
      deadline_at = monotonic_time + deadline
      loop do
        response = wait_run(run_id, workspace_id: workspace_id)
        run = response.is_a?(Hash) ? response["run"] : nil
        return response if run.is_a?(Hash) && run["closed"]
        return response if monotonic_time >= deadline_at
      end
    end

    def refresh_run(run_id, workspace_id: nil)
      request("POST", "/v1/runs/#{path_segment(run_id)}:refresh", query: workspace_query(workspace_id))
    end

    def cancel_run(run_id, workspace_id: nil)
      request("POST", "/v1/runs/#{path_segment(run_id)}:cancel", query: workspace_query(workspace_id))
    end

    def list_run_events(run_id, workspace_id: nil)
      request("GET", "/v1/runs/#{path_segment(run_id)}/events", query: workspace_query(workspace_id))
    end

    def get_run_decision(run_id, workspace_id: nil)
      request("GET", "/v1/runs/#{path_segment(run_id)}/decision", query: workspace_query(workspace_id))
    end

    def preview_placement(payload)
      request("POST", "/v1/placements:preview", json_body: stringify_keys(payload))
    end

    def list_connections(workspace_id: nil)
      request("GET", "/v1/connections", query: workspace_query(workspace_id))
    end

    def list_offers(workspace_id: nil)
      request("GET", "/v1/offers", query: workspace_query(workspace_id))
    end

    def create_workload(workspace_id, workload_id, name, idempotency_key:)
      request(
        "POST",
        "/v1/workloads",
        json_body: {
          "workspace_id" => workspace_id,
          "workload_id" => workload_id,
          "name" => name
        },
        idempotency_key: idempotency_key
      )
    end

    def list_workload_revisions(workload_id, workspace_id: nil)
      request("GET", "/v1/workloads/#{path_segment(workload_id)}/revisions", query: workspace_query(workspace_id))
    end

    def create_workload_revision(workload_id, workspace_id, revision, idempotency_key:)
      request(
        "POST",
        "/v1/workloads/#{path_segment(workload_id)}/revisions",
        query: workspace_query(workspace_id),
        json_body: { "revision" => stringify_keys(revision) },
        idempotency_key: idempotency_key
      )
    end

    def get_workload_revision(workload_id, revision_id, workspace_id: nil)
      request(
        "GET",
        "/v1/workloads/#{path_segment(workload_id)}/revisions/#{path_segment(revision_id)}",
        query: workspace_query(workspace_id)
      )
    end

    def resolve_image(image, platform)
      request("POST", "/v1/images:resolve", json_body: { "image" => image, "platform" => platform })
    end

    def get_sink_status(sink_id)
      request("GET", "/v1/sinks/#{path_segment(sink_id)}")
    end

    def deliver_sink(sink_id)
      request("POST", "/v1/sinks/#{path_segment(sink_id)}:deliver")
    end

    def replay_sink(sink_id, from_exclusive: nil, limit: nil, replay_id: nil)
      request(
        "POST",
        "/v1/sinks/#{path_segment(sink_id)}:replay",
        json_body: compact_hash({
          "from_exclusive" => from_exclusive,
          "limit" => limit,
          "replay_id" => replay_id
        })
      )
    end

    private

    def request_class(method)
      case method.to_s.upcase
      when "GET"
        Net::HTTP::Get
      when "POST"
        Net::HTTP::Post
      else
        raise ArgumentError, "unsupported HTTP method #{method.inspect}"
      end
    end

    def build_uri(path, query)
      uri = URI.parse("#{base_url}#{path}")
      encoded = encode_query(query)
      uri.query = encoded unless encoded.nil? || encoded.empty?
      uri
    end

    def encode_query(query)
      return nil if query.nil? || query.empty?

      URI.encode_www_form(query.reject { |_key, value| value.nil? })
    end

    def decode_response(response)
      body = response.body.to_s
      return nil if body.empty?

      content_type = response["Content-Type"].to_s.downcase
      return body unless content_type.include?("json")

      JSON.parse(body)
    end

    def api_error(response, payload)
      code = payload.is_a?(Hash) && payload["code"].to_s != "" ? payload["code"] : response.message
      message = payload.is_a?(Hash) && payload["message"].to_s != "" ? payload["message"] : response.message
      details = payload.is_a?(Hash) ? payload["details"] : nil
      Error.new(response.code.to_i, code, message, details: details, response: payload)
    end

    def workspace_query(workspace_id)
      effective = workspace_id.nil? ? @workspace_id : workspace_id
      return {} if effective.nil?

      { "workspace_id" => effective }
    end

    def path_segment(value)
      CGI.escape(value.to_s).gsub("+", "%20")
    end

    def compact_hash(hash)
      hash.reject { |_key, value| value.nil? }
    end

    def empty_value?(value)
      value.nil? || value == ""
    end

    def stringify_keys(value)
      case value
      when Hash
        value.each_with_object({}) do |(key, nested_value), result|
          result[key.to_s] = stringify_keys(nested_value)
        end
      when Array
        value.map { |item| stringify_keys(item) }
      else
        value
      end
    end

    def monotonic_time
      Process.clock_gettime(Process::CLOCK_MONOTONIC)
    end

    def new_run_id
      "run_#{SecureRandom.uuid}"
    end
  end
end
