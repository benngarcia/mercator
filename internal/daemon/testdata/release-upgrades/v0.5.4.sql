BEGIN TRANSACTION;
UPDATE events
SET data_json = json_set(
  data_json,
  '$.decision.booking',
  json_object(
    'id', 'booking_legacy_' || json_extract(data_json, '$.decision.id'),
    'run_id', json_extract(data_json, '$.decision.run_id'),
    'rental_id', 'rental_legacy_' || json_extract(data_json, '$.decision.selected_offer_snapshot_id'),
    'state', 'running',
    'schedule_version', 1
  )
)
WHERE event_type = 'compute.run.booking_decided.v1'
  AND COALESCE(json_extract(data_json, '$.decision.selected_offer_snapshot_id'), '') != ''
  AND json_type(data_json, '$.decision.booking') IS NULL;
COMMIT;
