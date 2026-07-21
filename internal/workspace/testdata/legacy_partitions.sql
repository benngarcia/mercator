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
  event_type, schema_version, occurred_at, command_key, request_hash,
  visibility, data_json
) VALUES
  ('evt_staging_later', 'staging', 'run', 'run_later', 1,
   'run.requested', 1, '2026-07-18T12:00:00Z', 'run:create:later',
   'sha256:later', 'public', '{}'),
  ('evt_experiments_first', 'staging-experiments', 'connection', 'conn_first', 1,
   'connection.created', 1, '2026-07-17T09:30:00Z', 'connection:create:first',
   'sha256:experiments', 'public', '{}'),
  ('evt_staging_first', 'staging', 'connection', 'conn_first', 1,
   'connection.created', 1, '2026-07-16T08:15:00Z', 'connection:create:first',
   'sha256:staging', 'public', '{}');
