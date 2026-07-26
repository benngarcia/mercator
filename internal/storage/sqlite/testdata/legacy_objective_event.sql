-- One closed Run recorded before a Run stated the kind of work it is. The
-- request carries the placement objective in both the public event and the
-- private one, and the Booking Decision carries the objective it was taken
-- under. Nothing here is a legacy event NAME: the names are current, and the
-- only stale thing is the vocabulary inside them, which is what makes this the
-- fixture for the service class rename rather than for the booking rename.
CREATE TABLE events (
  global_position INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL UNIQUE,
  workspace_id TEXT NOT NULL,
  stream_type TEXT NOT NULL,
  stream_id TEXT NOT NULL,
  stream_version INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  schema_version INTEGER NOT NULL,
  occurred_at TEXT NOT NULL,
  actor_json BLOB,
  correlation_id TEXT,
  causation_id TEXT,
  command_key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  visibility TEXT NOT NULL,
  data_json BLOB,
  private_data BLOB,
  UNIQUE(workspace_id, stream_type, stream_id, stream_version)
);

INSERT INTO events (
  event_id, workspace_id, stream_type, stream_id, stream_version,
  event_type, schema_version, occurred_at, actor_json, correlation_id,
  causation_id, command_key, request_hash, visibility, data_json, private_data
) VALUES (
  'evt_run_2_requested', 'ws_1', 'run', 'run_2', 1,
  'compute.run.requested.v1', 1, '2026-07-20T12:00:00Z',
  '{}', 'run_2', '',
  'run:create:run_2', 'sha256:legacy-objective', 'public',
  '{"run_id":"run_2","workload_revision":{"id":"wrev_2","spec":{"placement":{"objective":"fastest_start","expected_runtime_seconds":60}}}}',
  '{"run_id":"run_2","workload_revision":{"id":"wrev_2","spec":{"placement":{"objective":"fastest_start","expected_runtime_seconds":60}}}}'
);

INSERT INTO events (
  event_id, workspace_id, stream_type, stream_id, stream_version,
  event_type, schema_version, occurred_at, actor_json, correlation_id,
  causation_id, command_key, request_hash, visibility, data_json
) VALUES (
  'evt_run_2_booking_decided', 'ws_1', 'run', 'run_2', 2,
  'compute.run.booking_decided.v1', 1, '2026-07-20T12:00:01Z',
  '{}', 'run_2', '',
  'advance:placement:attempt_1', 'sha256:legacy-objective-decision',
  'public', '{"decision":{"id":"decision_2","run_id":"run_2","workload_revision_digest":"sha256:workload","evaluated_at":"2026-07-20T12:00:01Z","model_version":"latency-v1","policy":{"objective":"cheapest","expected_runtime_seconds":60},"collection_report":{},"candidates":[],"selected_offer_snapshot_id":"offer_1","booking":{"id":"booking_2","run_id":"run_2","rental_id":"rental_2","state":"running","schedule_version":1},"selection_reason_codes":["FEASIBLE","LOWEST_SCORE"]}}'
);

INSERT INTO events (
  event_id, workspace_id, stream_type, stream_id, stream_version,
  event_type, schema_version, occurred_at, actor_json, correlation_id,
  causation_id, command_key, request_hash, visibility, data_json
) VALUES (
  'evt_run_2_closed', 'ws_1', 'run', 'run_2', 3,
  'compute.run.closed.v1', 1, '2026-07-20T12:10:00Z',
  '{}', 'run_2', '',
  'advance:cleanup', 'sha256:legacy-objective-close',
  'public', '{"closed":true}'
);
