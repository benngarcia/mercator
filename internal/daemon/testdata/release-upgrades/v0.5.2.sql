BEGIN TRANSACTION;
UPDATE events
SET event_type = 'compute.run.booking_decided.v1'
WHERE event_type = 'compute.run.placement_decided.v1';
CREATE TABLE rental_schedules (
  workspace_id TEXT NOT NULL,
  rental_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  schedule_json BLOB NOT NULL,
  PRIMARY KEY(workspace_id, rental_id)
);
COMMIT;
