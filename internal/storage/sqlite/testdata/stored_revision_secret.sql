-- One workload with one stored revision, written the way the revision door used
-- to write it: the whole revision in the public payload and nothing private, so
-- the token the caller put in the container's environment is readable by every
-- reader of the public log. Nothing here is a legacy event name or a legacy
-- vocabulary; the only wrong thing is which payload the revision is in.
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
  'evt_workload_wrk_1_created', 'ws_1', 'workload', 'wrk_1', 1,
  'compute.workload.created.v1', 1, '2026-07-20T12:00:00Z',
  '{}', 'wrk_1', '',
  'workload:create:wrk_1', 'sha256:stored-revision-workload', 'public',
  '{"workload_id":"wrk_1","name":"trainer"}',
  NULL
),
(
  'evt_workload_wrk_1_revision_wrev_1_created', 'ws_1', 'workload', 'wrk_1', 2,
  'compute.workload.revision_created.v1', 1, '2026-07-20T12:00:01Z',
  '{}', 'wrk_1', '',
  'workload:revision:create:wrev_1', 'sha256:stored-revision', 'public',
  '{"revision":{"id":"wrev_1","workspace_id":"ws_1","workload_id":"wrk_1","digest":"sha256:revision","spec":{"containers":[{"name":"main","image":"ghcr.io/acme/trainer@sha256:0000000000000000000000000000000000000000000000000000000000000000","platform":{"os":"linux","architecture":"amd64"},"env":{"HF_TOKEN":{"value":"hf_live_SECRETVALUE"},"EMPTY":{}}}],"resources":{"cpu":{"min_millis":1000},"memory":{"min_bytes":1073741824},"ephemeral_disk":{"min_bytes":1073741824}},"network":{"inbound":"none"},"placement":{"service_class":"standard","expected_runtime_seconds":60},"execution":{"max_runtime_seconds":120,"max_pre_start_attempts":3},"artifacts":{}}}}',
  NULL
);
