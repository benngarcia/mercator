package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/benngarcia/mercator/internal/domain"
	"github.com/benngarcia/mercator/internal/eventlog"
	"github.com/benngarcia/mercator/internal/rental"
)

// The lease is stored whole rather than as a row per generation. What a Rental
// has been through is read back as one answer every time, and a shape that could
// return a lease with some of its generations missing would let a caller act on a
// machine it thought was the current one.
//
// The version is a column of its own so a write can be conditional on it in SQL,
// which is what makes two controllers ending one generation a conflict rather
// than a last-writer-wins.
const createRentals = `CREATE TABLE IF NOT EXISTS rentals (
	workspace_id TEXT NOT NULL,
	rental_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	rental_json BLOB NOT NULL,
	PRIMARY KEY(workspace_id, rental_id)
)`

func migrateRentals(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createRentals); err != nil {
		return fmt.Errorf("migrate Rentals: %w", err)
	}
	return nil
}

// RentalStore is the durable record of the capacity Mercator holds. A control
// plane that forgot a lease across a restart would leave the machine behind it
// billing with nothing able to name it, which is why every generation is written
// here before anything acts on it.
type RentalStore struct{ db *sql.DB }

func (store *RentalStore) Save(ctx context.Context, expectedVersion uint64, next domain.Rental) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if next.Version <= expectedVersion {
		return fmt.Errorf("Rental %q at version %d does not follow version %d", next.ID, next.Version, expectedVersion)
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encode Rental %q: %w", next.ID, err)
	}
	if expectedVersion == 0 {
		return store.take(ctx, next, encoded)
	}
	return store.advance(ctx, expectedVersion, next, encoded)
}

// take claims a lease identity. Nothing was there to replace, so an identity
// already in the table is a second controller having taken the same lease and
// the write is refused rather than overwriting what it holds.
func (store *RentalStore) take(ctx context.Context, next domain.Rental, encoded []byte) error {
	result, err := store.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO rentals (workspace_id, rental_id, version, rental_json)
		VALUES (?, ?, ?, ?)`,
		next.WorkspaceID, next.ID, next.Version, encoded)
	if err != nil {
		return fmt.Errorf("take Rental %q: %w", next.ID, err)
	}
	return wroteOne(result, next.ID)
}

// advance replaces the lease at the version the caller read. A lease at any other
// version, or none at all, is one something else moved first.
func (store *RentalStore) advance(ctx context.Context, expectedVersion uint64, next domain.Rental, encoded []byte) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE rentals SET version = ?, rental_json = ?
		WHERE workspace_id = ? AND rental_id = ? AND version = ?`,
		next.Version, encoded, next.WorkspaceID, next.ID, expectedVersion)
	if err != nil {
		return fmt.Errorf("store Rental %q: %w", next.ID, err)
	}
	return wroteOne(result, next.ID)
}

func wroteOne(result sql.Result, rentalID string) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("%w: Rental %s", eventlog.ErrConcurrencyConflict, rentalID)
	}
	return nil
}

func (store *RentalStore) Get(ctx context.Context, workspaceID, rentalID string) (domain.Rental, error) {
	var encoded []byte
	err := store.db.QueryRowContext(ctx,
		`SELECT rental_json FROM rentals WHERE workspace_id = ? AND rental_id = ?`,
		workspaceID, rentalID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Rental{}, fmt.Errorf("%w: %s", rental.ErrNotFound, rentalID)
	}
	if err != nil {
		return domain.Rental{}, fmt.Errorf("read Rental %q: %w", rentalID, err)
	}
	return decodeRental(encoded)
}

func (store *RentalStore) List(ctx context.Context, workspaceID string) ([]domain.Rental, error) {
	rows, err := store.db.QueryContext(ctx,
		`SELECT rental_json FROM rentals WHERE workspace_id = ? ORDER BY rental_id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list Rentals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var held []domain.Rental
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scan Rental: %w", err)
		}
		lease, err := decodeRental(encoded)
		if err != nil {
			return nil, err
		}
		held = append(held, lease)
	}
	return held, rows.Err()
}

func decodeRental(encoded []byte) (domain.Rental, error) {
	var lease domain.Rental
	if err := json.Unmarshal(encoded, &lease); err != nil {
		return domain.Rental{}, fmt.Errorf("decode Rental: %w", err)
	}
	return lease, nil
}

var _ rental.Store = (*RentalStore)(nil)
