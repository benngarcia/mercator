package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/benngarcia/mercator/internal/eventlog"
)

const createConnectionSecrets = `CREATE TABLE connection_secret (
	connection_id TEXT PRIMARY KEY,
	blob BLOB NOT NULL,
	CHECK (length(connection_id) > 0)
)`

const createGlobalRentalSchedules = `CREATE TABLE rental_schedules (
	rental_id TEXT PRIMARY KEY,
	version INTEGER NOT NULL,
	schedule_json BLOB NOT NULL
)`

func flattenWorkspaceStorage(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite storage: begin workspace removal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := eventlog.FlattenWorkspacePartitions(ctx, tx); err != nil {
		return fmt.Errorf("sqlite storage: remove event workspace partitions: %w", err)
	}
	if err := flattenConnectionSecrets(ctx, tx); err != nil {
		return err
	}
	if err := flattenRentalSchedules(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS events_require_workspace`); err != nil {
		return fmt.Errorf("sqlite storage: remove workspace trigger: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS workspaces`); err != nil {
		return fmt.Errorf("sqlite storage: remove workspace catalog: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite storage: commit workspace removal: %w", err)
	}
	return nil
}

func flattenConnectionSecrets(ctx context.Context, tx *sql.Tx) error {
	partitioned, err := tableHasColumn(ctx, tx, "connection_secret", "workspace_id")
	if err != nil || !partitioned {
		return err
	}
	if err := refuseDuplicateIdentity(ctx, tx, "connection_secret", "connection_id", "connection credential"); err != nil {
		return err
	}
	for _, statement := range []string{
		`ALTER TABLE connection_secret RENAME TO connection_secret_workspace_legacy`,
		createConnectionSecrets,
		`INSERT INTO connection_secret (connection_id, blob)
		 SELECT connection_id, blob FROM connection_secret_workspace_legacy`,
		`DROP TABLE connection_secret_workspace_legacy`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlite storage: remove connection credential workspace partition: %w", err)
		}
	}
	return nil
}

func flattenRentalSchedules(ctx context.Context, tx *sql.Tx) error {
	partitioned, err := tableHasColumn(ctx, tx, "rental_schedules", "workspace_id")
	if err != nil || !partitioned {
		return err
	}
	if err := refuseDuplicateIdentity(ctx, tx, "rental_schedules", "rental_id", "rental schedule"); err != nil {
		return err
	}
	for _, statement := range []string{
		`ALTER TABLE rental_schedules RENAME TO rental_schedules_workspace_legacy`,
		createGlobalRentalSchedules,
		`INSERT INTO rental_schedules (rental_id, version, schedule_json)
		 SELECT rental_id, version, schedule_json FROM rental_schedules_workspace_legacy`,
		`DROP TABLE rental_schedules_workspace_legacy`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlite storage: remove rental schedule workspace partition: %w", err)
		}
	}
	return nil
}

func refuseDuplicateIdentity(ctx context.Context, tx *sql.Tx, table, column, identity string) error {
	var duplicate string
	err := tx.QueryRowContext(ctx, `SELECT `+column+` FROM `+table+`
		GROUP BY `+column+` HAVING COUNT(*) > 1 LIMIT 1`).Scan(&duplicate)
	if err == nil {
		return fmt.Errorf("sqlite storage: cannot remove workspace partition from duplicate %s %q", identity, duplicate)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("sqlite storage: inspect %s identities: %w", identity, err)
	}
	return nil
}

func tableHasColumn(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
