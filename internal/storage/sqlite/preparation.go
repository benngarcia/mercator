package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// The rate at which Mercator may begin preparing capacity is the one part of
// speculation that cannot be recomputed. What is wanted is derived from the Runs
// and the machines every time, so a restarted control plane rebuilds it; when it
// last began a transfer is a moment that happened, and a process that forgot it
// would be free to begin another immediately. A Mercator restarting in a loop
// would then start a fetch on every boot, which is the bound not existing.
const createPreparationClock = `CREATE TABLE IF NOT EXISTS preparation_clock (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	began_unix_nano INTEGER NOT NULL
)`

func migratePreparationClock(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createPreparationClock); err != nil {
		return fmt.Errorf("migrate preparation clock: %w", err)
	}
	return nil
}

// PreparationClock is when this control plane last began preparing content for
// work it has not admitted. There is one row because the bound it serves is the
// fleet's: what it protects is a machine's link and this process's own egress,
// and every Run shares both.
//
// It records a decision Mercator made rather than anything a machine holds,
// which is what makes it durable state Mercator is allowed to keep. The desired
// sets stay in process for the opposite reason: they are a cache's contents seen
// from here, they are derived from the event log, and restating one is answered
// Duplicate.
type PreparationClock struct {
	db *sql.DB
}

// LastBegan is the moment preparation last started, and whether it ever has.
func (clock *PreparationClock) LastBegan(ctx context.Context) (time.Time, bool, error) {
	var nanos int64
	err := clock.db.QueryRowContext(ctx, `SELECT began_unix_nano FROM preparation_clock WHERE id = 1`).Scan(&nanos)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read preparation clock: %w", err)
	}
	return time.Unix(0, nanos).UTC(), true, nil
}

// RecordBegan stamps the moment preparation started. The latest moment wins
// however two writers reach it, because the bound is about how recently anything
// began and a stale write must never make a fresh transfer look old.
func (clock *PreparationClock) RecordBegan(ctx context.Context, at time.Time) error {
	_, err := clock.db.ExecContext(ctx, `
		INSERT INTO preparation_clock (id, began_unix_nano)
		VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET began_unix_nano =
			MAX(preparation_clock.began_unix_nano, excluded.began_unix_nano)`,
		at.UnixNano())
	if err != nil {
		return fmt.Errorf("record preparation clock: %w", err)
	}
	return nil
}
