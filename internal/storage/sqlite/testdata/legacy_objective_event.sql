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
  'evt_run_2_outcome', 'ws_1', 'run', 'run_2', 3,
  'compute.run.outcome_recorded.v1', 1, '2026-07-20T12:09:00Z',
  '{}', 'run_2', '',
  'advance:adjudicate', 'sha256:legacy-objective-outcome',
  'public', '{"outcome":"succeeded"}'
);

INSERT INTO events (
  event_id, workspace_id, stream_type, stream_id, stream_version,
  event_type, schema_version, occurred_at, actor_json, correlation_id,
  causation_id, command_key, request_hash, visibility, data_json
) VALUES (
  'evt_run_2_closed', 'ws_1', 'run', 'run_2', 4,
  'compute.run.closed.v1', 1, '2026-07-20T12:10:00Z',
  '{}', 'run_2', '',
  'advance:cleanup', 'sha256:legacy-objective-close',
  'public', '{"closed":true}'
);

-- One workload revision a caller stored before a Run stated the kind of work it
-- is. It is not a Run and never closes, so it is the site that proves the
-- refusal above is about Runs in flight rather than about any stream carrying
-- the old word. It states fastest_completion, which is the one objective no
-- other event here uses, so the class it becomes can only have come from this
-- site's own mapping.
INSERT INTO events (
  event_id, workspace_id, stream_type, stream_id, stream_version,
  event_type, schema_version, occurred_at, actor_json, correlation_id,
  causation_id, command_key, request_hash, visibility, data_json
) VALUES (
  'evt_workload_wl_1_revision_wrev_9_created', 'ws_1', 'workload', 'wl_1', 1,
  'compute.workload.revision_created.v1', 1, '2026-07-20T11:00:00Z',
  '{}', 'wl_1', '',
  'workload:revision:create:wrev_9', 'sha256:legacy-objective-revision', 'public',
  '{"revision":{"id":"wrev_9","workspace_id":"ws_1","workload_id":"wl_1","digest":"sha256:revision","spec":{"containers":[{"name":"main","image":"ghcr.io/acme/trainer@sha256:0000000000000000000000000000000000000000000000000000000000000000","platform":{"os":"linux","architecture":"amd64"}}],"resources":{"cpu":{"min_millis":1000},"memory":{"min_bytes":1073741824},"ephemeral_disk":{"min_bytes":1073741824}},"network":{"inbound":"none"},"placement":{"objective":"fastest_completion","expected_runtime_seconds":60},"execution":{"max_runtime_seconds":120,"max_pre_start_attempts":1}}}}'
);

-- The Run read model as it stood before the rename: the projection is derived
-- from the events above, and this row is what those events reduced to while the
-- vocabulary was still `objective`. It carries no service class at all, because
-- there was none to carry, and it is the copy every reader of GET /v1/runs is
-- served. A pre-migration database has this table at the current projection
-- schema version, so nothing asks for a rebuild on its own account.
CREATE TABLE runs (
  workspace_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  closed INTEGER NOT NULL,
  record_json BLOB NOT NULL,
  PRIMARY KEY(workspace_id, run_id)
);

INSERT INTO runs (workspace_id, run_id, closed, record_json) VALUES (
  'ws_1', 'run_2', 1,
  '{"id":"run_2","workspace_id":"ws_1","workload_revision_id":"wrev_2","phase":"closed","outcome":"succeeded","cleanup":"confirmed","closed":true}'
);

CREATE TABLE run_projection_metadata (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  schema_version INTEGER NOT NULL
);

INSERT INTO run_projection_metadata (singleton, schema_version) VALUES (1, 1);
