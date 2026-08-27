package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
)

const createRentalSchedules = `CREATE TABLE IF NOT EXISTS rental_schedules (
	rental_id TEXT PRIMARY KEY,
	version INTEGER NOT NULL,
	schedule_json BLOB NOT NULL
)`

func migrateRentalSchedules(ctx context.Context, db *sql.DB) error {
	if err := flattenRentalSchedules(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, createRentalSchedules); err != nil {
		return fmt.Errorf("migrate Rental Schedules: %w", err)
	}
	return nil
}

func flattenRentalSchedules(ctx context.Context, db *sql.DB) error {
	partitioned, err := tableHasColumn(ctx, db, "rental_schedules", "workspace_id")
	if err != nil || !partitioned {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var collision string
	err = tx.QueryRowContext(ctx, `SELECT rental_id FROM rental_schedules GROUP BY rental_id HAVING COUNT(*) > 1 LIMIT 1`).Scan(&collision)
	if err == nil {
		return fmt.Errorf("migrate Rental Schedules: duplicate rental %q across workspaces", collision)
	}
	if err != sql.ErrNoRows {
		return err
	}
	for _, statement := range []string{
		`ALTER TABLE rental_schedules RENAME TO rental_schedules_workspace_legacy`, createRentalSchedules,
		`INSERT INTO rental_schedules (rental_id, version, schedule_json) SELECT rental_id, version, schedule_json FROM rental_schedules_workspace_legacy`,
		`DROP TABLE rental_schedules_workspace_legacy`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate Rental Schedules: flatten workspaces: %w", err)
		}
	}
	return tx.Commit()
}

type RentalScheduleStore struct {
	db   *sql.DB
	log  *eventlog.SQLiteEventLog
	runs *RunStore
}

func (store *RentalScheduleStore) List(ctx context.Context) (map[string]domain.RentalSchedule, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT schedule_json
		FROM rental_schedules
		ORDER BY rental_id`)
	if err != nil {
		return nil, fmt.Errorf("list Rental Schedules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	schedules := map[string]domain.RentalSchedule{}
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scan Rental Schedule: %w", err)
		}
		var schedule domain.RentalSchedule
		if err := json.Unmarshal(encoded, &schedule); err != nil {
			return nil, fmt.Errorf("decode Rental Schedule: %w", err)
		}
		schedules[schedule.RentalID] = schedule
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Rental Schedules: %w", err)
	}
	return schedules, nil
}

func (store *RentalScheduleStore) Commit(
	ctx context.Context,
	event eventlog.AppendRequest,
	expectedVersion uint64,
	next domain.RentalSchedule,
	run domain.RunRecord,
) (eventlog.AppendResult, error) {
	if next.RentalID == "" {
		return eventlog.AppendResult{}, fmt.Errorf("Rental Schedule commit requires Rental identity")
	}
	if next.Version != expectedVersion+1 {
		return eventlog.AppendResult{}, fmt.Errorf("Rental Schedule version %d does not follow %d", next.Version, expectedVersion)
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return eventlog.AppendResult{}, fmt.Errorf("encode Rental Schedule: %w", err)
	}
	return store.log.AppendAtomic(ctx, event, func(ctx context.Context, tx *sql.Tx) error {
		currentVersion, err := rentalScheduleVersion(ctx, tx, next.RentalID)
		if err != nil {
			return err
		}
		if currentVersion != expectedVersion {
			return eventlog.ErrConcurrencyConflict
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO rental_schedules (rental_id, version, schedule_json)
			VALUES (?, ?, ?)
			ON CONFLICT(rental_id) DO UPDATE SET
				version = excluded.version,
				schedule_json = excluded.schedule_json`,
			next.RentalID, next.Version, encoded)
		if err != nil {
			return fmt.Errorf("store Rental Schedule: %w", err)
		}
		return store.runs.putTx(ctx, tx, run)
	})
}

func rentalScheduleVersion(ctx context.Context, tx *sql.Tx, rentalID string) (uint64, error) {
	var version uint64
	err := tx.QueryRowContext(ctx, `
		SELECT version
		FROM rental_schedules
		WHERE rental_id = ?`, rentalID).Scan(&version)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load Rental Schedule version: %w", err)
	}
	return version, nil
}
