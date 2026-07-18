package credential

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type SQLiteStore struct{ db *sql.DB }

func NewSQLiteStore(ctx context.Context, db *sql.DB) (*SQLiteStore, error) {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS connection_secret (
		workspace_id  TEXT NOT NULL,
		connection_id TEXT NOT NULL,
		blob          BLOB NOT NULL,
		PRIMARY KEY (workspace_id, connection_id)
	)`)
	if err != nil {
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Put(ctx context.Context, ws, id string, blob []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO connection_secret (workspace_id, connection_id, blob) VALUES (?, ?, ?)
		 ON CONFLICT(workspace_id, connection_id) DO UPDATE SET blob = excluded.blob`,
		ws, id, blob)
	return err
}

func (s *SQLiteStore) Get(ctx context.Context, ws, id string) ([]byte, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT blob FROM connection_secret WHERE workspace_id = ? AND connection_id = ?`, ws, id).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return blob, err
}

// MigrateSealKey re-seals every stored blob under the key derived from
// masterKey. Rows already sealed under the derived key are left alone; rows
// sealed under the raw master key (the pre-HKDF format) are re-sealed. A row
// neither key can open means the configured MERCATOR_SECRET_KEY is not the key
// the store was written with — that is a startup-fatal condition for the
// caller, reported per row so the operator sees exactly which connections are
// affected. Returns how many rows were re-sealed.
func (s *SQLiteStore) MigrateSealKey(ctx context.Context, masterKey []byte) (int, error) {
	if len(masterKey) == 0 {
		return 0, nil
	}
	sealKey := DeriveSealKey(masterKey)
	rows, err := s.db.QueryContext(ctx,
		`SELECT workspace_id, connection_id, blob FROM connection_secret`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type reseal struct {
		ws, id string
		blob   []byte
	}
	var pending []reseal
	var undecryptable []error
	for rows.Next() {
		var r reseal
		if err := rows.Scan(&r.ws, &r.id, &r.blob); err != nil {
			return 0, err
		}
		if _, err := Open(sealKey, r.blob); err == nil {
			continue
		}
		plain, err := Open(masterKey, r.blob)
		if err != nil {
			undecryptable = append(undecryptable,
				fmt.Errorf("credential for %s/%s cannot be decrypted with the configured MERCATOR_SECRET_KEY", r.ws, r.id))
			continue
		}
		resealed, err := Seal(sealKey, plain)
		if err != nil {
			return 0, fmt.Errorf("re-seal credential for %s/%s: %w", r.ws, r.id, err)
		}
		pending = append(pending, reseal{ws: r.ws, id: r.id, blob: resealed})
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(undecryptable) > 0 {
		return 0, errors.Join(undecryptable...)
	}
	for _, r := range pending {
		if err := s.Put(ctx, r.ws, r.id, r.blob); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}
