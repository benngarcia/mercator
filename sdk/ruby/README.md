# Mercator Ruby SDK

Small, dependency-free Ruby client for the Mercator V1 HTTP API.

## Run an image, get the exit code

```ruby
require "mercator"

client = Mercator::Client.new(
  "http://127.0.0.1:8080",
  token: "dev-token",
  workspace_id: "ws_1"
)

created = client.run_image(
  "busybox",
  args: ["echo", "hi"]
)
run_id = created.fetch("run_id") # == created.fetch("run").fetch("id")

result = client.wait_run_until_terminal(run_id)
run = result.fetch("run")
puts "#{run.fetch('outcome')} #{run.fetch('exit_code')}" # => succeeded 0
```

`run_image` generates a `run_id` when you omit one and derives a stable
`Idempotency-Key` from it (`"#{run_id}:create"`). Pass `idempotency_key:` only
when you need to coordinate retries with an external caller.

After the run closes, read the public event stream and placement decision from
the same client:

```ruby
events = client.list_run_events(run_id)
puts events.fetch("events").map { |event| event.fetch("type") }
# => [..., "compute.run.closed.v1"]

decision = client.get_run_decision(run_id).fetch("decision")
puts decision.fetch("selected_offer_snapshot_id")
# => offer_local_fake
```

## Install from source

The Ruby gem is not published to RubyGems for the first public launch. Install
it from a Mercator source checkout instead.

For a Bundler-managed application, add the local checkout to your `Gemfile`:

```ruby
gem "mercator-sdk", path: "/path/to/mercator/sdk/ruby"
```

Then run:

```sh
bundle install
```

For a one-off local gem install from the checkout:

```sh
cd sdk/ruby
gem build mercator-sdk.gemspec
gem install ./mercator-sdk-0.1.0.gem
```

## Local development

From the repository checkout:

```sh
cd sdk/ruby
bundle install
bundle exec ruby -Ilib:test test/test_client.rb
```

The SDK uses only Ruby standard-library runtime modules: `Net::HTTP`, `URI`,
`JSON`, and `Timeout`. Tests use WEBrick as a development dependency.

## Client

```ruby
client = Mercator::Client.new(
  "http://127.0.0.1:8080",
  token: "dev-token",
  workspace_id: "ws_1",
  timeout: 30.0
)
```

`Authorization: Bearer <token>` is sent on requests when `token` is set.
`workspace_id` on the client is applied to reads as the `workspace_id` query
parameter and to `create_run` as the body field unless the body already has one.
Pass `workspace_id:` to a method to override the default for that call.

## Methods

- `health_live`, `health_ready`, `get_openapi`
- `run_image(image, args: nil, env: nil, run_id: nil, workspace_id: nil, idempotency_key: nil)`
- `list_runs(workspace_id: nil)`, `create_run(payload, idempotency_key:, workspace_id: nil)`
- `get_run(run_id, workspace_id: nil)`, `wait_run(run_id, workspace_id: nil)`
- `wait_run_until_terminal(run_id, workspace_id: nil, deadline: 300.0)`
- `refresh_run(run_id, workspace_id: nil)`, `cancel_run(run_id, workspace_id: nil)`
- `list_run_events(run_id, workspace_id: nil)`, `get_run_decision(run_id, workspace_id: nil)`
- `preview_placement(payload)`
- `list_connections(workspace_id: nil)`, `list_offers(workspace_id: nil)`
- `create_workload(workspace_id, workload_id, name, idempotency_key:)`
- `list_workload_revisions(workload_id, workspace_id: nil)`
- `create_workload_revision(workload_id, workspace_id, revision, idempotency_key:)`
- `get_workload_revision(workload_id, revision_id, workspace_id: nil)`
- `resolve_image(image, platform)`
- `get_sink_status(sink_id)`, `deliver_sink(sink_id)`
- `replay_sink(sink_id, from_exclusive: nil, limit: nil, replay_id: nil)`
- `request(method, path, query: nil, json_body: nil, headers: nil, idempotency_key: nil)`

Flexible API objects such as workloads, revisions, decisions, offers, and
events are returned as hashes because the current V1 OpenAPI contract leaves
many nested shapes intentionally open.

## Errors

Non-2xx API responses raise `Mercator::Error` with:

- `status_code`
- `code`
- `message`
- `details`
- `response`

Transport failures also raise `Mercator::Error` with `code == "REQUEST_FAILED"`
and `status_code == nil`.
