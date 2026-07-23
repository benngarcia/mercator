package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

func migrateRunEventNames(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		UPDATE events
		SET event_type = 'compute.run.booking_decided.v1'
		WHERE event_type = 'compute.run.placement_decided.v1'
	`); err != nil {
		return fmt.Errorf("sqlite storage: migrate run event names: %w", err)
	}
	return nil
}
