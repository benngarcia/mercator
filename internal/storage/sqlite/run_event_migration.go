package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// migrateLegacyRunEvents renames a placement decision to the booking decision it
// became, and synthesises the booking a decision taken before bookings existed
// would have carried.
//
// Like the service class rename, it rewrites the run events the Run projection
// is reduced from, so it says in the same transaction that the stored projection
// is no longer derived from this log. Marking it separately would not survive a
// process killed in between: the rewrite is idempotent and finds nothing to do
// on the next boot, so a mark that has not landed by then never lands, and the
// projection answers with a Run whose decision has no booking while
// RequiresRebuild calls itself current for the life of the database.
func migrateLegacyRunEvents(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite storage: begin run event migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var openLegacyRuns bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM events AS legacy
			WHERE (
				legacy.event_type = 'compute.run.placement_decided.v1'
				OR (
					legacy.event_type = 'compute.run.booking_decided.v1'
					AND COALESCE(json_extract(legacy.data_json, '$.decision.selected_offer_snapshot_id'), '') != ''
					AND json_type(legacy.data_json, '$.decision.booking') IS NULL
				)
			)
			  AND NOT EXISTS (
				SELECT 1
				FROM events AS closed
				WHERE closed.workspace_id = legacy.workspace_id
				  AND closed.stream_type = legacy.stream_type
				  AND closed.stream_id = legacy.stream_id
				  AND closed.event_type = 'compute.run.closed.v1'
			  )
		)
	`).Scan(&openLegacyRuns); err != nil {
		return fmt.Errorf("sqlite storage: inspect legacy run events: %w", err)
	}
	if openLegacyRuns {
		return fmt.Errorf("sqlite storage: cannot migrate open legacy placement decisions")
	}

	rewritten, err := tx.ExecContext(ctx, `
		UPDATE events
		SET event_type = 'compute.run.booking_decided.v1',
		    data_json = CASE
		      WHEN COALESCE(json_extract(data_json, '$.decision.selected_offer_snapshot_id'), '') = ''
		        OR json_type(data_json, '$.decision.booking') IS NOT NULL
		        THEN data_json
		      ELSE json_set(
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
		    END
		WHERE event_type = 'compute.run.placement_decided.v1'
		   OR (
		     event_type = 'compute.run.booking_decided.v1'
		     AND COALESCE(json_extract(data_json, '$.decision.selected_offer_snapshot_id'), '') != ''
		     AND json_type(data_json, '$.decision.booking') IS NULL
		   )
	`)
	if err != nil {
		return fmt.Errorf("sqlite storage: migrate run event names: %w", err)
	}
	renamed, err := rewritten.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite storage: count migrated run events: %w", err)
	}
	if renamed > 0 {
		if err := markRunProjectionStale(ctx, tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite storage: commit run event migration: %w", err)
	}
	return nil
}
