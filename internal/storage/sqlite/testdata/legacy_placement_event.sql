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
  causation_id, command_key, request_hash, visibility, data_json
) VALUES (
  'evt_run_1_placement_decided', 'ws_1', 'run', 'run_1', 1,
  'compute.run.placement_decided.v1', 1, '2026-07-20T12:00:00Z',
  '{}', '', '',
  'advance:placement:attempt_1', 'sha256:legacy-placement',
  'public', '{"decision":{"id":"decision_1","run_id":"run_1","workload_revision_digest":"sha256:workload"}}'
);
