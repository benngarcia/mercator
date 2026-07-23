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
	workspace_id TEXT NOT NULL,
	rental_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	schedule_json BLOB NOT NULL,
	PRIMARY KEY(workspace_id, rental_id)
)`

func migrateRentalSchedules(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createRentalSchedules); err != nil {
		return fmt.Errorf("migrate Rental Schedules: %w", err)
	}
	return nil
}

type RentalScheduleStore struct {
	db  *sql.DB
	log *WorkspaceEventLog
}

func (store *RentalScheduleStore) List(ctx context.Context, workspaceID string) (map[string]domain.RentalSchedule, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT schedule_json
		FROM rental_schedules
		WHERE workspace_id = ?
		ORDER BY rental_id`, workspaceID)
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

func (store *RentalScheduleStore) Commit(ctx context.Context, event eventlog.AppendRequest, expectedVersion uint64, next domain.RentalSchedule) (eventlog.AppendResult, error) {
	if event.Stream.WorkspaceID == "" || next.RentalID == "" {
		return eventlog.AppendResult{}, fmt.Errorf("Rental Schedule commit requires Workspace and Rental identity")
	}
	if next.Version != expectedVersion+1 {
		return eventlog.AppendResult{}, fmt.Errorf("Rental Schedule version %d does not follow %d", next.Version, expectedVersion)
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return eventlog.AppendResult{}, fmt.Errorf("encode Rental Schedule: %w", err)
	}
	return store.log.appendIfWorkspaceActiveAtomic(ctx, event, func(ctx context.Context, tx *sql.Tx) error {
		currentVersion, err := rentalScheduleVersion(ctx, tx, event.Stream.WorkspaceID, next.RentalID)
		if err != nil {
			return err
		}
		if currentVersion != expectedVersion {
			return eventlog.ErrConcurrencyConflict
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO rental_schedules (workspace_id, rental_id, version, schedule_json)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(workspace_id, rental_id) DO UPDATE SET
				version = excluded.version,
				schedule_json = excluded.schedule_json`,
			event.Stream.WorkspaceID, next.RentalID, next.Version, encoded)
		if err != nil {
			return fmt.Errorf("store Rental Schedule: %w", err)
		}
		return nil
	})
}

func rentalScheduleVersion(ctx context.Context, tx *sql.Tx, workspaceID, rentalID string) (uint64, error) {
	var version uint64
	err := tx.QueryRowContext(ctx, `
		SELECT version
		FROM rental_schedules
		WHERE workspace_id = ? AND rental_id = ?`, workspaceID, rentalID).Scan(&version)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load Rental Schedule version: %w", err)
	}
	return version, nil
}
