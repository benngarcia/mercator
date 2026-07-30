-- A database from before a workspace was a tenancy boundary: workspaces exist,
-- each records who created it, and membership is not a thing the schema knows
-- about. One of them was itself backfilled from event history, so its creator
-- is the migration principal rather than a person.
CREATE TABLE workspaces (
  workspace_id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL CHECK (length(trim(display_name)) > 0),
  created_at TEXT NOT NULL,
  created_by TEXT NOT NULL CHECK (length(trim(created_by)) > 0),
  archived_at TEXT
);

INSERT INTO workspaces (workspace_id, display_name, created_at, created_by, archived_at) VALUES
  ('ws_research', 'Research', '2026-06-01T09:00:00Z', 'ana@example.com', NULL),
  ('ws_platform', 'Platform', '2026-06-02T10:30:00Z', 'brij@example.com', NULL),
  ('ws_retired', 'Retired', '2026-06-03T11:45:00Z', 'ana@example.com', '2026-06-30T12:00:00Z'),
  ('staging', 'staging', '2026-06-04T08:15:00Z', 'system:migration', NULL);
